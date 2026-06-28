package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

type ytdlpInfo struct {
	Title       string `json:"title"`
	Track       string `json:"track"`
	Artist      string `json:"artist"`
	Uploader    string `json:"uploader"`
	Channel     string `json:"channel"`
	Album       string `json:"album"`
	Genre       string `json:"genre"`
	ReleaseYear string `json:"release_year"`
	ReleaseDate string `json:"release_date"`
	UploadDate  string `json:"upload_date"`
	Playlist    string `json:"playlist"`
	Description string `json:"description"`
}

func (y *ytdlpInfo) BestTitle() string {
	for _, v := range []string{y.Track, y.Title} {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func (y *ytdlpInfo) BestArtist() string {
	for _, v := range []string{y.Artist, y.Uploader, y.Channel} {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func (y *ytdlpInfo) ReleaseYear() string {
	for _, v := range []string{y.ReleaseYear, y.ReleaseDate, y.UploadDate} {
		s := strings.TrimSpace(v)
		if len(s) >= 4 && isAllDigits(s[:4]) {
			return s[:4]
		}
	}
	return ""
}

func ytdlpInfoCandidates(audioPath string) []string {
	dir := filepath.Dir(audioPath)
	base := strings.TrimSuffix(filepath.Base(audioPath), filepath.Ext(audioPath))
	return []string{
		filepath.Join(dir, base+".info.json"),
		filepath.Join(dir, base+".webm.info.json"),
		filepath.Join(dir, base+".m4a.info.json"),
		filepath.Join(dir, base+".mp3.info.json"),
		filepath.Join(dir, base+".opus.info.json"),
	}
}

func loadYTDLPInfo(audioPath string) *ytdlpInfo {
	for _, candidate := range ytdlpInfoCandidates(audioPath) {
		b, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		var info ytdlpInfo
		if err := json.Unmarshal(b, &info); err != nil {
			slog.Debug("Invalid yt-dlp info.json, skipping.", "path", candidate, "error", err)
			continue
		}
		slog.Debug("Loaded yt-dlp info.json.", "audio", audioPath, "info", candidate)
		return &info
	}
	return nil
}

func rawString(raw map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		v, ok := raw[k]
		if !ok {
			continue
		}
		s := strings.TrimSpace(strings.TrimSpace(strings.Trim(strings.TrimSpace(strings.Trim(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.Trim(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(strings.TrimSpace(fmt.Sprintf("%v", v)), "[]")), "[]"))
		if s != "" && s != "<nil>" && s != "0" {
			return s
		}
	}
	return ""
}
