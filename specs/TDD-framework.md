# Technical Design Document — Framework
## Bifrost Framework (`framework/`)

**Version:** 1.0  
**Date:** 2026-04-05  
**Status:** Draft  
**References:** [TDD-project.md](TDD-project.md), [TDD-core.md](TDD-core.md)

---

## Table of Contents

1. [Overview](#1-overview)
2. [Package Structure](#2-package-structure)
3. [Configuration Store](#3-configuration-store)
4. [Log Store](#4-log-store)
5. [Key-Value Store](#5-key-value-store)
6. [Vector Store](#6-vector-store)
7. [Model Catalog](#7-model-catalog)
8. [MCP Catalog](#8-mcp-catalog)
9. [Streaming Accumulator](#9-streaming-accumulator)
10. [OAuth2](#10-oauth2)
11. [Encryption](#11-encryption)
12. [Environment Utilities](#12-environment-utilities)
13. [Migration System](#13-migration-system)
14. [Plugin Loader](#14-plugin-loader)
15. [Tracing](#15-tracing)

---

## 1. Overview

`framework/` is the **persistence and service layer** that sits between `core/` and `transports/`. It provides:

- **ConfigStore** — SQL-backed CRUD for providers, governance entities, sessions, MCP clients, prompts, and plugins
- **LogStore** — Request/response log storage with rich querying, histograms, and analytics
- **KVStore** — Abstract key-value store for session stickiness and caching metadata
- **VectorStore** — Vector embedding storage for semantic cache
- **ModelCatalog** — Model capabilities, pricing, and parameter schemas
- **Streaming** — Cross-plugin `StreamAccumulator` for coherent streaming response data
- **OAuth2** — Token management for enterprise identity providers
- **Encryption** — AES-256-GCM encryption for sensitive config data

`framework/` depends on `core/schemas` for shared types, but does not depend on `transports/`.

---

## 2. Package Structure

```
framework/
├── config.go              # FrameworkConfig (wraps ModelCatalog config)
├── list.go                # List helpers for entity collections
├── configstore/           # SQL-backed configuration persistence
│   ├── store.go           # ConfigStore interface + query params
│   ├── config.go          # ClientConfig CRUD
│   ├── rdb.go             # Shared Gorm RDB setup
│   ├── sqlite.go          # SQLite implementation
│   ├── postgres.go        # PostgreSQL implementation
│   ├── migrations.go      # Schema migrations
│   ├── encryption.go      # Transparent field encryption
│   ├── prompts.go         # Prompt repository operations
│   ├── clientconfig.go    # Client config operations
│   ├── dlock.go           # Distributed lock implementation
│   ├── logger.go          # Config store logger
│   ├── errors.go          # Error types
│   ├── utils.go           # Helpers
│   └── tables/            # Gorm model definitions
├── logstore/              # Request log persistence
│   ├── store.go           # LogStore interface
│   ├── config.go          # Log config types
│   ├── asyncjob.go        # Async job table operations
│   ├── cleaner.go         # Log retention cleanup goroutine
│   ├── rdb.go             # Shared Gorm setup
│   ├── sqlite.go          # SQLite implementation
│   ├── postgres.go        # PostgreSQL implementation
│   ├── migrations.go      # Log table migrations
│   ├── matviews.go        # Materialized views for histograms
│   ├── tables.go          # Gorm log model
│   ├── errors.go
│   └── logger.go
├── kvstore/               # Generic key-value store
│   └── kvstore.go         # KVStore interface + in-memory implementation
├── vectorstore/           # Vector similarity search
│   └── (implementations) # Interface + backends (Qdrant, etc.)
├── modelcatalog/          # Model registry and pricing
│   ├── main.go            # ModelCatalog struct + GetCapabilities()
│   ├── config.go          # Pricing config types
│   ├── pricing.go         # Pricing lookup and calculation
│   ├── overrides.go       # Custom pricing override management
│   ├── sync.go            # Remote pricing data synchronization
│   └── utils.go           # Helpers
├── mcpcatalog/            # MCP server catalog
├── streaming/             # Cross-plugin stream accumulation
│   ├── types.go           # StreamAccumulator, chunk types, AccumulatedData
│   ├── accumulator.go     # Accumulator lifecycle management
│   ├── chat.go            # Chat completion chunk processing
│   ├── audio.go           # Audio chunk processing
│   ├── images.go          # Image chunk processing
│   ├── responses.go       # Responses API chunk processing
│   └── transcription.go   # Transcription chunk processing
├── oauth2/                # OAuth2 token management
├── encrypt/               # AES-256-GCM encryption utilities
├── envutils/              # Environment variable resolution
├── migrator/              # Config/data migration orchestrator
├── plugins/               # Plugin loader and WASM/native plugin support
└── tracing/               # Distributed tracing bridge
```

---

## 3. Configuration Store

### 3.1 Interface

`ConfigStore` is a comprehensive CRUD interface for all system configuration. It is the single source of truth for persisted state.

**Entities managed:**

| Entity | Operations |
|--------|-----------|
| Client config | Read, update |
| Providers | List, get, create, update, delete |
| API keys | List, get by provider |
| Virtual keys | List (paginated), get, create, update, delete |
| Teams | List (paginated), get, create, update, delete |
| Customers | List (paginated), get, create, update, delete |
| Routing rules | List (paginated), get, create, update, delete |
| Model configs | List (paginated), get, create, update, delete |
| Provider governance | List, get, update, delete |
| MCP clients | List (paginated), get, create, update, delete |
| Sessions | Create, find by token, delete |
| WS tickets | Create, consume (single-use) |
| Plugins | List, get, upsert, delete |
| Prompt folders | List, get, create, update, delete |
| Prompts | List, get, create, update, delete |
| Prompt versions | List, get, create |
| Prompt sessions | Create, list |
| Budgets / rate limits | List (read-only from governance) |
| Pricing overrides | List, get, create, update, delete |
| Async jobs | Create, get, update, list, delete |

### 3.2 Implementations

| Backend | Module | Use Case |
|---------|--------|---------|
| SQLite | `configstore/sqlite.go` | Single-node, zero-dependency (default) |
| PostgreSQL | `configstore/postgres.go` | Multi-node or larger scale |

**Gorm ORM** is used for both backends. Schema migrations run at startup via `configstore/migrations.go`.

### 3.3 Query Parameters

Paginated endpoints accept typed query parameter structs:

```go
type VirtualKeyQueryParams struct {
    Limit, Offset int
    Search        string    // full-text search
    CustomerID    string    // filter by customer
    TeamID        string    // filter by team
}
```

Similar structs exist for Teams, Customers, Routing Rules, MCP Clients, Model Configs.

### 3.4 Transparent Field Encryption

When an encryption key is configured, `configstore/encryption.go` transparently encrypts/decrypts sensitive fields (API keys, credentials) before writing to and after reading from the database. Uses AES-256-GCM via `framework/encrypt`.

### 3.5 Distributed Lock

`configstore/dlock.go` provides a database-backed distributed lock used for:
- Multi-node budget reset coordination (governance plugin)
- Preventing duplicate migrations across nodes

### 3.6 Tables Package

`configstore/tables/` contains all Gorm model structs (table definitions):
- `ProvidersTable`, `KeysTable`, `VirtualKeysTable`, `TeamsTable`, `CustomersTable`
- `RoutingRulesTable`, `ModelConfigsTable`, `MCPClientsTable`
- `SessionsTable`, `PluginsTable`
- `FoldersTable`, `PromptsTable`, `VersionsTable`, `PromptSessionsTable`
- `PricingOverridesTable`, `AsyncJobsTable`

---

## 4. Log Store

### 4.1 `LogStore` Interface

```go
type LogStore interface {
    // CRUD
    Create(ctx, *Log) error
    CreateIfNotExists(ctx, *Log) error
    BatchCreateIfNotExists(ctx, []*Log) error
    FindByID(ctx, id) (*Log, error)
    Update(ctx, id, entry) error
    BulkUpdateCost(ctx, map[string]float64) error
    DeleteLog / DeleteLogs / DeleteLogsBatch

    // Rich queries
    SearchLogs(ctx, SearchFilters, PaginationOptions) (*SearchResult, error)
    GetStats(ctx, SearchFilters) (*SearchStats, error)

    // Histograms (pre-aggregated time buckets)
    GetHistogram / GetTokenHistogram / GetCostHistogram / GetModelHistogram
    GetLatencyHistogram / GetProviderCostHistogram / GetProviderTokenHistogram
    GetProviderLatencyHistogram

    GetModelRankings(ctx, SearchFilters) (*ModelRankingResult, error)

    // Filter metadata
    GetDistinctModels / GetDistinctKeyPairs / GetDistinctRoutingEngines / GetDistinctMetadataKeys

    // MCP tool logs
    GetMCPHistogram / GetMCPCostHistogram / GetMCPTopTools

    // Lifecycle
    Flush(ctx, since) error
    Close(ctx) error
    Ping(ctx) error
}
```

### 4.2 Implementations

| Backend | Module | Notes |
|---------|--------|-------|
| SQLite | `logstore/sqlite.go` | Default; single-file |
| PostgreSQL | `logstore/postgres.go` | Supports materialized views for histogram performance |

### 4.3 `SearchFilters`

```go
type SearchFilters struct {
    Providers       []string
    Models          []string
    Status          string
    StartTime       *time.Time
    EndTime         *time.Time
    VirtualKeyIDs   []string
    RoutingRuleIDs  []string
    MinLatency      *float64
    MaxLatency      *float64
    MinCost         *float64
    MaxCost         *float64
    SearchText      string
    SortBy          string
    Order           string    // "asc" | "desc"
}
```

### 4.4 Histogram Buckets

Histograms compute time-bucket aggregations at query time (SQLite) or via materialized views (PostgreSQL). Bucket size is configurable in seconds.

### 4.5 Log Retention Cleaner

`logstore/cleaner.go` runs a background goroutine that periodically deletes log entries older than `log_retention_days` (default: 365). Uses `DeleteLogsBatch` to avoid table locks on large deletes.

### 4.6 Async Job Store

`logstore/asyncjob.go` manages the async inference job lifecycle:

| Status | Description |
|--------|-------------|
| `pending` | Job submitted, inference not started |
| `running` | Inference in progress |
| `completed` | Response ready |
| `failed` | Error occurred |

A background cleaner deletes jobs older than configurable TTL.

---

## 5. Key-Value Store

### 5.1 `KVStore` Interface

```go
type KVStore interface {
    Get(ctx, key string) (string, error)
    Set(ctx, key, value string, ttl time.Duration) error
    Delete(ctx, key string) error
    Close() error
}
```

### 5.2 Use Cases

| Use Case | Key Pattern | Value |
|----------|------------|-------|
| Session stickiness | `session:<session_id>:<provider>` | selected key ID |
| WebSocket tickets | `ws-ticket:<token>` | session token |
| Distributed rate limit counters (enterprise) | `rl:<vk_id>:<window>` | counter |

### 5.3 Default Implementation

An in-memory implementation with TTL eviction is provided for single-node deployments. Enterprise builds connect to Redis for distributed KV.

---

## 6. Vector Store

### 6.1 `VectorStore` Interface

Used by the semantic cache plugin for similarity search:

```go
type VectorStore interface {
    Upsert(ctx, namespace string, vectors []VectorEntry) error
    Query(ctx, namespace string, vector []float32, topK int, filter map[string]any) ([]VectorResult, error)
    Delete(ctx, namespace, id string) error
    DeleteByFilter(ctx, namespace string, filter map[string]any) error
    Close() error
}
```

### 6.2 `VectorEntry`

```go
type VectorEntry struct {
    ID       string
    Vector   []float32
    Metadata map[string]any  // cache_key, provider, model, params_hash, expires_at
    Payload  string          // serialized response or stream chunks
}
```

### 6.3 Implementations

| Backend | Notes |
|---------|-------|
| In-memory (default) | Cosine similarity via brute-force, suitable for small caches |
| Qdrant (enterprise) | Production-grade ANN search, supports large caches |

Namespace isolation allows multiple cache configurations or tenants to coexist in one vector store.

---

## 7. Model Catalog

### 7.1 Purpose

`modelcatalog` provides:
- Model capabilities (supported request types, max tokens, vision support, etc.)
- Real-time pricing (USD per input/output token per model)
- Custom pricing overrides per organization
- Model parameter schemas for the UI model catalog

### 7.2 `ModelCatalog` Struct

```go
type ModelCatalog struct {
    config          *Config
    pricingData     map[string]ModelPricing  // model → pricing
    capabilitiesMap map[string]Capabilities  // model → capabilities
    overrides       []PricingOverride
    mu              sync.RWMutex
}
```

### 7.3 Pricing Sync

`modelcatalog/sync.go` periodically fetches updated pricing data from a remote source (configurable URL). This keeps cost calculations current as providers change token pricing. Sync runs on a background goroutine with configurable interval.

### 7.4 Custom Pricing Overrides

Administrators can define per-model custom pricing via the UI (`/api/governance/pricing-overrides`). These are stored in ConfigStore and loaded into the catalog's override list. When calculating cost, overrides take priority over catalog pricing.

### 7.5 `Config`

```go
type Config struct {
    PricingURL      string        // Remote pricing data URL
    SyncInterval    time.Duration // How often to fetch updates (default: 1h)
    Enabled         bool
}
```

---

## 8. MCP Catalog

`mcpcatalog/` maintains the registry of configured MCP servers (clients from Bifrost's perspective):
- Server metadata (name, URL, transport type)
- Cached tool schema lists
- Connection health state

The catalog is backed by ConfigStore (`MCPClientsTable`) and provides an in-memory view for fast tool resolution during inference.

---

## 9. Streaming Accumulator

### 9.1 Purpose

`StreamAccumulator` is a shared data structure used by multiple plugins (logging, semantic cache, Maxim, otel) to access the complete streaming response after all chunks arrive, without each plugin independently accumulating chunks.

### 9.2 `StreamAccumulator` Struct

```go
type StreamAccumulator struct {
    RequestID             string
    StartTimestamp        time.Time
    FirstChunkTimestamp   time.Time      // For TTFT calculation
    ChatStreamChunks      []*ChatStreamChunk
    ResponsesStreamChunks []*ResponsesStreamChunk
    AudioStreamChunks     []*AudioStreamChunk
    TranscriptionStreamChunks []*TranscriptionStreamChunk
    ImageStreamChunks     []*ImageStreamChunk

    // De-dup maps (prevents chunk loss on out-of-order arrival)
    ChatChunksSeen      map[int]struct{}
    // ... per type

    // Highest chunk index (metadata is in last chunk)
    MaxChatChunkIndex   int
    // ... per type

    IsComplete          bool
    FinalTimestamp      time.Time
    mu                  sync.Mutex
    refCount            atomic.Int64   // Reference counting for shared ownership
}
```

### 9.3 Chunk Types

Per streaming type:

| Type | Chunk Struct | Contents |
|------|-------------|---------|
| `chat.completion` | `ChatStreamChunk` | Delta content, finish reason, token usage, cost, log probs |
| `responses` | `ResponsesStreamChunk` | Responses API delta, usage |
| `audio.speech` | `AudioStreamChunk` | Audio binary delta |
| `audio.transcription` | `TranscriptionStreamChunk` | Text delta |
| `image.generation` | `ImageStreamChunk` | Image data chunks, per-image index |

### 9.4 `AccumulatedData`

After all chunks arrive, `StreamAccumulator` produces `AccumulatedData`:

```go
type AccumulatedData struct {
    RequestID, Model, Status   string
    Latency, TimeToFirstToken  int64
    StartTimestamp, EndTimestamp time.Time
    OutputMessage              *ChatMessage
    OutputMessages             []ResponsesMessage
    ToolCalls                  []ChatAssistantMessageToolCall
    ErrorDetails               *BifrostError
    TokenUsage                 *BifrostLLMUsage
    Cost                       *float64
    AudioOutput                *BifrostSpeechResponse
    TranscriptionOutput        *BifrostTranscriptionResponse
    ImageGenerationOutput      *BifrostImageGenerationResponse
    FinishReason               *string
    LogProbs                   *BifrostLogProbs
}
```

### 9.5 `ProcessedStreamResponse`

`ProcessedStreamResponse.ToBifrostResponse()` converts accumulated streaming data into a standard `BifrostResponse` for use by plugins that need the final response in the non-streaming format.

### 9.6 Reference Counting

`StreamAccumulator.refCount` tracks how many plugins hold a reference to the accumulator. The accumulator is freed when `refCount` reaches zero. This prevents premature garbage collection while allowing plugins to release their reference independently.

---

## 10. OAuth2

`framework/oauth2` manages OAuth2 token lifecycle for:
- Vertex AI service account credential exchange
- Enterprise SSO (Google, GitHub) for UI authentication
- MCP server OAuth2 authentication

Provides token caching with automatic refresh before expiry.

---

## 11. Encryption

`framework/encrypt` provides AES-256-GCM symmetric encryption:

**Key derivation:** Argon2id KDF applied to a passphrase to produce a 32-byte key:
```
key = Argon2id(passphrase, salt, time=1, memory=64MB, threads=4, keyLen=32)
```

**Usage:** The ConfigStore uses this to encrypt/decrypt sensitive fields (API keys, OAuth client secrets) transparently. An encryption key is configured via `config.json.encryption_key` or `env.BIFROST_ENCRYPTION_KEY`.

---

## 12. Environment Utilities

`framework/envutils` resolves `env.<VAR_NAME>` references at runtime:

```go
func Resolve(value string) string {
    if strings.HasPrefix(value, "env.") {
        return os.Getenv(strings.TrimPrefix(value, "env."))
    }
    return value
}
```

Applied at read time whenever provider API keys, credentials, or other sensitive config values are loaded from the ConfigStore.

---

## 13. Migration System

`framework/migrator` orchestrates schema migrations across ConfigStore and LogStore:

- Migrations are versioned and applied in order
- Already-applied migrations are skipped
- Migration state is tracked in a `migrations` table
- ConfigStore migrations: `configstore/migrations.go`
- LogStore migrations: `logstore/migrations.go`
- Runs on server startup before any handlers are registered

---

## 14. Plugin Loader

`framework/plugins` manages loading and lifecycle of custom (user-provided) plugins:

| Plugin Type | Loading Mechanism |
|-------------|------------------|
| Native shared object | `dlopen`-based loading (`plugin.Open()`) for Go `.so` files |
| WASM | WebAssembly runtime for sandboxed plugins |
| Built-in | Direct Go struct instantiation |

`PluginConfig.Path` points to the plugin binary. `PluginConfig.Placement` controls ordering relative to built-in plugins (`pre_builtin`, `builtin`, `post_builtin`).

The framework validates plugin interface compatibility after loading and calls `Init()` with the plugin's config JSON.

---

## 15. Tracing

`framework/tracing` bridges Bifrost's internal tracer interface with the observability plugins:

- Wraps `schemas.Tracer` interface
- Collects spans during request processing
- Calls all registered `ObservabilityPlugin.Inject(ctx, trace)` asynchronously after response is sent
- Ensures zero latency impact on the client request path

`schemas.DefaultTracer()` returns a `NoOpTracer` when no tracing backend is configured.

---

*Derived from source code analysis of `/framework/` as of 2026-04-05.*
