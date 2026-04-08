# Enterprise Missing Features — Gap Analysis

**Source:** [PRD.md](../PRD.md) — Section 5.3 (P2) & Section 6.2 Feature Matrix  
**Date:** 2026-04-08  
**Status:** Draft  
**Scope:** Features marked **Enterprise-only (❌ OSS / ✅ Enterprise)** in the PRD Feature Matrix

---

## Summary

| # | Feature | PRD Priority | Backend Status | Frontend Status | Plugin Status |
|---|---------|-------------|----------------|-----------------|---------------|
| 1 | RBAC | P2 | ❌ Not implemented | ⚠️ Fallback UI only | ❌ Missing |
| 2 | Audit Logs | P2 | ❌ Not implemented | ⚠️ Fallback UI only | ❌ Missing |
| 3 | Guardrails | P2 | ❌ Not implemented | ⚠️ Fallback UI only | ❌ Missing |
| 4 | PII Redactor | P2 | ❌ Not implemented | ⚠️ Fallback UI only | ❌ Missing |
| 5 | SCIM / SSO | P2 | ❌ Not implemented | ⚠️ Fallback UI only | ❌ Missing |
| 6 | Adaptive Routing | P2 | ❌ Not implemented | ⚠️ Fallback UI only | ❌ Missing |
| 7 | Multi-node Clustering | P2 | ❌ Not implemented | ⚠️ Fallback UI only | ❌ Missing |
| 8 | Alert Channels | P2 | ❌ Not implemented | ⚠️ Fallback UI only | ❌ Missing |
| 9 | Vault Support | P2 | ❌ Not implemented | ❌ No UI | ❌ Missing |
| 10 | Large Payload Optimization | P2 | ❌ Not implemented | ⚠️ Fallback UI only | ❌ Missing |
| 11 | MCP Tool Groups | Matrix | ❌ Not implemented | ⚠️ Fallback UI only | ❌ Missing |
| 12 | User Groups | Matrix | ❌ Not implemented | ⚠️ Fallback UI only | ❌ Missing |
| 13 | Data Connectors (BigQuery, Datadog) | Matrix | ❌ Not implemented | ⚠️ Fallback UI only | ❌ Missing |
| 14 | License Enforcement | Section 6.3 | ❌ Not implemented | ❌ Not implemented | ❌ Missing |

> **Key:** ❌ Not implemented | ⚠️ OSS fallback/placeholder UI (shows "Contact Us" upgrade prompt) | ✅ Implemented

---

## How Enterprise Features Are Gated (Current Architecture)

Per PRD Section 6.3, enterprise features should be gated via:
- **UI:** Webpack alias `@enterprise → app/enterprise/` (falls back to `app/_fallbacks/enterprise/` in OSS)
- **Backend:** Runtime license checks in Go

**Current state:** The OSS fallback components exist (`ui/app/_fallbacks/enterprise/components/`) and show "Contact Us" upgrade prompts correctly. However, **no `app/enterprise/` directory exists** — the enterprise module itself is absent. On the backend, **no license check mechanism exists** anywhere in `transports/`, `plugins/`, or `core/`.

---

## Feature-by-Feature Details

---

### 1. RBAC (Role-Based Access Control)

**PRD Requirement (P2):** Role-based access control for UI and API. Required for Enterprise v1.0 (Section 10.2).

**Current State:**
- UI: `/workspace/rbac/page.tsx` redirects to `/workspace/governance/rbac` which imports `@enterprise/components/rbac/rbacView` — falls back to "Unlock roles and permissions" CTA.
- Backend: No RBAC-related endpoints in `governance.go`, `middlewares.go`, or `session.go`. No role/permission tables in configstore.
- No middleware that checks caller role before allowing API operations.

