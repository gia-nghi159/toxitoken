package ratelimit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type Limiter interface {
	Allow(ctx context.Context, identifier string) (allowed bool, remaining int, err error)
}

// MemoryLimiter provides an in-memory sliding-window counter without Redis.
type MemoryLimiter struct {
	mu      sync.Mutex
	history map[string][]int64
	window  int64 // window in seconds
	limit   int
}

func NewMemoryLimiter(limit int, windowSeconds int64) *MemoryLimiter {
	if windowSeconds <= 0 {
		windowSeconds = 60 // Default fallback to prevent ticker panic
	}
	m := &MemoryLimiter{
		history: make(map[string][]int64),
		window:  windowSeconds,
		limit:   limit,
	}
	// Periodic cleanup: evict exhausted identifier entries to prevent unbounded map growth.
	go func() {
		ticker := time.NewTicker(time.Duration(windowSeconds) * time.Second * 2)
		for range ticker.C {
			now := time.Now().Unix()
			clearBefore := now - windowSeconds
			m.mu.Lock()
			for id, timestamps := range m.history {
				var valid []int64
				for _, ts := range timestamps {
					if ts > clearBefore {
						valid = append(valid, ts)
					}
				}
				if len(valid) == 0 {
					delete(m.history, id) // Key has no active timestamps — safe to evict
				} else {
					m.history[id] = valid
				}
			}
			m.mu.Unlock()
		}
	}()
	return m
}

func (m *MemoryLimiter) Allow(ctx context.Context, identifier string) (bool, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().Unix()
	clearBefore := now - m.window

	timestamps := m.history[identifier]
	var valid []int64
	for _, ts := range timestamps {
		if ts > clearBefore {
			valid = append(valid, ts)
		}
	}

	if len(valid) < m.limit {
		valid = append(valid, now)
		m.history[identifier] = valid
		remaining := m.limit - len(valid)
		return true, remaining, nil
	}

	m.history[identifier] = valid
	return false, 0, nil
}

// RedisLimiter uses a Redis Lua script for atomic sliding window rate limiting.
type RedisLimiter struct {
	client *redis.Client
	limit  int
	window int64
	script *redis.Script
}

const slidingWindowLua = `
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local clearBefore = now - window

redis.call('ZREMRANGEBYSCORE', key, '-inf', clearBefore)
local currentRequests = redis.call('ZCARD', key)

if currentRequests < limit then
    redis.call('ZADD', key, now, now)
    redis.call('EXPIRE', key, window)
    return {1, limit - currentRequests - 1}
else
    return {0, 0}
end
`

func NewRedisLimiter(client *redis.Client, limit int, windowSeconds int64) *RedisLimiter {
	return &RedisLimiter{
		client: client,
		limit:  limit,
		window: windowSeconds,
		script: redis.NewScript(slidingWindowLua),
	}
}

func (r *RedisLimiter) Allow(ctx context.Context, identifier string) (bool, int, error) {
	key := "ratelimit:" + identifier
	now := time.Now().Unix()

	res, err := r.script.Run(ctx, r.client, []string{key}, now, r.window, r.limit).Slice()
	if err != nil {
		return true, r.limit, err // Fail open on Redis connectivity issues
	}

	allowedVal, _ := res[0].(int64)
	remainingVal, _ := res[1].(int64)

	return allowedVal == 1, int(remainingVal), nil
}

// MiddlewareHandlerLegacy provides backward compatibility for the older two-argument signature,
// hardcoding the retry-after window to 60 seconds.
func MiddlewareHandlerLegacy(limiter Limiter, limit int) func(http.Handler) http.Handler {
	return MiddlewareHandler(limiter, limit, 60)
}

// MiddlewareHandler creates an HTTP handler that enforces sliding-window rate limits.
// windowSeconds is the rate limit window duration and is reflected in the Retry-After header.
func MiddlewareHandler(limiter Limiter, limit int, windowSeconds int64) func(http.Handler) http.Handler {
	retryAfter := strconv.FormatInt(windowSeconds, 10)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}

			identifier := r.RemoteAddr
			if auth := r.Header.Get("Authorization"); auth != "" {
				identifier = strings.TrimPrefix(auth, "Bearer ")
			}

			allowed, remaining, err := limiter.Allow(r.Context(), identifier)
			if err == nil {
				w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
				w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
			}

			if !allowed {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", retryAfter)
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"message": fmt.Sprintf("Rate limit exceeded. Retry after %s seconds.", retryAfter),
						"type":    "rate_limit_error",
						"code":    429,
					},
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
