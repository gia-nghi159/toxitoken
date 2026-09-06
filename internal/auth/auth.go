package auth

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Middleware enforces Bearer token authentication and isolates credentials.
type Middleware struct {
	MasterSecret string
	ValidTokens  map[string]bool
}

func NewMiddleware(masterSecret string, allowedTokens []string) *Middleware {
	m := &Middleware{
		MasterSecret: masterSecret,
		ValidTokens:  make(map[string]bool),
	}
	if masterSecret != "" {
		m.ValidTokens[masterSecret] = true
	}
	for _, tok := range allowedTokens {
		tok = strings.TrimSpace(tok)
		if tok != "" {
			m.ValidTokens[tok] = true
		}
	}
	return m
}

// Handler returns an HTTP middleware verifying the Bearer token.
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow unauthenticated health and version checks
		if r.URL.Path == "/health" || r.URL.Path == "/version" {
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": "Missing or invalid Authorization Bearer header",
					"type":    "authentication_error",
					"code":    401,
				},
			})
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		token = strings.TrimSpace(token)

		// Check token validity if authentication is enabled
		if len(m.ValidTokens) > 0 && !m.ValidTokens[token] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": "Invalid API token",
					"type":    "authentication_error",
					"code":    401,
				},
			})
			return
		}

		// Credential isolation: Strip client authorization header so upstream sees only provider secret
		r.Header.Del("Authorization")

		next.ServeHTTP(w, r)
	})
}
