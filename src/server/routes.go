package main

import (
	"encoding/json"
	"net/http"
	"time"

	eventsource "gopkg.in/antage/eventsource.v1"
)

var radiodata_sse eventsource.EventSource

func routes() http.Handler {
	radiodata_sse = eventsource.New(nil, func(req *http.Request) [][]byte {
		return [][]byte{}
	})

	mux := http.NewServeMux()

	// Health
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		type health struct {
			Status   string `json:"status"`
			Redis    bool   `json:"redis"`
			Postgres bool   `json:"postgres"`
			Uptime   string `json:"uptime"`
		}
		dbOK := dbp != nil && dbp.Ping() == nil
		status := "ok"
		if !dbOK {
			status = "degraded"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(health{
			Status:   status,
			Redis:    redisAvailable,
			Postgres: dbOK,
			Uptime:   time.Since(startTime).Round(time.Second).String(),
		})
	})
	mux.HandleFunc("/ready", Ready())

	// Static files
	fs := http.FileServer(http.Dir(c.RootPath + "public"))
	mux.Handle("/", fs)

	// SSE
	mux.Handle("/api/radiodata/sse", radiodata_sse)

	// Public API
	mux.Handle("/api/search", http.HandlerFunc(Search()))
	mux.Handle("/api/request/id", rateLimitRequest(http.HandlerFunc(RequestID())))
	mux.Handle("/api/request/bestmatch", rateLimitRequest(http.HandlerFunc(RequestBestMatch())))
	mux.Handle("/api/nowplaying/metadata", http.HandlerFunc(NowPlayingMetadata()))
	mux.Handle("/api/nowplaying/albumart", rateLimitArt(http.HandlerFunc(NowPlayingAlbumArt())))
	mux.Handle("/api/history", http.HandlerFunc(History()))
	mux.Handle("/api/listenurl", http.HandlerFunc(ListenURL()))
	mux.Handle("/api/listeners", http.HandlerFunc(Listeners()))
	mux.Handle("/api/bitrate", http.HandlerFunc(Bitrate()))
	mux.Handle("/api/version", http.HandlerFunc(Version()))

	if c.DevMode {
		mux.Handle("/api/dev/skip", http.HandlerFunc(DevSkip()))
	}

	return mux
}

var startTime = time.Now()
