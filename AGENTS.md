# AGENTS.md — Bifrost AI Gateway

> Context for AI agents (Claude Code, Copilot, Cursor, etc.) working on this codebase. Read this fully before making changes.

## What is Bifrost?

Bifrost is a high-performance AI gateway that unifies 20+ LLM providers behind a single OpenAI-compatible API with ~11µs overhead at 5,000 RPS. It also serves as an MCP (Model Context Protocol) gateway, turning static chat models into tool-calling agents.

GitHub: `maximhq/bifrost`

---

## Repository Layout

```
bifrost/
├── core/                           # Go core library — the engine
│   ├── bifrost.go                  # Main struct, request queuing, provider lifecycle (~3.4K lines)
│   ├── inference.go                # Inference routing, fallbacks, streaming dispatch (~1.9K lines)
│   ├── mcp.go                     # MCP integration entry point
│   ├── schemas/                   # ALL shared Go types — 41 files
│   │   ├── bifrost.go             # BifrostConfig, ModelProvider enum, RequestType enum, context keys
│   │   ├── provider.go            # Provider interface (30+ methods), NetworkConfig, ProviderConfig
│   │   ├── plugin.go              # LLMPlugin, MCPPlugin, HTTPTransportPlugin, ObservabilityPlugin
│   │   ├── context.go             # BifrostContext (custom context.Context with mutable values)
│   │   ├── chatcompletions.go     # Chat completion request/response types
│   │   ├── responses.go           # OpenAI Responses API types
│   │   ├── embedding.go           # Embedding types
│   │   ├── images.go              # Image generation types
│   │   ├── batch.go               # Batch operation types
│   │   ├── files.go               # File management types
│   │   ├── mcp.go                 # MCP types
│   │   ├── trace.go               # Tracer interface
│   │   └── logger.go              # Logger interface
│   ├── providers/                 # 20+ provider implementations
│   │   ├── openai/                # Reference implementation (largest, most complete)
│   │   ├── anthropic/             # Non-OpenAI-compatible example
│   │   ├── bedrock/               # AWS event-stream protocol
│   │   ├── gemini/                # Google-specific API shape
│   │   ├── groq/                  # OpenAI-compatible (minimal, delegates to openai/)
│   │   └── utils/                 # Shared: HTTP client, SSE parsing, error handling, scanner pool
│   ├── pool/                      # Generic Pool[T] — dual-mode (prod: sync.Pool, debug: full tracking)
│   │   ├── pool_prod.go           # Zero-overhead sync.Pool wrapper (default build)
│   │   └── pool_debug.go          # Double-release/use-after-release/leak detection (-tags pooldebug)
│   ├── mcp/                       # MCP protocol implementation
│   │   ├── agent.go               # Agent orchestration loop (multi-turn tool calling)
│   │   ├── clientmanager.go       # MCP client lifecycle management
│   │   ├── toolmanager.go         # Tool registration, discovery, filtering
│   │   ├── healthmonitor.go       # Client health monitoring
│   │   └── codemode/starlark/     # Starlark sandbox for code-mode execution
│   └── internal/
│       ├── llmtests/              # LLM integration test infra (48 files, scenario-based)
│       └── mcptests/              # MCP/Agent test infra (40+ files, mock-based)
│
├── framework/                     # Data persistence, streaming, ecosystem utilities
│   ├── configstore/               # Config storage backends (file, postgres)
│   ├── logstore/                  # Log storage backends (file, postgres)
│   ├── vectorstore/               # Vector storage (Weaviate, Qdrant, Redis, Pinecone)
│   ├── streaming/                 # Streaming accumulator, delta copying, response marshaling
│   │   ├── accumulator.go         # Chunk accumulation into full response (~24KB)
│   │   ├── chat.go                # Chat stream handling (~17KB)
│   │   └── responses.go           # Response stream marshaling (~35KB)
│   ├── modelcatalog/              # Model metadata registry
│   ├── tracing/                   # Distributed tracing helpers
│   └── encrypt/                   # Encryption utilities
│
├── transports/
│   ├── config.schema.json         # JSON Schema — THE source of truth for config.json (~2700 lines)
│   └── bifrost-http/              # HTTP gateway transport
│       ├── server/                # Server lifecycle, route registration
│       ├── handlers/              # 27 HTTP endpoint handlers
│       │   ├── inference.go       # Chat/text completions, responses API (~109KB)
│       │   ├── mcpinference.go    # MCP tool execution
│       │   ├── governance.go      # Virtual keys, teams, customers, budgets (~100KB)
│       │   ├── providers.go       # Provider CRUD, key management
│       │   ├── mcp.go             # MCP client registry management
│       │   ├── logging.go         # Log queries, stats, histograms
│       │   ├── config.go          # System configuration
│       │   ├── plugins.go         # Plugin CRUD
│       │   ├── cache.go           # Cache management
│       │   ├── session.go         # Auth/session management
│       │   ├── health.go          # Health checks
│       │   ├── mcpserver.go       # MCP server (SSE/streamable HTTP)
│       │   ├── websocket.go       # WebSocket handler
│       │   ├── devpprof.go        # Pool debug profiler endpoint (~23KB)
│       │   └── middlewares.go     # Middleware definitions
│       ├── lib/                   # ChainMiddlewares, config, context conversion
│       └── integrations/          # SDK compatibility layers
│           ├── openai.go          # OpenAI SDK drop-in compatibility
│           ├── anthropic.go       # Anthropic SDK compatibility
│           ├── bedrock.go         # AWS Bedrock SDK compatibility
│           ├── genai.go           # Google GenAI SDK compatibility
│           ├── langchain.go       # LangChain compatibility
│           ├── litellm.go         # LiteLLM compatibility
│           └── pydanticai.go      # PydanticAI compatibility
│
├── plugins/                       # Go plugins — each has own go.mod
│   ├── governance/                # Budget, rate limiting, virtual keys, routing, RBAC
│   ├── telemetry/                 # Prometheus metrics, push gateway
│   ├── logging/                   # Request/response audit logging
│   ├── localcache/                # Direct & Semantic response caching via vector store
│   ├── otel/                      # OpenTelemetry tracing
│   ├── mocker/                    # Mock responses for testing
│   ├── jsonparser/                # JSON extraction utilities
│   ├── maxim/                     # Maxim observability
│   └── compat/                    # LiteLLM SDK compatibility (HTTP transport)
│
├── ui/                            # React + vite web interface
│   ├── app/workspace/             # Feature pages (20+ workspace sections)
│   ├── components/                # Shared React components
│   └── lib/                       # Constants, utilities, types
│
├── tests/e2e/                     # Playwright E2E tests
│   ├── core/                      # Fixtures, page objects, helpers, API actions
│   └── features/                  # Per-feature test suites
│
├── docs/                          # Mintlify MDX documentation
│   ├── docs.json                  # Navigation config
│   ├── media/                     # Screenshots (ui-*.png naming convention)
│   └── (architecture|features|providers|mcp|plugins|enterprise|...)
│
├── .claude/skills/                # Claude Code skill definitions (4 skills)
├── go.work                        # Go workspace — requires Go 1.26.1
├── Makefile                       # Build, test, dev commands (1300+ lines)
└── terraform/                     # Infrastructure as Code
```

