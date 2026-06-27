// db_redis.go
// Rate limit database. Redis is optional - if unavailable, rate limiting is skipped.

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

var ctx = context.Background()
var dbr = RedisClient{}
var redisAvailable = false

type RedisClient struct {
	RateLimitRequest *redis.Client
	RateLimitArt     *redis.Client
}

func redisInit() {
	if c.RedisAddress == "" {
		slog.Warn("CSERVER_REDISADDRESS not set. Redis rate limiting disabled.", "func", "redisInit")
		return
	}
	addr := c.RedisAddress + c.RedisPort
	opts := &redis.Options{
		Addr:     addr,
		Password: c.RedisPassword,
		DB:       c.RedisDB,
	}
	client := redis.NewClient(opts)
	_, err := client.Ping(ctx).Result()
	if err != nil {
		slog.Warn("Redis unavailable. Rate limiting disabled. Use Caddy rate_limit module as alternative.",
			"func", "redisInit", "addr", addr, "error", err)
		return
	}
	dbr.RateLimitRequest = client
	dbr.RateLimitArt = redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: c.RedisPassword,
		DB:       c.RedisDB + 1,
	})
	redisAvailable = true
	slog.Info("Redis connected.", "func", "redisInit", "addr", addr)
}

func rateLimitRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !redisAvailable {
			next.ServeHTTP(w, r)
			return
		}
		ip, err := checkIP(r)
		if err != nil {
			slog.Error("IP check failed for request API.", "func", "rateLimitRequest", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, err = dbr.RateLimitRequest.Get(ctx, ip).Result()
		if err != nil {
			if err == redis.Nil {
				dbr.RateLimitRequest.Set(ctx, ip, nil, time.Duration(c.RequestRateLimit)*time.Second)
				next.ServeHTTP(w, r)
			} else {
				slog.Error("Redis error checking IP.", "func", "rateLimitRequest", "error", err)
				// Degrade: pass through on Redis error
				next.ServeHTTP(w, r)
			}
		} else {
			slog.Info(fmt.Sprintf("IP <%s> rate limited (request).", ip), "func", "rateLimitRequest")
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
			slog.Error("IP check failed for art API.", "func", "rateLimitArt", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, err = dbr.RateLimitArt.Get(ctx, ip).Result()
		if err != nil {
			if err == redis.Nil {
				dbr.RateLimitArt.Set(ctx, ip, 1, time.Duration(200)*time.Second)
				next.ServeHTTP(w, r)
			} else {
				slog.Error("Redis error checking IP.", "func", "rateLimitArt", "error", err)
				next.ServeHTTP(w, r)
			}
		} else {
			count, err := dbr.RateLimitArt.Get(ctx, ip).Int()
			if err != nil {
				slog.Error("Could not get art request count.", "func", "rateLimitArt", "error", err)
				next.ServeHTTP(w, r)
				return
			}
			if count >= 16 {
				slog.Info(fmt.Sprintf("IP <%s> art rate limited.", ip), "func", "rateLimitArt")
				w.WriteHeader(http.StatusNotModified)
				return
			}
			dbr.RateLimitArt.Set(ctx, ip, count+1, time.Duration(200)*time.Second)
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
		slog.Error("Could not split host:port from RemoteAddr.", "func", "checkIP", "error", err)
		return "", err
	}
	if ip == "" {
		return "", fmt.Errorf("blank IP in RemoteAddr")
	}
	return ip, nil
}
