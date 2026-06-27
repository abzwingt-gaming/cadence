// api_actions.go
// Icecast monitor, Liquidsoap telnet, filesystem watcher.

package main

import (
	"bufio"
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

var now     = RadioInfo{}
var nowMu   sync.RWMutex

// artCache holds base64-encoded art keyed by song path, cleared on track change.
var artCache   sync.Map
var artCacheKey string

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

// NowSnapshot returns a safe copy of now.
func NowSnapshot() RadioInfo {
	nowMu.RLock()
	defer nowMu.RUnlock()
	return now
}

func liquidsoapRequest(path string) (string, error) {
	conn, err := net.DialTimeout("tcp", c.LiquidsoapAddress+c.LiquidsoapPort, 5*time.Second)
	if err != nil {
		slog.Error("Liquidsoap connect failed.", "error", err)
		return "", err
	}
	defer conn.Close()
	fmt.Fprintf(conn, "request.push %s\n", path)
	msg, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		slog.Error("Liquidsoap read failed.", "error", err)
	}
	fmt.Fprintf(conn, "quit\n")
	slog.Info("Liquidsoap response.", "msg", strings.TrimSpace(msg))
	return msg, nil
}

func liquidsoapSkip() (string, error) {
	conn, err := net.DialTimeout("tcp", c.LiquidsoapAddress+c.LiquidsoapPort, 5*time.Second)
	if err != nil {
		slog.Error("Liquidsoap connect failed.", "error", err)
		return "", err
	}
	defer conn.Close()
	fmt.Fprintf(conn, "cadence1.skip\n")
	msg, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		slog.Error("Liquidsoap skip read failed.", "error", err)
	}
	fmt.Fprintf(conn, "quit\n")
	return msg, nil
}

func filesystemMonitor() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("fsnotify failed.", "error", err)
		return
	}
	defer watcher.Close()
	if err = watcher.Add(c.MusicDir); err != nil {
		slog.Error("Cannot watch music dir.", "error", err)
		return
	}
	// Debounce: wait 3s after last event before repopulating
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
				slog.Info("Music dir changed, repopulating DB.")
				if err := dbPopulate(); err != nil {
					slog.Error("Repopulate failed.", "error", err)
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
		titleVal  := j.Path("icestats.source.title").Data()
		if artistVal == nil || titleVal == nil {
			reset()
			return
		}

		nowMu.Lock()
		now.Song.Artist = artistVal.(string)
		now.Song.Title  = titleVal.(string)
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
			// Invalidate art cache
			artCache = sync.Map{}
			if redisAvailable {
				dbr.RateLimitArt.FlushDB(ctx)
			}
			radiodata_sse.SendEventMessage(nowSnap.Song.Title, "title", "")
			radiodata_sse.SendEventMessage(nowSnap.Song.Artist, "artist", "")
			if prev.Song.Title != "" && prev.Song.Artist != "" {
				history = append(history, playRecord{
					Title:  prev.Song.Title,
					Artist: prev.Song.Artist,
					Ended:  time.Now(),
				})
				if len(history) > 10 {
					history = history[1:]
				}
				radiodata_sse.SendEventMessage("update", "history", "")
			}
		}

		// Public stream URL: prefer CSERVER_PUBLIC_STREAM_URL, fallback to Icecast host/mountpoint
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
		prev = nowSnap
	}
	for {
		time.Sleep(1 * time.Second)
		check()
	}
}

var history = make([]playRecord, 0, 10)

type playRecord struct {
	Title  string
	Artist string
	Ended  time.Time
}
