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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Jeffail/gabs"
	"github.com/fsnotify/fsnotify"
)

var now = RadioInfo{}
var nowMu sync.RWMutex

// artCache stores base64-encoded album art keyed by song ID string.
// Evicted on every track change via artCacheClear().
var artCache sync.Map

// artCacheClear evicts all cached album art when the track changes.
// We do NOT reset artFlight here: any in-flight Do() calls for the
// previous track will complete and store into artCache, but that entry
// will simply be evicted on the next artCacheClear(). Resetting
// artFlight while callers are inside Do() is a data race.
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
	Listeners  int64
	Bitrate    float64
}

type SongData struct {
	ID     int
	Artist string
	Title  string
	Album  string
	Genre  string
	Year   string
	Path   string
}

var history []playRecord
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
	fmt.Fprintf(conn, "quit\n") //nolint:errcheck
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
	default:
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

// filesystemMonitor watches c.MusicDir recursively (including subdirectories)
// using fsnotify. File-system events are debounced before triggering a rescan.
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

	// Walk the music directory tree and watch every subdirectory,
	// because fsnotify does not recurse into subdirectories by default.
	watchErr := filepath.Walk(c.MusicDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Warn("Walk error while setting up watcher.", "path", path, "error", err)
			return nil
		}
		if info.IsDir() {
			if addErr := watcher.Add(path); addErr != nil {
				slog.Warn("Cannot watch subdirectory.", "dir", path, "error", addErr)
			}
		}
		return nil
	})
	if watchErr != nil {
		slog.Error("Cannot walk music dir for watcher setup.", "dir", c.MusicDir, "error", watchErr)
		return
	}
	slog.Info("Filesystem monitor active (recursive).", "dir", c.MusicDir, "debounce", c.FsnotifyDebounce)

	var debounce *time.Timer
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				slog.Warn("Filesystem watcher events channel closed.")
				return
			}
			slog.Debug("Filesystem event.", "event", event)
			// When a new directory is created, add it to the watch list
			// immediately so files added shortly after are also caught.
			if event.Has(fsnotify.Create) {
				if fi, statErr := os.Stat(event.Name); statErr == nil && fi.IsDir() {
					if addErr := watcher.Add(event.Name); addErr != nil {
						slog.Warn("Cannot watch new subdirectory.", "dir", event.Name, "error", addErr)
					} else {
						slog.Debug("Watching new subdirectory.", "dir", event.Name)
					}
				}
			}
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
func icecastSource(j *gabs.Container) *gabs.Container {
	src := j.Path("icestats.source")
	if src == nil {
		return nil
	}
	if children, err := src.Children(); err == nil && len(children) > 0 {
		if c.PublicStreamURL != "" {
			for _, child := range children {
				if listenURL, ok := child.Path("listenurl").Data().(string); ok {
					if strings.Contains(listenURL, c.PublicStreamURL) {
						return child
					}
				}
			}
		}
		return children[0]
	}
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
		// Limit Icecast body to 1 MB — status JSON should be tiny.
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
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

		// Capture the full snapshot under a single write lock.
		nowMu.Lock()
		now.Song.Artist = artistVal
		now.Song.Title = titleVal
		if h, ok := j.Path("icestats.host").Data().(string); ok {
			now.Host = h
		}
		if m, ok := src.Path("server_name").Data().(string); ok {
			now.Mountpoint = m
		}
		if l, ok := src.Path("listeners").Data().(float64); ok {
			now.Listeners = int64(l)
		}
		if b, ok := src.Path("bitrate").Data().(float64); ok {
			now.Bitrate = b
		}
		// Take the snapshot while still holding the write lock to avoid a
		// second lock acquisition immediately after Unlock().
		nowSnap := now
		nowMu.Unlock()

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
			if prev.Song.Title != "" && prev.Song.Title != "-" &&
				prev.Song.Artist != "" && prev.Song.Artist != "-" {
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

		publicStream := buildPublicStream(nowSnap.Host, nowSnap.Mountpoint)
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
	ticker := time.NewTicker(c.IcecastPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		check()
	}
}
