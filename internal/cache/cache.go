package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"toxitoken/pkg/models"
)

// CacheStore represents the persistence layer for exact-match prompt completions.
type CacheStore interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key string, text string, ttl time.Duration) error
	SaveCompletion(ctx context.Context, key string, text string) error
}

// memoryItem holds cached text with an absolute expiry timestamp.
type memoryItem struct {
	text      string
	expiresAt time.Time
}

// MemoryStore provides a thread-safe, zero-dependency in-memory cache with TTL.
type MemoryStore struct {
	mu    sync.RWMutex
	items map[string]memoryItem
}

func NewMemoryStore() *MemoryStore {
	m := &MemoryStore{
		items: make(map[string]memoryItem),
	}
	// Periodic cleanup goroutine
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			m.cleanup()
		}
	}()
	return m
}

func (m *MemoryStore) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for k, item := range m.items {
		if now.After(item.expiresAt) {
			delete(m.items, k)
		}
	}
}

func (m *MemoryStore) Get(ctx context.Context, key string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	item, exists := m.items[key]
	if !exists {
		return "", false, nil
	}
	if time.Now().After(item.expiresAt) {
		return "", false, nil
	}
	return item.text, true, nil
}

func (m *MemoryStore) Set(ctx context.Context, key string, text string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[key] = memoryItem{
		text:      text,
		expiresAt: time.Now().Add(ttl),
	}
	return nil
}

// SaveCompletion satisfies the proxy.StreamRecorder interface.
func (m *MemoryStore) SaveCompletion(ctx context.Context, key string, text string) error {
	return m.Set(ctx, key, text, 24*time.Hour)
}

// RedisStore provides Redis-backed persistence via go-redis.
type RedisStore struct {
	client *redis.Client
}

func NewRedisStore(redisURL string) (*RedisStore, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis url: %w", err)
	}
	client := redis.NewClient(opt)
	return &RedisStore{client: client}, nil
}

// Client returns the underlying Redis client so callers (e.g. the rate limiter)
// can share the same connection pool instead of opening a second one.
func (r *RedisStore) Client() *redis.Client {
	return r.client
}

func (r *RedisStore) Get(ctx context.Context, key string) (string, bool, error) {
	val, err := r.client.Get(ctx, "cache:"+key).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return val, true, nil
}

func (r *RedisStore) Set(ctx context.Context, key string, text string, ttl time.Duration) error {
	return r.client.Set(ctx, "cache:"+key, text, ttl).Err()
}

func (r *RedisStore) SaveCompletion(ctx context.Context, key string, text string) error {
	return r.Set(ctx, key, text, 24*time.Hour)
}

// ReplayJSON returns an instant OpenAI ChatCompletionResponse with X-Cache: HIT.
func ReplayJSON(w http.ResponseWriter, model, cachedText string) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "HIT")
	// Compute response payload
	words := strings.Fields(cachedText)
	resp := models.ChatCompletionResponse{
		ID:      "cache-cmpl-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []models.ResponseChoice{{
			Index: 0,
			Message: models.Message{Role: "assistant", Content: cachedText},
			FinishReason: "stop",
		}},
		Usage: models.Usage{PromptTokens: 10, CompletionTokens: len(words), TotalTokens: 10 + len(words)},
	}
	respBytes, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(respBytes)))
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(respBytes)
	return err
}

// ReplaySSE emits synthetic 2-to-4 word fragments with 10ms pacing to emulate streaming.
func ReplaySSE(ctx context.Context, w http.ResponseWriter, model, cachedText string, chunkDelay time.Duration) error {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Cache", "HIT")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	words := strings.Fields(cachedText)
	if len(words) == 0 {
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		_ = rc.Flush()
		return nil
	}

	cmplID := "cache-chunk-" + fmt.Sprintf("%d", time.Now().UnixNano())
	i := 0
	for i < len(words) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Bundle 2-3 words per chunk for realistic token cadence
		chunkSize := 3
		if i+chunkSize > len(words) {
			chunkSize = len(words) - i
		}
		fragment := strings.Join(words[i:i+chunkSize], " ")
		if i+chunkSize < len(words) {
			fragment += " "
		}
		i += chunkSize

		chunk := models.ChatCompletionChunk{
			ID:      cmplID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   model,
			Choices: []models.ChunkChoice{
				{
					Index: 0,
					Delta: models.ChunkDelta{
						Content: fragment,
					},
				},
			},
		}

		b, _ := json.Marshal(chunk)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return err
		}
		if err := rc.Flush(); err != nil {
			return err
		}

		if chunkDelay > 0 {
			time.Sleep(chunkDelay)
		}
	}

	// Terminal chunk
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	_ = rc.Flush()
	return nil
}
