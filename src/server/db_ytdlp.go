// db_ytdlp.go — yt-dlp .info.json sidecar loader.
//
// Priority model (lowest → highest wins in extractTags):
//   filename guess < sidecar < embedded tag
//
// The sidecar fills fields that embedded tags leave empty or set to
// generic values (YouTube channel name as artist, playlist as album, etc.).
// It never overwrites a non-empty embedded tag value.

package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// ytdlpInfo holds the subset of yt-dlp .info.json fields useful for tagging.
// Fields are unmarshalled directly; missing keys stay as zero strings.
type ytdlpInfo struct {
	// Primary metadata
	Title  string `json:"title"`  // video/track title
	Track  string `json:"track"`  // "track" tag if set (often from music.youtube.com)
	Artist string `json:"artist"` // "artist" tag if set

	// Channel / uploader — last-resort artist source
	Uploader string `json:"uploader"` // channel display name
	Channel  string `json:"channel"`  // channel name (may differ from uploader)

	// Album metadata
	Album    string `json:"album"`    // "album" tag if set
	Playlist string `json:"playlist"` // playlist title — used as album fallback

	// Genre
	Genre string `json:"genre"`

	// Date fields — tried in order for year extraction
	ReleaseYear int    `json:"release_year"` // integer, e.g. 2023
	ReleaseDate string `json:"release_date"` // "YYYYMMDD" or "YYYY-MM-DD"
	UploadDate  string `json:"upload_date"`  // "YYYYMMDD" — least preferred
}

// BestTitle returns the most specific available title.
// Prefers `track` (set by music.youtube.com) over the generic video `title`.
func (y *ytdlpInfo) BestTitle() string {
	for _, v := range []string{y.Track, y.Title} {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// BestArtist returns the most specific available artist.
// Prefers `artist` tag → `uploader` → `channel`.
// Channel/uploader is a last resort; it is often the YouTube channel name
// rather than the actual artist and should be cleaned by cleanArtist().
func (y *ytdlpInfo) BestArtist() string {
	for _, v := range []string{y.Artist, y.Uploader, y.Channel} {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// BestAlbum returns the most specific available album.
// Prefers `album` tag; falls back to playlist title so grouped YouTube
// playlist downloads get a useful album field instead of "Unknown Album".
func (y *ytdlpInfo) BestAlbum() string {
	for _, v := range []string{y.Album, y.Playlist} {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// BestYear returns a 4-digit year string from the most reliable available field.
// Priority: release_year (integer) > release_date > upload_date.
func (y *ytdlpInfo) BestYear() string {
	if y.ReleaseYear > 0 {
		return fmt.Sprintf("%d", y.ReleaseYear)
	}
	for _, v := range []string{y.ReleaseDate, y.UploadDate} {
		s := strings.TrimSpace(v)
		// Dates arrive as "YYYYMMDD" or "YYYY-MM-DD"; grab first 4 chars.
		if len(s) >= 4 && isAllDigits(s[:4]) {
			return s[:4]
		}
	}
	return ""
}

// ytdlpInfoCandidates returns sidecar paths to try, in preference order.
// yt-dlp names the sidecar after the audio file with ".info.json" appended,
// but when the file was converted after download the original extension may
// still be part of the base name (e.g. "song.opus.info.json" → "song.info.json").
func ytdlpInfoCandidates(audioPath string) []string {
	dir := filepath.Dir(audioPath)
	base := strings.TrimSuffix(filepath.Base(audioPath), filepath.Ext(audioPath))
	return []string{
		filepath.Join(dir, base+".info.json"),
		// Residual double-extension sidecars from pre-conversion downloads:
		filepath.Join(dir, base+".opus.info.json"),
		filepath.Join(dir, base+".webm.info.json"),
		filepath.Join(dir, base+".m4a.info.json"),
		filepath.Join(dir, base+".mp3.info.json"),
	}
}

// loadYTDLPInfo tries to load a yt-dlp .info.json sidecar for audioPath.
// Returns nil if no sidecar exists or none can be parsed — callers must
// treat nil as "no sidecar available" and fall through to other sources.
func loadYTDLPInfo(audioPath string) *ytdlpInfo {
	for _, candidate := range ytdlpInfoCandidates(audioPath) {
		b, err := os.ReadFile(candidate)
		if err != nil {
			// File simply doesn't exist — not worth logging.
			continue
		}
		var info ytdlpInfo
		if err := json.Unmarshal(b, &info); err != nil {
			slog.Warn("yt-dlp info.json parse error, skipping.",
				"path", candidate, "error", err)
			continue
		}
		slog.Debug("Loaded yt-dlp sidecar.",
			"audio", filepath.Base(audioPath),
			"sidecar", filepath.Base(candidate),
		)
		return &info
	}
	return nil
}

// rawTagString extracts the first non-empty string value from a raw tag map
// for any of the given keys. Used to read non-standard Vorbis/ID3 frames
// that the dhowden/tag library does not surface via typed accessors.
func rawTagString(raw map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		v, ok := raw[k]
		if !ok {
			continue
		}
		s := strings.TrimSpace(fmt.Sprintf("%v", v))
		if s != "" && s != "<nil>" && s != "0" {
			return s
		}
	}
	return ""
}
