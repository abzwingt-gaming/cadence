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
	"sync/atomic"
	"unicode"

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

var cleanupMu sync.RWMutex
var titleCleanupRe []*regexp.Regexp
var artistCleanupRe []*regexp.Regexp

// trackPrefixRe strips leading track numbers such as:
//
//	"01 - ", "02. ", "003 ", "1) "
var trackPrefixRe = regexp.MustCompile(`^\d{1,4}[\s.\-)]*[-–]?\s*`)

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

// sanitize trims whitespace and control characters from s.
// Returns fallback when s is empty after trimming.
func sanitize(s, fallback string) string {
	// Strip null bytes and other C0 control chars that ID3v1 tags can contain.
	s = strings.Map(func(r rune) rune {
		if r == 0 || (unicode.IsControl(r) && r != '\t' && r != '\n') {
			return -1
		}
		return r
	}, s)
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
// It first strips common leading track-number prefixes from the base name,
// and only treats the left side as an artist when it looks like one
// (non-numeric, at least 2 chars).
func guessFromFilename(path string) (artist, title string) {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = strings.ReplaceAll(base, "_-_", " - ")
	base = strings.ReplaceAll(base, "_", " ")

	// Strip track-number prefix before attempting to split.
	stripped := trackPrefixRe.ReplaceAllString(base, "")

	parts := strings.SplitN(stripped, " - ", 2)
	if len(parts) == 2 {
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])
		// Only treat left as artist if it's not purely numeric.
		if !isAllDigits(left) && left != "" {
			return left, right
		}
		return "", right
	}
	return "", strings.TrimSpace(stripped)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
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

	total := len(files)
	workers := c.ScanWorkers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	slog.Info("Starting scan.", "files", total, "workers", workers)

	sem := make(chan struct{}, workers)
	var (
		wg      sync.WaitGroup
		scanned atomic.Int64
		skipped atomic.Int64
	)
	for _, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(path string) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("Panic in scan worker, skipping file.",
						"path", path, "recover", r)
					skipped.Add(1)
				}
			}()

			title, album, artist, genre, year := extractTags(path)
			var upsertErr error
			if c.DBBackend == "sqlite" {
				upsertErr = sqliteUpsert(title, album, artist, genre, year, path)
			} else {
				upsertErr = postgresUpsert(title, album, artist, genre, year, path)
			}
			if upsertErr != nil {
				skipped.Add(1)
			} else {
				n := scanned.Add(1)
				// Progress log every 500 files.
				if n%500 == 0 {
					slog.Info("Scan progress.",
						"scanned", n,
						"skipped", skipped.Load(),
						"total", total,
					)
				}
			}
		}(f)
	}
	wg.Wait()
	slog.Info("Scan complete.",
		"scanned", scanned.Load(),
		"skipped", skipped.Load(),
		"total", total,
	)
	return nil
}

// extractTags reads ID3/Vorbis/MP4 tags from the audio file at path.
// All error paths fall back gracefully; a panic in the tag library is
// caught by the caller's recover() in dbPopulate.
func extractTags(path string) (title, album, artist, genre, year string) {
	// --- Step 1: filename-based defaults (always present) ---
	guessArtist, guessTitle := guessFromFilename(path)

	// Apply cleanup regexes to guessed values; fall back to the raw basename
	// so we never store an empty title.
	basenameFallback := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	title = sanitize(cleanTitle(guessTitle), basenameFallback)
	artist = sanitize(cleanArtist(guessArtist), "Unknown Artist")
	album = "Unknown Album"
	genre = ""
	year = ""

	// --- Step 2: open file ---
	f, err := os.Open(path)
	if err != nil {
		slog.Warn("Cannot open file for tag read.", "path", path, "error", err)
		return
	}
	defer f.Close()

	// --- Step 3: parse tags ---
	tags, err := tag.ReadFrom(f)
	if err != nil {
		// Not an error — many valid audio files have no embedded tags.
		slog.Debug("No tags found, using filename fallback.", "path", path, "reason", err)
		return
	}

	// --- Step 4: Title ---
	// Priority: tag > cleaned tag (if not empty after cleanup) > filename guess.
	if rawTitle := sanitize(tags.Title(), ""); rawTitle != "" {
		cleaned := cleanTitle(rawTitle)
		// If cleanup stripped everything (e.g. title is purely "(Official Video)"),
		// keep the original uncleaned tag value so the title is never empty.
		title = sanitize(cleaned, rawTitle)
	}

	// --- Step 5: Artist ---
	// Priority: tag > cleaned tag (non-empty after cleanup) > filename guess > "Unknown Artist".
	if rawArtist := sanitize(tags.Artist(), ""); rawArtist != "" {
		cleaned := cleanArtist(rawArtist)
		// Guard: if cleanup produced an empty string, fall back to the raw tag.
		artist = sanitize(cleaned, rawArtist)
	} else if guessArtist != "" {
		cleaned := cleanArtist(guessArtist)
		artist = sanitize(cleaned, guessArtist)
		slog.Debug("Artist tag empty, using filename guess.", "path", path, "artist", artist)
	}
	// artist was already initialised to "Unknown Artist" from step 1,
	// so if both tag and guess are empty it stays as the fallback.

	// --- Step 6: Album ---
	if rawAlbum := sanitize(tags.Album(), ""); rawAlbum != "" {
		album = rawAlbum
	}

	// --- Step 7: Genre ---
	// sanitize strips null bytes that ID3v1 genre fields often contain.
	genre = sanitize(tags.Genre(), "")

	// --- Step 8: Year ---
	// tag.Year() returns 0 for formats that store year as a string tag
	// (Vorbis YEAR, MP4 ©day, etc.). Try the numeric field first, then
	// fall back to the raw string via tag.Raw().
	if y := tags.Year(); y > 0 {
		year = fmt.Sprintf("%d", y)
	} else if raw := tags.Raw(); raw != nil {
		year = extractRawYear(raw)
	}

	return
}

// extractRawYear looks for common year tag keys in the raw tag map returned
// by tag.Metadata.Raw(). Handles Vorbis (YEAR / DATE), ID3v2 (TDRC / TYER),
// MP4 (©day), and ASF (WM/Year).
func extractRawYear(raw map[string]interface{}) string {
	candidates := []string{
		"YEAR", "year",
		"DATE", "date",
		"TDRC", "tdrc", // ID3v2.4 recording time
		"TYER", "tyer", // ID3v2.3 year
		"©day",    // MP4 / iTunes
		"WM/Year", // ASF / WMA
	}
	for _, k := range candidates {
		v, ok := raw[k]
		if !ok {
			continue
		}
		s := strings.TrimSpace(fmt.Sprintf("%v", v))
		if s == "" || s == "0" || s == "<nil>" {
			continue
		}
		// Use only the 4-digit year portion (e.g. "2023-04-01" → "2023").
		if len(s) >= 4 {
			s = s[:4]
		}
		if isAllDigits(s) {
			return s
		}
	}
	return ""
}
