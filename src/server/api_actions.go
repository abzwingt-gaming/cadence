// api_actions.go — Icecast monitor, Liquidsoap requests, play history.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// liquidsoapClient is a package-level http.Client with connection pooling.
// Creating a new http.Client per request exhausts file descriptors under load.
// Initialised once via initLiquidsoapClient() called from main().
var liquidsoapClient *http.Client

func initLiquidsoapClient() {
	liquidsoapClient = &http.Client{
		Timeout: c().LiquidsoapTimeout,
		Transport: &http.Transport{
			MaxIdleConns:       10,
			IdleConnTimeout:    90 * time.Second,
			DisableCompression: true,
		},
	}
}

// ── History ───────────────────────────────────────────────────────────────────

type playRecord struct {
	Title  string    `json:"Title"`
	Artist string    `json:"Artist"`
	Ended  time.Time `json:"Ended"`
}

var (
	historyMu sync.RWMutex
	history   []playRecord
)

func historyPush(title, artist string) {
	historyMu.Lock()
	defer historyMu.Unlock()
	if len(history) > 0 {
		last := &history[len(history)-1]
		if last.Title == title && last.Artist == artist {
			return // duplicate; do not double-record
		}
		last.Ended = time.Now()
	}
	history = append(history, playRecord{Title: title, Artist: artist})
	if hs := c().HistorySize; len(history) > hs {
		history = history[len(history)-hs:]
	}
}

func historyGet() []playRecord {
	historyMu.RLock()
	defer historyMu.RUnlock()
	out := make([]playRecord, len(history))
	copy(out, history)
	return out
}

// ── nowPlaying ────────────────────────────────────────────────────────────────

type nowPlayingData struct {
	Title      string
	Artist     string
	Mountpoint string
	Listeners  int
	Bitrate    int
}

var (
	nowMu      sync.RWMutex
	nowPlaying nowPlayingData
)

func getNowPlaying() nowPlayingData {
	nowMu.RLock()
	defer nowMu.RUnlock()
	return nowPlaying
}

// buildPublicStream returns the stream URL clients should connect to.
// Priority: CSERVER_PUBLIC_STREAM_URL (Caddy/CDN URL) -> Icecast listenurl host + path.
func buildPublicStream(icecastBase, mountpath string) string {
	if pu := c().PublicStreamURL; pu != "" {
		return strings.TrimRight(pu, "/") + mountpath
	}
	return strings.TrimRight(icecastBase, "/") + mountpath
}

// ── Icecast monitor ───────────────────────────────────────────────────────────

func icecastMonitor() {
	ticker := time.NewTicker(c().IcecastPollInterval)
	defer ticker.Stop()
	statusURL := c().IcecastStatusURL + "/status-json.xsl"

	for range ticker.C {
		func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				slog.Debug("Icecast poll failed.", "error", err)
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

			var raw map[string]interface{}
			if json.Unmarshal(body, &raw) != nil {
				return
			}
			icestats, _ := raw["icestats"].(map[string]interface{})
			if icestats == nil {
				return
			}

			var sources []map[string]interface{}
			switch v := icestats["source"].(type) {
			case map[string]interface{}:
				sources = []map[string]interface{}{v}
			case []interface{}:
				for _, s := range v {
					if m, ok := s.(map[string]interface{}); ok {
						sources = append(sources, m)
					}
				}
			}
			if len(sources) == 0 {
				return
			}
			src := sources[0]

			nd := nowPlayingData{}
			if t, ok := src["title"].(string); ok {
				nd.Title = t
			}
			if a, ok := src["artist"].(string); ok {
				nd.Artist = a
			}
			if l, ok := src["listeners"].(float64); ok {
				nd.Listeners = int(l)
			}
			if b, ok := src["bitrate"].(float64); ok {
				nd.Bitrate = int(b)
			}

			// Extract mountpoint from listenurl path, not server_name.
			if lu, ok := src["listenurl"].(string); ok && lu != "" {
				if u, err := url.Parse(lu); err == nil && u.Path != "" {
					nd.Mountpoint = buildPublicStream(c().IcecastStatusURL, u.Path)
				}
			}
			if nd.Mountpoint == "" {
				if sn, ok := src["server_name"].(string); ok && sn != "" {
					nd.Mountpoint = buildPublicStream(c().IcecastStatusURL, "/"+strings.TrimPrefix(sn, "/"))
				}
			}

			nowMu.Lock()
			prev := nowPlaying
			nowPlaying = nd
			nowMu.Unlock()

			if nd.Title != prev.Title || nd.Artist != prev.Artist {
				historyPush(nd.Title, nd.Artist)
				inMemoryArtCacheClear()
				artCacheClear()
				sseNotify(nd)
				slog.Info("Now playing.", "title", nd.Title, "artist", nd.Artist)
			}
		}()
	}
}

// ── Liquidsoap request ────────────────────────────────────────────────────────

func liquidsoapHTTP(path string) error {
	if liquidsoapClient == nil {
		initLiquidsoapClient()
	}
	rawURL := fmt.Sprintf("http://%s:%s/%s",
		c().LiquidsoapAddress, c().LiquidsoapHTTPPort, strings.TrimPrefix(path, "/"))
	ctx, cancel := context.WithTimeout(context.Background(), c().LiquidsoapTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := liquidsoapClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("liquidsoap returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func requestSong(id string) error {
	switch c().LiquidsoapMode {
	case "http":
		return liquidsoapHTTP("request/" + id)
	default:
		return liquidsoapTelnet("request.push " + id)
	}
}
