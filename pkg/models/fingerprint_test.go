package models

import (
	"testing"
)

func TestFingerprintDeterminism(t *testing.T) {
	temp1 := 0.7
	temp2 := 0.70000

	req1 := ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Hello world"},
		},
		Temperature: &temp1,
	}

	req2 := ChatCompletionRequest{
		Model: "  GPT-4O-MINI ",
		Messages: []Message{
			{Role: "system ", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Hello world"},
		},
		Temperature: &temp2,
	}

	fp1 := req1.Fingerprint("token1")
	fp2 := req2.Fingerprint("token1")

	if fp1 != fp2 {
		t.Fatalf("expected identical fingerprints, got %s vs %s", fp1, fp2)
	}

	// Verify temperature change alters fingerprint.
	temp3 := 0.8
	req3 := req1
	req3.Temperature = &temp3
	if req3.Fingerprint("token1") == fp1 {
		t.Fatalf("expected different fingerprint for different temperature")
	}

	// Verify content change alters fingerprint.
	req4 := req1
	req4.Messages = []Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hello world!"}, // Exclamation mark alters string content.
	}
	if req4.Fingerprint("token1") == fp1 {
		t.Fatalf("expected different fingerprint for different content")
	}

	// Verify different token alters fingerprint
	if req1.Fingerprint("token2") == fp1 {
		t.Fatalf("expected different fingerprint for different token (BYOK cache partitioning)")
	}
}
