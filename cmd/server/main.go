package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"toxitoken/internal/adapter"
	"toxitoken/internal/auth"
	"toxitoken/internal/cache"
	"toxitoken/internal/chaos"
	"toxitoken/internal/config"
	"toxitoken/internal/proxy"
	"toxitoken/internal/ratelimit"
	"toxitoken/pkg/models"
)

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, X-Chaos-Config, X-Proxy-Mode, Cache-Control")
		
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		
		next.ServeHTTP(w, r)
	})
}

func main() {
	cfg := config.Load()
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware)

	// Apply authentication middleware only to specific routes later.

	const rateLimitWindow int64 = 60 // seconds

	// Initialize cache store and rate limiter dependencies.
	var cacheStore cache.CacheStore
	var limiter ratelimit.Limiter

	if cfg.RedisURL != "" {
		rc, err := cache.NewRedisStore(cfg.RedisURL)
		if err != nil {
			log.Printf("Failed to connect to Redis at %s: %v. Falling back to in-memory.", cfg.RedisURL, err)
			cacheStore = cache.NewMemoryStore()
			limiter = ratelimit.NewMemoryLimiter(60, rateLimitWindow)
		} else {
			log.Println("Connected to Redis cache store & rate limiter.")
			cacheStore = rc
			// Assign shared Redis connection pool.
			limiter = ratelimit.NewRedisLimiter(rc.Client(), 60, rateLimitWindow)
		}
	} else {
		log.Println("REDIS_URL not configured. Operating with in-memory cache and sliding-window rate limiter.")
		cacheStore = cache.NewMemoryStore()
		limiter = ratelimit.NewMemoryLimiter(60, rateLimitWindow)
	}

	r.Use(ratelimit.MiddlewareHandler(limiter, 60, rateLimitWindow))

	streamer := proxy.NewStreamer(cacheStore)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","mode":"` + cfg.DefaultMode + `"}`))
	})

	r.Get("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"version":"1.0.0"}`))
	})

	// Serve the UI Playground
	r.Handle("/*", http.FileServer(http.Dir("./playground")))

	r.With(auth.Middleware).Post("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Failed to read request body"}}`))
			return
		}
		defer r.Body.Close()

		var req models.ChatCompletionRequest
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Malformed JSON"}}`))
			return
		}

		activeToken, _ := r.Context().Value(auth.ActiveTokenKey).(string)
		fingerprint := req.Fingerprint(activeToken)
		w.Header().Set("X-Fingerprint", fingerprint)

		// 1. CI Mock Mode Engine (X-Proxy-Mode: mock)
		proxyMode := strings.ToLower(r.Header.Get("X-Proxy-Mode"))
		if proxyMode == "mock" || cfg.DefaultMode == "mock" {
			mockText := "Deterministic mock response from toxitoken."
			if req.Stream {
				_ = cache.ReplaySSE(r.Context(), w, "toxi-mock-engine", mockText, 10*time.Millisecond)
				return
			}
			_ = cache.ReplayJSON(w, "toxi-mock-engine", mockText)
			return
		}

		// 2. Chaos & Fault Injection Engine
		var activeRule *chaos.Rule
		chaosHeader := r.Header.Get("X-Chaos-Config")
		if chaosHeader != "" {
			rule, err := chaos.ParseConfig(chaosHeader)
			if err == nil && rule != nil && rule.ShouldApply() {
				activeRule = rule
				if activeRule.ApplyPreFlight(w) {
					return
				}
				if activeRule.FuzzInjection != "" && len(req.Messages) > 0 {
					req.Messages[len(req.Messages)-1].Content += "\n\n[SYSTEM OVERRIDE]: " + activeRule.FuzzInjection
					bodyBytes, _ = json.Marshal(req)
					fingerprint = req.Fingerprint(activeToken)
					w.Header().Set("X-Fingerprint", fingerprint)
				}
			}
		}

		// 3. Exact-Match Cache Check (SHA-256 Prompt Fingerprint)
		if strings.ToLower(r.Header.Get("Cache-Control")) != "no-cache" {
			if cachedText, hit, err := cacheStore.Get(r.Context(), fingerprint); err == nil && hit {
				if req.Stream {
					_ = cache.ReplaySSE(r.Context(), w, req.Model, cachedText, 10*time.Millisecond)
					return
				}
				_ = cache.ReplayJSON(w, req.Model, cachedText)
				return
			}
		}

		// 4. Provider Translation: Google Gemini vs OpenAI Native
		if adapter.IsGeminiModel(req.Model) {
			w.Header().Set("X-Cache", "MISS")
			geminiReq, convErr := adapter.ConvertOpenAIToGemini(&req)
			if convErr != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(fmt.Sprintf(`{"error":{"message":"Gemini conversion failed: %s"}}`, convErr.Error())))
				return
			}

			// Resolve the Gemini Key (Sponsored vs BYOK)
			geminiKey := cfg.GeminiKey
			if activeToken != "" {
				geminiKey = activeToken
			}

			if req.Stream {
				_ = adapter.ForwardGeminiStream(r.Context(), w, geminiReq, req.Model, geminiKey, "", activeRule)
				return
			}

			// Non-streaming Gemini: call generateContent, write OpenAI-compatible JSON, and cache the result
			if candidateText, err := adapter.ForwardGeminiSync(r.Context(), w, geminiReq, req.Model, geminiKey, ""); err == nil && candidateText != "" {
				_ = cacheStore.Set(r.Context(), fingerprint, candidateText, 24*time.Hour)
			}
			return
		}

		// 5. Standard OpenAI Upstream Forwarding (Cache Miss)
		w.Header().Set("X-Cache", "MISS")
		
		// If it's not a BYOK token, reject it because we don't sponsor OpenAI anymore
		if activeToken == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":{"message":"OpenAI requires a Bring Your Own Key (BYOK). Please provide your OpenAI API key in the token flag.","code":402}}`))
			return
		}

		upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, cfg.UpstreamURL, bytes.NewReader(bodyBytes))
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"Failed to construct upstream request"}}`))
			return
		}

		upstreamReq.Header.Set("Content-Type", "application/json")
		upstreamReq.Header.Set("Authorization", "Bearer "+activeToken)

		client := &http.Client{Timeout: 0}
		resp, err := client.Do(upstreamReq)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":{"message":"Upstream failure: %s"}}`, err.Error())))
			return
		}

		// Non-200 upstream error handling
		if resp.StatusCode != http.StatusOK {
			defer resp.Body.Close()
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
			return
		}

		// Mid-stream chaos configuration
		dropAfter := 0
		tokenDripMs := 0
		dropChunkProb := 0.0
		var onDrop func(http.ResponseWriter)
		if activeRule != nil {
			if activeRule.DropAfterTokens > 0 {
				dropAfter = activeRule.DropAfterTokens
				onDrop = chaos.SeverConnection
			}
			tokenDripMs = activeRule.TokenDripDelayMs
			dropChunkProb = activeRule.DropChunkProbability
		}

		if req.Stream {
			_ = streamer.ForwardAndRecord(r.Context(), w, resp.Body, fingerprint, dropAfter, onDrop, tokenDripMs, dropChunkProb)
			return
		}

		// Non-streaming pass-through & Cache Recording
		// hopByHop lists headers that must NOT be forwarded from upstream to downstream
		// to avoid HTTP framing conflicts (e.g. Transfer-Encoding when body is already buffered).
		hopByHop := map[string]bool{
			"Transfer-Encoding": true,
			"Content-Length":    true,
			"Connection":        true,
			"Keep-Alive":        true,
			"Trailer":           true,
			"Upgrade":           true,
			"Te":                true,
		}
		defer resp.Body.Close()
		respBodyBytes, err := io.ReadAll(resp.Body)
		if err == nil {
			var completionResp models.ChatCompletionResponse
			if err := json.Unmarshal(respBodyBytes, &completionResp); err == nil && len(completionResp.Choices) > 0 {
				_ = cacheStore.Set(r.Context(), fingerprint, completionResp.Choices[0].Message.Content, 24*time.Hour)
			}
		}

		for k, v := range resp.Header {
			if !hopByHop[k] {
				w.Header()[k] = v
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBodyBytes)
	})

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // Unbounded write timeout required for persistent SSE streams
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("Toxitoken Edge Gateway running on port %s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server listen error: %s", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down gateway...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