---

## Go Workspace

Bifrost is a **multi-module Go workspace**. Each module has its own `go.mod`:

```
go.work
├── core/go.mod              # github.com/maximhq/bifrost/core
├── framework/go.mod         # github.com/maximhq/bifrost/framework
├── transports/go.mod        # github.com/maximhq/bifrost/transports
└── plugins/*/go.mod         # 9 plugin modules (governance, telemetry, logging, etc.)
```

**Rules:**
- Run `go mod tidy` in the **specific module directory**, not the root
- Cross-module imports resolve via workspace locally, but need explicit `require` in `go.mod` for releases
- The workspace requires **Go 1.26.1** (`go.work` directive)

---

## Build, Test & Dev Commands

```bash
# Development
make dev                                 # Full local dev (UI + API with hot reload via air)
make build                               # Build bifrost-http binary

# Core tests (provider integration tests — hit live APIs)
make test-core                           # All providers
make test-core PROVIDER=openai           # Specific provider
make test-core PROVIDER=openai TESTCASE=TestSimpleChat  # Specific test
make test-core PATTERN=TestStreaming      # Tests matching pattern
make test-core DEBUG=1                   # With Delve debugger on :2345

# MCP/Agent tests (mock-based, no live APIs)
make test-mcp                            # All MCP tests
make test-mcp TESTCASE=TestAgentLoop     # Specific test
make test-mcp TYPE=agent                 # By category (agent|tool|connection|codemode)

# Plugin tests
make test-plugins                        # All plugins
make test-governance                     # Governance plugin specifically

# Integration tests (SDK compatibility)
make test-integrations-py                # Python SDK tests
make test-integrations-ts                # TypeScript SDK tests

# E2E tests (Playwright, requires running dev server)
make run-e2e                             # All E2E tests
make run-e2e FLOW=providers              # Specific feature

# Code quality
make lint                                # Linting
make fmt                                 # Format code
```

---

## Architecture

### Request Flow

```
Client HTTP Request
  → FastHTTP Transport (parsing, validation ~2µs)
    → SDK Integration Layer (OpenAI/Anthropic/Bedrock format → Bifrost format)
      → Middleware Chain (lib.ChainMiddlewares, applied per-route)
        → HTTPTransportPreHook (HTTP-level plugins, can short-circuit)
          → PreLLMHook Pipeline (auth, rate-limit, cache check — registration order)
            → MCP Tool Discovery & Injection (if tool_choice present)
              → Provider Queue (channel-based, per-provider isolation)
                → Worker picks up request
                  → Key Selection (~10ns weighted random)
                    → Provider API Call (fasthttp client, connection pooling)
                      → Response / SSE Stream
                → PostLLMHook Pipeline (reverse order of PreLLMHooks)
              → Tool Execution Loop (if tool_calls in response, MCP agent loop)
            → HTTPTransportPostHook (reverse order)
          → Response Serialization
        → HTTP Response to Client
```

### Design Principles

