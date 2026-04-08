# Test Cases — License Enforcement

**Suite ID:** TC-013  
**SRS Reference:** §3.25 (LIC-01 → LIC-10)  
**TR Reference:** TR-F-010  
**Priority:** P0  
**Type:** Integration + Unit + API  

> **Note:** This suite must pass BEFORE any other enterprise feature test suite, as license gates all other features.

---

## Test License JWTs (RSA Test Keypair)

```
enterprise_license:     Valid, tier=enterprise, all features, exp: 2035-01-01
pro_license:            Valid, tier=pro, features=[guardrails,pii_redactor,alerts], exp: 2035-01-01
trial_license:          Valid, tier=enterprise_trial, exp: (now + 25 days)
expired_license:        tier=enterprise, exp: 2020-01-01 (expired)
tampered_license:       Valid structure but wrong signature (bit-flipped)
wrong_aud_license:      Valid sig, aud="wrong-service" (not "bifrost-gateway")
```

---

### TC-013-001 — Valid Enterprise License Enables All Features (GET /api/license)

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-010.1

**Preconditions:** `BIFROST_LICENSE_KEY` = `enterprise_license` JWT. Restart Bifrost.

**Steps:**
1. `GET /api/license`

**Expected Result:**
- HTTP 200
- `{"tier":"enterprise","is_valid":true,"org_name":"Test Corp","features":[...]}`
- `features` array contains: rbac, audit_logs, guardrails, pii_redactor, sso_oidc, sso_saml, scim, adaptive_routing, clustering, alerts, vault, mcp_tool_groups, user_groups, data_connectors
- `days_remaining` > 0

**Status:** READY

---

### TC-013-002 — No License Key = Community Tier

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-010.4

**Preconditions:** Unset `BIFROST_LICENSE_KEY` environment variable. Restart.

**Steps:**
1. `GET /api/license`
2. `POST /api/rbac/roles` (enterprise endpoint)

**Expected Result:**
- `GET /api/license` returns `{"tier":"community","is_valid":false,"features":[]}`
- `POST /api/rbac/roles` returns HTTP 402 with `{"error":{"code":"license_required","feature":"rbac"}}`

**Status:** READY

---

### TC-013-003 — Enterprise Endpoint Returns 402 Without License

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-010.4

**Preconditions:** Community mode (no license).

**Steps:**
1. Test each enterprise endpoint: guardrails, pii, sso, rbac, audit, cluster, vault, adaptive routing

**Expected Result:**
- All return HTTP 402
- Body format consistent: `{"error":{"code":"license_required","feature":"{feature_name}","message":"..."}}`

**Status:** READY

---

### TC-013-004 — Expired License: 7-Day Grace Period

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-010.2

**Preconditions:** `BIFROST_LICENSE_KEY` = `expired_license` (exp: 2020-01-01). Restart.

**Steps:**
1. `GET /api/license`
2. `POST /api/rbac/roles` (enterprise endpoint)

**Expected Result:**
- `GET /api/license`: `{"is_valid":false,"days_remaining":-{X},"tier":"enterprise"}`
- Enterprise endpoints: HTTP 402 (grace period of 7 days is already past for 2020 license)
- Application log shows: "License expired — enterprise features disabled"

**Status:** READY

---

### TC-013-005 — Tampered License JWT Rejected

**Priority:** P0 | **Type:** Unit + Integration  
**TR Reference:** TR-F-010.3

**Steps:**
1. Unit: `license.ParseLicense(tampered_jwt)` → returns error
2. Integration: Start Bifrost with `BIFROST_LICENSE_KEY` = tampered JWT
3. `GET /api/license`

**Expected Result:**
- Unit: `ParseLicense` returns error "license signature invalid"
- Integration: Bifrost starts normally (community mode fallback)
- `GET /api/license`: `{"tier":"community","is_valid":false}` + log warning "Invalid license key"

**Status:** READY

---

### TC-013-006 — Wrong Audience Claim Rejected

**Priority:** P0 | **Type:** Unit

**Steps:**
1. `license.ParseLicense(wrong_aud_license)` 

**Expected Result:**
- Error: "license signature invalid" or "invalid audience claim"
- NOT parsed as valid even though signature is correct

**Status:** READY

---

### TC-013-007 — License Validation Makes No External Network Calls

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-010.6

**Steps:**
1. Block all outbound network connections (firewall rule or mock)
2. Start Bifrost with valid enterprise license
3. Verify enterprise features work

**Expected Result:**
- Bifrost starts and validates license offline (embedded public key)
- No network timeout errors during startup
- All licensed features work normally

**Status:** READY

---

### TC-013-008 — Pro License: Pro Features Enabled, Enterprise Features Blocked

**Priority:** P0 | **Type:** Integration

**Preconditions:** `BIFROST_LICENSE_KEY` = `pro_license` (features: guardrails, pii_redactor, alerts).

**Steps:**
1. `POST /api/guardrails/policies` → should succeed
2. `POST /api/rbac/roles` → should return 402
3. `GET /api/sso/login` → should return 402

**Expected Result:**
- Pro features work; enterprise-only features return 402

**Status:** READY

---

### TC-013-009 — Trial License Shows Expiry Warning

**Priority:** P1 | **Type:** API

**Preconditions:** `BIFROST_LICENSE_KEY` = `trial_license` (25 days remaining).

**Steps:**
1. `GET /api/license`
2. Check application logs

**Expected Result:**
- `{"tier":"enterprise_trial","days_remaining":25,"is_trial":true}`
- Log message: "License reminder: 25 days remaining"

**Status:** READY

---

### TC-013-010 — GET /api/license Available Without Auth

**Priority:** P0 | **Type:** API

**Steps:**
1. `GET /api/license` with NO Authorization header

**Expected Result:**
- HTTP 200 (public endpoint — non-sensitive info)
- Returns license tier and features list
- Does NOT return secret fields (JTI, org_id, raw JWT)

**Status:** READY

---

### TC-013-011 — License Features Available in /api/license/features

**Priority:** P1 | **Type:** API

**Steps:**
1. `GET /api/license/features` with valid session token
2. Response used by UI to show/hide enterprise nav

**Expected Result:**
```json
{
  "rbac": true,
  "audit_logs": true,
  "guardrails": true,
  "sso_oidc": true,
  "clustering": true,
  ...
}
```
- Boolean map of all feature flags

**Status:** READY

---

### TC-013-012 — License Loaded from Environment Variable Priority

**Priority:** P0 | **Type:** Integration

**Preconditions:**
- `BIFROST_LICENSE_KEY` = enterprise_license
- config.json also has a different (pro) `license_key` field

**Steps:**
1. Start Bifrost
2. `GET /api/license`

**Expected Result:**
- Environment variable takes priority
- `tier` = "enterprise" (from env), NOT "pro" (from config.json)

**Status:** READY
