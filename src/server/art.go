// art.go — album art extraction with in-memory cache.
// singleflight prevents duplicate concurrent reads for the same track.

package main

import (
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/dhowden/tag"
	"golang.org/x/sync/singleflight"
)

type artEntry struct {
	data []byte
	mime string
}

var (
	artMu    sync.RWMutex
	artCache = map[string]*artEntry{}
	artSF    singleflight.Group
)

func inMemoryArtCacheClear() {
	artMu.Lock()
	artCache = map[string]*artEntry{}
	artMu.Unlock()
	slog.Debug("In-memory art cache cleared.")
}

// albumArtForTrack returns embedded or directory cover art for the currently
// playing track. Concurrent callers for the same key coalesce via singleflight.
func albumArtForTrack(title, artist string) ([]byte, string, error) {
	key := title + "\x00" + artist

	artMu.RLock()
	e, hit := artCache[key]
	artMu.RUnlock()
	if hit {
		return e.data, e.mime, nil
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
		artCache[key] = &artEntry{data: data, mime: mime}
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
	mime := "image/jpeg"
	if len(path) >= 4 && path[len(path)-4:] == ".png" {
		mime = "image/png"
	}
	return data, mime, nil
}