**Missing:**
- [ ] **Backend:** Role and permission schemas (`TableRole`, `TablePermission`, `TableUserRole`)
- [ ] **Backend:** RBAC middleware — checks `Authorization` token against role table before every `/api/*` route
- [ ] **Backend:** REST endpoints: `GET/POST/PUT/DELETE /api/rbac/roles`, `GET/POST/DELETE /api/rbac/roles/{id}/permissions`, `POST/DELETE /api/rbac/users/{id}/roles`
- [ ] **Backend:** Role assignment: owner, admin, viewer, operator (minimum set)
- [ ] **UI:** Full RBAC management page (`app/enterprise/components/rbac/rbacView.tsx`) with role listing, role creation, user-role assignment
- [ ] **Plugin/middleware:** `HTTPTransportPlugin` pre-hook that enforces RBAC per endpoint

---

### 2. Audit Logs

**PRD Requirement (P2):** Immutable record of all admin configuration changes. Required for Enterprise v1.0 compliance.

**Current State:**
- UI: `/workspace/audit-logs/page.tsx` imports `@enterprise/components/audit-logs/auditLogsView` — falls back to "Unlock audit logs for better compliance" CTA.
- Backend: `logging.go` handles **request/response logs** (LLM inference). No handler or schema for **admin action audit logs** (config changes, key creation, user actions).
- No write-ahead / append-only audit trail in configstore.

**Missing:**
- [ ] **Backend:** `TableAuditLog` schema with fields: `actor_id`, `action`, `resource_type`, `resource_id`, `before_state` (JSON), `after_state` (JSON), `ip_address`, `timestamp`
- [ ] **Backend:** Audit log writer — hook into every `POST/PUT/DELETE` governance handler to append an audit entry
- [ ] **Backend:** REST endpoint: `GET /api/audit-logs` with filters (actor, resource_type, date range), pagination
- [ ] **Backend:** Immutability guarantee — audit records must be append-only (no `UPDATE`/`DELETE` in DB layer)
- [ ] **UI:** Full audit log viewer with timeline view, actor filter, resource filter, diff viewer for before/after state

---

### 3. Guardrails

**PRD Requirement (P2):** Content safety rules (keyword, regex, PII detection) on requests and responses.

**Current State:**
- UI: `/workspace/guardrails/` imports `@enterprise/components/guardrails/guardrailsConfigurationView` — falls back to CTA.
- Backend: No guardrail handler, schema, or plugin. Grep for `guardrail` in `core/` returns only unrelated Bedrock/OpenRouter results.
- No pre/post LLM hook that inspects content against rules.

**Missing:**
- [ ] **Plugin:** New `guardrails` plugin (`plugins/guardrails/`) implementing `LLMPlugin` interface
  - `PreLLMHook`: scan request content against configured rules; block + return error on violation
  - `PostLLMHook`: scan response content; optionally redact or block
- [ ] **Backend:** `TableGuardrailRule` schema: `type` (keyword/regex/pii), `pattern`, `action` (block/warn/redact), `scope` (request/response/both), `provider_filter`
- [ ] **Backend:** REST endpoints: `GET/POST /api/guardrails/rules`, `PUT/DELETE /api/guardrails/rules/{id}`
- [ ] **UI:** Guardrail rule builder — add/edit/delete rules with live test input, violation preview
- [ ] **UI:** Provider-specific guardrail override page (`/workspace/guardrails/providers`)

---

### 4. PII Redactor

**PRD Requirement (P2):** Configurable PII detection + redaction before log storage.

**Current State:**
- UI: `/workspace/pii-redactor/` has two sub-routes (`/rules`, `/providers`) but both import from `@enterprise/components/pii-redactor/*` — all fall back to CTA.
- Backend: No PII handler or plugin anywhere. Grep for `pii` in `plugins/` returns only Go module checksums.
- The `logging` plugin stores raw request/response bodies with no redaction step.

**Missing:**
- [ ] **Plugin:** New `piiredactor` plugin (`plugins/piiredactor/`) implementing `LLMPlugin`
  - `PreLLMHook`: detect & redact PII from request content before forwarding
  - `PostLLMHook`: redact PII from response before handing to logging plugin
