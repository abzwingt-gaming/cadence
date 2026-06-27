// db.go - backend-agnostic DB helpers + tag extraction with title normalisation.

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

// compiled title cleanup patterns, built once at first populate.
var titleCleanupRe []*regexp.Regexp
var titleCleanupOnce sync.Once

func buildTitleCleanupRe() {
	titleCleanupOnce.Do(func() {
		for _, pat := range strings.Split(c.TitleCleanupPatterns, "|") {
			pat = strings.TrimSpace(pat)
			if pat == "" {
				continue
			}
			re, err := regexp.Compile(pat)
			if err != nil {
				slog.Warn("Bad title cleanup pattern, skipping.", "pattern", pat, "error", err)
				continue
			}
			titleCleanupRe = append(titleCleanupRe, re)
		}
		slog.Info(fmt.Sprintf("Title cleanup: %d patterns loaded.", len(titleCleanupRe)))
	})
}

// cleanTitle strips common yt-dlp suffixes and trims the result.
func cleanTitle(s string) string {
	for _, re := range titleCleanupRe {
		s = re.ReplaceAllString(s, "")
	}
	return strings.TrimSpace(s)
}

func sanitize(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	return s
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
	title  = strings.TrimSpace(title)
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
	buildTitleCleanupRe()

	if c.MusicDir == "" {
		slog.Warn("CSERVER_MUSIC_DIR not set, skipping populate.")
		return nil
	}
	if _, err := os.Stat(c.MusicDir); err != nil {
		slog.Error("Music directory not accessible.", "dir", c.MusicDir, "error", err)
		return err
	}

	var files []string
	err := filepath.Walk(c.MusicDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Warn("Walk error, skipping.", "path", path, "error", err)
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
	slog.Info(fmt.Sprintf("Scanning %d files with %d workers...", len(files), workers))

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

// extractTags reads ID3/Vorbis/MP4 tags. Always returns usable strings.
// Applies title normalisation to clean yt-dlp-style suffixes.
func extractTags(path string) (title, album, artist, genre, year string) {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	title  = base
	artist = "Unknown Artist"
	album  = "Unknown Album"

	f, err := os.Open(path)
	if err != nil {
		slog.Warn("Cannot open for tag read.", "path", path)
		return
	}
	defer f.Close()

	tags, err := tag.ReadFrom(f)
	if err != nil {
		slog.Debug("Tag read failed, using filename.", "path", path)
		title = cleanTitle(base)
		return
	}

	title  = cleanTitle(sanitize(tags.Title(),  base))
	artist = sanitize(tags.Artist(), "Unknown Artist")
	album  = sanitize(tags.Album(),  "Unknown Album")
	genre  = sanitize(tags.Genre(),  "")
	if y := tags.Year(); y > 0 {
		year = fmt.Sprintf("%d", y)
	}
	return
}
