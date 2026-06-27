package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// buildVersion is set at compile time: -ldflags "-X main.buildVersion=v1.2.3"
var buildVersion = ""
var c = ServerConfig{}

type ServerConfig struct {
	Version               string
	RootPath              string
	RequestRateLimit      int
	Port                  string
	MusicDir              string
	LiquidsoapAddress     string
	LiquidsoapPort        string
	LiquidsoapHTTPPort    string
	LiquidsoapMode        string
	LiquidsoapTimeout     time.Duration
	IcecastStatusURL      string
	IcecastPollInterval   time.Duration
	PublicStreamURL       string
	DBBackend             string
	DBRetries             int
	DBRetryDelay          time.Duration
	PostgresAddress       string
	PostgresPort          string
	PostgresUser          string
	PostgresPassword      string
	PostgresDBName        string
	PostgresTableName     string
	PostgresSSL           string
	SQLitePath            string
	RedisAddress          string
	RedisPort             string
	RedisPassword         string
	RedisDB               int
	ArtRateLimitWindow    time.Duration
	ArtRateLimitMax       int
	WhitelistPath         string
	DevMode               bool
	LogLevel              string
	ScanWorkers           int
	HistorySize           int
	FsnotifyDebounce      time.Duration
	TitleCleanupPatterns  string
	ArtistCleanupPatterns string
	PatternSeparator      string
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
			slog.Warn("Scheme in address will be stripped.", "addr", addr)
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

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	ms, err := strconv.Atoi(v)
	if err != nil {
		slog.Warn("Invalid duration env var, using default.", "key", key, "value", v, "default", def)
		return def
	}
	return time.Duration(ms) * time.Millisecond
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		slog.Warn("Invalid int env var, using default.", "key", key, "value", v, "default", def)
		return def
	}
	return n
}

func loadConfig() {
	if buildVersion != "" {
		c.Version = buildVersion
	} else {
		c.Version = envOrDefault("CSERVER_VERSION", "dev")
	}
	c.RootPath              = envOrDefault("CSERVER_ROOTPATH", "/app/public/")
	c.Port                  = envOrDefault("CSERVER_PORT", ":8080")
	c.MusicDir              = os.Getenv("CSERVER_MUSIC_DIR")
	c.LiquidsoapAddress     = stripScheme(envOrDefault("CSERVER_LIQUIDSOAPADDRESS", "liquidsoap"))
	c.LiquidsoapPort        = envOrDefault("CSERVER_LIQUIDSOAPPORT", ":1234")
	c.LiquidsoapHTTPPort    = envOrDefault("CSERVER_LIQUIDSOAP_HTTP_PORT", ":8001")
	c.LiquidsoapMode        = strings.ToLower(envOrDefault("CSERVER_LIQUIDSOAP_MODE", "auto"))
	c.LiquidsoapTimeout     = envDuration("CSERVER_LIQUIDSOAP_TIMEOUT_MS", 5000*time.Millisecond)
	c.IcecastStatusURL      = strings.TrimRight(envOrDefault("CSERVER_ICECAST_STATUS_URL", "http://icecast2:8000"), "/")
	c.IcecastPollInterval   = envDuration("CSERVER_ICECAST_POLL_INTERVAL_MS", 1000*time.Millisecond)
	c.PublicStreamURL       = os.Getenv("CSERVER_PUBLIC_STREAM_URL")
	c.DBBackend             = strings.ToLower(envOrDefault("CSERVER_DB_BACKEND", "postgres"))
	c.DBRetries             = envInt("CSERVER_DB_RETRIES", 5)
	c.DBRetryDelay          = envDuration("CSERVER_DB_RETRY_DELAY_MS", 3000*time.Millisecond)
	c.PostgresAddress       = stripScheme(envOrDefault("CSERVER_POSTGRESADDRESS", "postgres"))
	c.PostgresPort          = envOrDefault("CSERVER_POSTGRESPORT", "5432")
	c.PostgresUser          = envOrDefault("CSERVER_POSTGRESUSER", "postgres")
	c.PostgresPassword      = os.Getenv("POSTGRES_PASSWORD")
	c.PostgresDBName        = envOrDefault("CSERVER_POSTGRESDBNAME", "cadence")
	c.PostgresTableName     = envOrDefault("CSERVER_POSTGRESTABLENAME", "metadata")
	c.PostgresSSL           = envOrDefault("CSERVER_POSTGRESSSL", "disable")
	c.SQLitePath            = envOrDefault("CSERVER_SQLITE_PATH", "/data/cadence.db")
	c.RedisAddress          = stripScheme(envOrDefault("CSERVER_REDISADDRESS", "redis"))
	c.RedisPort             = envOrDefault("CSERVER_REDISPORT", "6379")
	c.RedisPassword         = os.Getenv("CSERVER_REDISPASSWORD")
	c.RedisDB               = envInt("CSERVER_REDISDB", 0)
	c.RequestRateLimit      = envInt("CSERVER_REQRATELIMIT", 5)
	c.ArtRateLimitWindow    = envDuration("CSERVER_ART_RATELIMIT_WINDOW_MS", 200000*time.Millisecond)
	c.ArtRateLimitMax       = envInt("CSERVER_ART_RATELIMIT_MAX", 16)
	c.WhitelistPath         = os.Getenv("CSERVER_WHITELIST_PATH")
	c.DevMode, _            = strconv.ParseBool(os.Getenv("CSERVER_DEVMODE"))
	c.LogLevel              = envOrDefault("CSERVER_LOGLEVEL", "info")
	c.ScanWorkers           = envInt("CSERVER_SCAN_WORKERS", 4)
	c.HistorySize           = envInt("CSERVER_HISTORY_SIZE", 10)
	c.FsnotifyDebounce      = envDuration("CSERVER_FSNOTIFY_DEBOUNCE_MS", 3000*time.Millisecond)
	c.PatternSeparator      = envOrDefault("CSERVER_PATTERN_SEPARATOR", ";;")
	c.TitleCleanupPatterns  = envOrDefault("CSERVER_TITLE_CLEANUP_PATTERNS",
		`\s*[\(\[][^\)\]]*[Oo]fficial[^\)\]]*[\)\]]`+`;;`+
		`\s*[\(\[][^\)\]]*[Ll]yrics?[^\)\]]*[\)\]]`+`;;`+
		`\s*[\(\[][^\)\]]*[Aa]udio[^\)\]]*[\)\]]`+`;;`+
		`\s*[\(\[][Hh][Dd][\)\]]`+`;;`+
		`\s*[\(\[][14][Kk][\)\]]`+`;;`+
		`\s*- [Tt]opic$`+`;;`+
		`\s*[\(\[][Mm]usic [Vv]ideo[^\)\]]*[\)\]]`+`;;`+
		`\s*[\(\[](?:ft|feat)\.?[^\)\]]*[\)\]]`)
	c.ArtistCleanupPatterns = envOrDefault("CSERVER_ARTIST_CLEANUP_PATTERNS",
		`\s*- [Tt]opic$`+`;;`+
		`\s*- [Vv][Ee][Vv][Oo]$`+`;;`+
		`\s*[Oo]fficial$`)
}

