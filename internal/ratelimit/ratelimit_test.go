package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMemoryLimiter(t *testing.T) {
	ctx := context.Background()
	limiter := NewMemoryLimiter(3, 10) // 3 requests per 10 seconds

	// 1st request
	allowed, remaining, err := limiter.Allow(ctx, "user-1")
	if err != nil || !allowed || remaining != 2 {
		t.Fatalf("1st request failed: allowed=%v remaining=%d", allowed, remaining)
	}

	// 2nd request
	allowed, remaining, _ = limiter.Allow(ctx, "user-1")
	if !allowed || remaining != 1 {
		t.Fatalf("2nd request failed: allowed=%v remaining=%d", allowed, remaining)
	}

	// 3rd request
	allowed, remaining, _ = limiter.Allow(ctx, "user-1")
	if !allowed || remaining != 0 {
		t.Fatalf("3rd request failed: allowed=%v remaining=%d", allowed, remaining)
	}

	// 4th request (should be throttled)
	allowed, _, _ = limiter.Allow(ctx, "user-1")
	if allowed {
		t.Fatalf("4th request should have been rejected by rate limiter")
	}

	// Different user should still be allowed
	allowed, remaining, _ = limiter.Allow(ctx, "user-2")
	if !allowed || remaining != 2 {
		t.Fatalf("user-2 should have been allowed")
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	limiter := NewMemoryLimiter(1, 10)
	mw := MiddlewareHandler(limiter, 1, 10)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	// 1st call -> 200 OK
	req1 := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req1.Header.Set("Authorization", "Bearer token-a")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec1.Code)
	}

	// 2nd call -> 429 Too Many Requests
	req2 := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req2.Header.Set("Authorization", "Bearer token-a")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests, got %d", rec2.Code)
	}
	if rec2.Header().Get("Retry-After") != "10" {
		t.Fatalf("expected Retry-After: 10 (window), got %s", rec2.Header().Get("Retry-After"))
	}
}
