// api.go - HTTP handlers.

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dhowden/tag"
	"golang.org/x/sync/singleflight"
)

// rescanRunning prevents overlapping rescans triggered by /api/admin/rescan,
// SIGHUP, or the filesystem watcher.
var rescanRunning atomic.Bool

// artFlight deduplicates concurrent in-flight album-art requests for the
// same song ID, preventing multiple goroutines opening and reading the same
// audio file simultaneously.
var artFlight singleflight.Group

// dbPingCache avoids a real DB round-trip on every health-check request.
// Refreshed by background goroutine every 10 s.
var dbPingOK atomic.Bool

func init() {
	go func() {
		for {
			time.Sleep(10 * time.Second)
			if dbActive != nil {
				dbPingOK.Store(dbActive.Ping() == nil)
			}
		}
	}()
}

// maxBodyBytes is the maximum request body size accepted by JSON handlers.
// Guards against accidental or malicious oversized POST bodies.
const maxBodyBytes = 512 * 1024 // 512 KB

// streamIdle reports whether the icecast monitor has set sentinel "idle" values.
func streamIdle(title, artist string) bool {
	return title == "" || title == "-" || artist == "-"
}

// buildPublicStream constructs the public stream URL from host+mountpoint,
// trimming slashes to prevent double-slash when host ends with "/" or
// mountpoint starts with "/". Used by both ListenURL() and icecastMonitor().
func buildPublicStream(host, mountpoint string) string {
	if c.PublicStreamURL != "" {
		return c.PublicStreamURL
	}
	return strings.TrimRight(host, "/") + "/" + strings.TrimLeft(mountpoint, "/")
}

// writeJSON encodes v as JSON into w, setting Content-Type and logging any
// encode error.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("JSON encode error.", "error", err)
	}
}

// decodeJSONBody decodes the request body into dst with a size limit.
// Returns false and writes the appropriate error response on failure.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if err.Error() == "http: request body too large" || isMaxBytesError(err, &maxErr) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
		return false
	}
	return true
}

// isMaxBytesError checks whether err is from http.MaxBytesReader.
func isMaxBytesError(err error, _ **http.MaxBytesError) bool {
	return strings.Contains(err.Error(), "request body too large")
}

func Search() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"search"`
		}
		if !decodeJSONBody(w, r, &req) {
			return
		}
		// Reject empty queries explicitly — searchByQuery returns an error
		// for empty strings, which would otherwise become a 500.
		if strings.TrimSpace(req.Query) == "" {
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
		writeJSON(w, results)
	}
}

func RequestID() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct{ ID string }
		if !decodeJSONBody(w, r, &req) {
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
		if !decodeJSONBody(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.Search) == "" {
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
		writeJSON(w, results[0])
	}
}

func NowPlayingAlbumArt() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := NowSnapshot()
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

		// Fast path: cache hit.
		if cached, ok := artCache.Load(cacheKey); ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(cached.([]byte))
			return
		}

		// Slow path: deduplicate concurrent requests for the same song via
		// singleflight. Only one goroutine opens the file; others wait and
		// share the result.
		v, err, _ := artFlight.Do(cacheKey, func() (interface{}, error) {
			// Double-check cache after acquiring the flight slot.
			if cached, ok := artCache.Load(cacheKey); ok {
				return cached.([]byte), nil
			}

			path, pathErr := getPathById(song.ID)
			if pathErr != nil {
				return nil, pathErr
			}

			var encoded string

			// 1. Embedded tag art.
			func() {
				f, ferr := os.Open(path)
				if ferr != nil {
					return
				}
				defer f.Close()
				// Limit tag read to 64 MB to avoid unbounded memory on corrupt files.
				tags, terr := tag.ReadFrom(io.LimitReader(f, 64*1024*1024))
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
				return nil, nil // signals 204 No Content
			}

			type ArtResponse struct {
				Picture string `json:"Picture"`
			}
			jsonBytes, merr := json.Marshal(ArtResponse{Picture: encoded})
			if merr != nil {
				return nil, merr
			}
			artCache.Store(cacheKey, jsonBytes)
			return jsonBytes, nil
		})

		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if v == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(v.([]byte))
	}
}

func History() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		historyMu.Lock()
		snap := make([]playRecord, len(history))
		copy(snap, history)
		historyMu.Unlock()
		if snap == nil {
			snap = []playRecord{}
		}
		writeJSON(w, snap)
	}
}

func ListenURL() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := NowSnapshot()
		publicURL := buildPublicStream(n.Host, n.Mountpoint)
		writeJSON(w, struct{ ListenURL string }{ListenURL: publicURL})
	}
}

func Listeners() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := NowSnapshot()
		listeners := n.Listeners
		if listeners < 0 {
			listeners = 0
		}
		writeJSON(w, struct{ Listeners int64 }{Listeners: listeners})
	}
}

func Bitrate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := NowSnapshot()
		writeJSON(w, struct{ Bitrate float64 }{Bitrate: n.Bitrate})
	}
}

func Version() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, struct{ Version string }{Version: c.Version})
	}
}

// Status returns a combined snapshot: title, artist, listeners, bitrate, listenurl.
// Allows the frontend to fetch everything in one round-trip instead of two.
func Status() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := NowSnapshot()
		listeners := n.Listeners
		if listeners < 0 {
			listeners = 0
		}
		writeJSON(w, struct {
			Title     string  `json:"Title"`
			Artist    string  `json:"Artist"`
			Listeners int64   `json:"Listeners"`
			Bitrate   float64 `json:"Bitrate"`
			ListenURL string  `json:"ListenURL"`
		}{
			Title:     n.Song.Title,
			Artist:    n.Song.Artist,
			Listeners: listeners,
			Bitrate:   n.Bitrate,
			ListenURL: buildPublicStream(n.Host, n.Mountpoint),
		})
	}
}

// AdminRescan triggers a DB rescan. Only registered when CSERVER_DEVMODE=true.
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
		// Use the cached ping result to avoid a DB round-trip on every probe.
		if dbActive == nil || !dbPingOK.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func Healthz() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dbOK := dbActive != nil && dbPingOK.Load()
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
		if err := json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  status,
			"db":      dbOK,
			"icecast": icecastOK,
			"redis":   redisAvailable.Load(),
			"version": c.Version,
		}); err != nil {
			slog.Warn("Healthz JSON encode error.", "error", err)
		}
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