func initDB() {
	var err error
	for i := 1; i <= c.DBRetries; i++ {
		slog.Info("Connecting to DB.", "backend", c.DBBackend, "attempt", i, "of", c.DBRetries)
		switch c.DBBackend {
		case "sqlite":
			err = sqliteInit()
		default:
			err = postgresInit()
		}
		if err == nil {
			slog.Info("DB connected, starting initial scan.")
			if populateErr := dbPopulate(); populateErr != nil {
				slog.Warn("Initial DB populate failed.", "error", populateErr)
			}
			return
		}
		slog.Warn("DB init failed.", "attempt", i, "of", c.DBRetries, "error", err)
		if i < c.DBRetries {
			slog.Info("Retrying DB connection.", "delay", c.DBRetryDelay)
			time.Sleep(c.DBRetryDelay)
		}
	}
	slog.Error("DB unreachable, giving up.", "backend", c.DBBackend, "attempts", c.DBRetries, "error", err)
	os.Exit(1)
}

// sighupReloading prevents concurrent SIGHUP-triggered reloads.
var sighupReloading atomic.Bool

func sighupHandler() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	for range ch {
		if !sighupReloading.CompareAndSwap(false, true) {
			slog.Warn("SIGHUP received but reload already in progress, skipping.")
			continue
		}
		go func() {
			defer sighupReloading.Store(false)
			slog.Info("SIGHUP: reloading config and rescanning music library.")
			loadConfig()
			slog.SetLogLoggerLevel(parseLogLevel(c.LogLevel))
			resetCleanupRe()
			if err := dbPopulate(); err != nil {
				slog.Error("Rescan after SIGHUP failed.", "error", err)
			}
			slog.Info("SIGHUP reload complete.")
		}()
	}
}

// loggingMiddleware logs method, path, status, and latency for every request.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		slog.Debug("HTTP",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"latency", time.Since(start).String(),
			"remote", r.RemoteAddr,
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

func main() {
	loadConfig()
	slog.SetLogLoggerLevel(parseLogLevel(c.LogLevel))

	slog.Info("Cadence starting.",
		"version", c.Version,
		"port", c.Port,
		"db", c.DBBackend,
		"liquidsoap_mode", c.LiquidsoapMode,
		"devmode", c.DevMode,
	)

	if c.MusicDir == "" {
		slog.Warn("CSERVER_MUSIC_DIR not set; music library will not be scanned.")
	}

	initDB()

	go redisInit()
	go filesystemMonitor()
	go icecastMonitor()
	go sighupHandler()

	handler := loggingMiddleware(routes())
	slog.Info("HTTP server listening.", "addr", c.Port)
	if err := http.ListenAndServe(c.Port, handler); err != nil {
		slog.Error("HTTP server crashed.", "error", err)
		os.Exit(1)
	}
}
