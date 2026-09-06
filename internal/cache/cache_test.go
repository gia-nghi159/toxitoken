package cache

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"toxitoken/pkg/models"
)

func TestMemoryStore(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	// Cache miss
	_, hit, err := store.Get(ctx, "nonexistent")
	if err != nil || hit {
		t.Fatalf("expected miss, got hit=%v err=%v", hit, err)
	}

	// Set and Cache Hit
	err = store.Set(ctx, "hash123", "Hello world completion", 1*time.Minute)
	if err != nil {
		t.Fatalf("failed to set cache: %v", err)
	}

	val, hit, err := store.Get(ctx, "hash123")
	if err != nil || !hit || val != "Hello world completion" {
		t.Fatalf("expected hit with 'Hello world completion', got hit=%v val=%s err=%v", hit, val, err)
	}
}

func TestDualReplayJSON(t *testing.T) {
	replayer := NewDualReplayer()
	rec := httptest.NewRecorder()

	err := replayer.ReplayJSON(rec, "gpt-4o-mini", "Hello world from cache")
	if err != nil {
		t.Fatalf("ReplayJSON failed: %v", err)
	}

	if rec.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected X-Cache: HIT, got %s", rec.Header().Get("X-Cache"))
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("expected application/json Content-Type, got %s", rec.Header().Get("Content-Type"))
	}

	var resp models.ChatCompletionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content != "Hello world from cache" {
		t.Fatalf("unexpected content in response: %+v", resp)
	}
}

func TestDualReplaySSE(t *testing.T) {
	replayer := NewDualReplayer()
	rec := httptest.NewRecorder()

	err := replayer.ReplaySSE(context.Background(), rec, "gpt-4o-mini", "One two three four five", 1*time.Millisecond)
	if err != nil {
		t.Fatalf("ReplaySSE failed: %v", err)
	}

	if rec.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected X-Cache: HIT, got %s", rec.Header().Get("X-Cache"))
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("expected text/event-stream Content-Type, got %s", rec.Header().Get("Content-Type"))
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data: [DONE]\n\n") {
		t.Fatalf("expected terminal [DONE] event, got body: %s", body)
	}
	if !strings.Contains(body, "One two three") {
		t.Fatalf("expected chunked words in SSE output, got: %s", body)
	}
}