- [ ] **Backend:** `TablePIIRule` schema: `entity_type` (EMAIL, PHONE, SSN, CREDIT_CARD, CUSTOM), `regex_override`, `redaction_mode` (mask/hash/remove), `enabled`
- [ ] **Backend:** PII detection engine — regex-based by default; pluggable NLP backend for `pii_detection_mode: ml`
- [ ] **Backend:** REST endpoints: `GET/POST /api/pii/rules`, `PUT/DELETE /api/pii/rules/{id}`, `GET/PUT /api/pii/providers` (per-provider overrides)
- [ ] **UI:** PII rule management page; provider-specific enable/disable toggles

---

### 5. SCIM / SSO Provisioning

**PRD Requirement (P2):** Automated user provisioning from enterprise IdPs (Okta, Azure AD).

**Current State:**
- UI: `/workspace/scim/page.tsx` imports `@enterprise/components/scim/scimView` — CTA fallback.
- Backend: `session.go` and `oauth2.go` handle basic session/OAuth2 login. No SCIM protocol endpoints, no SAML/OIDC SSO flow, no IdP directory sync.
- No user directory model (Bifrost currently has no persistent user table beyond session tokens).

**Missing:**
- [ ] **Backend:** User directory model: `TableUser` (id, email, name, external_id, idp_source, roles)
- [ ] **Backend:** SCIM 2.0 endpoints: `GET/POST /scim/v2/Users`, `GET/PUT/PATCH/DELETE /scim/v2/Users/{id}`, `GET /scim/v2/Groups`, provisioning webhooks
- [ ] **Backend:** SAML 2.0 SP metadata endpoint (`/auth/saml/metadata`) + assertion consumer (`/auth/saml/acs`)
- [ ] **Backend:** OIDC SSO: configuration store for IdP `client_id`, `client_secret`, `discovery_url`; callback handler
- [ ] **Backend:** Just-in-time (JIT) provisioning — create/update user on first SSO login
- [ ] **UI:** SCIM configuration page (token management, IdP setup), SSO settings page

---

### 6. Adaptive Routing

**PRD Requirement (P2):** AI-driven routing that considers provider latency, error rate, and cost in real-time.

**Current State:**
- UI: `/workspace/adaptive-routing/page.tsx` imports `@enterprise/components/adaptive-routing/adaptiveRoutingView` — CTA fallback.
- Backend: `plugins/governance/routing.go` implements CEL-based **static** routing rules. Grep for `adaptive`, `latency`, `error rate` in routing code returns no results.
- No provider health metric collection for programmatic routing decisions.

**Missing:**
- [ ] **Backend:** Provider metrics collector — per-provider sliding-window stats: p50/p95/p99 latency, error rate, token cost/req
- [ ] **Backend:** Adaptive routing engine in `plugins/governance/` — score providers on each request using collected metrics + configured weights (latency weight, error_rate weight, cost weight)
- [ ] **Backend:** `TableAdaptiveRoutingConfig` schema: weight ratios, sampling window size, min healthy threshold, fallback strategy
- [ ] **Backend:** REST endpoints: `GET/PUT /api/adaptive-routing/config`, `GET /api/adaptive-routing/stats` (per-provider live metrics)
- [ ] **UI:** Adaptive routing configuration UI — weight sliders, live provider health table, routing decision log

---

### 7. Multi-node Clustering

**PRD Requirement (P2):** Multi-node deployment with shared state and node health visibility.

**Current State:**
- UI: `/workspace/cluster/page.tsx` imports `@enterprise/components/cluster/clusterView` — CTA fallback.
- Backend: Bifrost is stateless per-node; governance plugin's in-memory `GovernanceData` is **local to each node**. No distributed cache, no node discovery, no shared state sync.
- PostgreSQL persistence exists but no cross-node invalidation/replication mechanism.

