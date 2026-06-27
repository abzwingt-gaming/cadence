// db_postgres.go
// Postgres metadata database. Used when CSERVER_DB_BACKEND=postgres (default).

package main

import (
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/lib/pq"
	_ "github.com/lib/pq"
)

var dbp *sql.DB

func postgresInit() error {
	dsn := fmt.Sprintf(
		"host='%s' port='%s' user='%s' password='%s' dbname='%s' sslmode='%s'",
		c.PostgresAddress, c.PostgresPort,
		c.PostgresUser, c.PostgresPassword,
		c.PostgresDBName, c.PostgresSSL,
	)
	var err error
	dbp, err = sql.Open("postgres", dsn)
	if err != nil {
		slog.Error("Couldn't open postgres connection.", "error", err)
		return err
	}
	if err = dbp.Ping(); err != nil {
		slog.Error("Couldn't ping postgres.", "error", err)
		return err
	}

	// Enable fuzzystrmatch
	_, err = dbp.Exec("CREATE EXTENSION IF NOT EXISTS fuzzystrmatch")
	if err != nil {
		// 42710 = already exists - safe to ignore
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "42710" {
			slog.Debug("fuzzystrmatch already enabled.")
		} else {
			slog.Warn("fuzzystrmatch enable failed; fuzzy search degraded.", "error", err)
		}
	}

	// Create table if not exists - never drop/recreate the whole DB
	createTable := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id     SERIAL PRIMARY KEY,
			title  VARCHAR(255),
			album  VARCHAR(255),
			artist VARCHAR(255),
			genre  VARCHAR(255),
			year   VARCHAR(4),
			path   VARCHAR(510) UNIQUE
		)`, c.PostgresTableName)
	_, err = dbp.Exec(createTable)
	if err != nil {
		slog.Error("Failed to create metadata table.", "error", err)
		return err
	}
	slog.Info("Postgres ready.", "table", c.PostgresTableName)
	dbActive = dbp
	return nil
}

func postgresUpsert(title, album, artist, genre, year, path string) error {
	upsert := fmt.Sprintf(`
		INSERT INTO %s (title, album, artist, genre, year, path)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (path) DO UPDATE
		  SET title=$1, album=$2, artist=$3, genre=$4, year=$5`,
		c.PostgresTableName)
	_, err := dbp.Exec(upsert, title, album, artist, genre, year, path)
	return err
}
