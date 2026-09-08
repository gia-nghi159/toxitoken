package adapter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"toxitoken/pkg/models"
)

func TestConvertOpenAIToGemini(t *testing.T) {
	req := &models.ChatCompletionRequest{
		Model: "gemini-2.5-flash",
		Messages: []models.Message{
			{Role: "user", Content: "Hello Gemini"},
			{Role: "assistant", Content: "Hello user"},
		},
	}

	gReq, err := ConvertOpenAIToGemini(req)
	if err != nil {
		t.Fatalf("unexpected conversion error: %v", err)
	}

	if len(gReq.Contents) != 2 {
		t.Fatalf("expected 2 contents, got %d", len(gReq.Contents))
	}
	if gReq.Contents[0].Role != "user" || gReq.Contents[0].Parts[0].Text != "Hello Gemini" {
		t.Errorf("unexpected content 0: %+v", gReq.Contents[0])
	}
	if gReq.Contents[1].Role != "model" || gReq.Contents[1].Parts[0].Text != "Hello user" {
		t.Errorf("unexpected content 1: %+v", gReq.Contents[1])
	}
}

func TestForwardGeminiStream(t *testing.T) {
	// Mock Gemini Upstream Server
	geminiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: {\"candidates\": [{\"content\": {\"parts\": [{\"text\": \"Hello from Gemini!\"}], \"role\": \"model\"}, \"finishReason\": \"STOP\"}]}\n\n")
	}))
	defer geminiServer.Close()

	gReq := &GeminiRequest{
		Contents: []GeminiContent{
			{Role: "user", Parts: []GeminiPart{{Text: "Hi"}}},
		},
	}

	rec := httptest.NewRecorder()
	err := ForwardGeminiStream(context.Background(), rec, gReq, "gemini-2.5-flash", "test-key", geminiServer.URL, nil)
	if err != nil {
		t.Fatalf("ForwardGeminiStream failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "chat.completion.chunk") {
		t.Errorf("expected OpenAI format chunk, got: %s", body)
	}
	if !strings.Contains(body, "Hello from Gemini!") {
		t.Errorf("expected translated delta content, got: %s", body)
	}
	if !strings.Contains(body, "data: [DONE]\n\n") {
		t.Errorf("expected terminal [DONE] event, got: %s", body)
	}
}
