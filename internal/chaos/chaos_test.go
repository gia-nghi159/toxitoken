package chaos

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseConfig(t *testing.T) {
	header := "rate=0.4,delay=200ms,status=502,drop_after=7"
	rule, err := ParseConfig(header)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rule == nil {
		t.Fatalf("expected non-nil rule")
	}
	if rule.Rate != 0.4 {
		t.Errorf("expected rate 0.4, got %f", rule.Rate)
	}
	if rule.Delay != 200*time.Millisecond {
		t.Errorf("expected delay 200ms, got %v", rule.Delay)
	}
	if rule.StatusCode != 502 {
		t.Errorf("expected status 502, got %d", rule.StatusCode)
	}
	if rule.DropAfterTokens != 7 {
		t.Errorf("expected drop_after 7, got %d", rule.DropAfterTokens)
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
