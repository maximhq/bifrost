# Technical Design Document — Core Engine
## Bifrost Core (`core/`)

**Version:** 1.0  
**Date:** 2026-04-05  
**Status:** Draft  
**References:** [TDD-project.md](TDD-project.md)

---

## Table of Contents

1. [Overview](#1-overview)
2. [Package Structure](#2-package-structure)
3. [Core Types & Schemas](#3-core-types--schemas)
4. [Bifrost Struct & Initialization](#4-bifrost-struct--initialization)
5. [Provider System](#5-provider-system)
6. [Request Routing & Key Selection](#6-request-routing--key-selection)
7. [Channel-Based Concurrency Model](#7-channel-based-concurrency-model)
8. [Plugin Pipeline](#8-plugin-pipeline)
9. [MCP Integration](#9-mcp-integration)
10. [Network Layer](#10-network-layer)
11. [Object Pooling](#11-object-pooling)
12. [Context System](#12-context-system)
13. [Supported Request Types](#13-supported-request-types)
14. [Streaming Architecture](#14-streaming-architecture)
15. [Failover & Fallback Logic](#15-failover--fallback-logic)

---

## 1. Overview

The `core/` package is the **standalone inference engine** of Bifrost. It has no dependency on `framework/` or `transports/` — it can be embedded directly into any Go application as an SDK.

Core responsibilities:
- Manage 20+ LLM provider client implementations
- Route requests to the correct provider and API key via weighted selection
- Execute the plugin pipeline (Pre/Post hooks) around every request
- Provide channel-based back-pressure per provider queue
- Handle failover through provider/model fallback chains
- Coordinate MCP tool calls for agentic workflows
- Manage all object pools for zero-allocation hot paths

---

## 2. Package Structure

```
core/
├── bifrost.go          # Bifrost struct, Init(), routing engine, plugin pipeline
├── logger.go           # Default logger implementation
├── utils.go            # Utility functions (WeightedRandomKeySelector, etc.)
├── schemas/            # All shared type definitions
│   ├── bifrost.go      # BifrostConfig, ModelProvider constants, RequestType, ContextKeys
│   ├── plugin.go       # Plugin interfaces (BasePlugin, LLMPlugin, MCPPlugin, etc.)
│   ├── account.go      # Account interface
│   ├── provider.go     # Provider interface
│   ├── mcp.go          # MCP types
│   ├── trace.go        # Trace/Span types for observability
│   └── ...             # Other shared types (request, response, key, etc.)
├── network/
│   ├── http.go         # HTTPClientFactory — fasthttp + net/http with proxy support
│   └── multipart.go    # Multipart body construction for file/audio/image endpoints
├── providers/          # Per-provider implementations
│   ├── openai/
│   ├── anthropic/
│   ├── azure/
│   ├── bedrock/
│   ├── vertex/
│   ├── gemini/
│   ├── groq/
│   ├── mistral/
│   ├── cohere/
│   ├── cerebras/
│   ├── ollama/
│   ├── huggingface/
│   ├── openrouter/
│   ├── perplexity/
│   ├── elevenlabs/
│   ├── nebius/
│   ├── xai/
│   ├── parasail/
│   ├── replicate/
│   ├── sgl/
│   ├── vllm/
│   ├── runway/
│   └── utils/          # Shared provider utilities
└── mcp/                # MCP client manager
    ├── manager.go      # MCPManagerInterface implementation
    └── codemode/       # Starlark-based code execution for MCP tools
```

---

## 3. Core Types & Schemas

### 3.1 `ModelProvider` (string enum)

All 22 supported providers:

```go
OpenAI, Azure, Anthropic, Bedrock, Cohere, Vertex, Mistral, Ollama, Groq,
SGL, Parasail, Perplexity, Cerebras, Gemini, OpenRouter, Elevenlabs,
HuggingFace, Nebius, XAI, Replicate, VLLM, Runway
```

Custom providers can extend these via the `SupportedBaseProviders` list.

### 3.2 `RequestType` (string enum)

All supported operation types:

| Category | Types |
|----------|-------|
| Chat | `chat_completion`, `chat_completion_stream` |
| Text | `text_completion`, `text_completion_stream` |
| Responses | `responses`, `responses_stream` |
| Embedding | `embedding` |
| Speech | `speech`, `speech_stream` |
| Transcription | `transcription`, `transcription_stream` |
| Images | `image_generation`, `image_edit`, `image_variation` + stream variants |
| Video | `video_generation`, `video_retrieve`, `video_download`, `video_delete`, `video_list`, `video_remix` |
| Batch | `batch_create`, `batch_list`, `batch_retrieve`, `batch_cancel`, `batch_results`, `batch_delete` |
| Files | `file_upload`, `file_list`, `file_retrieve`, `file_delete`, `file_content` |
| Containers | `container_create/list/retrieve/delete` + file variants |
| Other | `rerank`, `count_tokens`, `list_models`, `mcp_tool_execution`, `passthrough`, `realtime`, `websocket_responses` |

### 3.3 `BifrostConfig`

```go
type BifrostConfig struct {
    Account            Account       // Required: provides provider configs
    LLMPlugins         []LLMPlugin   // Plugin pipeline (ordered)
    MCPPlugins         []MCPPlugin   // MCP-specific plugins
    OAuth2Provider     OAuth2Provider
    Logger             Logger        // nil = default info logger
    Tracer             Tracer        // nil = NoOpTracer
    InitialPoolSize    int           // Default: 5000 (sync.Pool pre-warming)
    DropExcessRequests bool          // If true, drop requests when queue is full
    MCPConfig          *MCPConfig    // nil = MCP disabled
    KeySelector        KeySelector   // nil = WeightedRandomKeySelector
    KVStore            KVStore       // nil = session stickiness disabled
}
```

### 3.4 `ChannelMessage`

The internal unit passed through provider queues:

```go
type ChannelMessage struct {
    schemas.BifrostRequest
    Context        *schemas.BifrostContext
    Response       chan *schemas.BifrostResponse
    ResponseStream chan chan *schemas.BifrostStreamChunk
    Err            chan schemas.BifrostError
}
```

### 3.5 Context Keys

`BifrostContext` carries typed string keys across the request lifecycle. Key categories:

| Key | Purpose |
|-----|---------|
| `bifrost-session-token` | Authentication session |
| `x-bf-vk` | Virtual key identifier |
| `request-id` | Unique request ID |
| `bifrost-selected-key-id/name` | Which provider key was selected |
| `bifrost-governance-*` | Governance plugin-set IDs (VK, team, customer, routing rule) |
| `bifrost-governance-routing-engine-used` | Whether routing engine was applied |
| `bifrost-large-payload-mode` | Enables streaming body pass-through |
| `bifrost-deferred-usage` | Channel for post-stream token count updates |
| `bifrost-mcp-agent-depth` | Current MCP recursion depth |

---

## 4. Bifrost Struct & Initialization

### 4.1 Struct Fields

```go
type Bifrost struct {
    ctx, cancel         // Lifecycle context
    account             Account                              // config source
    llmPlugins          atomic.Pointer[[]LLMPlugin]          // lock-free plugin list
    mcpPlugins          atomic.Pointer[[]MCPPlugin]
    providers           atomic.Pointer[[]Provider]           // lock-free provider list
    requestQueues       sync.Map                             // provider name → *ProviderQueue
    waitGroups          sync.Map                             // provider name → *sync.WaitGroup
    providerMutexes     sync.Map                             // per-provider update lock
    channelMessagePool  sync.Pool                            // ChannelMessage reuse
    responseChannelPool sync.Pool                            // response channel reuse
    errorChannelPool    sync.Pool
    responseStreamPool  sync.Pool
    pluginPipelinePool  sync.Pool
    bifrostRequestPool  sync.Pool
    mcpRequestPool      sync.Pool
    oauth2Provider      OAuth2Provider
    logger              Logger
    tracer              atomic.Value                         // wraps Tracer interface
    MCPManager          MCPManagerInterface
    mcpInitOnce         sync.Once
    dropExcessRequests  atomic.Bool
    keySelector         KeySelector
    kvStore             KVStore
}
```

### 4.2 `Init()` Sequence

```
1. Validate: Account must be non-nil
2. Set default Logger if nil (info level)
3. Set default Tracer if nil (NoOpTracer)
4. Create BifrostContext with cancellation
5. Store plugin slices via atomic.Pointer (lock-free reads)
6. Store initial empty providers slice
7. Set keySelector = WeightedRandomKeySelector if nil
8. Pre-warm sync.Pools to InitialPoolSize (default 5000 allocations)
9. Call AddProviders() for each provider in Account.GetConfig()
```

### 4.3 Provider Hot-Reload

Providers can be added, updated, or removed at runtime without restarting:

```go
bifrost.AddProvider(name, providerConfig)
bifrost.UpdateProvider(name, providerConfig)
bifrost.RemoveProvider(name)
```

Provider updates use `providerMutexes` (per-provider) to prevent concurrent modifications. `atomic.Pointer` on the providers slice enables lock-free reads by all request-processing goroutines.

---

## 5. Provider System

### 5.1 `Provider` Interface

Each provider implements:

```go
type Provider interface {
    GetName() ModelProvider
    GetSupportedRequests() []RequestType
    // Send executes one request type; called by Bifrost routing
    Send(ctx *BifrostContext, req BifrostRequest, key Key) (*BifrostResponse, error)
    // GetModels returns available models for this provider
    GetModels() ([]string, error)
}
```

### 5.2 Provider Implementations

All 22 providers follow the same structural pattern:

```
providers/<name>/
  ├── <name>.go          # Provider struct, Send() dispatch, type assertions
  ├── chat.go            # ChatCompletion implementation
  ├── embedding.go       # Embedding implementation (if supported)
  ├── speech.go          # TTS implementation (if supported)
  └── ...                # Other request types
```

Each provider:
1. Translates `BifrostRequest` → provider-specific JSON format
2. Makes HTTP call via `network.HTTPClientFactory`
3. Translates provider response → `BifrostResponse`
4. Maps provider errors → `BifrostError` with standard type/code

### 5.3 Provider-Specific Auth Patterns

| Provider | Auth |
|----------|------|
| Standard (OpenAI, Groq, etc.) | `Authorization: Bearer <api_key>` |
| Azure | `api-key` header + endpoint + deployment map |
| Bedrock | AWS SigV4 signing (IAM access key + secret, or assumed role ARN) |
| Vertex | OAuth2 access token (service account JSON → token exchange) |
| Ollama | No auth (local) |

### 5.4 Custom Providers

Beyond the 22 built-in providers, custom providers can be registered using one of the 7 `SupportedBaseProviders` (`openai`, `anthropic`, `gemini`, `cohere`, `bedrock`, `huggingface`, `replicate`) as the protocol base, with a custom base URL and credentials.

---

## 6. Request Routing & Key Selection

### 6.1 Model String Parsing

Incoming model strings follow `provider/model` format:

```
"openai/gpt-4o"         → provider=openai, model=gpt-4o
"bedrock/anthropic.claude-3-5-sonnet-20241022-v2:0" → provider=bedrock, model=...
"azure/<deployment-id>" → provider=azure, model=<deployment-id>
```

The router splits on the first `/` to extract provider name, then looks up the registered provider.

### 6.2 Key Selection

For a given `(provider, model)` pair, the engine selects a key from the provider's key list:

**Default: `WeightedRandomKeySelector`**
- Filters keys by model compatibility (empty `models` list = accepts all)
- Normalizes weights across eligible keys
- Draws a random index proportional to weights
- Returns a single `Key` struct for the request

**Custom `KeySelector`:** Can be provided via `BifrostConfig.KeySelector` for advanced scenarios (e.g., session stickiness via `KVStore`).

### 6.3 Fallback Chain

```
Primary request attempt
  → On error (5xx, timeout, rate limit, etc.):
      → Try next fallback target from req.Fallbacks[]
      → Log fallback attempt with fallback_index in context
      → If all fallbacks exhausted: return final error
```

Whether fallbacks are attempted for plugin-generated errors is controlled by `BifrostError.AllowFallbacks`:
- `nil` or `&true` → attempt fallbacks
- `&false` → return error immediately

---

## 7. Channel-Based Concurrency Model

### 7.1 `ProviderQueue`

Each provider has exactly one `ProviderQueue`:

```go
type ProviderQueue struct {
    queue   chan *ChannelMessage  // buffered channel (size = ConcurrencyAndBufferSize.BufferSize)
    done    chan struct{}         // closed on shutdown
    closing uint32               // atomic flag
    signalOnce, closeOnce sync.Once
}
```

### 7.2 Per-Provider Worker Pool

When a provider is added, Bifrost spawns `ConcurrencyAndBufferSize.Concurrency` goroutines:

```
for i := 0; i < concurrency; i++ {
    go providerWorker(provider, queue)
}
```

Each worker:
1. Reads `ChannelMessage` from `queue` channel
2. Executes `Send(ctx, request, key)`
3. Sends result to `msg.Response` or `msg.Err` channel
4. Returns to waiting for next message

### 7.3 Back-Pressure

If `queue` channel is full:
- `DropExcessRequests = false` (default): caller blocks until a slot is available
- `DropExcessRequests = true`: caller receives immediate `BifrostError` with "queue full" message

### 7.4 Shutdown Safety

`ProviderQueue.signalClosing()` uses `sync.Once` + `atomic.StoreUint32` to safely signal shutdown to all producer goroutines without panic. `closeQueue()` uses `sync.Once` to prevent double-close of the channel.

The `WaitGroup` per provider ensures all in-flight requests complete before the queue is closed during `RemoveProvider`.

---

## 8. Plugin Pipeline

### 8.1 `PluginPipeline` Struct

```go
type PluginPipeline struct {
    llmPlugins       []LLMPlugin
    mcpPlugins       []MCPPlugin
    logger           Logger
    tracer           Tracer
    executedPreHooks int           // how many PreHooks ran (for symmetry)
    preHookErrors    []error
    postHookErrors   []error
    postHookTimings  map[string]*pluginTimingAccumulator  // streaming metrics
    postHookPluginOrder []string
    chunkCount       int
}
```

`PluginPipeline` objects are pooled via `pluginPipelinePool` to reduce allocations.

### 8.2 Execution Flow

```
ExecutePreHooks(ctx, req):
  for i, plugin in llmPlugins:
    (newReq, shortCircuit, err) = plugin.PreLLMHook(ctx, req)
    executedPreHooks++
    if shortCircuit != nil: break (skip provider call)
    if err != nil: record, continue

provider.Send() [skipped if shortCircuit]

ExecutePostHooks(ctx, resp, err):
  for i from executedPreHooks-1 downto 0:
    (newResp, newErr, pluginErr) = llmPlugins[i].PostLLMHook(ctx, resp, bifrostErr)
    record pluginErr as warning
```

### 8.3 Timing Accumulation (Streaming)

For streaming responses, `postHookTimings` accumulates per-plugin execution time across all chunks. At stream end, aggregated timings are added as child spans to the tracer for observability.

### 8.4 Plugin Placement

`PluginConfig.Placement` controls ordering relative to built-in plugins:
- `pre_builtin` — runs before governance/logging/cache
- `builtin` — reserved for first-party plugins
- `post_builtin` (default) — runs after first-party plugins

---

## 9. MCP Integration

### 9.1 `MCPManagerInterface`

```go
type MCPManagerInterface interface {
    GetTools(ctx *BifrostContext) ([]ChatToolFunction, error)
    ExecuteTool(ctx *BifrostContext, toolName string, args map[string]any) (*MCPToolResult, error)
    AddClient(config MCPClientConfig) error
    RemoveClient(id string) error
    Reconnect(id string) error
}
```

### 9.2 Initialization

`MCPManager` is initialized lazily on first use via `mcpInitOnce` to avoid startup overhead when MCP is not needed.

### 9.3 Agentic Tool Call Loop

When a chat completion response contains `tool_calls`, Bifrost executes the agentic loop:

```
1. Receive response with tool_calls
2. For each tool_call:
   a. Look up tool in MCPManager
   b. Execute tool via MCPManager.ExecuteTool()
   c. Append tool_result message to conversation
3. Re-send chat completion with updated messages
4. Repeat until no tool_calls in response OR depth limit reached
```

Depth is controlled by `BifrostContextKeyMCPAgentDepth` (default: 10 hops).

### 9.4 Starlark Code Mode

`mcp/codemode/starlark/` provides a Starlark-based sandboxed code execution environment for MCP tools that execute code (rather than calling external APIs).

---

## 10. Network Layer

### 10.1 `HTTPClientFactory`

Manages all outbound HTTP connections with optional global proxy support:

```go
type HTTPClientFactory struct {
    mu          sync.RWMutex
    proxyConfig *GlobalProxyConfig
    // Cached fasthttp.Client and net/http.Client per purpose
}
```

**Client purposes:**
- `ClientPurposeInference` — LLM API calls (fasthttp, high-throughput)
- `ClientPurposeAPI` — General API (guardrails, etc.) (fasthttp)
- `ClientPurposeSCIM` — Identity provider calls (net/http, required for OAuth flows)

### 10.2 Default Timeouts

| Setting | Value |
|---------|-------|
| ReadTimeout | 60s |
| WriteTimeout | 60s |
| MaxIdleConnDuration | 30s |
| MaxConnDuration | 300s |
| MaxConnsPerHost | 200 |

### 10.3 Proxy Support

`GlobalProxyConfig` supports HTTP, SOCKS5, and TCP proxies with:
- Optional basic auth (`Username` + `Password`)
- `NoProxy` bypass list
- Per-purpose enable flags (`EnableForInference`, `EnableForAPI`, `EnableForSCIM`)
- TLS verification skip option

Proxy configuration can be updated at runtime — the factory rebuilds clients on next use.

### 10.4 Multipart Handling

`network/multipart.go` constructs multipart request bodies for file upload, image editing, and audio transcription endpoints. Streaming-aware to avoid buffering large files entirely in memory.

---

## 11. Object Pooling

To achieve ~11 µs overhead at 5k RPS, Bifrost pre-allocates and pools all hot-path objects:

| Pool | Objects | Purpose |
|------|---------|---------|
| `channelMessagePool` | `*ChannelMessage` | Request routing messages |
| `responseChannelPool` | `chan *BifrostResponse` | Response delivery channels |
| `errorChannelPool` | `chan BifrostError` | Error delivery channels |
| `responseStreamPool` | `chan chan *BifrostStreamChunk` | Streaming response channels |
| `pluginPipelinePool` | `*PluginPipeline` | Plugin execution state |
| `bifrostRequestPool` | `*BifrostRequest` | Request structs |
| `mcpRequestPool` | `*BifrostMCPRequest` | MCP request structs |

Pools are pre-warmed at `Init()` to `InitialPoolSize` (default 5000) to avoid cold GC pressure.

`HTTPRequest` and `HTTPResponse` (for plugin transport interception) are also pooled with pre-allocated maps (16 headers, 8 query params, 4 path params).

---

## 12. Context System

`BifrostContext` embeds `context.Context` and is used as the carrier for all cross-cutting metadata within a request lifecycle.

**Creation:** `schemas.NewBifrostContextWithCancel(ctx)` wraps a standard context.

**Key design:** All keys are typed `BifrostContextKey` strings. Values are stored with `context.WithValue`. The context is never nil; an empty context is safe to use.

**Plugin communication via context:** Plugins use context values to pass state between PreHook and PostHook. Example: semantic cache stores the request embedding in context during PreHook so PostHook can read it without recomputing.

---

## 13. Supported Request Types

The engine dispatches to provider-specific handlers based on `RequestType`. Not all providers support all types:

| Request Type | Providers Supporting It (examples) |
|---|---|
| `chat_completion` | All 22 providers |
| `embedding` | OpenAI, Cohere, Vertex, Gemini, HuggingFace, Nebius, ... |
| `rerank` | Cohere |
| `speech` (TTS) | OpenAI, ElevenLabs |
| `transcription` (STT) | OpenAI, HuggingFace |
| `image_generation` | OpenAI (DALL-E), Replicate, Runway |
| `video_generation` | Runway, Replicate |
| `batch_*` | OpenAI, Anthropic, Bedrock |
| `file_*` | OpenAI, Anthropic |
| `container_*` | OpenAI |

Provider-specific supported types are declared via `GetSupportedRequests()`.

---

## 14. Streaming Architecture

### 14.1 SSE Streaming

For `*_stream` request types, providers return a channel of `*BifrostStreamChunk`:

```go
type BifrostStreamChunk struct {
    // Delta content, tool call deltas, etc.
    FinalChunk bool   // True for the last chunk (signals end of stream)
}
```

The Bifrost core pipes chunks from the provider's response reader through the `ResponseStream` channel to the transport layer.

### 14.2 Streaming Plugin Hooks

Each chunk passes through `HTTPTransportStreamChunkHook` in reverse plugin order before being written to the client. This enables:
- Cache accumulation (semantic cache captures full response)
- Per-chunk logging (logging plugin tracks streaming deltas)
- JSON validation and repair (jsonparser plugin)

---

## 15. Failover & Fallback Logic

### 15.1 Automatic Retry

On a transient error (5xx, timeout, rate limit) from a provider, Bifrost:
1. Logs the error with the provider name
2. Increments `fallback_index` in context
3. Tries the next entry in `req.Fallbacks`

### 15.2 Fallback Target Format

```go
req.Fallbacks = []string{
    "anthropic/claude-3-5-sonnet-20241022",
    "bedrock/anthropic.claude-3-sonnet",
}
```

Each fallback is a full `provider/model` string parsed identically to the primary target.

### 15.3 Plugin Error Control

When a `LLMPlugin` returns a `BifrostError`, the `AllowFallbacks` field controls behavior:

| Value | Behavior |
|-------|---------|
| `nil` | Same as `&true` (default — attempt fallbacks) |
| `&true` | Attempt fallbacks |
| `&false` | Return error immediately, skip remaining fallbacks |

This allows the governance plugin to block requests on budget exhaustion (`AllowFallbacks = &false`) while other error types naturally retry.

---

*Derived from source code analysis of `/core/` as of 2026-04-05.*
