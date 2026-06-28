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
	".mp3":  true,
	".flac": true,
	".ogg":  true,
	".m4a":  true,
	".m4b":  true, // audiobook container, same as m4a
	".opus": true,
	".wav":  true,
	".aac":  true,
	".wma":  true,
	".aiff": true,
	".aif":  true,
	".ape":  true,
}

var artworkFallbackNames = []string{
	"cover.jpg", "cover.png", "folder.jpg", "folder.png",
	"album.jpg", "album.png", "front.jpg", "front.png",
	"thumb.jpg", "thumb.png", "thumbnail.jpg", "thumbnail.png",
}

var cleanupMu sync.RWMutex
var titleCleanupRe []*regexp.Regexp
var artistCleanupRe []*regexp.Regexp

// trackPrefixRe strips leading track numbers such as:
//
//	"01 - ", "02. ", "003 ", "1) "
var trackPrefixRe = regexp.MustCompile(`^\d{1,4}[\s.\-)]*[-–]?\s*`)

// ytdlpIDRe matches the YouTube video ID suffix that yt-dlp appends:
//		"--<11-char-id>."
//		"__<11-char-id>."
//		" (<11-char-id>)."
// The ID is always 11 base64url characters.
var ytdlpIDRe = regexp.MustCompile(`(?:--|__| \()[A-Za-z0-9_-]{11}\)?(?:\.[a-z0-9]+)?$`)

// ytdlpSepRe matches yt-dlp's triple-dash playlist/track separator:
//		"Playlist-Title---001-Track-Title"
var ytdlpSepRe = regexp.MustCompile(`-{3,}`)

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
	sep := c().PatternSeparator
	if sep == "" {
		sep = ";;"
	}
	titleCleanupRe = compilePatterns(c().TitleCleanupPatterns, sep)
	artistCleanupRe = compilePatterns(c().ArtistCleanupPatterns, sep)
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

// guessFromFilename extracts artist and title from the filename alone.
//
// Handles multiple naming conventions, in order of specificity:
//
//  1. yt-dlp playlist format:
//     "Playlist-Title---NNN-Track-Title--<ytID>.mp3"
//     → artist = playlist title (dashes→spaces), title = track title
//
//  2. "Artist - Title" (space-dash-space):
//     "Pink Floyd - Comfortably Numb.flac"
//
//  3. Bare dash separator (no spaces):
//     "ArtistName-TrackTitle.mp3" where both sides are non-numeric
//
//  4. Fallback: whole basename becomes the title, artist left empty.
func guessFromFilename(path string) (artist, title string) {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	// ── 1. yt-dlp playlist format ──────────────────────────────────────────
	// Strip trailing YouTube ID ("--uMvFjbDAFbU" or "__uMvFjbDAFbU").
	base = ytdlpIDRe.ReplaceAllString(base, "")
	base = strings.TrimRight(base, "-_ ")

	// Detect triple-dash separator: "Playlist---NNN-Track-Title"
	if ytdlpSepRe.MatchString(base) {
		parts := ytdlpSepRe.Split(base, 2)
		playlist := dashesToSpaces(parts[0])
		trackPart := ""
		if len(parts) == 2 {
			trackPart = parts[1]
			// Strip leading track number from the track part ("005-track-title" → "track-title")
			trackPart = trackPrefixRe.ReplaceAllString(trackPart, "")
			trackPart = strings.TrimLeft(trackPart, "-")
			trackPart = dashesToSpaces(trackPart)
		}
		if trackPart == "" {
			trackPart = playlist
			playlist = ""
		}
		// Title-case the result so "the night has its own bandwidth" → readable.
		return strings.TrimSpace(playlist), strings.TrimSpace(trackPart)
	}

	// Replace remaining separators for the simpler patterns below.
	base = strings.ReplaceAll(base, "_-_", " - ")
	base = strings.ReplaceAll(base, "_", " ")

	// Strip leading track-number prefix.
	stripped := trackPrefixRe.ReplaceAllString(base, "")

	// ── 2. "Artist - Title" (space-dash-space) ─────────────────────────────
	if parts := strings.SplitN(stripped, " - ", 2); len(parts) == 2 {
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])
		if !isAllDigits(left) && left != "" {
			return left, right
		}
		return "", right
	}

	// ── 3. Bare dash: "ArtistName-TrackTitle" ─────────────────────────────
	// Only split if both sides are non-empty and neither is all-digits.
	if parts := strings.SplitN(stripped, "-", 2); len(parts) == 2 {
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])
		if left != "" && right != "" && !isAllDigits(left) && !isAllDigits(right) {
			return left, right
		}
	}

	// ── 4. Fallback ────────────────────────────────────────────────────────
	return "", strings.TrimSpace(stripped)
}

