// api_actions.go
// API interactions for Postgres, Icecast, Liquidsoap.

package main

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Jeffail/gabs"
	"github.com/fsnotify/fsnotify"
)

var now = RadioInfo{}

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

func searchByQuery(query string) ([]SongData, error) {
	query = strings.TrimSpace(query)
	slog.Debug(fmt.Sprintf("Search query: '%s'", query), "func", "searchByQuery")
	stmt := fmt.Sprintf(
		"SELECT id, artist, title, album, genre, year FROM %s WHERE artist ILIKE $1 OR title ILIKE $2 ORDER BY LEAST(levenshtein($3, artist), levenshtein($4, title))",
		c.PostgresTableName)
	rows, err := dbp.Query(stmt, "%"+query+"%", "%"+query+"%", query, query)
	if err != nil {
		slog.Error("DB search failed.", "func", "searchByQuery", "error", err)
		return nil, err
	}
	defer rows.Close()
	var results []SongData
	for rows.Next() {
		var s SongData
		if err = rows.Scan(&s.ID, &s.Artist, &s.Title, &s.Album, &s.Genre, &s.Year); err != nil {
			slog.Warn("Row scan error.", "func", "searchByQuery", "error", err)
			continue
		}
		results = append(results, s)
	}
	return results, nil
}

func searchByTitleArtist(title, artist string) ([]SongData, error) {
	title, artist = strings.TrimSpace(title), strings.TrimSpace(artist)
	stmt := fmt.Sprintf(
		"SELECT id, artist, title, album, genre, year FROM %s WHERE title LIKE $1 AND artist LIKE $2",
		c.PostgresTableName)
	rows, err := dbp.Query(stmt, title, artist)
	if err != nil {
		slog.Error("DB query failed.", "func", "searchByTitleArtist", "error", err)
		return nil, err
	}
	defer rows.Close()
	var results []SongData
	for rows.Next() {
		var s SongData
		if err = rows.Scan(&s.ID, &s.Artist, &s.Title, &s.Album, &s.Genre, &s.Year); err != nil {
			slog.Warn("Row scan error.", "func", "searchByTitleArtist", "error", err)
			continue
		}
		results = append(results, s)
	}
	return results, nil
}

func getPathById(id int) (string, error) {
	var path string
	stmt := fmt.Sprintf("SELECT path FROM %s WHERE id=$1", c.PostgresTableName)
	err := dbp.QueryRow(stmt, id).Scan(&path)
	if err != nil {
		slog.Error("DB path lookup failed.", "func", "getPathById", "error", err)
		return "", err
	}
	return path, nil
}

func liquidsoapRequest(path string) (string, error) {
	slog.Debug("Connecting to Liquidsoap...", "func", "liquidsoapRequest")
	conn, err := net.Dial("tcp", c.LiquidsoapAddress+c.LiquidsoapPort)
	if err != nil {
		slog.Error("Failed to connect to Liquidsoap.", "func", "liquidsoapRequest", "error", err)
		return "", err
	}
	defer conn.Close()
	fmt.Fprintf(conn, "request.push %s\n", path)
	msg, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		slog.Error("Failed to read Liquidsoap response.", "func", "liquidsoapRequest", "error", err)
	}
	slog.Info(fmt.Sprintf("Liquidsoap: %s", msg), "func", "liquidsoapRequest")
	fmt.Fprintf(conn, "quit\n")
	return msg, nil
}

func liquidsoapSkip() (string, error) {
	slog.Debug("Connecting to Liquidsoap...", "func", "liquidsoapSkip")
	conn, err := net.Dial("tcp", c.LiquidsoapAddress+c.LiquidsoapPort)
	if err != nil {
		slog.Error("Failed to connect to Liquidsoap.", "func", "liquidsoapSkip", "error", err)
		return "", err
	}
	defer conn.Close()
	fmt.Fprintf(conn, "cadence1.skip\n")
	msg, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		slog.Error("Failed to read Liquidsoap response.", "func", "liquidsoapSkip", "error", err)
	}
	fmt.Fprintf(conn, "quit\n")
	return msg, nil
}

