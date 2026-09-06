package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"toxitoken/internal/auth"
	"toxitoken/internal/cache"
	"toxitoken/internal/chaos"
	"toxitoken/internal/proxy"
	"toxitoken/internal/ratelimit"
	"toxitoken/pkg/mockupstream"
	"toxitoken/pkg/models"
)

func setupTestGateway(mockUpstreamURL string) (*httptest.Server, cache.CacheStore, ratelimit.Limiter) {
	r := chi.NewRouter()

	masterSecret := "test-secret"
	authMw := auth.NewMiddleware(masterSecret, nil)
	r.Use(authMw.Handler)

	cacheStore := cache.NewMemoryStore()
	limiter := ratelimit.NewMemoryLimiter(5, 60) // 5 reqs per 60s window
	r.Use(ratelimit.MiddlewareHandler(limiter, 5, 60))

	streamer := proxy.NewStreamer(cacheStore)
	replayer := cache.NewDualReplayer()

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	r.Post("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, `{"error":{"message":"bad body"}}`, http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		var req models.ChatCompletionRequest
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			http.Error(w, `{"error":{"message":"bad json"}}`, http.StatusBadRequest)
			return
		}

		fingerprint := req.Fingerprint()
		w.Header().Set("X-Fingerprint", fingerprint)

		// 1. Mock Mode
		if r.Header.Get("X-Proxy-Mode") == "mock" {
			mockText := "Deterministic mock response from toxitoken."
			if req.Stream {
				_ = replayer.ReplaySSE(r.Context(), w, "toxi-mock-engine", mockText, 1*time.Millisecond)
				return
			}
			_ = replayer.ReplayJSON(w, "toxi-mock-engine", mockText)
			return
		}

		// 2. Chaos
		var activeRule *chaos.Rule
		if chaosHeader := r.Header.Get("X-Chaos-Config"); chaosHeader != "" {
			if rule, err := chaos.ParseConfig(chaosHeader); err == nil && rule != nil && rule.ShouldApply() {
				activeRule = rule
				if activeRule.ApplyPreFlight(w) {
					return
				}
			}
		}

		// 3. Cache Hit
		if cachedText, hit, err := cacheStore.Get(r.Context(), fingerprint); err == nil && hit {
			if req.Stream {
				_ = replayer.ReplaySSE(r.Context(), w, req.Model, cachedText, 1*time.Millisecond)
				return
			}
			_ = replayer.ReplayJSON(w, req.Model, cachedText)
			return
		}

		// 4. Upstream Forwarding
		w.Header().Set("X-Cache", "MISS")
		upstreamReq, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, mockUpstreamURL, bytes.NewReader(bodyBytes))
		upstreamReq.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 0}
		resp, err := client.Do(upstreamReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		dropAfter := 0
		var onDrop func(http.ResponseWriter)
		if activeRule != nil && activeRule.DropAfterTokens > 0 {
			dropAfter = activeRule.DropAfterTokens
			onDrop = chaos.SeverConnection
		}

		if req.Stream {
			_ = streamer.ForwardAndRecord(r.Context(), w, resp.Body, fingerprint, dropAfter, onDrop)
			return
		}

		defer resp.Body.Close()
		respBytes, _ := io.ReadAll(resp.Body)
		var completion models.ChatCompletionResponse
		if err := json.Unmarshal(respBytes, &completion); err == nil && len(completion.Choices) > 0 {
			_ = cacheStore.Set(r.Context(), fingerprint, completion.Choices[0].Message.Content, 1*time.Hour)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBytes)
	})

	return httptest.NewServer(r), cacheStore, limiter
}

