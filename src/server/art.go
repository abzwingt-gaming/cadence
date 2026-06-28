// art.go — album art extraction with in-memory cache.
// singleflight prevents duplicate concurrent reads for the same track.
// TTL-based expiry prevents stale art after file-on-disk changes.

package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/dhowden/tag"
	"golang.org/x/sync/singleflight"
)

type artEntry struct {
	data     []byte
	mime     string
	cachedAt time.Time // FIX: added for TTL-based eviction
}

var (
	artMu    sync.RWMutex
	artCache = map[string]*artEntry{}
	artSF    singleflight.Group
)

// artCacheTTL returns how long an art entry is valid.
// Configurable via CSERVER_ART_CACHE_TTL_S (default 3600s).
func artCacheTTL() time.Duration {
	ttl := envInt("CSERVER_ART_CACHE_TTL_S", 3600)
	return time.Duration(ttl) * time.Second
}

func inMemoryArtCacheClear() {
	artMu.Lock()
	artCache = map[string]*artEntry{}
	artMu.Unlock()
	slog.Debug("In-memory art cache cleared.")
}

// albumArtForTrack returns embedded or directory cover art for the currently
// playing track. Concurrent callers for the same key coalesce via singleflight.
// Stale entries (older than artCacheTTL) are evicted on access.
func albumArtForTrack(title, artist string) ([]byte, string, error) {
	key := title + "\x00" + artist
	ttl := artCacheTTL()

	artMu.RLock()
	e, hit := artCache[key]
	artMu.RUnlock()
	if hit && time.Since(e.cachedAt) < ttl {
		return e.data, e.mime, nil
	}
	// Evict stale entry so singleflight re-fetches.
	if hit {
		artMu.Lock()
		delete(artCache, key)
		artMu.Unlock()
	}

	type result struct {
		data []byte
		mime string
	}
	v, err, _ := artSF.Do(key, func() (interface{}, error) {
		results, err := searchByTitleArtist(title, artist)
		if err != nil {
			return nil, fmt.Errorf("art DB lookup failed: %w", err)
		}
		if len(results) == 0 {
			return nil, fmt.Errorf("no track found for art: title=%q artist=%q", title, artist)
		}

		path, err := getPathById(results[0].ID)
		if err != nil {
			return nil, fmt.Errorf("art path lookup failed id=%d: %w", results[0].ID, err)
		}

		data, mime, err := readEmbeddedArt(path)
		if err != nil {
			data, mime, err = readFileArt(ArtworkPath(path))
			if err != nil {
				return nil, fmt.Errorf("no art found for %q", path)
			}
			slog.Debug("Art: using directory cover.", "path", path)
		} else {
			slog.Debug("Art: using embedded tag.", "path", path)
		}

		artMu.Lock()
		artCache[key] = &artEntry{data: data, mime: mime, cachedAt: time.Now()}
		artMu.Unlock()

		return result{data: data, mime: mime}, nil
	})
	if err != nil {
		return nil, "", err
	}
	r := v.(result)
	return r.data, r.mime, nil
}

func readEmbeddedArt(path string) ([]byte, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open %q: %w", path, err)
	}
	defer f.Close()

	tags, err := tag.ReadFrom(f)
	if err != nil {
		return nil, "", fmt.Errorf("tag read %q: %w", path, err)
	}
	pic := tags.Picture()
	if pic == nil || len(pic.Data) == 0 {
		return nil, "", fmt.Errorf("no embedded picture in %q", path)
	}
	mime := pic.MIMEType
	if mime == "" {
		mime = "image/jpeg"
	}
	return pic.Data, mime, nil
}

func readFileArt(path string) ([]byte, string, error) {
	if path == "" {
		return nil, "", fmt.Errorf("no cover file path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read cover %q: %w", path, err)
	}
	// FIX: was naive suffix check — misses .PNG, .webp, .gif, etc.
	// Use stdlib content sniffing on the first 512 bytes instead.
	mime := http.DetectContentType(data)
	// DetectContentType may return "application/octet-stream" for some image
	// formats it doesn't recognise; fall back to image/jpeg which is safest.
	if mime == "application/octet-stream" {
		mime = "image/jpeg"
	}
	return data, mime, nil
}
