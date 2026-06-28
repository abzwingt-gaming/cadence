// sse.go — Server-Sent Events for /api/radiodata/sse.
//
// Improvements:
//   - 20s keepalive comment frames prevent Caddy/Nginx from closing idle conns.
//   - CSERVER_SSE_MAX_CLIENTS cap (default 200) returns 503 when exceeded.
//   - Last-Event-ID replay: reconnecting clients get the last event immediately.

package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type sseClient struct {
	ch chan []byte
}

type sseEvent struct {
	Title     string `json:"Title"`
	Artist    string `json:"Artist"`
	Stream    string `json:"Stream"`
	Listeners int    `json:"Listeners"`
	Bitrate   int    `json:"Bitrate"`
}

var (
	sseMu       sync.RWMutex
	sseClients  = map[*sseClient]struct{}{}
	sseCount    atomic.Int64
	sseLastOnce sync.RWMutex
	sseLastMsg  []byte // last broadcast payload for Last-Event-ID replay
)

func sseMaxClients() int {
	return envInt("CSERVER_SSE_MAX_CLIENTS", 200)
}

func handleSSE(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Enforce client cap.
	if int(sseCount.Load()) >= sseMaxClients() {
		http.Error(w, "too many SSE clients", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fl.Flush()

	client := &sseClient{ch: make(chan []byte, 8)}
	sseMu.Lock()
	sseClients[client] = struct{}{}
	sseMu.Unlock()
	sseCount.Add(1)
	defer func() {
		sseMu.Lock()
		delete(sseClients, client)
		sseMu.Unlock()
		sseCount.Add(-1)
	}()

	// Replay last event for reconnecting clients that send Last-Event-ID.
	// We treat any non-empty Last-Event-ID as "give me the latest".
	var initialPayload []byte
	if r.Header.Get("Last-Event-ID") != "" {
		sseLastOnce.RLock()
		if sseLastMsg != nil {
			initialPayload = sseLastMsg
		}
		sseLastOnce.RUnlock()
	}
	if initialPayload == nil {
		if p, err := marshalSSE(getNowPlaying()); err == nil {
			initialPayload = p
		}
	}
	if initialPayload != nil {
		if _, err := fmt.Fprintf(w, "id: latest\ndata: %s\n\n", initialPayload); err != nil {
			slog.Debug("SSE: client disconnected before first write.", "error", err)
			return
		}
		fl.Flush()
	}

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case msg := <-client.ch:
			if _, err := fmt.Fprintf(w, "id: latest\ndata: %s\n\n", msg); err != nil {
				slog.Debug("SSE: client write error, closing stream.", "error", err)
				return
			}
			fl.Flush()
		case <-keepalive.C:
			// SSE comment — ignored by browsers but keeps the connection alive
			// through Caddy/Nginx idle-connection timeouts (default 60s).
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				slog.Debug("SSE: keepalive write error, closing stream.", "error", err)
				return
			}
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func marshalSSE(nd nowPlayingData) ([]byte, error) {
	return json.Marshal(sseEvent{
		Title:     nd.Title,
		Artist:    nd.Artist,
		Stream:    nd.Mountpoint,
		Listeners: nd.Listeners,
		Bitrate:   nd.Bitrate,
	})
}

// sseNotify broadcasts a now-playing update to all connected SSE clients.
// Non-blocking: slow clients drop the event rather than blocking the monitor.
func sseNotify(nd nowPlayingData) {
	payload, err := marshalSSE(nd)
	if err != nil {
		slog.Error("SSE: failed to marshal notify event.", "error", err)
		return
	}
	// Store for Last-Event-ID replay.
	sseLastOnce.Lock()
	sseLastMsg = payload
	sseLastOnce.Unlock()

	sseMu.RLock()
	defer sseMu.RUnlock()
	dropped := 0
	for client := range sseClients {
		select {
		case client.ch <- payload:
		default:
			dropped++
		}
	}
	if dropped > 0 {
		slog.Warn("SSE: dropped events for slow clients.", "dropped", dropped)
	}
}
