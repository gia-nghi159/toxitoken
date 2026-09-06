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

// Rule defines the parameters for chaos injection.
type Rule struct {
	Rate            float64       // Probability (0.0 to 1.0)
	Delay           time.Duration // Pre-flight latency
	StatusCode      int           // Artificial HTTP error status code (e.g. 429, 500, 502, 504)
	DropAfterTokens int           // Sever stream after flushing N *content* tokens (does not count system/wrapper tokens)
}

// ParseConfig parses a chaos configuration header.
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

// ShouldApply returns true if a random draw falls under the rule's rate.
func (r *Rule) ShouldApply() bool {
	if r == nil || r.Rate <= 0 {
		return false
	}
	if r.Rate >= 1.0 {
		return true
	}
	return rand.Float64() < r.Rate
}

// ApplyPreFlight executes any configured latency delay and returns true if a synthetic error was written.
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
