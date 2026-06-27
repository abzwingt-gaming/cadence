// api.go - HTTP handlers.

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/dhowden/tag"
)

// rescanRunning prevents overlapping rescans triggered by /api/admin/rescan,
// SIGHUP, or the filesystem watcher.
var rescanRunning atomic.Bool

// streamIdle reports whether the icecast monitor has set sentinel "idle" values.
func streamIdle(title, artist string) bool {
	return title == "" || title == "-" || artist == "-"
}

// buildPublicStream constructs the public stream URL from host+mountpoint,
// trimming slashes to prevent double-slash when host ends with "/" or
// mountpoint starts with "/".
func buildPublicStream(host, mountpoint string) string {
	if c.PublicStreamURL != "" {
		return c.PublicStreamURL
	}
	return strings.TrimRight(host, "/") + "/" + strings.TrimLeft(mountpoint, "/")
}

func Search() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"search"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		results, err := searchByQuery(req.Query)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Always return an array, never JSON null.
		if results == nil {
			results = []SongData{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(results)
	}
}

func RequestID() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct{ ID string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		id, err := strconv.Atoi(req.ID)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		path, err := getPathById(id)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if _, err = liquidsoapRequest(path); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

func RequestBestMatch() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Search string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		results, err := searchByQuery(req.Search)
		if err != nil || len(results) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		path, err := getPathById(results[0].ID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if _, err = liquidsoapRequest(path); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

func NowPlayingMetadata() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := NowSnapshot()
		// Guard: stream is idle or icecast unreachable.
		if streamIdle(n.Song.Title, n.Song.Artist) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		results, err := searchByTitleArtist(n.Song.Title, n.Song.Artist)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if len(results) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(results[0])
	}
}

func NowPlayingAlbumArt() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := NowSnapshot()
		// Guard: stream is idle or icecast unreachable.
		if streamIdle(n.Song.Title, n.Song.Artist) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		results, err := searchByTitleArtist(n.Song.Title, n.Song.Artist)
		if err != nil || len(results) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		song := results[0]
		cacheKey := fmt.Sprintf("%d", song.ID)

		if cached, ok := artCache.Load(cacheKey); ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(cached.([]byte))
			return
		}

		path, err := getPathById(song.ID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// 1. Try embedded tags — scoped in a closure so defer fires immediately.
		var encoded string
		func() {
			f, ferr := os.Open(path)
			if ferr != nil {
				return
			}
			defer f.Close()
			tags, terr := tag.ReadFrom(f)
			if terr == nil && tags.Picture() != nil {
				encoded = base64.StdEncoding.EncodeToString(tags.Picture().Data)
			}
		}()

		// 2. Fallback: cover.jpg / folder.jpg in the same directory.
		if encoded == "" {
			if fallbackPath := ArtworkPath(path); fallbackPath != "" {
				if data, ferr := os.ReadFile(fallbackPath); ferr == nil {
					encoded = base64.StdEncoding.EncodeToString(data)
					slog.Debug("Artwork from fallback file.", "path", fallbackPath)
				}
			}
		}

		if encoded == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		type ArtResponse struct {
			Picture string `json:"Picture"`
		}
		jsonBytes, merr := json.Marshal(ArtResponse{Picture: encoded})
		if merr != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		artCache.Store(cacheKey, jsonBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jsonBytes)
	}
}

func History() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		historyMu.Lock()
		snap := make([]playRecord, len(history))
		copy(snap, history)
		historyMu.Unlock()
		// Always return an array, never JSON null.
		if snap == nil {
			snap = []playRecord{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snap)
	}
}

func ListenURL() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := NowSnapshot()
		// buildPublicStream trims slashes to prevent double-slash when
		// Icecast host ends with "/" or mountpoint starts with "/".
		publicURL := buildPublicStream(n.Host, n.Mountpoint)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct{ ListenURL string }{ListenURL: publicURL})
	}
}

func Listeners() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := NowSnapshot()
		listeners := n.Listeners
		// -1 sentinel means icecast unreachable; report 0 to the client.
		if listeners < 0 {
			listeners = 0
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct{ Listeners int64 }{Listeners: listeners})
	}
}

func Bitrate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := NowSnapshot()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct{ Bitrate float64 }{Bitrate: n.Bitrate})
	}
}

func Version() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct{ Version string }{Version: c.Version})
	}
}

// AdminRescan triggers a DB rescan. Only registered when CSERVER_DEVMODE=true.
// Returns 202 immediately; 409 if a rescan is already in progress.
func AdminRescan() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !rescanRunning.CompareAndSwap(false, true) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte("rescan already running"))
			return
		}
		go func() {
			defer rescanRunning.Store(false)
			slog.Info("Manual rescan triggered via /api/admin/rescan.")
			resetCleanupRe()
			if err := dbPopulate(); err != nil {
				slog.Error("Rescan failed.", "error", err)
			}
		}()
		w.WriteHeader(http.StatusAccepted)
	}
}

func Readyz() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if dbActive == nil || dbActive.Ping() != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func Healthz() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dbOK := dbActive != nil && dbActive.Ping() == nil
		n := NowSnapshot()
		icecastOK := !streamIdle(n.Song.Title, n.Song.Artist)
		status := "ok"
		code := http.StatusOK
		if !dbOK || !icecastOK {
			status = "degraded"
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  status,
			"db":      dbOK,
			"icecast": icecastOK,
			"redis":   redisAvailable.Load(),
			"version": c.Version,
		})
	}
}

func DevSkip() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := liquidsoapSkip(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}