- **Provider isolation**: Each provider has its own worker pool and queue. One provider going down doesn't cascade to others.
- **Channel-based async**: Request routing uses Go channels (`chan *ChannelMessage`), not mutexes. The `ProviderQueue` struct manages channel lifecycle with atomic flags.
- **Object pooling everywhere**: `sync.Pool` wrappers reduce GC pressure. Pools exist for: channel messages, response channels, error channels, stream channels, plugin pipelines, MCP requests, HTTP request/response objects, scanner buffers.
- **Plugin pipeline symmetry**: Pre-hooks execute in registration order, post-hooks in **reverse** order (LIFO). For every pre-hook executed, the corresponding post-hook is guaranteed to run.
- **Streaming**: SSE chunks flow through `chan chan *schemas.BifrostStreamChunk`. Accumulated into full response for post-hooks via `framework/streaming/accumulator.go`.

### BifrostContext — Custom Context

`BifrostContext` (`core/schemas/context.go`) is a custom `context.Context` with **thread-safe mutable values**. Unlike standard Go contexts, values can be set after creation:

```go
ctx := schemas.NewBifrostContext(parent, deadline)
ctx.SetValue(key, value)     // Thread-safe, uses RWMutex
ctx.WithValue(key, value)    // Chainable variant
```

**Reserved context keys** (set by Bifrost internals — DO NOT set manually):
- `BifrostContextKeySelectedKeyID/Name` — Set by governance plugin
- `BifrostContextKeyGovernance*` — Set by governance plugin
- `BifrostContextKeyNumberOfRetries`, `BifrostContextKeyFallbackIndex` — Set by retry/fallback logic
- `BifrostContextKeyStreamEndIndicator` — Set by streaming infrastructure
- `BifrostContextKeyTrace*`, `BifrostContextKeySpan*` — Set by tracing middleware

**User-settable keys** (plugins and handlers can set these):
- `BifrostContextKeyVirtualKey` (`x-bf-vk`) — Virtual key for governance
- `BifrostContextKeyAPIKeyName` (`x-bf-api-key`) — Explicit key selection by name
- `BifrostContextKeyAPIKeyID` (`x-bf-api-key-id`) — Explicit key selection by ID (takes priority over name)
- `BifrostContextKeyRequestID` — Request ID
- `BifrostContextKeyExtraHeaders` — Extra headers to forward to provider
- `BifrostContextKeyURLPath` — Custom URL path for provider
- `BifrostContextKeySkipKeySelection` — Skip key selection (pass empty key)
- `BifrostContextKeyUseRawRequestBody` — Send raw body directly to provider

**Gotcha**: `BlockRestrictedWrites()` silently drops writes to reserved keys. This prevents plugins from accidentally overwriting internal state.

---

## Core Patterns

### Provider Implementation

There are **two categories** of providers:

**Category 1: Non-OpenAI-compatible** (Anthropic, Bedrock, Gemini, Cohere, HuggingFace, Replicate, ElevenLabs):
```
core/providers/<name>/
├── <name>.go              # Controller: constructor, interface methods, HTTP orchestration
├── <name>_test.go         # Tests
├── types.go               # ALL provider-specific structs (PascalCase prefixed with provider name)
├── utils.go               # Constants, base URLs, helpers (camelCase for unexported)
├── errors.go              # Error parsing: provider HTTP error → *schemas.BifrostError
├── chat.go                # Chat request/response converters
├── embedding.go           # Embedding converters (if supported)
├── images.go              # Image generation (if supported)
├── speech.go              # TTS/STT (if supported)
└── responses.go           # Responses API + streaming converters
```

**Category 2: OpenAI-compatible** (Groq, Cerebras, Ollama, Perplexity, OpenRouter, Parasail, Nebius, xAI, SGL):
```
core/providers/<name>/
├── <name>.go              # Minimal — constructor + delegates to openai.HandleOpenAI* functions
└── <name>_test.go         # Tests
```

**Converter function naming convention:**
- `To<ProviderName><Feature>Request()` — Bifrost schema → Provider API format
- `ToBifrost<Feature>Response()` — Provider API format → Bifrost schema
- These must be **pure transformation functions** — no HTTP calls, no logging, no side effects

**Provider constructor pattern:**
```go
func NewProvider(config schemas.ProviderConfig) (*Provider, error) {
    // Validate config, set up fasthttp.Client with connection pooling
    client := &fasthttp.Client{
        MaxConnsPerHost:     config.NetworkConfig.MaxConnsPerHost, // configurable, default 5000
        MaxIdleConnDuration: 30 * time.Second,
    }
    // After ConfigureProxy/ConfigureDialer/ConfigureTLS, build a sibling client
    // for streaming. BuildStreamingClient zeros ReadTimeout/WriteTimeout/MaxConnDuration
    // so streams aren't killed by fasthttp's whole-response deadline; per-chunk idle
    // is enforced at the app layer via NewIdleTimeoutReader.
    streamingClient := providerUtils.BuildStreamingClient(client)
    return &Provider{client: client, streamingClient: streamingClient, ...}, nil
}
```

