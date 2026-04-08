# Technical Design Document — Bifrost AI Gateway
## Project-Level Architecture Overview

**Version:** 1.0  
**Date:** 2026-04-05  
**Status:** Draft  
**References:** [api_overview.md](api_overview.md)

---

## Table of Contents

1. [Project Overview](#1-project-overview)
2. [Repository Structure](#2-repository-structure)
3. [Module Dependency Graph](#3-module-dependency-graph)
4. [System Architecture](#4-system-architecture)
5. [Request Lifecycle](#5-request-lifecycle)
6. [API Surface](#6-api-surface)
7. [Provider Model](#7-provider-model)
8. [Governance & Virtual Keys](#8-governance--virtual-keys)
9. [Plugin System](#9-plugin-system)
10. [Observability](#10-observability)
11. [MCP (Model Context Protocol)](#11-mcp-model-context-protocol)
12. [Deployment Topology](#12-deployment-topology)
13. [Performance Characteristics](#13-performance-characteristics)
14. [Configuration System](#14-configuration-system)

---

## 1. Project Overview

Bifrost is a **high-performance AI gateway** written in Go that:

- Unifies 20+ LLM providers behind a single OpenAI-compatible API
- Provides automatic failover and weighted load balancing across providers and API keys
- Supports semantic caching, governance (budgets, rate limits), MCP tool calling, and a plugin middleware pipeline
- Serves a built-in web control plane (Next.js static SPA embedded in the Go binary)
- Achieves **~11 µs overhead** per request at 5,000 RPS

**Two deployment modes:**

| Mode | Use Case |
|------|---------|
| **HTTP Gateway** (`transports/bifrost-http`) | Language-agnostic, drop-in replacement for OpenAI/Anthropic/GenAI/Bedrock SDKs |
| **Go SDK** (`core/`) | Direct Go library embedding — zero network hop |

---

## 2. Repository Structure

```
bifrost/
├── core/               # Core engine — provider implementations, routing, concurrency
│   ├── bifrost.go      # Main Bifrost struct and public API
│   ├── logger.go       # Pluggable logger interface
│   ├── utils.go        # Shared utility functions
│   ├── providers/      # Per-provider implementations (20+ providers)
│   ├── schemas/        # Shared type definitions (requests, responses, config)
│   ├── network/        # Channel-based concurrency layer
│   ├── mcp/            # MCP client management
│   └── internal/       # Internal helpers (not exported)
│
├── framework/          # Persistence and service layer
│   ├── config.go       # Top-level config struct
│   ├── configstore/    # Config persistence (file, DB, etc.)
│   ├── kvstore/        # Generic key-value store abstraction
│   ├── logstore/       # Request log storage
│   ├── vectorstore/    # Vector storage for semantic cache
│   ├── modelcatalog/   # Model registry and capabilities
│   ├── mcpcatalog/     # MCP server registry
│   ├── oauth2/         # OAuth2 token management
│   ├── streaming/      # Streaming response utilities
│   ├── tracing/        # Distributed tracing
│   ├── plugins/        # Plugin loader and lifecycle
│   ├── encrypt/        # Encryption utilities
│   ├── envutils/       # Environment variable resolution
│   └── migrator/       # Config/data migration system
│
├── plugins/            # First-party plugin implementations
│   ├── governance/     # Budget management, rate limiting, access control
│   ├── logging/        # Request logging to logstore
│   ├── semanticcache/  # Semantic similarity caching
│   ├── telemetry/      # Prometheus metrics
│   ├── otel/           # OpenTelemetry tracing
│   ├── maxim/          # Maxim observability integration
│   ├── mocker/         # Mock response injection
│   ├── litellmcompat/  # LiteLLM format compatibility shim
│   └── jsonparser/     # JSON streaming parser utilities
│
├── transports/
│   └── bifrost-http/   # HTTP gateway server (fasthttp-based)
│       ├── *.go        # Route handlers, middleware, server setup
│       └── ui/         # Embedded Next.js static SPA (built from /ui)
│
├── ui/                 # Control plane web application (Next.js 15)
│
├── tests/              # Integration and end-to-end test suites
├── docs/               # MDX documentation (deployed to docs.getbifrost.ai)
├── examples/           # Usage examples
├── helm-charts/        # Kubernetes Helm charts
├── terraform/          # Infrastructure-as-Code
└── cli/                # CLI tooling
```

---

## 3. Module Dependency Graph

```
transports/bifrost-http
    ├── framework/          (config, logstore, kvstore, plugins)
    │   ├── core/           (bifrost engine, providers, schemas)
    │   └── plugins/        (governance, logging, cache, telemetry)
    └── ui/ (embedded static assets)

core/                       (standalone — no framework dependency)
    └── providers/          (per-provider implementations)

Go module boundaries:
  - github.com/maximhq/bifrost/core       → standalone, no internal deps
  - github.com/maximhq/bifrost/framework  → depends on core
  - github.com/maximhq/bifrost/plugins/*  → depends on framework + core
  - github.com/maximhq/bifrost/transports/bifrost-http → depends on all above
```

---

## 4. System Architecture

### 4.1 High-Level Data Flow

```
Client Request
    │
    ▼
[HTTP Gateway — bifrost-http]
    │  Route matching
    │  Auth (Virtual Key / Session Cookie)
    │  Request normalization
    ▼
[Plugin Pipeline — Pre-request]
    │  governance (budget check, rate limit)
    │  semanticcache (cache lookup)
    │  logging (record request start)
    ▼
[Bifrost Core Engine]
    │  Model parsing (provider/model format)
    │  Routing rule evaluation (CEL)
    │  Provider + key selection (weighted)
    │  Channel-based concurrency control
    ▼
[Provider Implementation]
    │  Request translation (Bifrost → provider format)
    │  HTTP call to upstream provider
    │  Response translation (provider format → Bifrost)
    ▼
[Plugin Pipeline — Post-request]
    │  semanticcache (cache store)
    │  logging (record response, tokens, cost)
    │  telemetry (emit metrics)
    ▼
Client Response
```

### 4.2 Concurrency Model

Each provider key is managed by a dedicated goroutine channel pool in `core/network/`. Requests enter the pool via buffered channels with configurable:
- `concurrency` — max parallel in-flight requests per key
- `buffer_size` — request queue depth before backpressure

This provides natural flow control and prevents any single provider from being overwhelmed.

---

## 5. Request Lifecycle

### 5.1 Model Format

All inference requests use `provider/model` format:

```
openai/gpt-4o
anthropic/claude-3-5-sonnet-20241022
bedrock/anthropic.claude-3-5-sonnet-20241022-v2:0
vertex/gemini-2.0-flash
azure/<deployment-id>
ollama/llama3.2
```

### 5.2 Failover Chain

A request may specify primary + fallback targets:

```json
{
  "model": "openai/gpt-4o",
  "fallbacks": ["anthropic/claude-3-5-sonnet-20241022", "bedrock/anthropic.claude-3-sonnet"]
}
```

The engine tries each target in order on failure (5xx, timeout, rate limit). Fallback decisions are logged for observability.

### 5.3 Load Balancing

Multiple keys per provider are distributed using **weighted round-robin**. Each key has a `weight` field. The network layer tracks key health and adjusts distribution on transient errors.

---

## 6. API Surface

### 6.1 Inference Endpoints

| Group | Prefix | Description |
|-------|--------|-------------|
| Unified Bifrost API | `/v1/` | Standard inference in `provider/model` format |
| Async inference | `/v1/async/` | Submit → poll by `job_id` |
| OpenAI drop-in | `/openai/v1/` | Change `base_url` in OpenAI SDK only |
| Anthropic drop-in | `/anthropic/v1/` | Change `base_url` in Anthropic SDK only |
| Google GenAI drop-in | `/genai/v1beta/` | Change `api_endpoint` in GenAI SDK |
| AWS Bedrock drop-in | `/bedrock/` | Bedrock converse/invoke API |
| Cohere drop-in | `/cohere/v2/` | Cohere chat/embed |
| LiteLLM compat | `/litellm/` | LiteLLM routing format |
| LangChain compat | `/langchain/` | LangChain SDK format |
| PydanticAI compat | `/pydanticai/` | PydanticAI SDK format |

### 6.2 Inference Types Supported

| Type | Endpoint |
|------|---------|
| Chat completions | `POST /v1/chat/completions` |
| Text completions (legacy) | `POST /v1/completions` |
| Responses API (OpenAI) | `POST /v1/responses` |
| Embeddings | `POST /v1/embeddings` |
| Rerank | `POST /v1/rerank` |
| Text-to-speech | `POST /v1/audio/speech` |
| Speech-to-text | `POST /v1/audio/transcriptions` |
| Image generation | `POST /v1/images/generations` |
| Image edit/variation | `POST /v1/images/edits`, `/variations` |
| Video generation | `POST /v1/videos` |
| Files | `POST/GET/DELETE /v1/files` |
| Batches | `POST/GET /v1/batches` |
| Containers | `POST/GET/DELETE /v1/containers` |
| Models list | `GET /v1/models` |
| MCP tool execution | `POST /v1/mcp/tool/execute` |

### 6.3 Admin API Endpoints

| Domain | Prefix |
|--------|--------|
| Provider CRUD | `GET/POST/PUT/DELETE /api/providers` |
| Model listing | `GET /api/models`, `/api/models/details`, `/api/models/base` |
| Virtual keys | `/api/governance/virtual-keys` |
| Teams | `/api/governance/teams` |
| Customers | `/api/governance/customers` |
| Routing rules | `/api/governance/routing-rules` |
| Model configs | `/api/governance/model-configs` |
| Provider governance | `/api/governance/providers` |
| Budgets (read) | `GET /api/governance/budgets` |
| Pricing overrides | `/api/governance/pricing-overrides` |
| Logs | `GET /api/logs` (rich filter params) |
| Log histograms | `GET /api/logs/histogram/*` |
| MCP logs | `GET /api/mcp-logs` |
| Config | `GET/PUT /api/config`, `/api/proxy-config` |
| Plugins | `GET/PUT/DELETE /api/plugins/{name}` |
| MCP clients | `/api/mcp/clients`, `/api/mcp/client/{id}` |
| Session | `POST /api/session/login`, `/logout`, `GET /api/session/ws-ticket` |
| Prompt repo | `/api/prompt-repo/folders`, `/api/prompt-repo/prompts` |
| Cache | `DELETE /api/cache/clear/{requestId}` |
| Version | `GET /api/version` |

### 6.4 Infrastructure Endpoints

| Endpoint | Purpose |
|---------|---------|
| `GET /health` | Health check with component status |
| `GET /metrics` | Prometheus metrics scrape endpoint |
| `WS /ws` | WebSocket — real-time UI updates (logs, metrics) |
| `GET /mcp` | MCP server SSE endpoint for external tool clients |

### 6.5 Authentication Modes

| Mode | Mechanism | Used By |
|------|-----------|---------|
| Virtual Key | `Authorization: Bearer <vk>` or `X-BF-VK: <vk>` | API consumers |
| Direct API Key | `X-BF-API-Key: <provider_key>` | Direct key pass-through (if enabled) |
| Session Cookie | `Cookie: bifrost_session=<token>` | UI / admin API |

---

## 7. Provider Model

### 7.1 Supported Providers (20+)

| Provider | Format | Auth Method |
|----------|--------|------------|
| OpenAI | `openai/<model>` | API key |
| Anthropic | `anthropic/<model>` | API key |
| AWS Bedrock | `bedrock/<model-id>` | IAM (access key + secret, or ARN) |
| Google Vertex | `vertex/<model>` | OAuth2 / service account JSON |
| Azure OpenAI | `azure/<deployment-id>` | API key + endpoint + deployment map |
| Google Gemini | `gemini/<model>` | API key |
| Groq | `groq/<model>` | API key |
| Mistral | `mistral/<model>` | API key |
| Cohere | `cohere/<model>` | API key |
| Cerebras | `cerebras/<model>` | API key |
| Ollama | `ollama/<model>` | No key (local) |
| Hugging Face | `huggingface/<model>` | API key |
| OpenRouter | `openrouter/<model>` | API key |
| Perplexity | `perplexity/<model>` | API key |
| ElevenLabs | `elevenlabs/<model>` | API key |
| Nebius | `nebius/<model>` | API key |
| xAI | `xai/<model>` | API key |
| Parasail | `parasail/<model>` | API key |
| Replicate | `replicate/<model>` | API key |
| SGL | `sgl/<model>` | API key |
| vLLM | `vllm/<model>` | URL + model name |
| Runway | `runway/<model>` | API key |

### 7.2 Provider Configuration

Each provider is configured with:

```json
{
  "provider": "openai",
  "keys": [
    {
      "value": "sk-...",         // literal or "env.VAR_NAME"
      "models": ["gpt-4o"],      // empty = all models
      "weight": 1.0
    }
  ],
  "network_config": {
    "timeout": 30,
    "max_retries": 3
  },
  "concurrency_and_buffer_size": {
    "concurrency": 100,
    "buffer_size": 1000
  }
}
```

API key values accept `env.VARIABLE_NAME` references — the gateway resolves them from the runtime environment, avoiding plaintext secret storage.

---

## 8. Governance & Virtual Keys

### 8.1 Virtual Key Hierarchy

```
Virtual Key
  ├── provider_configs: [{provider, weight, allowed_models}]
  ├── budget: {max_limit, reset_duration, calendar_aligned}
  └── rate_limit: {token_max_limit, request_max_limit, reset_durations}
```

Virtual keys act as logical proxies that route to real provider keys. A single virtual key can span multiple providers with weighted distribution, enabling per-key routing and budget control without exposing real API keys to consumers.

### 8.2 Governance Hierarchy

```
Global limits
  └── Team limits
        └── Customer limits
              └── Virtual Key limits
```

Budget and rate limits are enforced at each level. Exceeding any limit returns HTTP 429.

### 8.3 Routing Rules (CEL)

Routing rules use **Common Expression Language (CEL)** to match requests:

```json
{
  "name": "prod-gpt4-routing",
  "cel_expression": "model == 'gpt-4o'",
  "targets": [
    {"provider": "openai", "weight": 0.7},
    {"provider": "anthropic", "model": "claude-3-5-sonnet-20241022", "weight": 0.3}
  ],
  "priority": 10
}
```

CEL fields available: `model`, request headers, metadata, user attributes. Rules are evaluated top-down by priority.

---

## 9. Plugin System

Plugins are middleware that intercept the request/response pipeline. Each plugin implements a standard interface and can:
- Inspect and modify request parameters before forwarding
- Inspect and modify response before returning to client
- Record side effects (logs, metrics, cache entries)
- Short-circuit the pipeline (e.g., return cached response, block on budget)

### 9.1 First-Party Plugins

| Plugin | Purpose | Enterprise-only |
|--------|---------|----------------|
| `governance` | Budget checks, rate limiting, access control | No |
| `logging` | Request/response logging to logstore | No |
| `semanticcache` | Semantic similarity cache lookup/store | No |
| `telemetry` | Prometheus metrics emission | No |
| `otel` | OpenTelemetry distributed tracing | No |
| `maxim` | Maxim AI observability integration | No |
| `mocker` | Return mock responses for testing | No |
| `litellmcompat` | Normalize LiteLLM format to Bifrost format | No |
| `jsonparser` | JSON streaming parse utilities | No |

### 9.2 Plugin Lifecycle

```
server start → plugin.Init(config) → register with pipeline
request      → plugin.PreProcess(req) → [core] → plugin.PostProcess(req, resp)
server stop  → plugin.Shutdown()
```

Plugins can be enabled/disabled at runtime via `PUT /api/plugins/{name}` without restarting the server.

---

## 10. Observability

### 10.1 Request Logging

Every request is logged with:
- Timestamp, provider, model, virtual key ID
- Tokens (prompt, completion, total)
- Cost (computed from pricing catalog or custom pricing overrides)
- Latency (TTFB, total)
- Status (success, error type)
- Request/response bodies (configurable retention)

Logs are queryable via `GET /api/logs` with rich filters: provider, model, status, time range, virtual key, content search, min/max latency, min/max cost.

### 10.2 Histograms & Analytics

Pre-aggregated time-series histograms available for:
- Request counts, token usage, cost, latency — over time
- Breakdown by provider and model
- Top model rankings

### 10.3 Prometheus Metrics

`GET /metrics` exposes standard Prometheus format. Includes request counters, latency histograms, error rates, and provider health gauges.

### 10.4 Distributed Tracing

OpenTelemetry (OTLP) traces can be emitted via the `otel` plugin. Each request produces a span with provider, model, and token attributes.

### 10.5 WebSocket Real-time Feed

The UI connects to `WS /ws` with a short-lived ticket (`POST /api/session/ws-ticket`). The server pushes log events and metric updates in real time. Message format: `{ type: string, payload: any }`.

---

## 11. MCP (Model Context Protocol)

Bifrost functions as both an **MCP client** (calling external MCP servers on behalf of models) and an **MCP server** (exposing tools to external MCP clients via SSE at `GET /mcp`).

### 11.1 MCP Client Flow

1. MCP server registered via `POST /api/mcp/client`
2. Bifrost maintains connection to the external MCP server
3. When a chat completion includes `tools`, Bifrost resolves tool schemas from registered MCP servers
4. Tool call results are injected back into the conversation
5. MCP calls are logged separately (`GET /api/mcp-logs`)

### 11.2 MCP Authentication

External MCP servers support: Bearer token, API key header, OAuth 2.0.

---

## 12. Deployment Topology

### 12.1 Single Node (Default)

```
Docker / npx / binary
  └── bifrost-http (port 8080)
        ├── /data/bifrost.db   (SQLite — config, logs, governance)
        └── ui/ (embedded SPA)
```

### 12.2 Multi-Node Enterprise (Cluster)

Multiple Bifrost nodes with shared storage backend. Node health and load distribution visible in the UI cluster view.

### 12.3 Container / Kubernetes

Official Helm charts in `helm-charts/`. Terraform configurations in `terraform/`.

### 12.4 Startup Options

| Method | Command |
|--------|---------|
| NPX | `npx -y @maximhq/bifrost` |
| Docker | `docker run -p 8080:8080 maximhq/bifrost` |
| Docker with persistence | `docker run -p 8080:8080 -v $(pwd)/data:/app/data maximhq/bifrost` |
| Go binary | Build from `transports/bifrost-http` |

---

## 13. Performance Characteristics

- **Overhead:** ~11 µs per request at 5,000 RPS (benchmarked)
- **Channel-based back-pressure:** Configurable `concurrency` + `buffer_size` per provider key prevent overloading upstream APIs
- **Semantic cache:** Reduces upstream calls and latency for semantically similar repeated queries
- **Streaming:** Server-Sent Events (SSE) for all streaming endpoints — proxy passes chunks through with minimal buffering
- **Static binary:** Next.js UI is embedded at build time; no separate web server needed

---

## 14. Configuration System

### 14.1 Config Hierarchy

```
Runtime config (persisted in configstore — typically SQLite)
  └── Provider configs
  └── Plugin configs
  └── Proxy settings (timeouts, buffer sizes, concurrency)
  └── Client settings (CORS origins, auth modes)
  └── Governance settings
```

### 14.2 Config Update Flow

1. Admin updates config via UI or `PUT /api/config`
2. `framework/configstore` persists the change
3. Bifrost core hot-reloads the affected components (no restart required for most changes)
4. UI shows sync status indicator until propagation is confirmed

### 14.3 Environment Variable References

Provider API keys can be specified as `env.VAR_NAME` rather than literal values. Resolved at request time from the process environment. This supports HashiCorp Vault sidecars and other secret injection patterns.

### 14.4 config.schema.json

The transport layer ships a `config.schema.json` (JSON Schema format) describing all valid configuration fields and types, used by the UI form renderer and for config validation.

---

## Module TDDs

Detailed technical design for each module is documented separately:

| Module | Document |
|--------|---------|
| Core engine | [TDD-core.md](TDD-core.md) |
| Framework | [TDD-framework.md](TDD-framework.md) |
| Plugins | [TDD-plugins.md](TDD-plugins.md) |
| Transports (HTTP gateway) | [TDD-transports.md](TDD-transports.md) |
| UI control plane | [../ui/specs/TDD.md](../ui/specs/TDD.md) |

---

*Derived from source code and documentation analysis at `/bifrost/` as of 2026-04-05.*
