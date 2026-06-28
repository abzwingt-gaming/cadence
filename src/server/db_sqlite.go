// db_sqlite.go
// SQLite metadata database. Used when CSERVER_DB_BACKEND=sqlite.
// Uses modernc.org/sqlite (pure Go, no CGO required).

package main

import (
	"database/sql"
	"fmt"
	"log/slog"

	_ "modernc.org/sqlite"
)

var dbs *sql.DB

func sqliteInit() error {
	var err error
	// WAL mode: supports multiple concurrent readers + one writer.
	// busy_timeout=5000ms: readers/writers wait instead of returning SQLITE_BUSY.
	// synchronous=NORMAL: safe under WAL (crash won't corrupt), faster than FULL.
	// foreign_keys=on: referential integrity.
	dsn := fmt.Sprintf(
		"%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(on)",
		c.SQLitePath,
	)
	slog.Info("Opening SQLite.", "path", c.SQLitePath)
	dbs, err = sql.Open("sqlite", dsn)
	if err != nil {
		slog.Error("Cannot open SQLite.", "path", c.SQLitePath, "error", err)
		return err
	}
	// WAL mode allows multiple concurrent readers — do not cap to 1.
	// Write serialisation is handled by SQLite's internal locking +
	// busy_timeout. Capping to 1 would serialise reads during scan, hurting
	// search latency.
	dbs.SetMaxOpenConns(0) // unlimited (Go default)
	if err = dbs.Ping(); err != nil {
		slog.Error("Cannot ping SQLite.", "path", c.SQLitePath, "error", err)
		return err
	}
	_, err = dbs.Exec(`
		CREATE TABLE IF NOT EXISTS metadata (
			id     INTEGER PRIMARY KEY AUTOINCREMENT,
			title  TEXT,
			album  TEXT,
			artist TEXT,
			genre  TEXT,
			year   TEXT,
			path   TEXT UNIQUE
		)`)
	if err != nil {
		slog.Error("Failed to create SQLite metadata table.", "error", err)
		return err
	}
	slog.Info("SQLite ready.", "path", c.SQLitePath)
	dbActive = dbs
	return nil
}

func sqliteUpsert(title, album, artist, genre, year, path string) error {
	_, err := dbs.Exec(`
		INSERT INTO metadata (title, album, artist, genre, year, path)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE
		  SET title=excluded.title, album=excluded.album,
		      artist=excluded.artist, genre=excluded.genre, year=excluded.year`,
		title, album, artist, genre, year, path)
	if err != nil {
		slog.Warn("SQLite upsert failed.", "path", path, "error", err)
	}
	return err
}

func sqliteSearchByQuery(query string) ([]SongData, error) {
	slog.Debug("sqliteSearchByQuery.", "query", query)
	rows, err := dbs.Query(`
		SELECT id, artist, title, album, genre, year FROM metadata
		WHERE artist LIKE ? OR title LIKE ?
		ORDER BY
		  CASE WHEN title  LIKE ? THEN 0 ELSE 1 END,
		  CASE WHEN artist LIKE ? THEN 0 ELSE 1 END
		LIMIT 200`,
		"%"+query+"%", "%"+query+"%", query+"%", query+"%")
	if err != nil {
		slog.Error("sqliteSearchByQuery failed.", "query", query, "error", err)
		return nil, err
	}
	defer rows.Close()
	return scanSongs(rows)
}

// sqliteSearchByTitleArtist uses case-insensitive LIKE with wildcards for
// parity with the Postgres ILIKE implementation.
// Bare LIKE without % is exact match — Icecast metadata often differs
// slightly from stored tags, so substring matching is required.
func sqliteSearchByTitleArtist(title, artist string) ([]SongData, error) {
	slog.Debug("sqliteSearchByTitleArtist.", "title", title, "artist", artist)
	rows, err := dbs.Query(
		`SELECT id, artist, title, album, genre, year FROM metadata
		 WHERE title LIKE ? AND artist LIKE ? LIMIT 5`,
		"%"+title+"%", "%"+artist+"%")
	if err != nil {
		slog.Error("sqliteSearchByTitleArtist failed.", "title", title, "artist", artist, "error", err)
		return nil, err
	}
	defer rows.Close()
	return scanSongs(rows)
}
