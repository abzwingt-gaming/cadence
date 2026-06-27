// db_redis.go
// Rate limit database functions.

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
	addr := c.RedisAddress + c.RedisPort
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: c.RedisPassword,
		DB:       c.RedisDBRequest,
	})
	_, err := client.Ping(ctx).Result()
	if err != nil {
		slog.Warn("Redis is unavailable. Rate limiting disabled. Consider using Caddy rate_limit module.",
			"func", "redisInit", "addr", addr, "error", err)
		return
	}
	dbr.RateLimitRequest = client
	dbr.RateLimitArt = redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: c.RedisPassword,
		DB:       c.RedisDBArt,
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
			slog.Error("Couldn't start IP address check for request API.", "func", "rateLimitRequest", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, err = dbr.RateLimitRequest.Get(ctx, ip).Result()
		if err != nil {
			if err == redis.Nil {
				dbr.RateLimitRequest.Set(ctx, ip, nil, time.Duration(c.RequestRateLimit)*time.Second)
				next.ServeHTTP(w, r)
			} else {
				slog.Error("Redis error while checking IP.", "func", "rateLimitRequest", "error", err)
				next.ServeHTTP(w, r) // degrade gracefully
			}
		} else {
			slog.Info(fmt.Sprintf("IP <%s> is rate limited.", ip), "func", "rateLimitRequest")
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
			slog.Error("Couldn't start IP address check for artwork API.", "func", "rateLimitArt", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, err = dbr.RateLimitArt.Get(ctx, ip).Result()
		if err != nil {
			if err == redis.Nil {
				dbr.RateLimitArt.Set(ctx, ip, 1, time.Duration(200)*time.Second)
				next.ServeHTTP(w, r)
			} else {
				slog.Error("Redis error while checking IP.", "func", "rateLimitArt", "error", err)
				next.ServeHTTP(w, r) // degrade gracefully
			}
		} else {
			count, err := dbr.RateLimitArt.Get(ctx, ip).Int()
			if err != nil {
				slog.Error("Couldn't get artwork request count.", "func", "rateLimitArt", "error", err)
				next.ServeHTTP(w, r)
				return
			}
			if count >= 16 {
				slog.Info(fmt.Sprintf("IP <%s> is art rate limited.", ip), "func", "rateLimitArt")
				w.WriteHeader(http.StatusNotModified)
				return
			}
			dbr.RateLimitArt.Set(ctx, ip, count+1, time.Duration(200)*time.Second)
			next.ServeHTTP(w, r)
		}
	})
}

func checkIP(r *http.Request) (ip string, err error) {
	if r.RemoteAddr != "" {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			slog.Error("Couldn't split client address.", "func", "checkIP", "error", err)
			return "", err
		}
		if ip == "" {
			slog.Warn("Client IP was blank.", "func", "checkIP")
			return "", err
		}
		return ip, nil
	}
	return "", err
}
