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
// Note: Length-prefixing secures the semicolon delimiter against injection attacks.
func (r *ChatCompletionRequest) Fingerprint() string {
	h := sha256.New()

	// Normalize model identifier.
	h.Write([]byte(strings.ToLower(strings.TrimSpace(r.Model))))

	// Normalize temperature to 2 decimal places.
	temp := 1.0
	if r.Temperature != nil {
		temp = *r.Temperature
	}
	h.Write([]byte(fmt.Sprintf(":t=%.2f:", temp)))

	// Encode length-prefixed message sequence to preserve boundaries.
	for _, msg := range r.Messages {
		role := strings.TrimSpace(msg.Role)
		content := msg.Content
		fmt.Fprintf(h, "%d:%s:%d:%s;", len(role), role, len(content), content)
	}

	return hex.EncodeToString(h.Sum(nil))
}
