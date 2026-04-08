# TASK-014 — License Enforcement

**Feature:** License Enforcement  
**TECH Spec:** [TECH-014-license.md](../TECH-014-license.md)  
**Phase:** 0 (Foundation — must complete before all other enterprise tasks)  
**Estimate:** 3 days  
**Assignee:** —  
**Status:** 🟢 Completed

---

## Context

License validation gates all enterprise features. It must be implemented first.  
License keys are RS256-signed JWTs validated entirely offline using an embedded public key.

**Feature name constants (used in `IsFeatureEnabled()`):**

| Feature String | Tier Required |
|----------------|--------------|
| `rbac` | enterprise |
| `audit_logs` | pro |
| `guardrails` | pro |
| `pii_redactor` | pro |
| `sso_oidc` | enterprise |
| `sso_saml` | enterprise |
| `scim` | enterprise |
| `adaptive_routing` | pro |
| `clustering` | enterprise |
| `alerts` | pro |
| `vault` | enterprise |
| `large_payload` | pro |
| `mcp_tool_groups` | pro |
| `user_groups` | enterprise |
| `data_connectors` | enterprise |

---

## Tasks

### TASK-014-01 — Create `framework/license` package

**Files to create:**
- `framework/license/license.go` — `License` struct, `LicenseTier` constants
- `framework/license/jwt.go` — RS256 JWT parsing via `github.com/lestrrat-go/jwx/v3`
- `framework/license/features.go` — `featureTierMap`, tier ordering
- `framework/license/enforcer.go` — `LicenseEnforcer`, `IsFeatureEnabled()`, `globalEnforcer`
- `framework/license/keys/bifrost-license-2026.pub.pem` — embed placeholder (dev: self-signed)

**Acceptance criteria:**
- [ ] `ParseLicense(rawJWT)` correctly validates RS256 signature
- [ ] `ParseLicense` returns error for expired, tampered, or wrong-audience JWTs
- [ ] `IsFeatureEnabled("rbac")` returns `false` when `globalEnforcer.license == nil`
- [ ] `IsFeatureEnabled("rbac")` returns `true` when license contains `"rbac"` in features array
- [ ] 7-day grace period: `IsFeatureEnabled` returns `true` for up to 7 days after `exp`
- [ ] Unit test: all 15 feature strings tested for community / pro / enterprise tiers

**Dependencies:** Add to `framework/go.mod`:
```
github.com/lestrrat-go/jwx/v3 v3.x.x
```

---

### TASK-014-02 — License loading at server bootstrap

**Files to modify:**
- `transports/bifrost-http/server/server.go` — add `loadLicense()` call in `Bootstrap()`

**Acceptance criteria:**
- [ ] Priority order: `BIFROST_LICENSE_KEY` env → Vault `bifrost/license` → `config.json.license_key`
- [ ] Missing license key → log info "running in Community tier", continue startup
- [ ] Invalid JWT → log error, continue startup (degraded mode, no enterprise features)
- [ ] Expired license → log warn with days-since-expiry, continue with grace period logic
- [ ] `license.StartExpiryWatcher()` goroutine started after successful parse

---

### TASK-014-03 — License API handler

**Files to create:**
- `transports/bifrost-http/handlers/license.go`

**Endpoints:**
```
GET  /api/license              — returns LicenseStatus (no auth required)
POST /api/license/validate     — validate JWT without applying (super_admin only)
GET  /api/license/features     — map[string]bool of all features (authenticated)
```

**Acceptance criteria:**
- [ ] `GET /api/license` returns `{"tier":"community","is_valid":true}` with no license key
- [ ] `GET /api/license/features` returns all 15 feature flags as `true`/`false`
- [ ] `POST /api/license/validate` returns 402 if caller is not `super_admin`
- [ ] Raw JWT string never returned in any response

---

### TASK-014-04 — `EnterpriseGate` UI component

**Files to create:**
- `ui/lib/license.ts` — `FeatureFlags` type, `useLicenseFeatures()` hook
- `ui/components/EnterpriseGate.tsx` — conditional render wrapper
- `ui/app/enterprise/license/page.tsx` — license dashboard page
- `ui/app/enterprise/license/components/LicenseCard.tsx`
- `ui/app/enterprise/license/components/FeatureMatrix.tsx`
- `ui/app/enterprise/license/components/UpgradePrompt.tsx`

**Acceptance criteria:**
- [ ] `useLicenseFeatures()` fetches `GET /api/license/features` on app mount, caches in context
- [ ] `<EnterpriseGate feature="rbac">` renders `<UpgradePrompt>` when `rbac=false`
- [ ] License dashboard shows tier badge, expiry date, feature list with enabled/disabled status
- [ ] Navigation items for enterprise features hidden when feature is disabled

---

### TASK-014-05 — Add `go.mod` dependency + update `go.work`

**Acceptance criteria:**
- [ ] `cd framework && go mod tidy` succeeds
- [ ] `go work sync` succeeds
- [ ] `make build` succeeds

---

## Definition of Done

- [ ] All subtasks complete
- [ ] `framework/license` unit test coverage ≥ 80%
- [ ] `GET /api/license` E2E test passes in both community and enterprise mode
- [ ] `make build` passes with new dependency
