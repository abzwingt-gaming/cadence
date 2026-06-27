package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
)

var c = ServerConfig{}

type ServerConfig struct {
	Version           string
	RootPath          string
	RequestRateLimit  int
	Port              string
	MusicDir          string
	LiquidsoapAddress string
	LiquidsoapPort    string
	// Internal status endpoint (Docker hostname, never leaves the network)
	IcecastStatusURL  string
	// Public stream URL sent to the browser
	PublicStreamURL   string
	DBBackend         string // "postgres" or "sqlite"
	PostgresAddress   string
	PostgresPort      string
	PostgresUser      string
	PostgresPassword  string
	PostgresDBName    string
	PostgresTableName string
	PostgresSSL       string
	SQLitePath        string
	RedisAddress      string
	RedisPort         string
	RedisPassword     string
	RedisDB           int
	WhitelistPath     string
	DevMode           bool
	LogLevel          string
	ScanWorkers       int
}

func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// stripScheme removes http:// or https:// from addresses that must be host-only.
func stripScheme(addr string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(addr, prefix) {
			slog.Warn(fmt.Sprintf("Scheme prefix found in '%s', stripping. Use host:port format.", addr))
			return strings.TrimPrefix(addr, prefix)
		}
	}
	return addr
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	c.Version           = envOrDefault("CSERVER_VERSION", "dev")
	c.RootPath          = envOrDefault("CSERVER_ROOTPATH", "/cadence/server/")
	c.RequestRateLimit, _ = strconv.Atoi(envOrDefault("CSERVER_REQRATELIMIT", "5"))
	c.Port              = envOrDefault("CSERVER_PORT", ":8080")
	c.MusicDir          = os.Getenv("CSERVER_MUSIC_DIR")
	c.LiquidsoapAddress = stripScheme(envOrDefault("CSERVER_LIQUIDSOAPADDRESS", "liquidsoap"))
	c.LiquidsoapPort    = envOrDefault("CSERVER_LIQUIDSOAPPORT", ":1234")

	// Internal Icecast status URL (used only by server, never forwarded to client)
	c.IcecastStatusURL  = envOrDefault("CSERVER_ICECAST_STATUS_URL", "http://icecast2:8000")
	// Trim trailing slash
	c.IcecastStatusURL  = strings.TrimRight(c.IcecastStatusURL, "/")
	// Public stream URL sent to browser - defaults to same as status host but can differ
	c.PublicStreamURL   = os.Getenv("CSERVER_PUBLIC_STREAM_URL")

	c.DBBackend         = strings.ToLower(envOrDefault("CSERVER_DB_BACKEND", "postgres"))
	c.PostgresAddress   = stripScheme(envOrDefault("CSERVER_POSTGRESADDRESS", "postgres"))
	c.PostgresPort      = envOrDefault("CSERVER_POSTGRESPORT", "5432")
	c.PostgresUser      = envOrDefault("CSERVER_POSTGRESUSER", "postgres")
	c.PostgresPassword  = os.Getenv("POSTGRES_PASSWORD")
	c.PostgresDBName    = envOrDefault("CSERVER_POSTGRESDBNAME", "cadence")
	c.PostgresTableName = envOrDefault("CSERVER_POSTGRESTABLENAME", "metadata")
	c.PostgresSSL       = envOrDefault("CSERVER_POSTGRESSSL", "disable")
	c.SQLitePath        = envOrDefault("CSERVER_SQLITE_PATH", "/data/cadence.db")

	c.RedisAddress      = stripScheme(envOrDefault("CSERVER_REDISADDRESS", "redis"))
	c.RedisPort         = envOrDefault("CSERVER_REDISPORT", ":6379")
	c.RedisPassword     = os.Getenv("CSERVER_REDISPASSWORD")
	c.RedisDB, _        = strconv.Atoi(envOrDefault("CSERVER_REDISDB", "0"))

	c.WhitelistPath     = os.Getenv("CSERVER_WHITELIST_PATH")
	c.DevMode, _        = strconv.ParseBool(os.Getenv("CSERVER_DEVMODE"))
	c.LogLevel          = envOrDefault("CSERVER_LOGLEVEL", "info")
	c.ScanWorkers, _    = strconv.Atoi(envOrDefault("CSERVER_SCAN_WORKERS", "4"))

	slog.SetLogLoggerLevel(parseLogLevel(c.LogLevel))

	// Validate required config
	if c.MusicDir == "" {
		slog.Warn("CSERVER_MUSIC_DIR is not set; database will not be populated.")
	}

	// Init DB backend
	switch c.DBBackend {
	case "sqlite":
		if err := sqliteInit(); err != nil {
			slog.Warn("SQLite init failed.", "error", err)
		} else {
			if err := dbPopulate(); err != nil {
				slog.Warn("Initial DB population failed.", "error", err)
			}
		}
	default: // postgres
		if err := postgresInit(); err != nil {
			slog.Warn("Postgres init failed.", "error", err)
		} else {
			if err := dbPopulate(); err != nil {
				slog.Warn("Initial DB population failed.", "error", err)
			}
		}
	}

	go redisInit()
	go filesystemMonitor()
	go icecastMonitor()

	slog.Info(fmt.Sprintf("Starting Cadence %s on %s [db=%s]", c.Version, c.Port, c.DBBackend))
	if err := http.ListenAndServe(c.Port, routes()); err != nil {
		slog.Error("Cadence failed to start!", "error", err)
	}
}
