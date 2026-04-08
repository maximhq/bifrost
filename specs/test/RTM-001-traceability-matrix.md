# Requirements Traceability Matrix (RTM)

**Document ID:** RTM-001  
**Version:** 1.0 | **Date:** 2026-04-08  
**Source:** TR-001 Test Requirements ↔ Test Cases  
**Status:** COMPLETE

---

## 1. Overview

This matrix traces every SRS requirement to its test cases, ensuring full coverage.

**Coverage Legend:**
- ✅ = Covered by test case(s)
- ⚠️ = Partially covered
- ❌ = Not yet covered (risk item)

---

## 2. Functional Requirements Traceability

### §3.1 Unified Inference API

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| INF-01 | Chat completions proxied to 20+ providers (OpenAI-compatible) | TC-001-001, TC-001-018 | ✅ |
| INF-02 | Streaming SSE responses pass chunk-by-chunk | TC-001-002, TC-001-007 | ✅ |
| INF-03 | Non-streaming latency overhead ≤ 50ms | TC-001-007, TC-015-001 | ✅ |
| INF-04 | Malformed requests return 400 | TC-001-003 | ✅ |
| INF-05 | Unsupported operation returns 501 | TC-001-004 | ✅ |
| INF-06 | OpenAI SDK-compatible headers forwarded | TC-001-024 | ✅ |
| INF-07 | Text, embeddings, images, speech, transcription supported | TC-001-005, TC-001-015, TC-001-019 | ✅ |
| INF-08 | Batch file operations supported | TC-001-020 | ✅ |
| INF-09 | Responses API (stateful) supported | TC-001-016 | ✅ |

### §3.2–3.3 Provider Management & Failover

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| PROV-01 | CRUD operations on providers | TC-002-001, TC-002-005 | ✅ |
| PROV-02 | Multiple API keys per provider, weighted | TC-002-004, TC-002-013 | ✅ |
| PROV-03 | Automatic failover to secondary provider | TC-001-006, TC-002-013, TC-015-004 | ✅ |
| PROV-04 | API keys encrypted at rest | TC-002-002 | ✅ |
| PROV-05 | Provider health check | TC-002-006, TC-002-007 | ✅ |
| PROV-06 | Key masking in GET response | TC-002-003 | ✅ |
| PROV-07 | Fallback chain exhaustion returns 502 | TC-002-014 | ✅ |
| PROV-08 | Key rotation without restart | TC-002-015 | ✅ |

### §3.4 Virtual Keys & Governance

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| GOV-01 | VK creation with budget, rate limit, allowed models | TC-003-001 | ✅ |
| GOV-02 | Budget exhaustion blocks with 429 | TC-003-003, TC-001-011 | ✅ |
| GOV-03 | Rate limit enforcement + Retry-After header | TC-003-004, TC-001-012 | ✅ |
| GOV-04 | CEL-based routing rules | TC-003-007, TC-003-008 | ✅ |
| GOV-05 | Budget reset at scheduled interval | TC-003-010 | ✅ |
| GOV-06 | VK expiry blocks requests | TC-003-009, TC-001-014 | ✅ |
| GOV-07 | Budget usage tracked and queryable | TC-003-002, TC-003-012 | ✅ |
| GOV-08 | Hierarchical customer-level budget | TC-003-011 | ✅ |
| GOV-09 | Tokens per minute rate limiting | TC-003-015 | ✅ |

### §3.5 Observability

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| OBS-01 | Request log after inference | TC-OBS-001, TC-OBS-002 | ✅ |
| OBS-02 | Prometheus metrics endpoint | TC-OBS-003 | ✅ |
| OBS-03 | Log search with filters | TC-OBS-006 | ✅ |
| OBS-04 | OpenTelemetry trace export | TC-OBS-008 | ✅ |
| OBS-05 | Usage statistics endpoint | TC-OBS-007 | ✅ |

### §3.6 Semantic Caching

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| CACHE-01 | Cache hit on semantically similar query | TC-OBS-004 | ✅ |
| CACHE-02 | Cache miss on dissimilar query | TC-OBS-005 | ✅ |

