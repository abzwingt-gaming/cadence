// api.go
// HTTP handlers.

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/dhowden/tag"
)

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

// NowPlayingAlbumArt returns base64-encoded album art with in-memory caching.
func NowPlayingAlbumArt() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := NowSnapshot()
		results, err := searchByTitleArtist(n.Song.Title, n.Song.Artist)
		if err != nil || len(results) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		song := results[0]
		cacheKey := fmt.Sprintf("%d", song.ID)

		// Check in-memory cache first
		if cached, ok := artCache.Load(cacheKey); ok {
			w.Header().Set("Content-Type", "application/json")
			w.Write(cached.([]byte))
			return
		}

		path, err := getPathById(song.ID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		file, err := os.Open(path)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		defer file.Close()

		tags, err := tag.ReadFrom(file)
		if err != nil || tags.Picture() == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		type ArtResponse struct {
			Picture string `json:"Picture"`
		}
		encoded := base64.StdEncoding.EncodeToString(tags.Picture().Data)
		res := ArtResponse{Picture: encoded}
		jsonBytes, err := json.Marshal(res)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Store in cache
		artCache.Store(cacheKey, jsonBytes)
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonBytes)
	}
}

func History() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(history)
	}
}

func ListenURL() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := NowSnapshot()
		publicURL := c.PublicStreamURL
		if publicURL == "" {
			publicURL = n.Host + "/" + n.Mountpoint
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct{ ListenURL string }{ListenURL: publicURL})
	}
}

func Listeners() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := NowSnapshot()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct{ Listeners int }{Listeners: int(n.Listeners)})
	}
}

func Bitrate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := NowSnapshot()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct{ Bitrate int }{Bitrate: int(n.Bitrate)})
	}
}

func Version() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct{ Version string }{Version: c.Version})
	}
}

func Ready() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
}

// Healthz returns DB and Icecast reachability status.
func Healthz() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dbOK := dbActive != nil && dbActive.Ping() == nil
		n := NowSnapshot()
		icecastOK := n.Song.Title != "-" && n.Song.Title != ""
		status := "ok"
		code   := http.StatusOK
		if !dbOK || !icecastOK {
			status = "degraded"
			code   = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   status,
			"db":       dbOK,
			"icecast":  icecastOK,
			"redis":    redisAvailable,
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
