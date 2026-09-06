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

	fp1 := req1.Fingerprint()
	fp2 := req2.Fingerprint()

	if fp1 != fp2 {
		t.Fatalf("expected identical fingerprints, got %s vs %s", fp1, fp2)
	}

	// Changing temperature must change fingerprint
	temp3 := 0.8
	req3 := req1
	req3.Temperature = &temp3
	if req3.Fingerprint() == fp1 {
		t.Fatalf("expected different fingerprint for different temperature")
	}

	// Changing content must change fingerprint
	req4 := req1
	req4.Messages = []Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hello world!"}, // added exclamation mark
	}
	if req4.Fingerprint() == fp1 {
		t.Fatalf("expected different fingerprint for different content")
	}
}