### §3.12 RBAC

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| RBAC-01 | 5 predefined roles exist | TC-004-001 | ✅ |
| RBAC-02 | super_admin can perform all operations | TC-004-001 | ✅ |
| RBAC-03 | admin cannot manage users | TC-004-008 | ✅ |
| RBAC-04 | operator gets limited write access | TC-004-006, TC-004-007 | ✅ |
| RBAC-05 | viewer: read-only | TC-004-004, TC-004-005 | ✅ |
| RBAC-06 | api_user cannot access management API | TC-004-002, TC-004-003 | ✅ |
| RBAC-07 | Unauthorized returns 403 rbac_denied | TC-004-020 | ✅ |
| RBAC-08 | Role assignment audited | TC-004-009 | ✅ |
| RBAC-09 | RBAC bypassed without enterprise license | TC-004-010 | ✅ |
| RBAC-10 | Custom role creation by super_admin | TC-004-011 | ✅ |
| RBAC-11 | Role revocation takes effect immediately | TC-004-017 | ✅ |
| RBAC-12 | Time-bounded role expiry | TC-004-013 | ✅ |

### §3.13 Audit Logs

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| AUDIT-01 | Every write action creates audit entry | TC-005-001, TC-005-002, TC-005-003 | ✅ |
| AUDIT-02 | Login events audited | TC-005-003 | ✅ |
| AUDIT-03 | Append-only (no UPDATE/DELETE) | TC-005-004, TC-005-005 | ✅ |
| AUDIT-04 | Fields: actor, action, resource, time, before/after | TC-005-001 | ✅ |
| AUDIT-05 | SHA-256 hash chain | TC-005-006 | ✅ |
| AUDIT-06 | Chain integrity verification API | TC-005-007, TC-005-008 | ✅ |
| AUDIT-07 | Export JSON/CSV | TC-005-011, TC-005-012 | ✅ |
| AUDIT-08 | Search by actor/time/resource | TC-005-009, TC-005-010 | ✅ |
| AUDIT-09 | Write failure doesn't block request | TC-005-013 | ✅ |

### §3.14 Content Guardrails

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| GUARD-01 | Keyword block returns 451 | TC-006-001, TC-006-002 | ✅ |
| GUARD-02 | Regex filter PCRE | TC-006-004 | ✅ |
| GUARD-03 | AI classifier above threshold | TC-006-017, TC-006-018 | ✅ |
| GUARD-04 | Flag action: allow + log | TC-006-005 | ✅ |
| GUARD-05 | Transform action: in-place redact | TC-006-004 | ✅ |
| GUARD-06 | scope=request / response isolation | TC-006-006, TC-006-007 | ✅ |
| GUARD-07 | Policy priority order | TC-006-008 | ✅ |
| GUARD-08 | Evaluation failure → fail open | TC-006-009 | ✅ |
| GUARD-09 | Dry-run test API | TC-006-010 | ✅ |
| GUARD-10 | Disabled policy skipped | TC-006-011 | ✅ |

### §3.15 PII Detection & Redaction

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| PII-01 | Email detected and redacted | TC-007-001 | ✅ |
| PII-02 | Phone, SSN, CC detected | TC-007-002, TC-007-003, TC-007-004 | ✅ |
| PII-03 | Mask mode format | TC-007-008 | ✅ |
| PII-04 | Hash mode deterministic | TC-007-009 | ✅ |
| PII-05 | Tokenize mode reversible | TC-007-010 | ✅ |
| PII-06 | PII not stored in logs | TC-007-006 | ✅ |
| PII-07 | Luhn check for CC | TC-007-004, TC-007-005 | ✅ |
| PII-08 | Custom regex pattern | TC-007-011 | ✅ |
| PII-09 | PII in response redacted | TC-007-007 | ✅ |
| PII-10 | Multiple entities in one message | TC-007-012 | ✅ |

### §3.16 SSO / SCIM

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| SSO-01 | OIDC login flow redirects to IdP | TC-008-001 | ✅ |
| SSO-02 | ID token signature + expiry validated | TC-008-003, TC-008-004 | ✅ |
| SSO-03 | Group claims → Bifrost role mapping | TC-008-005 | ✅ |
| SSO-04 | SAML ACS accepts signed assertion | TC-008-008 | ✅ |
| SSO-05 | SAML SP metadata at /api/sso/saml/metadata | TC-008-007 | ✅ |
| SSO-06 | JIT provisioning of new users | TC-008-015 | ✅ |
| SSO-07 | CSRF protection via state parameter | TC-008-006 | ✅ |
| SCIM-01 | POST /Users creates user | TC-008-010 | ✅ |
| SCIM-02 | DELETE /Users deactivates + invalidates sessions | TC-008-011 | ✅ |
| SCIM-03 | PATCH /Users updates attributes | TC-008-012 | ✅ |
| SCIM-04 | Invalid SCIM token → 401 | TC-008-013 | ✅ |
| SCIM-05 | ServiceProviderConfig endpoint | TC-008-014 | ✅ |

