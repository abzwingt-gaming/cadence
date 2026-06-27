// api.go
// HTTP handler functions.

package main

import (
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
		slog.Debug(fmt.Sprintf("Search from %s.", r.RemoteAddr))
		type body struct {
			Query string `json:"search"`
		}
		var b body
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		results, err := searchByQuery(b.Query)
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
		slog.Info(fmt.Sprintf("Song request from %s.", r.RemoteAddr))
		type body struct {
			ID string `json:"ID"`
		}
		var b body
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		id, err := strconv.Atoi(b.ID)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		path, err := getPathById(id)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
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
		type body struct {
			Query string `json:"Search"`
		}
		var b body
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		results, err := searchByQuery(b.Query)
		if err != nil || len(results) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		path, err := getPathById(results[0].ID)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
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
		results, err := searchByTitleArtist(now.Song.Title, now.Song.Artist)
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
		results, err := searchByTitleArtist(now.Song.Title, now.Song.Artist)
		if err != nil || len(results) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		path, err := getPathById(results[0].ID)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		file, err := os.Open(path)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer file.Close()
		tags, err := tag.ReadFrom(file)
		if err != nil || tags.Picture() == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct{ Picture []byte }{tags.Picture().Data})
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct{ ListenURL string }{publicStreamURL()})
	}
}

func Listeners() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct{ Listeners int }{int(now.Listeners)})
	}
}

func Bitrate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct{ Bitrate int }{int(now.Bitrate)})
	}
}

func Version() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct{ Version string }{c.Version})
	}
}

func Ready() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
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
