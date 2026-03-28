---
phase: 02-fork-and-oidc-authentication
plan: 03
subsystem: auth
tags: [oidc, jwt, keycloak, go-oidc, singleflight, jwks]

# Dependency graph
requires:
  - phase: 02-fork-and-oidc-authentication/01
    provides: "Fork infrastructure with Go workspace and golang-jwt v5.3.1"
provides:
  - "OIDCConfig struct with JSON parsing and validation"
  - "KeycloakClaims struct for Keycloak JWT claims extraction"
  - "OIDCProvider singleton with OIDC discovery and JWKS caching"
  - "ValidateToken with singleflight-deduplicated JWKS refresh on key rotation"
  - "IsJWT helper to distinguish JWTs from session UUIDs"
  - "BifrostContextKeyOIDC* context keys for OIDC-authenticated requests"
affects: [02-fork-and-oidc-authentication/04]

# Tech tracking
tech-stack:
  added: ["coreos/go-oidc/v3 v3.17.0"]
  patterns: ["singleflight JWKS refresh dedup", "context.Background for OIDC provider lifecycle", "BifrostContextKeyOIDC* prefix for context keys"]

key-files:
  created:
    - framework/oidc/config.go
    - framework/oidc/config_test.go
    - framework/oidc/claims.go
    - framework/oidc/claims_test.go
    - framework/oidc/provider.go
    - framework/oidc/provider_test.go
  modified:
    - framework/go.mod
    - framework/go.sum

key-decisions:
  - "Used coreos/go-oidc v3.17.0 for OIDC discovery and ID token verification (D-05)"
  - "singleflight.Group deduplicates concurrent JWKS refresh calls on unknown kid (D-06)"
  - "context.Background() for provider creation to avoid request context lifecycle issues (D-07)"
  - "BifrostContextKeyOIDC* prefix avoids collision with existing auth context keys (D-03)"

patterns-established:
  - "OIDC package pattern: config.go (struct+validation), claims.go (JWT claims), provider.go (singleton+validation)"
  - "Mock OIDC server pattern: httptest.NewServer with discovery + JWKS endpoints for unit testing"
  - "IsJWT token format detection: len >= 10, exactly 3 non-empty dot-separated segments"

requirements-completed: [AUTH-01, AUTH-05, AUTH-07]

# Metrics
duration: 4min
completed: 2026-03-28
---

# Phase 02 Plan 03: OIDC Core Package Summary

**OIDC core package with OIDCConfig validation, KeycloakClaims extraction, and OIDCProvider singleton using go-oidc v3.17.0 with singleflight JWKS refresh**

## Performance

- **Duration:** 4 min
- **Started:** 2026-03-28T09:58:07Z
- **Completed:** 2026-03-28T10:02:26Z
- **Tasks:** 2
- **Files modified:** 8 (6 created, 2 modified)

## Accomplishments
- OIDCConfig struct with Validate() that enforces required fields (issuer_url, client_id) and sets defaults for scopes, org_claim, groups_claim
- KeycloakClaims struct that extracts sub, email, organization_id, groups from Keycloak JWTs, plus 5 BifrostContextKeyOIDC* context keys
- OIDCProvider singleton performing OIDC discovery at startup, validating JWTs against cached JWKS, with singleflight-deduplicated refresh on key rotation
- 21 unit tests covering config validation, claims unmarshaling, provider creation, token validation (valid/expired/wrong-audience), singleflight dedup, and IsJWT format detection

## Task Commits

Each task was committed atomically:

1. **Task 1: Create OIDCConfig and KeycloakClaims** - `3d31776c` (test)
2. **Task 2: Create OIDCProvider with singleflight JWKS refresh** - `1f5249e5` (feat)

## Files Created/Modified
- `framework/oidc/config.go` - OIDCConfig struct with JSON tags and Validate() for required fields and defaults
- `framework/oidc/config_test.go` - 7 tests covering validation errors and default values
- `framework/oidc/claims.go` - KeycloakClaims struct (embeds jwt.RegisteredClaims), RealmAccess, 5 context key constants
- `framework/oidc/claims_test.go` - 4 tests covering claims unmarshaling and context key uniqueness
- `framework/oidc/provider.go` - OIDCProvider with NewOIDCProvider(), ValidateToken(), refreshAndRetry(), IsJWT(), GetConfig()
- `framework/oidc/provider_test.go` - 10 tests with mock OIDC server (discovery + JWKS), concurrent singleflight verification
- `framework/go.mod` - Added coreos/go-oidc/v3 v3.17.0 as direct dependency
- `framework/go.sum` - Updated checksums

## Decisions Made
- Used coreos/go-oidc v3.17.0 (latest stable) instead of v3.14+ as initially considered -- v3.17.0 is current and has all needed features
- golang-jwt/jwt/v5 v5.3.1 already present as indirect dependency from Plan 01's CVE fix -- promoted to direct usage in claims.go
- Mock OIDC server approach (httptest.NewServer) chosen over go-oidc's oidctest package for full control over JWKS responses
- IsJWT uses length >= 10 threshold to avoid false positives on short strings like "a.b.c"

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None

## User Setup Required
None - no external service configuration required.

## Known Stubs
None - all code is fully implemented with no placeholder data.

## Next Phase Readiness
- framework/oidc/ package complete and ready for Plan 04 (OIDC middleware integration)
- OIDCProvider is the foundation that the middleware will import to validate Bearer JWT tokens
- KeycloakClaims and context keys will be used by the middleware to populate fasthttp request context
- No existing Bifrost files were modified (AUTH-07 compliance verified)

## Self-Check: PASSED

- All 6 created files verified on disk
- Both task commits (3d31776c, 1f5249e5) verified in git history
- 21/21 unit tests passing
- go vet clean

---
*Phase: 02-fork-and-oidc-authentication*
*Completed: 2026-03-28*
