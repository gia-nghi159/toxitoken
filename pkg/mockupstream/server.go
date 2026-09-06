package mockupstream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"

	"toxitoken/pkg/models"
)

// Server is a local zero-cost upstream mock server that simulates OpenAI API behavior.
type Server struct {
	*httptest.Server
	ChunkDelay     time.Duration
	RequestCount   int64
	SimulateError  int // e.g. 401, 429, 500
	CustomResponse string
}

// NewServer starts and returns a mock upstream server.
func NewServer() *Server {
	mock := &Server{
		ChunkDelay: 10 * time.Millisecond,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&mock.RequestCount, 1)

		if mock.SimulateError > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(mock.SimulateError)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":{"message":"Simulated upstream error","type":"server_error","code":%d}}`, mock.SimulateError)))
			return
		}

		var req models.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid request body"}}`))
			return
		}

		respText := "Deterministic mock response from toxitoken."
		if mock.CustomResponse != "" {
			respText = mock.CustomResponse
		}

		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(http.StatusOK)

			rc := http.NewResponseController(w)
			words := strings.Fields(respText)

			for i, word := range words {
				chunkText := word
				if i < len(words)-1 {
					chunkText += " "
				}

				chunk := models.ChatCompletionChunk{
					ID:      "mock-cmpl-chunk",
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   req.Model,
					Choices: []models.ChunkChoice{
						{
							Index: 0,
							Delta: models.ChunkDelta{
								Content: chunkText,
							},
						},
					},
				}
				chunkBytes, _ := json.Marshal(chunk)
				_, _ = fmt.Fprintf(w, "data: %s\n\n", chunkBytes)
				_ = rc.Flush()

				if mock.ChunkDelay > 0 {
					time.Sleep(mock.ChunkDelay)
				}
			}

			// Terminal SSE event
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			_ = rc.Flush()
			return
		}

		// Non-streaming response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := models.ChatCompletionResponse{
			ID:      "mock-cmpl-toxi",
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   req.Model,
			Choices: []models.ResponseChoice{
				{
					Index: 0,
					Message: models.Message{
						Role:    "assistant",
						Content: respText,
					},
					FinishReason: "stop",
				},
			},
			Usage: models.Usage{
				PromptTokens:     10,
				CompletionTokens: len(strings.Fields(respText)),
				TotalTokens:      10 + len(strings.Fields(respText)),
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	mock.Server = httptest.NewServer(handler)
	return mock
}
