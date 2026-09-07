package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"toxitoken/pkg/models"
)

type StreamRecorder interface {
	SaveCompletion(ctx context.Context, key string, text string) error
}

type Streamer struct {
	Recorder StreamRecorder
}

func NewStreamer(recorder StreamRecorder) *Streamer {
	return &Streamer{Recorder: recorder}
}

// ForwardAndRecord pipes raw SSE lines directly to the client while accumulating text deltas.
func (s *Streamer) ForwardAndRecord(
	ctx context.Context,
	w http.ResponseWriter,
	upstreamBody io.ReadCloser,
	cacheKey string,
	dropAfterTokens int,
	onDrop func(http.ResponseWriter),
	tokenDripMs int,
	dropChunkProb float64,
) error {
	defer upstreamBody.Close()

	rc := http.NewResponseController(w)

	// Set required headers for zero-buffer edge streaming
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	reader := bufio.NewReader(upstreamBody)
	var contentAccumulator strings.Builder
	tokenCount := 0

	for {
		// Read line-by-line without 64KB max buffer constraint
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			// Mid-Stream Chaos Severing trigger
			if dropAfterTokens > 0 && tokenCount >= dropAfterTokens && onDrop != nil {
				onDrop(w)
				return nil
			}

			// Data Loss Chaos Simulation (Drop Chunk)
			if dropChunkProb > 0 && bytes.HasPrefix(bytes.TrimSpace(line), []byte("data: ")) {
				if rand.Float64() < dropChunkProb {
					continue // Skip writing this chunk to simulate TCP packet loss
				}
			}

			// Token Drip Chaos Simulation (Latency Jitter)
			if tokenDripMs > 0 && bytes.HasPrefix(bytes.TrimSpace(line), []byte("data: ")) {
				time.Sleep(time.Duration(tokenDripMs) * time.Millisecond)
			}

			// Immediate zero-buffer flush to downstream client
			if _, writeErr := w.Write(line); writeErr != nil {
				return writeErr
			}
			if flushErr := rc.Flush(); flushErr != nil {
				return flushErr
			}

			// Intercept and accumulate tokens for caching
			trimmed := bytes.TrimSpace(line)
			if bytes.HasPrefix(trimmed, []byte("data: ")) {
				payload := bytes.TrimPrefix(trimmed, []byte("data: "))
				if bytes.Equal(payload, []byte("[DONE]")) {
					// Read the remaining trailing empty line of SSE frame if present
					if nextLine, _ := reader.ReadBytes('\n'); len(nextLine) > 0 {
						_, _ = w.Write(nextLine)
						_ = rc.Flush()
					}
					break
				}

				var chunk models.ChatCompletionChunk
				if err := json.Unmarshal(payload, &chunk); err == nil && len(chunk.Choices) > 0 {
					textDelta := chunk.Choices[0].Delta.Content
					if textDelta != "" {
						// tokenCount tracks non-empty content deltas only.
						// Role-only preamble chunks and finish_reason frames are not counted.
						// dropAfterTokens must be interpreted relative to content tokens, not raw SSE frames.
						tokenCount++
						contentAccumulator.WriteString(textDelta)
					}
				}
			}
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}

	// Persist accumulated result asynchronously
	if s.Recorder != nil && cacheKey != "" && contentAccumulator.Len() > 0 {
		go func(key, text string) {
			_ = s.Recorder.SaveCompletion(context.Background(), key, text)
		}(cacheKey, contentAccumulator.String())
	}

	return nil
}