**Streaming vs unary client:** Every provider holds two clients — `client` for unary requests (`ReadTimeout=30s` bounds the whole response) and `streamingClient` for SSE / EventStream / chunked paths (`ReadTimeout=0`; the per-chunk `NewIdleTimeoutReader` is the only governor). Pass `provider.streamingClient` to every `Handle*Streaming` / `Handle*StreamRequest` helper and to direct `Do` calls inside `*Stream` methods. For new providers, apply the same pattern — missing the switch means streams get killed at 30s.

**Note:** Bedrock uses `net/http` (not fasthttp) with HTTP/2 support. Its `http.Transport` is configured with `ForceAttemptHTTP2: true` and `MaxConnsPerHost` from `NetworkConfig` to allow multiple HTTP/2 connections when the server's per-connection stream limit (100 for AWS Bedrock) is reached. Use `providerUtils.BuildStreamingHTTPClient(client)` to derive the streaming variant — it shares the base `Transport` (safe for concurrent reuse) but clears `Client.Timeout`.

### The Provider Interface

`core/schemas/provider.go` defines the `Provider` interface with **30+ methods**. Every provider must implement all of them (returning "not supported" for unsupported operations). The interface covers:

- `ListModels`, `ChatCompletion`, `ChatCompletionStream`
- `Responses`, `ResponsesStream` (OpenAI Responses API)
- `TextCompletion`, `TextCompletionStream`
- `Embedding`, `Speech`, `SpeechStream`, `Transcription`, `TranscriptionStream`
- `ImageGeneration`, `ImageGenerationStream`, `ImageEdit`, `ImageEditStream`, `ImageVariation`
- `CountTokens`
- `Batch*` (Create, List, Retrieve, Cancel, Results)
- `File*` (Upload, List, Retrieve, Delete, Content)
- `Container*` and `ContainerFile*` (Create, List, Retrieve, Delete, Content)

**Streaming methods** receive a `PostHookRunner` callback and return `chan *BifrostStreamChunk`:
```go
ChatCompletionStream(ctx *BifrostContext, postHookRunner PostHookRunner, key Key, request *BifrostChatRequest) (chan *BifrostStreamChunk, *BifrostError)
```

### Error Handling

Each provider has `errors.go` with an `ErrorConverter` function:
```go
type ErrorConverter func(resp *fasthttp.Response, requestType schemas.RequestType, providerName schemas.ModelProvider, model string) *schemas.BifrostError
```

The shared utility `providerUtils.HandleProviderAPIError()` handles common HTTP error parsing. Provider-specific parsers add extra field mapping. Errors always carry metadata:
```go
bifrostErr.ExtraFields.Provider = providerName
bifrostErr.ExtraFields.ModelRequested = model
bifrostErr.ExtraFields.RequestType = requestType
```

### Plugin System

Four plugin interfaces exist:

| Interface | Hook Methods | When Called |
|-----------|-------------|------------|
| `LLMPlugin` | `PreLLMHook`, `PostLLMHook` | Every LLM request (SDK + HTTP) |
| `MCPPlugin` | `PreMCPHook`, `PostMCPHook` | Every MCP tool execution |
| `HTTPTransportPlugin` | `HTTPTransportPreHook`, `HTTPTransportPostHook`, `HTTPTransportStreamChunkHook` | HTTP gateway only (not Go SDK) |
| `ObservabilityPlugin` | `Inject(ctx, trace)` | Async, after response written to wire |

**Key plugin behaviors:**
- Plugin errors are **logged as warnings**, never returned to the caller
- Pre-hooks can **short-circuit** by returning `*LLMPluginShortCircuit` (cache hit, auth failure, rate limit)
- Post-hooks receive both response and error — either can be nil. Plugins can **recover from errors** (set error to nil, provide response) or **invalidate responses** (set response to nil, provide error)
- `BifrostError.AllowFallbacks` controls whether fallback providers are tried: `nil` or `&true` = allow, `&false` = block
- `HTTPTransportStreamChunkHook` is called **per-chunk** during streaming — can modify, skip, or abort the stream

### Pool System

`core/pool/` provides `Pool[T]` with two build modes:

```go
// Production (default): zero-overhead sync.Pool wrapper
// Debug (-tags pooldebug): tracks double-release, use-after-release, leaks with stack traces
p := pool.New[MyType]("descriptive-name", func() *MyType { return &MyType{} })
obj := p.Get()
// ... use obj ...
// MUST reset ALL fields before Put — pool does not auto-reset
p.Put(obj)
```

**Acquire/Release pattern** for types with complex reset logic (used in `schemas/plugin.go`):
```go
req := schemas.AcquireHTTPRequest()    // Get from pool, pre-allocated maps
defer schemas.ReleaseHTTPRequest(req)  // Clears all maps and fields, returns to pool
```

### HTTP Transport Layer

**Handler pattern:** Handlers are structs with injected dependencies:
```go
type CompletionHandler struct {
    client       *bifrost.Bifrost
    handlerStore lib.HandlerStore
    config       *lib.Config
}
```

**Route registration:** Each handler implements `RegisterRoutes(router, middlewares...)` — routes get middleware chains applied per-route via `lib.ChainMiddlewares()`.

**SDK integration layers** (`transports/bifrost-http/integrations/`) provide request/response converters between provider-native SDK formats and Bifrost's internal format. This enables drop-in replacement of OpenAI SDK, Anthropic SDK, AWS Bedrock SDK, Google GenAI SDK, LangChain, and LiteLLM.

