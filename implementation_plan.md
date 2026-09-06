# Architecture Blueprint & Implementation Plan: Toxitoken

Toxitoken is a dual-component developer tool and edge infrastructure system designed to manage, secure, mock, and stress-test LLM API traffic. It consists of a high-performance Go reverse proxy gateway (`server`) and a developer terminal CLI (`toxi`).

---

## Tactical Development Guardrails

> [!TIP]
> **1. $0 Token Spend During Development**:
> Before wiring any real upstream keys, we run against a lightweight local mock upstream HTTP server that emits synthetic SSE chunks with realistic pacing. All chunk flushing, stream accumulation, cache hits, and connection severing are validated with zero API spend.

> [!NOTE]
> **2. Provider Adapter Sequence**:
> Standardize 100% on the OpenAI wire protocol first (Proxy core, Dual-Replay Cache, Chaos Injection, and CLI). The Google Gemini translation adapter (`candidates[0].content.parts[0].text` mapping) is isolated to Phase 6 so JSON schema translation never blocks core systems infrastructure work.

---

## Technical Refinements & Operational Landmines Addressed

### 1. HTTP/2 Connection Severing vs. `http.Hijacker`
- **The Landmine**: `http.NewResponseController(w).Hijack()` returns `http.ErrNotSupported` when the client negotiates HTTP/2 via ALPN. Multiplexed streams share one TCP connection; hijacking would sever all streams or fail.
- **The Solution**: In `internal/chaos/sever.go`, attempt `rc.Hijack()`.
  - **HTTP/1.1**: Close the underlying TCP connection abruptly (`conn.Close()`).
  - **HTTP/2**: Catch `http.ErrNotSupported` and `panic(http.ErrAbortHandler)`. Go's `net/http` HTTP/2 server catches this sentinel panic and immediately dispatches an `RST_STREAM` frame with `INTERNAL_ERROR` without closing chunks or trailers, cleanly simulating a dropped connection.

### 2. Cache Representation: Canonical JSON & Dual Replay Engine
- **The Landmine**: Storing raw SSE wire chunks prevents serving `stream: false` requests without costly re-parsing.
- **The Solution**: Accumulate delta text during upstream streaming into a complete canonical response string. Store the normalized payload in Redis / In-Memory cache (`24h TTL`).
  - **Cache Hit on `stream: false`**: Return standard OpenAI completion JSON with `X-Cache: HIT`.
  - **Cache Hit on `stream: true`**: Dual-replay engine tokenizes the cached text into 2-to-4 word fragments, flushing them as SSE events (`data: {...}\n\n`) with a 10ms ticker, culminating in `data: [DONE]\n\n`.

### 3. Fingerprint Determinism (Format-Agnostic Field Hashing)
- **The Landmine**: `json.Marshal` on request payloads introduces false cache misses due to map key order, whitespace variance, and float formatting differences (`0.7` vs `0.700`).
- **The Solution**: Construct the SHA-256 digest directly from normalized fields with length-prefixing:
  $$\text{Digest} = \text{hex}(\text{SHA256}(\text{model} \parallel \text{fmt.Sprintf}(":t=\%.2f:", \text{temp}) \parallel \text{len}(\text{role}) : \text{role} : \text{len}(\text{content}) : \text{content} ;))$$

### 4. `bufio.Scanner` Buffer Overflow (`bufio.ErrTooLong`) & Context Propagation
- **The Landmine**: Go's default `bufio.Scanner` fails on lines larger than 64 KB (`bufio.MaxScanTokenSize`). Extended reasoning blocks or large structured outputs will terminate streams silently. Fixed client timeouts also truncate long model generations.
- **The Solution**: Use `bufio.NewReader(upstreamBody).ReadBytes('\n')` to eliminate buffer caps. Configure `http.Client{Timeout: 0}` and bind upstream requests directly to `r.Context()`. Downstream client disconnects immediately cancel the upstream call, halting token spend.

### 5. Upstream Non-200 Error Handling on `stream: true` & SSE Framing
- **The Landmine**: If upstream returns `401 Unauthorized` or `429 Too Many Requests`, it sends an `application/json` error, not SSE. Calling the SSE flusher blindly sets `200 OK` and `text/event-stream`, confusing client SDKs. Abruptly breaking on `data: [DONE]` can omit the closing SSE frame boundary (`\n\n`).
- **The Solution**: Verify `resp.StatusCode == http.StatusOK` before setting SSE headers; pass through error status codes and JSON payloads untouched. Ensure full SSE event delimiter (`\n\n`) framing upon stream termination.

---

## Scope & Anti-Overengineering Boundaries

