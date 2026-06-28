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

// liquidsoapClient is a shared http.Client with connection pooling.
// Allocated once via initLiquidsoapClient() from main().
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
// Prefers CSERVER_PUBLIC_STREAM_URL when set (Caddy/CDN front), otherwise
// assembles from the Icecast listenurl.
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
	slog.Info("Icecast monitor started.", "url", statusURL, "interval", c().IcecastPollInterval)

	for range ticker.C {
		if err := pollIcecast(statusURL); err != nil {
			slog.Debug("Icecast poll failed.", "error", err)
		}
	}
}

func pollIcecast(statusURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d from Icecast", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	var raw struct {
		Icestats struct {
			Source json.RawMessage `json:"source"`
		} `json:"icestats"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("JSON unmarshal: %w", err)
	}

	// Source can be a single object or an array — handle both.
	var sources []map[string]interface{}
	var single map[string]interface{}
	if json.Unmarshal(raw.Icestats.Source, &single) == nil {
		sources = []map[string]interface{}{single}
	} else {
		if err := json.Unmarshal(raw.Icestats.Source, &sources); err != nil {
			return fmt.Errorf("parse sources: %w", err)
		}
	}
	if len(sources) == 0 {
		return fmt.Errorf("no active Icecast sources")
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
		slog.Info("Now playing changed.",
			"title", nd.Title,
			"artist", nd.Artist,
			"listeners", nd.Listeners,
		)
		historyPush(nd.Title, nd.Artist)
		inMemoryArtCacheClear()
		artCacheClear()
		sseNotify(nd)
	}
	if nd.Listeners != prev.Listeners {
		slog.Debug("Listener count changed.", "listeners", nd.Listeners)
	}
	return nil
}

// ── Liquidsoap HTTP request ───────────────────────────────────────────────────

func liquidsoapHTTP(path string) error {
	if liquidsoapClient == nil {
		initLiquidsoapClient()
	}
	rawURL := fmt.Sprintf("http://%s:%s/%s",
		c().LiquidsoapAddress, c().LiquidsoapHTTPPort,
		strings.TrimPrefix(path, "/"))
	ctx, cancel := context.WithTimeout(context.Background(), c().LiquidsoapTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("liquidsoap HTTP build request path=%q: %w", path, err)
	}
	resp, err := liquidsoapClient.Do(req)
	if err != nil {
		return fmt.Errorf("liquidsoap HTTP %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // drain so connection is reused
	if resp.StatusCode >= 400 {
		return fmt.Errorf("liquidsoap HTTP %s returned %d", rawURL, resp.StatusCode)
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