---

## Gotchas

### 1. Always Reset Pooled Objects Before Put

Every pooled object must have **all** fields zeroed before `pool.Put()`. Stale data leaks between requests. The debug build catches double-release and use-after-release but **not** missing resets.

```go
// WRONG — stale data from previous request leaks to next user
pool.Put(msg)

// RIGHT
msg.Response = nil
msg.Error = nil
msg.Context = nil
msg.ResponseStream = nil
pool.Put(msg)
```

### 2. Channel Lifecycle — ProviderQueue Pattern

`ProviderQueue` uses atomic flags and `sync.Once` to prevent "send on closed channel" panics:
```go
type ProviderQueue struct {
    queue      chan *ChannelMessage
    done       chan struct{}
    closing    uint32         // atomic: 0=open, 1=closing
    signalOnce sync.Once      // ensure signal fires only once
    closeOnce  sync.Once      // ensure close fires only once
}
```
Always check the atomic closing flag before sending. Never close a channel without this pattern.

### 3. NetworkConfig Duration Serialization

`RetryBackoffInitial` and `RetryBackoffMax` are `time.Duration` (nanoseconds) in Go but **milliseconds** (integers) in JSON. Custom `MarshalJSON`/`UnmarshalJSON` handles conversion. If adding new duration fields to any config struct, follow this pattern exactly.

### 4. ExtraHeaders — Defensive Map Copy

`NetworkConfig.ExtraHeaders` is deep-copied in `CheckAndSetDefaults()` to prevent data races between concurrent requests. Apply the same `maps.Copy()` pattern to any new map fields in config structs.

### 5. Provider Interface Has 30+ Methods

Adding a new operation type requires changes across the entire codebase:
1. Add method to `Provider` interface in `core/schemas/provider.go`
2. Implement in **all** 20+ providers (most return "not supported")
3. Add `RequestType` constant in `core/schemas/bifrost.go`
4. Add to `AllowedRequests` struct and `IsOperationAllowed()` switch
5. Add handler endpoint in `transports/bifrost-http/handlers/`
6. Wire up in `core/bifrost.go` and `core/inference.go`

### 6. OpenAI Provider Changes Cascade to 9+ Providers

Groq, Cerebras, Ollama, Perplexity, OpenRouter, Parasail, Nebius, xAI, and SGL all delegate to `openai.HandleOpenAI*` functions. **Any change to OpenAI converter logic affects all of them.** Always test broadly: `make test-core` (all providers).

### 7. Scanner Buffer Pool Has a Capacity Cap

The SSE scanner buffer pool in `core/providers/utils/utils.go` starts at 4KB. Buffers grow dynamically but those exceeding **64KB are discarded** (not returned to pool) to prevent memory bloat. Be aware when working with providers that send very large SSE events.

### 8. Plugin Execution Order is Meaningful

Pre-hooks: registration order (first registered → first to run). Post-hooks: **reverse** order. This creates "wrapping" semantics — the first plugin registered is the outermost wrapper (its pre-hook runs first, post-hook runs last). Changing registration order changes behavior.

### 9. Fallbacks Re-execute the Full Plugin Pipeline

When a provider fails and the request falls to a fallback, the **entire plugin pipeline** re-executes from scratch. Governance checks, caching, and logging all run again for each attempt. Intentional, but surprising when debugging request counts or cost tracking.

### 10. `AllowedRequests` Nil Semantics

A **nil** `*AllowedRequests` means "all operations allowed." A **non-nil** value only allows fields explicitly set to `true`. This applies to both `ProviderConfig.AllowedRequests` and `CustomProviderConfig.AllowedRequests`.

### 11. BifrostContext Reserved Keys Are Silently Dropped

When `BlockRestrictedWrites()` is active, writes to reserved keys (governance IDs, retry counts, fallback index, etc.) are **silently ignored** — no error. If your plugin needs to pass data through context, use your own custom key type.

### 12. `fasthttp`, Not `net/http`

Bifrost uses `github.com/valyala/fasthttp` for provider HTTP calls. The API is different from `net/http`:
- Use `fasthttp.AcquireRequest()`/`fasthttp.ReleaseRequest()` for lifecycle
- `fasthttp.Client` pools connections per-host (`NetworkConfig.MaxConnsPerHost`, default 5000, 30s idle)
- Request/response bodies accessed via `resp.Body()` (returns `[]byte`, not `io.Reader`)
- **Exception:** Bedrock uses `net/http` (for AWS SigV4 signing) with `http.Transport` configured for HTTP/2 multi-connection support

### 13. `sonic`, Not `encoding/json`

JSON marshaling in hot paths uses `github.com/bytedance/sonic` for performance. `core/schemas/` uses standard `encoding/json` for custom marshaling (e.g., `NetworkConfig`). Don't mix them accidentally.

### 14. Atomic Pointer for Hot Config Reload

`Bifrost` uses `atomic.Pointer` for providers and plugins lists. On updates: create new slice → atomically swap pointer. **Never mutate the slice in place** — concurrent readers would see partial state.