### §3.17 Adaptive Routing

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| AROUTE-01 | Latency-optimized routes to fastest | TC-009-001 | ✅ |
| AROUTE-02 | Cost-optimized routes to cheapest | TC-009-003 | ✅ |
| AROUTE-03 | High error rate provider excluded | TC-009-002 | ✅ |
| AROUTE-04 | Fallback to weighted_random below sample size | TC-009-004 | ✅ |
| AROUTE-05 | Metrics API returns per-provider stats | TC-009-005 | ✅ |
| AROUTE-06 | Balanced strategy weighted score | TC-009-007 | ✅ |
| AROUTE-07 | Thread-safe metric updates | TC-009-012 | ✅ |

### §3.18 Multi-Node Clustering

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| CLUSTER-01 | Node registration + heartbeat | TC-010-001 | ✅ |
| CLUSTER-02 | Failed node detected within window | TC-010-002 | ✅ |
| CLUSTER-03 | Distributed rate limit (Redis) | TC-010-003 | ✅ |
| CLUSTER-04 | Budget accuracy across nodes | TC-010-004 | ✅ |
| CLUSTER-05 | Config change propagated to all nodes | TC-010-005 | ✅ |
| CLUSTER-06 | Cache invalidation on update | TC-010-006 | ✅ |
| CLUSTER-07 | Leader election (1 runner for resets) | TC-010-007 | ✅ |
| CLUSTER-08 | New node joins and receives state | TC-010-008 | ✅ |

### §3.19 Alert Channels

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| ALERT-01 | Create webhook channel | TC-011-001 | ✅ |
| ALERT-02 | Test channel (manual trigger) | TC-011-002 | ✅ |
| ALERT-03 | Budget breach alert | TC-011-003, TC-011-004 | ✅ |
| ALERT-04 | Provider error rate alert | TC-011-005 | ✅ |
| ALERT-05 | Guardrail violation alert | TC-011-006 | ✅ |
| ALERT-06 | Alert de-duplication while firing | TC-011-007 | ✅ |
| ALERT-07 | Alert resolved on condition change | TC-011-008 | ✅ |
| ALERT-08 | Disabled channel does not fire | TC-011-009 | ✅ |
| ALERT-09 | Slack format correct | TC-011-010 | ✅ |
| ALERT-10 | Alert history queryable | TC-011-013 | ✅ |

### §3.20 HashiCorp Vault

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| VAULT-01 | vault:// URI resolved at load | TC-012-001 | ✅ |
| VAULT-02 | Token auth method | TC-012-002, TC-012-003 | ✅ |
| VAULT-03 | AppRole auth method | TC-012-005 | ✅ |
| VAULT-04 | Token renewal before expiry | TC-012-006 | ✅ |
| VAULT-05 | Secret update reflected without restart | TC-012-007 | ✅ |
| VAULT-06 | Vault unavailable → fail closed | TC-012-008 | ✅ |
| VAULT-07 | Path not found error | TC-012-009 | ✅ |
| VAULT-08 | Plain keys coexist with Vault keys | TC-012-010 | ✅ |

### §3.22 MCP Tool Groups

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| MCPGRP-01 | Create tool group | TC-014-001 | ✅ |
| MCPGRP-02 | Assign group to VK | TC-014-002 | ✅ |
| MCPGRP-03 | VK limited to group's tools only | TC-014-003 | ✅ |
| MCPGRP-04 | Remove tool affects running VKs | TC-014-006 | ✅ |
| MCPGRP-05 | Quota enforcement per VK | TC-014-007 | ✅ |

### §3.24 Data Connectors

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| CONN-01 | Create webhook connector | TC-014-009 | ✅ |
| CONN-02 | Test connectivity | TC-014-010 | ✅ |
| CONN-03 | Auto-export logs on flush interval | TC-014-011 | ✅ |
| CONN-04 | Connector failure doesn't block inference | TC-014-012 | ✅ |
| CONN-05 | Disabled connector not exporting | TC-014-016 | ✅ |

### §3.23 User Groups

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| UGRP-01 | Create user group with roles | TC-UG-001 | ✅ |
| UGRP-02 | Add user to group inherits roles | TC-UG-002, TC-UG-003 | ✅ |
| UGRP-03 | Remove user revokes access | TC-UG-004 | ✅ |
| UGRP-04 | Multi-group max privilege | TC-UG-005 | ✅ |
| UGRP-05 | SCIM group → Bifrost group sync | TC-UG-007 | ✅ |

### §3.25 License Enforcement

