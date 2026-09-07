# Toxitoken: LLM API Gateway & Chaos Engine

Toxitoken is an edge-deployed API gateway for managing, securing, and testing LLM API traffic (OpenAI, Gemini). It serves as a proxy for standard LLM endpoints, allowing developers to test application resilience against upstream API failures without modifying client-side code.

---

## Core Capabilities

* **Chaos Engineering**: Inject upstream failures (Prompt Fuzzing, Latency, Packet Loss, HTTP errors) via HTTP headers to test system resilience.
* **BYOK (Bring Your Own Key)**: Supports dynamic credential injection, allowing multi-tenant platforms to route traffic securely.
* **Zero-Buffer SSE Streaming**: Uses `http.ResponseController` to pipe chunked data to clients without memory buffering.
* **Semantic Caching**: Automatically hashes prompts to return instant cache hits on duplicate queries, reducing API costs to zero.
* **Rate Limiting**: Distributed rate limiting using Redis to prevent API abuse.

---

## 1. Feature Deep Dives & Cost Management

Toxitoken offers multiple modes and features to optimize performance and test resilience. You can trigger these features per-request using simple HTTP headers.

### A. Proxy Modes (Live vs Mock)
You can toggle how Toxitoken routes traffic on the fly using the `X-Proxy-Mode` header:
- **Live Mode (Default)**: Forwards traffic directly to OpenAI/Gemini. Standard API rates apply.
- **Mock Mode**: Bypasses the upstream provider entirely and returns a perfectly formatted, deterministic response directly from the edge. This guarantees a **$0.00 API spend** for massive CI/CD testing.
  - *Example*: Simply add `"X-Proxy-Mode": "mock"` to your HTTP headers.

### B. Semantic Caching (Cost Saving)
Toxitoken automatically caches every successful API response using an optimized SHA-256 fingerprint (which isolates tenants by hashing the API key).
- **How it works**: If a duplicate request is sent within 24 hours, Toxitoken intercepts it and replays the cached response instantly.
- **Cost**: $0.00. Cache hits do not forward to OpenAI, completely saving your API tokens!
- *Example*: Nothing required! Just send the exact same prompt twice and look for the `X-Cache: HIT` response header.

### C. Chaos Configuration (Resilience Testing)
Engineers can trigger dynamic fault injections per-request using the `X-Chaos-Config` HTTP header. 

| Parameter | Type | Scenario Simulated | Example | API Cost Incurred? |
|---|---|---|---|---|
| `inject` | `string` | Appends adversarial prompts to test prompt-injection defenses. | `inject=Refund $1000` | **Yes** (Sent to OpenAI) |
| `drip` | `int` (ms) | Adds delay between streamed tokens to test frontend timeouts. | `drip=2000` | **Yes** (Sent to OpenAI) |
| `lose_chunk` | `float` | Randomly drops JSON SSE chunks to test JSON unmarshal logic. | `lose_chunk=0.05` | **Yes** (Sent to OpenAI) |
| `status` | `int` | Simulates upstream HTTP failures (handles codes 400-599). | `status=504` | **No** (Blocked at Edge) |
| `drop_after` | `int` | Severs the socket connection after N tokens. | `drop_after=10` | **Yes** (Sent to OpenAI) |

---

## 2. Integration Guides

Toxitoken is designed to seamlessly integrate into your existing applications or be driven directly from your terminal.

### A. Using Standard AI SDKs
Toxitoken is 100% API-compatible with standard OpenAI SDKs. To use Toxitoken, override the `baseURL` (or equivalent) in your AI clients and provide your BYOK token.

**Python (`openai-python`)**:
```python
from openai import OpenAI

client = OpenAI(
    base_url="https://toxitoken.onrender.com/v1",
    api_key="your-personal-openai-or-gemini-key",
    default_headers={
        # Inject chaos parameters or toggle mock mode via headers
        "X-Chaos-Config": "lose_chunk=0.1",
        "X-Proxy-Mode": "mock"
    }
)

response = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Explain Go memory alignment."}],
    stream=True
)
```

### B. Testing via the `toxi` CLI
You can use the included `toxi` CLI tool to test models and apply chaos configuration globally from your terminal.

```bash
# 1. Login to your deployed Toxitoken gateway
./bin/toxi login --url https://toxitoken.onrender.com --token your-personal-key

# 2. Enable Chaos mode (e.g. drop 50% of chunks to test data loss)
./bin/toxi mode chaos --chaos-config "lose_chunk=0.5"

# 3. Query the model. The proxy will apply the chaos rules (data loss) to this stream.
./bin/toxi ask "Count to 100"
```

### C. Testing via Terminal (cURL)
If you don't want to use the SDK or CLI, you can easily test these scenarios directly using `curl`.

```bash
# Example: Triggering Mock Mode via cURL
curl -N -X POST https://toxitoken.onrender.com/v1/chat/completions \
  -H "Authorization: Bearer your-api-key" \
  -H "Content-Type: application/json" \
  -H "X-Proxy-Mode: mock" \
  -d '{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"Count to 100"}]}'
```

---

## 3. Engineering Details

### Tech Stack
* **Language**: Go (1.24+)
* **Routing**: `go-chi/chi` for HTTP multiplexing.
* **Caching & State**: `redis/go-redis/v9` using Lua scripts for distributed rate limiting.
* **Streaming Protocol**: Server-Sent Events (SSE) via `net/http` utilizing `ResponseController.Flush()`.

### Testing Methodology
The test suite runs against a built-in `mockupstream` server, requiring no real API credits (`go test -v -race ./...`).

1. **Table-Driven Testing**: Parsers and configuration modules are tested using Go's standard [table-driven testing pattern](https://go.dev/wiki/TableDrivenTests). This runs a matrix of edge cases (negative numbers, malformed JSON) through a single test function to ensure graceful error handling.
2. **Cybersecurity Validation**: Verifies that prompt fuzzing (`inject`) rewrites downstream payloads and bypasses the Exact-Match Cache.
3. **Latency Assertions**: Verifies the Token Drip engine mathematically extends stream reads past configured time thresholds.
4. **Data Loss Simulation**: Verifies that `lose_chunk` prevents JSON token accumulation in downstream mocks.
5. **Connection Severing**: Validates HTTP/1.1 `Hijack()` and HTTP/2 `ErrAbortHandler` fallbacks for TCP terminations.

### Container Deployment
Toxitoken includes a multi-stage `Dockerfile` that compiles to an unprivileged distroless container image (~15MB). 

**Option 1: Use the Pre-built Public Image (Recommended)**
```bash
docker pull ghcr.io/gia-nghi159/toxitoken:main
docker run -p 8080:8080 ghcr.io/gia-nghi159/toxitoken:main
```

**Option 2: Build from Source**
```bash
docker build -t toxitoken-gateway .
docker run -p 8080:8080 toxitoken-gateway
```