// db_redis.go
// Rate limiting via Redis. Redis is optional.
// If unavailable, rate limiting is skipped gracefully.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"

	"github.com/redis/go-redis/v9"
)

var ctx = context.Background()
var dbr = RedisClient{}

// redisAvailable is written once in redisInit and read concurrently from
// HTTP handlers — must be atomic to avoid a data race.
var redisAvailable atomic.Bool

type RedisClient struct {
	RateLimitRequest *redis.Client
	RateLimitArt     *redis.Client
}

func redisAddr() string {
	// c.RedisPort is stored without colon (e.g. "6379"), normalise defensively.
	port := c.RedisPort
	if len(port) > 0 && port[0] == ':' {
		port = port[1:]
	}
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
		slog.Warn("Redis unavailable. Rate limiting disabled.", "addr", addr, "error", err)
		return
	}
	dbr.RateLimitRequest = rdb
	dbr.RateLimitArt = redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: c.RedisPassword,
		DB:       c.RedisDB + 1,
	})
	redisAvailable.Store(true)
	slog.Info("Redis connected. Rate limiting enabled.",
		"addr", addr, "db", c.RedisDB,
		"art_window", c.ArtRateLimitWindow,
		"art_max", c.ArtRateLimitMax,
		"req_window_sec", c.RequestRateLimit,
	)
}

func rateLimitRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !redisAvailable.Load() {
			next.ServeHTTP(w, r)
			return
		}
		ip, err := checkIP(r)
		if err != nil {
			slog.Error("checkIP failed in rateLimitRequest.", "remote", r.RemoteAddr, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, err = dbr.RateLimitRequest.Get(ctx, ip).Result()
		if err == redis.Nil {
			dbr.RateLimitRequest.Set(ctx, ip, 1, c.ArtRateLimitWindow)
			next.ServeHTTP(w, r)
		} else if err != nil {
			slog.Warn("Redis error in rateLimitRequest, passing through.", "ip", ip, "error", err)
			next.ServeHTTP(w, r)
		} else {
			slog.Info("Request rate limited.", "ip", ip)
			w.WriteHeader(http.StatusTooManyRequests)
		}
	})
}

func rateLimitArt(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !redisAvailable.Load() {
			next.ServeHTTP(w, r)
			return
		}
		ip, err := checkIP(r)
		if err != nil {
			slog.Error("checkIP failed in rateLimitArt.", "remote", r.RemoteAddr, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, err = dbr.RateLimitArt.Get(ctx, ip).Result()
		if err == redis.Nil {
			dbr.RateLimitArt.Set(ctx, ip, 1, c.ArtRateLimitWindow)
			next.ServeHTTP(w, r)
		} else if err != nil {
			slog.Warn("Redis error in rateLimitArt, passing through.", "ip", ip, "error", err)
			next.ServeHTTP(w, r)
		} else {
			count, countErr := dbr.RateLimitArt.Get(ctx, ip).Int()
			if countErr != nil {
				slog.Warn("Redis count error in rateLimitArt, passing through.", "ip", ip, "error", countErr)
				next.ServeHTTP(w, r)
				return
			}
			if count >= c.ArtRateLimitMax {
				slog.Debug("Art rate limit reached.", "ip", ip, "count", count, "max", c.ArtRateLimitMax)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			dbr.RateLimitArt.Set(ctx, ip, count+1, c.ArtRateLimitWindow)
			next.ServeHTTP(w, r)
		}
	})
}

func checkIP(r *http.Request) (string, error) {
	if r.RemoteAddr == "" {
		return "", fmt.Errorf("empty RemoteAddr")
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "", fmt.Errorf("SplitHostPort(%q): %w", r.RemoteAddr, err)
	}
	if ip == "" {
		return "", fmt.Errorf("blank IP in RemoteAddr %q", r.RemoteAddr)
	}
	return ip, nil
}
