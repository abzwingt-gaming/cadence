// db_sqlite.go
// SQLite metadata database. Used when CSERVER_DB_BACKEND=sqlite.
// Uses modernc.org/sqlite (pure Go, no CGO required).

package main

import (
	"database/sql"
	"log/slog"

	_ "modernc.org/sqlite"
)

var dbs *sql.DB

func sqliteInit() error {
	var err error
	dbs, err = sql.Open("sqlite", c.SQLitePath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		slog.Error("Couldn't open SQLite.", "path", c.SQLitePath, "error", err)
		return err
	}
	if err = dbs.Ping(); err != nil {
		slog.Error("Couldn't ping SQLite.", "error", err)
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
	return err
}

func sqliteSearchByQuery(query string) ([]SongData, error) {
	rows, err := dbs.Query(`
		SELECT id, artist, title, album, genre, year FROM metadata
		WHERE artist LIKE ? OR title LIKE ?
		ORDER BY
		  CASE WHEN title  LIKE ? THEN 0 ELSE 1 END,
		  CASE WHEN artist LIKE ? THEN 0 ELSE 1 END
		LIMIT 200`,
		"%"+query+"%", "%"+query+"%", query+"%", query+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSongRows(rows)
}

func sqliteSearchByTitleArtist(title, artist string) ([]SongData, error) {
	rows, err := dbs.Query(
		`SELECT id, artist, title, album, genre, year FROM metadata WHERE title=? AND artist=? LIMIT 5`,
		title, artist)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSongRows(rows)
}

func scanSongRows(rows *sql.Rows) ([]SongData, error) {
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
