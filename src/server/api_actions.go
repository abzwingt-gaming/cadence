// api_actions.go
// Icecast monitor, Liquidsoap HTTP/telnet, filesystem watcher.

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Jeffail/gabs"
	"github.com/fsnotify/fsnotify"
)

var now = RadioInfo{}
var nowMu sync.RWMutex

// artCache stores base64-encoded album art keyed by song ID string.
// Cleared on track change via artCacheClear().
var artCache sync.Map

func artCacheClear() {
	artCache.Range(func(k, _ any) bool {
		artCache.Delete(k)
		return true
	})
}

type RadioInfo struct {
	Song       SongData
	Host       string
	Mountpoint string
	Listeners  int64   // -1 = icecast unreachable / stream idle
	Bitrate    float64
}

type SongData struct {
	ID     int
	Artist string
	Title  string
	Album  string
	Genre  string
	Year   string // stored as TEXT/VARCHAR; kept as string to avoid scan type mismatch
	Path   string
}

var history   []playRecord
var historyMu sync.Mutex

type playRecord struct {
	Title  string
	Artist string
	Ended  time.Time
}

func NowSnapshot() RadioInfo {
	nowMu.RLock()
	defer nowMu.RUnlock()
	return now
}

// liquidsoapHTTP calls the Liquidsoap 2.x HTTP harbor API.
// Port is stored without colon; URL is built cleanly.
func liquidsoapHTTP(cmd, arg string) (string, error) {
	url := fmt.Sprintf("http://%s:%s/api/%s",
		c.LiquidsoapAddress, c.LiquidsoapHTTPPort, cmd)
	var body io.Reader
	if arg != "" {
		body = bytes.NewReader([]byte(arg))
	} else {
		body = bytes.NewReader(nil)
	}
	client := &http.Client{Timeout: c.LiquidsoapTimeout}
	resp, err := client.Post(url, "text/plain", body)
	if err != nil {
		slog.Warn("Liquidsoap HTTP request failed.", "cmd", cmd, "url", url, "error", err)
		return "", err
	}
	defer resp.Body.Close()
	res, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		slog.Warn("Liquidsoap HTTP error.", "cmd", cmd, "status", resp.StatusCode, "body", string(res))
		return "", fmt.Errorf("liquidsoap HTTP %d: %s", resp.StatusCode, res)
	}
	slog.Debug("Liquidsoap HTTP ok.", "cmd", cmd, "resp", strings.TrimSpace(string(res)))
	return string(res), nil
}

// liquidsoapTelnet uses the classic telnet protocol (Liquidsoap server.telnet).
func liquidsoapTelnet(cmd string) (string, error) {
	addr := net.JoinHostPort(c.LiquidsoapAddress, c.LiquidsoapPort)
	conn, err := net.DialTimeout("tcp", addr, c.LiquidsoapTimeout)
	if err != nil {
		slog.Error("Liquidsoap telnet connect failed.", "addr", addr, "error", err)
		return "", err
	}
	defer conn.Close()
	// SetDeadline bounds both write and read; error is non-fatal but logged.
	if dlErr := conn.SetDeadline(time.Now().Add(c.LiquidsoapTimeout)); dlErr != nil {
		slog.Warn("Liquidsoap telnet SetDeadline failed.", "error", dlErr)
	}
	if _, err = fmt.Fprintf(conn, "%s\n", cmd); err != nil {
		slog.Error("Liquidsoap telnet write failed.", "cmd", cmd, "error", err)
		return "", err
	}
	msg, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		slog.Warn("Liquidsoap telnet read partial.", "cmd", cmd, "error", err)
	}
	fmt.Fprintf(conn, "quit\n") //nolint:errcheck // best-effort
	slog.Debug("Liquidsoap telnet ok.", "cmd", cmd, "resp", strings.TrimSpace(msg))
	return msg, err
}

