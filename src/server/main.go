package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var buildVersion = ""

var cfg atomic.Pointer[ServerConfig]

func c() *ServerConfig { return cfg.Load() }

type ServerConfig struct {
	Version              string
	RootPath             string
	Port                 string
	MusicDir             string
	LiquidsoapAddress    string
	LiquidsoapPort       string
	LiquidsoapHTTPPort   string
	LiquidsoapMode       string
	LiquidsoapTimeout    time.Duration
	IcecastStatusURL     string
	IcecastPollInterval  time.Duration
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

	// RatelimitEnabled: built-in Redis rate limiter.
	// Production behind Caddy: keep false; configure rate_limit in Caddyfile.
	// Caddy sees real client IPs; this limiter keys on RemoteAddr = Caddy's IP.
	// Direct access / dev: set true and add the redis compose profile.
	RatelimitEnabled   bool
	RequestRateLimit   int
	ArtRateLimitWindow time.Duration
	ArtRateLimitMax    int

	// RealIPHeader / TrustedProxy: only used when RatelimitEnabled=true.
	// TrustedProxy MUST be set when RealIPHeader is set — see validateConfig.
	RealIPHeader string
	TrustedProxy string

	AdminToken            string // CSERVER_ADMIN_TOKEN — required for /api/admin/*
	WhitelistPath         string
	DevMode               bool
	LogLevel              string
	ScanWorkers           int
	HistorySize           int
	FsnotifyDebounce      time.Duration
	PatternSeparator      string
	TitleCleanupPatterns  string
	ArtistCleanupPatterns string
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		slog.Warn("Invalid int env var.", "key", key, "default", def)
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		// Accept Go duration strings ("5s", "200ms") first for readability,
		// fall back to plain integer milliseconds for backward compat.
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		if ms, err := strconv.Atoi(v); err == nil {
			return time.Duration(ms) * time.Millisecond
		}
		slog.Warn("Invalid duration env var (expected Go duration or ms integer).", "key", key, "default", def)
	}
	return def
}

func envBool(key string) bool {
	b, _ := strconv.ParseBool(os.Getenv(key))
	return b
}

func stripScheme(s string) string {
	for _, p := range []string{"https://", "http://"} {
		if strings.HasPrefix(s, p) {
			slog.Warn("Scheme stripped from address env var.", "value", s)
			return strings.TrimPrefix(s, p)
		}
	}
	return s
}

func normalizePort(s string) string {
	return ":" + strings.TrimPrefix(s, ":")
}

