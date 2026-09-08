package chaos

import "strings"

// ApproximateTokens naively splits text into tokens (keeping spaces) to simulate
// OpenAI's fine-grained streaming granularity when translating from large chunks.
func ApproximateTokens(text string) []string {
	var tokens []string
	var buf strings.Builder
	for _, r := range text {
		buf.WriteRune(r)
		if r == ' ' || r == '\n' || r == '\t' {
			tokens = append(tokens, buf.String())
			buf.Reset()
		}
	}
	if buf.Len() > 0 {
		tokens = append(tokens, buf.String())
	}
	return tokens
}
