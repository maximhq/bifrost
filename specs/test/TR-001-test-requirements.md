# Test Requirements — Bifrost v2.0 Enterprise

**Document ID:** TR-001  
**Version:** 1.0 | **Date:** 2026-04-08  
**SRS Reference:** SRS v2.0, PRD v2.0, URD v2.0  
**Status:** READY

---

## 1. Purpose

This document defines WHAT must be tested for Bifrost v2.0. Each Test Requirement (TR) is derived from SRS functional requirements (FR) or non-functional requirements (NFR) and traces to a test case suite.

---

## 2. Test Requirement Categories

### 2.1 Functional Test Requirements

#### TR-F-001 — Unified Inference API (SRS §3.1)
| ID | Requirement | Derived From |
|----|------------|-------------|
| TR-F-001.1 | Chat completions must be proxied to all 20+ providers using OpenAI-compatible format | SRS INF-01 |
| TR-F-001.2 | Streaming SSE responses must pass through chunk-by-chunk without buffering | SRS INF-02 |
| TR-F-001.3 | Non-streaming response latency overhead must be ≤ 50ms above raw provider latency | SRS INF-03 |
| TR-F-001.4 | Malformed requests must return 400 with descriptive error JSON | SRS INF-04 |
| TR-F-001.5 | Unsupported operation for a provider returns 501 with provider name in error | SRS INF-05 |
| TR-F-001.6 | All OpenAI SDK-compatible headers must be forwarded correctly | SRS INF-06 |
| TR-F-001.7 | Text completions, embeddings, image generation, speech, transcription all supported | SRS INF-07 |

#### TR-F-002 — Provider Management (SRS §3.2)
| ID | Requirement | Derived From |
|----|------------|-------------|
| TR-F-002.1 | CRUD operations on providers persist correctly in configstore | SRS PROV-01 |
| TR-F-002.2 | Multiple API keys per provider with weighted selection | SRS PROV-02 |
| TR-F-002.3 | Automatic failover to secondary provider on primary failure | SRS PROV-03 |
| TR-F-002.4 | API key values are encrypted at rest | SRS PROV-04 |
| TR-F-002.5 | Provider health check reflects current reachability | SRS PROV-05 |

#### TR-F-003 — Virtual Keys & Governance (SRS §3.4)
| ID | Requirement | Derived From |
|----|------------|-------------|
| TR-F-003.1 | Virtual key creation with budget, rate limit, allowed models | SRS GOV-01 |
| TR-F-003.2 | Budget exhaustion blocks further requests with 429 | SRS GOV-02 |
| TR-F-003.3 | Rate limit enforcement with correct Retry-After header | SRS GOV-03 |
| TR-F-003.4 | Per-model routing rules via CEL expressions | SRS GOV-04 |
| TR-F-003.5 | Budget reset triggered at scheduled interval | SRS GOV-05 |
| TR-F-003.6 | Virtual key expiry blocks requests after expiry date | SRS GOV-06 |
| TR-F-003.7 | Budget usage is tracked per virtual key and returned via API | SRS GOV-07 |

#### TR-F-004 — RBAC (SRS §3.12)
| ID | Requirement | Derived From |
|----|------------|-------------|
| TR-F-004.1 | 5 predefined roles exist: super_admin, admin, operator, viewer, api_user | RBAC-01 |
| TR-F-004.2 | super_admin can perform all operations | RBAC-02 |
| TR-F-004.3 | admin cannot manage users or license | RBAC-03 |
| TR-F-004.4 | operator gets read-only access to providers | RBAC-04 |
| TR-F-004.5 | viewer cannot write to any resource | RBAC-05 |
| TR-F-004.6 | api_user cannot access any management API endpoint | RBAC-06 |
| TR-F-004.7 | Unauthorized access returns 403 with `rbac_denied` error code | RBAC-07 |
| TR-F-004.8 | Role assignment is recorded in audit log | RBAC-08 |
| TR-F-004.9 | RBAC is bypassed (returns community behavior) when no enterprise license | RBAC-09 |
| TR-F-004.10 | Custom role creation by super_admin with explicit permission list | RBAC-10 |

