package models

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Fingerprint builds a collision-resistant, format-agnostic SHA-256 digest
// that normalizes model casing, float precision, and uses length-prefixing
// to eliminate delimiter-collision vulnerabilities.
// Note: While a semicolon ';' is used to delimit entries, the explicit length
// prefixing protects against injection even if role or content contains ';'.
func (r *ChatCompletionRequest) Fingerprint() string {
	h := sha256.New()

	// 1. Normalized model identifier
	h.Write([]byte(strings.ToLower(strings.TrimSpace(r.Model))))

	// 2. Normalized temperature (fixed 2 decimal places to match 0.7 vs 0.700)
	temp := 1.0
	if r.Temperature != nil {
		temp = *r.Temperature
	}
	h.Write([]byte(fmt.Sprintf(":t=%.2f:", temp)))

	// 3. Length-prefixed message sequence (preserves order and boundaries)
	for _, msg := range r.Messages {
		role := strings.TrimSpace(msg.Role)
		content := msg.Content
		fmt.Fprintf(h, "%d:%s:%d:%s;", len(role), role, len(content), content)
	}

	return hex.EncodeToString(h.Sum(nil))
}