**Missing:**
- [ ] **Backend:** Cluster membership protocol — node registration + heartbeat (via PostgreSQL or Redis)
- [ ] **Backend:** Distributed cache invalidation — when a VK/rule changes on node A, all nodes must invalidate their in-memory cache
- [ ] **Backend:** Node health table: `TableClusterNode` (id, hostname, last_heartbeat, version, status)
- [ ] **Backend:** REST endpoints: `GET /api/cluster/nodes`, `GET /api/cluster/nodes/{id}/health`
- [ ] **Backend:** Distributed lock support for operations that must run once per cluster (budget reset, rate limit resets)
- [ ] **Infrastructure:** Redis adapter for cross-node pub/sub invalidation (optional dependency, activated by `clustering: true` in config)
- [ ] **UI:** Cluster topology view — node list, heartbeat status, per-node request throughput

---

### 8. Alert Channels

**PRD Requirement (P2):** Webhook/Slack/PagerDuty alerts on budget breach, provider errors, guardrail violations.

**Current State:**
- UI: `/workspace/alert-channels/page.tsx` imports `@enterprise/components/alert-channels/alertChannelsView` — CTA fallback.
- Backend: No alert handler, schema, or notification dispatcher in any handler or plugin. Alert on budget breach is not implemented even in the governance plugin's tracker.

**Missing:**
- [ ] **Backend:** `TableAlertChannel` schema: `type` (webhook/slack/pagerduty/email), `endpoint_url`, `auth_header`, `enabled`, `events[]`
- [ ] **Backend:** `TableAlertEvent` enum: `BUDGET_BREACH`, `BUDGET_WARNING` (80%), `PROVIDER_ERROR_RATE`, `GUARDRAIL_VIOLATION`, `RATE_LIMIT_HIT`
- [ ] **Backend:** Alert dispatcher service — triggered from governance tracker on threshold breach; sends async HTTP POST to configured channels
- [ ] **Backend:** REST endpoints: `GET/POST /api/alert-channels`, `PUT/DELETE /api/alert-channels/{id}`, `POST /api/alert-channels/{id}/test`
- [ ] **UI:** Alert channel configuration page — add/edit/delete channels, event subscription checkboxes, test button

---

### 9. Vault Support

**PRD Requirement (P2):** HashiCorp Vault integration for injecting API keys as secrets, replacing plaintext key storage.

**Current State:**
- UI: No Vault-specific UI page exists (no directory under `workspace/`).
- Backend: API keys are stored as encrypted strings in `configstore` DB. No Vault client, no dynamic secret lease/renewal.
- `config.schema.json` has no Vault configuration block.

**Missing:**
- [ ] **Backend:** Vault client integration (`github.com/hashicorp/vault/api`) in `framework/` or `transports/`
- [ ] **Backend:** `VaultConfig` schema: `address`, `auth_method` (token/approle/k8s), `role_id`, `secret_id`, `mount_path`
- [ ] **Backend:** Secret resolver — when provider key value starts with `vault://path`, resolve at startup or per-request
- [ ] **Backend:** Lease renewal daemon — renews dynamic secrets before expiry
- [ ] **Config:** `transports/config.schema.json` — add `vault` block to system configuration schema
- [ ] **UI:** Vault settings page under `/workspace/config/` or `/workspace/providers/` — configure Vault connection, test connectivity

---

### 10. Large Payload Optimization

**PRD Requirement (P2):** Streaming handling for multi-hundred-MB payloads (audio, video, batch file operations).

**Current State:**
- UI: `_fallbacks/enterprise/components/large-payload/` exists — CTA fallback only.
- Backend: Current streaming uses in-memory `chan *BifrostStreamChunk`. Audio/video file upload uses standard `fasthttp` body buffering — entire body held in memory.
- No chunked file upload, no streaming multipart handling for `POST /v1/audio/transcriptions` with large files.