#### TR-F-005 — Audit Logs (SRS §3.13)
| ID | Requirement | Derived From |
|----|------------|-------------|
| TR-F-005.1 | Every management API write action creates an audit log entry | AUDIT-01 |
| TR-F-005.2 | Audit entries are append-only — no UPDATE or DELETE permitted | AUDIT-03 |
| TR-F-005.3 | Each entry includes SHA-256 hash of previous entry (hash chain) | AUDIT-05 |
| TR-F-005.4 | Chain integrity verification API returns pass/fail per sequence range | AUDIT-06 |
| TR-F-005.5 | Export to JSON/CSV for any time range within 5 minutes | AUDIT-07 |
| TR-F-005.6 | Login and logout events are recorded | AUDIT-08 |
| TR-F-005.7 | Search by actor, resource, action, time range | AUDIT-09 |
| TR-F-005.8 | Audit log write failure must never fail the triggering request | AUDIT-10 |

#### TR-F-006 — Content Guardrails (SRS §3.14)
| ID | Requirement | Derived From |
|----|------------|-------------|
| TR-F-006.1 | Keyword-block policy stops matching requests with 451 status | GUARD-01 |
| TR-F-006.2 | Regex filter matches PCRE patterns in request/response content | GUARD-02 |
| TR-F-006.3 | AI classifier calls moderation API and blocks above threshold | GUARD-03 |
| TR-F-006.4 | Flag action passes request through but logs violation | GUARD-04 |
| TR-F-006.5 | Transform action redacts matched content in-place | GUARD-05 |
| TR-F-006.6 | Policy with scope=request does not scan response, and vice versa | GUARD-06 |
| TR-F-006.7 | Policies are priority-ordered; first match wins | GUARD-07 |
| TR-F-006.8 | Guardrail evaluation failure (error) must fail open (pass through) | GUARD-08 |
| TR-F-006.9 | Dry-run test API returns evaluation result without executing | GUARD-09 |
| TR-F-006.10 | Disabled guardrail policy is skipped | GUARD-10 |

#### TR-F-007 — PII Detection & Redaction (SRS §3.15)
| ID | Requirement | Derived From |
|----|------------|-------------|
| TR-F-007.1 | Email addresses detected and redacted in request body | PII-01 |
| TR-F-007.2 | Phone numbers, SSNs, credit cards detected with correct entity type | PII-02 |
| TR-F-007.3 | Mask mode replaces PII with `[REDACTED_<TYPE>]` | PII-03 |
| TR-F-007.4 | Hash mode is deterministic (same input → same hash) | PII-04 |
| TR-F-007.5 | Tokenize mode produces reversible token stored in KVStore | PII-05 |
| TR-F-007.6 | PII is never stored in log store (logging plugin sees redacted content) | PII-06 |
| TR-F-007.7 | Credit card numbers pass Luhn algorithm check before flagging | PII-07 |
| TR-F-007.8 | Custom regex PII pattern is applied alongside built-in patterns | PII-08 |

#### TR-F-008 — SSO / SCIM (SRS §3.16)
| ID | Requirement | Derived From |
|----|------------|-------------|
| TR-F-008.1 | OIDC login flow redirects to IdP and exchanges code for session | SSO-01 |
| TR-F-008.2 | ID token signature and expiry are validated | SSO-02 |
| TR-F-008.3 | OIDC group claims are mapped to Bifrost roles via config | SSO-03 |
| TR-F-008.4 | SAML ACS endpoint accepts signed assertion and creates session | SSO-04 |
| TR-F-008.5 | SP metadata XML is available at `/api/sso/saml/metadata` | SSO-05 |
| TR-F-008.6 | SCIM POST /Users creates external user with correct role | SCIM-01 |
| TR-F-008.7 | SCIM DELETE /Users/{id} deactivates user and invalidates sessions | SCIM-02 |
| TR-F-008.8 | SCIM PATCH /Users/{id} updates user attributes | SCIM-03 |
| TR-F-008.9 | Invalid SCIM token returns 401 | SCIM-04 |
| TR-F-008.10 | SSO-provisioned user inherits role from group mapping | SSO-06 |

