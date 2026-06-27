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

func postgresInit() (err error) {
	// Give Postgres time to finish startup in Docker
	time.Sleep(5 * time.Second)
	dsn := fmt.Sprintf("host='%s' port='%s' user='%s' password='%s' dbname='%s' sslmode='%s'",
		c.PostgresAddress, c.PostgresPort, c.PostgresUser, c.PostgresPassword, c.PostgresDBName, c.PostgresSSL)
	dbp, err = sql.Open("postgres", dsn)
	if err != nil {
		slog.Error("Couldn't open a connection to database.", "func", "postgresInit", "error", err)
		return err
	}
	err = dbp.Ping()
	if err != nil {
		slog.Error("Couldn't ping the metadata database.", "func", "postgresInit", "error", err)
		return err
	}
	// Enable fuzzystrmatch for levenshtein-based search ranking
	_, err = dbp.Exec("CREATE EXTENSION IF NOT EXISTS fuzzystrmatch")
	if err != nil {
		slog.Error("Failed to enable fuzzystrmatch. Search will function in a degraded state.", "func", "postgresInit", "error", err)
		// Non-fatal: continue anyway
	}
	slog.Info("Postgres connected.", "func", "postgresInit")
	return nil
}

func postgresPopulate() error {
	// Drop and recreate only the table, not the entire database.
	// DROP DATABASE cannot be run while connected to that database in Postgres.
	dropTable := fmt.Sprintf("DROP TABLE IF EXISTS %s", c.PostgresTableName)
	createTable := fmt.Sprintf(`CREATE TABLE %s
	(
	   id      serial PRIMARY KEY,
	   title   character varying(255),
	   album   character varying(255),
	   artist  character varying(255),
	   genre   character varying(255),
	   year    integer,
	   path    character varying(510)
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
			slog.Error("Failed to build database table.", "func", "postgresPopulate", "error", err)
			return err
		}
	}

	_, err = os.Stat(c.MusicDir)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Error(fmt.Sprintf("Music directory <%s> does not exist.", c.MusicDir), "func", "postgresPopulate", "error", err)
		}
		return err
	}

	insertInto := fmt.Sprintf(
		"INSERT INTO %s (title, album, artist, genre, year, path) VALUES ($1, $2, $3, $4, $5, $6)",
		c.PostgresTableName)

	supportedExtensions := []string{".mp3", ".flac", ".ogg", ".m4a", ".wav"}
	scanned, skipped := 0, 0

	slog.Info(fmt.Sprintf("Scanning music directory: <%s>", c.MusicDir), "func", "postgresPopulate")
	err = filepath.Walk(c.MusicDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Error("Error during filepath walk.", "func", "postgresPopulate", "error", err)
			return err
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		supported := false
		for _, e := range supportedExtensions {
			if ext == e {
				supported = true
				break
			}
		}
		if !supported {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			slog.Warn(fmt.Sprintf("Could not open <%s>, skipping.", path), "func", "postgresPopulate", "error", err)
			skipped++
			return nil // continue scan
		}
		defer file.Close()

		// Read tags with fallback for untagged files
		var title, album, artist, genre string
		var year int

		tags, err := tag.ReadFrom(file)
		if err != nil {
			slog.Warn(fmt.Sprintf("Could not read tags from <%s>, using filename as title.", path), "func", "postgresPopulate", "error", err)
			title = strings.TrimSuffix(filepath.Base(path), ext)
			artist, album, genre, year = "Unknown", "Unknown", "Unknown", 0
		} else {
			title = strings.TrimSpace(tags.Title())
			if title == "" {
				title = strings.TrimSuffix(filepath.Base(path), ext)
			}
			artist = strings.TrimSpace(tags.Artist())
			if artist == "" {
				artist = "Unknown"
			}
			album = strings.TrimSpace(tags.Album())
			if album == "" {
				album = "Unknown"
			}
			genre = strings.TrimSpace(tags.Genre())
			if genre == "" {
				genre = "Unknown"
			}
			year = tags.Year()
		}

		_, err = dbp.Exec(insertInto, title, album, artist, genre, year, path)
		if err != nil {
			slog.Warn(fmt.Sprintf("Failed to insert metadata for <%s>, skipping.", path), "func", "postgresPopulate", "error", err)
			skipped++
			return nil // continue scan
		}
		slog.Debug(fmt.Sprintf("Indexed: %s by %s", title, artist), "func", "postgresPopulate")
		scanned++
		return nil
	})
	if err != nil {
		slog.Error("Music directory walk failed.", "func", "postgresPopulate", "error", err)
		return err
	}
	slog.Info(fmt.Sprintf("Database population completed. Indexed: %d, Skipped: %d", scanned, skipped), "func", "postgresPopulate")
	return nil
}
