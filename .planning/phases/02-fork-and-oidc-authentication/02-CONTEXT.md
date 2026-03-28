# Phase 2: Fork and OIDC Authentication - Context

**Gathered:** 2026-03-28
**Status:** Ready for planning

<domain>
## Phase Boundary

Establish the Bifrost fork with correct Go workspace strategy and implement Keycloak OIDC authentication end-to-end. A Keycloak-authenticated user can make LLM requests through Bifrost with their identity mapped to the correct Customer/Team for budget and rate limit enforcement.

Two work streams:
1. **Fork infrastructure** -- `go.work` strategy, CVE fix (golang-jwt v5.3.1), CI pipeline for multi-arch Docker image, upstream merge tracking process
2. **OIDC authentication** -- discovery via `.well-known`, JWT validation in middleware, claims extraction and mapping to Bifrost governance entities, config.json OIDC section, token refresh race prevention

</domain>

<decisions>
## Implementation Decisions

### OIDC Integration Approach
- **D-01:** New OIDC middleware in new files only -- `framework/oidc/` for core OIDC logic, `handlers/oidc.go` for HTTP handlers. Never modify existing auth files (`middlewares.go`, `session.go`, `oauth2.go`) to minimize upstream merge conflicts (AUTH-07)
- **D-02:** OIDC middleware slots into the fasthttp middleware chain BEFORE the existing AuthMiddleware. When a valid OIDC JWT is present, short-circuit existing auth -- OIDC is the production auth path, session/Basic auth is for admin dashboard
- **D-03:** Use dedicated `BifrostContextKeyOIDC*` context keys to avoid collision with existing auth context values
- **D-04:** Fail loud at startup if OIDC is configured but discovery fails -- fail-fast prevents running without auth. If OIDC is not configured (no `oidc` section in config.json), skip OIDC middleware entirely (backward-compatible)
- **D-05:** Use `coreos/go-oidc/v3` (v3.17.0) for OIDC discovery + ID token verification, and `golang-jwt/jwt/v5` (v5.3.1) for hot-path JWT parsing
- **D-06:** Token refresh uses `singleflight.Group` from day one to prevent refresh token races under concurrent requests (research PITFALL #2)
- **D-07:** Create `oidc.Provider` with `context.Background()` (never request context) and retry once on unknown kid for JWKS key rotation handling

### Claims Mapping Strategy
- **D-08:** v1 requires pre-provisioned governance entities -- lookup existing Bifrost Customer by `organization_id` claim (matched to Customer's external_id), return 403 if not found. Auto-provisioning of Customer/VirtualKey on first login is deferred to v2
- **D-09:** `organization_id` comes from a custom Keycloak protocol mapper that adds it to access token claims. The exact claim path needs validation against the stragixlabs realm during implementation
- **D-10:** `sub` claim maps to Bifrost User, `groups` claim maps to Team by name. Unmapped groups are silently skipped in v1
- **D-11:** `email` claim is extracted for logging/audit purposes but not used for entity resolution

### Fork Repository Strategy
- **D-12:** Fork lives at `stragix-innovations/bifrost` on GitHub
- **D-13:** Keep `maximhq/bifrost` import paths everywhere in Go source. Use `go.work` for local development, `replace` directives in `go.mod` for Docker builds. The existing `setup-go-workspace.sh` CI script already initializes the 12-module workspace
- **D-14:** Manual periodic merge from `upstream/main`. CI job checks for upstream drift weekly. Document the merge process in FORK-04

### CI/CD Pipeline
- **D-15:** GitHub Actions with `docker buildx` for multi-arch builds (amd64 + arm64), push to GHCR at `ghcr.io/stragix-innovations/bifrost`
- **D-16:** Push to `main` triggers image build + push. PRs trigger tests only (no image push)
- **D-17:** Image tagging follows semantic versioning: `v{version}` tags from releases, `latest` from main

### Security
- **D-18:** Bump `golang-jwt/jwt/v5` from v5.3.0 to v5.3.1 to fix CVE-2025-30204 (memory allocation vulnerability during header parsing). This is FORK-02 and is a day-one requirement

### Config Format
- **D-19:** Add `oidc` section to config.json alongside existing `auth` section. Contains: `issuer_url` (Keycloak realm URL), `client_id`, `client_secret` (from Vault via ExternalSecret), `scopes` (default: `["openid", "profile", "email"]`), and claim name mappings (`org_claim`, `groups_claim`)
- **D-20:** OIDC config is optional -- if absent, Bifrost falls back to existing Basic/Bearer auth (full backward compatibility with upstream)

### Claude's Discretion
- Group-to-Team mapping implementation details (exact data structures, caching strategy)
- OIDC middleware error response format (should align with existing Bifrost error format)
- Test strategy for OIDC middleware (mock OIDC server vs `oidctest` package from go-oidc v3.14+)
- Upstream merge conflict resolution approach for the CI weekly check job

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Bifrost Auth Architecture
- `transports/bifrost-http/handlers/middlewares.go` -- Existing AuthMiddleware with Basic/Bearer/session validation. OIDC middleware must slot in before this
- `transports/bifrost-http/handlers/oauth2.go` -- Existing OAuth2 handler (for MCP OAuth client flows, NOT OIDC). Reference for handler patterns
- `transports/bifrost-http/handlers/session.go` -- Session management. OIDC should short-circuit this for JWT-authenticated requests
- `framework/oauth2/main.go` -- OAuth2Provider with PKCE, token exchange, refresh. Reference for token handling patterns
- `framework/oauth2/discovery.go` -- OAuth discovery implementation. Reference for how Bifrost does discovery (RFC 8414)

### Fork and Build Infrastructure
- `.github/workflows/scripts/setup-go-workspace.sh` -- Existing go.work setup for CI. Lists all 12 modules
- `Makefile` -- Build targets, test commands
- `.dockerignore` -- Files excluded from Docker build context

### Core Architecture
- `core/bifrost.go` -- Provider registry, sync.Map keyed by ModelProvider. DO NOT MODIFY
- `core/schemas/account.go` -- Customer, Team, User, VirtualKey types and CustomProviderConfig struct
- `transports/bifrost-http/lib/config.go` -- Config parsing, provider reconciliation, AuthConfig loading

### Research
- `.planning/research/SUMMARY.md` -- Full research summary with recommended stack, pitfalls, architecture approach
- `.planning/research/PITFALLS.md` -- Detailed pitfall analysis (import paths, refresh races, merge conflicts, JWKS rotation)
- `.planning/research/ARCHITECTURE.md` -- CustomProviderConfig discovery, middleware chain flow, governance integration points

### External References
- coreos/go-oidc v3 -- https://github.com/coreos/go-oidc (standard Go OIDC library)
- golang-jwt CVE-2025-30204 -- https://github.com/golang-jwt/jwt/security/advisories/GHSA-mh63-6h87-95cp
- oauth2-proxy Keycloak OIDC -- reference implementation for Keycloak OIDC patterns

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- `framework/oauth2/discovery.go` -- OAuth discovery following RFC 8414. Reference pattern for OIDC discovery, though OIDC will use go-oidc's built-in discovery rather than reimplementing
- `framework/oauth2/main.go` -- PKCE generation, token exchange, singleflight-ready architecture. Reference for token handling patterns
- `setup-go-workspace.sh` -- Already handles all 12 Go modules. May need minimal extension for new `framework/oidc/` module
- `AuthMiddleware` struct -- Uses `atomic.Pointer` for hot config updates, `configstore.ConfigStore` for persistence. OIDC middleware should follow the same pattern

### Established Patterns
- **Middleware chain**: fasthttp middleware with `schemas.BifrostHTTPMiddleware` type signature. OIDC middleware must conform to this interface
- **Context keys**: Bifrost uses string context keys on fasthttp.RequestCtx (`BifrostContextKey*` prefix). OIDC must use same pattern with `BifrostContextKeyOIDC*` prefix
- **Config hot-reload**: AuthMiddleware uses `atomic.Pointer[configstore.AuthConfig]` with `UpdateAuthConfig()`. OIDC config should follow the same atomic pointer pattern
- **Logging**: Uses `schemas.Logger` interface with `logger` package-level variable. Set via `SetLogger()` function
- **Error responses**: `SendError(ctx, statusCode, message)` helper function for consistent error formatting

### Integration Points
- `transports/bifrost-http/handlers/init.go` -- Where middleware chain is assembled. OIDC middleware registration goes here
- `transports/bifrost-http/lib/config.go` -- Config parsing. OIDC config section parsing goes here (or new `oidc_config.go`)
- `framework/configstore/clientconfig.go` -- AuthConfig struct. May need OIDC config struct alongside (not inside)

</code_context>

<specifics>
## Specific Ideas

- Existing `framework/oauth2/` is for MCP OAuth client flows (Bifrost authenticating TO external MCP servers). The new OIDC work is the reverse: external users authenticating TO Bifrost. These are completely separate concerns -- don't mix them
- The Bearer auth path in AuthMiddleware already handles base64-encoded username:password as a backward-compat fallback. OIDC JWT tokens will also be Bearer tokens but are NOT base64 username:password -- the OIDC middleware must intercept Bearer tokens first and only fall through to existing auth if OIDC validation fails or OIDC is not configured
- The Bifrost codebase uses `fasthttp` (not net/http). go-oidc and golang-jwt both use `net/http` internally for JWKS fetching, but JWT validation itself is pure Go -- no framework dependency conflict
- The `is_key_less: true` flag on CustomProviderConfig from Phase 1 needs validation before Phase 2 builds on it. If it doesn't work, a workaround may be needed

</specifics>

<deferred>
## Deferred Ideas

- **Auto-provisioning of Customer/VirtualKey from OIDC claims** -- v2 requirement. Zero manual setup per tenant. Deferred because it requires governance plugin modifications
- **Group-to-allowed-models mapping** -- v2 requirement. Keycloak groups determine model access tiers
- **PKCE for public clients** -- v2 requirement. Not needed until Bifrost UI is exposed as SPA
- **Per-instance cost tracking** -- v2 requirement. Custom pricing ($0 for self-hosted)
- **Clustering/HA** -- single replica + K8s restarts sufficient for current scale

</deferred>

---

*Phase: 02-fork-and-oidc-authentication*
*Context gathered: 2026-03-28*
