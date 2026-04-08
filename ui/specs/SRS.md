# Software Requirements Specification (SRS)
## Bifrost — AI Proxy Gateway

**Version:** 1.0  
**Date:** 2026-04-05  
**Status:** Draft

---

## Table of Contents

1. [Introduction](#1-introduction)
2. [Overall Description](#2-overall-description)
3. [Functional Requirements](#3-functional-requirements)
4. [Non-Functional Requirements](#4-non-functional-requirements)
5. [System Architecture](#5-system-architecture)
6. [UI Component Requirements](#6-ui-component-requirements)
7. [Data Requirements](#7-data-requirements)
8. [External Interface Requirements](#8-external-interface-requirements)
9. [Constraints & Assumptions](#9-constraints--assumptions)

---

## 1. Introduction

### 1.1 Purpose
This document specifies the software requirements for **Bifrost**, an enterprise AI proxy gateway that routes, manages, and observes LLM API traffic across multiple providers. The UI is a Next.js 15 web application serving as the control plane for the gateway.

### 1.2 Scope
Bifrost provides:
- A unified proxy interface for multiple LLM providers (OpenAI, Anthropic, Google Vertex, Azure, etc.)
- Traffic routing, load balancing, and failover across providers
- Observability: real-time logs, audit trails, usage metrics
- Governance: RBAC, virtual API keys, rate limiting, guardrails
- MCP (Model Context Protocol) integration for tool-augmented AI workflows
- Plugin extensibility for custom transformations and integrations

### 1.3 Definitions & Acronyms

| Term | Definition |
|------|-----------|
| LLM | Large Language Model |
| MCP | Model Context Protocol — a protocol for AI tool integration |
| RBAC | Role-Based Access Control |
| CEL | Common Expression Language — used in routing rules |
| SCIM | System for Cross-domain Identity Management |
| RTK | Redux Toolkit (state management) |
| PII | Personally Identifiable Information |

---

## 2. Overall Description

### 2.1 Product Perspective
Bifrost sits between API consumers (developers, applications) and LLM providers. The UI control plane enables administrators to configure, monitor, and govern all proxy traffic without editing configuration files directly.

```
Clients → [Bifrost Proxy Gateway] → [LLM Providers]
                    ↑
            [UI Control Plane]
```

### 2.2 User Classes

| Class | Description | Access Level |
|-------|-------------|-------------|
| Super Admin | Full system control | All features |
| Admin | Manages providers, routing, keys | Most features |
| Developer | Reads logs, manages own keys | Logs, keys |
| Viewer | Read-only dashboard access | Dashboard, logs (read) |

### 2.3 Operating Environment
- **Frontend:** Next.js 15, React 19, TypeScript 5, Tailwind CSS 4
- **Runtime:** Node.js 20+, browser (Chrome 120+, Firefox 120+, Safari 17+)
- **Backend:** Go-based proxy server
- **Communication:** REST API + WebSocket for real-time updates
- **Auth:** Session-based, SCIM-compatible

---

## 3. Functional Requirements

### 3.1 Authentication & Session Management

| ID | Requirement |
|----|-------------|
| AUTH-01 | The system shall provide a login page accessible at `/login` |
| AUTH-02 | Sessions shall persist across browser refreshes |
| AUTH-03 | Unauthenticated users shall be redirected to `/login` |
| AUTH-04 | The system shall display a "No Permission" view for unauthorized feature access |
| AUTH-05 | SCIM provisioning shall be configurable for enterprise identity providers |

### 3.2 Dashboard

| ID | Requirement |
|----|-------------|
| DASH-01 | The dashboard shall display real-time traffic summary (requests/min, error rate, latency) |
| DASH-02 | The dashboard shall show provider health status |
| DASH-03 | The dashboard shall be the default landing page after login |
| DASH-04 | Metrics shall update without full page reload via WebSocket |

### 3.3 Provider Management

| ID | Requirement |
|----|-------------|
| PROV-01 | Admins shall be able to add, edit, and delete LLM provider configurations |
| PROV-02 | Each provider shall support: API key (plain or `env.VAR` reference), base URL, model list, request type filters |
| PROV-03 | API keys shall be redacted in the UI after saving (show `xxxx****xxxx` pattern) |
| PROV-04 | The system shall validate provider-specific fields before saving |
| PROV-05 | Vertex AI providers shall accept service account JSON or `env.` references as credentials |
| PROV-06 | The system shall show which request types (chat, embeddings, image, etc.) each provider supports |
| PROV-07 | Azure providers shall accept deployment maps as JSON or redacted values |
| PROV-08 | Provider forms shall display live validation errors on blur |

### 3.4 Routing Rules

| ID | Requirement |
|----|-------------|
| ROUTE-01 | Admins shall create routing rules using CEL (Common Expression Language) expressions |
| ROUTE-02 | Rules shall support conditions on: model name, request headers, metadata, user attributes |
| ROUTE-03 | Rules shall specify target providers and weights for load balancing |
| ROUTE-04 | The rule builder shall provide a visual query interface for non-technical users |
| ROUTE-05 | Rules shall have a priority order and be evaluated top-down |
| ROUTE-06 | The system shall validate CEL expressions before saving |

### 3.5 Logs & Observability

| ID | Requirement |
|----|-------------|
| LOG-01 | The system shall display real-time request/response logs |
| LOG-02 | Logs shall be filterable by: provider, model, status, time range, user, custom metadata |
| LOG-03 | Each log entry shall show: timestamp, provider, model, tokens used, latency, status |
| LOG-04 | Logs shall be paginated with configurable page size adapting to viewport height |
| LOG-05 | Log detail view shall show full request and response bodies |
| LOG-06 | Audit logs shall record all admin configuration changes (enterprise feature) |
| LOG-07 | MCP logs shall be separately viewable for tool-call tracing |
| LOG-08 | The filter UI shall support saving and reusing filter presets |

### 3.6 Virtual Keys (API Key Management)

| ID | Requirement |
|----|-------------|
| KEY-01 | Admins shall create virtual API keys that proxy to real provider keys |
| KEY-02 | Each virtual key shall support rate limits (requests per minute, tokens per minute) |
| KEY-03 | Virtual keys shall support expiry dates |
| KEY-04 | Keys shall be associated with specific providers or allow any provider |
| KEY-05 | Created key values shall be shown once and never again |
| KEY-06 | Keys shall be revocable immediately |

### 3.7 Governance & RBAC

| ID | Requirement |
|----|-------------|
| GOV-01 | Admins shall define roles with granular feature-level permissions |
| GOV-02 | Users shall be assigned one or more roles |
| GOV-03 | Role assignments shall take effect immediately without re-login |
| GOV-04 | The system shall provide a user management interface |
| GOV-05 | User groups shall be supported for bulk role assignment |
| GOV-06 | RBAC configuration shall be exportable and importable |

### 3.8 Model Limits & Quotas

| ID | Requirement |
|----|-------------|
| LIMIT-01 | Admins shall set per-model token and request quotas |
| LIMIT-02 | Limits shall be configurable per user, per key, or globally |
| LIMIT-03 | The system shall display current usage vs. limit in real-time |
| LIMIT-04 | Exceeding a limit shall return a standard HTTP 429 response to the client |

### 3.9 MCP (Model Context Protocol) Integration

| ID | Requirement |
|----|-------------|
| MCP-01 | The system shall support registering external MCP servers |
| MCP-02 | Each MCP server shall have configurable authentication (bearer token, API key, OAuth) |
| MCP-03 | MCP tools shall be browsable and assignable to providers or routing rules |
| MCP-04 | MCP tool groups shall allow bundling related tools for assignment |
| MCP-05 | MCP call logs shall be separately queryable from request logs |
| MCP-06 | The MCP registry shall show server health and tool availability |

### 3.10 Guardrails

| ID | Requirement |
|----|-------------|
| GUARD-01 | Admins shall define content safety rules applied to requests and responses |
| GUARD-02 | Rules shall support: keyword blocking, regex patterns, PII detection |
| GUARD-03 | Guardrail violations shall be logged with the offending content redacted |
| GUARD-04 | Guardrails shall be assignable to specific providers or globally |

### 3.11 PII Redactor

| ID | Requirement |
|----|-------------|
| PII-01 | The system shall detect and redact PII in logs before storage |
| PII-02 | Redaction patterns shall be configurable (regex, entity types) |
| PII-03 | The configuration UI shall allow testing redaction rules against sample text |

### 3.12 Prompt Repository

| ID | Requirement |
|----|-------------|
| PROMPT-01 | Admins shall create and version system prompts |
| PROMPT-02 | Prompts shall support template variables using `{{ variable }}` syntax |
| PROMPT-03 | Prompts shall be deployable to specific providers or routing rules |
| PROMPT-04 | Prompt deployments shall track active version and allow rollback |
| PROMPT-05 | The prompt editor shall support syntax highlighting and variable preview |

### 3.13 Plugins

| ID | Requirement |
|----|-------------|
| PLUG-01 | The system shall support configurable plugins for request/response transformation |
| PLUG-02 | Each plugin shall have a configuration schema rendered as a form |
| PLUG-03 | Plugins shall be enableable/disableable without restarting the proxy |
| PLUG-04 | The UI shall list available plugins with descriptions and configuration status |

### 3.14 Adaptive Routing

| ID | Requirement |
|----|-------------|
| ADAPT-01 | The system shall support AI-driven adaptive routing based on provider performance |
| ADAPT-02 | Adaptive routing shall consider: latency, error rate, cost per token |
| ADAPT-03 | Routing decisions shall be visible in the observability dashboard |

### 3.15 Alert Channels

| ID | Requirement |
|----|-------------|
| ALERT-01 | Admins shall configure alert channels (webhook, Slack, PagerDuty, etc.) |
| ALERT-02 | Alerts shall fire on: provider errors, quota breaches, guardrail violations |
| ALERT-03 | Each alert channel shall be testable from the UI |

### 3.16 Custom Pricing

| ID | Requirement |
|----|-------------|
| PRICE-01 | Admins shall define custom per-token pricing for cost reporting |
| PRICE-02 | Custom pricing shall be configurable per model |
| PRICE-03 | Cost calculations shall appear in logs and dashboards |

### 3.17 Configuration Management

| ID | Requirement |
|----|-------------|
| CONF-01 | The system shall provide proxy performance tuning settings (timeouts, buffer sizes, concurrency) |
| CONF-02 | Configuration changes shall show a sync status indicator |
| CONF-03 | Client settings (allowed origins, auth modes) shall be configurable from the UI |
| CONF-04 | Large payload optimization settings shall be configurable |

### 3.18 Cluster Management

| ID | Requirement |
|----|-------------|
| CLUSTER-01 | Multi-node cluster status shall be viewable from the UI (enterprise feature) |
| CLUSTER-02 | Node health and load distribution shall be visible per node |

---

## 4. Non-Functional Requirements

### 4.1 Performance

| ID | Requirement |
|----|-------------|
| PERF-01 | The UI shall load the initial dashboard in under 2 seconds on a standard connection |
| PERF-02 | Log table pagination shall render within 300ms |
| PERF-03 | Real-time log updates shall have less than 500ms latency via WebSocket |
| PERF-04 | The table page size shall auto-adjust to viewport height using ResizeObserver |
| PERF-05 | Debounce delay on search inputs shall be ≤ 300ms to prevent excessive API calls |

### 4.2 Usability

| ID | Requirement |
|----|-------------|
| USE-01 | The UI shall support dark and light themes with system preference detection |
| USE-02 | All form fields shall show inline validation messages on blur |
| USE-03 | Destructive actions (delete, revoke) shall require confirmation dialogs |
| USE-04 | Tables shall support column reordering and pinning |
| USE-05 | Mobile viewports (< 768px) shall display a responsive layout |
| USE-06 | Loading states shall be communicated with skeleton loaders or spinners |

### 4.3 Security

| ID | Requirement |
|----|-------------|
| SEC-01 | API keys shall never appear in plain text after initial creation |
| SEC-02 | All API requests from the UI shall be authenticated via session token |
| SEC-03 | CORS origins shall be strictly validated against `isValidOrigin` rules |
| SEC-04 | Env variable references (`env.VAR`) shall be supported to avoid storing secrets in config |
| SEC-05 | PII detection shall be applied to logs before persistence |

### 4.4 Reliability

| ID | Requirement |
|----|-------------|
| REL-01 | WebSocket connections shall reconnect with exponential backoff on disconnection |
| REL-02 | API errors shall display user-friendly toast notifications |
| REL-03 | Enterprise features unavailable in the current license shall show a fallback view, not an error |

### 4.5 Maintainability

| ID | Requirement |
|----|-------------|
| MAINT-01 | All utility functions shall have corresponding unit tests |
| MAINT-02 | Component variants shall be managed with class-variance-authority (CVA) |
| MAINT-03 | API integrations shall use RTK Query for caching and invalidation |
| MAINT-04 | Validation logic shall use the `Validator` class for consistency |

### 4.6 Accessibility

| ID | Requirement |
|----|-------------|
| ACC-01 | Interactive components shall use Radix UI primitives for ARIA compliance |
| ACC-02 | Color contrast shall meet WCAG 2.1 AA for both light and dark themes |
| ACC-03 | All form fields shall have associated `<label>` elements |

---

## 5. System Architecture

### 5.1 Frontend Stack

```
Next.js 15 (App Router)
├── React 19 + TypeScript 5
├── Tailwind CSS 4 (styling)
├── Radix UI (accessible primitives)
├── Redux Toolkit + RTK Query (state + API)
├── Zustand (component-local state)
├── React Hook Form + Zod (forms + validation)
├── TanStack Table (data tables)
├── Monaco Editor (code editing)
├── Recharts (charts)
└── Sonner (toast notifications)
```

### 5.2 State Management Strategy

| Layer | Tool | Use Case |
|-------|------|----------|
| Server state / API cache | RTK Query | All remote data fetching |
| Global app state | Redux Toolkit | Auth, app-level flags |
| UI / ephemeral state | Zustand | Form state, modals |
| Real-time data | WebSocket + Context | Logs, metrics streams |

### 5.3 Routing Structure

```
/                    → redirects to /workspace/dashboard
/login               → authentication
/workspace/
  dashboard          → overview metrics
  providers          → LLM provider management
  logs               → request logs
  routing-rules      → CEL routing rules
  virtual-keys       → API key management
  governance         → users and roles
  rbac               → role definitions (enterprise)
  guardrails         → content safety rules
  pii-redactor       → PII detection config
  plugins            → plugin management
  mcp-settings       → MCP server config
  mcp-registry       → MCP server registry
  mcp-logs           → MCP call logs
  mcp-tool-groups    → tool group management
  mcp-auth-config    → MCP authentication
  prompt-repo        → prompt management
  model-limits       → quota management
  model-catalog      → model registry
  observability      → metrics and tracing
  adaptive-routing   → AI-driven routing
  alert-channels     → notification config
  custom-pricing     → cost configuration
  cluster            → multi-node management (enterprise)
  audit-logs         → admin audit trail (enterprise)
  scim               → identity provisioning (enterprise)
  config             → proxy configuration
/pprof               → Go profiling (admin)
```

---

## 6. UI Component Requirements

### 6.1 Form Validation

All forms shall use the `Validator` class with the following standard rules:

| Validator | Behavior |
|-----------|----------|
| `Validator.required(value)` | Fails for `null`, `undefined`, `""`, `0` |
| `Validator.email(value)` | RFC 5321 email format |
| `Validator.url(value)` | Must start with `http://` or `https://` |
| `Validator.minLength(value, n)` | String length ≥ n |
| `Validator.maxLength(value, n)` | String length ≤ n |
| `Validator.minValue(value, n)` | Numeric value ≥ n |
| `Validator.maxValue(value, n)` | Numeric value ≤ n |
| `Validator.custom(bool, msg)` | Arbitrary condition |

### 6.2 Button Component

The Button component shall support:
- Variants: `default`, `destructive`, `outline`, `secondary`, `ghost`, `link`
- Sizes: `default`, `sm`, `lg`, `icon`
- `isLoading` prop showing a spinner and disabling interaction
- `dataTestId` prop for automated testing

### 6.3 Data Tables

All data tables shall:
- Calculate page size dynamically based on container height (`useTablePageSize`)
- Support column reordering via drag-and-drop
- Support column pinning (left/right)
- Show loading skeletons while data is fetching

### 6.4 Template Variables

Prompt and configuration fields supporting template variables shall:
- Extract variables matching `{{ variable_name }}` pattern
- Support dot-notation: `{{ user.email }}`
- Support underscore names: `{{ api_key }}`
- Deduplicate repeated variables
- Not extract Jinja2-style `{% %}` blocks as variables

### 6.5 Real-Time Updates

Features using real-time data (logs, metrics) shall:
- Connect via WebSocket with subscription-based message routing
- Reconnect automatically with exponential backoff on failure
- Send heartbeat pings to detect stale connections
- Gracefully degrade to polling if WebSocket is unavailable

---

## 7. Data Requirements

### 7.1 API Key Storage

- Keys are stored server-side; the UI never persists raw keys
- Display format after save: first 4 chars + 24 asterisks + last 4 chars (32 chars total)
- Short keys (≤ 8 chars): displayed as all asterisks
- `env.VARIABLE_NAME` references are valid alternatives to literal keys

### 7.2 Origin Validation

Allowed CORS origins must satisfy one of:
- Exact `*` wildcard
- `http://` or `https://` with hostname + optional port, no path/query/fragment
- Wildcard subdomain: `https://*.example.com`

### 7.3 Redis Address Formats

Cache/session Redis addresses shall be accepted in these formats:
- `host:port`
- `[IPv6]:port`
- `redis://host:port`
- `rediss://host:port` (TLS)

### 7.4 Byte Formatting

Storage and bandwidth values shall be displayed in human-readable format with one decimal place: `B`, `KB`, `MB`, `GB`, `TB`.

---

## 8. External Interface Requirements

### 8.1 Backend API

- REST API over HTTP/HTTPS
- Base URL configurable via environment variable at build time
- Authentication via session cookie
- Standard JSON request/response bodies
- RTK Query manages caching, invalidation, and optimistic updates

### 8.2 WebSocket

- Endpoint for real-time log and metric streaming
- Message format: JSON with `type` and `payload` fields
- Subscription model: clients subscribe to named channels
- Reconnection: exponential backoff (1s, 2s, 4s, … up to 30s cap)

### 8.3 MCP Servers

- External MCP servers communicate over HTTP
- Authentication: Bearer token, API key header, or OAuth 2.0
- Tool schemas are fetched from servers and cached in the UI

### 8.4 Identity Providers (SCIM)

- SCIM 2.0 protocol for user provisioning
- Supports Okta, Azure AD, and generic SCIM providers

---

## 9. Constraints & Assumptions

### 9.1 Constraints

- Enterprise features (RBAC, audit logs, SCIM, cluster management, large payload optimization, adaptive routing, alert channels, PII redactor, MCP auth, user groups, MCP tool groups) require a valid enterprise license; unlicensed deployments show fallback views.
- The UI requires JavaScript to be enabled; it is a client-rendered SPA after the initial SSR shell.
- Provider API keys are never transmitted back to the browser after initial save; only redacted values are returned.

### 9.2 Assumptions

- The backend Go proxy server is running and reachable from the UI server.
- WebSocket connectivity is available (not blocked by proxy/firewall).
- Browser local storage is available for theme preferences.
- The deployment environment supports Next.js 15 SSR (Node.js 20+).

---

*This SRS is derived from the Bifrost UI source code at `/ui/` and reflects the implemented feature set as of 2026-04-05.*