#### TR-F-009 — Adaptive Routing (SRS §3.17)
| ID | Requirement | Derived From |
|----|------------|-------------|
| TR-F-009.1 | Latency-optimized strategy routes to lowest P50 provider | AROUTE-01 |
| TR-F-009.2 | Cost-optimized strategy routes to cheapest provider per 1M tokens | AROUTE-02 |
| TR-F-009.3 | Provider with error_rate > threshold is excluded from routing | AROUTE-03 |
| TR-F-009.4 | Routing falls back to weighted_random when sample size < minimum | AROUTE-04 |
| TR-F-009.5 | Metrics endpoint returns P50/P95 latency and error rate per provider | AROUTE-05 |

#### TR-F-010 — License Enforcement (SRS §3.25)
| ID | Requirement | Derived From |
|----|------------|-------------|
| TR-F-010.1 | Valid enterprise license enables all enterprise features | LIC-01 |
| TR-F-010.2 | Expired license disables enterprise features after 7-day grace period | LIC-02 |
| TR-F-010.3 | Tampered JWT fails signature validation and is rejected | LIC-03 |
| TR-F-010.4 | Community tier (no license) returns 402 on enterprise endpoints | LIC-04 |
| TR-F-010.5 | `GET /api/license` returns tier, expiry, and features list | LIC-05 |
| TR-F-010.6 | License validation never makes external network calls | LIC-06 |

---

### 2.2 Non-Functional Test Requirements

#### TR-NF-001 — Performance (SRS §4)
| ID | Requirement | Target |
|----|------------|--------|
| TR-NF-001.1 | Gateway overhead at 5,000 RPS | ≤ 11µs P99 |
| TR-NF-001.2 | Chat completion non-streaming latency | ≤ 50ms gateway overhead |
| TR-NF-001.3 | Concurrent streaming connections | ≥ 1,000 simultaneous |
| TR-NF-001.4 | Provider failover time | ≤ 200ms |
| TR-NF-001.5 | API response time for management endpoints | ≤ 500ms P95 |

#### TR-NF-002 — Security (SRS §4)
| ID | Requirement | Target |
|----|------------|--------|
| TR-NF-002.1 | All API keys encrypted at rest (AES-256 or Vault Transit) | 100% |
| TR-NF-002.2 | PII never appears in log store | 100% |
| TR-NF-002.3 | Audit log entries cannot be deleted or modified | 100% |
| TR-NF-002.4 | OIDC state parameter prevents CSRF | RFC 6749 compliant |
| TR-NF-002.5 | SCIM tokens stored as bcrypt hash | 100% |

#### TR-NF-003 — Scalability (SRS §4)
| ID | Requirement | Target |
|----|------------|--------|
| TR-NF-003.1 | Horizontal scale | ≥ 10 nodes |
| TR-NF-003.2 | Rate limit accuracy in cluster mode | ≤ 5% over-count |
| TR-NF-003.3 | Budget drift in cluster mode | ≤ 1% |

#### TR-NF-004 — Reliability (SRS §4)
| ID | Requirement | Target |
|----|------------|--------|
| TR-NF-004.1 | System uptime SLA | ≥ 99.9% |
| TR-NF-004.2 | Zero data loss on node failure (PostgreSQL) | 100% |
| TR-NF-004.3 | Guardrail failure must not block inference | fail-open |
| TR-NF-004.4 | Audit log write failure must not block management API | fail-open |

---

## 3. Exclusions (Out of Scope for v2.0 Initial Test Cycle)

| Excluded Item | Reason |
|--------------|--------|
| Bedrock streaming with HTTP/2 | Covered by existing provider integration tests |
| UI visual regression | Separate Chromatic/Percy pipeline |
| Billing / payment integration | Handled by external payment provider |
| Terraform infrastructure | Separate infra testing pipeline |

---

## 4. Entry / Exit Criteria

### Entry Criteria (per test suite)
- [ ] Feature implementation merged to `main`
- [ ] Unit tests passing (`make test-core`)
- [ ] Dev environment healthy (all services running)
- [ ] Test data fixtures prepared

### Exit Criteria (per release)
- [ ] 100% of P0 test cases executed
- [ ] ≥ 95% pass rate on P0 cases
- [ ] ≥ 90% pass rate on P1 cases
- [ ] 0 Critical/Blocker defects open
- [ ] All TR-NF performance targets met
- [ ] Security scan (static analysis) clean