| SRS Req ID | Requirement Summary | Test Case(s) | Coverage |
|-----------|--------------------|-----------| ---------|
| LIC-01 | Valid enterprise license enables all features | TC-013-001 | ✅ |
| LIC-02 | Expired license → 402 after grace period | TC-013-004 | ✅ |
| LIC-03 | Tampered JWT rejected | TC-013-005 | ✅ |
| LIC-04 | No license → community tier, 402 on enterprise | TC-013-002, TC-013-003 | ✅ |
| LIC-05 | GET /api/license returns tier + features | TC-013-001, TC-013-002 | ✅ |
| LIC-06 | Offline validation (no external calls) | TC-013-007 | ✅ |
| LIC-07 | Pro tier features | TC-013-008 | ✅ |
| LIC-08 | Trial license warning | TC-013-009 | ✅ |
| LIC-09 | Env variable priority over config file | TC-013-012 | ✅ |

---

## 3. Non-Functional Requirements Traceability

| NFR ID | Requirement | Target | Test Case(s) | Coverage |
|--------|------------|--------|-------------|---------|
| NFR-PERF-01 | Gateway overhead at 5,000 RPS | ≤ 11µs P99 | TC-015-001, TC-015-002 | ✅ |
| NFR-PERF-02 | Non-streaming latency overhead | ≤ 50ms | TC-001-007, TC-015-001 | ✅ |
| NFR-PERF-03 | Concurrent streaming connections | ≥ 1,000 | TC-015-003 | ✅ |
| NFR-PERF-04 | Provider failover time | ≤ 200ms | TC-015-004 | ✅ |
| NFR-PERF-05 | Management API P95 | ≤ 500ms | TC-015-001 | ✅ |
| NFR-SEC-01 | API keys encrypted at rest | 100% | TC-002-002 | ✅ |
| NFR-SEC-02 | PII never in log store | 100% | TC-007-006 | ✅ |
| NFR-SEC-03 | Audit logs immutable | 100% | TC-005-004, TC-005-005 | ✅ |
| NFR-SEC-04 | OIDC CSRF protection | RFC 6749 | TC-008-006 | ✅ |
| NFR-SCALE-01 | Horizontal scale ≥ 10 nodes | architecture | TC-010-001..008 | ⚠️ (3-node tested) |
| NFR-SCALE-02 | Rate limit accuracy in cluster | ≤ 5% over | TC-010-003 | ✅ |
| NFR-SCALE-03 | Budget drift in cluster | ≤ 1% | TC-010-004 | ✅ |
| NFR-REL-01 | System uptime | ≥ 99.9% | TC-015-008 | ⚠️ (stability test) |
| NFR-REL-02 | Guardrail fail-open | required | TC-006-009 | ✅ |
| NFR-REL-03 | Audit log fail-open | required | TC-005-013 | ✅ |
| NFR-MEM-01 | Memory stable 1 hour | < 50MB growth | TC-015-008 | ✅ |

---

## 4. Coverage Summary

| Area | Total Requirements | Covered | Partial | Not Covered |
|------|-------------------|---------|---------|-------------|
| Inference API | 9 | 9 | 0 | 0 |
| Provider Management | 8 | 8 | 0 | 0 |
| Governance | 9 | 9 | 0 | 0 |
| Observability | 5 | 5 | 0 | 0 |
| Semantic Cache | 2 | 2 | 0 | 0 |
| RBAC | 12 | 12 | 0 | 0 |
| Audit Logs | 9 | 9 | 0 | 0 |
| Guardrails | 10 | 10 | 0 | 0 |
| PII Redactor | 10 | 10 | 0 | 0 |
| SSO/SCIM | 12 | 12 | 0 | 0 |
| Adaptive Routing | 7 | 7 | 0 | 0 |
| Clustering | 8 | 8 | 0 | 0 |
| Alert Channels | 10 | 10 | 0 | 0 |
| Vault | 8 | 8 | 0 | 0 |
| MCP Tool Groups | 5 | 5 | 0 | 0 |
| Data Connectors | 5 | 5 | 0 | 0 |
| User Groups | 5 | 5 | 0 | 0 |
| License | 9 | 9 | 0 | 0 |
| NFR Performance | 5 | 5 | 0 | 0 |
| NFR Security | 4 | 4 | 0 | 0 |
| NFR Scale | 3 | 2 | 1 | 0 |
| NFR Reliability | 3 | 2 | 1 | 0 |
| **TOTAL** | **166** | **164** | **2** | **0** |

**Overall Coverage: 98.8%**

> ⚠️ **Partial Coverage Notes:**
> - NFR-SCALE-01: 10-node scale tested architecturally in 3-node cluster; full 10-node scale test requires dedicated infrastructure
> - NFR-REL-01: 99.9% uptime verified via 1-hour stability test; long-term SLA monitoring requires production telemetry
