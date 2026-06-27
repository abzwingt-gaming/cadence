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
	// Internal address cadence uses to poll Icecast status (server-side, Docker network)
	IcecastStatusURL  string
	// Public stream URL sent to browser clients (e.g. https://radio.example.com/stream)
	PublicStreamURL   string
	PostgresAddress   string
	PostgresPort      string
	PostgresUser      string
	PostgresPassword  string
	PostgresDBName    string
	PostgresTableName string
	PostgresSSL       string
	RedisAddress      string
	RedisPort         string
	RedisPassword     string
	RedisDB           int
	WhitelistPath     string
	DevMode           bool
	LogLevel          string
}

func parseLogLevel(level string) slog.Level {
	if level == "" {
		return slog.LevelInfo
	}
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		slog.Warn(fmt.Sprintf("Unrecognized log level '%s', defaulting to info.", level), "func", "parseLogLevel")
		return slog.LevelInfo
	}
}

// stripScheme removes http:// or https:// from an address string.
// Cadence always prepends http:// internally for Icecast polling.
func stripScheme(addr string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(addr, prefix) {
			slog.Warn(fmt.Sprintf("Scheme prefix found in address '%s' - stripping. Use host:port format.", addr),
				"func", "stripScheme")
			return strings.TrimPrefix(addr, prefix)
		}
	}
	return addr
}

func main() {
	c.Version = os.Getenv("CSERVER_VERSION")
	c.RootPath = os.Getenv("CSERVER_ROOTPATH")
	c.RequestRateLimit, _ = strconv.Atoi(os.Getenv("CSERVER_REQRATELIMIT"))
	c.Port = os.Getenv("CSERVER_PORT")
	c.MusicDir = os.Getenv("CSERVER_MUSIC_DIR")
	c.LiquidsoapAddress = os.Getenv("CSERVER_LIQUIDSOAPADDRESS")
	c.LiquidsoapPort = os.Getenv("CSERVER_LIQUIDSOAPPORT")

	// Internal Icecast status URL (server-to-server, Docker network)
	// Defaults to http://icecast2:8000 if not set
	icecastHost := stripScheme(os.Getenv("CSERVER_ICECASTADDRESS"))
	if icecastHost == "" {
		icecastHost = "icecast2:8000"
	}
	c.IcecastStatusURL = "http://" + icecastHost + "/status-json.xsl"

	// Public stream URL for browser audio element
	// If not set, falls back to the Icecast host (original behavior)
	c.PublicStreamURL = os.Getenv("CSERVER_PUBLIC_STREAM_URL")

	c.PostgresAddress = os.Getenv("CSERVER_POSTGRESADDRESS")
	c.PostgresPort = os.Getenv("CSERVER_POSTGRESPORT")
	c.PostgresUser = os.Getenv("CSERVER_POSTGRESUSER")
	c.PostgresPassword = os.Getenv("POSTGRES_PASSWORD")
	c.PostgresDBName = os.Getenv("CSERVER_POSTGRESDBNAME")
	c.PostgresTableName = os.Getenv("CSERVER_POSTGRESTABLENAME")
	c.PostgresSSL = os.Getenv("CSERVER_POSTGRESSSL")
	c.RedisAddress = os.Getenv("CSERVER_REDISADDRESS")
	c.RedisPort = os.Getenv("CSERVER_REDISPORT")
	c.RedisPassword = os.Getenv("CSERVER_REDISPASSWORD")
	c.RedisDB, _ = strconv.Atoi(os.Getenv("CSERVER_REDISDB"))
	c.WhitelistPath = os.Getenv("CSERVER_WHITELIST_PATH")
	c.DevMode, _ = strconv.ParseBool(os.Getenv("CSERVER_DEVMODE"))
	c.LogLevel = os.Getenv("CSERVER_LOGLEVEL")

	slog.SetLogLoggerLevel(parseLogLevel(c.LogLevel))

	slog.Info("Starting Cadence (homelab build).", "version", c.Version)
	slog.Info(fmt.Sprintf("Icecast status URL: %s", c.IcecastStatusURL))
	if c.PublicStreamURL != "" {
		slog.Info(fmt.Sprintf("Public stream URL override: %s", c.PublicStreamURL))
	}

	if postgresInit() == nil {
		if postgresPopulate() != nil {
			slog.Warn("Initial database population failed.", "func", "main")
		}
	}
	go redisInit()
	go filesystemMonitor()
	go icecastMonitor()

	slog.Info(fmt.Sprintf("Listening on port %s.", c.Port), "func", "main")
	if err := http.ListenAndServe(c.Port, routes()); err != nil {
		slog.Error("Cadence failed to start!", "func", "main", "error", err)
	}
}
