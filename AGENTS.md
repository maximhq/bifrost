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
│   ├── semanticcache/             # Semantic response caching via vector store
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
├── go.work                        # Go workspace — requires Go 1.27.4
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
├── transports/go.mod        # github.com/maximhq/bifrost/transports/v2
└── plugins/*/go.mod         # 9 plugin modules (governance, telemetry, logging, etc.)
```

**Rules:**
- Run `go mod tidy` in the **specific module directory**, not the root
- Cross-module imports resolve via workspace locally, but need explicit `require` in `go.mod` for releases
- The workspace requires **Go 1.27.4** (`go.work` directive)

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

# Framework tests (require local backing services — bring them up FIRST)
docker compose -f tests/docker-compose.yml up -d   # postgres, weaviate, qdrant, pinecone, and the 4 redis variants
make test-framework                                # All framework packages

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
- **Data lives in the live store**: every piece of data a process keeps (configuration, runtime state, caches) goes in `framework/livestore`. Components keep only handles (connections, goroutines, locks, compiled programs), per-request buffers and in-flight working state: state for one request, stream or run, keyed by its ID and removed on every path that ends it (stream accumulators, pending log entries, traces being built), listed under `inflight` in `tests/rawmaps/allowlist.txt`. Anything that outlives the request belongs in the store. Pick the structure by what a miss means:
  - **Collection**: every row of one config-database kind, loaded at boot and refreshed after every write. A miss means the row does not exist.
  - **Cache**: only the values someone asked for, fetched by a loader on a miss from wherever they live (the config store included) and dropped by expiry or eviction. A miss means "go fetch".
  - **Table**: what the process itself writes, with no loader. A miss means nothing was written yet. State tables keyed by an entity ID and tables with a generation are tables.
  - **Where each lives**: `Store.Collections` holds config-store collections only, with no methods that merge records or hide state; `Store.Views` holds the tables derived from them that several packages read, rebuilt by a subscriber once per publish; a table only one component uses stays with that component. Follow `.claude/skills/live-store/SKILL.md` when adding or changing anything the live store holds.
- **Adding a config.json section**: Settings (client, server, websocket, framework, auth, proxy and the like) stay fields on `lib.Config`; everything that becomes live-store data waits in `bootConfig` (`transports/bifrost-http/lib/livestore.go`) until `loadLiveStore`, the one place collections are filled, loads it; a table its owning component fills is the exception (step 5). To add a content section:
  1. Add the section to `transports/config.schema.json` (the source of truth, rule 16).
  2. Add the field to `ConfigData`.
  3. Add a field for it to `bootConfig`, saying what fills it and whether a gateway without a config store loads it.
  4. Write its loader in three steps: parse and validate the section into that `bootConfig` field; with a config store, sync it into the database; for a collection, never write the live store.
  5. Give it a collection and the database loader `LoadAll` uses, or, for content its owning component applies itself, a table that component fills during its own setup, in both modes. Feature flags and datasheet overrides work this way: their owners fill their tables, the feature flag store from `initFeatureFlags` and the model catalog from `loadDatasheetOverridesFromFile`.
  6. For a collection, on a gateway without a config store, add its records to `bootConfig.records`, or say there that it is skipped and keep the loader's early return and warning.
  7. Extend `TestConfigJSONContentPerMode` (`lib/configcontent_test.go`), which pins what each mode loads for every kind `bootConfig` holds.

  `TestRepositoryKeepsDataInTheLiveStore` (`tests/rawmaps`, a module of its own; run it with `cd tests/rawmaps && GOWORK=off go test -count=1 ./...`; without `-count=1` Go reports a cached pass, since it does not notice changed files outside the test module) fails on a new `sync.Map` field or package-level `sync.Map`, a map on a struct with a mutex, maps behind an atomic pointer, or a package-level map a function writes; when one genuinely cannot go in the store, add it to `tests/rawmaps/allowlist.txt` with its group and reason.

- **Writes reach memory the same way for every entity**: after a database write, the live store is updated by one of three rules, whatever the entity:
  1. **Delete**: read the records that refer to the entity (its holders) and delete it in one transaction; a failed read refuses the delete and changes nothing. Then remove the entity's record with a `Remove` ref, which reads nothing and cannot fail (its children cascade in the same publish), evict the caches bound to it, and re-read the holders with retries. If that re-read still fails, answer 500, saying the delete was saved but not loaded.
  2. **Create or update**: re-read the written rows with retries, bounded by the request's deadline or by a small fixed budget when it has none. If the read still fails, keep the previous record and answer 500, saying the write was saved but not loaded.
  3. **Never write configuration to the database from live-store contents**: a partial update merges onto a database read, not onto the live record. State the process owns (usage, last reset, reset epochs, discovery status) is exempt.

  Only an explicit delete removes a record without reading it. A reload or reconcile that lists rows itself and finds a held record missing from that list passes it to `Refresh` as a plain ref, which re-reads it, instead of removing it, since the list may be older than the record. `RefreshKind` lists and publishes under every record lock, so its listing is never older than a record memory holds and its removals stand; it is meant for small kinds whose change messages name no row.

  A change applied on behalf of another process follows the same rules with `lib.DBLookupRetry()`. Every write path that re-reads (a collection's rows, or a deleted entity's holders) needs a test where that re-read fails, showing which of these outcomes it gives.

### Every change states its cost on the request flow

Bifrost's value is ~11µs of overhead at 5,000 RPS, so every change is evaluated for the memory allocations and processing time it adds to the end-to-end flow, not only for correctness. Before a PR is opened:

- **Measure, do not assert.** Code on the request path (handlers, plugins, providers, streaming, key selection, auth) ships with a `Benchmark*` in the existing `_test.go` file, run with `-benchmem`, and the PR body records allocs/op and ns/op before and after. A change that cannot be benchmarked states plainly why ("config load only", "runs once per provider rebuild").
- **The common case must not regress.** A feature that is off by default, or only applies to some keys/providers, adds zero allocations and at most a nil/empty check to requests that do not use it. Put the work behind that check, not in front of it.
- **Per-credential or per-stream state lives in a manager, not per request.** Caches keyed by credential or `RequestID` are built once and reused (see the token cache in `core/providers/utils/tokencache` and the streaming accumulator); the per-request cost is a map load, never a rebuild.
- **Name the memory that is retained.** Anything cached for longer than a request (tokens, connections, slots, buffers) states its bound in the PR body: what it is keyed by, how large an entry is, and when it is freed.

Reviewers treat a missing cost statement the same way as missing tests.

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

**Hard rule — never store stream-sized data in `BifrostContext`.** Context holds small handles only: IDs, durations, booleans, interface pointers. Any per-request state that scales with stream content (chunk buffers, accumulated payloads, replay queues, large per-request slices/maps) must live in a top-level manager keyed by `RequestID`, not in `ctx`. Reference implementations:

- `framework/streaming.Accumulator` — owns a `sync.Map` of per-stream `StreamAccumulator` entries keyed by `RequestID`. Only `BifrostContextKeyAccumulatorID` (the ID string) is stored on the context; the chunk buffers live in the manager. The pause/resume gate (`gate.go`) extends the same per-stream entry with a state machine — again, **no buffer in ctx**.
- The `Tracer` interface (in ctx as a small pointer) is the access path for plugins/providers to reach managers without putting bulky data on the context itself.

When in doubt: if your new ctx key would hold a slice/map that grows with request content, route the storage through a manager and keep only the ID in ctx.

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

**Audit changes:** A management handler that writes config records the change with `lib.RecordAuditChange` right after the write commits, before any step that can still fail (an in-memory reload, a read-back, a client rebuild, a cluster broadcast). The enterprise audit layer writes a recorded change even when the request then fails, so recording later loses the event for a change that is already saved. Never record before the write: a failed write must leave no event.

- Snapshot the row as written. When the response comes from a reload or read-back, call `rec.SetAfter(...)` with it once that step succeeds, so a successful request records what it serves.
- On a branch whose rollback undid the write, call `rec.Discard()`, also when the rollback committed and then returned `lib.SavedNotAppliedError`. Keep the event only when the rollback write itself failed, because the change is still stored.
- A loop of writes records one change per write as it commits.
- Snapshots never carry secrets: pass redacted values (`Redacted()`, `configstore.RedactedKey`).
- `TestCommitPointRule` in `handlers/auditchange_test.go` fails when a handler sends a saved-then-failed 500 ("... in database but failed to ...") before its first `RecordAuditChange`. Fix the handler; don't reword the message to get past it.
- When a lower layer commits and then fails, return `lib.SavedNotAppliedError` so the handler can tell a saved change from a failed write (`lib.IsSavedNotApplied`).

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

For reading or writing a **single field** (or a handful) inside a larger raw JSON payload, prefer `github.com/tidwall/gjson`/`github.com/tidwall/sjson` over decoding into `map[string]interface{}` and re-encoding — the shared helpers `providerUtils.GetJSONField`/`SetRawJSONField`/`DeleteJSONField`/`JSONFieldExists`/`GetJSONSubtree` (`core/providers/utils/utils.go`) wrap these and should be reused where the path is a lookup on an already-in-scope `[]byte`/`json.RawMessage`. Full-document decode into a map/struct is still correct when you need the whole shape (e.g. re-marshaling an entire object to normalize it) — the point is not to round-trip an entire object through a `map[string]interface{}` just to inspect one key. When marshaling back out, always use `providerUtils.MarshalSorted` (never a raw `sonic.Marshal`/`json.Marshal`), since unsorted map keys reorder nondeterministically and break prompt-cache-relevant byte stability.

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

### 19. Framework Tests Need `tests/docker-compose.yml`, Not `framework/docker-compose.yml`

`make test-framework` fails ~30 tests in `framework/vectorstore` with no services running. Bring the stack up first:

```bash
docker compose -f tests/docker-compose.yml up -d
```

Two compose files define overlapping services on the **same host ports** (9000, 6379, 6334, 5081), so only one can run at a time. Use the `tests/` one:

| | `tests/docker-compose.yml` | `framework/docker-compose.yml` |
|---|---|---|
| Redis | plain 6379, **TLS 6380, cluster 7000, cluster-TLS 7100** | plain 6379 only |
| TLS certs | `redis-certs-init` writes `tests/redis-certs/` | none |
| Weaviate | 1.32.4, pins `CLUSTER_ADVERTISE_ADDR` | 1.25.0, no advertise addr |

The differences are load-bearing, not cosmetic:

- `redis_test.go` dials **6380** and **7100** for the TLS and TLS-cluster client tests, and `readTestCACert` reads `tests/redis-certs/ca.crt`. The `framework/` file provides neither, so 5 tests fail against it.
- Weaviate's memberlist aborts startup with `Failed to get final advertise address: No private IP address found` unless `CLUSTER_ADVERTISE_ADDR` is set ([weaviate#7474](https://github.com/weaviate/weaviate/issues/7474)). The `tests/` file pins a static IP; the `framework/` file does not, so its Weaviate crash-loops and 4 more tests fail.

Note that `qdrant` and `pinecone` report `(unhealthy)` in `docker compose ps` under the `framework/` file because those images have no `wget` for the healthcheck. The services themselves are fine, so ignore that specific signal and probe the port instead.

Only `framework/vectorstore` needs any of this. Every other framework package passes with nothing running.

### 20. Every URL Bifrost Emits About Itself Carries `server.base_path`

Bifrost can be mounted under a URL prefix (`server.base_path`, e.g. `/bifrost` for `https://host/bifrost`). Routing, the UI shell, assets, API calls and WebSockets already handle it; the recurring regression is a **self-referencing URL or path built by hand** that assumes the domain root. Any place that prints, returns, redirects to, copies, or navigates to a Bifrost URL must go through the helpers, never string-concatenate from `/`:

| Layer | Use | Never |
|---|---|---|
| Go, absolute URL | `lib.BuildBaseURL(ctx, override)` (scheme + host + prefix; a valid explicit override is used instead, trimmed of surrounding whitespace and a trailing `/`, and never gets `BasePath()` appended; an invalid one falls back to the inferred URL) | `"https://" + host + "/..."` |
| Go, root-relative path in a response body, `Location`, or `Link` header | `lib.WithBasePath("/api/...")` | `fmt.Sprintf("/api/...")` |
| Go, scheme decisions (HSTS, cookie `Secure`, trace attributes) | `lib.RequestIsHTTPS(ctx)` | reading `X-Forwarded-Proto` yourself |
| UI, copyable or displayed URL | `getExampleBaseUrl()` / `getEndpointUrl()` (`ui/lib/utils/port.ts`), `getExternalBaseUrl()` for MCP and A2A surfaces | `window.location.origin + "/mcp"` |
| UI, path label, `window.open`, `router.history.push` | `withBasePath()` (`ui/lib/utils/basePath.ts`), or `router.navigate({ href })` which applies the router basepath | a literal `/workspace/...` outside the router |
| Docs, Helm `NOTES.txt`, Terraform, Makefile | state the prefix form next to every self URL a client or browser reaches through the proxy (`https://host/<prefix>/login`); probes and health checks that hit the service directly, bypassing the ingress, may stay on `/health` because requests are accepted without the prefix | a root-only example for a proxied URL |

Rules that follow from the implementation:

- **Requests are accepted with or without the prefix.** `basePathMiddleware` strips it, so route handlers and the `whitelisted_routes` allowlist always see root-relative paths. A base path whose first segment matches a route (`api`, `v1`, ...) or a dashboard page root (`workspace`, `login`, `oauth`, `agent`, `pprof`, compared case-insensitively because the TanStack basepath rewrite is) is rejected at startup by `lib.CheckBasePathCollision`.
- **Admin-supplied public URLs are used verbatim.** `mcp_external_client_url`, `a2a_external_client_url` and the OAuth `issuer_url` must already include the prefix; the code never appends it. Say so in any help text or doc that introduces such a setting.
- **`.well-known` discovery lives at the origin root** (RFC 8414, RFC 9728 §3.1): an issuer of `https://host/bifrost` advertises `https://host/.well-known/oauth-protected-resource/bifrost/mcp`, built by `oauth2ResourceMetadataURL`, and the proxy must forward `/.well-known/`.
- **Session cookies stay `Path=/`** on purpose: a `/bifrost/`-scoped cookie would not ride the unprefixed `/api` calls the contract allows, and fasthttp's `SetCookie` keys by name so logout could not expire both paths.
- **A redirect to a third party is marked external** with `markLocationExternal(ctx)` so `prefixLocation` leaves it alone. Otherwise, on a prefixed request, `prefixLocation` re-prefixes a root-relative `Location` and an absolute one whose authority is the request host; protocol-relative (`//host/...`) and other-host URLs are left as they are.

Tests to extend, never duplicate: Go sets the prefix with `lib.SetBasePath("/bifrost")` plus `t.Cleanup(func() { lib.SetBasePath("") })` (`lib/basepath_test.go`, `handlers/middlewares_test.go`, `handlers/mcp_test.go`); UI vitest stubs `window.__BIFROST_BASE_PATH__` (`ui/lib/utils/basePath.test.ts`, `port.test.ts`); E2E has its own worker started with `--base-path` under `tests/e2e/features/base-path/`, where every self URL the dashboard shows is asserted against `basePathOf(testInfo)`. A new self URL ships with a case in each layer it touches.

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

### Every fix and every change ships with regression tests

Any issue fix or code change lands together with regression tests that pin the new behavior. This applies to every issue type (bug, feature, refactor) and every layer, not only bug fixes in `core/`:

- **Unit tests wherever possible**, added to the existing test file per the convention below.
- **A provider-harness case for every wire-visible change** (next sections).
- **MCP harness cases for every MCP behaviour change** ("Every MCP change ships with MCP harness cases" below). MCP cases go in the MCP harness, not the provider harness.
- **Semantic cache wiring cases for every semantic cache behaviour change** ("Every semantic cache change ships with semantic cache wiring cases" below). Semantic cache cases go in the semantic cache wiring suite, not the provider harness.
- **Routing harness cases for every routing behaviour change** ("Every routing change ships with routing harness cases" below). Routing cases go in the routing harness, not the provider harness.
- **E2E coverage for `ui/` changes** (`tests/e2e/`) - the harness cannot see the UI.

The only exemptions: no wire-visible effect AND no testable behavior change (comments, internal renames, log lines). An exempt change must say so explicitly in the PR.

### Bug fixes: red before green

Before writing a fix, add (or extend) a test that reproduces the bug and confirm it fails for the expected reason — a wrong assertion, not a compile error or an unrelated panic. Only then implement the fix, and confirm the same test now passes. For bugs reachable through `make run-provider-harness-test`, add the harness regression case (see `.claude/skills/harness-test-writer/SKILL.md`) alongside Go-level tests: Go tests give a fast, free red/green loop while coding; the harness case is the live end-to-end pin, expected red pre-fix and green post-fix, validated structurally (`augment-provider-harness.mjs` / `filter-collection.mjs`) without needing a live paid run during development. For MCP bugs, the end-to-end pin is an MCP harness case instead (`.claude/skills/mcp-harness/SKILL.md`), run red and green with `TESTS=<ID> make run-mcp-harness-test`. For semantic cache bugs, it is a semantic cache wiring row (`.claude/skills/semantic-cache-wiring/SKILL.md`): check it with the generator and the suite's library tests while coding, and hand the user its live run. For routing bugs, it is a routing harness scenario (`.claude/skills/routing-harness/SKILL.md`): check it with the generators and the collection script tests while coding, and hand the user its live run.

### Add tests to existing test files, never new ones

Do not create a new `_test.go` file for a package that already has one. Put the test in the existing file that covers the same code: `<name>_test.go` next to `<name>.go`, or the topical file that already exists (`chat_test.go`, `counttokens_test.go`). One test file per bug or feature scatters a package's tests across many small files and makes it hard to find what already covers a source file. Create a new test file only for a source file that has no test file yet, and name it after that source file.

### Every wire-visible change ships with a provider-harness case

Any change under `core/`, `framework/`, `transports/bifrost-http/`, or `plugins/` that a client can observe on the wire must land together with a case in `tests/e2e/api/collections/provider-harness.json` (see `.claude/skills/harness-test-writer/SKILL.md`). This covers new features and refactors, not only bug fixes - the rule in the previous section is the narrower instance of this one. MCP behaviour is the exception: its cases go in the MCP harness ("Every MCP change ships with MCP harness cases"); only a change to how a provider converts tool definitions or tool calls also needs a provider-harness case. Semantic cache behaviour is another exception: its cases go in the semantic cache wiring suite ("Every semantic cache change ships with semantic cache wiring cases"); only a change that also alters what a provider receives or returns on a request the cache does not answer needs a provider-harness case too. Routing behaviour is a third: its cases go in the routing harness ("Every routing change ships with routing harness cases"); only a change that also alters how a provider converts the request it was routed to or the response it returns needs a provider-harness case too.

These layers all sit on the request path, so any of them can change the bytes a client sees - and that end-to-end behaviour is what the harness exists to pin. A Go unit test proves the function does what you meant; only the harness proves the bytes a real client sends still come back correct through the whole stack. The gap between those two is where regressions live: a fail-soft that fires on one request shape and silently skips a sibling shape passes every unit test it has.

**Never skip any error status code in a harness test script.** Do not open a test with an early-return guard like `if ([401, 403, 429, 500, 502, 503, 504].indexOf(pm.response.code) !== -1) { return; }` — every unexpected status, including auth failures, rate limits, and 5xx, must fail the assertion loudly rather than silently passing the case. Assert the exact status (or bound) the case expects and include `pm.response.text()` in the failure message.

Write the case so it is **red before the change and green after**, and validate it structurally while developing — no live paid run needed:

```bash
node tests/e2e/api/runners/augment-provider-harness.mjs --source tests/e2e/api/collections/provider-harness.json --out tmp/harness-augmented.json
node tests/e2e/api/runners/filter-collection.mjs --source tmp/harness-augmented.json --out tmp/filtered.json --feature "<keyword>"
```

Insert into the collection surgically (a script that splices the new object in, never a whole-file reserialize) — the file is ~50k lines and a reformat buries the actual change.

The narrow exemptions: changes with no wire-visible effect (comments, internal renames, log lines) and behaviour no HTTP request can reach. If a change is exempt, say so explicitly in the PR rather than leaving the omission unexplained.

### Every MCP change ships with MCP harness cases

Any change to MCP behaviour a client can observe lands together with cases in the MCP harness, `tests/e2e/api/runners/build-mcp-harness.mjs` (see `.claude/skills/mcp-harness/SKILL.md`). MCP behaviour covers MCP clients and their upstream auth, connection types, `/mcp` and `/mcp/<slug>`, `/v1/mcp/tool/execute`, MCP tools in inference and code mode, tool filtering at every level, virtual keys' MCP grants and Virtual MCPs, per-user discovery and the sessions API, MCP logs and pricing, the MCP library, Bifrost's OAuth server and the Bifrost MCP on `/v1/mcp`. These cases never go in `provider-harness.json`.

The skill lists, for each kind of MCP change, every case it needs: a new upstream auth type is checked on every connection type that can carry it, on each inbound auth mode, through expiry and refresh, per caller and shared, and on every surface that calls tools; a filtering change is checked at each level and on every surface that lists or calls tools. Cover every value the change reaches, not only the one named in the issue, and state in the PR any the change cannot reach.

The MCP harness is hermetic: it boots its own Bifrost and an mcpchaos fleet and calls nothing outside the machine. Run it yourself, scoped to your cases, and report the IDs and results (new or changed cases red before the change and green after, the existing tests the change reaches green before and after):

```bash
TESTS=<ID>,<ID> make run-mcp-harness-test
```

The same harness rules as above apply: never skip an error status in a test script, assert exact statuses and messages, and never assert a 500 as the expected answer; a known bug is asserted with `A.skip(...)` until its fix lands.

### Every semantic cache change ships with semantic cache wiring cases

Any change to semantic cache behaviour a client can observe lands together with cases in the semantic cache wiring suite, `tests/e2e/api/runners/build-semantic-cache-wiring.mjs` (see `.claude/skills/semantic-cache-wiring/SKILL.md`). Semantic cache behaviour covers the `semantic_cache` plugin and its config, what a request writes, hits or misses and what a hit replays, direct and semantic mode on every cacheable request type and route, streams, the `x-bf-cache-*` request headers, `extra_fields.cache_debug` and the `x-bifrost-cache-*` response headers, identity scoping, the `/api/cache` clear endpoints, the vector stores the plugin runs on, and the cache fields on log rows, logs filters and stats. These cases never go in `provider-harness.json`.

The skill lists, for each kind of semantic cache change, every case it needs: a new input to the cache key is checked with a hit and a miss in direct and semantic mode, streamed and not; a new request type with a write, a hit, a replay equal to the original, the response headers and the log row; a config field with its default, its exact 400s and its effect. Cover every value the change reaches, not only the one named in the issue, and state in the PR any the change cannot reach.

The suite is generated: edit the generator, regenerate the committed collection with `node tests/e2e/api/runners/build-semantic-cache-wiring.mjs`, and commit both. Its run is live and paid (it boots a clean Bifrost on Redis and on Weaviate and calls real providers), so, like the provider harness, it is the user's to trigger. Run the offline checks yourself: unit tests written on the plugin's fake stores, the transport tests, and `node tests/e2e/api/runners/lib/semantic-cache-wiring.test.mjs`. Most of the plugin package's existing tests call OpenAI through `env.OPENAI_API_KEY` and a running Weaviate, and skip themselves when either is missing, so `go test ./...` there prints `ok` without proving anything; report every `--- SKIP` as skipped, never as passed. Then end the report with the whole plugin package and the suite's run scoped to the folders the change reaches (`--folder`, repeatable), every placeholder filled in:

```bash
docker compose -f tests/docker-compose.yml up -d
(cd plugins/semanticcache && set -o pipefail && go test -count=1 -v ./... 2>&1 | grep -E -- '--- (FAIL|SKIP)|^(ok|FAIL)')
# build from the local modules: a plain `make build` sets GOWORK=off and uses the published plugin
[ -f go.work ] || make setup-workspace
make build LOCAL=1
tests/e2e/api/runners/individual/run-newman-semantic-cache-wiring-tests.sh --binary tmp/bifrost-http --port <port> --store both --folder "<folder>"
```

The same harness rules as above apply: never skip an error status in a test script, assert exact statuses and messages, and never assert a 500 as the expected answer. The suite has no skip, so a known bug's row lands with its fix.

### Every routing change ships with routing harness cases

Any change to how a request is routed that a client can observe lands together with cases in the routing harness: `tests/e2e/api/runners/build-routing-wiring.mjs` for the routing path and `tests/e2e/api/runners/build-model-catalog-wiring.mjs` for the model catalog (see `.claude/skills/routing-harness/SKILL.md`). Routing covers routing rules, model routers and the complexity router, the model catalog and key aliases, a model named without a provider, virtual key provider configs and governance load balancing, key selection and key pins, direct keys, session affinity, fallbacks and retries, `extra_fields.routing_info`, the `x-bifrost-routing-info-*` headers and the routing log trail. These cases never go in `provider-harness.json`. Its existing routing folders stay where they are, and a change that alters what one of them pins updates it there too.

The skill lists, for each kind of routing change, every case it needs: a rule change is checked on each scope, CEL input and target shape it reaches, with its fallbacks and chains; a load balancing change on weighted, zero and allow-list-only configs and on a candidate that cannot serve; a fallback change on every fallback source. Cover every value the change reaches, not only the one named in the issue, and state in the PR any the change cannot reach. When the generator has no step for a case (streams, sessions, direct keys, rule scopes, model routers, a scripted upstream), extend the generator in the same PR; never put the case anywhere else.

The suites are generated: edit the generator, regenerate the committed collection, and commit both. Their runs are live and paid (each runner boots a clean Bifrost and calls real providers), so, like the provider harness, they are the user's to trigger. Run the Go unit tests, both generators and `node tests/e2e/api/collections/collection-scripts.test.mjs` yourself, then end the report with the runs the change reaches, every placeholder filled in:

```bash
# build from the local modules: a plain `make build` sets GOWORK=off and uses the published core, framework and plugins
[ -f go.work ] || make setup-workspace
make build LOCAL=1
tests/e2e/api/runners/individual/run-newman-routing-wiring-tests.sh --binary tmp/bifrost-http --port <port>
tests/e2e/api/runners/individual/run-newman-model-catalog-wiring-tests.sh --binary tmp/bifrost-http --port <port>
```

The same harness rules as above apply: never skip an error status in a test script, assert exact statuses and messages, and never assert a 500 as the expected answer. The generators have no skip, so a known bug's case lands with its fix.

### Provider-native features are tested on every route that reaches the provider

A provider is reachable through more than its own SDK endpoint, and a fix that only looks at the route named in the issue is incomplete. Before calling a provider-native feature (a content block type, a beta header, a request parameter) fixed or added, enumerate every inbound route that can carry it to that provider and cover each one:

- **The provider's native surface**: `/anthropic/v1/messages`, `/genai/...`, `/bedrock/...` and the matching SDK integration under `transports/bifrost-http/integrations/`.
- **The neutral OpenAI-shaped surfaces**: `/v1/responses` and `/v1/chat/completions`, routed by the `provider/model` prefix. The payload arrives in neutral shape (for example a Responses `function` tool, not an Anthropic `input_schema` tool), and the client cannot set provider headers such as `anthropic-beta`, so any header the feature needs must be derived from the body by the gateway.
- **The Go SDK**, which enters at the same neutral types as `/v1/responses`.
- **Other hosts of the same model family** that reuse the provider's converters: Bedrock, Bedrock Mantle, Vertex, Azure, OpenRouter. Find them with `grep -rl "providers/<provider>\"" core/providers transports/bifrost-http` and check the per-host gates (`ProviderFeatures`, placement gates like `DefaultSupportsMidConversationSystem`) rather than assuming the converter change reaches them.

For each route the change reaches: a unit test that drives that route's ingress type into the provider egress, and a provider-harness case in the same folder with the route in the case name, using the shape and headers a real client on that route would send (so a `/v1/responses` case must not carry `anthropic-beta`). Routes the change does not reach are not silently skipped: the PR states each one and why (no equivalent construct on that wire, gated off by the feature table, dropped by a placement fallback), so the gap is a recorded decision and not an omission. If the fix leaves a host with the beta header on but the body construct dropped, say so explicitly; that mismatch is itself a bug to track.

### Every non-exempt wire-visible fix ends with unit tests, then a harness command handed to the user

Unit tests and `make test-core` are the finish line for the agent. Run the Go-level red/green loop and the regression reruns, and report what passed and what failed.

The live provider harness is the user's to run, not the agent's. Do not launch it. Instead, end the report with both final commands in a plain code block, ready to paste: the `make dev` line that starts Bifrost from the code under test, and the single-line `make run-provider-harness-test` line that runs against it. No box drawing around them, since border characters make the commands impossible to select. Naming the scope is the agent's job; spending the money is the user's call. A semantic cache change hands over the semantic cache wiring run instead ("Every semantic cache change ships with semantic cache wiring cases"), and both when it also changes what a provider receives or returns on a request the cache does not answer. A routing change hands over the routing harness runs instead ("Every routing change ships with routing harness cases"), and the provider harness as well when it also changes what a provider receives or returns, or touches an existing routing folder there.

**RUN THE PROVIDER HARNESS.** Unit tests are green. The live run is yours to trigger.

```bash
# 1. backing services: start Weaviate (idempotent)
docker compose -f tests/docker-compose.yml up -d weaviate

# 2. wait until Weaviate HTTP is ready (-f fails on 503) AND gRPC answers (the client does not check it at startup)
until curl -sf http://localhost:9000/v1/.well-known/ready >/dev/null && nc -z localhost 50051; do sleep 2; done

# 3. pick a free port (worktrees run side by side, so never assume 8080)
lsof -nP -iTCP:<port> -sTCP:LISTEN

# 4. start Bifrost from the code under test on that port, then wait for /health
make dev PORT=<port> APP_DIR=$(pwd)/tests/integrations/python

# 5. run the harness against that server
make run-provider-harness-test PROVIDER=<provider> FEATURE="<keyword>" BASE_URL=http://localhost:<port>
```

Do not pass `APP_DIR` or `CI=1` to `run-provider-harness-test`. `APP_DIR` already defaults to `tests/integrations/python` (Makefile:2255), the same profile `make dev` is pointed at, and `CI=1` suppresses the interactive HTML viewer that makes a live run readable. `make dev` is the one that needs `APP_DIR` spelled out, because it is what decides which code and config the server runs. Always pass `PORT` to `make dev` and the matching `BASE_URL` to the harness: several worktrees may be running at once, and the harness viewer itself listens on 8090, so pick a port that is neither 8080 nor 8090 unless `lsof` shows both free.

Never print that block with a placeholder still in it. `<provider>`, `<keyword>`, and `<port>` belong to the template; substitute the real values for the change so every line pastes straight into a shell. The keyword is descriptive text from the case or folder names (for example `"sdk fidelity"`), never an issue or PR number.

The exemptions are the ones in the previous section: a change with no wire-visible effect (comments, internal renames, log lines, test-only or guidance-only edits) or behaviour no HTTP request can reach is exempt. For an exempt change, say so explicitly instead of printing the block.

```bash
# Scoped to the change (preferred): the provider and a keyword from the affected cases
make run-provider-harness-test PROVIDER=<provider> FEATURE="<keyword>"

# Curated ~100-request smoke set across all providers, when the change is cross-cutting
make run-provider-harness-test SMOKE=1
```

The profile is the shared provider config at `tests/integrations/python/config.json` that every live check uses. Pass it to `make dev` as `APP_DIR=$(pwd)/tests/integrations/python` so a stale server or another config never answers for the code under test; the harness target already defaults to it and does not need it repeated.

`HARNESS_MAX_REQUESTS=<n>` is an optional enforced spend bound: the recipe checks every newman launch against its exact filtered request count before it starts and refuses any launch that would cross the cap (exit 3), so the live total never exceeds the approved number. Add it when a run is broad enough that the cost is worth capping; a `PROVIDER=` + `FEATURE=` scoped run is usually small enough not to need it. The preflight count from `filter-collection.mjs` is only an estimate because shared producers repeat per provider fork. Stream-cancellation probes are never sent under a cap.

The gateway port is a blocking precondition worth restating in the block: the recipe reuses any server whose `/health` answers at `BASE_URL` and never starts the `APP_DIR` one, so a stale listener silently tests old code. `lsof -nP -iTCP:<port> -sTCP:LISTEN` must come back empty, or show only a Bifrost started from the code under test. Starting it first with `make dev PORT=<port> APP_DIR=$(pwd)/tests/integrations/python` and waiting for `/health` is the reliable pattern, since a cold start can outlast the recipe's 60s health wait. Weaviate must be up before that `make dev`: the profile enables a `weaviate` vector store on `localhost:9000`, so bring it up with `docker compose -f tests/docker-compose.yml up -d weaviate` first or the server fails to bootstrap. `up -d` returns before Weaviate is ready, so retry until `curl -sf http://localhost:9000/v1/.well-known/ready` passes (`-f` makes a 503 fail) and `nc -z localhost 50051` succeeds: the profile sets `grpc_config` to `localhost:50051`, and the client does not check gRPC reachability at startup, so a missing gRPC port only fails on the first gRPC-backed call.

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

These prove the logic; the end-to-end MCP cases live in the MCP harness ("Every MCP change ships with MCP harness cases").

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

Skills are available via `/skill-name`:

### `/docs-writer <feature-name>`
Write, update, or review Mintlify MDX documentation. Researches UI code, Go handlers, and config schema. Validates `config.json` examples against `transports/config.schema.json`. Outputs docs with Web UI / API / config.json tabs.

Variants: `/docs-writer update <doc-path>`, `/docs-writer review <doc-path>`

### `/e2e-test <feature-name>`
Create, run, debug, audit, or auto-update Playwright E2E tests.

Variants:
- `/e2e-test fix <spec>` — Debug and fix a failing test
- `/e2e-test sync` — Detect UI changes, update affected tests automatically
- `/e2e-test audit` — Scan specs for incorrect/weak assertions (P0-P6 severity scale)

### `/harness-test-writer <PR# | issue# | URL>`
Add regression test cases to the provider harness (`tests/e2e/api/collections/provider-harness.json`) from a merged PR or GitHub issue. Traces the affected wire path, checks existing coverage, designs cases per harness conventions, inserts them surgically without reformatting the file, and validates via the augment and filter scripts. This is the skill the Testing section's harness-case rule points at.

### `mcp-harness`
Which MCP harness cases each kind of MCP change needs (upstream auth, connection types, inbound auth on `/mcp`, filtering and Virtual MCPs, tool execution, MCP tools in inference, per-user discovery, logs, the library, the Bifrost MCP), where they go in `tests/e2e/api/runners/build-mcp-harness.mjs`, how to write them and how to run them. This is the skill the Testing section's MCP harness rule points at.

### `semantic-cache-wiring`
Which semantic cache wiring cases each kind of semantic cache change needs (config fields, cache key inputs, request types and routes, streams, per-request headers, semantic matching, identity scoping, the plugin and clear APIs, vector stores, `cache_debug`, response headers and logs), where they go in `tests/e2e/api/runners/build-semantic-cache-wiring.mjs`, how to write them and how to run them. This is the skill the Testing section's semantic cache rule points at.

### `routing-harness`
Which routing harness cases each kind of routing change needs (routing rules, model routers and the complexity router, the model catalog and key aliases, virtual key routing and governance load balancing, key selection, key pins and direct keys, session affinity, fallbacks and retries, entry points, `routing_info` and the routing log), where they go in `tests/e2e/api/runners/build-routing-wiring.mjs` and `build-model-catalog-wiring.mjs`, how to write them and how to run them. This is the skill the Testing section's routing rule points at.

### `/investigate-issue <issue-id>`
Investigate a GitHub issue from `maximhq/bifrost`. Fetches issue details, classifies by type/area, searches codebase, traces dependencies, analyzes side effects, plans the required regression tests (unit/harness/LLM/MCP/E2E), and presents an implementation plan with per-change approval gates.

### `/resolve-pr-comments <pr-number>`
Systematically address unresolved PR review comments. Uses GraphQL to get unresolved threads, presents each with FIX/REPLY/SKIP options, collects fixes locally, and only posts replies **after code is pushed** to remote.

---

## Common Workflows

### Modify chat completions across all providers
1. Change types in `core/schemas/chatcompletions.go`
2. Update converter functions in each provider's `chat.go`
3. If streaming affected, update `framework/streaming/` (accumulator, delta copy)
4. Run `make test-core` (all providers)
5. Add/extend unit tests and a provider-harness regression case (see Testing section)

### Add a new field to API responses
1. Add to schema type in `core/schemas/`
2. Map in provider response converter (`ToBifrost*Response`)
3. Handle in streaming accumulator if applicable
4. Update HTTP handler if field needs special serialization
5. Update `transports/config.schema.json` if configurable
6. Add/extend unit tests and a provider-harness regression case (see Testing section)

### Add a new plugin
1. Create `plugins/<name>/` with its own `go.mod`
2. Implement `LLMPlugin`, `MCPPlugin`, or `HTTPTransportPlugin` interface
3. Add to `go.work`
4. Register in transport layer or Bifrost config
5. Add test targets to `Makefile`
6. Add unit tests, plus the end-to-end cases its wire-visible behaviour needs: MCP harness cases for MCP behaviour (an `MCPPlugin`, tool filtering, MCP logs), otherwise a provider-harness regression case (see the Testing section)

### Modify a UI feature
1. Find workspace page: `ui/app/workspace/<feature>/`
2. Check existing `data-testid` attributes — E2E tests depend on them
3. Add `data-testid` to new interactive elements
4. Put every user-visible string through i18n (see [UI i18n](#ui-i18n)) and run `cd ui && npm run i18n:check`
5. Run `make run-e2e FLOW=<feature>` to verify
6. If E2E tests break, use `/e2e-test sync` to update them

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
- **Struct comments**: Keep struct field comments to a short trailing one-liner (or none). Long multi-line explanations do not belong inside struct definitions; put the rationale in the doc comment of the function that uses the field, or in the package doc.
- **Comments describe the code, not the issue that prompted it**: a comment or godoc says what the code does and why, in terms that hold for every input it handles. Never quote the bug report, PR or review thread, the customer, client, test prompt or values that exposed the problem, and never write "fixes the X issue" or "previously this did Y". That history belongs in the commit message and PR. Test names and test comments name the behaviour they check, not the incident that reproduced it.
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

### Entity Selectors — never hand-roll an entity picker

Any UI that lets a user pick an existing entity (virtual key, team, customer, user, business unit, …) **must** go through `ui/components/entitySelectors/`. Do not build a new `Select`/`Combobox` + `useState` + debounce + fetch stack for this — that pattern was already duplicated across surfaces and consolidated here.

**Use an existing selector** — import it and pass one of the three modes:

```tsx
import { VirtualKeySelector } from "@/components/entitySelectors/virtualKeySelector";

<VirtualKeySelector value={id} onChange={setId} fallbackOption={{ value: row.id, label: row.name }} />   // single
<VirtualKeySelector multiple value={ids} onChange={setIds} />                                            // multi (chips inside the control)
<VirtualKeySelector mode="add" onSelect={(o) => appendRow(o)} />                                         // fire-and-forget add
```

Available today: `virtualKeySelector`, `teamSelector`, `customerSelector` (OSS); `userSelector`, `businessUnitSelector` (enterprise — reached via registry, see below).

**Always pass `fallbackOption` / `fallbackOptions`** when editing an existing row. Selectors fetch nothing until the popover opens, so a preselected id renders as a raw UUID otherwise.

**Adding a selector for a new entity** — write a thin wrapper, never a new picker. Copy `customerSelector.tsx` (the simplest one) and change only what genuinely differs: the list query, the by-id label resolver, and the label/description fields. The wrapper must:

1. Call `useEntitySelectorSearch()` for open/search/debounce state, and pass `skip` to the RTK Query hook — nothing is fetched until the picker opens.
2. `useMemo` the `options` array. Multi mode feeds it to react-select as `defaultOptions`, which re-syncs on identity change and will loop if the identity churns.
3. Ship a `LabelResolver` component (`EntityLabelResolverProps`) that fetches one entity by id and calls `onResolved` — this is what keeps selected-but-unfetched ids from rendering as UUIDs.
4. Type its props as `OwnProps & EntitySelectorModeProps` and extend `EntitySelectorCommonProps`, so all three modes and the shared prop surface come for free.
5. Default `limit` to `ENTITY_SELECTOR_PAGE_SIZE`; expose a `filters` prop only if the endpoint supports server-side scoping.
6. Search is **server-side** — never fetch a page and filter it client-side.

Do not edit `entitySelector.tsx` to accommodate one surface. It only carries behaviour identical across every entity; per-entity differences belong in the wrapper, per-surface differences in props (`trigger`, `triggerClassName`, `excludeIds`, `noPortal`, `className`).

**OSS ↔ enterprise placement.** `entitySelector.tsx` and any selector whose API is OSS live in `ui/components/entitySelectors/`. A selector for an enterprise-only API lives in `bifrost-enterprise/enterprise-ui/app/components/entitySelectors/` and OSS must never import it directly — OSS reaches it through a runtime registry (`ui/lib/registries/userPicker.tsx`, `ui/lib/registries/modelLimitScopes.tsx`), with an empty fallback under `ui/app/_fallbacks/enterprise/` so OSS-only builds simply hide the option. Keep single mode prop-compatible with the registry contract (`{ value, onChange, disabled, fallbackOption }`) so the selector can be registered as-is.

---

### Provider and model pickers — always `ProviderSelector` / `ModelSelector`

Every provider or model picker goes through `ui/components/ui/providerSelector.tsx` or `modelSelector.tsx`. Never hand-roll a `Select` over `VisibleProviderNames`, a `Combobox` over `useGetProvidersQuery`, or a search box over `useGetModelsQuery`: these already carry provider icons and labels, server-side model search with paging, deprecated demotion, a pinned "Selected" row for a value no longer in the list, and multi-mode chips.

```tsx
<ProviderSelector value={p} onChange={setP} />                              // single
<ProviderSelector multiple value={ps} onChange={setPs} />                   // multi
<ProviderSelector mode="add" onSelect={add} trigger={<Button>Add</Button>} /> // fire-and-forget
<ModelSelector provider={p} value={m} onChange={setM} allowCustomModel />
```

- `source` on `ProviderSelector`: `"configured"` (default, what the user set up), `"catalog"` (everything Bifrost supports, for add flows), `"values"` (a list from elsewhere, e.g. analytics labels that may name a deleted provider).
- Scope models with `provider` / `keys` / `vks`; `baseModelsWithoutProvider` collapses duplicates when no provider is picked, `allowCustomModel` accepts a name off-catalog, `unfiltered` bypasses the provider's model pool.
- Rows outside the source list go in `extraOptions` (above), `footerOptions` (below), or `allOption` for an "All Providers" sentinel. `ALL_MODELS_OPTION` is exported for the `*` row. Never merge them into the fetched array yourself.
- They own fetch, search, paging and reset. No parent `useState` mirror, debounce, or refetch-on-open.
- Per-surface differences are props, not forks: `size="sm"`, `contentWidth`, `noPortal` (inside a sheet), `className`, `inputId` / `ariaDescribedBy` / `ariaInvalid`, `data-testid`, `optionTestId`, `contentTestId`. For selectability use `getOptionState` (model) or `disabled` + `disabledReason` on an option (provider). Anything new is a prop defaulting to today's behaviour.
- Pure helpers live in `providerSelector.utils.ts` with a case in `providerSelector.test.ts`, since the components pull in the store.

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

## UI i18n

Every user-visible string in `ui/` goes through i18next. That covers JSX text, text props (`label`, `placeholder`, `title`, `description`, `tooltip`, `aria-label`, `alt`), toasts, zod validation messages, and `label`/`title`/`description` fields in option, tab and column arrays.

- In components: `const { t } = useTranslation("<ns>")`. Outside React: `i18n.t("key", { ns: "<ns>" })` from `@/lib/i18n`. Never write `"ns:key"`; `nsSeparator` is off.
- Keys are typed against `ui/locales/en`, so an unknown key fails `npm run typecheck`. Do not cast `t` to get around it.
- Namespaces: `common` (shared verbs, entity names, `validation.*`, `apiErrors.*`), `shell` (nav and layout), `login`, `observability` (logs, metrics, traces), `models` (providers, routing, pricing), `mcp`, `governance` (virtual keys, budgets, teams), `config` (settings). Reuse an existing `common` key before adding a new one.
- Adding a key: add it to `locales/en/<ns>.json` and to every other locale directory (de, es, fr, it, ja, ko, pt, ru, zh-CN, zh-TW) with a real translation, not the English text. Keep the same `{{placeholders}}`, the same `<Trans>` tags (`<b0>`, `<code1>`) and the same array lengths. Edit catalogs surgically; never re-serialize a whole file by hand.
- One whole sentence per key. Interpolate values (`t("deleted", { name })`); never concatenate translated fragments or build sentences with `+`.
- Plurals: `key` plus `key_other` in English, and pass `count`. Russian also needs `_few` and `_many`.
- Inline markup goes through `<Trans>` with indexed components rather than splitting a sentence into several keys.
- Static config arrays store a `labelKey` and translate at render time. Template keys (`` t(`x.${v}`) ``) are fine only when `v` is a typed union.
- Zod messages must stay lazy so they follow a language switch: `.min(1, msg("validation.nameIsRequired"))` with `msg` from `@/lib/i18n/validationMessage`, and `message: i18n.t("validation.…")` inside `superRefine`/`addIssue`. A string literal is resolved once, in English.
- Dates and numbers follow the UI language: pass `getLocale()` to `toLocaleString`, `toLocaleDateString`, `toLocaleTimeString` and `Intl.*`, and `dateFnsLocale(getLocale())` to date-fns calls that print words or AM/PM. Never hardcode `"en-US"`.
- Do not translate provider or model names, API field names, code samples, log payloads, or the dev-only pages (`app/pprof`, `components/devProfiler.tsx`). Mark an intentional literal with `// i18n-ignore` (or `{/* i18n-ignore */}`) on the same line or the line above, with a short reason.
- New namespace: create the JSON file in every locale directory, then import the English file and add the name to `NAMESPACES` in `ui/lib/i18n/resources.ts`. Other languages load lazily through `import.meta.glob`, so they need no registration.
- New language: add the folder under `ui/locales/` (and `enterprise-ui/app/locales/` in the enterprise repo), an entry in `SUPPORTED_LOCALES` in `ui/lib/i18n/index.ts`, and a date-fns locale in `ui/lib/i18n/dateLocale.ts`.

Before finishing UI work, run from `ui/`:

```bash
npm run i18n:check                 # hardcoded strings, validation messages, catalog parity; CI runs this
npm run i18n:check -- app/workspace/logs   # limit the literal scan to the paths you touched
npm run i18n:check -- --unused     # optional: English keys nothing references
```

It must exit 0. The `ui-i18n` workflow fails a pull request otherwise.

---

## TypeScript Guidelines

- Avoid using `any` unless absolutely unavoidable
- Prefer strict typing and inference
- Define reusable types in shared locations

---

## Code Quality & Formatting

After writing code, format only the files you changed:

```bash
cd ui && npm run format:changed          # oxfmt on files changed against HEAD, plus untracked ones
cd ui && npm run format:changed:check    # same set, check only
```

Do not run `npm run format` or `npm run format:check` across the tree. The committed files were not all formatted with the same oxfmt version, so a repo-wide run rewrites unrelated files that then have to be reverted. If a formatter run touched files outside your change, restore them with `git checkout -- <file>` before committing.

Then verify the build and the i18n check:

```bash
cd ui && npm run build
cd ui && npm run i18n:check
```

* Code must pass formatting, build and i18n checks
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
