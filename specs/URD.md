# User Requirements Document (URD)
## Bifrost AI Gateway

**Version:** 1.0  
**Date:** 2026-04-05  
**Status:** Draft  
**References:** [SRS.md](SRS.md), [TDD-project.md](TDD-project.md)

---

## Table of Contents

1. [Purpose & Scope](#1-purpose--scope)
2. [User Classes](#2-user-classes)
3. [API Consumer Requirements](#3-api-consumer-requirements)
4. [Platform Administrator Requirements](#4-platform-administrator-requirements)
5. [Developer / Team Lead Requirements](#5-developer--team-lead-requirements)
6. [Go SDK Integrator Requirements](#6-go-sdk-integrator-requirements)
7. [DevOps / Infrastructure Operator Requirements](#7-devops--infrastructure-operator-requirements)
8. [Viewer Requirements](#8-viewer-requirements)
9. [Cross-Role Requirements](#9-cross-role-requirements)

---

## 1. Purpose & Scope

This document captures requirements from the perspective of each **user of Bifrost** — what they need to do, why, and what success looks like. It complements the SRS (what the system must do) by grounding requirements in real user goals.

---

## 2. User Classes

| Class | Who They Are | Primary Goal |
|-------|-------------|-------------|
| **API Consumer** | Application code, AI frameworks (LangChain, LiteLLM, etc.) | Call any LLM through one stable endpoint without managing providers |
| **Platform Admin** | DevOps/ML Platform engineer who deploys and configures Bifrost | Configure providers, governance, plugins, and monitor the system |
| **Developer / Team Lead** | Developer building features with LLMs | View usage, manage keys for their service, debug failures |
| **Go SDK Integrator** | Go developer embedding Bifrost as a library | Add provider routing/failover/caching to a Go application |
| **DevOps / Infra Operator** | Engineer running Bifrost in production (Docker/K8s) | Deploy, scale, observe, and maintain the gateway |
| **Viewer** | Stakeholder needing visibility without edit access | Monitor cost, usage, and system health |

---

## 3. API Consumer Requirements

*These are the needs of code calling the inference API.*

### 3.1 Drop-in Replacement

**As an** application using the OpenAI SDK,  
**I want** to change only the `base_url` to point at Bifrost,  
**so that** I get failover, load balancing, and governance without any other code changes.

**Acceptance criteria:**
- `POST /openai/v1/chat/completions` accepts the exact same request format as `api.openai.com/v1/chat/completions`
- The response format is byte-for-byte compatible with the OpenAI SDK's expectations
- Same applies for Anthropic, Google GenAI, AWS Bedrock, and Cohere SDKs

### 3.2 Unified Multi-Provider API

**As an** application integrating multiple LLM providers,  
**I want** a single endpoint that routes to any provider using `provider/model` format,  
**so that** I don't have to manage multiple base URLs, auth schemes, or SDK clients.

**Acceptance criteria:**
- `POST /v1/chat/completions` with `"model": "anthropic/claude-3-5-sonnet-20241022"` routes correctly
- Response schema is consistent regardless of which provider handled the request
- Supports chat, embeddings, images, audio, video, rerank, files, and batches

### 3.3 Automatic Failover

**As an** application with SLA requirements,  
**I want** to specify fallback providers in my request,  
**so that** my request succeeds even if the primary provider is unavailable.

**Acceptance criteria:**
- Adding `"fallbacks": ["anthropic/claude-3-5-sonnet-20241022"]` to any request enables automatic retry on 5xx/timeout
- Fallover is transparent — the same response schema is returned regardless of which provider handled it
- Fallback attempts are reflected in the `x-bf-fallback-index` response header

### 3.4 Streaming Support

**As an** application streaming LLM responses,  
**I want** the proxy to forward SSE chunks with minimal added latency,  
**so that** my users see a fast, responsive typing experience.

**Acceptance criteria:**
- `"stream": true` works on all inference endpoints
- First token arrives within 50ms of the upstream provider sending it (exclusive of network)
- `data: [DONE]` is forwarded correctly

### 3.5 Async Inference

**As an** application running batch or long-horizon inference jobs,  
**I want** to submit requests asynchronously and poll for results,  
**so that** I don't hold open HTTP connections for long-running calls.

**Acceptance criteria:**
- `POST /v1/async/chat/completions` returns `{ job_id }` immediately with HTTP 202
- `GET /v1/async/chat/completions/{job_id}` returns `{ status, result }` where status is `pending`, `completed`, or `failed`
- Jobs are cleaned up after configurable TTL

### 3.6 Virtual Key Authentication

**As an** application consumer,  
**I want** to use a virtual key for authentication,  
**so that** I don't need access to raw provider API keys and my usage is tracked and rate-limited.

**Acceptance criteria:**
- `Authorization: Bearer <virtual_key>` is accepted on all inference endpoints
- Requests beyond the VK's budget or rate limit return HTTP 429 with a clear message
- The virtual key value is opaque to the consumer — they cannot derive the underlying provider key

---

## 4. Platform Administrator Requirements

*These are the needs of the engineer who deploys and configures Bifrost.*

### 4.1 Provider Configuration

**As an** admin,  
**I want** to add, update, and remove LLM provider configurations through the UI,  
**so that** I can onboard new providers and rotate API keys without restarting the service.

**Acceptance criteria:**
- Adding a provider through the UI is reflected in routing within 5 seconds
- API keys can be specified as `env.VAR_NAME` references to avoid storing secrets in the database
- After saving, API keys are shown only in redacted form (`xxxx****xxxx`)
- Azure, Vertex, and Bedrock providers have their custom credential fields (deployment maps, service account JSON, IAM keys)

### 4.2 Virtual Key & Budget Management

**As an** admin,  
**I want** to create virtual API keys with per-provider routing weights and budget limits,  
**so that** I can control which teams spend on which providers and prevent cost overruns.

**Acceptance criteria:**
- Virtual keys can target multiple providers with weighted distribution
- Budget can be set in USD with monthly/weekly/daily/hourly reset periods
- Exceeding budget returns HTTP 429 immediately
- Budget usage is visible in real-time on the virtual key detail page
- Virtual key value is shown only once on creation

### 4.3 Routing Rules

**As an** admin,  
**I want** to create routing rules using CEL expressions,  
**so that** I can dynamically route specific models or request patterns to different providers.

**Acceptance criteria:**
- CEL expressions can match on model name, request headers, metadata, and user attributes
- Rules have a configurable priority and are evaluated top-down
- The UI provides a visual query builder for non-technical admins
- Invalid CEL expressions are rejected with a clear error before saving
- Rule changes take effect immediately without restart

### 4.4 Plugin Management

**As an** admin,  
**I want** to enable/disable plugins (governance, caching, logging, telemetry) through the UI,  
**so that** I can tune the pipeline without editing config files or restarting.

**Acceptance criteria:**
- Plugin enable/disable takes effect without restart
- Each plugin's configuration is editable through a form in the UI
- Plugin status (active/error/disabled) is visible on the plugins page

### 4.5 MCP Server Registry

**As an** admin,  
**I want** to register and manage external MCP tool servers,  
**so that** LLM conversations can use external tools without per-request configuration.

**Acceptance criteria:**
- MCP clients can be added with name, URL, transport type, and auth credentials
- Connection health status is visible per MCP server
- Tools from each server are browsable in the registry
- Reconnect can be triggered from the UI

### 4.6 Authentication Setup

**As an** admin deploying Bifrost in a team environment,  
**I want** to enable username/password authentication for the control plane,  
**so that** only authorized users can view logs or change configuration.

**Acceptance criteria:**
- Auth is disabled by default for zero-friction solo setup
- Enabling auth requires `admin_username` and `admin_password` in config
- Sessions last 30 days and survive page refresh
- Auth enforcement on inference endpoints is independently configurable

---

## 5. Developer / Team Lead Requirements

*These are the needs of developers using Bifrost day-to-day.*

### 5.1 Request Log Search

**As a** developer debugging an LLM integration,  
**I want** to search request logs with filters (provider, model, status, time range, content),  
**so that** I can find and inspect a specific failed request quickly.

**Acceptance criteria:**
- Log search returns results within 300ms for typical filter combinations
- Each log entry shows: timestamp, provider, model, tokens, cost, latency, status
- Clicking a log entry shows the full request and response bodies
- Logs can be filtered by my virtual key to see only my requests

### 5.2 Real-time Log Feed

**As a** developer actively testing an integration,  
**I want** to see new requests appear in the log view as they happen,  
**so that** I can watch the live effect of my code changes.

**Acceptance criteria:**
- New log entries appear in the table within 500ms of the request completing
- The real-time feed reconnects automatically if the WebSocket drops

### 5.3 Usage Analytics

**As a** team lead,  
**I want** to view charts of request volume, token usage, cost, and latency over time,  
**so that** I can track trends and identify cost regressions.

**Acceptance criteria:**
- Dashboard shows requests/min, error rate, and latency updated in real time
- Histogram charts show trends over selectable time windows
- Breakdown by provider and model is available

### 5.4 Virtual Key Self-Service

**As a** developer,  
**I want** to create and manage my own virtual API key with a rate limit,  
**so that** I can use Bifrost from my application without needing an admin for every key rotation.

**Acceptance criteria:**
- Creating a key requires only name + optional rate limit/budget
- The key value is shown once on creation with a copy button
- Keys can be revoked immediately

---

## 6. Go SDK Integrator Requirements

*These are the needs of a Go developer embedding Bifrost as a library.*

### 6.1 Zero-Dependency Embedding

**As a** Go developer,  
**I want** to import `github.com/maximhq/bifrost/core` and call `bifrost.Init()`,  
**so that** I get provider routing without running a separate process.

**Acceptance criteria:**
- `core/` has no dependency on `framework/` or `transports/`
- `bifrost.Init(ctx, config)` returns a `*Bifrost` ready to accept requests
- A minimal working example requires < 20 lines of Go

### 6.2 Pluggable Components

**As a** Go SDK user,  
**I want** to supply my own `Account`, `Logger`, `Tracer`, `KeySelector`, and `KVStore` implementations,  
**so that** Bifrost integrates with my existing infrastructure.

**Acceptance criteria:**
- All injectable dependencies are Go interfaces defined in `core/schemas`
- Default implementations are provided for all injectable components
- Custom plugins can be registered as `LLMPlugin` or `MCPPlugin` implementations

### 6.3 Concurrent Request Handling

**As a** Go SDK user serving concurrent users,  
**I want** Bifrost to handle concurrent requests safely with configurable per-provider concurrency,  
**so that** I can tune throughput without managing goroutines myself.

**Acceptance criteria:**
- `BifrostConfig.ConcurrencyAndBufferSize` controls max parallel requests per provider key
- All internal data structures are safe for concurrent use
- `DropExcessRequests` controls behavior when queues fill

---

## 7. DevOps / Infrastructure Operator Requirements

### 7.1 Zero-Config Startup

**As an** operator,  
**I want** to start Bifrost with a single command (`npx -y @maximhq/bifrost` or `docker run maximhq/bifrost`),  
**so that** I can evaluate and prototype without writing configuration files.

**Acceptance criteria:**
- The binary starts and serves on `:8080` with no required arguments
- The UI is accessible at `http://localhost:8080` immediately
- First provider can be added through the UI

### 7.2 Persistent Configuration

**As an** operator running Bifrost in Docker,  
**I want** all configuration to persist across container restarts by mounting a volume,  
**so that** I don't lose provider and governance setup on every deploy.

**Acceptance criteria:**
- Mounting `-v $(pwd)/data:/app/data` persists SQLite DB and config
- On restart, all providers, virtual keys, and plugins are restored from the DB

### 7.3 Health Check

**As an** operator using container orchestration,  
**I want** a health endpoint I can configure as a liveness/readiness probe,  
**so that** the orchestrator restarts unhealthy instances.

**Acceptance criteria:**
- `GET /health` returns HTTP 200 with `{ "status": "ok", "components": { "db_pings": "ok" } }` when healthy
- Returns non-200 when the database is unreachable

### 7.4 Prometheus Metrics

**As an** operator running a Prometheus/Grafana stack,  
**I want** to scrape Bifrost metrics from `/metrics`,  
**so that** I can build dashboards and alerts for the gateway.

**Acceptance criteria:**
- `/metrics` returns standard Prometheus text format
- Metrics include: request counts, latency histograms, token usage, error rates, cache hit rates
- All metrics have `provider` and `model` labels for drill-down

### 7.5 Kubernetes Deployment

**As an** operator deploying to Kubernetes,  
**I want** an official Helm chart,  
**so that** I can deploy Bifrost with GitOps-standard tooling.

**Acceptance criteria:**
- Helm chart in `helm-charts/` supports configuring replicas, resources, persistence, and ingress
- Chart is available on Artifact Hub

### 7.6 Encryption at Rest

**As an** operator with security requirements,  
**I want** to configure a passphrase that encrypts all sensitive fields in the database,  
**so that** a database dump does not expose API keys.

**Acceptance criteria:**
- Setting `encryption_key` or `BIFROST_ENCRYPTION_KEY` env var enables transparent AES-256-GCM encryption
- Encrypted databases cannot be read without the correct key
- The key is never stored in the database

---

## 8. Viewer Requirements

### 8.1 Read-Only Dashboard Access

**As a** business stakeholder,  
**I want** to view the cost and usage dashboard without the ability to change configuration,  
**so that** I can monitor LLM spend without risk of accidental changes.

**Acceptance criteria:**
- The dashboard shows real-time metrics: requests/min, total cost, error rate, top models
- Viewer cannot access provider config, governance, or plugin pages
- Dashboard is available without requiring technical expertise

### 8.2 Cost Visibility

**As a** stakeholder tracking AI infrastructure spend,  
**I want** to see cost breakdowns by provider, model, team, and time period,  
**so that** I can report on LLM costs and identify optimization opportunities.

**Acceptance criteria:**
- Cost histograms show spend over time at daily/weekly/monthly granularity
- Cost is broken down by provider and model
- Custom pricing overrides allow accurate cost reporting when negotiated rates differ from defaults

---

## 9. Cross-Role Requirements

These requirements apply to all user classes.

### 9.1 Dark / Light Theme

**As any** UI user,  
**I want** the UI to respect my system theme preference and allow manual override,  
**so that** I can work comfortably in any lighting environment.

### 9.2 Responsive Tables

**As any** UI user,  
**I want** data tables to automatically adjust page size based on my screen height,  
**so that** I see as many rows as fit without scrolling unnecessarily.

### 9.3 Fast UI Navigation

**As any** UI user,  
**I want** navigation between pages to be instant with no full-page reload,  
**so that** the control plane feels like a native app, not a website.

### 9.4 Actionable Errors

**As any** user of the inference API,  
**I want** error responses to include a machine-readable `error.type` and a human-readable `error.message`,  
**so that** I can programmatically handle different error types and display meaningful messages.

**Error format:**
```json
{
  "error": {
    "type": "invalid_request_error",
    "message": "model should be in provider/model format",
    "code": "invalid_model",
    "param": "model"
  }
}
```

### 9.5 Enterprise Feature Visibility

**As any** UI user on an OSS deployment,  
**I want** to see enterprise features clearly labeled and shown with an upgrade prompt,  
**so that** I know what's available and how to unlock it, rather than seeing errors or broken pages.

---

*This URD is derived from user personas and source code analysis of Bifrost as of 2026-04-05.*