### 15. MCP Tool Filtering is 4 Levels Deep

Tool access follows: Global filter → Client-level filter → Tool-level filter → Per-request filter (HTTP headers). All four levels must agree for a tool to be available. Changes to filtering logic must respect this hierarchy.

### 16. `config.schema.json` is the Source of Truth

`transports/config.schema.json` (~2700 lines) is the authoritative definition for all `config.json` fields. Documentation examples must match. When adding config fields: update schema first → handlers → docs.

### 17. UI `data-testid` Attributes Are Load-Bearing

E2E tests depend on `data-testid` attributes. Convention: `data-testid="<entity>-<element>-<qualifier>"`. If you rename or remove one, search `tests/e2e/` for references. If you add new interactive elements, add `data-testid`.

### 18. E2E Tests — Never Marshal Payloads to Maps

In `tests/e2e/core/`, **never marshal API payloads to a `Record`/`Map`/plain-object and then re-serialize**. Field ordering matters for backend validation and snapshot comparisons. Construct payloads as object literals with fields in the intended order and pass directly to Playwright's `request.post({ data })`. Avoid `Object.fromEntries()`, `JSON.parse(JSON.stringify(...))` round-trips, or destructuring into an intermediate `Record<string, unknown>` — these can silently reorder fields.

---

## Adding a New Provider — Full Checklist

1. Create `core/providers/<name>/` with files per the pattern (see "Provider Implementation" above)
2. Add `ModelProvider` constant in `core/schemas/bifrost.go`
3. Add to `StandardProviders` list in `core/schemas/bifrost.go`
4. Register in `core/bifrost.go` — add import + case in provider init switch
5. **UI integration** (all required):
   - `ui/lib/constants/config.ts` — model placeholder + key requirement
   - `ui/lib/constants/icons.tsx` — provider icon
   - `ui/lib/constants/logs.ts` — provider display name (2 places)
   - `docs/openapi/openapi.json` — OpenAPI spec update
   - `transports/config.schema.json` — config schema (2 locations)
6. **CI/CD**: Add env vars to `.github/workflows/pr-tests.yml` and `release-pipeline.yml` (4 jobs)
7. **Docs**: Create `docs/providers/supported-providers/<name>.mdx`
8. **Test**: `make test-core PROVIDER=<name>`

---

## Testing

### Always prefer `make test-core` over raw `go test` for provider-level tests

The `make test-core` target is the canonical harness for provider tests — it wires up env vars from `.env` (provider API keys), invokes the per-provider `{provider}_test.go` entrypoint in `core/providers/<provider>/`, and routes through the shared `core/internal/llmtests/` scenario suite that validates end-to-end behavior (including streaming).

Running bare `go test ./core/providers/<provider>/...` only executes unit tests and skips the llmtests scenarios — so it won't catch regressions in streaming, tool-calling, or provider-specific response shapes.

```bash
make test-core PROVIDER=anthropic TESTCASE=TestChatCompletionStream   # exact test
make test-core PROVIDER=openai PATTERN=Stream                          # substring match
make test-core PROVIDER=bedrock                                        # all scenarios for one provider
make test-core DEBUG=1 PROVIDER=gemini TESTCASE=TestResponsesStream    # attach Delve on :2345
```

`PATTERN` and `TESTCASE` are mutually exclusive. Provider name must match a directory under `core/providers/` (e.g. `anthropic`, `openai`, `bedrock`, `vertex`, `azure`, `gemini`, `cohere`, `mistral`, `groq`, etc.).

### LLM Tests (`core/internal/llmtests/`)

Scenario-based tests that run against **live provider APIs** with dual-API testing (Chat Completions + Responses API):

```go
func RunMyScenarioTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, cfg ComprehensiveTestConfig) {
    // Use validation presets: BasicChatExpectations(), ToolCallExpectations(), etc.
    // Use retry framework for flaky assertions
}
```

- Register in `tests.go` `testScenarios` slice
- Add `Scenarios.MyScenario` flag to `ComprehensiveTestConfig`
- Run: `make test-core PROVIDER=<name> TESTCASE=<TestName>`

### MCP Tests (`core/internal/mcptests/`)

Mock-based tests with `DynamicLLMMocker` and declarative setup:

```go
manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
    InProcessTools:   []string{"echo", "calculator"},
    AutoExecuteTools: []string{"*"},
    MaxDepth:         5,
})
// Queue mock LLM responses, assert tool execution order
```

Categories: `agent_*_test.go`, `tool_*_test.go`, `connection_*_test.go`, `codemode_*_test.go`

Run: `make test-mcp TESTCASE=<TestName>`

### E2E Tests (`tests/e2e/`)

Playwright tests with page objects, data factories, fixtures:

- Page objects extend `BasePage`, use `getByTestId()` as primary selector strategy
- Data factories use `Date.now()` for unique names (prevents collision in parallel runs)
- Track created resources in arrays, clean up in `afterEach`
- Import `test`/`expect` from `../../core/fixtures/base.fixture` (never from `@playwright/test`)
- **Never marshal API payloads to a `Record`/`Map`/plain-object and then re-serialize.** Field ordering matters for snapshot comparisons and some backend validations. Construct payloads as object literals with fields in the intended order and pass directly to Playwright's `request.post({ data })`. Do NOT destructure into an intermediate `Record<string, unknown>` or use `Object.fromEntries()` / `JSON.parse(JSON.stringify(...))` round-trips, as these can reorder fields.

