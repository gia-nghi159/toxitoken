package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"toxitoken/internal/chaos"
	"toxitoken/pkg/models"
)

// GeminiPart represents a piece of content in Gemini's API.
type GeminiPart struct {
	Text string `json:"text"`
}

// GeminiContent represents an entry in Gemini's contents array.
type GeminiContent struct {
	Role  string       `json:"role"` // "user" or "model"
	Parts []GeminiPart `json:"parts"`
}

// GeminiRequest is the payload sent to Google Gemini generateContent / streamGenerateContent.
type GeminiRequest struct {
	Contents []GeminiContent `json:"contents"`
}

// GeminiResponse represents the top-level structure of a Gemini completion response.
type GeminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []GeminiPart `json:"parts"`
			Role  string       `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
}

// IsGeminiModel checks if the requested model string targets a Gemini engine.
func IsGeminiModel(model string) bool {
	lower := strings.ToLower(model)
	return strings.HasPrefix(lower, "gemini")
}

// ConvertOpenAIToGemini translates standard OpenAI messages to Gemini contents.
func ConvertOpenAIToGemini(req *models.ChatCompletionRequest) (*GeminiRequest, error) {
	var contents []GeminiContent

	for _, msg := range req.Messages {
		role := "user"
		if strings.ToLower(msg.Role) == "assistant" {
			role = "model"
		}

		contents = append(contents, GeminiContent{
			Role: role,
			Parts: []GeminiPart{
				{Text: msg.Content},
			},
		})
	}

	return &GeminiRequest{Contents: contents}, nil
}

// ForwardGeminiStream executes a streaming request to Gemini and pipes normalized OpenAI SSE events.
func ForwardGeminiStream(
	ctx context.Context,
	w http.ResponseWriter,
	geminiReq *GeminiRequest,
	model string,
	apiKey string,
	baseURL string,
	activeRule *chaos.Rule,
) error {
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?key=%s&alt=sse", baseURL, model, apiKey)

	bodyBytes, err := json.Marshal(geminiReq)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, err = io.Copy(w, resp.Body)
		return err
	}

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	reader := bufio.NewReader(resp.Body)
	cmplID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())

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
	tokenCount := 0

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if bytes.HasPrefix(trimmed, []byte("data: ")) {
				payload := bytes.TrimPrefix(trimmed, []byte("data: "))
				var gResp GeminiResponse
				if jsonErr := json.Unmarshal(payload, &gResp); jsonErr == nil && len(gResp.Candidates) > 0 {
					parts := gResp.Candidates[0].Content.Parts
					if len(parts) > 0 && parts[0].Text != "" {
						if dropAfter > 0 && tokenCount >= dropAfter && onDrop != nil {
							onDrop(w)
							return nil
						}
						if dropChunkProb > 0 && rand.Float64() < dropChunkProb {
							continue
						}
						if tokenDripMs > 0 {
							time.Sleep(time.Duration(tokenDripMs) * time.Millisecond)
						}
						
						tokenCount++
						
						chunk := models.ChatCompletionChunk{
							ID:      cmplID,
							Object:  "chat.completion.chunk",
							Created: time.Now().Unix(),
							Model:   model,
							Choices: []models.ChunkChoice{
								{
									Index: 0,
									Delta: models.ChunkDelta{
										Content: parts[0].Text,
									},
								},
							},
						}
						chunkJSON, _ := json.Marshal(chunk)
						_, _ = fmt.Fprintf(w, "data: %s\n\n", chunkJSON)
						_ = rc.Flush()
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

	// Terminal OpenAI SSE chunk
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	_ = rc.Flush()
	return nil
}

// ForwardGeminiSync executes a non-streaming request to Gemini and returns
// the complete text of the first candidate, along with an OpenAI-compatible
// JSON body written directly to w.
// Returns (completionText, error) so callers can persist the result to cache.
func ForwardGeminiSync(
	ctx context.Context,
	w http.ResponseWriter,
	geminiReq *GeminiRequest,
	model string,
	apiKey string,
	baseURL string,
) (string, error) {
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", baseURL, model, apiKey)

	bodyBytes, err := json.Marshal(geminiReq)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBytes)
		return "", nil
	}

	// Parse Gemini response and extract candidate text
	var gResp GeminiResponse
	if err := json.Unmarshal(respBytes, &gResp); err != nil || len(gResp.Candidates) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"Gemini returned empty candidates","type":"provider_error","code":502}}`))
		return "", fmt.Errorf("empty candidates in Gemini response")
	}

	var textBuilder strings.Builder
	for _, part := range gResp.Candidates[0].Content.Parts {
		textBuilder.WriteString(part.Text)
	}
	candidateText := textBuilder.String()

	// Return an OpenAI-compatible ChatCompletionResponse
	completionResp := models.ChatCompletionResponse{
		ID:      fmt.Sprintf("gemini-cmpl-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []models.ResponseChoice{
			{
				Index: 0,
				Message: models.Message{
					Role:    "assistant",
					Content: candidateText,
				},
				FinishReason: strings.ToLower(gResp.Candidates[0].FinishReason),
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(completionResp)
	return candidateText, nil
}
