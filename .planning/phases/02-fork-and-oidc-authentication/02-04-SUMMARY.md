---
phase: 02-fork-and-oidc-authentication
plan: 04
subsystem: auth
tags: [oidc, keycloak, jwt, middleware, fasthttp, governance, go]

# Dependency graph
requires:
  - phase: 02-fork-and-oidc-authentication/02-03
    provides: framework/oidc package (OIDCProvider, KeycloakClaims, BifrostContextKeyOIDC* constants)
provides:
  - OIDC middleware handler that validates Keycloak JWTs and resolves governance entities
  - Server.go wiring for OIDC middleware in API and inference middleware chains
  - Config.json "oidc" section parsing for OIDC provider configuration
affects: [03-production-deployment, governance-plugin, auth-proxy-integration]

# Tech tracking
tech-stack:
  added: []
  patterns: [OIDC middleware with governance resolution, context.Background for OIDC validation, configStore mock pattern for handler tests]

key-files:
  created:
    - transports/bifrost-http/handlers/oidc.go
    - transports/bifrost-http/handlers/oidc_test.go
  modified:
    - transports/bifrost-http/server/server.go
    - transports/bifrost-http/lib/config.go

key-decisions:
  - "Used context.Background() for OIDC ValidateToken calls because fasthttp.RequestCtx does not safely implement context.Context in all scenarios (nil server pointer in tests, per D-07)"
  - "Added OIDCConfigRaw (json.RawMessage) to Config struct instead of parsed OIDCConfig to keep config.go changes minimal -- parsing deferred to server.go Bootstrap"
  - "Modified config.go in addition to server.go (deviation from plan) to properly flow OIDC config from config.json through the existing LoadConfig pipeline"

patterns-established:
  - "OIDC middleware pattern: configStore interface mock with only GetCustomer/GetTeams for lightweight handler tests"
  - "Governance entity resolution pattern: org_id claim -> Customer lookup -> groups claim -> Team name matching"

requirements-completed: [AUTH-02, AUTH-03, AUTH-04, AUTH-06]

# Metrics
duration: 7min
completed: 2026-03-28
---

# Phase 02 Plan 04: OIDC Middleware Summary

**OIDC middleware validates Keycloak JWTs, resolves Customer/Team governance entities via configStore, and wires into Bifrost's fasthttp middleware chain before AuthMiddleware**

## Performance

- **Duration:** 7 min
- **Started:** 2026-03-28T10:05:00Z
- **Completed:** 2026-03-28T10:12:00Z
- **Tasks:** 2
- **Files modified:** 4

## Accomplishments
- OIDCMiddleware validates JWTs via go-oidc, resolves Customer by org_id (403 if not found per D-08), resolves Teams from groups by name (silently skipping unmapped per D-10)
- 11 test cases covering all middleware flow branches: valid JWT, expired JWT, invalid signature, non-JWT Bearer, missing auth, Basic auth, nil provider, empty org_id, customer not found, unmapped groups, no groups
- Server.go wired with OIDC provider initialization (fail-fast per D-04) and middleware positioned before AuthMiddleware in both API and inference chains (D-02)
- Config.json "oidc" section flows through existing LoadConfig pipeline via json.RawMessage

## Task Commits

Each task was committed atomically:

1. **Task 1: Create OIDC middleware handler with governance entity resolution** - `47869037` (feat)
2. **Task 2: Wire OIDC middleware into server.go Bootstrap and add OIDC config loading** - `f6c9dc8d` (feat)

## Files Created/Modified
- `transports/bifrost-http/handlers/oidc.go` - OIDC middleware: JWT validation, governance Customer/Team resolution, context key setting
- `transports/bifrost-http/handlers/oidc_test.go` - 11 test cases with mock OIDC server and mock configStore
- `transports/bifrost-http/server/server.go` - OIDC provider init in Bootstrap, middleware chain insertion before AuthMiddleware
- `transports/bifrost-http/lib/config.go` - OIDCConfigRaw field on Config struct, "oidc" JSON field on ConfigData

## Decisions Made
- Used `context.Background()` for OIDC `ValidateToken` calls -- fasthttp.RequestCtx's context.Context implementation requires an initialized server pointer which is nil in test scenarios and could cause panics. This also aligns with D-07 (stable context for OIDC operations)
- Stored OIDC config as `json.RawMessage` in the Config struct rather than a parsed OIDCConfig, keeping config.go changes minimal while deferring parsing to server.go's Bootstrap
- Modified config.go (ConfigData, TempConfigData, Config structs) to support "oidc" JSON section -- necessary deviation from plan's "only server.go modified" constraint

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Modified config.go for OIDC config flow**
- **Found during:** Task 2 (server.go wiring)
- **Issue:** Plan stated only server.go should be modified among existing files. However, config.json is parsed via `LoadConfig` in lib/config.go, and the OIDC config section needs to flow through the existing deserialization pipeline. Server.go cannot re-read config.json independently
- **Fix:** Added `OIDCConfig json.RawMessage` to ConfigData struct, TempConfigData struct, and `OIDCConfigRaw json.RawMessage` to Config struct. 3 one-line additions plus 1 field assignment
- **Files modified:** transports/bifrost-http/lib/config.go
- **Verification:** `go build ./bifrost-http/server/...` and `go build ./bifrost-http/handlers/...` both succeed
- **Committed in:** f6c9dc8d (Task 2 commit)

**2. [Rule 1 - Bug] Used context.Background() instead of fasthttp.RequestCtx for OIDC validation**
- **Found during:** Task 1 (test execution)
- **Issue:** Passing `*fasthttp.RequestCtx` as `context.Context` to `ValidateToken` caused nil pointer dereference in tests because `RequestCtx.Done()` accesses the internal server pointer which is nil in test-created contexts
- **Fix:** Changed `oidcProvider.ValidateToken(ctx, token)` to `oidcProvider.ValidateToken(context.Background(), token)` -- correct per D-07
- **Files modified:** transports/bifrost-http/handlers/oidc.go
- **Verification:** All 11 tests pass
- **Committed in:** 47869037 (Task 1 commit)

---

**Total deviations:** 2 auto-fixed (1 blocking, 1 bug)
**Impact on plan:** Both fixes necessary for correct operation. Config.go change is 4 minimal additions. No scope creep.

## Issues Encountered
None beyond the auto-fixed deviations.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- OIDC authentication pipeline complete: framework/oidc (Plan 03) + middleware/wiring (Plan 04)
- Phase 3 (Production Deployment) can now deploy the forked image with OIDC config in config.json
- Keycloak client needs to be created in the stragixlabs realm with redirect URIs for Bifrost
- Customer governance entities need to be pre-provisioned in Bifrost's config.json for each Keycloak org

## Known Stubs
None - all data paths are fully wired. No placeholder data or TODO markers in created files.

## Self-Check: PASSED

All files verified present. All commit hashes found in git log.

---
*Phase: 02-fork-and-oidc-authentication*
*Completed: 2026-03-28*
