package chaos

import (
	"errors"
	"net/http"
)

// SeverConnection abruptly terminates the active downstream stream across HTTP/1.1 and HTTP/2.
func SeverConnection(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	conn, _, err := rc.Hijack()
	if err == nil {
		// HTTP/1.1: Abruptly sever the underlying TCP connection without sending close frames.
		_ = conn.Close()
		return
	}

	// HTTP/2 fallback: Hijack is unsupported on multiplexed HTTP/2 streams (shared TCP connection).
	// Panicking with http.ErrAbortHandler instructs Go's HTTP/2 server to immediately issue an
	// RST_STREAM frame with INTERNAL_ERROR to the client without sending closing chunks or trailers.
	if errors.Is(err, http.ErrNotSupported) || err != nil {
		panic(http.ErrAbortHandler)
	}
}
