# Software Requirements Specification (SRS)
## Bifrost AI Gateway

**Version:** 2.0  
**Date:** 2026-04-08  
**Status:** Draft  
**Previous Version:** 1.0 (2026-04-05)  
**Change Requests:** [CR-ENT-001](crs/CR-ENT-001-PRD-enterprise-edition.md), [CR-ENT-002](crs/CR-ENT-002-URD-enterprise-edition.md)  
**Gap Analysis:** [enterprise-missing-features.md](bugs/enterprise-missing-features.md)

### Changelog

| Version | Date | Changes |
|---------|------|---------|
| 1.0 | 2026-04-05 | Initial SRS — OSS baseline |
| 2.0 | 2026-04-08 | Enterprise Edition — 14 new feature sets: RBAC, Audit Logs, Guardrails, PII Redactor, SSO/SCIM, Adaptive Routing, Multi-node Clustering, Alert Channels, HashiCorp Vault, Large Payload, MCP Tool Groups, User Groups, Data Connectors, License Enforcement. New user classes: Security Officer, Compliance Auditor, IdP Administrator. New non-functional requirements for enterprise scale. |

---

## Table of Contents

1. [Introduction](#1-introduction)
2. [Overall Description](#2-overall-description)
3. [Functional Requirements](#3-functional-requirements)
   - 3.1 Unified Inference API
   - 3.2 Provider Management
   - 3.3 Failover & Load Balancing
   - 3.4 Virtual Keys & Governance
   - 3.5 Observability
   - 3.6 Semantic Caching
   - 3.7 MCP Integration
   - 3.8 Plugin System
   - 3.9 Configuration Management
   - 3.10 Control Plane UI
   - 3.11 Authentication & Session
   - **3.12 Role-Based Access Control (Enterprise)** ✨
   - **3.13 Audit Logs (Enterprise)** ✨
   - **3.14 Content Guardrails (Enterprise)** ✨
   - **3.15 PII Detection & Redaction (Enterprise)** ✨
   - **3.16 SSO & SCIM Provisioning (Enterprise)** ✨
   - **3.17 Adaptive Routing (Enterprise)** ✨
   - **3.18 Multi-node Clustering (Enterprise)** ✨
   - **3.19 Alert Channels (Enterprise)** ✨
   - **3.20 HashiCorp Vault Integration (Enterprise)** ✨
   - **3.21 Large Payload Optimization (Enterprise)** ✨
   - **3.22 MCP Tool Groups (Enterprise)** ✨
   - **3.23 User Groups (Enterprise)** ✨
   - **3.24 Data Connectors (Enterprise)** ✨
   - **3.25 License Enforcement (Enterprise)** ✨
4. [Non-Functional Requirements](#4-non-functional-requirements)
5. [System Architecture Constraints](#5-system-architecture-constraints)
6. [External Interface Requirements](#6-external-interface-requirements)
7. [Data Requirements](#7-data-requirements)
8. [Constraints & Assumptions](#8-constraints--assumptions)

---

## 1. Introduction

### 1.1 Purpose

This document specifies the software requirements for **Bifrost**, a high-performance AI gateway that unifies access to 20+ LLM providers through a single OpenAI-compatible API. It covers the complete system: the Go backend gateway, the Next.js control plane UI, and the plugin framework.

**v2.0 scope extension:** This revision adds enterprise-grade requirements for organizations with 10,000+ users, including identity management (RBAC, SCIM, SSO), compliance controls (Audit Logs, PII Redaction, Guardrails), operational resilience (Multi-node Clustering, Adaptive Routing, Alert Channels), secret management (HashiCorp Vault), and data integration (Data Connectors).

### 1.2 Scope

Bifrost provides:
- A unified proxy for all major LLM providers (OpenAI, Anthropic, Google Vertex, AWS Bedrock, Azure, and 15+ more)
- Automatic failover, weighted load balancing, and semantic caching
- Governance: virtual API keys, hierarchical budgets, rate limits, routing rules
- Observability: real-time logs, metrics (Prometheus), distributed tracing (OpenTelemetry)
- MCP (Model Context Protocol) gateway for tool-augmented AI workflows
- A web control plane (UI) for visual configuration and monitoring
- A plugin system for custom request/response middleware
- **[v2.0]** RBAC with 5 predefined roles enforced across all API endpoints
- **[v2.0]** Immutable audit logs for all administrative actions
- **[v2.0]** Content guardrails (keyword / regex / AI classifier) on requests and responses
- **[v2.0]** PII detection and redaction before log storage
- **[v2.0]** SSO (SAML 2.0 / OIDC) and SCIM 2.0 for enterprise IdP integration
- **[v2.0]** Adaptive routing based on real-time provider latency, error rate, and cost
- **[v2.0]** Multi-node clustering with distributed state and cross-node cache invalidation
- **[v2.0]** Alert channels (Slack, PagerDuty, Webhook, Email) for operational events
- **[v2.0]** HashiCorp Vault integration for runtime secret injection
- **[v2.0]** Data connectors for log export to BigQuery, Datadog, S3, and Elasticsearch
- **[v2.0]** Enterprise license enforcement gating all enterprise-only features

### 1.3 Definitions

| Term | Definition |
|------|-----------|
| LLM | Large Language Model |
| MCP | Model Context Protocol — AI tool integration protocol |
| VK | Virtual Key — proxy API key managed by Bifrost |
| CEL | Common Expression Language — used in routing rules |
| RBAC | Role-Based Access Control |
| SSE | Server-Sent Events — HTTP streaming protocol |
| Semantic cache | Cache using vector similarity to match equivalent queries |
| Provider | An LLM API vendor (OpenAI, Anthropic, etc.) |
| Plugin | Middleware component extending Bifrost functionality |
| RTK Query | Redux Toolkit Query — UI API client library |
| SCIM | System for Cross-domain Identity Management — IdP provisioning protocol |
| SAML | Security Assertion Markup Language — SSO federation standard |
| OIDC | OpenID Connect — identity layer on top of OAuth 2.0 |
| JIT | Just-in-Time provisioning — auto-create user on first SSO login |
| PII | Personally Identifiable Information |
| Guardrail | Content safety rule applied to LLM requests/responses |
| Adaptive routing | Dynamic provider selection based on real-time performance metrics |
| Cluster node | A single Bifrost instance in a multi-node deployment |
| Data connector | Integration that exports inference logs to external storage (BQ, Datadog, S3) |
| Vault | HashiCorp Vault — secrets management platform |
| License | Enterprise license JWT that gates access to enterprise features |
| Tool Group | Named, reusable set of MCP tools assignable to multiple Virtual Keys |
| User Group | Named set of users sharing RBAC roles |
| Audit log | Immutable record of an administrative action |

---

## 2. Overall Description

### 2.1 Product Perspective

```
API Consumers (apps, developers, AI frameworks)
         │
         ▼
[Bifrost Gateway — HTTP :8080]
         │
  ┌──────┴──────┐
  │  Plugin     │
  │  Pipeline   │ ← governance, cache, logging, telemetry
  └──────┬──────┘
         │
  ┌──────┴──────┐
  │  Core       │
  │  Engine     │ ← routing, key selection, concurrency
  └──────┬──────┘
         │
  ┌──────┴──────────────────────────┐
  │  Providers                      │
  │  OpenAI · Anthropic · Bedrock   │
  │  Vertex · Azure · Groq · ...    │
  └─────────────────────────────────┘
         │
  ┌──────┴──────┐
  │  Control    │
  │  Plane UI   │ ← Next.js SPA served on same port
  └─────────────┘
```

### 2.2 User Classes

| Class | Description | Primary Interactions | Since |
|-------|-------------|---------------------|-------|
| API Consumer | Application/developer calling LLM APIs | Inference endpoints (`/v1/*`) | v1.0 |
| Platform Admin | Configures providers, governance, plugins | UI control plane + Admin API | v1.0 |
| Developer | Reads logs, manages virtual keys | UI logs, keys pages | v1.0 |
| Viewer | Read-only monitoring | Dashboard, logs (read) | v1.0 |
| Go SDK User | Embeds Bifrost directly in Go code | `core/` package API | v1.0 |
| **Security Officer** | CISO / Security Engineer — enforces access, compliance, guardrails | RBAC, Audit Logs, Guardrails, PII, SSO, Vault pages | **v2.0** |
| **Compliance Auditor** | Internal/external auditor querying evidence | Audit log export, PII reports, Guardrail violation reports | **v2.0** |
| **IdP Administrator** | IT Identity team managing user lifecycle | SCIM config, SSO settings, User Groups | **v2.0** |

### 2.3 Operating Environment

| Component | Environment | Since |
|-----------|-------------|-------|
| Gateway | Go 1.24+, Linux/macOS/Windows | v1.0 |
| Persistence | SQLite (default) or PostgreSQL | v1.0 |
| Control Plane UI | Next.js 15, React 19, TypeScript 5, Node.js 20+ | v1.0 |
| Container | Docker (Alpine), Kubernetes via Helm | v1.0 |
| Client browsers | Chrome 120+, Firefox 120+, Safari 17+ | v1.0 |
| **Redis** | Optional — required for cluster mode cross-node pub/sub (Redis 7+) | **v2.0** |
| **HashiCorp Vault** | Optional — required when Vault secret references are used (Vault 1.12+) | **v2.0** |
| **IdP (SAML/OIDC)** | Optional — Okta, Azure AD, Google Workspace, Keycloak, ADFS | **v2.0** |

---

## 3. Functional Requirements

### 3.1 Unified Inference API

| ID | Requirement |
|----|-------------|
| INF-01 | The system shall accept chat completion requests at `POST /v1/chat/completions` in OpenAI-compatible format |
| INF-02 | The system shall support streaming responses via Server-Sent Events (SSE) |
| INF-03 | The model field shall accept `provider/model` format (e.g., `openai/gpt-4o`) |
| INF-04 | The system shall support text completions, embeddings, rerank, TTS, STT, image generation, video, files, batches, and containers |
| INF-05 | The system shall support async (submit/poll) mode for all inference types via `/v1/async/*` |
| INF-06 | The system shall expose drop-in replacement endpoints for OpenAI, Anthropic, Google GenAI, AWS Bedrock, and Cohere SDKs |
| INF-07 | The system shall support LiteLLM, LangChain, and PydanticAI framework endpoint formats |
| INF-08 | The system shall add ≤ 20 µs overhead per request at up to 5,000 RPS |

### 3.2 Provider Management

| ID | Requirement |
|----|-------------|
| PROV-01 | Admins shall add, update, and remove provider configurations via API and UI |
| PROV-02 | Each provider shall support multiple API keys with configurable weights for load balancing |
| PROV-03 | Provider API keys shall accept `env.VAR_NAME` references to avoid plaintext storage |
| PROV-04 | Provider API keys shall be redacted in API responses after initial creation |
| PROV-05 | Each provider shall have configurable `concurrency` and `buffer_size` per key |
| PROV-06 | Each provider shall have configurable network settings: timeout, max retries |
| PROV-07 | The system shall support global and per-provider HTTP proxy configuration |
| PROV-08 | Provider changes shall take effect without server restart |

### 3.3 Failover & Load Balancing

| ID | Requirement |
|----|-------------|
| FAIL-01 | Clients shall specify a `fallbacks` array of `provider/model` strings for automatic failover |
| FAIL-02 | On a 5xx, timeout, or rate limit error, the system shall automatically retry with the next fallback |
| FAIL-03 | Load balancing across multiple keys for the same provider shall use weighted random selection |
| FAIL-04 | Plugin-generated errors shall control fallback behavior via `AllowFallbacks` flag |
| FAIL-05 | Fallback attempts shall be recorded in logs and metrics |

### 3.4 Virtual Keys & Governance

| ID | Requirement |
|----|-------------|
| GOV-01 | Admins shall create virtual API keys that proxy to real provider keys |
| GOV-02 | Virtual keys shall support per-provider routing with configurable weights |
| GOV-03 | Virtual keys shall support budget limits (USD) with configurable reset periods |
| GOV-04 | Virtual keys shall support token and request rate limits per time window |
| GOV-05 | Virtual keys shall support model allowlists (only permitted models can be requested) |
| GOV-06 | Budget and rate limit enforcement shall be hierarchical: team → customer → virtual key |
| GOV-07 | Exceeding any limit shall return HTTP 429 |
| GOV-08 | Virtual key values shall be shown only once on creation |
| GOV-09 | Admins shall create teams and customers as organizational groupings |
| GOV-10 | Routing rules using CEL expressions shall override provider/model selection |
| GOV-11 | Routing rules shall evaluate conditions on model, headers, metadata, and user attributes |
| GOV-12 | Routing rules shall have priority order and be evaluated top-down |

### 3.5 Observability

| ID | Requirement |
|----|-------------|
| OBS-01 | Every inference request shall be logged with: timestamp, provider, model, tokens, cost, latency, status |
| OBS-02 | Logs shall be queryable with filters: provider, model, status, time range, VK, cost range, latency range, content search |
| OBS-03 | Logs shall support paginated retrieval with configurable page size |
| OBS-04 | Pre-aggregated histograms shall be available for: request counts, tokens, cost, latency (over time and by provider) |
| OBS-05 | The system shall expose Prometheus metrics at `GET /metrics` |
| OBS-06 | The system shall support OpenTelemetry trace export to configurable OTLP collectors |
| OBS-07 | Real-time log and metric updates shall be delivered to connected UI clients via WebSocket |
| OBS-08 | MCP tool call logs shall be separately queryable |
| OBS-09 | Log retention shall be configurable (default: 365 days) |

### 3.6 Semantic Caching

| ID | Requirement |
|----|-------------|
| CACHE-01 | The system shall cache responses using exact hash matching (Tier 1) and semantic similarity (Tier 2) |
| CACHE-02 | Semantic similarity shall use configurable embedding models and cosine similarity threshold |
| CACHE-03 | Cache entries shall have configurable TTL (default: 5 minutes) |
| CACHE-04 | Cache hits shall be labeled by type (direct/semantic) in logs and metrics |
| CACHE-05 | Streaming responses shall be cached and replayed as synthetic streams on cache hit |
| CACHE-06 | Cache keys shall be configurable per-request and globally |

### 3.7 MCP Integration

| ID | Requirement |
|----|-------------|
| MCP-01 | Admins shall register external MCP servers via the API and UI |
| MCP-02 | Bifrost shall act as an MCP client, executing tools on behalf of LLM conversations |
| MCP-03 | Bifrost shall act as an MCP server, exposing its tools to external MCP clients |
| MCP-04 | MCP tool call depth shall be configurable (default: 10 hops) |
| MCP-05 | MCP tool execution timeout shall be configurable (default: 30 seconds) |
| MCP-06 | MCP servers shall support authentication: Bearer token, API key, OAuth 2.0 |
| MCP-07 | MCP calls shall be logged separately for traceability |
| MCP-08 | Starlark-based code execution shall be supported as an MCP tool type |

### 3.8 Plugin System

| ID | Requirement |
|----|-------------|
| PLUG-01 | The system shall execute a plugin pipeline of Pre/Post hooks around every inference call |
| PLUG-02 | Plugins shall be able to short-circuit the pipeline and return synthetic responses |
| PLUG-03 | Post hooks shall run in reverse registration order (stack semantics) |
| PLUG-04 | Plugins shall be enabled/disabled at runtime without restart |
| PLUG-05 | Custom plugins shall be loadable as native Go `.so` or WASM modules |
| PLUG-06 | Plugin execution shall be symmetric: PostHook only runs for plugins whose PreHook executed |
| PLUG-07 | Observability plugins shall receive completed traces asynchronously (zero latency impact) |

### 3.9 Configuration Management

| ID | Requirement |
|----|-------------|
| CONF-01 | All configuration shall be persistable to SQLite (default) or PostgreSQL |
| CONF-02 | Configuration changes via API shall take effect without restart |
| CONF-03 | Sensitive values shall support transparent encryption at rest (AES-256-GCM) |
| CONF-04 | Configuration shall be exportable and importable as JSON |
| CONF-05 | All sensitive fields shall support `env.VAR_NAME` environment variable references |

### 3.10 Control Plane UI

| ID | Requirement |
|----|-------------|
| UI-01 | The UI shall be a Next.js SPA embedded in the gateway binary |
| UI-02 | The UI shall provide visual management for all admin API capabilities |
| UI-03 | The UI shall display real-time log updates via WebSocket |
| UI-04 | The UI shall support dark and light themes |
| UI-05 | Enterprise-only features shall show upgrade prompts in OSS deployments |
| UI-06 | All forms shall show inline validation errors on blur |
| UI-07 | Destructive actions shall require confirmation dialogs |

### 3.11 Authentication & Session

| ID | Requirement |
|----|-------------|
| AUTH-01 | Admin endpoints shall be protectable by username/password authentication |
| AUTH-02 | Authentication shall be optional (disabled by default for zero-friction startup) |
| AUTH-03 | Sessions shall persist for 30 days via HTTP-only cookies |
| AUTH-04 | WebSocket connections shall authenticate via short-lived single-use tickets |
| AUTH-05 | Authentication can be independently enforced on inference endpoints |

---

### 3.12 Role-Based Access Control ✨ [v2.0 — Enterprise]

| ID | Requirement |
|----|-------------|
| RBAC-01 | The system shall support 5 predefined roles: **Owner**, **Admin**, **Operator**, **Developer**, **Viewer** |
| RBAC-02 | Every HTTP endpoint under `/api/` shall enforce a minimum required role; unauthorized callers receive HTTP 403 |
| RBAC-03 | Each authenticated user shall have one or more assigned roles; no user may have zero roles |
| RBAC-04 | The system shall prevent removing the last Owner role assignment (anti-lockout protection) |
| RBAC-05 | Role assignments shall take effect immediately without requiring logout/login |
| RBAC-06 | Admins shall manage roles and role assignments via `GET/POST/PUT/DELETE /api/rbac/roles` and `POST/DELETE /api/rbac/users/{id}/roles` |
| RBAC-07 | The UI shall display all users and their current roles, with search and filter capabilities |
| RBAC-08 | RBAC enforcement shall add ≤ 1 ms latency per request (cached permission lookup) |
| RBAC-09 | Role and permission data shall be cached in-memory; cache invalidated on any role change |
| RBAC-10 | RBAC shall be enforced for all enterprise features; OSS features respect basic admin auth only |

**Role Permission Matrix:**

| Endpoint Category | Owner | Admin | Operator | Developer | Viewer |
|-------------------|-------|-------|----------|-----------|--------|
| Provider CRUD | ✅ | ✅ | ❌ | ❌ | ❌ |
| Virtual Key CRUD | ✅ | ✅ | ✅ | ✅* | ❌ |
| Routing Rules | ✅ | ✅ | ✅ | ❌ | ❌ |
| Plugin config | ✅ | ✅ | ❌ | ❌ | ❌ |
| RBAC management | ✅ | ✅ | ❌ | ❌ | ❌ |
| Logs (read) | ✅ | ✅ | ✅ | ✅ | ✅ |
| Audit logs (read) | ✅ | ✅ | ❌ | ❌ | ❌ |
| Dashboard | ✅ | ✅ | ✅ | ✅ | ✅ |
| License management | ✅ | ❌ | ❌ | ❌ | ❌ |

*Developer: only VKs they own, within budget allocation

---

### 3.13 Audit Logs ✨ [v2.0 — Enterprise]

| ID | Requirement |
|----|-------------|
| AUDIT-01 | The system shall create an immutable audit record for every state-changing admin action (POST/PUT/DELETE on governance, provider, RBAC, plugin, SSO, and license endpoints) |
| AUDIT-02 | Each audit record shall contain: `id`, `timestamp` (UTC), `actor_email`, `actor_ip`, `action`, `resource_type`, `resource_id`, `before_state` (JSON), `after_state` (JSON) |
| AUDIT-03 | The system shall provide no API endpoint that modifies or deletes audit records |
| AUDIT-04 | Audit logs shall be queryable via `GET /api/audit-logs` with filters: `actor`, `action`, `resource_type`, `resource_id`, `from`, `to`; pagination supported |
| AUDIT-05 | Audit log results shall be exportable as CSV via `GET /api/audit-logs/export` |
| AUDIT-06 | Audit records shall be retained for a minimum configurable period (default: 2 years) |
| AUDIT-07 | When storage utilization exceeds 80%, the system shall emit a warning alert but shall NOT auto-delete audit records |
| AUDIT-08 | Audit log write shall be asynchronous and shall add ≤ 2 ms latency to any admin request |
| AUDIT-09 | Login events (including SSO) and logout events shall also generate audit records |
| AUDIT-10 | The `before_state` and `after_state` fields shall redact sensitive values (API keys, passwords) |

---

### 3.14 Content Guardrails ✨ [v2.0 — Enterprise]

| ID | Requirement |
|----|-------------|
| GUARD-01 | The system shall execute a guardrails check on LLM request content (pre-LLM hook) and optionally on response content (post-LLM hook) |
| GUARD-02 | The guardrails engine shall support three rule types: **keyword** (case-insensitive substring/wildcard), **regex** (PCRE), **ai-classifier** (secondary LLM model evaluation) |
| GUARD-03 | Each rule shall have a configurable `scope`: `request`, `response`, or `both` |
| GUARD-04 | Each rule shall have a configurable `action`: `block` (return HTTP 400 with error), `warn` (log violation, continue), or `redact` (replace matched content with `[REDACTED]`, continue) |
| GUARD-05 | The default action for new rules shall be `warn` (non-breaking) |
| GUARD-06 | Rules shall be applicable `globally` or scoped to specific providers |
| GUARD-07 | Guardrail violations shall be logged with: `rule_id`, `rule_name`, `matched_content_snippet` (first 100 chars), `action_taken`, `request_id` |
| GUARD-08 | The UI shall provide a rule tester with sample input and live result display |
| GUARD-09 | Keyword and regex rules shall add ≤ 5 ms latency; AI classifier rules shall execute asynchronously (non-blocking for `warn` action) |
| GUARD-10 | `GET/POST /api/guardrails/rules` and `PUT/DELETE /api/guardrails/rules/{id}` shall manage rules |

---

### 3.15 PII Detection & Redaction ✨ [v2.0 — Enterprise]

| ID | Requirement |
|----|-------------|
| PII-01 | The system shall detect and redact PII in LLM request/response content **before** the logging plugin writes to storage |
| PII-02 | Out-of-the-box detection shall cover: email addresses, phone numbers (VN + international), national ID / passport numbers, credit card numbers (Luhn-validated), full names (NER), physical addresses, dates of birth |
| PII-03 | Each entity type shall support a configurable redaction mode: `mask` (replace with `***`), `hash` (SHA-256, first 8 hex chars), `remove` (delete field entirely) |
| PII-04 | Custom entity types shall be addable via user-defined regex patterns |
| PII-05 | PII Redactor shall be configurable per provider (enable/disable independently) |
| PII-06 | A PII event summary dashboard shall display: total events/day, top entity types detected, trend over 30 days |
| PII-07 | `GET/POST /api/pii/rules` and `PUT/DELETE /api/pii/rules/{id}` shall manage PII rules |
| PII-08 | `GET/PUT /api/pii/providers` shall configure per-provider PII settings |
| PII-09 | PII redaction shall add ≤ 10 ms latency per request |
| PII-10 | False positive rate for built-in entity types shall be < 2% on a standard English + Vietnamese corpus |

---

### 3.16 SSO & SCIM Provisioning ✨ [v2.0 — Enterprise]

| ID | Requirement |
|----|-------------|
| SSO-01 | The system shall support OIDC 1.0 SSO with: Google Workspace, Azure AD, Okta, Keycloak |
| SSO-02 | The system shall support SAML 2.0 SP-initiated SSO with: Okta, ADFS, Azure AD, OneLogin |
| SSO-03 | The system shall expose a SAML SP metadata endpoint at `GET /auth/saml/metadata` |
| SSO-04 | JIT provisioning shall create a Bifrost user account on first successful SSO login if one does not exist |
| SSO-05 | IdP group → Bifrost role mapping shall be configurable in the UI |
| SSO-06 | Local password login shall be disable-able when SSO is configured (enforced SSO mode) |
| SSO-07 | Bifrost sessions shall not outlive the IdP session; SSO logout shall invalidate Bifrost session within 5 minutes |
| SSO-08 | `GET /api/system/sso/status` shall return: `enabled`, `provider_type`, `enforced`, `last_user_sync` |
| SCIM-01 | The system shall implement SCIM 2.0 User endpoints: `GET/POST /scim/v2/Users`, `GET/PUT/PATCH/DELETE /scim/v2/Users/{id}` |
| SCIM-02 | The system shall implement SCIM 2.0 Group endpoints: `GET /scim/v2/Groups` for read-only group sync |
| SCIM-03 | SCIM bearer tokens shall be generatable and revocable from the UI; tokens are scoped to SCIM operations only |
| SCIM-04 | User deactivation via SCIM shall invalidate all active Bifrost sessions and suspend API key access within 30 seconds |
| SCIM-05 | `GET /api/system/scim/status` shall return: `enabled`, `last_sync`, `users_synced_count`, `errors_count` |

---

### 3.17 Adaptive Routing ✨ [v2.0 — Enterprise]

| ID | Requirement |
|----|-------------|
| AROUTE-01 | The system shall collect per-provider sliding-window metrics: p50/p95/p99 latency, error rate, cost per 1K tokens |
| AROUTE-02 | Window size shall be configurable: 1, 5, or 15 minutes |
| AROUTE-03 | The adaptive routing engine shall compute a provider score: `score = latency_w × norm_latency + error_w × error_rate + cost_w × norm_cost` |
| AROUTE-04 | Weights (`latency_w`, `error_w`, `cost_w`) shall be configurable via `PUT /api/adaptive-routing/config`; must sum to 1.0 |
| AROUTE-05 | Scores shall be recomputed every 30 seconds (configurable) |
| AROUTE-06 | A provider with error_rate exceeding a configurable threshold (default 20%) shall be marked `degraded` and assigned the lowest routing priority |
| AROUTE-07 | Adaptive routing applies only when a routing rule does not pin a specific provider |
| AROUTE-08 | `GET /api/adaptive-routing/stats` shall return live scores, metrics, and degradation status per provider |
| AROUTE-09 | Routing decision logging shall be available as opt-in debug mode |
| AROUTE-10 | Score computation overhead shall be ≤ 1 ms per routed request (cached from scheduled recompute) |

---

### 3.18 Multi-node Clustering ✨ [v2.0 — Enterprise]

| ID | Requirement |
|----|-------------|
| CLUSTER-01 | Cluster mode shall be activated by setting `clustering.enabled: true` in config; requires PostgreSQL (SQLite not supported in cluster mode) |
| CLUSTER-02 | Each node shall register in a `cluster_nodes` table with: `id`, `hostname`, `version`, `started_at`, `last_heartbeat`, `status` |
| CLUSTER-03 | Nodes shall emit a heartbeat every 10 seconds; a node with no heartbeat for 30 seconds shall be marked `dead` |
| CLUSTER-04 | When a Virtual Key, Routing Rule, or Provider configuration changes on any node, a cache invalidation event shall be published via Redis pub/sub or PostgreSQL LISTEN/NOTIFY |
| CLUSTER-05 | All nodes shall process the invalidation event and evict the relevant in-memory cache entry within 500 ms |
| CLUSTER-06 | Budget usage counters and rate limit counters shall use PostgreSQL atomic operations (no per-node local state) |
| CLUSTER-07 | Distributed locks shall prevent duplicate scheduled operations (budget reset, log cleanup) across nodes |
| CLUSTER-08 | `GET /api/cluster/nodes` shall return: node list, heartbeat age, version, request count/min |
| CLUSTER-09 | `GET /api/cluster/nodes/{id}/health` shall return: memory usage, goroutine count, queue depths |
| CLUSTER-10 | Redis is optional; if absent, the system shall fall back to PostgreSQL LISTEN/NOTIFY (slightly higher latency, still < 500 ms) |

---

### 3.19 Alert Channels ✨ [v2.0 — Enterprise]

| ID | Requirement |
|----|-------------|
| ALERT-01 | The system shall support 4 channel types: `webhook` (HTTP POST), `slack` (native Block Kit formatting), `pagerduty` (Events API v2), `email` (SMTP) |
| ALERT-02 | The system shall support 6 trigger events: `BUDGET_WARNING` (configurable threshold, default 80%), `BUDGET_EXCEEDED`, `RATE_LIMIT_HIT`, `PROVIDER_DEGRADED`, `GUARDRAIL_VIOLATION`, `CLUSTER_NODE_DOWN` |
| ALERT-03 | Each channel shall independently subscribe to a subset of the 6 event types |
| ALERT-04 | Alert dispatch shall be asynchronous — must not block and must not add measurable latency to any request |
| ALERT-05 | Alert delivery latency (event trigger → notification received) shall be ≤ 60 seconds |
| ALERT-06 | Per-event per-channel cooldown shall be configurable (default: 15 minutes) to prevent alert storms |
| ALERT-07 | `POST /api/alert-channels/{id}/test` shall immediately dispatch a test notification to verify connectivity |
| ALERT-08 | `GET/POST /api/alert-channels` and `PUT/DELETE /api/alert-channels/{id}` shall manage channels |
| ALERT-09 | Failed alert deliveries shall be retried up to 3 times with exponential backoff |
| ALERT-10 | Alert delivery history (last 30 days) shall be queryable via `GET /api/alert-channels/{id}/history` |

---

### 3.20 HashiCorp Vault Integration ✨ [v2.0 — Enterprise]

| ID | Requirement |
|----|-------------|
| VAULT-01 | Provider API key values of the form `vault://secret/path#field` shall be resolved from HashiCorp Vault at startup |
| VAULT-02 | The system shall support 3 Vault auth methods: `token`, `approle` (role_id + secret_id), `kubernetes` (ServiceAccount JWT) |
| VAULT-03 | Resolved secrets shall be cached in memory; secrets shall not be fetched per-request |
| VAULT-04 | For dynamic secrets, the system shall renew the Vault lease before it expires (renewal buffer: 20% of TTL, configurable) |
| VAULT-05 | If Vault is unreachable at startup and a Vault reference exists, the system SHALL NOT start; it shall log a clear error and exit |
| VAULT-06 | If Vault becomes unreachable after startup (renewal failure), the system shall log a warning and continue using cached secrets until TTL expiry |
| VAULT-07 | Vault configuration fields: `address`, `namespace` (Vault Enterprise), `auth_method`, `role_id`, `secret_id`, `tls_skip_verify`, `ca_cert_path`, `mount_path` |
| VAULT-08 | `GET /api/system/vault/status` shall return: `connected`, `auth_method`, `last_renewal`, `secrets_tracked_count` |
| VAULT-09 | The Vault configuration block shall be added to `transports/config.schema.json` |
| VAULT-10 | Vault credentials (role_id, secret_id, token) shall follow the same encryption-at-rest policy as provider API keys |

---

### 3.21 Large Payload Optimization ✨ [v2.0 — Enterprise]

| ID | Requirement |
|----|-------------|
| LPAY-01 | The system shall support streaming multipart body upload for file/audio/video inputs exceeding 50 MB without loading the entire body into RAM |
| LPAY-02 | Chunked HTTP transfer encoding shall be supported for uploading to providers that accept it |
| LPAY-03 | SSE response streams for long-running completions shall not accumulate the entire response in memory unless a post-hook requires accumulation |
| LPAY-04 | `max_body_size` shall be configurable per provider in `NetworkConfig` |
| LPAY-05 | A `stream_threshold_bytes` parameter shall control when streaming mode activates (default: 10 MB) |
| LPAY-06 | System shall handle ≥ 10 concurrent 200 MB upload+transcription pipelines without OOM |
| LPAY-07 | Load tests for 200 MB audio file upload and transcription shall be part of the CI test suite |

---

### 3.22 MCP Tool Groups ✨ [v2.0 — Enterprise]

| ID | Requirement |
|----|-------------|
| MCPGRP-01 | Admins shall create named Tool Groups consisting of a list of tool names from a specific MCP client |
| MCPGRP-02 | Virtual Keys shall be assignable to one or more Tool Groups instead of individual tool lists |
| MCPGRP-03 | Changes to a Tool Group's tool list shall immediately apply to all Virtual Keys referencing that group |
| MCPGRP-04 | `GET/POST /api/mcp/tool-groups` and `PUT/DELETE /api/mcp/tool-groups/{id}` shall manage Tool Groups |
| MCPGRP-05 | `POST/DELETE /api/mcp/tool-groups/{id}/tools` shall add/remove individual tools from a group |
| MCPGRP-06 | Deleting a Tool Group that is referenced by one or more Virtual Keys shall be rejected with HTTP 409 |
| MCPGRP-07 | `GET /api/mcp/tool-groups` response shall include a `virtual_key_count` field per group |

---

### 3.23 User Groups ✨ [v2.0 — Enterprise]

| ID | Requirement |
|----|-------------|
| UGRP-01 | Admins shall create named User Groups with an associated set of RBAC role assignments |
| UGRP-02 | Users added to a User Group shall inherit all roles assigned to that group |
| UGRP-03 | A user's effective permissions shall be the union of their direct roles and all roles from their groups |
| UGRP-04 | Removing a user from a User Group shall immediately recompute and apply their effective permissions |
| UGRP-05 | SCIM Group sync shall map IdP groups to Bifrost User Groups by matching group name (configurable) |
| UGRP-06 | `GET/POST /api/user-groups` and `PUT/DELETE /api/user-groups/{id}` shall manage User Groups |
| UGRP-07 | `GET /api/users/{id}/effective-permissions` shall return the merged permission set for a user (direct + group) |

---

### 3.24 Data Connectors ✨ [v2.0 — Enterprise]

| ID | Requirement |
|----|-------------|
| CONN-01 | The system shall support 4 connector types for exporting inference logs: `bigquery`, `datadog`, `s3` (also GCS-compatible), `elasticsearch` |
| CONN-02 | Each connector shall have configurable fields: `type`, `enabled`, `credentials` (JSON, AES-256-GCM encrypted at rest), `sync_interval` (1-60 min), `filter_expr` (CEL expression to pre-filter records) |
| CONN-03 | BigQuery connector shall stream records to a configured dataset/table; partition by ingestion date; schema auto-created if absent |
| CONN-04 | Datadog connector shall ship logs via Datadog Logs Intake API with configurable service tags |
| CONN-05 | S3/GCS connector shall batch-export records as JSONL or Parquet to a configured bucket/prefix |
| CONN-06 | Elasticsearch connector shall index records using a configurable index name pattern |
| CONN-07 | Connector credentials shall be subject to the same AES-256-GCM encryption policy as API keys |
| CONN-08 | `POST /api/data-connectors/{id}/test` shall validate credentials and write one synthetic test record |
| CONN-09 | Connector status endpoint shall return: `last_sync`, `records_exported`, `errors_count`, `lag_seconds` |
| CONN-10 | Export processing shall be asynchronous; failed exports shall retry with exponential backoff (max 4 hours) |
| CONN-11 | Export failure alerts shall be emitted via Alert Channels if configured |
| CONN-12 | `GET/POST /api/data-connectors` and `PUT/DELETE /api/data-connectors/{id}` shall manage connectors |

---

### 3.25 License Enforcement ✨ [v2.0 — Enterprise]

| ID | Requirement |
|----|-------------|
| LIC-01 | The system shall validate an enterprise license key at startup; the key is provided via `config.json` or the `BIFROST_LICENSE_KEY` environment variable |
| LIC-02 | License keys shall be signed JWTs containing: `issued_to`, `features[]`, `expires_at`, `node_count`, `issued_at` |
| LIC-03 | An enterprise API endpoint handler shall call `lib.IsFeatureEnabled(feature)` before executing; callers without a valid license for that feature receive HTTP 403 with an upgrade message |
| LIC-04 | OSS builds (no license key) shall gracefully return HTTP 403 with an upgrade prompt — not HTTP 500 or a crash |
| LIC-05 | A license expiry warning banner shall appear in the UI when fewer than 30 days remain |
| LIC-06 | After license expiry, a 7-day grace period shall allow all licensed features to continue operating with a persistent warning |
| LIC-07 | After the grace period, enterprise features shall serve HTTP 403 until a valid license is provided |
| LIC-08 | The license key shall NOT be stored in the database; it is read-only from the config source |
| LIC-09 | `GET /api/system/license` shall return (for all authenticated users): `status`, `issued_to`, `features[]`, `expires_at`, `days_remaining`, `node_limit` |
| LIC-10 | The license validator shall enforce `node_count` — if active cluster nodes exceed the licensed limit, new node registrations shall be rejected with a clear error |

---

## 4. Non-Functional Requirements

### 4.1 Performance

| ID | Requirement | Since |
|----|-------------|-------|
| PERF-01 | Gateway overhead shall be ≤ 20 µs per request at 5,000 RPS | v1.0 |
| PERF-02 | All per-request hot-path allocations shall be eliminated via object pooling | v1.0 |
| PERF-03 | Provider concurrency shall be controllable per key to prevent upstream overloading | v1.0 |
| PERF-04 | Excess requests shall either queue or be dropped depending on configuration | v1.0 |
| PERF-05 | UI initial load shall complete in under 2 seconds on standard connection | v1.0 |
| PERF-06 | Log table pagination shall render within 300ms | v1.0 |
| PERF-07 | RBAC middleware check shall add ≤ 1 ms latency per API request | **v2.0** |
| PERF-08 | Guardrail keyword/regex rules shall add ≤ 5 ms latency per inference request | **v2.0** |
| PERF-09 | PII redaction shall add ≤ 10 ms latency per inference request | **v2.0** |
| PERF-10 | Audit log write shall be asynchronous; shall add ≤ 2 ms latency to admin requests | **v2.0** |
| PERF-11 | Adaptive routing score computation overhead shall be ≤ 1 ms per request | **v2.0** |
| PERF-12 | Cross-node cache invalidation shall complete within 500 ms | **v2.0** |

### 4.2 Security

| ID | Requirement | Since |
|----|-------------|-------|
| SEC-01 | Provider API keys shall never appear in plain text after initial creation | v1.0 |
| SEC-02 | All sensitive database fields shall be encryptable via AES-256-GCM | v1.0 |
| SEC-03 | CORS origins shall be strictly validated | v1.0 |
| SEC-04 | Security response headers (X-Frame-Options, CSP, HSTS, etc.) shall be set on all responses | v1.0 |
| SEC-05 | Passwords shall be hashed using scrypt before storage | v1.0 |
| SEC-06 | The system shall support global HTTP proxy with optional TLS verification skip | v1.0 |
| SEC-07 | PII data shall never be persisted to any storage layer (including audit logs) | **v2.0** |
| SEC-08 | Audit records shall be cryptographically append-only at the application layer (no UPDATE/DELETE on audit_logs table) | **v2.0** |
| SEC-09 | SCIM bearer tokens and SSO credentials shall follow the same encryption-at-rest policy as API keys | **v2.0** |
| SEC-10 | Vault credentials (token, role_id, secret_id) shall never be returned in API responses | **v2.0** |
| SEC-11 | Enterprise license key shall not be stored in the database | **v2.0** |
| SEC-12 | All sessions shall be invalidatable immediately upon user deactivation or admin revocation | **v2.0** |

### 4.3 Reliability

| ID | Requirement | Since |
|----|-------------|-------|
| REL-01 | Automatic failover shall require no client-side changes | v1.0 |
| REL-02 | WebSocket connections shall reconnect with exponential backoff on disconnection | v1.0 |
| REL-03 | Graceful shutdown shall flush all pending logs and metrics before exit | v1.0 |
| REL-04 | Database migrations shall be idempotent and run automatically on startup | v1.0 |
| REL-05 | Cluster mode shall maintain service continuity when up to (N-1)/2 nodes fail, where N ≥ 3 | **v2.0** |
| REL-06 | Alert dispatch failures shall be retried up to 3 times before being logged as undeliverable | **v2.0** |
| REL-07 | Data connector export failures shall be retried with exponential backoff (max 4-hour interval) | **v2.0** |
| REL-08 | If Vault is unreachable at startup, the system shall exit immediately with a clear error | **v2.0** |

### 4.4 Scalability

| ID | Requirement | Since |
|----|-------------|-------|
| SCALE-01 | The system shall support PostgreSQL as the persistence backend for multi-node deployments | v1.0 |
| SCALE-02 | The distributed lock system shall prevent duplicate operations across nodes | v1.0 |
| SCALE-03 | A shared KVStore shall be available for session stickiness in clustered deployments | v1.0 |
| SCALE-04 | The system shall support ≥ 500 concurrent administrators on the control plane UI without degradation | **v2.0** |
| SCALE-05 | Audit log storage shall support ≥ 10 million records per day | **v2.0** |
| SCALE-06 | SCIM provisioning throughput shall be ≥ 1,000 user operations per minute | **v2.0** |
| SCALE-07 | Data connectors shall export ≥ 100,000 inference log records per minute | **v2.0** |
| SCALE-08 | The system shall support ≥ 50 simultaneous alert channels | **v2.0** |

### 4.5 Maintainability

| ID | Requirement | Since |
|----|-------------|-------|
| MAINT-01 | The core engine shall have zero dependency on the framework or transport layer | v1.0 |
| MAINT-02 | Each provider shall be independently testable | v1.0 |
| MAINT-03 | All utility functions shall have unit tests | v1.0 |
| MAINT-04 | The plugin interface shall be versioned to allow backward-compatible evolution | v1.0 |
| MAINT-05 | Each enterprise feature module (guardrails, piiredactor, adaptiverouting) shall be a separate Go module with its own `go.mod` | **v2.0** |
| MAINT-06 | Enterprise UI components shall reside in `ui/app/enterprise/` and be replaceable independently | **v2.0** |

### 4.6 Compliance ✨ [v2.0]

| ID | Requirement |
|----|-------------|
| COMP-01 | The audit log system shall produce evidence sufficient for SOC 2 Type II controls (CC6.1, CC6.2, CC6.3) |
| COMP-02 | PII Redactor shall enable GDPR Art. 25 (data minimization) and CCPA compliance for log data |
| COMP-03 | SCIM deprovisioning shall complete within 30 seconds, satisfying timely access revocation controls |
| COMP-04 | All sensitive data encryption shall use AES-256-GCM with key derivation via PBKDF2-SHA256, satisfying FIPS 140-2 data-at-rest requirements |
| COMP-05 | Audit log export shall produce artifacts sufficient for ISO 27001 A.12.4 (logging and monitoring) evidence |
| COMP-06 | The compliance report export endpoint (`GET /api/audit-logs/export`) shall be accessible only to Owner and Admin roles |

---

## 5. System Architecture Constraints

**v1.0 Constraints (retained):**
- Core engine must remain a standalone Go module with no framework dependencies
- The HTTP transport must use FastHTTP for performance requirements
- The static UI must be embeddable in the Go binary via `go:embed`
- Next.js must be configured for static export (`output: "export"`)
- Enterprise and OSS builds must share a single codebase via Webpack alias substitution

**v2.0 Additional Constraints:**
- Enterprise feature plugins (`guardrails`, `piiredactor`, `adaptiverouting`) must be separate Go modules in `plugins/` with their own `go.mod` — maintains module isolation
- Enterprise UI components must reside in `ui/app/enterprise/` and be activated via the existing `@enterprise` Webpack alias — no changes to OSS build pipeline
- Audit log records must be written via a dedicated append-only DB function; no `UPDATE` or `DELETE` must be callable on the `audit_logs` table from application code
- PII redaction must be applied as an `LLMPlugin.PostLLMHook` that runs **before** the logging plugin receives the response — plugin registration order is mandatory
- Cluster mode requires PostgreSQL — SQLite cluster mode must explicitly return an error at startup
- License key must be read exclusively from config file or environment variable — never written to or read from database
- The `lib.IsFeatureEnabled(feature string)` helper must be the sole mechanism for enterprise feature gating — no direct license parsing in handlers

---

## 6. External Interface Requirements

### 6.1 Inference API

- REST + JSON over HTTP/HTTPS
- SSE for streaming responses
- Content-Type: `application/json` (or `multipart/form-data` for file/audio uploads)
- Authentication: `Authorization: Bearer <vk>`, `X-BF-VK: <vk>`, or `X-BF-API-Key: <key>`

### 6.2 Admin API

- REST + JSON over HTTP/HTTPS
- Authentication: session cookie or Bearer token
- Base path: `/api/`

### 6.3 WebSocket

- Endpoint: `WS /ws`
- Auth: short-lived ticket (`?ticket=<token>`)
- Message format: `{ "type": string, "payload": any }`

### 6.4 Prometheus

- Endpoint: `GET /metrics`
- Standard Prometheus text format
- Optional Push Gateway support

### 6.5 OpenTelemetry

- OTLP over HTTP or gRPC
- Configurable collector URL, headers, and trace format

### 6.6 MCP Server

- `GET /mcp` — SSE endpoint for persistent MCP clients
- `POST /mcp` — JSON-RPC for stateless MCP clients

### 6.7 SCIM 2.0 API ✨ [v2.0]

- Base path: `/scim/v2/`
- Authentication: `Authorization: Bearer <scim_token>` (dedicated SCIM token, not session cookie)
- Compliant with RFC 7643 (SCIM Core Schema) and RFC 7644 (SCIM Protocol)
- Content-Type: `application/scim+json`
- Endpoints: `GET/POST /scim/v2/Users`, `GET/PUT/PATCH/DELETE /scim/v2/Users/{id}`, `GET /scim/v2/Groups`

### 6.8 SAML 2.0 / OIDC ✨ [v2.0]

- SAML: `GET /auth/saml/metadata` (SP metadata), `POST /auth/saml/acs` (assertion consumer)
- OIDC: `GET /auth/oidc/callback` (callback URL registered with IdP)
- Session issued as HTTP-only cookie on successful SSO authentication

### 6.9 HashiCorp Vault ✨ [v2.0]

- Vault API v1 (`github.com/hashicorp/vault/api`)
- Outbound HTTPS to configured Vault address
- Auth methods: token, AppRole (`/auth/approle/login`), Kubernetes (`/auth/kubernetes/login`)
- Secret engines: KV v1 and KV v2

### 6.10 Data Connectors ✨ [v2.0]

| Connector | External Interface |
|-----------|-------------------|
| BigQuery | Google Cloud BigQuery Storage Write API (gRPC) or HTTP |
| Datadog | Datadog Logs Intake API (`https://http-intake.logs.datadoghq.com`) |
| S3 / GCS | AWS S3 API (or GCS S3-compatible endpoint) |
| Elasticsearch | Elasticsearch Bulk API (`POST /<index>/_bulk`) |

---

## 7. Data Requirements

### 7.1 API Key Redaction

- After saving, keys are displayed as: `<first4>` + 24 asterisks + `<last4>` (32 chars)
- Keys ≤ 8 chars: displayed as all asterisks
- `env.VAR_NAME` references are considered valid key values
- `vault://path#field` references are displayed as `[Vault Secret: path]`

### 7.2 Log Retention

- Default: 365 days
- Configurable per deployment
- Deletion uses batched operations to avoid table locks

### 7.3 Budget Reset Periods

- Accepted formats: `1h`, `1d`, `1w`, `1M` (hourly, daily, weekly, monthly)
- Optional `calendar_aligned` mode: resets at start of calendar period

### 7.4 Model String Format

- All inference model references use `provider/model` format
- Provider is a lowercase string matching a registered provider name
- Model is provider-specific

### 7.5 New Database Tables ✨ [v2.0]

The following tables shall be added to the configstore schema with GORM auto-migration:

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `users` | Persistent user directory (SSO/SCIM) | `id`, `email`, `external_id`, `idp_source`, `is_active` |
| `rbac_roles` | Role definitions | `id`, `name`, `description` |
| `rbac_permissions` | Endpoint-to-role mapping | `role_id`, `method`, `path_pattern` |
| `rbac_user_roles` | User-role assignments | `user_id`, `role_id`, `assigned_by`, `assigned_at` |
| `audit_logs` | Immutable admin action audit trail | `id`, `timestamp`, `actor_email`, `actor_ip`, `action`, `resource_type`, `resource_id`, `before_state`, `after_state` |
| `guardrail_rules` | Content safety rules | `id`, `type`, `pattern`, `scope`, `action`, `provider_filter`, `enabled` |
| `pii_rules` | PII entity detection configs | `id`, `entity_type`, `regex_override`, `redaction_mode`, `enabled` |
| `alert_channels` | Notification channel configs | `id`, `type`, `endpoint_url`, `auth_header_encrypted`, `events[]`, `enabled`, `cooldown_minutes` |
| `alert_history` | Delivery history | `id`, `channel_id`, `event_type`, `sent_at`, `status`, `error` |
| `cluster_nodes` | Node registry for clustering | `id`, `hostname`, `version`, `started_at`, `last_heartbeat`, `status` |
| `mcp_tool_groups` | Reusable MCP tool groupings | `id`, `name`, `description`, `mcp_client_id` |
| `mcp_tool_group_tools` | Tools within a group | `group_id`, `tool_name` |
| `vk_mcp_tool_group_configs` | VK-to-tool-group assignments | `vk_id`, `group_id` |
| `user_groups` | Named groups for bulk role assignment | `id`, `name`, `description` |
| `user_group_members` | User-to-group membership | `group_id`, `user_id` |
| `user_group_roles` | Role assignments for a group | `group_id`, `role_id` |
| `data_connectors` | External log export configs | `id`, `type`, `credentials_encrypted`, `enabled`, `sync_interval`, `filter_expr` |
| `scim_tokens` | SCIM API tokens | `id`, `token_hash`, `description`, `created_at`, `last_used_at` |
| `adaptive_routing_metrics` | Per-provider sliding-window stats | `provider`, `window`, `p95_latency_ms`, `error_rate`, `cost_per_1k_tokens`, `computed_at` |

### 7.6 Audit Log Immutability Contract

- The `audit_logs` table shall have a database-level trigger or application-layer constraint that rejects any `UPDATE` or `DELETE` statement
- The ORM layer shall only expose `Create` and `Find` operations for this table
- Retention expiry (Section 7.2 policy extended to audit logs) shall be the only allowed deletion mechanism, implemented via a scheduled job with explicit audit log retention config

### 7.7 PII Data Handling Contract

- PII entities detected by the PII Redactor plugin shall never reach the `logging` plugin in unredacted form
- Plugin registration order in the pipeline shall be enforced: PII Redactor → Logging (PII Redactor always registered before Logging when both are enabled)
- The `before_state` / `after_state` fields in audit log records shall apply the same PII redaction rules as inference logs

---

## 8. Constraints & Assumptions

### 8.1 Constraints

**v1.0 Constraints (retained):**
- Enterprise features require a valid enterprise license
- The embedded UI requires JavaScript; it is a CSR SPA after the initial SSR shell
- API keys are never returned after initial save (only redacted values)

**v2.0 Additional Constraints:**
- Cluster mode requires PostgreSQL — SQLite is not supported in cluster mode
- Cluster mode requires Redis (preferred) or PostgreSQL LISTEN/NOTIFY for cross-node pub/sub
- PII Redactor plugin must be registered before the Logging plugin in the plugin pipeline
- Audit log table must be append-only: no `UPDATE` or `DELETE` from application code
- License key must only be read from config file or `BIFROST_LICENSE_KEY` env var — never stored in DB
- `lib.IsFeatureEnabled()` must be the sole gating mechanism — no direct license parsing in individual handlers
- SCIM endpoints must use dedicated SCIM bearer tokens (not session cookies)
- Vault references (`vault://...`) must be resolved at startup, not at request time

### 8.2 Assumptions

**v1.0 Assumptions (retained):**
- The deployment environment has outbound HTTPS access to LLM provider APIs
- WebSocket connectivity is available between UI clients and the gateway
- For SQLite deployments, the `/data` volume is a persistent filesystem mount
- Environment variables containing secrets are available in the gateway process environment

**v2.0 Additional Assumptions:**
- Enterprise deployments have PostgreSQL 14+ available as the primary datastore
- Enterprise deployments optionally have Redis 7+ for cluster pub/sub
- Enterprise deployments have network access to the configured HashiCorp Vault instance
- Enterprise IdP (Okta, Azure AD, etc.) supports either SCIM 2.0 or SAML 2.0
- Outbound HTTPS from Bifrost to external data connector endpoints (BigQuery, Datadog APIs) is available
- Enterprise license key is provisioned and accessible before Bifrost startup

---

*This SRS v2.0 reflects OSS baseline (v1.0) plus all 14 Enterprise feature sets defined in CR-ENT-001 and CR-ENT-002, dated 2026-04-08.*