func liquidsoapRequest(path string) (string, error) {
	slog.Debug("liquidsoapRequest.", "path", path, "mode", c.LiquidsoapMode)
	switch c.LiquidsoapMode {
	case "http":
		return liquidsoapHTTP("cadence1.push", path)
	case "telnet":
		return liquidsoapTelnet(fmt.Sprintf("cadence1.push %s", path))
	default: // auto: try HTTP first, fall back to telnet
		if msg, err := liquidsoapHTTP("cadence1.push", path); err == nil {
			return msg, nil
		}
		slog.Warn("Liquidsoap HTTP failed, falling back to telnet.", "path", path)
		return liquidsoapTelnet(fmt.Sprintf("cadence1.push %s", path))
	}
}

func liquidsoapSkip() (string, error) {
	slog.Debug("liquidsoapSkip.", "mode", c.LiquidsoapMode)
	switch c.LiquidsoapMode {
	case "http":
		return liquidsoapHTTP("cadence1.skip", "")
	case "telnet":
		return liquidsoapTelnet("cadence1.skip")
	default:
		if msg, err := liquidsoapHTTP("cadence1.skip", ""); err == nil {
			return msg, nil
		}
		return liquidsoapTelnet("cadence1.skip")
	}
}

func filesystemMonitor() {
	if c.MusicDir == "" {
		slog.Info("Filesystem monitor disabled (CSERVER_MUSIC_DIR not set).")
		return
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("Failed to create fsnotify watcher.", "error", err)
		return
	}
	defer watcher.Close()
	if err = watcher.Add(c.MusicDir); err != nil {
		slog.Error("Cannot watch music dir.", "dir", c.MusicDir, "error", err)
		return
	}
	slog.Info("Filesystem monitor active.", "dir", c.MusicDir, "debounce", c.FsnotifyDebounce)
	var debounce *time.Timer
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				slog.Warn("Filesystem watcher events channel closed.")
				return
			}
			slog.Debug("Filesystem event.", "event", event)
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(c.FsnotifyDebounce, func() {
				if rescanRunning.CompareAndSwap(false, true) {
					defer rescanRunning.Store(false)
					slog.Info("Music dir changed, repopulating DB.", "dir", c.MusicDir)
					if err := dbPopulate(); err != nil {
						slog.Error("Repopulate failed after fs change.", "error", err)
					}
				} else {
					slog.Debug("Skipping fs-triggered rescan, one already in progress.")
				}
			})
		case fsErr, ok := <-watcher.Errors:
			if !ok {
				slog.Warn("Filesystem watcher errors channel closed.")
				return
			}
			slog.Error("Filesystem watcher error.", "error", fsErr)
		}
	}
}

// icecastSource extracts per-source fields from the parsed Icecast JSON.
// Icecast returns "source" as either a single object or an array when
// multiple mountpoints are active. We always pick the first active source
// that matches our mountpoint (or simply the first one).
func icecastSource(j *gabs.Container) *gabs.Container {
	src := j.Path("icestats.source")
	if src == nil {
		return nil
	}
	// Array case: multiple mountpoints.
	if children, err := src.Children(); err == nil && len(children) > 0 {
		// Prefer the mountpoint that matches our configured public stream,
		// otherwise fall back to the first child.
		mount := "/" + c.PostgresTableName // e.g. /cadence1 — not ideal, use a dedicated config field eventually
		_ = mount
		for _, child := range children {
			if listenURL, ok := child.Path("listenurl").Data().(string); ok {
				if c.PublicStreamURL != "" && strings.Contains(listenURL, c.PublicStreamURL) {
					return child
				}
			}
		}
		// No match — return first active source.
		return children[0]
	}
	// Single object case.
	return src
}

