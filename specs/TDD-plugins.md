# Technical Design Document — Plugins
## Bifrost Plugin System

**Version:** 1.0  
**Date:** 2026-04-05  
**Status:** Draft  
**References:** [TDD-project.md](TDD-project.md), [TDD-framework.md](TDD-framework.md)

---

## Table of Contents

1. [Overview](#1-overview)
2. [Plugin Interface Contract](#2-plugin-interface-contract)
3. [Plugin Lifecycle & Execution Order](#3-plugin-lifecycle--execution-order)
4. [Governance Plugin](#4-governance-plugin)
5. [Logging Plugin](#5-logging-plugin)
6. [Semantic Cache Plugin](#6-semantic-cache-plugin)
7. [Telemetry Plugin (Prometheus)](#7-telemetry-plugin-prometheus)
8. [OpenTelemetry Plugin](#8-opentelemetry-plugin)
9. [Maxim Plugin](#9-maxim-plugin)
10. [LiteLLM Compatibility Plugin](#10-litellm-compatibility-plugin)
11. [Mocker Plugin](#11-mocker-plugin)
12. [JSON Parser Plugin](#12-json-parser-plugin)
13. [Plugin Registration & Configuration](#13-plugin-registration--configuration)

---

## 1. Overview

The Bifrost plugin system is a **symmetric pre/post hook middleware pipeline** that wraps every LLM inference call and HTTP transport event. Plugins intercept requests and responses to add cross-cutting concerns — governance, observability, caching, testing — without modifying core engine logic.

**Plugin directory structure:**

```
plugins/
├── governance/       # Budget, rate limits, routing enforcement
├── logging/          # Request/response log persistence
├── semanticcache/    # Semantic + exact-match response cache
├── telemetry/        # Prometheus metrics
├── otel/             # OpenTelemetry traces & metrics
├── maxim/            # Maxim observability integration
├── litellmcompat/    # LiteLLM text-to-chat compatibility shim
├── mocker/           # Mock response injection for testing
└── jsonparser/       # Streaming JSON validation & repair
```

Each plugin is an independent Go module that imports the core schemas and implements one or more plugin interfaces.

---

## 2. Plugin Interface Contract

### 2.1 Base Interface

All plugins implement:

```go
type BasePlugin interface {
    GetName() string   // Unique plugin identifier
    Cleanup() error    // Called on server shutdown — flush buffers, close connections
}
```

### 2.2 LLM Hook Interface

Plugins that intercept inference calls:

```go
type LLMPlugin interface {
    BasePlugin
    PreLLMHook(ctx *BifrostContext, req *BifrostRequest) (
        *BifrostRequest, *LLMPluginShortCircuit, error)
    PostLLMHook(ctx *BifrostContext, resp *BifrostResponse, bifrostErr *BifrostError) (
        *BifrostResponse, *BifrostError, error)
}
```

- `PreLLMHook` — runs before the provider is called; can modify the request or short-circuit the pipeline
- `PostLLMHook` — runs after the provider responds; can modify response or recover errors
- **Symmetry guarantee:** PostLLMHook is only called for plugins whose PreLLMHook was executed

### 2.3 HTTP Transport Interface

Plugins that intercept at the HTTP layer (before routing):

```go
type HTTPTransportPlugin interface {
    BasePlugin
    HTTPTransportPreHook(ctx *BifrostContext, req *HTTPRequest) (*HTTPResponse, error)
    HTTPTransportPostHook(ctx *BifrostContext, req *HTTPRequest, resp *HTTPResponse) error
    HTTPTransportStreamChunkHook(ctx *BifrostContext, req *HTTPRequest, chunk *BifrostStreamChunk) (
        *BifrostStreamChunk, error)
}
```

### 2.4 Observability Interface

Plugins that consume completed traces asynchronously:

```go
type ObservabilityPlugin interface {
    BasePlugin
    Inject(ctx context.Context, trace *Trace) error
}
```

`Inject` is called **after the response has been sent to the client**, ensuring it never adds latency to the request path.

### 2.5 Short-Circuit

`LLMPluginShortCircuit` allows a PreLLMHook to skip the provider call entirely and return a synthetic response:

```go
type LLMPluginShortCircuit struct {
    Response *BifrostResponse
}
```

When returned (non-nil), remaining PreHooks are skipped, the provider is not called, and PostHooks for all previously-executed PreHooks run in reverse order.

---

## 3. Plugin Lifecycle & Execution Order

### 3.1 Registration Order

Plugins are registered as an ordered slice. Execution order follows this registration order for Pre hooks, and **reverse** order for Post hooks (stack semantics).

### 3.2 Per-Request Pipeline

```
Incoming request
    ↓
[HTTPTransportPreHook] × plugins (forward order)
    ↓
[PreLLMHook] × plugins (forward order)
    │  ── if ShortCircuit returned, skip provider call ──┐
    ↓                                                     │
Provider HTTP call                                       │
    ↓                                                    ─┘
[PostLLMHook] × plugins (reverse order)
    ↓
[HTTPTransportPostHook] × plugins (reverse order)
    ↓
Client receives response
    ↓ (async)
[Inject] × ObservabilityPlugins (async, non-blocking)
```

### 3.3 Streaming Pipeline

For streaming responses, `HTTPTransportStreamChunkHook` runs per-chunk in reverse plugin order, allowing each plugin to observe or transform individual SSE chunks.

### 3.4 Error Recovery

`PostLLMHook` receives both `*BifrostResponse` and `*BifrostError` (either may be nil). A plugin can **recover an error** by setting the error to nil and returning a valid response. This enables fallback response injection at the plugin layer.

### 3.5 Shutdown

`Cleanup()` is called on all registered plugins during server shutdown. Plugins must flush pending async work (log batches, metric pushes) in `Cleanup()`.

---

## 4. Governance Plugin

**Package:** `plugins/governance`  
**Interfaces:** `LLMPlugin`, `HTTPTransportPlugin`  
**Purpose:** Enforce hierarchical budget limits, rate limits, virtual key validation, and dynamic routing rules.

### 4.1 Architecture

```
GovernancePlugin
  ├── BudgetResolver      — pure decision engine (no I/O)
  ├── UsageTracker        — persists and resets usage counters
  └── RoutingEngine       — evaluates routing rules per request
```

### 4.2 Configuration

```go
type Config struct {
    IsVkMandatory   *bool      // If true, requests without x-bf-vk are rejected
    RequiredHeaders *[]string  // Live-configurable required headers
    IsEnterprise    bool       // Enables enterprise features
}
```

### 4.3 Governance Rules (Pre Hook)

Enforced in order:

| Rule | Description |
|------|-------------|
| Virtual Key validation | Extracts `x-bf-vk` from context; rejects if mandatory and missing |
| Model whitelist | Checks if requested model is allowed by the virtual key |
| Provider validation | Checks if requested provider is configured on the virtual key |
| Budget check | Evaluates team → customer → VK budget hierarchy |
| Rate limit check | Evaluates request + token rate limits |
| Routing rule evaluation | Applies matching CEL routing rules to override provider/model |

### 4.4 Usage Tracking (Post Hook)

After a successful response, `PostLLMHook` records:
- Token usage (prompt + completion tokens) against VK, team, customer budgets
- USD cost against budget limits
- Request count against rate limits

### 4.5 Data Storage

- **In-memory:** `LocalGovernanceStore` — in-process map with TTL-based expiry
- **Persistent:** Optional `ConfigStore` (SQL/SQLite via Gorm) for durability across restarts
- **Distributed reset:** Lock-based startup reset for multi-node deployments

### 4.6 Hierarchical Budget Hierarchy

```
Global
  └── Team budget
        └── Customer budget
              └── Virtual Key budget
```

Each level can independently specify `max_limit`, `reset_duration`, and `calendar_aligned`. The most restrictive active limit wins. Exceeding any level returns HTTP 429.

---

## 5. Logging Plugin

**Package:** `plugins/logging`  
**Interface:** `LLMPlugin`  
**Purpose:** Persist every request and response to a log store for observability and analytics.

### 5.1 Configuration

```go
type Config struct {
    Enabled          bool
    BatchSize        int
    FlushInterval    time.Duration
    MaxConcurrency   int
}
```

### 5.2 Log Entry Lifecycle

```
PreLLMHook
  └── CreateInitialLogEntry(provider, model, params, inputs, requestID)
       └── Writes: timestamp, provider, model, VK ID, session token, request body

PostLLMHook
  └── UpdateLogEntry(status, tokens, cost, latency, response body, errors)

Streaming PostLLMHook
  └── UpdateLogEntryDelta(chunk) — per-chunk token delta accumulation
       └── On FinalChunk: writes total usage
```

### 5.3 Async Batch Processing

Logs are not written synchronously. They enter a queue and are flushed in batches:
- Configurable `BatchSize` and `FlushInterval`
- Background goroutine drains queue at flush interval or when batch fills
- `MaxConcurrency` limits parallel DB writes
- `Cleanup()` performs a final flush before shutdown

### 5.4 Large Payload Handling

When `BifrostContextKeyLargePayloadMode` is set in context, request/response bodies are truncated before logging. This prevents excessive memory usage for large payloads (multimodal, batch, etc.).

### 5.5 Deferred Usage Updates

For streaming responses, token counts may arrive out-of-band. The plugin uses a `BifrostContextKeyDeferredUsage` channel to receive token updates after the response is sent and applies them as cost backfill operations.

### 5.6 Supported Request Types

Logs all request types: chat completion, text completion, embeddings, image generation, audio (TTS/STT), video, rerank.

---

## 6. Semantic Cache Plugin

**Package:** `plugins/semanticcache`  
**Interface:** `LLMPlugin`  
**Purpose:** Two-tier response caching: exact-match (hash) and semantic-similarity (embedding).

### 6.1 Configuration

```go
type Config struct {
    Provider                     schemas.ModelProvider  // Embedding provider
    Keys                         []schemas.Key          // Embedding API keys
    EmbeddingModel               string                 // e.g., "text-embedding-3-small"
    TTL                          time.Duration          // Default: 5 minutes
    Threshold                    float64                // Cosine similarity (default: 0.8)
    VectorStoreNamespace         string
    Dimension                    int                    // Embedding vector dimension
    DefaultCacheKey              string
    ConversationHistoryThreshold int                    // Skip caching if > N messages
    CacheByModel                 *bool                  // Default: true
    CacheByProvider              *bool                  // Default: true
    ExcludeSystemPrompt          *bool                  // Default: false
    CleanUpOnShutdown            bool
}
```

### 6.2 Caching Tiers

**Tier 1 — Direct Hash Cache (Exact Match)**

```
Cache ID: "<provider>:<model>:<cacheKey>:<xxhash(request)>:<paramsHash>"
Lookup:   O(1) via VectorStore direct chunk fetch
```

Uses xxhash for fast request fingerprinting. Parameters hash is computed separately to allow strict parameter matching.

**Tier 2 — Semantic Cache (Similarity Match)**

```
Cache ID: embedding vector stored in VectorStore
Lookup:   k-NN cosine similarity search with metadata filters
Match:    Returns top-1 result if cosine similarity ≥ Threshold
```

Metadata filters enforce: `cache_key`, `params_hash`, `provider`, `model` — preventing false-positive matches across different configurations.

### 6.3 Cache Key Composition

The cache key string is built from:
1. Concatenated message content (excluding system prompt if `ExcludeSystemPrompt = true`)
2. Provider name (if `CacheByProvider = true`)
3. Model name (if `CacheByModel = true`)
4. Per-request override from request context

### 6.4 Pre Hook Flow

```
1. Compute request hash → direct cache lookup
2. If hit (Tier 1) → ShortCircuit with cached response
3. Generate text embedding for semantic lookup
4. VectorStore nearest-neighbor query with metadata filters
5. If hit (Tier 2) → ShortCircuit with cached response
6. Store embedding in context (for Post Hook)
```

### 6.5 Post Hook Flow

```
1. If cache miss (hit=false in context):
   a. Retrieve embedding from context
   b. Store response in VectorStore with TTL
   c. Store direct hash entry for future exact matches
2. If streaming: use StreamAccumulator
   a. Accumulate chunks per-request
   b. On FinalChunk: serialize chunks to JSON, store in cache
```

### 6.6 Stream Accumulation

`StreamAccumulator` tracks in-flight streaming responses keyed by request ID. On the final chunk, the full accumulated response is written to the cache as a serialized JSON chunk array. Cache hits for streaming requests replay the stored chunk array as synthetic stream chunks.

---

## 7. Telemetry Plugin (Prometheus)

**Package:** `plugins/telemetry`  
**Interfaces:** `LLMPlugin`, `HTTPTransportPlugin`  
**Purpose:** Expose Prometheus metrics for all HTTP and upstream provider calls.

### 7.1 Configuration

```go
type Config struct {
    CustomLabels []string              // Additional label key names
    Registry     *prometheus.Registry  // Custom registry (nil = default)
    PushGateway  *PushGatewayConfig
}

type PushGatewayConfig struct {
    Enabled        bool
    PushGatewayURL string   // e.g., http://pushgateway:9091
    JobName        string   // Default: "bifrost"
    InstanceID     string   // Default: hostname
    PushInterval   int      // Seconds, default: 15
    BasicAuth      *BasicAuthConfig
}
```

### 7.2 Metrics

**HTTP Layer Metrics:**

| Metric | Type | Labels |
|--------|------|--------|
| `http_requests_total` | Counter | path, method, status |
| `http_request_duration_seconds` | Histogram | path, method, status |
| `http_request_size_bytes` | Histogram | path, method |
| `http_response_size_bytes` | Histogram | path, method |

**Bifrost Upstream Metrics:**

| Metric | Type | Labels |
|--------|------|--------|
| `bifrost_upstream_requests_total` | Counter | provider, model, method, virtual_key_id, virtual_key_name, routing_engine_used, routing_rule_id, routing_rule_name, selected_key_id, selected_key_name, number_of_retries, fallback_index, team_id, team_name, customer_id, customer_name |
| `bifrost_upstream_latency_seconds` | Histogram | same as above |
| `bifrost_success_requests_total` | Counter | same as above |
| `bifrost_error_requests_total` | Counter | + status_code |
| `bifrost_input_tokens_total` | Counter | provider, model |
| `bifrost_output_tokens_total` | Counter | provider, model |
| `bifrost_cache_hits_total` | Counter | + cache_type (direct/semantic) |
| `bifrost_cost_total` | Counter | provider, model |
| `bifrost_stream_first_token_latency_seconds` | Histogram | provider, model |
| `bifrost_stream_inter_token_latency_seconds` | Histogram | provider, model |

**Built-in Collectors:** GoCollector (runtime), ProcessCollector (OS memory/CPU).

### 7.3 Push Gateway

In multi-node deployments, Prometheus scraping produces incorrect per-instance aggregates. The push gateway option periodically pushes metrics to a Prometheus Push Gateway at configurable intervals (default: 15s). The final push happens synchronously in `Cleanup()` before shutdown.

### 7.4 Hook Implementation

- `PreLLMHook` — records request start timestamp in context
- `PostLLMHook` — computes latency, extracts token/cost data, records all upstream metrics in a background goroutine (non-blocking)
- `HTTPTransportPreHook` / `HTTPTransportPostHook` — records HTTP-layer metrics

---

## 8. OpenTelemetry Plugin

**Package:** `plugins/otel`  
**Interface:** `ObservabilityPlugin`  
**Purpose:** Export distributed traces to an OpenTelemetry collector.

### 8.1 Configuration

```go
type Config struct {
    ServiceName         string
    CollectorURL        string            // OTLP endpoint
    Headers             map[string]string // "env." prefix = env var reference
    TraceType           TraceType         // "genai_extension" | "vercel" | "open_inference"
    Protocol            Protocol          // "http" | "grpc"
    TLSCACert           string            // Path to CA cert
    Insecure            bool
    MetricsEnabled      bool
    MetricsEndpoint     string
    MetricsPushInterval int               // 1–300 seconds, default: 15
}
```

### 8.2 Trace Format Support

| Format | Description |
|--------|-------------|
| `genai_extension` | Specialized LLM trace attributes (tokens, model, provider) |
| `vercel` | Vercel's proprietary trace standard |
| `open_inference` | Community open standard for LLM traces |

### 8.3 Async Trace Injection

The `Inject(ctx, trace)` method converts a completed `schemas.Trace` to OTLP `ResourceSpan` format and exports it asynchronously. This means zero latency impact on the request path.

Resource attributes are read from the `OTEL_RESOURCE_ATTRIBUTES` environment variable at startup.

### 8.4 Metrics Export

When `MetricsEnabled = true`, the plugin also pushes Prometheus-style metrics to an OTEL metrics endpoint at the configured interval. Metrics include upstream requests, latency, success/error, token usage, and cost (resolved via ModelCatalog for pricing).

---

## 9. Maxim Plugin

**Package:** `plugins/maxim`  
**Interface:** `LLMPlugin`, `ObservabilityPlugin`  
**Purpose:** Structured trace/generation logging to Maxim's observability platform.

### 9.1 Configuration

```go
type Config struct {
    LogRepoID string  // Default log repository ID (empty = skip logging)
    APIKey    string  // Maxim API key (required)
}
```

### 9.2 Trace Lifecycle

**PreLLMHook:**
1. Resolve effective log repo ID (request context override > default)
2. Create or reuse Maxim trace (using `TraceIDKey` from context for multi-turn reuse)
3. Create a generation within the trace (tagged with model, provider, messages, parameters)
4. Extract and log file/image attachments from message content
5. If streaming: register with StreamAccumulator

**PostLLMHook (async):**
1. Add response content to generation
2. Attach error details if present
3. Attach custom tags (`TagsKey` from context)
4. End generation and trace
5. Async flush (non-blocking)

### 9.3 Context Keys

| Key | Type | Purpose |
|-----|------|---------|
| `TraceIDKey` | string | Reusable trace ID for multi-request sessions |
| `GenerationIDKey` | string | Unique per-request generation ID |
| `SessionIDKey` | string | Groups traces into sessions |
| `TraceNameKey` | string | Human-readable trace label |
| `GenerationNameKey` | string | Human-readable generation label |
| `LogRepoIDKey` | string | Per-request log repository override |
| `TagsKey` | `map[string]string` | Custom key-value tags |

### 9.4 Multi-Repository Support

Maxim loggers are created lazily and cached per `LogRepoID`. Different requests can target different log repositories without instantiating multiple plugin instances. Access to the logger map is protected by `RWMutex`.

---

## 10. LiteLLM Compatibility Plugin

**Package:** `plugins/litellmcompat`  
**Interface:** `LLMPlugin`  
**Purpose:** Transparent conversion of LiteLLM text completion requests to chat completion format when the target model does not natively support text completions.

### 10.1 Transformation Logic

**PreLLMHook:**
1. Check if request is a `TextCompletionRequest`
2. Query ModelCatalog (if available) — does the model support text completion natively?
3. If **no native support**: convert to `ChatCompletionRequest` by wrapping prompt in `[{role: "user", content: prompt}]`
4. Store a `TransformContext` in the Bifrost context indicating the transformation was applied
5. If streaming: update request type to `ChatCompletionStreamRequest`

**PostLLMHook:**
1. Check if `TransformContext` is set in context
2. If yes: convert `ChatCompletionResponse` → `TextCompletionResponse`
   - Move `choices[0].message.content` → `choices[0].text`
3. Set `LiteLLMCompat = true` in response metadata
4. Propagate error metadata if present

### 10.2 Design Principle

This plugin makes the transformation **invisible to callers**. Client code using `/v1/completions` gets back a properly formatted text completion response regardless of whether the backend provider executed a chat completion internally.

---

## 11. Mocker Plugin

**Package:** `plugins/mocker`  
**Interface:** `LLMPlugin`  
**Purpose:** Inject synthetic mock responses based on configurable rules, for testing and development.

### 11.1 Configuration

```go
type MockerConfig struct {
    Enabled         bool
    GlobalLatency   *Latency
    Rules           []MockRule      // Evaluated in priority order
    DefaultBehavior string          // "passthrough" | "error" | "success"
}

type MockRule struct {
    Name        string
    Enabled     bool
    Priority    int
    Conditions  Conditions          // AND-logic matching
    Responses   []Response          // Weighted random selection
    Latency     *Latency            // Overrides GlobalLatency
    Probability float64             // 0.0–1.0 activation probability
}

type Conditions struct {
    Providers    []string
    Models       []string
    MessageRegex *string
    RequestSize  *SizeRange
}

type Latency struct {
    Type     string   // "fixed" | "uniform"
    Value    float64  // ms; or min for uniform
    MaxValue float64  // max for uniform
}
```

### 11.2 Rule Engine

```
1. Iterate rules in descending priority order
2. For each enabled rule:
   a. Match all conditions (AND logic):
      - provider in Providers list
      - model in Models list
      - message content matches MessageRegex
      - request size within SizeRange
   b. Apply probability threshold (uniform random)
3. If no rule matches: apply DefaultBehavior
4. Select response by normalized weight distribution
5. Apply latency (rule-specific or global)
6. Return ShortCircuit with generated response
```

### 11.3 Response Generation

Success responses are generated using a **faker library** to produce realistic content (plausible text, proper token counts, realistic finish reasons). Error responses return a `BifrostError` with configurable type and message.

### 11.4 Statistics

Each rule maintains atomic hit counters (`RuleHits`, `ResponsesGenerated`) for runtime observability.

---

## 12. JSON Parser Plugin

**Package:** `plugins/jsonparser`  
**Interface:** `LLMPlugin`  
**Purpose:** Validate and repair partial JSON in streaming LLM responses.

### 12.1 Configuration

```go
type PluginConfig struct {
    Usage           Usage          // "all_requests" | "per_request"
    CleanupInterval time.Duration  // Cleanup goroutine interval
    MaxAge          time.Duration  // Entry expiry (default: 30 min)
}
```

### 12.2 Parsing Strategy

1. **Chunk Accumulation:** Per-request buffer stores streaming content chunks (keyed by request ID)
2. **Partial JSON Repair:** Attempt to make accumulated content valid JSON by appending missing closing tokens
3. **Validation:** `json.Valid()` check on repaired content
4. **Error Handling:** If repair fails, set `SkipStream = true` on the chunk

### 12.3 Per-Request vs All-Requests Mode

| Mode | Behavior |
|------|---------|
| `all_requests` | Parse all streaming chat responses unconditionally |
| `per_request` | Only parse when `EnableStreamingJSONParser = true` in context |

### 12.4 Memory Management

Accumulation buffers are cleared on `FinalChunk`. A background goroutine runs at `CleanupInterval` and evicts entries older than `MaxAge` to prevent memory leaks from abandoned connections. All map access is protected by `RWMutex`.

---

## 13. Plugin Registration & Configuration

### 13.1 Runtime Enable/Disable

All plugins are manageable at runtime via:

```
GET    /api/plugins          → List plugins with names and enabled status
GET    /api/plugins/{name}   → Get plugin configuration
PUT    /api/plugins/{name}   → Update plugin configuration (hot reload)
DELETE /api/plugins/{name}   → Disable plugin
```

Changing plugin configuration via API triggers the framework's config reload callbacks, allowing plugins to pick up new settings without restarting the server.

### 13.2 Configuration Persistence

Plugin configurations are stored in the `ConfigStore` (SQL database) under their plugin name key. On startup, configurations are loaded and passed to each plugin's initializer.

### 13.3 First-Party Plugin Initialization Order

Recommended initialization order (respects data dependencies):

```
1. litellmcompat  — request normalization (earliest)
2. mocker         — may short-circuit before governance
3. governance     — budget/rate-limit enforcement
4. semanticcache  — cache lookup (before provider call)
5. logging        — record all requests including cache hits
6. telemetry      — metrics (broadest coverage)
7. otel           — traces (after logging has request ID)
8. maxim          — observability (last post hook)
9. jsonparser     — streaming repair (last in chain)
```

---

*Derived from source code analysis of `/plugins/` as of 2026-04-05.*
