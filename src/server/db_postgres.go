// db_postgres.go
// Metadata database configuration and population.

package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dhowden/tag"
	"github.com/lib/pq"
)

var dbp *sql.DB

func postgresInit() error {
	dsn := fmt.Sprintf("host='%s' port='%s' user='%s' password='%s' dbname='%s' sslmode='%s'",
		c.PostgresAddress, c.PostgresPort, c.PostgresUser, c.PostgresPassword, c.PostgresDBName, c.PostgresSSL)

	// Retry up to 10 times with 3s delay instead of blind sleep
	var err error
	for i := 1; i <= 10; i++ {
		dbp, err = sql.Open("postgres", dsn)
		if err != nil {
			slog.Warn(fmt.Sprintf("DB open failed (attempt %d/10): %v", i, err), "func", "postgresInit")
			time.Sleep(3 * time.Second)
			continue
		}
		err = dbp.Ping()
		if err != nil {
			slog.Warn(fmt.Sprintf("DB ping failed (attempt %d/10): %v", i, err), "func", "postgresInit")
			time.Sleep(3 * time.Second)
			continue
		}
		break
	}
	if err != nil {
		slog.Error("Could not connect to Postgres after 10 attempts.", "func", "postgresInit", "error", err)
		return err
	}

	// Enable fuzzystrmatch for levenshtein-based search ranking.
	_, err = dbp.Exec("CREATE EXTENSION IF NOT EXISTS fuzzystrmatch")
	if err != nil {
		// Some managed Postgres instances block this; degrade gracefully
		slog.Warn("Could not enable fuzzystrmatch. Fuzzy search may be degraded.", "func", "postgresInit", "error", err)
	}
	slog.Info("Postgres connected.", "func", "postgresInit")
	return nil
}

func postgresPopulate() error {
	// Use DROP TABLE / CREATE TABLE - never DROP DATABASE.
	// Dropping the database from within the same connection is not supported
	// in Postgres and causes: 'ERROR: cannot drop the currently open database'.
	dropTable := fmt.Sprintf("DROP TABLE IF EXISTS %s", c.PostgresTableName)
	createTable := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id     SERIAL PRIMARY KEY,
			title  VARCHAR(255),
			album  VARCHAR(255),
			artist VARCHAR(255),
			genre  VARCHAR(255),
			year   VARCHAR(4),
			path   VARCHAR(510)
		)`, c.PostgresTableName)

	slog.Debug(fmt.Sprintf("Dropping table <%s>...", c.PostgresTableName), "func", "postgresPopulate")
	_, err := dbp.Exec(dropTable)
	if err != nil {
		slog.Error("Failed to drop table.", "func", "postgresPopulate", "error", err)
		return err
	}

	slog.Debug(fmt.Sprintf("Creating table <%s>...", c.PostgresTableName), "func", "postgresPopulate")
	_, err = dbp.Exec(createTable)
	if err != nil {
		pqErr, ok := err.(*pq.Error)
		if ok && pqErr.Code == "42P07" {
			slog.Info("Metadata table already exists.", "func", "postgresPopulate")
		} else {
			slog.Error("Failed to create table.", "func", "postgresPopulate", "error", err)
			return err
		}
	}

	_, err = os.Stat(c.MusicDir)
	if err != nil {
		slog.Error(fmt.Sprintf("Music directory <%s> not accessible.", c.MusicDir), "func", "postgresPopulate", "error", err)
		return err
	}

	insertInto := fmt.Sprintf(
		"INSERT INTO %s (title, album, artist, genre, year, path) VALUES ($1, $2, $3, $4, $5, $6)",
		c.PostgresTableName)

	// Supported extensions
	extensions := map[string]bool{
		".mp3":  true,
		".flac": true,
		".ogg":  true,
		".m4a":  true,
		".opus": true,
		".aac":  true,
	}

	var scanned, inserted, skipped int
	slog.Info(fmt.Sprintf("Scanning music directory: %s", c.MusicDir), "func", "postgresPopulate")

	err = filepath.Walk(c.MusicDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Warn("Walk error, skipping path.", "func", "postgresPopulate", "path", path, "error", err)
			return nil // continue walk
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !extensions[ext] {
			return nil
		}
		scanned++

		file, err := os.Open(path)
		if err != nil {
			slog.Warn("Could not open file, skipping.", "func", "postgresPopulate", "path", path, "error", err)
			skipped++
			return nil // continue walk, do not abort
		}
		defer file.Close()

		// Read tags - use fallback values if tags missing or corrupt
		title, album, artist, genre, year := "", "", "", "", ""
		tags, err := tag.ReadFrom(file)
		if err != nil {
			slog.Warn("Could not read tags, using filename as title.", "func", "postgresPopulate", "path", path, "error", err)
		} else {
			title = strings.TrimSpace(tags.Title())
			album = strings.TrimSpace(tags.Album())
			artist = strings.TrimSpace(tags.Artist())
			genre = strings.TrimSpace(tags.Genre())
			if tags.Year() > 0 {
				year = fmt.Sprintf("%d", tags.Year())
			}
		}

		// Fallback for empty title: use filename without extension
		if title == "" {
			title = strings.TrimSuffix(filepath.Base(path), ext)
		}
		if artist == "" {
			artist = "Unknown Artist"
		}
		if album == "" {
			album = "Unknown Album"
		}

		_, err = dbp.Exec(insertInto, title, album, artist, genre, year, path)
		if err != nil {
			slog.Warn("Could not insert track, skipping.", "func", "postgresPopulate", "path", path, "error", err)
			skipped++
			return nil // continue walk
		}
		inserted++
		slog.Debug(fmt.Sprintf("Indexed: %s - %s", artist, title), "func", "postgresPopulate")
		return nil
	})
	if err != nil {
		slog.Error("Music directory walk failed.", "func", "postgresPopulate", "error", err)
		return err
	}
	slog.Info(fmt.Sprintf("Database population done. scanned=%d inserted=%d skipped=%d", scanned, inserted, skipped),
		"func", "postgresPopulate")
	return nil
}