func icecastMonitor() {
	var prev RadioInfo

	reset := func() {
		nowMu.Lock()
		now.Song.Title = "-"
		now.Song.Artist = "-"
		now.Host = "-"
		now.Mountpoint = "-"
		now.Listeners = -1
		nowMu.Unlock()
	}

	client := &http.Client{Timeout: 5 * time.Second}

	check := func() {
		url := c.IcecastStatusURL + "/status-json.xsl"
		resp, err := client.Get(url)
		if err != nil {
			slog.Debug("Icecast unreachable.", "url", url, "error", err)
			reset()
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			slog.Warn("Icecast returned non-200.", "status", resp.StatusCode)
			reset()
			return
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			slog.Warn("Icecast body read error.", "error", err)
			reset()
			return
		}
		j, err := gabs.ParseJSON(body)
		if err != nil {
			slog.Warn("Icecast JSON parse error.", "error", err)
			reset()
			return
		}

		src := icecastSource(j)
		if src == nil {
			slog.Debug("Icecast: no source data (stream idle?).")
			reset()
			return
		}

		artistVal, aOK := src.Path("artist").Data().(string)
		titleVal, tOK := src.Path("title").Data().(string)
		if !aOK || !tOK || (artistVal == "" && titleVal == "") {
			slog.Debug("Icecast: source present but no artist/title (stream idle?).")
			reset()
			return
		}

		nowMu.Lock()
		now.Song.Artist = artistVal
		now.Song.Title = titleVal
		if h, ok := j.Path("icestats.host").Data().(string); ok {
			now.Host = h
		}
		if m, ok := src.Path("server_name").Data().(string); ok {
			now.Mountpoint = m
		}
		// listeners is a JSON number; gabs decodes all numbers as float64.
		if l, ok := src.Path("listeners").Data().(float64); ok {
			now.Listeners = int64(l)
		}
		if b, ok := src.Path("bitrate").Data().(float64); ok {
			now.Bitrate = b
		}
		nowMu.Unlock()

		nowSnap := NowSnapshot()

		if prev.Song.Title != nowSnap.Song.Title || prev.Song.Artist != nowSnap.Song.Artist {
			slog.Info("Now playing changed.",
				"title", nowSnap.Song.Title,
				"artist", nowSnap.Song.Artist,
			)
			artCacheClear()
			if redisAvailable.Load() {
				if flushErr := dbr.RateLimitArt.FlushDB(ctx).Err(); flushErr != nil {
					slog.Warn("Redis FlushDB for art rate limit failed.", "error", flushErr)
				}
			}
			radiodata_sse.SendEventMessage(nowSnap.Song.Title, "title", "")
			radiodata_sse.SendEventMessage(nowSnap.Song.Artist, "artist", "")
			if prev.Song.Title != "" && prev.Song.Artist != "" && prev.Song.Title != "-" {
				historyMu.Lock()
				history = append(history, playRecord{
					Title:  prev.Song.Title,
					Artist: prev.Song.Artist,
					Ended:  time.Now(),
				})
				if len(history) > c.HistorySize {
					history = history[len(history)-c.HistorySize:]
				}
				historyMu.Unlock()
				radiodata_sse.SendEventMessage("update", "history", "")
				slog.Debug("History updated.", "size", len(history))
			}
		}

		publicStream := c.PublicStreamURL
		if publicStream == "" {
			publicStream = nowSnap.Host + "/" + nowSnap.Mountpoint
		}
		if prev.Host != nowSnap.Host || prev.Mountpoint != nowSnap.Mountpoint {
			radiodata_sse.SendEventMessage(publicStream, "listenurl", "")
			slog.Debug("Stream URL updated.", "url", publicStream)
		}
		if prev.Listeners != nowSnap.Listeners {
			radiodata_sse.SendEventMessage(fmt.Sprint(nowSnap.Listeners), "listeners", "")
		}
		if prev.Bitrate != nowSnap.Bitrate {
			radiodata_sse.SendEventMessage(fmt.Sprint(nowSnap.Bitrate), "bitrate", "")
		}
		prev = nowSnap
	}

	slog.Info("Icecast monitor started.", "url", c.IcecastStatusURL, "poll", c.IcecastPollInterval)
	// Use a Ticker so poll interval is wall-clock accurate regardless of
	// how long check() takes. time.Sleep would drift when Icecast is slow.
	ticker := time.NewTicker(c.IcecastPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		check()
	}
}