**Missing:**
- [ ] **Backend:** Streaming multipart body reader in `core/providers/openai/` for file upload endpoints
- [ ] **Backend:** Backpressure-aware SSE consumer — avoid buffering entire response for `ResponsesStream` on large outputs
- [ ] **Backend:** Configurable `max_body_size` and `stream_threshold_bytes` in `NetworkConfig` per provider
- [ ] **Backend:** Chunked file transfer to providers that support it (Bedrock, OpenAI batch files)
- [ ] **Tests:** Load tests confirming 200MB+ audio file upload and transcription pipeline

---

### 11. MCP Tool Groups

**PRD Requirement (Feature Matrix):** Enterprise-only MCP tool grouping (reusable access control sets).

**Current State:**
- UI: `/workspace/mcp-tool-groups/page.tsx` imports `@enterprise/components/mcp-tool-groups/mcpToolGroups` — CTA fallback.
- Backend: MCP tool access is per-VK (`TableVirtualKeyMCPConfig.ToolsToExecute`). No concept of reusable "tool groups" assignable to multiple VKs.

**Missing:**
- [ ] **Backend:** `TableMCPToolGroup` schema: `id`, `name`, `description`, `tools[]` (list of tool names), `mcp_client_id`
- [ ] **Backend:** VK-to-ToolGroup link: `TableVirtualKeyMCPToolGroupConfig`
- [ ] **Backend:** REST endpoints: `GET/POST /api/mcp/tool-groups`, `PUT/DELETE /api/mcp/tool-groups/{id}`, `POST/DELETE /api/mcp/tool-groups/{id}/tools`
- [ ] **UI:** Tool group builder — create group, add tools from MCP client registry, assign groups to VKs

---

### 12. User Groups

**PRD Requirement (Feature Matrix):** Enterprise-only user grouping for bulk permission management.

