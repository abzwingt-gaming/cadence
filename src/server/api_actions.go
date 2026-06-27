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

var artCache sync.Map

type RadioInfo struct {
	Song       SongData
	Host       string
	Mountpoint string
	Listeners  float64
	Bitrate    float64
}

type SongData struct {
	ID     int
	Artist string
	Title  string
	Album  string
	Genre  string
	Year   int
	Path   string
}

// history is protected by historyMu.
// Written in icecastMonitor goroutine, read in History() HTTP handler.
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

// liquidsoapRequest pushes a path to Liquidsoap.
// CSERVER_LIQUIDSOAP_MODE: http | telnet | auto (default)
func liquidsoapRequest(path string) (string, error) {
	switch strings.ToLower(c.LiquidsoapMode) {
	case "http":
		return liquidsoapHTTP("request.push", path)
	case "telnet":
		return liquidsoapTelnet(fmt.Sprintf("request.push %s", path))
	default: // auto: HTTP first, telnet fallback
		if msg, err := liquidsoapHTTP("request.push", path); err == nil {
			return msg, nil
		}
		slog.Warn("Liquidsoap HTTP failed, falling back to telnet.")
		return liquidsoapTelnet(fmt.Sprintf("request.push %s", path))
	}
}

func liquidsoapSkip() (string, error) {
	switch strings.ToLower(c.LiquidsoapMode) {
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

// liquidsoapHTTP uses Liquidsoap 2.x HTTP harbor API.
func liquidsoapHTTP(cmd, arg string) (string, error) {
	url := fmt.Sprintf("http://%s%s/api/%s",
		c.LiquidsoapAddress, c.LiquidsoapHTTPPort, cmd)
	var body *bytes.Reader
	if arg != "" {
		body = bytes.NewReader([]byte(arg))
	} else {
		body = bytes.NewReader(nil)
	}
	resp, err := http.Post(url, "text/plain", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	res, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("liquidsoap HTTP %d: %s", resp.StatusCode, res)
	}
	slog.Info("Liquidsoap HTTP.", "cmd", cmd, "resp", strings.TrimSpace(string(res)))
	return string(res), nil
}

// liquidsoapTelnet uses the classic telnet protocol.
// Requires server.telnet = true in the liquidsoap .liq script.
func liquidsoapTelnet(cmd string) (string, error) {
	conn, err := net.DialTimeout("tcp", c.LiquidsoapAddress+c.LiquidsoapPort, 5*time.Second)
	if err != nil {
		slog.Error("Liquidsoap telnet connect failed.", "error", err)
		return "", err
	}
	defer conn.Close()
	fmt.Fprintf(conn, "%s\n", cmd)
	msg, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		slog.Error("Liquidsoap telnet read failed.", "error", err)
	}
	fmt.Fprintf(conn, "quit\n")
	slog.Info("Liquidsoap telnet.", "cmd", cmd, "msg", strings.TrimSpace(msg))
	return msg, nil
}

func filesystemMonitor() {
	if c.MusicDir == "" {
		return
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("fsnotify failed.", "error", err)
		return
	}
	defer watcher.Close()
	if err = watcher.Add(c.MusicDir); err != nil {
		slog.Error("Cannot watch music dir.", "dir", c.MusicDir, "error", err)
		return
	}
	var debounce *time.Timer
	for {
		select {
		case _, ok := <-watcher.Events:
			if !ok {
				return
			}
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(3*time.Second, func() {
				if rescanRunning.CompareAndSwap(false, true) {
					defer rescanRunning.Store(false)
					slog.Info("Music dir changed, repopulating DB.")
					if err := dbPopulate(); err != nil {
						slog.Error("Repopulate failed.", "error", err)
					}
				}
			})
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			slog.Error("Watcher error.", "error", err)
		}
	}
}

func icecastMonitor() {
	var prev RadioInfo
	reset := func() {
		nowMu.Lock()
		now.Song.Title, now.Song.Artist = "-", "-"
		now.Host, now.Mountpoint = "-", "-"
		now.Listeners = -1
		nowMu.Unlock()
	}
	check := func() {
		resp, err := http.Get(c.IcecastStatusURL + "/status-json.xsl")
		if err != nil {
			slog.Debug("Icecast unreachable.", "error", err)
			reset()
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			reset()
			return
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			reset()
			return
		}
		j, err := gabs.ParseJSON(body)
		if err != nil {
			reset()
			return
		}
		artistVal := j.Path("icestats.source.artist").Data()
		titleVal := j.Path("icestats.source.title").Data()
		if artistVal == nil || titleVal == nil {
			reset()
			return
		}

		nowMu.Lock()
		now.Song.Artist = artistVal.(string)
		now.Song.Title = titleVal.(string)
		if h := j.Path("icestats.host").Data(); h != nil {
			now.Host = h.(string)
		}
		if m := j.Path("icestats.source.server_name").Data(); m != nil {
			now.Mountpoint = m.(string)
		}
		if l := j.Path("icestats.source.listeners").Data(); l != nil {
			now.Listeners = l.(float64)
		}
		if b := j.Path("icestats.source.bitrate").Data(); b != nil {
			now.Bitrate = b.(float64)
		}
		nowMu.Unlock()

		nowSnap := NowSnapshot()

		if prev.Song.Title != nowSnap.Song.Title || prev.Song.Artist != nowSnap.Song.Artist {
			slog.Info("Now Playing.", "title", nowSnap.Song.Title, "artist", nowSnap.Song.Artist)
			artCache = sync.Map{}
			if redisAvailable.Load() {
				dbr.RateLimitArt.FlushDB(ctx)
			}
			radiodata_sse.SendEventMessage(nowSnap.Song.Title, "title", "")
			radiodata_sse.SendEventMessage(nowSnap.Song.Artist, "artist", "")
			if prev.Song.Title != "" && prev.Song.Artist != "" {
				historyMu.Lock()
				history = append(history, playRecord{
					Title:  prev.Song.Title,
					Artist: prev.Song.Artist,
					Ended:  time.Now(),
				})
				if len(history) > 10 {
					history = history[1:]
				}
				historyMu.Unlock()
				radiodata_sse.SendEventMessage("update", "history", "")
			}
		}

		publicStream := c.PublicStreamURL
		if publicStream == "" {
			publicStream = nowSnap.Host + "/" + nowSnap.Mountpoint
		}
		if prev.Host != nowSnap.Host || prev.Mountpoint != nowSnap.Mountpoint {
			radiodata_sse.SendEventMessage(publicStream, "listenurl", "")
		}
		if prev.Listeners != nowSnap.Listeners {
			radiodata_sse.SendEventMessage(fmt.Sprint(nowSnap.Listeners), "listeners", "")
		}
		if prev.Bitrate != nowSnap.Bitrate {
			radiodata_sse.SendEventMessage(fmt.Sprint(nowSnap.Bitrate), "bitrate", "")
		}
		prev = nowSnap
	}
	for {
		time.Sleep(1 * time.Second)
		check()
	}
}