| Feature Dimension | In-Scope (Deliverable) | Out-of-Scope (Anti-Overengineering) |
|---|---|---|
| **Protocol / Endpoint** | `/v1/chat/completions` (OpenAI format), `/health`, `/v1/models` | Fine-tuning endpoints, audio/image APIs, batch endpoints |
| **Streaming** | SSE (`text/event-stream`) zero-buffer line flusher (`http.ResponseController`) | WebSockets, gRPC, bi-directional audio streaming |
| **Providers** | OpenAI (native direct pass-through) + Gemini (format translation adapter) | Anthropic Claude XML, Cohere, Bedrock, Mistral custom dialects |
| **Caching** | Exact-match field-based SHA-256, Upstash Redis & in-memory `sync.Map` fallback | Vector embeddings semantic search, fuzzy matching |
| **Chaos Injection** | Header-driven (`X-Chaos-Config`): probability rate, pre-delay, synthetic status codes, mid-stream token severing | Distributed chaos mesh, BPF packet drops |
| **Rate Limiting** | Redis sliding-window Lua counter + local sliding-window fallback by Bearer token | Distributed tiered billing, token-cost metering |
| **CLI (`toxi`)** | `toxi login`, `toxi mode [live|mock|chaos]`, `toxi ask "<prompt>"`, `toxi pipe` (stdin) | Full TUI terminal app, interactive chat session repl |
| **Data Persistence** | Redis (Upstash) key-value / sets + CLI local `~/.toxi/config.json` | SQL databases, persistent audit logs |

---

## Target Project Architecture

```
toxitoken/
├── cmd/
│   ├── server/          # Gateway entrypoint (main.go)
│   └── toxi/            # Developer CLI entrypoint (main.go)
├── internal/
│   ├── adapter/         # Provider translation (Gemini adapter)
│   ├── auth/            # Bearer token validation & credential swapping
│   ├── cache/           # Field SHA-256 fingerprinting & Redis/In-memory store
│   ├── chaos/           # Token-aware delay, drop, and fault injector (HTTP/1.1 & HTTP/2)
│   ├── config/          # Environment & CLI config loaders
│   ├── proxy/           # Reverse proxy handler & SSE flusher / accumulator
│   └── ratelimit/       # Sliding-window counter via Redis Lua & in-memory fallback
├── pkg/
│   ├── mockupstream/    # Lightweight 0-cost upstream mock server for dev/tests
│   └── models/          # OpenAI-compatible schemas & canonical fingerprinting
├── tests/
│   └── e2e/             # Integration tests for streaming, cache, chaos & CLI
├── Dockerfile
├── go.mod
└── go.sum
```

---

## Tactical Step-by-Step Execution Sequence

```
1. Mock Upstream Test Server (0 API cost)
   └── 2. Proxy Core & SSE Flusher (Phase 1)
       └── 3. Cache & Dual Replay (Phase 2)
           └── 4. Chaos Engine (Phase 3)
               └── 5. Toxi CLI Client (Phase 5)
                   └── 6. Auth, Rate Limiting & Gemini Adapter (Phase 4 & 6)
```

### Step 1: Mock Upstream Test Server (`pkg/mockupstream`)
- Fast, local HTTP test server that emits OpenAI-compatible SSE chunks at controllable intervals (e.g. 10-25ms per token).
- Allows 100% automated validation of stream chunking, drop-after-tokens, and cache recording without touching external networks or consuming credits.

### Step 2: Proxy Core & Zero-Buffer SSE Streaming (Phase 1)
- `pkg/models/models.go` and `pkg/models/fingerprint.go`.
- `internal/config/config.go`.
- `internal/proxy/stream.go` (Uncapped `bufio.Reader`, context cancellation, zero-buffer flusher, non-200 passthrough).
- `cmd/server/main.go` routing and forwarding.

### Step 3: Pluggable Caching Engine & Dual Replay (Phase 2)
- `internal/cache/cache.go`: `CacheStore` interface with `MemoryStore` (`sync.Map`) and `RedisStore` (`go-redis/v9`).
- Exact-match retrieval with dual-mode replay (Instant JSON vs synthetic 10ms SSE stream).
- Asynchronous stream accumulation saving complete response on `[DONE]`.

### Step 4: Chaos & Fault Injection Engine (Phase 3)
- `internal/chaos/sever.go`: HTTP/1.1 `Hijack()` + HTTP/2 `panic(http.ErrAbortHandler)`.
- `internal/chaos/chaos.go`: Parses `X-Chaos-Config` (`rate`, `delay`, `status`, `drop_after`).
- Pre-flight latency and synthetic error status codes (`429`, `500`, `502`, `504`).
- Mid-stream token count severing.

### Step 5: Toxi Developer CLI Client (Phase 5)
- `cmd/toxi/main.go` with Cobra.
- `login` (`~/.toxi/config.json` mode `0600`), `mode [live|mock|chaos]`, `ask "<prompt>"`, `pipe` (stdin).

### Step 6: Auth, Rate Limiting & Provider Translation (Phase 4 & Phase 6)
- `internal/auth/auth.go`: Bearer token validation and credential isolation.
- `internal/ratelimit/ratelimit.go`: Atomic sliding-window limiter (Redis Lua + local fallback).
- `internal/adapter/gemini.go`: Gemini API mapping (`candidates[0].content.parts[0].text`).
- Multi-stage `Dockerfile` and automated CI suite.

---

## Verification Plan

### Automated Testing Suite
```bash
go test -v -race ./...
```
1. `pkg/models/fingerprint_test.go`: Collision-resistance and format invariance.
2. `internal/cache/cache_test.go`: Dual replay verification (JSON vs simulated SSE chunk pacing).
3. `internal/chaos/sever_test.go`: Protocol-aware socket dropping.
4. `tests/e2e/stream_test.go`: End-to-end proxy pipeline against `pkg/mockupstream`.
