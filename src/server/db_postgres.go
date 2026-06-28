// db_postgres.go
// Postgres metadata database. Used when CSERVER_DB_BACKEND=postgres (default).

package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"

	_ "github.com/lib/pq"
)

var dbp *sql.DB

func postgresInit() error {
	conf := c()
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(conf.PostgresUser, conf.PostgresPassword),
		Host:   fmt.Sprintf("%s:%s", conf.PostgresAddress, conf.PostgresPort),
		Path:   "/" + conf.PostgresDBName,
	}
	q := u.Query()
	q.Set("sslmode", conf.PostgresSSL)
	u.RawQuery = q.Encode()
	dsn := u.String()

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

	if _, err = dbp.Exec("CREATE EXTENSION IF NOT EXISTS fuzzystrmatch"); err != nil {
		slog.Warn("fuzzystrmatch enable failed; fuzzy search degraded.", "error", err)
	}

	createTable := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id     SERIAL PRIMARY KEY,
			title  VARCHAR(255),
			album  VARCHAR(255),
			artist VARCHAR(255),
			genre  VARCHAR(255),
			year   VARCHAR(4),
			path   VARCHAR(510) UNIQUE
		)`, conf.PostgresTableName)
	if _, err = dbp.Exec(createTable); err != nil {
		slog.Error("Failed to create metadata table.", "error", err)
		return err
	}
	slog.Info("Postgres ready.", "table", conf.PostgresTableName)
	dbActive = dbp
	return nil
}

func postgresUpsert(title, album, artist, genre, year, path string) error {
	upsert := fmt.Sprintf(`
		INSERT INTO %s (title, album, artist, genre, year, path)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (path) DO UPDATE
		  SET title=$1, album=$2, artist=$3, genre=$4, year=$5`,
		c().PostgresTableName)
	_, err := dbp.Exec(upsert, title, album, artist, genre, year, path)
	return err
}
