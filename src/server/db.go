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
	"cover.jpg", "cover.png", "folder.jpg", "folder.png", "album.jpg", "album.png",
}

// cleanupMu guards titleCleanupRe and artistCleanupRe.
// resetCleanupRe() sets both to nil; ensureCleanupRe() rebuilds lazily.
// This pattern is safe for SIGHUP hot-reload (no sync.Once reassignment).
var cleanupMu      sync.RWMutex
var titleCleanupRe  []*regexp.Regexp
var artistCleanupRe []*regexp.Regexp

// resetCleanupRe clears compiled patterns so they are rebuilt on next use.
// Called by SIGHUP handler and AdminRescan.
func resetCleanupRe() {
	cleanupMu.Lock()
	titleCleanupRe = nil
	artistCleanupRe = nil
	cleanupMu.Unlock()
}

func ensureCleanupRe() {
	// Fast path
	cleanupMu.RLock()
	if titleCleanupRe != nil {
		cleanupMu.RUnlock()
		return
	}
	cleanupMu.RUnlock()

	// Slow path: build under write lock (double-checked)
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	if titleCleanupRe != nil {
		return
	}
	titleCleanupRe = compilePatterns(c.TitleCleanupPatterns)
	artistCleanupRe = compilePatterns(c.ArtistCleanupPatterns)
	slog.Info("Cleanup patterns compiled.",
		"title", len(titleCleanupRe), "artist", len(artistCleanupRe))
}

func compilePatterns(raw string) []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, pat := range strings.Split(raw, "|") {
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

// ArtworkPath returns the path of a cover image in the same directory as
// audioPath, or "" if none found.
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

// guessFromFilename tries to extract Artist - Title from the filename.
// Common patterns: "Artist - Title", "Artist_-_Title".
// Returns empty strings if no separator found.
func guessFromFilename(path string) (artist, title string) {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	// Normalise "_-_" separators used by some downloaders.
	base = strings.ReplaceAll(base, "_-_", " - ")
	parts := strings.SplitN(base, " - ", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return "", strings.TrimSpace(base)
}

func searchByQuery(query string) ([]SongData, error) {
	query = strings.TrimSpace(query)
	if c.DBBackend == "sqlite" {
		return sqliteSearchByQuery(query)
	}
	sel := fmt.Sprintf(
		`SELECT id, artist, title, album, genre, year FROM %s
		 WHERE artist ILIKE $1 OR title ILIKE $2
		 ORDER BY LEAST(levenshtein($3, artist), levenshtein($4, title))
		 LIMIT 200`,
		c.PostgresTableName)
	rows, err := dbp.Query(sel, "%"+query+"%", "%"+query+"%", query, query)
	if err != nil {
		slog.Error("searchByQuery failed.", "error", err)
		return nil, err
	}
	defer rows.Close()
	return scanSongs(rows)
}

func searchByTitleArtist(title, artist string) ([]SongData, error) {
	title = strings.TrimSpace(title)
	artist = strings.TrimSpace(artist)
	if c.DBBackend == "sqlite" {
		return sqliteSearchByTitleArtist(title, artist)
	}
	sel := fmt.Sprintf(
		`SELECT id, artist, title, album, genre, year FROM %s
		 WHERE title LIKE $1 AND artist LIKE $2 LIMIT 5`,
		c.PostgresTableName)
	rows, err := dbp.Query(sel, title, artist)
	if err != nil {
		slog.Error("searchByTitleArtist failed.", "error", err)
		return nil, err
	}
	defer rows.Close()
	return scanSongs(rows)
}

func getPathById(id int) (string, error) {
	table := c.PostgresTableName
	if c.DBBackend == "sqlite" {
		table = "metadata"
	}
	var path string
	err := dbActive.QueryRow(
		fmt.Sprintf(`SELECT path FROM %s WHERE id=$1`, table), id,
	).Scan(&path)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("song id %d not found", id)
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
	return results, rows.Err()
}

func dbPopulate() error {
	ensureCleanupRe()

	if c.MusicDir == "" {
		slog.Warn("CSERVER_MUSIC_DIR not set, skipping populate.")
		return nil
	}
	if _, err := os.Stat(c.MusicDir); err != nil {
		slog.Error("Music dir not accessible.", "dir", c.MusicDir, "error", err)
		return err
	}

	var files []string
	err := filepath.Walk(c.MusicDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Warn("Walk error.", "path", path, "error", err)
			return nil
		}
		if !info.IsDir() && audioExtensions[strings.ToLower(filepath.Ext(path))] {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return err
	}

	workers := c.ScanWorkers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	slog.Info(fmt.Sprintf("Scanning %d files, %d workers.", len(files), workers))

	fileCh := make(chan string, len(files))
	for _, f := range files {
		fileCh <- f
	}
	close(fileCh)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		scanned int
		skipped int
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range fileCh {
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
			}
		}()
	}
	wg.Wait()
	slog.Info(fmt.Sprintf("Scan done: %d ok, %d skipped.", scanned, skipped))
	return nil
}

// extractTags reads ID3/Vorbis/MP4 tags from path.
// Auto-repair: if title or artist are missing/generic, falls back to
// filename parsing ("Artist - Title" pattern) so yt-dlp downloads get
// correct metadata without manual tagging.
func extractTags(path string) (title, album, artist, genre, year string) {
	guessArtist, guessTitle := guessFromFilename(path)

	// Sane defaults from filename
	title = cleanTitle(guessTitle)
	artist = sanitize(guessArtist, "Unknown Artist")
	album = "Unknown Album"

	f, err := os.Open(path)
	if err != nil {
		slog.Warn("Cannot open for tag read.", "path", path)
		return
	}
	defer f.Close()

	tags, err := tag.ReadFrom(f)
	if err != nil {
		// No tags at all — filename guess is already set above.
		slog.Debug("No tags, using filename.", "path", path)
		return
	}

	// Use tag values; fall back to filename-derived values when tag is empty.
	if t := strings.TrimSpace(tags.Title()); t != "" {
		title = cleanTitle(t)
	}
	if a := strings.TrimSpace(tags.Artist()); a != "" {
		artist = cleanArtist(a)
	} else if guessArtist != "" {
		artist = cleanArtist(guessArtist)
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
