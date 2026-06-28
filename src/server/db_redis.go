// db_redis.go
// Optional Redis integration.
//
// Two independent uses:
//   1. Built-in rate limiting (only when CSERVER_RATELIMIT_ENABLED=true).
//      Production with Caddy: leave disabled — Caddy handles rate limiting per
//      real client IP via rate_limit directive (see config/Caddyfile.example).
//      The built-in limiter keys on RemoteAddr which is always Caddy's IP
//      behind a proxy, making per-client limiting impossible without extra config.
//
//   2. Album-art cache clear on track change (artCacheClear).
//      Always attempted when Redis is reachable, regardless of RatelimitEnabled.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	redisClient    *redis.Client
	redisAvailable atomic.Bool // FIX: was plain bool — data race between redisInit goroutine and HTTP handlers
)

// redisInit connects with exponential-backoff retries and runs a background
// ping loop for reconnect detection. Call as a goroutine.
func redisInit() {
	addr := fmt.Sprintf("%s:%s", c().RedisAddress, c().RedisPort)
	opts := &redis.Options{
		Addr:     addr,
		Password: c().RedisPassword,
		DB:       c().RedisDB,
	}

	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		cl := redis.NewClient(opts)
		if err := cl.Ping(context.Background()).Err(); err != nil {
			slog.Warn("Redis connect failed; retrying.", "addr", addr, "error", err, "backoff", backoff)
			_ = cl.Close()
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		redisClient = cl
		redisAvailable.Store(true)
		slog.Info("Redis connected.", "addr", addr)
		break
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if err := redisClient.Ping(context.Background()).Err(); err != nil {
			slog.Warn("Redis ping failed; marking unavailable.", "error", err)
			redisAvailable.Store(false)
			cl := redis.NewClient(opts)
			if err2 := cl.Ping(context.Background()).Err(); err2 == nil {
				_ = redisClient.Close()
				redisClient = cl
				redisAvailable.Store(true)
				slog.Info("Redis reconnected.", "addr", addr)
			} else {
				_ = cl.Close()
			}
		}
	}
}

// artCacheClear removes rate-limit art cache keys from Redis on track change.
// Uses SCAN+DEL to avoid blocking with FLUSHDB.
// FIX: was "art:*" which never matched anything — keys are written as "rl:art:*".
func artCacheClear() {
	if !redisAvailable.Load() || redisClient == nil {
		return
	}
	ctx := context.Background()
	var cursor uint64
	var keys []string
	for {
		batch, next, err := redisClient.Scan(ctx, cursor, "rl:art:*", 100).Result()
		if err != nil {
			slog.Warn("Redis SCAN failed during art cache clear.", "error", err)
			return
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if len(keys) > 0 {
		if err := redisClient.Del(ctx, keys...).Err(); err != nil {
			slog.Warn("Redis DEL failed during art cache clear.", "error", err)
		}
	}
}

// luaIncrWithTTL atomically increments a counter and sets TTL on first call.
// Fixes the INCR+EXPIRE TOCTOU race: without this, two concurrent requests
// can both see count==1 and each reset the TTL independently.
var luaIncrWithTTL = redis.NewScript(`
	local n = redis.call("INCR", KEYS[1])
	if n == 1 then
		redis.call("EXPIRE", KEYS[1], ARGV[1])
	end
	return n
`)

// rateLimitRequest enforces per-IP song request rate limiting.
// Returns true (and writes 429) when the client is over limit.
// No-op when RatelimitEnabled=false or Redis is unavailable.
func rateLimitRequest(w http.ResponseWriter, r *http.Request) bool {
	if !c().RatelimitEnabled || !redisAvailable.Load() || redisClient == nil {
		return false
	}
	ip, _ := realIP(r)
	key := "rl:req:" + ip
	windowSec := int64(c().ArtRateLimitWindow.Seconds())
	limit := int64(c().RequestRateLimit)

	n, err := luaIncrWithTTL.Run(context.Background(), redisClient, []string{key}, windowSec).Int64()
	if err != nil {
		slog.Warn("Rate limit script error; allowing request.", "error", err)
		return false
	}
	if n > limit {
		http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
		return true
	}
	return false
}

// rateLimitArt enforces per-IP album-art rate limiting.
func rateLimitArt(w http.ResponseWriter, r *http.Request) bool {
	if !c().RatelimitEnabled || !redisAvailable.Load() || redisClient == nil {
		return false
	}
	ip, _ := realIP(r)
	key := "rl:art:" + ip
	windowSec := int64(c().ArtRateLimitWindow.Seconds())
	limit := int64(c().ArtRateLimitMax)

	n, err := luaIncrWithTTL.Run(context.Background(), redisClient, []string{key}, windowSec).Int64()
	if err != nil {
		slog.Warn("Art rate limit script error; allowing request.", "error", err)
		return false
	}
	if n > limit {
		http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
		return true
	}
	return false
}
