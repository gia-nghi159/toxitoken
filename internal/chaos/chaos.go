package chaos

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Rule defines parameters for chaos injection.
type Rule struct {
	Rate            float64       // Define probability (0.0 to 1.0).
	Delay           time.Duration // Define pre-flight latency.
	StatusCode      int           // Define artificial HTTP error status code (e.g., 429, 500).
	DropAfterTokens int           // Sever stream after flushing N content tokens.
}

// ParseConfig parses chaos configuration header.
// Example: "rate=0.4,delay=500ms,status=504,drop_after=10"
func ParseConfig(header string) (*Rule, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, nil
	}

	rule := &Rule{
		Rate: 1.0, // Default to 1.0 if not specified
	}

	parts := strings.Split(header, ",")
	for _, part := range parts {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(kv[0]))
		v := strings.TrimSpace(kv[1])

		switch k {
		case "rate":
			if r, err := strconv.ParseFloat(v, 64); err == nil {
				if r < 0 {
					r = 0
				} else if r > 1 {
					r = 1
				}
				rule.Rate = r
			}
		case "delay":
			if d, err := time.ParseDuration(v); err == nil {
				rule.Delay = d
			}
		case "status":
			if s, err := strconv.Atoi(v); err == nil {
				rule.StatusCode = s
			}
		case "drop_after":
			if da, err := strconv.Atoi(v); err == nil {
				rule.DropAfterTokens = da
			}
		}
	}

	return rule, nil
}

// ShouldApply computes probability draw against rule rate.
func (r *Rule) ShouldApply() bool {
	if r == nil || r.Rate <= 0 {
		return false
	}
	if r.Rate >= 1.0 {
		return true
	}
	return rand.Float64() < r.Rate
}

// ApplyPreFlight executes configured latency delay and writes synthetic errors.
func (r *Rule) ApplyPreFlight(w http.ResponseWriter) bool {
	if r == nil {
		return false
	}

	if r.Delay > 0 {
		time.Sleep(r.Delay)
	}

	if r.StatusCode >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(r.StatusCode)
		errResp := map[string]any{
			"error": map[string]any{
				"message": fmt.Sprintf("Chaos injected error: HTTP %d", r.StatusCode),
				"type":    "chaos_error",
				"code":    r.StatusCode,
			},
		}
		_ = json.NewEncoder(w).Encode(errResp)
		return true
	}

	return false
}