func TestE2EFullGatewayLifecycle(t *testing.T) {
	// 1. Start zero-cost mock upstream
	mockUpstream := mockupstream.NewServer()
	mockUpstream.ChunkDelay = 2 * time.Millisecond
	defer mockUpstream.Close()

	// 2. Start Gateway
	gateway, cacheStore, _ := setupTestGateway(mockUpstream.URL)
	defer gateway.Close()

	client := &http.Client{}

	// Test Case A: Health check (unauthenticated)
	t.Run("HealthCheck", func(t *testing.T) {
		resp, err := client.Get(gateway.URL + "/health")
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("health check failed: %v", err)
		}
	})

	// Test Case B: Auth verification (missing token -> 401)
	t.Run("AuthEnforcement", func(t *testing.T) {
		req, _ := http.NewRequest("POST", gateway.URL+"/v1/chat/completions", strings.NewReader(`{}`))
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 Unauthorized for missing token, got %v", resp.StatusCode)
		}
	})

	reqPayload := models.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []models.Message{
			{Role: "user", Content: "Test streaming message"},
		},
		Stream: true,
	}
	bodyBytes, _ := json.Marshal(reqPayload)

	// Test Case C: Streaming Pass-Through (Cache Miss)
	t.Run("StreamingPassThroughAndAccumulation", func(t *testing.T) {
		req, _ := http.NewRequest("POST", gateway.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
		req.Header.Set("Authorization", "Bearer test-secret")
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.Header.Get("X-Cache") != "MISS" {
			t.Errorf("expected X-Cache: MISS on first request, got %s", resp.Header.Get("X-Cache"))
		}

		reader := bufio.NewReader(resp.Body)
		var tokensReceived int
		var accumulated strings.Builder

		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				trimmed := bytes.TrimSpace(line)
				if bytes.HasPrefix(trimmed, []byte("data: ")) {
					payload := bytes.TrimPrefix(trimmed, []byte("data: "))
					if bytes.Equal(payload, []byte("[DONE]")) {
						break
					}
					var chunk models.ChatCompletionChunk
					if jsonErr := json.Unmarshal(payload, &chunk); jsonErr == nil && len(chunk.Choices) > 0 {
						tokensReceived++
						accumulated.WriteString(chunk.Choices[0].Delta.Content)
					}
				}
			}
			if err != nil {
				break
			}
		}

		if tokensReceived == 0 {
			t.Fatalf("expected streaming tokens, got 0")
		}
		if !strings.Contains(accumulated.String(), "Deterministic mock response") {
			t.Fatalf("unexpected content accumulated: %s", accumulated.String())
		}

		// Wait briefly for asynchronous cache save
		time.Sleep(50 * time.Millisecond)

		// Verify stored in cache
		fingerprint := reqPayload.Fingerprint()
		cached, hit, _ := cacheStore.Get(context.Background(), fingerprint)
		if !hit || cached == "" {
			t.Fatalf("expected response to be persisted in cache after stream finished")
		}
	})

	// Test Case D: Exact-Match Cache Hit (Dual-Replay SSE)
	t.Run("CacheHitSSE", func(t *testing.T) {
		req, _ := http.NewRequest("POST", gateway.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
		req.Header.Set("Authorization", "Bearer test-secret")
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("cache request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.Header.Get("X-Cache") != "HIT" {
			t.Fatalf("expected X-Cache: HIT, got %s", resp.Header.Get("X-Cache"))
		}
	})

	// Test Case E: Exact-Match Cache Hit (Dual-Replay JSON for stream: false)
	t.Run("CacheHitJSON", func(t *testing.T) {
		nonStreamReq := reqPayload
		nonStreamReq.Stream = false
		nsBytes, _ := json.Marshal(nonStreamReq)

		req, _ := http.NewRequest("POST", gateway.URL+"/v1/chat/completions", bytes.NewReader(nsBytes))
		req.Header.Set("Authorization", "Bearer test-secret")
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("non-streaming request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.Header.Get("X-Cache") != "HIT" {
			t.Fatalf("expected X-Cache: HIT for stream:false request, got %s", resp.Header.Get("X-Cache"))
		}

		var compResp models.ChatCompletionResponse
		if err := json.NewDecoder(resp.Body).Decode(&compResp); err != nil {
			t.Fatalf("failed to decode cached JSON response: %v", err)
		}
		if len(compResp.Choices) == 0 || compResp.Choices[0].Message.Content == "" {
			t.Fatalf("empty content in cached response")
		}
	})

	// Test Case F: CI Mock Mode (Bypasses Upstream)
	t.Run("CIMockMode", func(t *testing.T) {
		beforeCount := mockUpstream.RequestCount

		mockReqPayload := models.ChatCompletionRequest{
			Model: "brand-new-never-seen-model",
			Messages: []models.Message{
				{Role: "user", Content: "mock prompt"},
			},
			Stream: false,
		}
		mockBytes, _ := json.Marshal(mockReqPayload)

		req, _ := http.NewRequest("POST", gateway.URL+"/v1/chat/completions", bytes.NewReader(mockBytes))
		req.Header.Set("Authorization", "Bearer test-secret")
		req.Header.Set("X-Proxy-Mode", "mock")
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("mock mode failed: %v", err)
		}
		defer resp.Body.Close()

		afterCount := mockUpstream.RequestCount
		if afterCount != beforeCount {
			t.Fatalf("upstream called in mock mode: count increased from %d to %d", beforeCount, afterCount)
		}
	})

	// Test Case G: Chaos Fault Injection (Synthetic 504)
	t.Run("ChaosFaultInjection", func(t *testing.T) {
		req, _ := http.NewRequest("POST", gateway.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
		req.Header.Set("Authorization", "Bearer test-secret")
		req.Header.Set("X-Chaos-Config", "rate=1.0,status=504")
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("chaos request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusGatewayTimeout {
			t.Fatalf("expected HTTP 504 Gateway Timeout, got %d", resp.StatusCode)
		}
	})

	// Test Case H: Chaos Mid-Stream Socket Severing
	t.Run("ChaosMidStreamSevering", func(t *testing.T) {
		newReqPayload := models.ChatCompletionRequest{
			Model: "gpt-4o-mini",
			Messages: []models.Message{
				{Role: "user", Content: fmt.Sprintf("Unique query for chaos %d", time.Now().UnixNano())},
			},
			Stream: true,
		}
		newBytes, _ := json.Marshal(newReqPayload)

		req, _ := http.NewRequest("POST", gateway.URL+"/v1/chat/completions", bytes.NewReader(newBytes))
		req.Header.Set("Authorization", "Bearer test-secret")
		req.Header.Set("X-Chaos-Config", "rate=1.0,drop_after=2")
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			// Early socket drop can return error or severed stream
			return
		}
		defer resp.Body.Close()

		reader := bufio.NewReader(resp.Body)
		tokens := 0
		var readErr error
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 && bytes.HasPrefix(bytes.TrimSpace(line), []byte("data: {")) {
				tokens++
			}
			if err != nil {
				readErr = err
				break
			}
		}

		if readErr == nil {
			t.Fatalf("expected stream to be severed abruptly with read error, got clean finish")
		}
	})
}
