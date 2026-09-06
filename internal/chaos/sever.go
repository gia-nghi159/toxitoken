package chaos

import (
	"errors"
	"net/http"
)

// SeverConnection terminates active downstream streams.
func SeverConnection(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	conn, _, err := rc.Hijack()
	if err == nil {
		// Terminate HTTP/1.1 TCP connection without close frames.
		_ = conn.Close()
		return
	}

	// Execute HTTP/2 fallback via panic(http.ErrAbortHandler) to issue RST_STREAM.
	if errors.Is(err, http.ErrNotSupported) || err != nil {
		panic(http.ErrAbortHandler)
	}
}
