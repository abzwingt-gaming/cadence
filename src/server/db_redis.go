// db_redis.go
// Rate limiting via Redis. Redis is optional.
// If unavailable, rate limiting is skipped (use external tools like Caddy).

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
)

var ctx           = context.Background()
var dbr           = RedisClient{}
var redisAvailable = false

type RedisClient struct {
	RateLimitRequest *redis.Client
	RateLimitArt     *redis.Client
}

func redisInit() {
	rdb := redis.NewClient(&redis.Options{
		Addr:     c.RedisAddress + c.RedisPort,
		Password: c.RedisPassword,
		DB:       c.RedisDB,
	})
	if _, err := rdb.Ping(ctx).Result(); err != nil {
		slog.Warn("Redis unavailable. Rate limiting disabled. Use Caddy rate_limit if needed.", "error", err)
		return
	}
	dbr.RateLimitRequest = rdb
	dbr.RateLimitArt = redis.NewClient(&redis.Options{
		Addr:     c.RedisAddress + c.RedisPort,
		Password: c.RedisPassword,
		DB:       c.RedisDB + 1, // art uses next DB index
	})
	redisAvailable = true
	slog.Info("Redis connected.", "addr", c.RedisAddress+c.RedisPort, "db", c.RedisDB)
}

func rateLimitRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !redisAvailable {
			next.ServeHTTP(w, r)
			return
		}
		ip, err := checkIP(r)
		if err != nil {
			slog.Error("checkIP failed.", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, err = dbr.RateLimitRequest.Get(ctx, ip).Result()
		if err == redis.Nil {
			dbr.RateLimitRequest.Set(ctx, ip, 1, time.Duration(c.RequestRateLimit)*time.Second)
			next.ServeHTTP(w, r)
		} else if err != nil {
			slog.Warn("Redis error in rateLimitRequest, passing through.", "error", err)
			next.ServeHTTP(w, r)
		} else {
			slog.Info(fmt.Sprintf("Rate limited: %s", ip))
			w.WriteHeader(http.StatusTooManyRequests)
		}
	})
}

func rateLimitArt(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !redisAvailable {
			next.ServeHTTP(w, r)
			return
		}
		ip, err := checkIP(r)
		if err != nil {
			slog.Error("checkIP failed.", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, err = dbr.RateLimitArt.Get(ctx, ip).Result()
		if err == redis.Nil {
			dbr.RateLimitArt.Set(ctx, ip, 1, 200*time.Second)
			next.ServeHTTP(w, r)
		} else if err != nil {
			slog.Warn("Redis error in rateLimitArt, passing through.", "error", err)
			next.ServeHTTP(w, r)
		} else {
			count, _ := dbr.RateLimitArt.Get(ctx, ip).Int()
			if count >= 16 {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			dbr.RateLimitArt.Set(ctx, ip, count+1, 200*time.Second)
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
		return "", err
	}
	if ip == "" {
		return "", fmt.Errorf("blank IP")
	}
	return ip, nil
}
