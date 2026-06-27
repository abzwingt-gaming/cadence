package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// buildVersion is set at compile time via ldflags:
// -X main.buildVersion=v1.2.3
// Falls back to CSERVER_VERSION env, then "dev".
var buildVersion = ""

var c = ServerConfig{}

type ServerConfig struct {
	Version              string
	RootPath             string
	RequestRateLimit     int
	Port                 string
	MusicDir             string
	LiquidsoapAddress    string
	LiquidsoapPort       string
	IcecastStatusURL     string
	PublicStreamURL      string
	DBBackend            string
	DBRetries            int
	DBRetryDelay         time.Duration
	PostgresAddress      string
	PostgresPort         string
	PostgresUser         string
	PostgresPassword     string
	PostgresDBName       string
	PostgresTableName    string
	PostgresSSL          string
	SQLitePath           string
	RedisAddress         string
	RedisPort            string
	RedisPassword        string
	RedisDB              int
	WhitelistPath        string
	DevMode              bool
	LogLevel             string
	ScanWorkers          int
	// Comma-separated regex patterns to strip from song titles after tag read.
	// Applied automatically during dbPopulate.
	TitleCleanupPatterns string
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

func stripScheme(addr string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(addr, prefix) {
			slog.Warn(fmt.Sprintf("Scheme in address '%s', stripping.", addr))
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

// initDB attempts DB init with retries. Calls os.Exit(1) if all retries fail.
func initDB() {
	var err error
	for i := 1; i <= c.DBRetries; i++ {
		switch c.DBBackend {
		case "sqlite":
			err = sqliteInit()
		default:
			err = postgresInit()
		}
		if err == nil {
			if populateErr := dbPopulate(); populateErr != nil {
				slog.Warn("DB populate failed.", "error", populateErr)
			}
			return
		}
		if i < c.DBRetries {
			slog.Warn(fmt.Sprintf("DB init failed (attempt %d/%d), retrying in %s...",
				i, c.DBRetries, c.DBRetryDelay))
			time.Sleep(c.DBRetryDelay)
		}
	}
	slog.Error(fmt.Sprintf("DB init failed after %d attempts. Check your DB config and connectivity.", c.DBRetries),
		"backend", c.DBBackend, "error", err)
	os.Exit(1)
}

func main() {
	// Version: ldflags wins, then env, then "dev"
	if buildVersion != "" {
		c.Version = buildVersion
	} else {
		c.Version = envOrDefault("CSERVER_VERSION", "dev")
	}

	c.RootPath         = envOrDefault("CSERVER_ROOTPATH", "/cadence/server/")
	c.RequestRateLimit, _ = strconv.Atoi(envOrDefault("CSERVER_REQRATELIMIT", "5"))
	c.Port             = envOrDefault("CSERVER_PORT", ":8080")
	c.MusicDir         = os.Getenv("CSERVER_MUSIC_DIR")
	c.LiquidsoapAddress = stripScheme(envOrDefault("CSERVER_LIQUIDSOAPADDRESS", "liquidsoap"))
	c.LiquidsoapPort   = envOrDefault("CSERVER_LIQUIDSOAPPORT", ":1234")
	c.IcecastStatusURL = strings.TrimRight(envOrDefault("CSERVER_ICECAST_STATUS_URL", "http://icecast2:8000"), "/")
	c.PublicStreamURL  = os.Getenv("CSERVER_PUBLIC_STREAM_URL")

	c.DBBackend        = strings.ToLower(envOrDefault("CSERVER_DB_BACKEND", "postgres"))
	c.DBRetries, _     = strconv.Atoi(envOrDefault("CSERVER_DB_RETRIES", "5"))
	retryMs, _         := strconv.Atoi(envOrDefault("CSERVER_DB_RETRY_DELAY_MS", "3000"))
	c.DBRetryDelay     = time.Duration(retryMs) * time.Millisecond

	c.PostgresAddress  = stripScheme(envOrDefault("CSERVER_POSTGRESADDRESS", "postgres"))
	c.PostgresPort     = envOrDefault("CSERVER_POSTGRESPORT", "5432")
	c.PostgresUser     = envOrDefault("CSERVER_POSTGRESUSER", "postgres")
	c.PostgresPassword = os.Getenv("POSTGRES_PASSWORD")
	c.PostgresDBName   = envOrDefault("CSERVER_POSTGRESDBNAME", "cadence")
	c.PostgresTableName = envOrDefault("CSERVER_POSTGRESTABLENAME", "metadata")
	c.PostgresSSL      = envOrDefault("CSERVER_POSTGRESSSL", "disable")
	c.SQLitePath       = envOrDefault("CSERVER_SQLITE_PATH", "/data/cadence.db")

	c.RedisAddress     = stripScheme(envOrDefault("CSERVER_REDISADDRESS", "redis"))
	c.RedisPort        = envOrDefault("CSERVER_REDISPORT", ":6379")
	c.RedisPassword    = os.Getenv("CSERVER_REDISPASSWORD")
	c.RedisDB, _       = strconv.Atoi(envOrDefault("CSERVER_REDISDB", "0"))

	c.WhitelistPath    = os.Getenv("CSERVER_WHITELIST_PATH")
	c.DevMode, _       = strconv.ParseBool(os.Getenv("CSERVER_DEVMODE"))
	c.LogLevel         = envOrDefault("CSERVER_LOGLEVEL", "info")
	c.ScanWorkers, _   = strconv.Atoi(envOrDefault("CSERVER_SCAN_WORKERS", "4"))

	// Default yt-dlp cleanup patterns; override or extend via env
	c.TitleCleanupPatterns = envOrDefault("CSERVER_TITLE_CLEANUP_PATTERNS",
		`\s*[\(\[][^\)\]]*[Oo]fficial[^\)\]]*[\)\]]|`+
		`\s*[\(\[][^\)\]]*[Ll]yrics?[^\)\]]*[\)\]]|`+
		`\s*[\(\[][^\)\]]*[Aa]udio[^\)\]]*[\)\]]|`+
		`\s*[\(\[][Hh][Dd][\)\]]|`+
		`\s*[\(\[][14][Kk][\)\]]|`+
		`\s*- [Tt]opic$|`+
		`\s*[\(\[][Mm]usic [Vv]ideo[^\)\]]*[\)\]]|`+
		`\s*[\(\[](?:ft|feat)\.?[^\)\]]*[\)\]]`)

	slog.SetLogLoggerLevel(parseLogLevel(c.LogLevel))

	if c.MusicDir == "" {
		slog.Warn("CSERVER_MUSIC_DIR is not set; DB will not be populated.")
	}

	// Fatal DB init with retries
	initDB()

	go redisInit()
	go filesystemMonitor()
	go icecastMonitor()

	slog.Info(fmt.Sprintf("Cadence %s starting on %s [db=%s]", c.Version, c.Port, c.DBBackend))
	if err := http.ListenAndServe(c.Port, routes()); err != nil {
		slog.Error("HTTP server error.", "error", err)
		os.Exit(1)
	}
}
