// api.go
// API functions.

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

// POST /api/search
func Search() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slog.Debug(fmt.Sprintf("Search request from client %s.", r.RemoteAddr), "func", "Search")
		type Search struct {
			Query string `json:"search"`
		}
		var search Search
		decoder := json.NewDecoder(r.Body)
		err := decoder.Decode(&search)
		if err != nil {
			slog.Error("Unable to decode search body.", "func", "Search", "error", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		queryResults, err := searchByQuery(search.Query)
		if err != nil {
			slog.Error("Unable to execute search by query.", "func", "Search", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		jsonMarshal, err := json.Marshal(queryResults)
		if err != nil {
			slog.Error("Failed to marshal results from the search.", "func", "Search", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonMarshal)
	}
}

// POST /api/request/id
func RequestID() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slog.Info(fmt.Sprintf("Request-by-ID by client %s.", r.RemoteAddr), "func", "Request")
		type Request struct {
			ID string `json:"ID"`
		}
		var request Request
		decoder := json.NewDecoder(r.Body)
		err := decoder.Decode(&request)
		if err != nil {
			slog.Error("Unable to decode request.", "func", "RequestID", "error", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reqID, err := strconv.Atoi(request.ID)
		if err != nil {
			slog.Error("Unable to convert request ID to integer.", "func", "RequestID", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		path, err := getPathById(reqID)
		if err != nil {
			slog.Error("Unable to find file path by song ID.", "func", "RequestID", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, err = liquidsoapRequest(path)
		if err != nil {
			slog.Error("Unable to submit song request.", "func", "RequestID", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

// POST /api/request/bestmatch
func RequestBestMatch() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		type RequestBestMatch struct {
			Query string `json:"Search"`
		}
		var rbm RequestBestMatch
		decoder := json.NewDecoder(r.Body)
		err := decoder.Decode(&rbm)
		if err != nil {
			slog.Error("Unable to decode request body.", "func", "RequestBestMatch", "error", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		queryResults, err := searchByQuery(rbm.Query)
		if err != nil {
			slog.Error("Unable to search by query.", "func", "RequestBestMatch", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if len(queryResults) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		path, err := getPathById(queryResults[0].ID)
		if err != nil {
			slog.Error("Unable to find file path by song ID.", "func", "RequestBestMatch", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, err = liquidsoapRequest(path)
		if err != nil {
			slog.Error("Unable to submit song request.", "func", "RequestBestMatch", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

// GET /api/nowplaying/metadata
func NowPlayingMetadata() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		queryResults, err := searchByTitleArtist(now.Song.Title, now.Song.Artist)
		if err != nil {
			slog.Error("Unable to search by title and artist.", "func", "NowPlayingMetadata", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if len(queryResults) < 1 {
			slog.Warn("Currently playing song not found in database.", "func", "NowPlayingMetadata")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		jsonMarshal, err := json.Marshal(queryResults[0])
		if err != nil {
			slog.Error("Failed to marshal results.", "func", "NowPlayingMetadata", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonMarshal)
	}
}

// GET /api/nowplaying/albumart
func NowPlayingAlbumArt() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		queryResults, err := searchByTitleArtist(now.Song.Title, now.Song.Artist)
		if err != nil {
			slog.Error("Unable to search by title and artist.", "func", "NowPlayingAlbumArt", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if len(queryResults) < 1 {
			slog.Warn("Currently playing song not found in database.", "func", "NowPlayingAlbumArt")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		path, err := getPathById(queryResults[0].ID)
		if err != nil {
			slog.Error("Unable to find file path by song ID.", "func", "NowPlayingAlbumArt", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		file, err := os.Open(path)
		if err != nil {
			slog.Error("Unable to open file for album art extraction.", "func", "NowPlayingAlbumArt", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		tags, err := tag.ReadFrom(file)
		if err != nil {
			slog.Warn("Unable to read tags on file for art extraction.", "func", "NowPlayingAlbumArt", "error", err)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if tags.Picture() == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		type SongData struct {
			Picture []byte
		}
		result := SongData{Picture: tags.Picture().Data}
		jsonMarshal, err := json.Marshal(result)
		if err != nil {
			slog.Error("Failed to marshal art data.", "func", "NowPlayingAlbumArt", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonMarshal)
	}
}

// GET /api/history
func History() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jsonMarshal, err := json.Marshal(history)
		if err != nil {
			slog.Error("Failed to marshal play history.", "func", "History", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonMarshal)
	}
}

// GET /api/streamurl
// Returns the public-facing stream URL (CSERVER_STREAMURL).
// This is what the browser audio element should point to.
// Separate from /api/listenurl which reflects internal Icecast host.
func StreamURL() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		type StreamURL struct {
			StreamURL string
		}
		url := c.StreamURL
		if url == "" {
			// Fallback: build from internal Icecast data (original behavior)
			if now.Host != "" && now.Host != "-" {
				url = "http://" + now.Host + now.Mountpoint
			}
		}
		jsonMarshal, err := json.Marshal(StreamURL{StreamURL: url})
		if err != nil {
			slog.Error("Failed to marshal stream URL.", "func", "StreamURL", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonMarshal)
	}
}

// GET /api/listenurl
func ListenURL() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		type ListenURL struct {
			ListenURL string
		}
		listenurl := ListenURL{ListenURL: now.Host + "/" + now.Mountpoint}
		jsonMarshal, err := json.Marshal(listenurl)
		if err != nil {
			slog.Error("Failed to marshal listen URL.", "func", "ListenURL", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonMarshal)
	}
}

// GET /api/listeners
func Listeners() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		type Listeners struct {
			Listeners int
		}
		jsonMarshal, err := json.Marshal(Listeners{Listeners: int(now.Listeners)})
		if err != nil {
			slog.Error("Failed to marshal listeners.", "func", "Listeners", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonMarshal)
	}
}

// GET /api/bitrate
func Bitrate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		type Bitrate struct {
			Bitrate int
		}
		jsonMarshal, err := json.Marshal(Bitrate{Bitrate: int(now.Bitrate)})
		if err != nil {
			slog.Error("Failed to marshal bitrate.", "func", "Bitrate", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonMarshal)
	}
}

// GET /api/version
func Version() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		type Version struct {
			Version string
		}
		jsonMarshal, err := json.Marshal(Version{Version: c.Version})
		if err != nil {
			slog.Error("Failed to marshal version.", "func", "Version", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(jsonMarshal)
	}
}

// GET /ready
func Ready() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
}

// GET /api/dev/skip
func DevSkip() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, err := liquidsoapSkip()
		if err != nil {
			slog.Error("Unable to skip the playing song.", "func", "DevSkip", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}