Run: `make run-e2e FLOW=<feature>`

---

## Claude Code Skills

Four skills are available via `/skill-name`:

### `/docs-writer <feature-name>`
Write, update, or review Mintlify MDX documentation. Researches UI code, Go handlers, and config schema. Validates `config.json` examples against `transports/config.schema.json`. Outputs docs with Web UI / API / config.json tabs.

Variants: `/docs-writer update <doc-path>`, `/docs-writer review <doc-path>`

### `/e2e-test <feature-name>`
Create, run, debug, audit, or auto-update Playwright E2E tests.

Variants:
- `/e2e-test fix <spec>` — Debug and fix a failing test
- `/e2e-test sync` — Detect UI changes, update affected tests automatically
- `/e2e-test audit` — Scan specs for incorrect/weak assertions (P0-P6 severity scale)

### `/investigate-issue <issue-id>`
Investigate a GitHub issue from `maximhq/bifrost`. Fetches issue details, classifies by type/area, searches codebase, traces dependencies, analyzes side effects, suggests tests (LLM/MCP/E2E), and presents an implementation plan with per-change approval gates.

### `/resolve-pr-comments <pr-number>`
Systematically address unresolved PR review comments. Uses GraphQL to get unresolved threads, presents each with FIX/REPLY/SKIP options, collects fixes locally, and only posts replies **after code is pushed** to remote.

---

## Common Workflows

### Modify chat completions across all providers
1. Change types in `core/schemas/chatcompletions.go`
2. Update converter functions in each provider's `chat.go`
3. If streaming affected, update `framework/streaming/` (accumulator, delta copy)
4. Run `make test-core` (all providers)

### Add a new field to API responses
1. Add to schema type in `core/schemas/`
2. Map in provider response converter (`ToBifrost*Response`)
3. Handle in streaming accumulator if applicable
4. Update HTTP handler if field needs special serialization
5. Update `transports/config.schema.json` if configurable

### Add a new plugin
1. Create `plugins/<name>/` with its own `go.mod`
2. Implement `LLMPlugin`, `MCPPlugin`, or `HTTPTransportPlugin` interface
3. Add to `go.work`
4. Register in transport layer or Bifrost config
5. Add test targets to `Makefile`

### Modify a UI feature
1. Find workspace page: `ui/app/workspace/<feature>/`
2. Check existing `data-testid` attributes — E2E tests depend on them
3. Add `data-testid` to new interactive elements
4. Run `make run-e2e FLOW=<feature>` to verify
5. If E2E tests break, use `/e2e-test sync` to update them

---

## Key Files Quick Reference

| What | Where |
|------|-------|
| Main Bifrost struct & queuing | `core/bifrost.go` |
| Inference routing & fallbacks | `core/inference.go` |
| Provider interface (30+ methods) | `core/schemas/provider.go` |
| ModelProvider enum & context keys | `core/schemas/bifrost.go` |
| Plugin interfaces & pooled HTTP types | `core/schemas/plugin.go` |
| BifrostContext (mutable context) | `core/schemas/context.go` |
| Chat completion types | `core/schemas/chatcompletions.go` |
| Responses API types | `core/schemas/responses.go` |
| Object pool (prod + debug) | `core/pool/pool_prod.go`, `pool_debug.go` |
| Shared provider utils & SSE parsing | `core/providers/utils/utils.go` |
| Streaming accumulator | `framework/streaming/accumulator.go` |
| HTTP inference handler | `transports/bifrost-http/handlers/inference.go` |
| Governance handler | `transports/bifrost-http/handlers/governance.go` |
| Config schema (source of truth) | `transports/config.schema.json` |
| Pool debug profiler | `transports/bifrost-http/handlers/devpprof.go` |
| LLM test infrastructure | `core/internal/llmtests/` |
| MCP test infrastructure | `core/internal/mcptests/` |
| E2E test infrastructure | `tests/e2e/core/` |
| Docs navigation config | `docs/docs.json` |
| CI/CD workflows | `.github/workflows/` |

---

## Code Style

- **Go**: `gofmt`/`goimports`. No custom linter config.
- **TypeScript/React**: Oxfmt. TanStack Router.
- **JSON tags**: `snake_case` matching provider API conventions.
- **Error strings**: Lowercase, no trailing punctuation (Go convention).
- **Provider types**: Prefixed with provider name in PascalCase (`AnthropicChatRequest`, `GeminiEmbeddingResponse`).
- **Converter functions**: Pure — no side effects, no logging, no HTTP.
- **Pool names**: Descriptive string passed to `pool.New()` (e.g., `"channel-message"`, `"response-stream"`).
- **Context keys**: Use `BifrostContextKey` type. Custom plugins should define their own key types to avoid collisions.
- **Go filenames**: No underscores. The only permitted underscore is the `_test.go` suffix. Examples: `pluginpipeline.go`, `pluginpipeline_test.go` — never `plugin_pipeline.go` or `plugin_pipeline_race_test.go`. Concatenate words (lowercase, no separators) for multi-word filenames.

