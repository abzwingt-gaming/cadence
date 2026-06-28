// sse.go — Server-Sent Events for /api/radiodata/sse.

package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
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
	sseMu      sync.RWMutex
	sseClients = map[*sseClient]struct{}{}
)

func handleSSE(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
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
	defer func() {
		sseMu.Lock()
		delete(sseClients, client)
		sseMu.Unlock()
	}()

	// Send current state immediately on connect.
	if payload, err := marshalSSE(getNowPlaying()); err != nil {
		slog.Warn("SSE: failed to marshal initial event.", "error", err)
	} else if _, err = fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		slog.Debug("SSE: client disconnected before first write.", "error", err)
		return
	} else {
		fl.Flush()
	}

	for {
		select {
		case msg := <-client.ch:
			if _, err := fmt.Fprintf(w, "data: %s\n\n", msg); err != nil {
				slog.Debug("SSE: client write error, closing stream.", "error", err)
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
