// db.go - DB helpers, tag extraction, title/artist normalisation, artwork fallback.

package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/dhowden/tag"
)

var dbActive *sql.DB

var audioExtensions = map[string]bool{
	".mp3": true, ".flac": true, ".ogg": true,
	".m4a": true, ".opus": true, ".wav": true, ".aac": true,
}

var artworkFallbackNames = []string{
	"cover.jpg", "cover.png", "folder.jpg", "folder.png",
	"album.jpg", "album.png", "front.jpg", "front.png",
}

var cleanupMu      sync.RWMutex
var titleCleanupRe  []*regexp.Regexp
var artistCleanupRe []*regexp.Regexp

func resetCleanupRe() {
	cleanupMu.Lock()
	titleCleanupRe = nil
	artistCleanupRe = nil
	cleanupMu.Unlock()
	slog.Debug("Cleanup patterns cleared; will recompile on next scan.")
}

func ensureCleanupRe() {
	cleanupMu.RLock()
	if titleCleanupRe != nil {
		cleanupMu.RUnlock()
		return
	}
	cleanupMu.RUnlock()

	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	if titleCleanupRe != nil {
		return
	}
	sep := c.PatternSeparator
	if sep == "" {
		sep = ";;"
	}
	titleCleanupRe = compilePatterns(c.TitleCleanupPatterns, sep)
	artistCleanupRe = compilePatterns(c.ArtistCleanupPatterns, sep)
	slog.Info("Cleanup patterns compiled.",
		"title_patterns", len(titleCleanupRe),
		"artist_patterns", len(artistCleanupRe),
		"separator", sep,
	)
}

func compilePatterns(raw, sep string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, pat := range strings.Split(raw, sep) {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			slog.Warn("Bad cleanup pattern, skipping.", "pattern", pat, "error", err)
			continue
		}
		out = append(out, re)
	}
	return out
}

func applyRe(s string, res []*regexp.Regexp) string {
	for _, re := range res {
		s = re.ReplaceAllString(s, "")
	}
	return strings.TrimSpace(s)
}

func cleanTitle(s string) string {
	cleanupMu.RLock()
	res := titleCleanupRe
	cleanupMu.RUnlock()
	return applyRe(s, res)
}

func cleanArtist(s string) string {
	cleanupMu.RLock()
	res := artistCleanupRe
	cleanupMu.RUnlock()
	return applyRe(s, res)
}

func sanitize(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	return s
}

