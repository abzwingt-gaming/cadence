// api.go — HTTP handlers and routing.

package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
)

// ── realIP ────────────────────────────────────────────────────────────────────

func realIP(r *http.Request) (string, error) {
	conf := c()
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if conf.RealIPHeader == "" {
		return remoteIP, nil
	}

	if conf.TrustedProxy != "" {
		_, trustedNet, err := net.ParseCIDR(conf.TrustedProxy)
		if err != nil {
			if remoteIP != conf.TrustedProxy {
				return remoteIP, nil
			}
		} else if !trustedNet.Contains(net.ParseIP(remoteIP)) {
			return remoteIP, nil
		}
	}

	if val := r.Header.Get(conf.RealIPHeader); val != "" {
		if idx := strings.Index(val, ","); idx != -1 {
			val = strings.TrimSpace(val[:idx])
		}
		return val, nil
	}
	return remoteIP, nil
}

// ── writeJSON ─────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("JSON encode error.", "error", err)
	}
}

// ── Health ────────────────────────────────────────────────────────────────────

var dbReady atomic.Bool

func handleLivez(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": c().Version})
}

func handleReadyz(w http.ResponseWriter, r *http.Request) {
	if !dbReady.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db not ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Now playing ───────────────────────────────────────────────────────────────

func handleNowPlaying(w http.ResponseWriter, r *http.Request) {
	nd := getNowPlaying()
	writeJSON(w, http.StatusOK, map[string]any{
		"Title":      nd.Title,
		"Artist":     nd.Artist,
		"Mountpoint": nd.Mountpoint,
	})
}

func handleListeners(w http.ResponseWriter, r *http.Request) {
	nd := getNowPlaying()
	writeJSON(w, http.StatusOK, map[string]int{"Listeners": nd.Listeners})
}

func handleBitrate(w http.ResponseWriter, r *http.Request) {
	nd := getNowPlaying()
	writeJSON(w, http.StatusOK, map[string]any{
		"Bitrate":   nd.Bitrate,
		"Available": nd.Bitrate > 0,
	})
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	nd := getNowPlaying()
	writeJSON(w, http.StatusOK, map[string]any{
		"Title":     nd.Title,
		"Artist":    nd.Artist,
		"Stream":    nd.Mountpoint,
		"Listeners": nd.Listeners,
		"Bitrate":   nd.Bitrate,
	})
}

// ── History ───────────────────────────────────────────────────────────────────

func handleHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, historyGet())
}

// ── Song request ──────────────────────────────────────────────────────────────

func handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if rateLimitRequest(w, r) {
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		http.Error(w, "invalid request body: 'id' required", http.StatusBadRequest)
		return
	}
	if err := requestSong(body.ID); err != nil {
		slog.Error("Song request to Liquidsoap failed.", "id", body.ID, "error", err)
		http.Error(w, "failed to queue song", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "queued"})
}

// ── Album art ─────────────────────────────────────────────────────────────────

func handleAlbumArt(w http.ResponseWriter, r *http.Request) {
	if rateLimitArt(w, r) {
		return
	}
	nd := getNowPlaying()
	data, mime, err := albumArtForTrack(nd.Title, nd.Artist)
	if err != nil {
		http.Error(w, "art not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	if _, err := w.Write(data); err != nil {
		slog.Debug("Album art write error (client likely disconnected).", "error", err)
	}
}

// ── Search ────────────────────────────────────────────────────────────────────

func handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) < 2 {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	results, err := dbSearch(q)
	if err != nil {
		slog.Warn("Search error.", "q", q, "error", err)
		http.Error(w, "search failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, results)
}

// ── Admin / dev ───────────────────────────────────────────────────────────────

func handleAdminRescan(w http.ResponseWriter, r *http.Request) {
	if sighupReloading.Load() {
		http.Error(w, "scan already in progress", http.StatusConflict)
		return
	}
	sighupReloading.Store(true)
	go func() {
		defer sighupReloading.Store(false)
		if err := dbPopulate(); err != nil {
			slog.Error("Admin rescan failed.", "error", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "scan started"})
}

func handleDevSkip(w http.ResponseWriter, r *http.Request) {
	if !c().DevMode {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := requestSong("skip"); err != nil {
		http.Error(w, "skip failed", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "skipped"})
}

// ── Routes ────────────────────────────────────────────────────────────────────

func routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/livez", handleLivez)
	mux.HandleFunc("/readyz", handleReadyz)

	mux.HandleFunc("/api/radiodata/sse", handleSSE)
	mux.HandleFunc("/api/nowplaying", handleNowPlaying)
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/listeners", handleListeners)
	mux.HandleFunc("/api/bitrate", handleBitrate)
	mux.HandleFunc("/api/history", handleHistory)

	mux.HandleFunc("/api/request/", handleRequest)
	mux.HandleFunc("/api/nowplaying/albumart", handleAlbumArt)
	mux.HandleFunc("/api/search", handleSearch)

	mux.HandleFunc("/api/admin/rescan", handleAdminRescan)
	mux.HandleFunc("/api/dev/skip", handleDevSkip)

	mux.Handle("/", http.FileServer(http.Dir(c().RootPath)))

	return mux
}
