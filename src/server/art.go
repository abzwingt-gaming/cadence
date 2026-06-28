// art.go — album art extraction with in-memory cache.

package main

import (
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/dhowden/tag"
)

type artEntry struct {
	data []byte
	mime string
}

var (
	artMu    sync.RWMutex
	artCache = map[string]*artEntry{}
)

func inMemoryArtCacheClear() {
	artMu.Lock()
	artCache = map[string]*artEntry{}
	artMu.Unlock()
}

// albumArtForTrack looks up the current track in the DB, reads embedded
// art from the audio file, and falls back to cover.jpg/png in the same dir.
func albumArtForTrack(title, artist string) ([]byte, string, error) {
	key := title + "\x00" + artist

	artMu.RLock()
	if e, ok := artCache[key]; ok {
		artMu.RUnlock()
		return e.data, e.mime, nil
	}
	artMu.RUnlock()

	results, err := searchByTitleArtist(title, artist)
	if err != nil || len(results) == 0 {
		return nil, "", fmt.Errorf("track not found")
	}
	path, err := getPathById(results[0].ID)
	if err != nil {
		return nil, "", err
	}

	data, mime, err := readEmbeddedArt(path)
	if err != nil {
		data, mime, err = readFileArt(ArtworkPath(path))
		if err != nil {
			return nil, "", fmt.Errorf("no art found for %q", path)
		}
	}

	artMu.Lock()
	artCache[key] = &artEntry{data: data, mime: mime}
	artMu.Unlock()

	return data, mime, nil
}

func readEmbeddedArt(path string) ([]byte, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	tags, err := tag.ReadFrom(f)
	if err != nil {
		return nil, "", err
	}
	pic := tags.Picture()
	if pic == nil || len(pic.Data) == 0 {
		return nil, "", fmt.Errorf("no embedded picture")
	}
	mime := pic.MIMEType
	if mime == "" {
		mime = "image/jpeg"
	}
	return pic.Data, mime, nil
}

func readFileArt(path string) ([]byte, string, error) {
	if path == "" {
		return nil, "", fmt.Errorf("no cover file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Debug("Cover file read error.", "path", path, "error", err)
		return nil, "", err
	}
	mime := "image/jpeg"
	if len(path) > 4 && path[len(path)-4:] == ".png" {
		mime = "image/png"
	}
	return data, mime, nil
}