// ArtworkPath returns the first matching cover image in the same directory
// as audioPath, or "" if none found.
func ArtworkPath(audioPath string) string {
	dir := filepath.Dir(audioPath)
	for _, name := range artworkFallbackNames {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// guessFromFilename extracts artist and title from "Artist - Title" patterns.
func guessFromFilename(path string) (artist, title string) {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = strings.ReplaceAll(base, "_-_", " - ")
	parts := strings.SplitN(base, " - ", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return "", strings.TrimSpace(base)
}

func searchByQuery(query string) ([]SongData, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty search query")
	}
	slog.Debug("searchByQuery.", "query", query, "backend", c.DBBackend)
	if c.DBBackend == "sqlite" {
		return sqliteSearchByQuery(query)
	}
	sel := fmt.Sprintf(
		`SELECT id, artist, title, album, genre, year FROM %s
		 WHERE artist ILIKE $1 OR title ILIKE $2
		 ORDER BY LEAST(
		   levenshtein(lower($3), lower(artist)),
		   levenshtein(lower($4), lower(title))
		 ) LIMIT 200`,
		c.PostgresTableName)
	rows, err := dbp.Query(sel, "%"+query+"%", "%"+query+"%", query, query)
	if err != nil {
		slog.Error("searchByQuery failed.", "query", query, "error", err)
		return nil, err
	}
	defer rows.Close()
	return scanSongs(rows)
}

func searchByTitleArtist(title, artist string) ([]SongData, error) {
	title = strings.TrimSpace(title)
	artist = strings.TrimSpace(artist)
	if title == "" {
		return nil, fmt.Errorf("empty title in searchByTitleArtist")
	}
	slog.Debug("searchByTitleArtist.", "title", title, "artist", artist)
	if c.DBBackend == "sqlite" {
		return sqliteSearchByTitleArtist(title, artist)
	}
	// Use ILIKE with wildcards for fuzzy matching — Icecast metadata may
	// include extra punctuation or spacing not present in the DB tags.
	sel := fmt.Sprintf(
		`SELECT id, artist, title, album, genre, year FROM %s
		 WHERE title ILIKE $1 AND artist ILIKE $2 LIMIT 5`,
		c.PostgresTableName)
	rows, err := dbp.Query(sel, "%"+title+"%", "%"+artist+"%")
	if err != nil {
		slog.Error("searchByTitleArtist failed.", "title", title, "artist", artist, "error", err)
		return nil, err
	}
	defer rows.Close()
	return scanSongs(rows)
}

// getPathById fetches the file path for a song ID.
// Uses correct placeholder syntax per backend (? for SQLite, $1 for Postgres).
func getPathById(id int) (string, error) {
	var (
		query string
		arg   interface{} = id
	)
	if c.DBBackend == "sqlite" {
		query = `SELECT path FROM metadata WHERE id=?`
	} else {
		query = fmt.Sprintf(`SELECT path FROM %s WHERE id=$1`, c.PostgresTableName)
	}
	var path string
	err := dbActive.QueryRow(query, arg).Scan(&path)
	if err == sql.ErrNoRows {
		slog.Warn("Song not found by id.", "id", id)
		return "", fmt.Errorf("song id %d not found", id)
	}
	if err != nil {
		slog.Error("getPathById query error.", "id", id, "error", err)
	}
	return path, err
}

func scanSongs(rows *sql.Rows) ([]SongData, error) {
	var results []SongData
	for rows.Next() {
		var s SongData
		if err := rows.Scan(&s.ID, &s.Artist, &s.Title, &s.Album, &s.Genre, &s.Year); err != nil {
			slog.Warn("Row scan error, skipping.", "error", err)
			continue
		}
		results = append(results, s)
	}
	if err := rows.Err(); err != nil {
		slog.Error("Rows iteration error.", "error", err)
		return results, err
	}
	return results, nil
}

func dbPopulate() error {
	ensureCleanupRe()

	if c.MusicDir == "" {
		slog.Warn("CSERVER_MUSIC_DIR not set, skipping populate.")
		return nil
	}
	if _, err := os.Stat(c.MusicDir); err != nil {
		slog.Error("Music dir not accessible.", "dir", c.MusicDir, "error", err)
		return fmt.Errorf("music dir %q not accessible: %w", c.MusicDir, err)
	}

	slog.Info("Walking music directory.", "dir", c.MusicDir)
	var files []string
	err := filepath.Walk(c.MusicDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Warn("Walk error, skipping path.", "path", path, "error", err)
			return nil
		}
		if !info.IsDir() && audioExtensions[strings.ToLower(filepath.Ext(path))] {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk error: %w", err)
	}

	workers := c.ScanWorkers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	slog.Info("Starting scan.", "files", len(files), "workers", workers)

	sem := make(chan struct{}, workers)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		scanned int
		skipped int
	)
	for _, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(path string) {
			defer wg.Done()
			defer func() { <-sem }()
			title, album, artist, genre, year := extractTags(path)
			var upsertErr error
			if c.DBBackend == "sqlite" {
				upsertErr = sqliteUpsert(title, album, artist, genre, year, path)
			} else {
				upsertErr = postgresUpsert(title, album, artist, genre, year, path)
			}
			mu.Lock()
			if upsertErr != nil {
				skipped++
			} else {
				scanned++
			}
			mu.Unlock()
		}(f)
	}
	wg.Wait()
	slog.Info("Scan complete.", "scanned", scanned, "skipped", skipped, "total", len(files))
	return nil
}

func extractTags(path string) (title, album, artist, genre, year string) {
	guessArtist, guessTitle := guessFromFilename(path)

	// Sanitize the filename-guessed title with a fallback to the bare filename
	// so cleanup regexes that strip everything don't leave an empty title.
	title = sanitize(cleanTitle(guessTitle), filepath.Base(path))
	artist = sanitize(guessArtist, "Unknown Artist")
	album = "Unknown Album"

	f, err := os.Open(path)
	if err != nil {
		slog.Warn("Cannot open file for tag read.", "path", path, "error", err)
		return
	}
	defer f.Close()

	tags, err := tag.ReadFrom(f)
	if err != nil {
		slog.Debug("No tags found, using filename fallback.", "path", path)
		return
	}

	if t := strings.TrimSpace(tags.Title()); t != "" {
		// Sanitize cleaned tag title too — fallback to raw tag if cleanup
		// strips everything (e.g. title is literally "(Official Video)").
		title = sanitize(cleanTitle(t), t)
	}
	if a := strings.TrimSpace(tags.Artist()); a != "" {
		artist = cleanArtist(a)
	} else if guessArtist != "" {
		artist = cleanArtist(guessArtist)
		slog.Debug("Artist tag empty, using filename guess.", "path", path, "artist", artist)
	}
	if al := strings.TrimSpace(tags.Album()); al != "" {
		album = al
	}
	genre = sanitize(tags.Genre(), "")
	if y := tags.Year(); y > 0 {
		year = fmt.Sprintf("%d", y)
	}
	return
}
