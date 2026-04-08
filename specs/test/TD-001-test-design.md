# Test Design — Bifrost v2.0 Enterprise

**Document ID:** TD-001  
**Version:** 1.0 | **Date:** 2026-04-08  
**Reference:** TR-001, SRS v2.0  
**Status:** READY

---

## 1. Test Scope & Approach

### 1.1 Test Levels

| Level | Tooling | Responsibility |
|-------|---------|---------------|
| **Unit** | Go `testing` package, `testify` | Pure functions, parser, validator, checker logic |
| **Integration** | Go `testing` + real PostgreSQL/Redis | Handler↔DB, plugin pipeline, feature flag gates |
| **API (Contract)** | Playwright API / Go http test client | All REST endpoints, request/response shape |
| **E2E** | Playwright browser | UI flows, SSO login, guardrail admin, RBAC screens |
| **Performance** | k6 + Grafana | 5,000 RPS throughput, latency P99, streaming |
| **Security** | Manual + `gosec`, OWASP ZAP | Auth bypass, injection, audit log tamper |

### 1.2 Test Strategy per Feature Area

```
Enterprise Feature Pipeline:
  License ──► RBAC ──► Audit ──► Guardrails ──► PII ──► Logging
               │
               └──► SSO/SCIM ──► User Groups
```

**Testing order follows dependency graph:**
1. License (gates everything)
2. RBAC (needed for all endpoint tests)
3. Audit Logs (verify writes from all other tests)
4. Core OSS: Inference, Providers, Governance
5. Guardrails + PII Redactor
6. SSO/SCIM + User Groups
7. Adaptive Routing + Clustering
8. Alerts + Vault
9. MCP Tool Groups + Connectors
10. Performance

---

## 2. Test Environment Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Test Runner (GitHub Actions / Local)      │
│                                                              │
│  ┌───────────────┐   ┌─────────────────┐   ┌─────────────┐ │
│  │  Go Unit      │   │  Integration    │   │  E2E        │ │
│  │  Tests        │   │  Tests          │   │  (Playwright│ │
│  │  make test-   │   │  make test-     │   │   + API)    │ │
│  │  core         │   │  integration    │   │  make run-  │ │
│  └───────────────┘   └────────┬────────┘   │  e2e        │ │
│                               │            └──────┬──────┘ │
└───────────────────────────────┼───────────────────┼──────────┘
                                │                   │
              ┌─────────────────▼───────────────────▼──────┐
              │           Test Infrastructure               │
              │  ┌─────────────┐  ┌───────┐  ┌──────────┐ │
              │  │  Bifrost    │  │ Mock  │  │  Wiremock│ │
              │  │  Gateway    │  │ LLM   │  │  (IdP)   │ │
              │  │  :8080      │  │:9090  │  │  :8088   │ │
              │  └─────────────┘  └───────┘  └──────────┘ │
              │  ┌─────────────┐  ┌───────┐               │
              │  │  PostgreSQL │  │ Redis │               │
              │  │  :5432      │  │ :6379 │               │
              │  └─────────────┘  └───────┘               │
              └─────────────────────────────────────────────┘
```

### 2.1 Mock LLM Server

A Go HTTP mock server that mimics OpenAI-compatible responses for deterministic testing:

```go
// tests/mocks/llm_server.go

// Endpoints:
// POST /v1/chat/completions  → configurable response (fixture JSON)
// POST /v1/chat/completions  → streaming fixture (SSE chunks from file)
// GET  /v1/models            → static model list
// POST /v1/moderations       → configurable moderation score

