# Product Requirements Document (PRD)
## Bifrost AI Gateway

**Version:** 1.0  
**Date:** 2026-04-05  
**Status:** Draft  
**References:** [SRS.md](SRS.md), [URD.md](URD.md), [TDD-project.md](TDD-project.md)

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Problem Statement](#2-problem-statement)
3. [Goals & Success Metrics](#3-goals--success-metrics)
4. [User Personas](#4-user-personas)
5. [Feature Requirements](#5-feature-requirements)
6. [OSS vs Enterprise Feature Split](#6-oss-vs-enterprise-feature-split)
7. [Constraints & Non-Goals](#7-constraints--non-goals)
8. [Go-to-Market Considerations](#8-go-to-market-considerations)
9. [Dependencies & Risks](#9-dependencies--risks)
10. [Release Criteria](#10-release-criteria)

---

## 1. Executive Summary

**Bifrost** is an open-source, high-performance AI gateway that gives engineering teams a single, stable interface to every major LLM provider. It eliminates the fragmentation of managing multiple provider SDKs, API keys, rate limits, and failover strategies — replacing them with one endpoint, one credential (virtual key), and one control plane.

Bifrost runs as a lightweight binary or Docker container with zero external dependencies. Teams go from install to first API call in under a minute, with real-time monitoring, automatic failover, and semantic caching working out of the box.

An enterprise tier extends the platform with adaptive routing, guardrails, PII redaction, RBAC, clustering, and compliance features for organizations running AI at scale.

---

## 2. Problem Statement

### 2.1 The Pain Today

Teams building AI-powered products face a fragmented infrastructure problem:

| Problem | Impact |
|---------|--------|
| **Multiple provider SDKs** to maintain — OpenAI, Anthropic, Bedrock, Vertex all have different APIs, auth schemes, and response formats | Every provider adds SDK version management, error handling boilerplate, and schema translation code |
| **No built-in failover** — a single provider outage takes down the whole product | Manual fallback logic is complex, error-prone, and inconsistent across teams |
| **API key proliferation** — raw provider keys scattered across services, environments, and team members | Key rotation is painful; leaked keys cause security incidents; usage is invisible |
| **Cost blindness** — teams don't know what models cost by team, feature, or environment until the monthly bill arrives | Over-spending on premium models; no per-team accountability |
| **Latency of repeated queries** — the same or semantically similar prompts are sent to the LLM repeatedly | Wasted cost and added latency for responses that could be cached |
| **Fragmented observability** — logs in N different provider dashboards, metrics in N formats | Debugging cross-provider issues is nearly impossible |

### 2.2 Who Feels This Pain Most

- **ML Platform teams** who need to provide a self-serve LLM gateway to dozens of product teams
- **Startups** integrating LLMs who want reliability and governance without building it themselves
- **Enterprises** adopting LLMs who need compliance controls (PII, audit, RBAC) before going to production

---

## 3. Goals & Success Metrics

### 3.1 Product Goals

| Goal | Metric | Target |
|------|--------|--------|
| Zero-friction adoption | Time from install to first API call | < 60 seconds |
| Performance parity with direct SDK | Overhead per request at 5k RPS | ≤ 20 µs |
| Reduce integration work | Lines of code vs. raw multi-provider integration | -70% |
| Provider reliability | Effective uptime with failover | 99.95%+ |
| Cost visibility | Time to first cost dashboard | < 5 minutes post-install |
| Community growth | GitHub stars, Docker pulls | Tracked quarterly |

### 3.2 Engineering Success Criteria

- Gateway overhead ≤ 20 µs per request at 5,000 RPS (benchmarked)
- All provider adapters tested with real API calls in CI
- Zero `sync.Mutex` on the hot request path — lock-free via `atomic.Pointer` and channels
- UI initial load < 2 seconds; table render < 300ms

---

## 4. User Personas

### Persona A: "The Platform Engineer" (Primary)

**Name:** Alex, ML Platform Lead  
**Company:** Scale-up with 50+ engineers across 10 product teams  
**Situation:** Each product team integrates LLMs independently — multiple OpenAI keys, no cost tracking, no failover. When OpenAI has an outage, everything breaks.  
**Goal:** Deploy a central gateway that all teams route through. Single place for API keys, rate limits, cost attribution, and monitoring.  
**Success:** All 10 teams routing through Bifrost within 1 sprint. Zero-downtime during next provider incident.

### Persona B: "The Solo Developer" (High-volume)

**Name:** Jordan, indie developer  
**Company:** Solo AI product with 5k daily users  
**Situation:** Running on OpenAI only. Spent $200 last month on redundant prompts. Worried about vendor lock-in.  
**Goal:** Add failover to Anthropic, enable semantic caching, and understand which features are costing money.  
**Success:** 30% cost reduction from caching. < 1 minute of user-visible downtime per month.

### Persona C: "The Enterprise Architect" (Enterprise buyer)

**Name:** Morgan, Principal Architect  
**Company:** Fortune 500 financial services firm  
**Situation:** Legal requires audit trails of all AI completions. Security requires PII redaction before logging. IT requires SSO and RBAC.  
**Goal:** Deploy Bifrost in a private network with enterprise controls before any LLM use goes to production.  
**Success:** Passing AI governance audit. All prompt/response data stays inside corporate network (except what goes to providers).

### Persona D: "The SDK User" (Go developer)

**Name:** Sam, backend engineer  
**Company:** High-frequency API company writing everything in Go  
**Situation:** Needs LLM routing in a hot path — no room for a sidecar process or network hop.  
**Goal:** Embed Bifrost directly as a Go library, plug in their existing observability and auth systems.  
**Success:** Bifrost `core/` imported with 15 lines of Go. No extra latency vs. calling the provider directly (minus propagation).

---

## 5. Feature Requirements

### 5.1 P0 — Must Ship (Core Value)

These are the features that define the product and must work reliably before any release.

| Feature | Description | User Story |
|---------|-------------|-----------|
| **Unified inference API** | Single `/v1/chat/completions` endpoint routing to any provider via `provider/model` format | Persona A, B |
| **OpenAI-compatible drop-in** | `/openai/v1/` prefix makes Bifrost a literal `base_url` swap | Persona B, D |
| **Automatic failover** | `fallbacks[]` in request body triggers transparent retry on failure | Persona A, B |
| **Weighted load balancing** | Multiple keys per provider distributed by weight | Persona A |
| **Virtual API keys** | Proxy keys with rate limits and budgets that map to real provider keys | Persona A |
| **Real-time request logs** | Every request logged; searchable by provider, model, status, time, cost | Persona A, B |
| **Provider CRUD** | Add/update/remove provider configs via UI and API with hot-reload | Persona A |
| **Embedded control plane UI** | Next.js SPA served from the same binary — no separate frontend deploy | Persona A, B |
| **Zero-config startup** | `npx -y @maximhq/bifrost` runs with no required arguments | Persona B |
| **Docker support** | Official image with `/app/data` volume for persistence | Persona A |

### 5.2 P1 — High Priority (Differentiating Features)

These drive retention and competitive differentiation.

| Feature | Description | User Story |
|---------|-------------|-----------|
| **Semantic caching** | Two-tier cache (exact hash + cosine similarity) reduces repeated LLM calls | Persona B |
| **CEL routing rules** | Dynamic routing rules evaluated per request using Common Expression Language | Persona A |
| **Prometheus metrics** | `/metrics` endpoint with request count, latency, token usage, cost per provider/model | Persona A, C |
| **OpenTelemetry export** | OTLP trace export to any compatible collector | Persona A, C |
| **MCP gateway** | Register external MCP servers; Bifrost routes tool calls in agentic workflows | Persona A |
| **Governance hierarchy** | Team → Customer → VK budget + rate limit hierarchy | Persona A, C |
| **Anthropic / GenAI / Bedrock drop-ins** | SDK-specific prefix endpoints for other providers | Persona B, D |
| **Async inference** | Submit-then-poll job API for long-running requests | Persona A |
| **Plugin system** | Pre/Post hook middleware pipeline; custom `.so` and WASM plugins | Persona A, D |
| **WebSocket real-time feed** | Log and metric updates pushed to UI without polling | Persona A, B |

### 5.3 P2 — Important (Enterprise Enablement)

Required for enterprise buyers; gated behind license.

| Feature | Description | User Story |
|---------|-------------|-----------|
| **RBAC** | Role-based access control for UI and API | Persona C |
| **Audit logs** | Immutable record of all admin configuration changes | Persona C |
| **Guardrails** | Content safety rules (keyword, regex, PII detection) on requests and responses | Persona C |
| **PII redactor** | Configurable PII detection + redaction before log storage | Persona C |
| **SCIM provisioning** | Automated user provisioning from enterprise IdPs (Okta, Azure AD) | Persona C |
| **Adaptive routing** | AI-driven routing that considers provider latency, error rate, and cost | Persona A |
| **Clustering** | Multi-node deployment with shared state and node health visibility | Persona A, C |
| **Alert channels** | Webhook/Slack/PagerDuty alerts on budget breach, provider errors, guardrail violations | Persona A |
| **Vault support** | HashiCorp Vault integration for secret injection | Persona C |
| **Large payload optimization** | Streaming handling for multi-hundred-MB payloads (audio, video, batch) | Persona A |

### 5.4 P3 — Nice-to-Have (Future Backlog)

| Feature | Description |
|---------|-------------|
| Prompt repository | Version-controlled system prompts with deployment tracking |
| Model catalog | Browsable model registry with capability metadata and pricing |
| Custom pricing overrides | Per-model pricing configuration for accurate cost attribution |
| LiteLLM / LangChain compat | Framework-specific endpoint formats |
| Data connectors | Direct log export to BigQuery, Datadog, etc. |
| Model parameter presets | Saved parameter configurations for common use cases |

---

## 6. OSS vs Enterprise Feature Split

### 6.1 Principle

> **OSS** is production-ready for single-team or SMB use. **Enterprise** adds controls required by regulated industries and large organizations.

### 6.2 Feature Matrix

| Feature | OSS | Enterprise |
|---------|-----|-----------|
| All inference endpoints | ✅ | ✅ |
| Provider management | ✅ | ✅ |
| Virtual keys + basic budgets | ✅ | ✅ |
| Routing rules (CEL) | ✅ | ✅ |
| Request logging | ✅ | ✅ |
| Prometheus metrics | ✅ | ✅ |
| OpenTelemetry | ✅ | ✅ |
| Semantic caching | ✅ | ✅ |
| Plugin system | ✅ | ✅ |
| MCP gateway | ✅ | ✅ |
| Async inference | ✅ | ✅ |
| SQLite persistence | ✅ | ✅ |
| PostgreSQL persistence | ✅ | ✅ |
| Helm chart | ✅ | ✅ |
| Governance hierarchy (team/customer) | ✅ | ✅ |
| RBAC | ❌ | ✅ |
| Audit logs | ❌ | ✅ |
| Guardrails | ❌ | ✅ |
| PII redactor | ❌ | ✅ |
| SCIM / SSO | ❌ | ✅ |
| Adaptive routing | ❌ | ✅ |
| Multi-node clustering | ❌ | ✅ |
| Alert channels | ❌ | ✅ |
| Vault support | ❌ | ✅ |
| Large payload optimization | ❌ | ✅ |
| MCP tool groups | ❌ | ✅ |
| User groups | ❌ | ✅ |
| Data connectors (BigQuery, Datadog) | ❌ | ✅ |

### 6.3 Implementation Mechanism

OSS and enterprise ship from the same codebase. Enterprise features are gated at build time via Webpack alias substitution (`@enterprise →` enterprise module) in the UI, and via license checks at runtime in the Go backend.

OSS users see upgrade prompts for enterprise features — never broken pages or errors.

---

## 7. Constraints & Non-Goals

### 7.1 Constraints

| Constraint | Rationale |
|-----------|-----------|
| The core engine must have zero dependency on framework/transports | Go SDK users need to import `core/` alone |
| The gateway must ship as a single binary with the UI embedded | Zero-config startup requirement; no separate web server |
| API keys must never be returned in plain text after initial creation | Security baseline |
| SQLite must remain the default (no external DB required) | Zero-config startup; operators who need multi-node opt in to PostgreSQL |
| HTTP overhead must stay ≤ 20 µs at 5k RPS | Performance is a core differentiator; measured in CI benchmarks |

### 7.2 Non-Goals (v1.0)

These are explicitly out of scope for the current version:

- **Training or fine-tuning workflows** — Bifrost is an inference gateway, not a training platform
- **Prompt management with A/B testing** — the prompt repository stores prompts but does not run experiments
- **Built-in vector database** — Bifrost integrates with external vector stores; it does not ship one
- **GraphQL API** — the management API is REST only
- **Multi-tenant SaaS** — Bifrost is self-hosted; cloud-hosted SaaS is not on the v1 roadmap
- **Model serving** — Bifrost routes to provider APIs; it does not host model inference itself (vLLM/Ollama are supported as providers)

---

## 8. Go-to-Market Considerations

### 8.1 Distribution

| Channel | Priority |
|---------|---------|
| `npx -y @maximhq/bifrost` | P0 — lowest possible friction for first run |
| `docker run maximhq/bifrost` | P0 — standard production deployment |
| Helm chart on Artifact Hub | P1 — Kubernetes-native teams |
| Go module `go get github.com/maximhq/bifrost/core` | P1 — SDK embedding |
| Homebrew / apt | P2 — local development convenience |

### 8.2 Open Core Strategy

- Core gateway and observability features are Apache 2.0 open source
- Enterprise features are proprietary; licensed per deployment
- All contributions to `core/`, `framework/`, and `plugins/` are subject to CLA

### 8.3 Developer Adoption Funnel

```
Discovery (GitHub, docs.getbifrost.ai, blog)
    ↓
Zero-config first run (npx / docker)
    ↓
First provider configured (< 5 min via UI)
    ↓
First API call through Bifrost
    ↓
First cost/usage insight from dashboard
    ↓
Team adoption (multiple virtual keys, governance)
    ↓
Enterprise evaluation (guardrails, RBAC, clustering)
```

---

## 9. Dependencies & Risks

### 9.1 External Dependencies

| Dependency | Purpose | Risk |
|------------|---------|------|
| LLM provider APIs | Core functionality | Provider API changes break adapters |
| FastHTTP | HTTP performance | Major version breaking changes |
| Gorm + SQLite/PostgreSQL | Persistence | DB driver compatibility on edge platforms |
| Next.js 15 | Control plane UI | Framework upgrade churn |
| Radix UI | Accessible UI primitives | Library breaking changes |

### 9.2 Risks

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|-----------|
| Provider API format changes | Medium | High | Per-provider adapters isolated; automated integration tests against live APIs |
| Performance regression | Low | High | Benchmark CI gate blocks PRs that increase overhead > 5 µs |
| Security vulnerability in dependency | Low | High | Snyk scanning in CI; `dependabot` for automatic updates |
| SQLite concurrency limits | Medium | Medium | Document PostgreSQL migration path; multi-node requires PostgreSQL |
| Enterprise feature complexity increasing OSS binary size | Low | Low | Build-time separation via Webpack alias; Go tree-shaking |

---

## 10. Release Criteria

### 10.1 OSS v1.0 Readiness

Before tagging a stable v1.0:

- [ ] All P0 features implemented and tested end-to-end
- [ ] All 20+ provider adapters passing integration tests
- [ ] Performance benchmark: ≤ 20 µs overhead at 5,000 RPS
- [ ] Zero known data loss or data corruption bugs
- [ ] UI passes manual UX review on Chrome, Firefox, Safari
- [ ] Helm chart installs cleanly on Kubernetes 1.28+
- [ ] Security scan (Snyk) shows no critical/high vulnerabilities
- [ ] Documentation covers: quick start, provider configuration, governance, plugins
- [ ] `npx -y @maximhq/bifrost` tested on macOS and Linux

### 10.2 Enterprise v1.0 Readiness

In addition to OSS criteria:

- [ ] RBAC fully tested with role assignment and permission enforcement
- [ ] Audit logs persisted durably and queryable by admin
- [ ] Guardrails tested with keyword, regex, and PII detection rules
- [ ] SCIM provisioning tested with Okta and Azure AD
- [ ] Multi-node clustering tested with 3-node PostgreSQL-backed deployment
- [ ] Adaptive routing benchmarked against manual routing rules
- [ ] License enforcement verified (gated features unavailable without valid license)

---

*This PRD reflects the implemented state of Bifrost as of 2026-04-05, derived from source code, API documentation, and user persona analysis.*