func buildConfig() *ServerConfig {
	sep := envStr("CSERVER_PATTERN_SEPARATOR", ";;")
	ver := buildVersion
	if ver == "" {
		ver = envStr("CSERVER_VERSION", "dev")
	}
	return &ServerConfig{
		Version:             ver,
		RootPath:            envStr("CSERVER_ROOTPATH", "/app/public/"),
		Port:                normalizePort(envStr("CSERVER_PORT", "8080")),
		MusicDir:            os.Getenv("CSERVER_MUSIC_DIR"),
		LiquidsoapAddress:   stripScheme(envStr("CSERVER_LIQUIDSOAPADDRESS", "liquidsoap")),
		LiquidsoapPort:      strings.TrimPrefix(envStr("CSERVER_LIQUIDSOAPPORT", "1234"), ":"),
		LiquidsoapHTTPPort:  strings.TrimPrefix(envStr("CSERVER_LIQUIDSOAP_HTTP_PORT", "8001"), ":"),
		LiquidsoapMode:      strings.ToLower(envStr("CSERVER_LIQUIDSOAP_MODE", "auto")),
		LiquidsoapTimeout:   envDur("CSERVER_LIQUIDSOAP_TIMEOUT_MS", 5*time.Second),
		IcecastStatusURL:    strings.TrimRight(envStr("CSERVER_ICECAST_STATUS_URL", "http://icecast2:8000"), "/"),
		IcecastPollInterval: envDur("CSERVER_ICECAST_POLL_INTERVAL_MS", time.Second),
		PublicStreamURL:     os.Getenv("CSERVER_PUBLIC_STREAM_URL"),
		DBBackend:           strings.ToLower(envStr("CSERVER_DB_BACKEND", "sqlite")),
		DBRetries:           envInt("CSERVER_DB_RETRIES", 5),
		DBRetryDelay:        envDur("CSERVER_DB_RETRY_DELAY_MS", 3*time.Second),
		PostgresAddress:     stripScheme(envStr("CSERVER_POSTGRESADDRESS", "cadence-postgres")),
		PostgresPort:        strings.TrimPrefix(envStr("CSERVER_POSTGRESPORT", "5432"), ":"),
		PostgresUser:        envStr("CSERVER_POSTGRESUSER", "postgres"),
		PostgresPassword:    os.Getenv("POSTGRES_PASSWORD"),
		PostgresDBName:      envStr("CSERVER_POSTGRESDBNAME", "cadence"),
		PostgresTableName:   envStr("CSERVER_POSTGRESTABLENAME", "metadata"),
		PostgresSSL:         envStr("CSERVER_POSTGRESSSL", "disable"),
		SQLitePath:          envStr("CSERVER_SQLITE_PATH", "/data/cadence.db"),
		RedisAddress:        stripScheme(envStr("CSERVER_REDISADDRESS", "cadence-redis")),
		RedisPort:           strings.TrimPrefix(envStr("CSERVER_REDISPORT", "6379"), ":"),
		RedisPassword:       os.Getenv("CSERVER_REDISPASSWORD"),
		RedisDB:             envInt("CSERVER_REDISDB", 0),
		RatelimitEnabled:    envBool("CSERVER_RATELIMIT_ENABLED"),
		RequestRateLimit:    envInt("CSERVER_REQRATELIMIT", 5),
		ArtRateLimitWindow:  envDur("CSERVER_ART_RATELIMIT_WINDOW_MS", 200*time.Second),
		ArtRateLimitMax:     envInt("CSERVER_ART_RATELIMIT_MAX", 16),
		RealIPHeader:        os.Getenv("CSERVER_REAL_IP_HEADER"),
		TrustedProxy:        os.Getenv("CSERVER_TRUSTED_PROXY"),
		AdminToken:          os.Getenv("CSERVER_ADMIN_TOKEN"),
		WhitelistPath:       os.Getenv("CSERVER_WHITELIST_PATH"),
		DevMode:             envBool("CSERVER_DEVMODE"),
		LogLevel:            envStr("CSERVER_LOGLEVEL", "info"),
		ScanWorkers:         envInt("CSERVER_SCAN_WORKERS", 4),
		HistorySize:         envInt("CSERVER_HISTORY_SIZE", 10),
		FsnotifyDebounce:    envDur("CSERVER_FSNOTIFY_DEBOUNCE_MS", 3*time.Second),
		PatternSeparator:    sep,
		TitleCleanupPatterns: envStr("CSERVER_TITLE_CLEANUP_PATTERNS",
			`\s*[\(\[][^\)\]]*[Oo]fficial[^\)\]]*[\)\]]`+sep+
				`\s*[\(\[][^\)\]]*[Ll]yrics?[^\)\]]*[\)\]]`+sep+
				`\s*[\(\[][^\)\]]*[Aa]udio[^\)\]]*[\)\]]`+sep+
				`\s*[\(\[][Hh][Dd][\)\]]`+sep+
				`\s*[\(\[][14][Kk][\)\]]`+sep+
				`\s*- [Tt]opic$`+sep+
				`\s*[\(\[][Mm]usic [Vv]ideo[^\)\]]*[\)\]]`+sep+
				`\s*[\(\[](?:ft|feat)\.?[^\)\]]*[\)\]]`),
		ArtistCleanupPatterns: envStr("CSERVER_ARTIST_CLEANUP_PATTERNS",
			`\s*- [Tt]opic$`+sep+
				`\s*- [Vv][Ee][Vv][Oo]$`+sep+
				`\s*[Oo]fficial$`),
	}
}

func validateConfig(conf *ServerConfig) {
	if conf.RootPath == "/" || conf.RootPath == "" {
		slog.Error("CSERVER_ROOTPATH must not be '/' or empty — would expose the entire filesystem.")
		os.Exit(1)
	}
	info, err := os.Stat(conf.RootPath)
	if err != nil {
		slog.Error("CSERVER_ROOTPATH inaccessible.", "path", conf.RootPath, "error", err)
		os.Exit(1)
	}
	if !info.IsDir() {
		slog.Error("CSERVER_ROOTPATH is not a directory.", "path", conf.RootPath)
		os.Exit(1)
	}

	if conf.AdminToken == "" {
		slog.Warn("CSERVER_ADMIN_TOKEN is not set — /api/admin/* endpoints are disabled.")
	}

	if conf.RatelimitEnabled {
		if conf.RealIPHeader == "" {
			slog.Warn("[RATELIMIT] Built-in rate limiter ON but CSERVER_REAL_IP_HEADER is unset. " +
				"Behind Caddy every request arrives from Caddy's IP — all clients share one bucket. " +
				"Set CSERVER_REAL_IP_HEADER=X-Forwarded-For, or disable CSERVER_RATELIMIT_ENABLED and use Caddy rate_limit.")
		}
		if conf.RealIPHeader != "" && conf.TrustedProxy == "" {
			slog.Warn("[RATELIMIT] CSERVER_REAL_IP_HEADER set but CSERVER_TRUSTED_PROXY is empty — "+
				"header will be IGNORED (fail-safe). Set CSERVER_TRUSTED_PROXY to Caddy's IP or CIDR.",
				"header", conf.RealIPHeader)
		}
	} else {
		slog.Info("[RATELIMIT] Built-in rate limiting disabled. Use Caddy rate_limit directive (see config/Caddyfile.example).")
	}

	if conf.MusicDir == "" {
		slog.Warn("CSERVER_MUSIC_DIR not set — music library will not be scanned.")
	}
}

