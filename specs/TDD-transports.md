# Technical Design Document — Transports
## Bifrost HTTP Gateway (`transports/bifrost-http`)

**Version:** 1.0  
**Date:** 2026-04-05  
**Status:** Draft  
**References:** [TDD-project.md](TDD-project.md), [TDD-core.md](TDD-core.md)

---

## Table of Contents

1. [Overview](#1-overview)
2. [Technology Stack](#2-technology-stack)
3. [Directory Structure](#3-directory-structure)
4. [Server Startup Sequence](#4-server-startup-sequence)
5. [Route Architecture](#5-route-architecture)
6. [Middleware Pipeline](#6-middleware-pipeline)
7. [Authentication & Session Management](#7-authentication--session-management)
8. [Inference Handlers](#8-inference-handlers)
9. [Provider Integration Adapters](#9-provider-integration-adapters)
10. [Governance API](#10-governance-api)
11. [Logs & Observability API](#11-logs--observability-api)
12. [MCP API](#12-mcp-api)
13. [Configuration API](#13-configuration-api)
14. [WebSocket Architecture](#14-websocket-architecture)
15. [Static UI Serving](#15-static-ui-serving)
16. [CORS & Security Headers](#16-cors--security-headers)
17. [Deployment](#17-deployment)

---

## 1. Overview

`transports/bifrost-http` is the **production HTTP gateway** that exposes Bifrost as a network service. It wraps the core Bifrost engine and framework services behind a high-performance FastHTTP server, serving both the inference API and the management control plane.

Key responsibilities:
- Route HTTP requests to the appropriate Bifrost core operation
- Authenticate and authorize via session cookies and virtual key validation
- Serve the embedded Next.js control plane UI
- Stream responses via SSE/chunked encoding
- Broadcast real-time events over WebSocket
- Persist and expose observability data (logs, metrics, histograms)

---

## 2. Technology Stack

| Component | Library | Reason |
|-----------|---------|--------|
| HTTP server | `github.com/valyala/fasthttp` | Ultra-low-overhead HTTP; ~11 µs per request at 5k RPS |
| Router | `github.com/fasthttp/router` | Compatible fasthttp router |
| WebSocket | `github.com/fasthttp/websocket` | fasthttp-native WS upgrade |
| ORM / persistence | `gorm.io/gorm` + SQLite driver | Single-file persistence, zero external dependencies |
| Password hashing | scrypt | Session credential hashing |
| Key derivation | Argon2id | Encryption key derivation from passphrase |
| UI embedding | `//go:embed all:ui` | Static SPA bundled into binary |

---

## 3. Directory Structure

```
transports/bifrost-http/
├── main.go                  # Entry point: CLI flags, bootstrap, start
├── server/
│   └── server.go            # BifrostHTTPServer: bootstrap, route registration, lifecycle
├── handlers/
│   ├── config.go            # /api/config, /api/proxy-config, /api/version
│   ├── provider.go          # /api/providers/*
│   ├── governance.go        # /api/governance/* (VKs, teams, customers, routing rules)
│   ├── logging.go           # /api/logs/*, /api/mcp-logs/*
│   ├── mcp.go               # /api/mcp/client/*
│   ├── session.go           # /api/session/login|logout|ws-ticket
│   ├── plugins.go           # /api/plugins/*
│   ├── prompts.go           # /api/prompt-repo/*
│   ├── health.go            # /health
│   ├── inference.go         # /v1/chat/completions, /v1/embeddings, audio, image, etc.
│   ├── completion.go        # /v1/completions (text completions)
│   ├── async.go             # /v1/async/*
│   ├── mcp_inference.go     # /v1/mcp/tool/execute
│   ├── ui.go                # Embedded Next.js SPA handler
│   └── ws_responses.go      # /ws/responses/* (streaming WebSocket mode)
├── websocket/
│   ├── handler.go           # WebSocket upgrade, client management, broadcast
│   └── session.go           # WebSocket session tracking
├── integrations/
│   ├── openai.go            # /openai/v1/* — OpenAI SDK drop-in
│   ├── anthropic.go         # /anthropic/v1/* — Anthropic SDK drop-in
│   ├── genai.go             # /genai/v1beta/* — Google GenAI SDK drop-in
│   ├── bedrock.go           # /bedrock/* — AWS Bedrock SDK drop-in
│   ├── cohere.go            # /cohere/v2/* — Cohere SDK drop-in
│   ├── litellm.go           # /litellm/* — LiteLLM format
│   ├── langchain.go         # /langchain/* — LangChain format
│   └── pydanticai.go        # /pydanticai/* — PydanticAI format
├── lib/
│   ├── config.go            # In-memory Config struct with RWMutex
│   ├── middleware.go        # ChainMiddlewares(), auth, CORS, security headers, logging
│   ├── validation.go        # Request validation helpers
│   └── context.go           # Context key extraction utilities
├── config.schema.json       # JSON Schema for configuration validation/UI rendering
├── Dockerfile               # Multi-stage build (Node.js UI + Go binary)
└── ui/                      # Build output of /ui (embedded via go:embed)
```

---

## 4. Server Startup Sequence

```
main.go
  ├── Parse CLI flags: -port, -host, -app-dir, -log-level, -log-style
  ├── Initialize logger
  └── server.Bootstrap()
        ├── Create/verify app directory (default: ./data)
        ├── Load config from config.json + env var overrides
        ├── Initialize ConfigStore (SQLite via Gorm)
        │     └── Run migrations (providers, governance, sessions, etc.)
        ├── Initialize LogStore (if logging enabled)
        ├── Initialize VectorStore (if semantic cache configured)
        ├── Create WebSocketHandler (starts heartbeat goroutine)
        ├── Initialize plugins in config order:
        │     ├── governance, logging, semanticcache
        │     ├── telemetry, otel, maxim
        │     └── mocker, litellmcompat, jsonparser
        ├── Initialize Bifrost client (core engine)
        │     └── Registers providers, network pools, plugin pipeline
        ├── Start log retention cleaner goroutine
        └── Start async job cleaner goroutine

server.RegisterInferenceRoutes()
  ├── Create InferenceHandler, CompletionHandler, AsyncHandler
  ├── Create MCPInferenceHandler, MCPServerHandler
  └── Register /v1/* routes with middleware chains

server.RegisterAPIRoutes()
  ├── Create handler instances for each domain
  └── Register /api/* routes with auth middleware

server.RegisterUIRoutes()
  └── Register UIHandler last (catch-all for SPA routing)

server.Start()
  ├── TCP listen on configured host:port
  ├── Print plugin status summary
  └── fasthttp.Server.Serve(listener)
        └── Graceful shutdown on SIGINT/SIGTERM
```

---

## 5. Route Architecture

### 5.1 Inference Routes (`/v1/*`)

| Method | Path | Handler | Description |
|--------|------|---------|-------------|
| POST | `/v1/chat/completions` | InferenceHandler | Chat completions (sync + SSE stream) |
| POST | `/v1/completions` | CompletionHandler | Text completions (legacy) |
| POST | `/v1/embeddings` | InferenceHandler | Embeddings |
| POST | `/v1/rerank` | InferenceHandler | Rerank |
| POST | `/v1/responses` | InferenceHandler | OpenAI Responses API |
| POST | `/v1/responses/input_tokens` | InferenceHandler | Count input tokens |
| POST | `/v1/audio/speech` | InferenceHandler | Text-to-speech |
| POST | `/v1/audio/transcriptions` | InferenceHandler | Speech-to-text |
| POST | `/v1/images/generations` | InferenceHandler | Image generation |
| POST | `/v1/images/edits` | InferenceHandler | Image editing (multipart) |
| POST | `/v1/images/variations` | InferenceHandler | Image variations (multipart) |
| POST/GET | `/v1/videos` | InferenceHandler | Video generation/listing |
| GET/DELETE | `/v1/videos/{id}` | InferenceHandler | Video management |
| GET | `/v1/videos/{id}/content` | InferenceHandler | Video download |
| POST/GET | `/v1/files` | InferenceHandler | File upload/listing |
| GET/DELETE | `/v1/files/{id}` | InferenceHandler | File management |
| GET | `/v1/models` | InferenceHandler | List available models |
| POST/GET | `/v1/batches` | InferenceHandler | Batch job management |
| POST/GET | `/v1/containers` | InferenceHandler | Container management |
| POST | `/v1/mcp/tool/execute` | MCPInferenceHandler | Execute MCP tool |

### 5.2 Async Routes (`/v1/async/*`)

| Method | Path | Description |
|--------|------|-------------|
| POST | `/v1/async/chat/completions` | Submit async chat job |
| GET | `/v1/async/chat/completions/{job_id}` | Poll job result |
| POST/GET | `/v1/async/completions/{job_id?}` | Async text completions |
| POST/GET | `/v1/async/responses/{job_id?}` | Async responses |
| POST/GET | `/v1/async/embeddings/{job_id?}` | Async embeddings |
| POST/GET | `/v1/async/audio/speech/{job_id?}` | Async TTS |
| POST/GET | `/v1/async/audio/transcriptions/{job_id?}` | Async STT |
| POST/GET | `/v1/async/images/generations/{job_id?}` | Async image gen |

### 5.3 Management API Routes (`/api/*`)

See [Project TDD §6.3](TDD-project.md#63-admin-api-endpoints) for the full route table.

### 5.4 Provider Integration Routes

| Prefix | SDK |
|--------|-----|
| `/openai/v1/` | OpenAI Python/JS SDK |
| `/openai/openai/deployments/` | Azure OpenAI SDK |
| `/anthropic/v1/` | Anthropic Python/JS SDK |
| `/genai/v1beta/` | Google GenAI SDK |
| `/bedrock/` | AWS Bedrock SDK |
| `/cohere/v2/` | Cohere SDK |
| `/litellm/` | LiteLLM |
| `/langchain/` | LangChain |
| `/pydanticai/` | PydanticAI |

---

## 6. Middleware Pipeline

Middleware is composed using `lib.ChainMiddlewares(h, m1, m2, ...)` which applies middlewares left-to-right:

### 6.1 Available Middlewares

| Middleware | Applied To | Purpose |
|-----------|-----------|---------|
| `AuthMiddleware` | Management API routes | Validates session token or cookie |
| `CorsMiddleware` | All routes | Sets CORS headers, validates origin, handles preflight |
| `SecurityHeadersMiddleware` | All routes | Adds security response headers |
| `RequestDecompressionMiddleware` | Inference routes | Decompresses gzip/deflate/brotli/zstd request bodies |
| `TransportInterceptorMiddleware` | All routes | Runs plugin `HTTPTransportPreHook`/`PostHook` |
| `TracingMiddleware` | All routes | Injects trace ID, propagates distributed trace context |
| `LoggingMiddleware` | All routes | Logs HTTP request/response (skips /health, /_next, /api/dev) |

### 6.2 Request Decompression

The decompression middleware handles:
- Small payloads (< threshold): buffered decompression using pooled readers
- Large payloads (≥ threshold): streaming decompression to avoid holding entire body in memory
- Formats: gzip, deflate, brotli, zstd

### 6.3 Security Headers

Applied to all responses:

| Header | Value |
|--------|-------|
| `X-Frame-Options` | `DENY` |
| `X-Content-Type-Options` | `nosniff` |
| `Referrer-Policy` | `strict-origin-when-cross-origin` |
| `Content-Security-Policy` | `frame-ancestors 'none'` |
| `Strict-Transport-Security` | `max-age=31536000; includeSubDomains` (HTTPS only) |
| `Permissions-Policy` | Disables camera, microphone, geolocation |

---

## 7. Authentication & Session Management

### 7.1 Session Cookie Flow

```
POST /api/session/login
  body: { username, password }
    ↓
  Validate against admin_username + scrypt(admin_password)
    ↓
  Generate session token (UUID)
    ↓
  Persist to SessionsTable with 30-day expiry
    ↓
  Set-Cookie: bifrost_session=<token>
              HttpOnly; Secure; SameSite=Lax; Max-Age=2592000
```

### 7.2 Token Validation

`AuthMiddleware` extracts the token from:
1. `Authorization: Bearer <token>` header (preferred)
2. `bifrost_session` cookie (fallback)

Validates:
- Token exists in `SessionsTable`
- `ExpiresAt` is in the future

On failure: returns HTTP 401 with `{ error: { message: "..." } }`.

### 7.3 WebSocket Ticket

WebSocket connections cannot send custom headers during the upgrade handshake. Bifrost issues short-lived single-use tickets:

```
POST /api/session/ws-ticket  (requires auth)
  ← { ticket: "<random-token>" }

WS  /ws?ticket=<random-token>
  → Validates ticket against WSTicketStore (in-memory, TTL ~30s)
  → Ticket consumed on first use
```

### 7.4 Credential Storage

- Admin password stored as `scrypt(password)` hash in `config.json` / ConfigStore
- Sessions stored in SQLite `sessions` table
- Optional encryption at rest: encryption key is derived via Argon2id KDF → 32-byte AES-256 key

### 7.5 Auth Toggle

Authentication is optional. `auth_config.is_enabled = false` disables session validation for all endpoints. `enforce_auth_on_inference` controls whether the inference endpoints (`/v1/*`) also require auth.

---

## 8. Inference Handlers

### 8.1 Request Normalization

All inference handlers follow this pattern:

```
1. Parse and validate request body (JSON, or multipart for file/audio/image)
2. Extract model string → parse provider/model format
3. Extract virtual key from x-bf-vk header or Authorization Bearer token
4. Build BifrostRequest with provider, model, params, messages
5. Call bifrostClient.Send(ctx, req)
6. If streaming: pipe SSE chunks to response writer
7. If non-streaming: JSON-marshal response → write
```

### 8.2 Streaming (SSE)

For streaming requests (`"stream": true`):

```
Response headers:
  Content-Type: text/event-stream
  Cache-Control: no-cache
  Connection: keep-alive

Per chunk:
  data: { "id": "...", "choices": [{ "delta": { "content": "..." } }] }\n\n

Final:
  data: [DONE]\n\n
```

Stream chunks flow through the `HTTPTransportStreamChunkHook` plugin pipeline per chunk.

### 8.3 Async Handler

Submit (`POST /v1/async/chat/completions`):
1. Validate request
2. Generate `job_id` (UUID)
3. Write job to async job store with status `pending`
4. Spawn goroutine: execute inference → update job to `completed` or `failed`
5. Return `{ job_id }` immediately with HTTP 202

Poll (`GET /v1/async/chat/completions/{job_id}`):
1. Look up job in store
2. If `pending`: return HTTP 200 `{ status: "pending" }`
3. If `completed`: return HTTP 200 `{ status: "completed", result: <response> }`
4. If `failed`: return HTTP 200 `{ status: "failed", error: <error> }`
5. If not found (expired): return HTTP 404

A background goroutine cleans up jobs older than configurable TTL.

---

## 9. Provider Integration Adapters

Each adapter translates the native SDK format to the Bifrost unified format and back.

### 9.1 OpenAI Adapter (`/openai/v1/`)

OpenAI requests use the same schema as Bifrost's unified API. The adapter:
1. Injects `openai/` prefix to the model field (`gpt-4o` → `openai/gpt-4o`)
2. Forwards to `InferenceHandler`
3. Returns response without modification (schema is identical)

**Azure OpenAI variant:** `/openai/openai/deployments/{deployment-id}/...`
- Injects `azure/<deployment-id>` as the model value

### 9.2 Anthropic Adapter (`/anthropic/v1/`)

Anthropic's native format differs from OpenAI:
- Input: `{ model, messages, max_tokens, system }` (no `messages[].role = "system"`)
- Output: `{ content: [{ type: "text", text: "..." }], usage: { input_tokens, output_tokens } }`

The adapter:
1. Translates Anthropic request → Bifrost `ChatCompletionRequest`
2. Calls Bifrost core
3. Translates Bifrost response → Anthropic response format

### 9.3 Google GenAI Adapter (`/genai/v1beta/`)

Google's `generateContent` API uses a different schema (`contents`, `parts`, `candidates`). The adapter performs bidirectional schema translation.

### 9.4 AWS Bedrock Adapter (`/bedrock/`)

Routes `POST /bedrock/model/{modelId}/converse` and `converse-stream` to Bifrost inference. Model ID maps to `bedrock/<modelId>`.

### 9.5 LiteLLM / LangChain / PydanticAI

These adapters primarily normalize non-standard model string formats and route to the appropriate provider-specific adapter or the unified `/v1/` handler.

---

## 10. Governance API

### 10.1 Virtual Key CRUD

```
POST /api/governance/virtual-keys
  body: CreateVirtualKeyRequest {
    name, description,
    provider_configs: [{ provider, weight, allowed_models }],
    budget: { max_limit, reset_duration, calendar_aligned },
    rate_limit: { token_max_limit, request_max_limit, ... },
    mcp_configs: [{ client_id, allowed_tools }],
    team_id, customer_id,
    is_active
  }
  response: { virtual_key: { id, key_value, ... } }
```

The generated `key_value` is shown only once and never returned again.

### 10.2 Data Flow

```
Handler receives request
    ↓
Validate with CreateVirtualKeyRequest/UpdateVirtualKeyRequest
    ↓
Persist to ConfigStore (SQL)
    ↓
Call governance plugin callback to update in-memory store
    ↓
Return updated entity
```

### 10.3 Routing Rules

Routing rules use CEL expressions evaluated against request attributes. The handler validates CEL syntax before persisting.

```
POST /api/governance/routing-rules
  body: {
    name, cel_expression,
    targets: [{ provider, model, weight }],
    fallbacks: ["provider/model"],
    priority, enabled
  }
```

---

## 11. Logs & Observability API

### 11.1 Log Query

`GET /api/logs` supports rich query parameters:

| Parameter | Type | Description |
|-----------|------|-------------|
| `providers` | string[] | Filter by provider names |
| `models` | string[] | Filter by model names |
| `status` | string | `success` \| `error` |
| `start_time` | RFC3339 | Time range start |
| `end_time` | RFC3339 | Time range end |
| `virtual_key_ids` | string[] | Filter by virtual key |
| `routing_rule_ids` | string[] | Filter by routing rule |
| `min_latency` / `max_latency` | float | Latency bounds (seconds) |
| `min_cost` / `max_cost` | float | Cost bounds (USD) |
| `search_text` | string | Full-text content search |
| `sort_by` | string | Column to sort |
| `order` | asc/desc | Sort order |
| `limit` / `offset` | int | Pagination |

### 11.2 Histograms

Pre-aggregated time-series data for dashboard charts:

| Endpoint | Metric |
|---------|--------|
| `GET /api/logs/histogram` | Request count over time |
| `GET /api/logs/histogram/tokens` | Token usage over time |
| `GET /api/logs/histogram/cost` | Cost over time |
| `GET /api/logs/histogram/models` | Per-model breakdown |
| `GET /api/logs/histogram/latency` | Latency percentiles |
| `GET /api/logs/histogram/cost/by-provider` | Cost by provider |
| `GET /api/logs/histogram/tokens/by-provider` | Tokens by provider |
| `GET /api/logs/histogram/latency/by-provider` | Latency by provider |

### 11.3 Real-time Broadcasting

After each inference completes, the logging handler calls `WebSocketHandler.BroadcastLogUpdate(logEntry)` which fans out the new log entry to all connected UI WebSocket clients.

---

## 12. MCP API

### 12.1 Client Management

```
GET    /api/mcp/clients                     List all MCP clients (full list or paginated)
POST   /api/mcp/client                      Register new MCP client
GET    /api/mcp/client/{id}                 Get client config + tool list
PUT    /api/mcp/client/{id}                 Update client config
DELETE /api/mcp/client/{id}                 Remove client
POST   /api/mcp/client/{id}/reconnect       Force reconnect
POST   /api/mcp/client/{id}/complete-oauth  Complete OAuth flow
```

### 12.2 MCP Server Mode

Bifrost also acts as an **MCP server** exposing its tools to external MCP clients:

```
POST /mcp   → JSON-RPC request/response (for stateless MCP clients)
GET  /mcp   → Server-Sent Events stream (for persistent MCP clients)
```

### 12.3 Tool Execution

```
POST /v1/mcp/tool/execute
  body: { tool_name, arguments, client_id? }
  response: { result: <tool output> }
```

---

## 13. Configuration API

### 13.1 Runtime Config Update

```
PUT /api/config
  body: BifrostConfig
    ↓
  ConfigStore.Save(config)
    ↓
  Trigger reload callbacks:
    - Provider reload
    - Plugin config reload
    - In-memory config cache invalidation
```

Changes to most configuration fields take effect immediately without restart. Provider key changes trigger a provider client rebuild.

### 13.2 In-Memory Config Cache

`lib.Config` is the authoritative in-memory configuration, protected by `RWMutex`. All handlers read from this cache. Writes go through ConfigStore and update the cache atomically.

### 13.3 Environment Variable References

All `env.` prefixed values in config are resolved at read time:

```
{ "value": "env.OPENAI_API_KEY" }
  → os.Getenv("OPENAI_API_KEY")
```

This allows secrets to be injected via environment without being stored in the database.

---

## 14. WebSocket Architecture

### 14.1 Connection Lifecycle

```
GET /ws?ticket=<token>
  ↓
Origin validation (CorsMiddleware)
  ↓
Ticket validation (WSTicketStore, single-use)
  ↓
fasthttp/websocket.Upgrade()
  ↓
WebSocketHandler.HandleConnection(conn)
  ├── Register client in thread-safe client map
  ├── Start read goroutine (drains incoming messages for protocol compliance)
  └── Start heartbeat goroutine (ping every N seconds)
```

### 14.2 Broadcast

```go
WebSocketHandler.BroadcastLogUpdate(logEntry)   // New log entry
WebSocketHandler.BroadcastMCPLogUpdate(mcpLog)  // New MCP log
WebSocketHandler.BroadcastEvent(event)           // Generic event
```

Each broadcast iterates the client map and writes to each connection under a per-connection mutex to prevent concurrent write panics.

### 14.3 Message Format

All messages are JSON: `{ "type": "<channel>", "payload": <data> }`

Clients subscribe to channels by type in the UI `useWebSocket` hook.

### 14.4 WebSocket Responses Mode

`/ws/responses/*` supports an alternative streaming mode where the server maintains upstream connections (`bfws.Pool`) and multiplexes streaming AI responses over WebSocket instead of SSE. This is used for specific integration patterns requiring bidirectional WebSocket communication.

---

## 15. Static UI Serving

The Next.js SPA is embedded at build time:

```go
//go:embed all:ui
var uiFS embed.FS
```

`UIHandler` serves files from `uiFS`. It is registered **last** in the router so it acts as a catch-all for all routes not matched by API handlers (SPA client-side routing support).

Static assets (`/_next/static/`, `/assets/`) are served with long-lived cache headers. `index.html` is served for all unmatched paths to support SPA deep-linking.

---

## 16. CORS & Security Headers

### 16.1 CORS Logic

```
Request arrives with Origin header
  ↓
Is origin "localhost" or "127.0.0.1" or "::1"?
  → Always allowed (+ credentials)
  ↓
Does origin match allowed_origins list?
  → Set Access-Control-Allow-Origin: <origin>
  → Set Access-Control-Allow-Credentials: true
  ↓
For OPTIONS preflight:
  → Return 200 with CORS headers
  → Reflect requested headers in credentialed mode
  ↓
Origin not in allowlist:
  → 403 Forbidden
```

**Config:** `client.allowed_origins` — array of exact origins or `"*"`.

**Security note:** Per Fetch spec, `Access-Control-Allow-Headers: *` with credentials is treated as a literal `"*"`, not a wildcard. Bifrost reflects back the specific requested headers for credentialed preflight requests.

### 16.2 CORS Headers Set

| Header | Value |
|--------|-------|
| `Access-Control-Allow-Origin` | Matched origin |
| `Access-Control-Allow-Methods` | GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD |
| `Access-Control-Allow-Headers` | Default set + configured headers |
| `Access-Control-Allow-Credentials` | `true` |
| `Access-Control-Max-Age` | `86400` |
| `Vary` | `Origin` |

---

## 17. Deployment

### 17.1 Docker Multi-Stage Build

```dockerfile
Stage 1 (Node.js 22 alpine):
  - npm install & build UI (Next.js static export)
  - Output: ./ui/out/

Stage 2 (Go 1.24):
  - Copy UI build output to transports/bifrost-http/ui/
  - go build -tags sqlite_omit_load_extension
  - Static binary with embedded UI

Stage 3 (Alpine 3.23):
  - Copy binary from Stage 2
  - Volume: /app/data (SQLite, config, logs)
  - EXPOSE 8080
  - HEALTHCHECK: GET /health
```

### 17.2 Runtime Tuning

Environment variables:

| Variable | Default | Purpose |
|----------|---------|---------|
| `GOGC` | `100` | Go GC target percentage |
| `GOMEMLIMIT` | unset | Soft memory limit |
| `BIFROST_HOST` | `localhost` | Bind address |
| `LOG_LEVEL` | `info` | Log verbosity |
| `BIFROST_ENCRYPTION_KEY` | unset | Enable at-rest encryption |

### 17.3 CLI Flags

```bash
bifrost-http \
  -port 8080 \
  -host 0.0.0.0 \
  -app-dir /app/data \
  -log-level info \
  -log-style json
```

### 17.4 Graceful Shutdown

On SIGINT or SIGTERM:
1. Stop accepting new connections
2. Drain in-flight requests (fasthttp graceful shutdown)
3. Call `plugin.Cleanup()` for all registered plugins (flushes logs, pushes final metrics)
4. Close ConfigStore and LogStore connections
5. Exit

---

*Derived from source code analysis of `/transports/bifrost-http/` as of 2026-04-05.*
