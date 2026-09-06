# Toxitoken (LLM Edge Gateway & Developer CLI)

**Toxitoken** is a dual-component developer tool and edge infrastructure system designed to manage, secure, mock, and stress-test LLM API traffic. It consists of:

1. **The Edge Proxy (`server`)**: A high-performance reverse proxy that sits between applications and model providers (OpenAI, Gemini). It handles credential isolation, zero-buffer SSE stream piping, atomic sliding-window rate limiting, exact-match caching with dual-replay, CI/CD response mocking, and token-aware chaos injection.
2. **The Developer CLI (`toxi`)**: A compiled terminal binary that developers use to query models, pipe code context via Unix standard input, authenticate against the proxy, and toggle gateway operational modes (`live`, `mock`, `chaos`) on the fly.

---

## Key Capabilities

* **Zero-Buffer SSE Streaming**: Uses `http.ResponseController` and unbounded `bufio.Reader` line-reading with `X-Accel-Buffering: no` to flush chunks downstream without memory buffering delays.
* **Context Propagation**: Connects upstream calls to client `r.Context()` with `Timeout: 0`. When client disconnects or closes the terminal, upstream calls cancel immediately to avoid wasted token spend.
* **Format-Agnostic Prompt Fingerprinting**: Delimiter-safe, length-prefixed SHA-256 digest over normalized fields (`model`, `temperature`, `role`, `content`).
* **Canonical JSON Caching & Dual Replay**: Stream accumulator captures full completions without stalling delivery. Serves `stream: false` requests instantly as JSON and `stream: true` requests as simulated 10ms SSE chunk streams with `X-Cache: HIT`.
* **Protocol-Aware Chaos Engine**: Header-driven (`X-Chaos-Config`) latency injection, synthetic status codes (`429`, `500`, `502`, `504`), and mid-stream socket severing (HTTP/1.1 `Hijack()` + TCP termination, HTTP/2 `panic(http.ErrAbortHandler)` producing `RST_STREAM` frame).
* **Credential Isolation**: Strips incoming client auth tokens and injects master provider keys (`OPENAI_API_KEY`, `GEMINI_API_KEY`).
* **Sliding-Window Rate Limiting**: Atomic sliding-window counter via Redis Lua script with automatic zero-dependency in-memory fallback.
* **Zero External Dependencies by Default**: Runs immediately out of the box with thread-safe in-memory stores; seamlessly connects to Upstash Redis when `REDIS_URL` is set.
* **Zero API Cost Testing (`pkg/mockupstream`)**: Built-in mock upstream server allows 100% automated validation of stream chunking, drop-after-tokens, and cache recording without burning real API credits.

---

## Architecture

```text
                      [ Developer Terminal / CI Pipeline ]
                                       │
                  ┌────────────────────┴────────────────────┐
                  │ (Standard Input / CLI Flag / CI Runner) │
                  ▼                                         ▼
         [ toxi CLI Client ]                       [ App Test Suites ]
                  │                                         │
                  └────────────────────┬────────────────────┘
                                       │ HTTP POST /v1/chat/completions
                                       │ Headers: X-Proxy-Mode, X-Chaos-Config, Bearer
                                       ▼
                     [ Toxitoken Gateway Server (Go / Chi) ]
                                       │
        ┌──────────────────────────────┼──────────────────────────────┐
        ▼                              ▼                              ▼
  [ 1. Auth & Rate-Limit ]       [ 2. Cache / Mock ]           [ 3. Chaos Engine ]
  (Redis Sliding Window)         (SHA-256 Hash Store)         (Jitter / Error Injected)
        │                              │                              │
        └──────────────────────────────┼──────────────────────────────┘
                                       ▼ (If Cache Miss & Live Mode)
                     [ Provider Protocol Adapter ]
                     (OpenAI Direct Pass / Gemini Translation)
                                       │
                                       ▼ (Chunked SSE Stream)
               [ Upstream Providers (OpenAI / Google Gemini) ]
```

---

## Quick Start

### 1. Build Binaries
```bash
go build -o bin/server ./cmd/server
go build -o bin/toxi ./cmd/toxi
```

### 2. Run Gateway Server
```bash
# In-memory mode (0 external dependencies required)
PORT=8080 ./bin/server

# With OpenAI & Redis
PORT=8080 OPENAI_API_KEY="sk-..." REDIS_URL="redis://localhost:6379" ./bin/server
```

### 3. Configure CLI
```bash
./bin/toxi login --url http://localhost:8080 --token dev-secret-token
```

### 4. Interactive CLI Usage
```bash
# Query model with real-time token streaming
./bin/toxi ask "Explain memory alignment in Go"

# Pipe Unix stdin as context
cat main.go | ./bin/toxi pipe "Audit this Go code for concurrency bugs"

# Switch gateway mode to mock (0 API token spend)
./bin/toxi mode mock
./bin/toxi ask "Hello"

# Switch gateway mode to chaos (test downstream resilience)
./bin/toxi mode chaos --chaos-config "rate=1.0,status=504"
./bin/toxi ask "Hello" # Triggers artificial HTTP 504 Gateway Timeout

# Switch back to live mode
./bin/toxi mode live
```

---

## Chaos Engineering Configuration

Pass the `X-Chaos-Config` header in curl or configure it via `toxi mode chaos`:

| Parameter | Type | Description | Example |
|---|---|---|---|
| `rate` | `float` | Probability of applying chaos (0.0 to 1.0) | `rate=0.5` |
| `delay` | `duration`| Pre-flight latency injection | `delay=1500ms` |
| `status` | `int` | Artificial HTTP error status code | `status=502` |
| `drop_after` | `int` | Flush N *content* tokens then abruptly sever TCP socket / RST stream | `drop_after=8` |

Example curl test:
```bash
curl -N -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer dev-secret-token" \
  -H "Content-Type: application/json" \
  -H "X-Chaos-Config: rate=1.0,drop_after=5" \
  -d '{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"Tell me a story"}]}'
```

---

## Testing & Quality Assurance

Run the comprehensive test suite with the race detector enabled:
```bash
go test -v -race ./...
```

Includes tests for:
* Deterministic field SHA-256 fingerprinting
* In-memory and Redis cache storage with dual-replay (JSON vs synthetic SSE chunks)
* Protocol-aware connection severing (HTTP/1.1 vs HTTP/2 fallback)
* Sliding-window rate limiting
* Google Gemini request/SSE translation
* Full lifecycle E2E proxy integration tests against `pkg/mockupstream`

---

## Container Deployment

A multi-stage `Dockerfile` compiles static Go binaries to a minimal unprivileged distroless container image (~15MB):

```bash
docker build -t toxitoken:latest .
docker run -p 8080:8080 -e OPENAI_API_KEY="sk-..." toxitoken:latest
```