**Current State:**
- UI: `_fallbacks/enterprise/components/user-groups/` exists — CTA fallback only. No `workspace/user-groups/` page either.
- Backend: No user group concept in any handler or schema. Depends on RBAC (#1) and SCIM (#5) being present first.

**Missing:**
- [ ] **Backend:** `TableUserGroup` schema: `id`, `name`, `description`, `roles[]`
- [ ] **Backend:** User-to-group membership: `TableUserGroupMember`
- [ ] **Backend:** REST endpoints: `GET/POST /api/user-groups`, `PUT/DELETE /api/user-groups/{id}`, group member management
- [ ] **UI:** User group management page — create groups, assign roles, add users
- [ ] **UI:** Workspace page `/workspace/user-groups/` (currently missing entirely)

> **Dependency:** User Groups require RBAC (#1) and SCIM (#5) to be meaningful.

---

### 13. Data Connectors (BigQuery, Datadog)

**PRD Requirement (Feature Matrix):** Direct log export to BigQuery, Datadog, and similar platforms.

**Current State:**
- UI: `_fallbacks/enterprise/components/data-connectors/` exists — CTA fallback only.
- Backend: No data connector handler, schema, or export pipeline. The `logging` plugin writes to local file/PostgreSQL only.
- `framework/logstore/` has `file` and `postgres` backends. No BigQuery, Datadog, S3, or Elasticsearch adapters.

**Missing:**
- [ ] **Backend:** `TableDataConnector` schema: `type` (bigquery/datadog/s3/elasticsearch), `credentials` (encrypted JSON), `enabled`, `sync_interval`, `dataset/index/bucket`
- [ ] **Backend:** BigQuery connector (`framework/logstore/bigquery/`) — streams inference logs to BQ table on flush
- [ ] **Backend:** Datadog connector (`framework/logstore/datadog/`) — ships logs via Datadog Logs API
- [ ] **Backend:** S3/GCS connector — periodic log batch export to object storage
- [ ] **Backend:** REST endpoints: `GET/POST /api/data-connectors`, `PUT/DELETE /api/data-connectors/{id}`, `POST /api/data-connectors/{id}/test`
- [ ] **UI:** Data connector configuration page — connector type picker, credential form, sync status indicator

---

### 14. License Enforcement

**PRD Requirement (Section 6.3):** Enterprise features gated at runtime via license checks in Go backend. OSS users see upgrade prompts, not broken pages or errors.

**Current State:**
- No license validation code exists anywhere in the codebase.
- UI correctly shows CTA for unlicensed features (via `_fallbacks/enterprise/`).
- Backend has no middleware or startup check that validates a license key.

**Missing:**
- [ ] **Backend:** License schema: `license_key` (JWT or signed payload), validated fields: `issued_to`, `features[]`, `expires_at`, `node_count`
- [ ] **Backend:** License validator — parse and verify signature at startup; store parsed claims in `lib.Config`
- [ ] **Backend:** Feature gate helper: `lib.IsFeatureEnabled(ctx, "rbac") bool` — checked at the top of each enterprise endpoint handler, returns 403 with upgrade message if not licensed
- [ ] **Config:** `transports/config.schema.json` — add `license_key` field to system config block
- [ ] **Backend:** License status endpoint: `GET /api/system/license` — returns licensed features, expiry, node limit
- [ ] **UI:** License status indicator in sidebar/settings; show licensed features vs. upgrade prompts per feature

---

## Cross-cutting Concerns

### Backend — No Enterprise Plugin Module
None of the 14 enterprise features have a corresponding Go plugin or handler implementation. New modules are needed:
- `plugins/guardrails/` — Guardrails plugin
- `plugins/piiredactor/` — PII Redactor plugin
- `plugins/adaptiverouting/` — Adaptive routing engine
- Per-feature handler files in `transports/bifrost-http/handlers/`

### UI — Enterprise Module is Absent
The `ui/app/enterprise/` directory does not exist. All `@enterprise/*` imports resolve to OSS fallbacks. The full enterprise UI module must be built and mounted at this path.

### Database — Missing Tables
The following tables are entirely absent from `framework/configstore/tables/`:

| Table | Used By |
|-------|---------|
| `audit_logs` | Audit Logs |
| `rbac_roles`, `rbac_permissions`, `rbac_user_roles` | RBAC |
| `guardrail_rules` | Guardrails |
| `pii_rules` | PII Redactor |
| `alert_channels`, `alert_events` | Alert Channels |
| `cluster_nodes` | Multi-node Clustering |
| `mcp_tool_groups`, `vk_mcp_tool_group_configs` | MCP Tool Groups |
| `users` | SCIM/SSO, RBAC, User Groups |
| `user_groups`, `user_group_members` | User Groups |
| `data_connectors` | Data Connectors |

Schema migrations for all of these need to be added to the GORM auto-migration chain in `framework/configstore/`.

---

## Reference: PRD Feature Matrix — Enterprise Column

| Feature | OSS | Enterprise | Implementation Status |
|---------|-----|------------|----------------------|
| RBAC | ❌ | ✅ | ❌ Not implemented |
| Audit logs | ❌ | ✅ | ❌ Not implemented |
| Guardrails | ❌ | ✅ | ❌ Not implemented |
| PII redactor | ❌ | ✅ | ❌ Not implemented |
| SCIM / SSO | ❌ | ✅ | ❌ Not implemented |
| Adaptive routing | ❌ | ✅ | ❌ Not implemented |
| Multi-node clustering | ❌ | ✅ | ❌ Not implemented |
| Alert channels | ❌ | ✅ | ❌ Not implemented |
| Vault support | ❌ | ✅ | ❌ Not implemented |
| Large payload optimization | ❌ | ✅ | ❌ Not implemented |
| MCP tool groups | ❌ | ✅ | ❌ Not implemented |
| User groups | ❌ | ✅ | ❌ Not implemented |
| Data connectors (BigQuery, Datadog) | ❌ | ✅ | ❌ Not implemented |
| License enforcement | — | — | ❌ Not implemented |