// Controlled via:
// X-Mock-Delay: 200          → inject artificial latency
// X-Mock-Error: 500          → force error response
// X-Mock-Score: 0.9          → moderation category score
```

### 2.2 Mock IdP (WireMock / Custom)

Simulates OIDC and SAML identity providers:

```
GET  /.well-known/openid-configuration  → discovery document
POST /oauth2/token                      → returns signed JWT
GET  /jwks                              → public key set
POST /saml/idp                          → SAML response assertion
```

---

## 3. Test Data Design

### 3.1 Standard Test Sessions (Fixtures)

```json
// tests/fixtures/sessions.json
{
  "super_admin_token": "tok_super_admin_test_12345",
  "admin_token":       "tok_admin_test_67890",
  "operator_token":    "tok_operator_test_abcde",
  "viewer_token":      "tok_viewer_test_fghij",
  "api_user_token":    "tok_api_user_test_klmno",
  "invalid_token":     "tok_invalid_does_not_exist",
  "expired_license_token": "tok_expired_ent_test"
}
```

### 3.2 Standard Test Virtual Keys

```json
// tests/fixtures/virtual_keys.json
{
  "vk_unlimited":       { "budget": null, "rate_limit": null },
  "vk_tight_budget":    { "budget": { "max": 0.01, "currency": "USD" } },
  "vk_rate_limited":    { "rate_limit": { "requests_per_minute": 1 } },
  "vk_expired":         { "expires_at": "2020-01-01T00:00:00Z" },
  "vk_model_restricted":{ "allowed_models": ["gpt-4o-mini"] }
}
```

### 3.3 Known PII Test Strings

```
emails:      "contact@example.com", "user.name+tag@domain.co.uk"
phones:      "+1-800-555-0199", "(415) 555-2671"
ssns:        "123-45-6789", "987 65 4321"
credit_cards:"4532015112830366", "5425233430109903"  (valid Luhn)
invalid_cc:  "1234567890123456"  (invalid Luhn — must NOT be flagged)
```

### 3.4 Guardrail Test Payloads

```json
// tests/fixtures/guardrail_payloads.json
{
  "safe_prompt":   "What is the capital of France?",
  "keyword_match": "How do I make a bomb at home?",
  "regex_match":   "Call me at 555-1234 for the meeting",
  "borderline":    "I need information about chemistry reactions"
}
```

### 3.5 License JWTs (Test Keys — RSA Test Keypair)

```
// Enterprise license (valid, 1 year)
// Pro license (valid, 1 year)
// Expired license (exp: 2020-01-01)
// Tampered license (valid structure, wrong signature)
// Community (no license key)
```

---

## 4. Test Case Template

Each test case follows this structure:

```markdown
### TC-{SUITE}-{NNN} — {Title}

**Priority:** P0 | P1 | P2  
**Type:** Unit | Integration | API | E2E  
**TR Reference:** TR-F-XXX.Y  
**Preconditions:** List setup steps or state requirements  

**Steps:**
1. Step one
2. Step two

**Expected Result:** What should happen  
**Actual Result:** (filled during execution)  
**Status:** DRAFT | READY | PASSED | FAILED  
**Notes:** Edge cases, known issues
```

---

## 5. Test Automation Strategy

### 5.1 Go Integration Tests Structure

```
tests/
├── integration/
│   ├── helpers/
│   │   ├── client.go      // HTTP test client wrapper
│   │   ├── fixtures.go    // test data loader
│   │   └── setup.go       // DB seed + teardown
│   ├── rbac_test.go
│   ├── audit_test.go
│   ├── guardrails_test.go
│   ├── pii_test.go
│   ├── sso_test.go
│   └── license_test.go
```

### 5.2 Playwright E2E Test Structure

```
tests/e2e/
├── core/
│   ├── fixtures/          // test user sessions, VK setups
│   └── page-objects/      // RBAC page, Audit page, etc.
└── features/
    ├── rbac/
    │   ├── role-assignment.spec.ts
    │   └── permission-matrix.spec.ts
    ├── audit-logs/
    │   └── audit-trail.spec.ts
    ├── guardrails/
    │   └── policy-enforcement.spec.ts
    ├── sso/
    │   └── oidc-login.spec.ts
    └── license/
        └── feature-gating.spec.ts
```

### 5.3 k6 Performance Test Scripts

```
tests/performance/
├── baseline.js         // 1,000 RPS steady state
├── ramp.js             // ramp to 5,000 RPS over 10 min
├── spike.js            // sudden 10x load spike
├── streaming.js        // 500 concurrent streaming connections
└── scenarios/
    ├── with_guardrails.js
    ├── with_pii.js
    └── with_rbac.js
```

---

## 6. Defect Severity Classification

| Severity | Definition | Example |
|----------|-----------|---------|
| **Critical** | System down or data loss | Audit logs writable, PII stored in logs |
| **Blocker** | Feature completely broken | RBAC allows api_user to delete providers |
| **Major** | Feature partially broken | Guardrail bypass via encoding trick |
| **Minor** | Feature works, edge case failure | Missing error message detail |
| **Trivial** | Visual/cosmetic issue | Wrong icon in UI |

### Exit Criteria per Severity
- Critical: 0 open at release
- Blocker: 0 open at release
- Major: ≤ 2 open with workaround documented
- Minor: ≤ 10 open on backlog
- Trivial: tracked, not release-blocking

---

## 7. Risk Assessment

| Risk | Probability | Impact | Mitigation |
|------|------------|--------|-----------|
| OIDC IdP unavailable during test | Medium | High | Use WireMock mock IdP |
| Vault instance not available | Medium | Medium | Use local dev Vault + mock mode |
| Test data collision in shared DB | High | Medium | Isolated test schemas per run |
| Race conditions in distributed rate limit test | Low | High | Increase Redis TTL in test config |
| License JWT expiry during test | Low | Medium | Use 10-year test license JWTs |
