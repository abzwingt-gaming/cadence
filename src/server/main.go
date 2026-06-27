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

// buildVersion is set at compile time via -ldflags "-X main.buildVersion=v1.2.3"
var buildVersion = ""
var c = ServerConfig{}

type ServerConfig struct {
	Version               string
	RootPath              string
	RequestRateLimit      int
	Port                  string
	MusicDir              string
	LiquidsoapAddress     string
	LiquidsoapPort        string // plain port, no colon, e.g. "1234"
	LiquidsoapHTTPPort    string // plain port, no colon, e.g. "8001"
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
	// RealIPHeader: header set by Caddy/nginx with the real client IP.
	// e.g. "X-Forwarded-For" or "X-Real-IP". Empty = use RemoteAddr.
	RealIPHeader string
	// TrustedProxy: CIDR or IP of the upstream proxy. Only read RealIPHeader
	// when RemoteAddr matches this. Empty = trust all (only safe on LAN).
	TrustedProxy string
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

func applyLogLevel() {
	level := parseLogLevel(c.LogLevel)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})))
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

// stripColon strips a leading colon from a port string (":8080" → "8080").
func stripColon(port string) string {
	return strings.TrimPrefix(port, ":")
}

// normalizePort ensures the port has a leading colon for http.ListenAndServe.
// Accepts both "8080" and ":8080".
func normalizePort(port string) string {
	p := strings.TrimPrefix(port, ":")
	return ":" + p
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
	c.RootPath            = envOrDefault("CSERVER_ROOTPATH", "/app/public/")
	// normalizePort ensures a leading colon regardless of user input format.
	c.Port                = normalizePort(envOrDefault("CSERVER_PORT", "8080"))
	c.MusicDir            = os.Getenv("CSERVER_MUSIC_DIR")
	c.LiquidsoapAddress   = stripScheme(envOrDefault("CSERVER_LIQUIDSOAPADDRESS", "liquidsoap"))
	// Ports stored WITHOUT colon; stripColon handles user input with or without.
	c.LiquidsoapPort      = stripColon(envOrDefault("CSERVER_LIQUIDSOAPPORT", "1234"))
	c.LiquidsoapHTTPPort  = stripColon(envOrDefault("CSERVER_LIQUIDSOAP_HTTP_PORT", "8001"))
	c.LiquidsoapMode      = strings.ToLower(envOrDefault("CSERVER_LIQUIDSOAP_MODE", "auto"))
	c.LiquidsoapTimeout   = envDuration("CSERVER_LIQUIDSOAP_TIMEOUT_MS", 5*time.Second)
	c.IcecastStatusURL    = strings.TrimRight(envOrDefault("CSERVER_ICECAST_STATUS_URL", "http://icecast2:8000"), "/")
	c.IcecastPollInterval = envDuration("CSERVER_ICECAST_POLL_INTERVAL_MS", time.Second)
	c.PublicStreamURL     = os.Getenv("CSERVER_PUBLIC_STREAM_URL")
	c.DBBackend           = strings.ToLower(envOrDefault("CSERVER_DB_BACKEND", "sqlite"))
	c.DBRetries           = envInt("CSERVER_DB_RETRIES", 5)
	c.DBRetryDelay        = envDuration("CSERVER_DB_RETRY_DELAY_MS", 3*time.Second)
	c.PostgresAddress     = stripScheme(envOrDefault("CSERVER_POSTGRESADDRESS", "cadence-postgres"))
	c.PostgresPort        = stripColon(envOrDefault("CSERVER_POSTGRESPORT", "5432"))
	c.PostgresUser        = envOrDefault("CSERVER_POSTGRESUSER", "postgres")
	c.PostgresPassword    = os.Getenv("POSTGRES_PASSWORD")
	c.PostgresDBName      = envOrDefault("CSERVER_POSTGRESDBNAME", "cadence")
	c.PostgresTableName   = envOrDefault("CSERVER_POSTGRESTABLENAME", "metadata")
	c.PostgresSSL         = envOrDefault("CSERVER_POSTGRESSSL", "disable")
	c.SQLitePath          = envOrDefault("CSERVER_SQLITE_PATH", "/data/cadence.db")
	c.RedisAddress        = stripScheme(envOrDefault("CSERVER_REDISADDRESS", "cadence-redis"))
	c.RedisPort           = stripColon(envOrDefault("CSERVER_REDISPORT", "6379"))
	c.RedisPassword       = os.Getenv("CSERVER_REDISPASSWORD")
	c.RedisDB             = envInt("CSERVER_REDISDB", 0)
	c.RequestRateLimit    = envInt("CSERVER_REQRATELIMIT", 5)
	c.ArtRateLimitWindow  = envDuration("CSERVER_ART_RATELIMIT_WINDOW_MS", 200*time.Second)
	c.ArtRateLimitMax     = envInt("CSERVER_ART_RATELIMIT_MAX", 16)
	c.WhitelistPath       = os.Getenv("CSERVER_WHITELIST_PATH")
	c.DevMode, _          = strconv.ParseBool(os.Getenv("CSERVER_DEVMODE"))
	c.LogLevel            = envOrDefault("CSERVER_LOGLEVEL", "info")
	c.ScanWorkers         = envInt("CSERVER_SCAN_WORKERS", 4)
	c.HistorySize         = envInt("CSERVER_HISTORY_SIZE", 10)
	c.FsnotifyDebounce    = envDuration("CSERVER_FSNOTIFY_DEBOUNCE_MS", 3*time.Second)
	c.PatternSeparator    = envOrDefault("CSERVER_PATTERN_SEPARATOR", ";;")
	// Behind Caddy: set CSERVER_REAL_IP_HEADER=X-Forwarded-For
	// Behind nginx:  set CSERVER_REAL_IP_HEADER=X-Real-IP
	// Direct (no proxy): leave empty
	c.RealIPHeader        = os.Getenv("CSERVER_REAL_IP_HEADER")
	c.TrustedProxy        = os.Getenv("CSERVER_TRUSTED_PROXY")
	c.TitleCleanupPatterns = envOrDefault("CSERVER_TITLE_CLEANUP_PATTERNS",
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
	slog.Error("DB unreachable after all retries.", "backend", c.DBBackend, "attempts", c.DBRetries, "last_error", err)
	os.Exit(1)
}

var sighupReloading atomic.Bool

func sighupHandler() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	for range ch {
		if !sighupReloading.CompareAndSwap(false, true) {
			slog.Warn("SIGHUP: reload already in progress, skipping.")
			continue
		}
		go func() {
			defer sighupReloading.Store(false)
			slog.Info("SIGHUP: reloading config and rescanning music library.")
			loadConfig()
			applyLogLevel()
			resetCleanupRe()
			if err := dbPopulate(); err != nil {
				slog.Error("Rescan after SIGHUP failed.", "error", err)
			}
			slog.Info("SIGHUP reload complete.")
		}()
	}
}

// loggingMiddleware logs every request at DEBUG level.
// statusWriter proxies http.Flusher so SSE (eventsource) keeps working
// through the middleware chain.
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

// Flush implements http.Flusher so that SSE (eventsource) can flush
// chunks through the logging middleware without panicking.
func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func main() {
	loadConfig()
	applyLogLevel()

	slog.Info("Cadence starting.",
		"version", c.Version,
		"port", c.Port,
		"db", c.DBBackend,
		"liquidsoap_mode", c.LiquidsoapMode,
		"devmode", c.DevMode,
		"real_ip_header", fmt.Sprintf("%q", c.RealIPHeader),
	)

	if c.MusicDir == "" {
		slog.Warn("CSERVER_MUSIC_DIR not set; music library will not be scanned.")
	}

	initDB()

	go redisInit()
	go filesystemMonitor()
	go icecastMonitor()
	go sighupHandler()

	// loggingMiddleware is applied ONCE here; routes() does NOT wrap again.
	handler := loggingMiddleware(routes())
	slog.Info("HTTP server listening.", "addr", c.Port)
	if err := http.ListenAndServe(c.Port, handler); err != nil {
		slog.Error("HTTP server crashed.", "error", err)
		os.Exit(1)
	}
}