// dashesToSpaces converts hyphen-separated-words to space separated words.
// Single dashes between words become spaces; runs of dashes become one space.
func dashesToSpaces(s string) string {
	// Replace any run of dashes/underscores with a single space.
	re := regexp.MustCompile(`[-_]+`)
	return strings.TrimSpace(re.ReplaceAllString(s, " "))
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
	slog.Debug("searchByQuery.", "query", query, "backend", c().DBBackend)
	if c().DBBackend == "sqlite" {
		return sqliteSearchByQuery(query)
	}
	sel := fmt.Sprintf(
		`SELECT id, artist, title, album, genre, year FROM %s
		 WHERE artist ILIKE $1 OR title ILIKE $2
		 ORDER BY LEAST(
		   levenshtein(lower($3), lower(artist)),
		   levenshtein(lower($4), lower(title))
		 ) LIMIT 200`,
		c().PostgresTableName)
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
	if c().DBBackend == "sqlite" {
		return sqliteSearchByTitleArtist(title, artist)
	}
	sel := fmt.Sprintf(
		`SELECT id, artist, title, album, genre, year FROM %s
		 WHERE title ILIKE $1 AND artist ILIKE $2 LIMIT 5`,
		c().PostgresTableName)
	rows, err := dbp.Query(sel, "%"+title+"%", "%"+artist+"%")
	if err != nil {
		slog.Error("searchByTitleArtist failed.", "title", title, "artist", artist, "error", err)
		return nil, err
	}
	defer rows.Close()
	return scanSongs(rows)
}