func filesystemMonitor() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("Could not create fs watcher.", "func", "filesystemMonitor", "error", err)
		return
	}
	defer watcher.Close()
	if err = watcher.Add(c.MusicDir); err != nil {
		slog.Error("Could not watch music directory.", "func", "filesystemMonitor", "error", err)
		return
	}
	done := make(chan bool)
	go func() {
		for {
			select {
			case _, ok := <-watcher.Events:
				if !ok {
					continue
				}
				slog.Info("Music library changed, re-indexing...", "func", "filesystemMonitor")
				if err := postgresPopulate(); err != nil {
					slog.Error("Re-index failed.", "func", "filesystemMonitor", "error", err)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					continue
				}
				slog.Error("Watcher error.", "func", "filesystemMonitor", "error", err)
			}
		}
	}()
	<-done
}

// publicStreamURL returns the URL the browser should use to play the stream.
// Prefers CSERVER_PUBLIC_STREAM_URL env override, falls back to Icecast host/mountpoint.
func publicStreamURL() string {
	if c.PublicStreamURL != "" {
		return c.PublicStreamURL
	}
	if now.Host != "" && now.Host != "-" && now.Mountpoint != "" && now.Mountpoint != "-" {
		return now.Host + "/" + now.Mountpoint
	}
	return ""
}

func icecastMonitor() {
	var prev RadioInfo
	reset := func() {
		now.Song.Title = "-"
		now.Song.Artist = "-"
		now.Host = "-"
		now.Mountpoint = "-"
		now.Listeners = -1
	}
	check := func() {
		// c.IcecastStatusURL is already fully formed (set in main.go)
		resp, err := http.Get(c.IcecastStatusURL)
		if err != nil {
			slog.Error("Cannot reach Icecast.", "func", "icecastMonitor", "url", c.IcecastStatusURL, "error", err)
			reset()
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			slog.Debug("Icecast returned non-200.", "func", "icecastMonitor", "status", resp.StatusCode)
			reset()
			return
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			slog.Debug("Could not read Icecast response body.", "func", "icecastMonitor")
			reset()
			return
		}
		json, err := gabs.ParseJSON(body)
		if err != nil {
			slog.Debug("Could not parse Icecast JSON.", "func", "icecastMonitor")
			reset()
			return
		}
		if json.Path("icestats.source.title").Data() == nil || json.Path("icestats.source.artist").Data() == nil {
			slog.Debug("Icecast connected but nothing playing.", "func", "icecastMonitor")
			reset()
			return
		}

		now.Song.Artist = json.Path("icestats.source.artist").Data().(string)
		now.Song.Title = json.Path("icestats.source.title").Data().(string)
		now.Host = json.Path("icestats.host").Data().(string)
		now.Mountpoint = json.Path("icestats.source.server_name").Data().(string)
		now.Listeners = json.Path("icestats.source.listeners").Data().(float64)
		now.Bitrate = json.Path("icestats.source.bitrate").Data().(float64)

		if prev.Song.Title != now.Song.Title || prev.Song.Artist != now.Song.Artist {
			slog.Info(fmt.Sprintf("Now Playing: %s - %s", now.Song.Artist, now.Song.Title), "func", "icecastMonitor")
			if redisAvailable {
				dbr.RateLimitArt.FlushDB(ctx)
			}
			radiodata_sse.SendEventMessage(now.Song.Title, "title", "")
			radiodata_sse.SendEventMessage(now.Song.Artist, "artist", "")
			if prev.Song.Title != "" && prev.Song.Artist != "" {
				history = append(history, playRecord{Title: prev.Song.Title, Artist: prev.Song.Artist, Ended: time.Now()})
				if len(history) > 10 {
					history = history[1:]
				}
				radiodata_sse.SendEventMessage("update", "history", "")
			}
		}
		if prev.Host != now.Host || prev.Mountpoint != now.Mountpoint {
			psu := publicStreamURL()
			slog.Info(fmt.Sprintf("Stream URL: %s", psu), "func", "icecastMonitor")
			radiodata_sse.SendEventMessage(psu, "listenurl", "")
		}
		if prev.Listeners != now.Listeners {
			radiodata_sse.SendEventMessage(fmt.Sprint(now.Listeners), "listeners", "")
		}
		prev = now
	}
	go func() {
		for {
			time.Sleep(1 * time.Second)
			check()
		}
	}()
}

var history = make([]playRecord, 0, 10)

type playRecord struct {
	Title  string
	Artist string
	Ended  time.Time
}