# Frontend Code Guidelines & Patterns

This document defines the standards, structure, and best practices for writing frontend code in this project.

---

## Tech Stack

- **React** (with Vite)
- **TypeScript**
- **@tanstack/react-router** (type-safe routing)
- **Tailwind CSS v4**
- **Radix UI** (primitives)
- **Local UI component library** (`ui/components/ui/`) built on Radix primitives

---

## Folder Structure

```text

/ui
├── app                # Routes & pages
├── components        # Shared components
│   └── ui            # Core design system components
├── hooks             # Custom React hooks
├── lib               # Utilities, helpers, shared logic
└── app/enterprise    # Enterprise-specific code (via symlink)

```

### Rules

- All frontend code must live inside `/ui`
- Routes and pages → `ui/app`
- Shared/reusable components → `ui/components`
- Core UI primitives → `ui/components/ui`
- Utilities and libraries → `ui/lib`
- Custom hooks → `ui/hooks`

---

## Libraries & Usage

### Core Libraries

- `react` → UI library
- `typescript` → Type safety
- `tailwindcss` → Styling
- `@tanstack/react-router` → Routing

### UI & Visualization

- `@radix-ui/react-*` → UI primitives
- `ui/components/ui/*` → Project's Radix-based component system
- `recharts` → Charts
- `monaco-editor` → Code editor

### Utilities

- `date-fns` → Date/time formatting
- `nuqs` → Query param state management

### Tooling

- `Oxfmt` → Code formatting
- `vitest` → Testing

---

## Routing Convention

For every new route:

```text

ui/app/<route-name>/
├── layout.tsx   # Route definition using createFileRoute
├── page.tsx     # Page content
└── views/       # Optional: route-specific components

```

### Rules

- Folder name must match route name
- Always use `createFileRoute` in `layout.tsx`
- `page.tsx` should only handle composition (not heavy logic)
- Route-specific components go inside `views/`

---

## Component Guidelines

### Reusability First

- Always check if similar components/functions already exist
- Prefer extending or refactoring existing code over duplication
- Only create new components if reuse is not feasible

---

### Component Placement

- Shared → `ui/components`
- Route-specific → `views/` inside route folder

---

### JSX & Rendering

- Avoid deeply nested conditional rendering
- Break complex UI into smaller components
- Keep components readable and maintainable

---

### Lists & Keys

- Always use **stable, unique keys**
- Never use array index as key (unless unavoidable)

---

## React Best Practices

- Avoid unnecessary or unstable dependencies in hooks
- Prevent infinite loops in `useEffect`
- Keep dependency arrays accurate and minimal
- Prefer derived state over duplicated state

---

## State Management

### Priority Order

1. Query Params (`nuqs`) → for persistent/shareable state
2. Local State → for UI-only state
3. Redux → only when truly necessary

---

### Query Params (`nuqs`)

- Use for state that should persist across refresh/navigation
- Use proper parsers like `parseAsString` or `parseAsInteger`
- Do NOT mix query param state with local/redux state
- Follow a single consistent pattern across the codebase

---

### Redux

- Use only when global/shared state is required
- Avoid unnecessary slices
- Prefer simpler alternatives when possible

---

### RTK Query (`@reduxjs/toolkit/query`)

- Use for API calls and caching
- Use **granular tags** for cache invalidation
- Avoid invalidating entire datasets unnecessarily
- Implement **optimistic updates** where applicable

---

## Forms

We use:

- `react-hook-form`
- `zod v4` (for schema validation)

### Rules

- Always define a Zod schema
- Include meaningful validation messages
- Prefer **inline field errors** (not toast notifications)
- Use `refine` / `superRefine` for complex validation
- Store schemas in: `ui/lib/types/schemas.ts`

---

## Tables

- Use `@tanstack/react-table` **only for large/complex datasets**
- For simple tables → build custom lightweight components
- Prioritize performance over abstraction

---

## ⚡ Performance Guidelines

- Lazy load heavy or rarely-used libraries
- Avoid unnecessary re-renders
- Split large components into smaller ones
- Keep bundle size minimal

---

## Dependency Rules

- Do NOT add new dependencies unless absolutely necessary
- Always pin exact versions (no `^` or `~`)
- Prefer existing libraries in the codebase

---

## TypeScript Guidelines

- Avoid using `any` unless absolutely unavoidable
- Prefer strict typing and inference
- Define reusable types in shared locations

---

## Code Quality & Formatting

After writing code:

```bash
cd ui && npm run format
````

Then verify build:

```bash
cd ui && npm run build
```

* Code must pass formatting and build checks
* Follow consistent naming and structure conventions

---

## Anti-Patterns to Avoid

* Duplicate components without considering reuse
* Mixing multiple state management approaches unnecessarily
* Overusing Redux
* Using unstable hook dependencies
* Adding heavy libraries for simple use cases
* Poorly structured or deeply nested JSX

---

## Summary

* Prioritize **reusability, performance, and consistency**
* Follow **strict folder structure and routing conventions**
* Use **the right tool for the right problem**
* Keep code **simple, predictable, and maintainable**