func getPathById(id int) (string, error) {
	var (
		query string
		arg   interface{} = id
	)
	if c().DBBackend == "sqlite" {
		query = `SELECT path FROM metadata WHERE id=?`
	} else {
		query = fmt.Sprintf(`SELECT path FROM %s WHERE id=$1`, c().PostgresTableName)
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

	if c().MusicDir == "" {
		slog.Warn("CSERVER_MUSIC_DIR not set, skipping populate.")
		return nil
	}
	if _, err := os.Stat(c().MusicDir); err != nil {
		slog.Error("Music dir not accessible.", "dir", c().MusicDir, "error", err)
		return fmt.Errorf("music dir %q not accessible: %w", c().MusicDir, err)
	}

	slog.Info("Walking music directory.", "dir", c().MusicDir)

	// seen tracks real paths to avoid double-scanning symlinked dirs.
	seen := make(map[string]struct{})
	var files []string
	err := filepath.Walk(c().MusicDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Warn("Walk error, skipping path.", "path", path, "error", err)
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !audioExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		// Resolve symlinks to deduplicate.
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			real = path // can't resolve; use as-is
		}
		if _, dup := seen[real]; dup {
			slog.Debug("Skipping duplicate (symlink).", "path", path, "real", real)
			return nil
		}
		seen[real] = struct{}{}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk error: %w", err)
	}

	total := len(files)
	workers := c().ScanWorkers
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
			if c().DBBackend == "sqlite" {
				upsertErr = sqliteUpsert(title, album, artist, genre, year, path)
			} else {
				upsertErr = postgresUpsert(title, album, artist, genre, year, path)
			}
			if upsertErr != nil {
				skipped.Add(1)
			} else {
				n := scanned.Add(1)
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

// extractTags resolves metadata for a single audio file.
//
// Priority (lowest → highest; each layer only fills empty slots):
//
//  1. filename guess        — always available, weakest signal
//  2. yt-dlp sidecar       — enriches fields the embedded tags left blank
//  3. embedded tags        — strongest signal; wins over sidecar when present
//
// The sidecar is intentionally loaded before opening the audio file so
// that the tag-library parse (step 3) always has the last word.
func extractTags(path string) (title, album, artist, genre, year string) {
	// ── Step 1: filename guess ───────────────────────────────────────────────────────
	guessArtist, guessTitle := guessFromFilename(path)
	basenameFallback := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	title = sanitize(cleanTitle(guessTitle), basenameFallback)
	artist = sanitize(cleanArtist(guessArtist), "Unknown Artist")
	album = "Unknown Album"
	genre = ""
	year = ""

	// ── Step 2: yt-dlp sidecar ────────────────────────────────────────────────────
	if info := loadYTDLPInfo(path); info != nil {
		if s := sanitize(cleanTitle(info.BestTitle()), ""); s != "" {
			title = s
		}
		if s := sanitize(cleanArtist(info.BestArtist()), ""); s != "" {
			artist = s
		}
		if s := sanitize(info.BestAlbum(), ""); s != "" {
			album = s
		}
		if s := sanitize(info.Genre, ""); s != "" {
			genre = s
		}
		if s := info.BestYear(); s != "" {
			year = s
		}
	}

	// ── Step 3: embedded tags ─────────────────────────────────────────────────────
	f, err := os.Open(path)
	if err != nil {
		slog.Warn("Cannot open file for tag read.", "path", path, "error", err)
		return
	}
	defer f.Close()

	tags, err := tag.ReadFrom(f)
	if err != nil {
		// No embedded tags — filename + sidecar values stand.
		slog.Debug("No embedded tags; using filename/sidecar values.",
			"path", path, "reason", err)
		return
	}

	// Title: embedded tag beats everything.
	if raw := sanitize(tags.Title(), ""); raw != "" {
		cleaned := cleanTitle(raw)
		// Guard: if cleanup strips the whole title (e.g. tag is just
		// "(Official Video)"), keep the original uncleaned value.
		title = sanitize(cleaned, raw)
	}

	// Artist: embedded TPE1 > raw non-standard frames (album artist / TPE2) > sidecar > filename.
	if raw := sanitize(tags.Artist(), ""); raw != "" {
		cleaned := cleanArtist(raw)
		artist = sanitize(cleaned, raw)
	} else if rawMap := tags.Raw(); rawMap != nil {
		// Only check album-artist frames — not ARTIST/artist which tags.Artist()
		// already tried. This avoids clobbering the sidecar artist with
		// "Various Artists" from compilation tags unnecessarily.
		if s := sanitize(cleanArtist(rawTagString(rawMap,
			"album_artist", "ALBUM_ARTIST",
			"TPE2",
		)), ""); s != "" {
			artist = s
		}
	}

	// Album: embedded tag beats sidecar (playlist name).
	if raw := sanitize(tags.Album(), ""); raw != "" {
		album = raw
	}

	// Genre: embedded tag beats sidecar.
	if raw := sanitize(tags.Genre(), ""); raw != "" {
		genre = raw
	}

	// Year: prefer embedded numeric year; fall through raw map; sidecar fills gap.
	if y := tags.Year(); y > 0 {
		year = fmt.Sprintf("%d", y)
	} else if rawMap := tags.Raw(); rawMap != nil {
		if s := extractRawYear(rawMap); s != "" {
			year = s
		}
	}

	return
}

// extractRawYear looks for common year tag keys in the raw tag map.
// Handles Vorbis (YEAR / DATE), ID3v2 (TDRC / TYER), MP4 (©day), ASF (WM/Year).
func extractRawYear(raw map[string]interface{}) string {
	candidates := []string{
		"YEAR", "year",
		"DATE", "date",
		"TDRC", "tdrc", // ID3v2.4 recording time (may be "YYYY-MM-DD")
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
		// Timestamps like "2023-07-15" or "20230715" — take first 4 chars.
		if len(s) >= 4 {
			s = s[:4]
		}
		// Require exactly 4 digits to avoid storing malformed partials.
		if len(s) == 4 && isAllDigits(s) {
			return s
		}
	}
	return ""
}
