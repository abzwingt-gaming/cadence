// sse.go — Server-Sent Events for /api/radiodata/sse.
// Replaces the old eventsource.v1 dependency.

package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"
)

type sseClient struct {
	ch chan string
}

var (
	sseMu     sync.RWMutex
	sseClients = map[*sseClient]struct{}{}
)

func handleSSE(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fl.Flush()

	client := &sseClient{ch: make(chan string, 4)}
	sseMu.Lock()
	sseClients[client] = struct{}{}
	sseMu.Unlock()

	defer func() {
		sseMu.Lock()
		delete(sseClients, client)
		sseMu.Unlock()
	}()

	// Send current state immediately on connect.
	nd := getNowPlaying()
	fmt.Fprintf(w, "data: {\"Title\":%q,\"Artist\":%q,\"Stream\":%q}\n\n",
		nd.Title, nd.Artist, nd.Mountpoint)
	fl.Flush()

	for {
		select {
		case msg := <-client.ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// sseNotify broadcasts a now-playing update to all connected SSE clients.
func sseNotify(nd nowPlayingData) {
	msg := fmt.Sprintf("{\"Title\":%q,\"Artist\":%q,\"Stream\":%q}",
		nd.Title, nd.Artist, nd.Mountpoint)
	sseMu.RLock()
	defer sseMu.RUnlock()
	for client := range sseClients {
		select {
		case client.ch <- msg:
		default:
			slog.Debug("SSE client buffer full, dropping event.")
		}
	}
}
