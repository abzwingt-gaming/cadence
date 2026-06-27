// db_redis.go
// Rate limiting via Redis. Redis is optional — if unavailable, all requests pass through.
//
// Behind a reverse proxy (Caddy, nginx):
//   Set CSERVER_REAL_IP_HEADER=X-Forwarded-For   (Caddy default)
//   Set CSERVER_REAL_IP_HEADER=X-Real-IP          (nginx default)
//   Optionally set CSERVER_TRUSTED_PROXY=172.20.0.0/16 to only trust that CIDR.
//
// To disable rate limiting entirely: don't run Redis (omit --profile redis).

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

var ctx = context.Background()
var dbr = RedisClient{}

// redisAvailable is set once in redisInit; read concurrently from HTTP handlers.
var redisAvailable atomic.Bool

type RedisClient struct {
	RateLimitRequest *redis.Client
	RateLimitArt     *redis.Client
}

func redisAddr() string {
	port := strings.TrimPrefix(c.RedisPort, ":")
	return net.JoinHostPort(c.RedisAddress, port)
}

func redisInit() {
	addr := redisAddr()
	slog.Info("Connecting to Redis.", "addr", addr, "db", c.RedisDB)
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: c.RedisPassword,
		DB:       c.RedisDB,
	})
	if _, err := rdb.Ping(ctx).Result(); err != nil {
		slog.Warn("Redis unavailable — rate limiting disabled.", "addr", addr, "error", err)
		return
	}
	dbr.RateLimitRequest = rdb
	dbr.RateLimitArt = redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: c.RedisPassword,
		DB:       c.RedisDB + 1, // dedicated DB so FlushDB is safe
	})
	redisAvailable.Store(true)
	slog.Info("Redis connected — rate limiting enabled.",
		"addr", addr,
		"real_ip_header", c.RealIPHeader,
		"trusted_proxy", c.TrustedProxy,
		"req_rate_limit_sec", c.RequestRateLimit,
		"art_window", c.ArtRateLimitWindow,
		"art_max", c.ArtRateLimitMax,
	)
}

// realIP extracts the true client IP.
// When CSERVER_REAL_IP_HEADER is set (e.g. "X-Forwarded-For" for Caddy),
// it reads that header — but only if RemoteAddr matches CSERVER_TRUSTED_PROXY
// (or if TrustedProxy is empty, which trusts all — safe on a LAN Docker network).
// Falls back to RemoteAddr when the header is absent or the proxy is not trusted.
func realIP(r *http.Request) (string, error) {
	remoteIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr may have no port in some edge cases
		remoteIP = r.RemoteAddr
	}

	if c.RealIPHeader == "" {
		// No proxy configured — use RemoteAddr directly.
		if remoteIP == "" {
			return "", fmt.Errorf("empty RemoteAddr")
		}
		return remoteIP, nil
	}

	// Check trusted proxy.
	if c.TrustedProxy != "" {
		_, trustedNet, parseErr := net.ParseCIDR(c.TrustedProxy)
		if parseErr != nil {
			// Try plain IP match
			trustedIP := net.ParseIP(c.TrustedProxy)
			parsedRemote := net.ParseIP(remoteIP)
			if trustedIP == nil || parsedRemote == nil || !trustedIP.Equal(parsedRemote) {
				// Proxy not trusted; use RemoteAddr
				return remoteIP, nil
			}
		} else {
			parsedRemote := net.ParseIP(remoteIP)
			if parsedRemote == nil || !trustedNet.Contains(parsedRemote) {
				return remoteIP, nil
			}
		}
	}

	// Read the header — take the first (leftmost) IP in X-Forwarded-For.
	hdrVal := r.Header.Get(c.RealIPHeader)
	if hdrVal == "" {
		return remoteIP, nil
	}
	parts := strings.SplitN(hdrVal, ",", 2)
	ip := strings.TrimSpace(parts[0])
	if ip == "" {
		return remoteIP, nil
	}
	return ip, nil
}

// rateLimitRequest allows one request per CSERVER_REQRATELIMIT seconds per IP.
func rateLimitRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !redisAvailable.Load() {
			next.ServeHTTP(w, r)
			return
		}
		ip, err := realIP(r)
		if err != nil {
			slog.Error("realIP failed in rateLimitRequest.", "remote", r.RemoteAddr, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		key := "rl:req:" + ip
		window := time.Duration(c.RequestRateLimit) * time.Second
		// SETNX: set key with TTL only if it does not exist.
		set, err := dbr.RateLimitRequest.SetNX(ctx, key, 1, window).Result()
		if err != nil {
			slog.Warn("Redis error in rateLimitRequest, passing through.", "ip", ip, "error", err)
			next.ServeHTTP(w, r)
			return
		}
		if !set {
			// Key already existed → rate limited.
			slog.Info("Request rate limited.", "ip", ip)
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimitArt allows up to CSERVER_ART_RATELIMIT_MAX album-art fetches
// per CSERVER_ART_RATELIMIT_WINDOW_MS per IP.
func rateLimitArt(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !redisAvailable.Load() {
			next.ServeHTTP(w, r)
			return
		}
		ip, err := realIP(r)
		if err != nil {
			slog.Error("realIP failed in rateLimitArt.", "remote", r.RemoteAddr, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		key := "rl:art:" + ip
		// INCR atomically increments (or creates at 1). Single round-trip.
		count, err := dbr.RateLimitArt.Incr(ctx, key).Result()
		if err != nil {
			slog.Warn("Redis INCR error in rateLimitArt, passing through.", "ip", ip, "error", err)
			next.ServeHTTP(w, r)
			return
		}
		if count == 1 {
			// First request in this window: set the expiry.
			if expErr := dbr.RateLimitArt.Expire(ctx, key, c.ArtRateLimitWindow).Err(); expErr != nil {
				slog.Warn("Redis Expire error in rateLimitArt.", "ip", ip, "error", expErr)
			}
		}
		if count > int64(c.ArtRateLimitMax) {
			slog.Debug("Art rate limit reached.", "ip", ip, "count", count, "max", c.ArtRateLimitMax)
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
