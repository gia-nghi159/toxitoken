package chaos

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseConfig(t *testing.T) {
	tests := []struct {
		name          string
		header        string
		expectedRate  float64
		expectedDelay time.Duration
		expectedCode  int
		expectedDrop  int
		expectedDrip  int
		expectedInject string
		expectedChunkProb float64
	}{
		{
			name:   "empty header",
			header: "",
		},
		{
			name:          "valid full configuration",
			header:        "rate=0.4,delay=200ms,status=502,drop_after=7,drip=1500,inject=Ignore rules,lose_chunk=0.25",
			expectedRate:  0.4,
			expectedDelay: 200 * time.Millisecond,
			expectedCode:  502,
			expectedDrop:  7,
			expectedDrip:  1500,
			expectedInject: "Ignore rules",
			expectedChunkProb: 0.25,
		},
		{
			name:          "invalid values fall back or ignore",
			header:        "rate=abc,delay=-50,status=notint,drop_after=1.5,drip=fast,lose_chunk=huge",
			expectedRate:  1.0, // default
			expectedDelay: 0,
			expectedCode:  0,
			expectedDrop:  0,
			expectedDrip:  0,
			expectedInject: "",
			expectedChunkProb: 0,
		},
		{
			name:          "out of bounds probabilities",
			header:        "rate=5.0,lose_chunk=-0.5",
			expectedRate:  1.0, // clamped
			expectedChunkProb: 0.0, // clamped
		},
		{
			name:          "spaces and casing",
			header:        " Rate = 0.5 , DELAY = 1s, INJECT= Speak French  ",
			expectedRate:  0.5,
			expectedDelay: 1 * time.Second,
			expectedInject: "Speak French",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule, err := ParseConfig(tt.header)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.header == "" {
				if rule != nil {
					t.Fatalf("expected nil rule for empty header")
				}
				return
			}
			if rule == nil {
				t.Fatalf("expected non-nil rule")
			}
			if rule.Rate != tt.expectedRate {
				t.Errorf("expected rate %f, got %f", tt.expectedRate, rule.Rate)
			}
			if rule.Delay != tt.expectedDelay {
				t.Errorf("expected delay %v, got %v", tt.expectedDelay, rule.Delay)
			}
			if rule.StatusCode != tt.expectedCode {
				t.Errorf("expected status %d, got %d", tt.expectedCode, rule.StatusCode)
			}
			if rule.DropAfterTokens != tt.expectedDrop {
				t.Errorf("expected drop_after %d, got %d", tt.expectedDrop, rule.DropAfterTokens)
			}
			if rule.TokenDripDelayMs != tt.expectedDrip {
				t.Errorf("expected drip %d, got %d", tt.expectedDrip, rule.TokenDripDelayMs)
			}
			if rule.FuzzInjection != tt.expectedInject {
				t.Errorf("expected inject %q, got %q", tt.expectedInject, rule.FuzzInjection)
			}
			if rule.DropChunkProbability != tt.expectedChunkProb {
				t.Errorf("expected drop_chunk_prob %f, got %f", tt.expectedChunkProb, rule.DropChunkProbability)
			}
		})
	}
}

func TestApplyPreFlightFault(t *testing.T) {
	rule := &Rule{
		Rate:       1.0,
		StatusCode: 504,
	}

	rec := httptest.NewRecorder()
	handled := rule.ApplyPreFlight(rec)

	if !handled {
		t.Fatalf("expected preflight to handle error")
	}
	if rec.Code != 504 {
		t.Fatalf("expected 504 status code, got %d", rec.Code)
	}
}

func TestSeverConnectionFallbackToErrAbortHandler(t *testing.T) {
	rec := httptest.NewRecorder() // Does not implement Hijacker

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic on unsupported hijack, got nil")
		}
		if r != http.ErrAbortHandler {
			t.Fatalf("expected panic(http.ErrAbortHandler), got %v", r)
		}
	}()

	SeverConnection(rec)
}