var logMu sync.Mutex

// validLogLevels maps recognised level strings to slog.Level.
// An unrecognised string is treated as INFO and a warning is emitted.
var validLogLevels = map[string]slog.Level{
	"debug": slog.LevelDebug,
	"info":  slog.LevelInfo,
	"warn":  slog.LevelWarn,
	"error": slog.LevelError,
}

func applyLogLevel(conf *ServerConfig) {
	key := strings.ToLower(conf.LogLevel)
	lvl, ok := validLogLevels[key]
	if !ok {
		slog.Warn("Unknown CSERVER_LOGLEVEL; defaulting to info.", "value", conf.LogLevel)
		lvl = slog.LevelInfo
	}
	logMu.Lock()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))
	logMu.Unlock()
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
			slog.Info("SIGHUP: reloading config and rescanning library.")
			newConf := buildConfig()
			applyLogLevel(newConf)
			cfg.Store(newConf)
			resetCleanupRe()
			historyMu.Lock()
			if len(history) > newConf.HistorySize {
				history = history[len(history)-newConf.HistorySize:]
			}
			historyMu.Unlock()
			if err := dbPopulate(); err != nil {
				slog.Error("Library rescan after SIGHUP failed.", "error", err)
			}
			slog.Info("SIGHUP reload complete.")
		}()
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}
func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (sw *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := sw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("hijack not supported")
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		ip, _ := realIP(r)
		slog.Debug("HTTP",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"latency", time.Since(start),
			"ip", ip,
		)
	})
}

func main() {
	conf := buildConfig()
	applyLogLevel(conf)
	cfg.Store(conf)
	validateConfig(conf)

	slog.Info("Cadence starting.",
		"version", conf.Version,
		"port", conf.Port,
		"db", conf.DBBackend,
		"sqlite_path", conf.SQLitePath,
		"music_dir", conf.MusicDir,
		"icecast_url", conf.IcecastStatusURL,
		"liquidsoap_mode", conf.LiquidsoapMode,
		"devmode", conf.DevMode,
		"ratelimit_builtin", conf.RatelimitEnabled,
	)

	initLiquidsoapClient()

	var dbErr error
	for i := 1; i <= conf.DBRetries; i++ {
		slog.Info("Connecting to DB.", "backend", conf.DBBackend, "attempt", i)
		switch conf.DBBackend {
		case "sqlite":
			dbErr = sqliteInit()
		default:
			dbErr = postgresInit()
		}
		if dbErr == nil {
			if err := dbPopulate(); err != nil {
				slog.Warn("Initial library scan failed.", "error", err)
			}
			dbReady.Store(true)
			break
		}
		slog.Warn("DB init failed.", "attempt", i, "error", dbErr)
		if i < conf.DBRetries {
			time.Sleep(conf.DBRetryDelay)
		}
	}
	if dbErr != nil {
		slog.Error("DB unreachable after all retries.", "attempts", conf.DBRetries, "error", dbErr)
		os.Exit(1)
	}

	go redisInit()
	go filesystemMonitor()
	go icecastMonitor()
	go sighupHandler()

	// ── Graceful shutdown on SIGTERM / SIGINT ─────────────────────────────────
	// Allows Docker / Portainer restacks to drain connections cleanly instead
	// of hard-killing the process and leaving SSE clients in error state.
	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, syscall.SIGTERM, syscall.SIGINT)

	srv := &http.Server{
		Addr:    conf.Port,
		Handler: loggingMiddleware(routes()),
	}

	go func() {
		sig := <-shutdownCh
		slog.Info("Shutdown signal received; draining connections.", "signal", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("HTTP server shutdown error.", "error", err)
		}
	}()

	slog.Info("HTTP server listening.", "addr", conf.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("HTTP server crashed.", "error", err)
		os.Exit(1)
	}
	slog.Info("Cadence stopped gracefully.")
}
