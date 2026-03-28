# Phase 2: Fork and OIDC Authentication - Research

**Researched:** 2026-03-28
**Domain:** Go multi-module fork management + Keycloak OIDC JWT authentication in fasthttp gateway
**Confidence:** HIGH

## Summary

This phase has two orthogonal work streams: (1) establishing the Bifrost fork infrastructure with correct Go workspace strategy, CVE fix, and CI pipeline; and (2) implementing Keycloak OIDC authentication as a new middleware that slots into the existing fasthttp chain. The fork already exists at `stragix-innovations/bifrost` with `upstream` remote configured to `maximhq/bifrost`. Phase 1's config-only work validated CustomProviderConfig -- Phase 2 writes actual Go code for the first time.

The fork strategy is settled: keep `maximhq/bifrost` import paths in all Go source, use `go.work` for local development (Bifrost already has `setup-go-workspace.sh` covering 12 modules), and `GOWORK=off` in Docker builds (the existing Dockerfile already does this). The OIDC implementation lives entirely in new files (`framework/oidc/` package + `handlers/oidc.go`) to minimize upstream merge conflicts. The middleware validates Keycloak JWTs using `coreos/go-oidc/v3` for JWKS management and `golang-jwt/jwt/v5` (bumped to v5.3.1 for CVE fix) for hot-path claims parsing. Claims are mapped to Bifrost governance entities: `organization_id` (Keycloak user attribute, mapped via protocol mapper to top-level claim) resolves to Bifrost Customer by `external_id`, `sub` maps to User, `groups` maps to Team by name.

**Primary recommendation:** Build the fork infrastructure (go.work validation, CVE fix, CI pipeline) first, then layer OIDC middleware on top. OIDC middleware must slot BEFORE the existing AuthMiddleware in the chain and short-circuit when a valid JWT is present, falling through to existing Basic/Bearer/session auth when OIDC is not configured.

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions
- **D-01:** New OIDC middleware in new files only -- `framework/oidc/` for core OIDC logic, `handlers/oidc.go` for HTTP handlers. Never modify existing auth files (`middlewares.go`, `session.go`, `oauth2.go`) to minimize upstream merge conflicts (AUTH-07)
- **D-02:** OIDC middleware slots into the fasthttp middleware chain BEFORE the existing AuthMiddleware. When a valid OIDC JWT is present, short-circuit existing auth -- OIDC is the production auth path, session/Basic auth is for admin dashboard
- **D-03:** Use dedicated `BifrostContextKeyOIDC*` context keys to avoid collision with existing auth context values
- **D-04:** Fail loud at startup if OIDC is configured but discovery fails -- fail-fast prevents running without auth. If OIDC is not configured (no `oidc` section in config.json), skip OIDC middleware entirely (backward-compatible)
- **D-05:** Use `coreos/go-oidc/v3` (v3.17.0) for OIDC discovery + ID token verification, and `golang-jwt/jwt/v5` (v5.3.1) for hot-path JWT parsing
- **D-06:** Token refresh uses `singleflight.Group` from day one to prevent refresh token races under concurrent requests (research PITFALL #2)
- **D-07:** Create `oidc.Provider` with `context.Background()` (never request context) and retry once on unknown kid for JWKS key rotation handling
- **D-08:** v1 requires pre-provisioned governance entities -- lookup existing Bifrost Customer by `organization_id` claim (matched to Customer's external_id), return 403 if not found. Auto-provisioning deferred to v2
- **D-09:** `organization_id` comes from a custom Keycloak protocol mapper that adds it to access token claims. The exact claim path needs validation against the stragixlabs realm during implementation
- **D-10:** `sub` claim maps to Bifrost User, `groups` claim maps to Team by name. Unmapped groups are silently skipped in v1
- **D-11:** `email` claim is extracted for logging/audit purposes but not used for entity resolution
- **D-12:** Fork lives at `stragix-innovations/bifrost` on GitHub
- **D-13:** Keep `maximhq/bifrost` import paths everywhere in Go source. Use `go.work` for local development, `replace` directives in `go.mod` for Docker builds. The existing `setup-go-workspace.sh` CI script already initializes the 14-module workspace
- **D-14:** Manual periodic merge from `upstream/main`. CI job checks for upstream drift weekly. Document the merge process in FORK-04
- **D-15:** GitHub Actions with `docker buildx` for multi-arch builds (amd64 + arm64), push to GHCR at `ghcr.io/stragix-innovations/bifrost`
- **D-16:** Push to `main` triggers image build + push. PRs trigger tests only (no image push)
- **D-17:** Image tagging follows semantic versioning: `v{version}` tags from releases, `latest` from main
- **D-18:** Bump `golang-jwt/jwt/v5` from v5.3.0 to v5.3.1 to fix CVE-2025-30204 (memory allocation vulnerability during header parsing). Day-one requirement
- **D-19:** Add `oidc` section to config.json alongside existing `auth` section. Contains: `issuer_url`, `client_id`, `client_secret`, `scopes`, and claim name mappings (`org_claim`, `groups_claim`)
- **D-20:** OIDC config is optional -- if absent, Bifrost falls back to existing Basic/Bearer auth (full backward compatibility with upstream)

### Claude's Discretion
- Group-to-Team mapping implementation details (exact data structures, caching strategy)
- OIDC middleware error response format (should align with existing Bifrost error format)
- Test strategy for OIDC middleware (mock OIDC server vs `oidctest` package from go-oidc v3.14+)
- Upstream merge conflict resolution approach for the CI weekly check job

### Deferred Ideas (OUT OF SCOPE)
- Auto-provisioning of Customer/VirtualKey from OIDC claims -- v2 requirement
- Group-to-allowed-models mapping -- v2 requirement
- PKCE for public clients -- v2 requirement
- Per-instance cost tracking -- v2 requirement
- Clustering/HA -- single replica + K8s restarts sufficient
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| FORK-01 | Establish fork with `go.work` strategy -- keep `maximhq/bifrost` import paths | Fork exists at stragix-innovations/bifrost with upstream remote. `setup-go-workspace.sh` covers 12 modules. Dockerfile uses `GOWORK=off`. go.work is gitignored. |
| FORK-02 | Bump `golang-jwt/jwt/v5` from v5.3.0 to v5.3.1 (CVE-2025-30204 fix) | golang-jwt v5.3.1 verified on GitHub (Jan 28, 2025). CVE affects v5.0.0-rc.1 through v5.2.1 (memory allocation during header parsing). Both `core/go.mod` and `transports/go.mod` list v5.3.0 as indirect dep. |
| FORK-03 | CI pipeline for multi-arch Docker image (amd64 + arm64) push to GHCR | Existing upstream `release-pipeline.yml` provides reference. Existing `Dockerfile` handles CGO cross-compilation with `musl-dev`. `docker buildx` with QEMU emulation for arm64. |
| FORK-04 | Upstream merge tracking -- document process for periodic merges | Fork has `upstream` remote configured. Weekly CI job via `git merge upstream/main --no-commit` test. Document in CONTRIBUTING.md or FORK.md. |
| AUTH-01 | OIDC discovery via `.well-known/openid-configuration` | `coreos/go-oidc/v3` v3.17.0 `NewProvider(ctx, issuerURL)` handles full discovery. Keycloak issuer URL: `https://{host}/realms/{realm}`. Must use `context.Background()` to avoid issue #339. |
| AUTH-02 | JWT token validation in middleware -- validate against JWKS, check audience + expiry | go-oidc `IDTokenVerifier.Verify()` + golang-jwt `Parser.ParseWithClaims()` with `WithIssuer`, `WithAudience`. Bearer tokens differentiated from session tokens by JWT structure (3 dot-separated segments). |
| AUTH-03 | Claims extraction -- `sub`, `email`, `groups`, `organization_id` | `organization_id` is a top-level claim via Keycloak UserAttribute protocol mapper (verified in Pulumi test: `claimName: "organization_id"`). `groups` via Keycloak group membership mapper. Custom claims struct for golang-jwt. |
| AUTH-04 | Claims-to-governance mapping -- `organization_id` to Customer, `sub` to User, `groups` to Team | Customer lookup by ID matching `organization_id` claim. TableCustomer has `ID` (varchar primary key). Team lookup by `Name` matching group claim value. v1 = pre-provisioned only, 403 if not found. |
| AUTH-05 | Config.json OIDC section -- issuer URL, client ID/secret, scopes, claim mappings | New `OIDCConfig` struct alongside existing `AuthConfig` in configstore. Parsed during config loading in `lib/config.go` or new `oidc_config.go`. Optional section -- absence means OIDC disabled. |
| AUTH-06 | Token refresh with `singleflight.Group` -- prevent refresh races | `golang.org/x/sync/singleflight` already available (transports/go.mod: `golang.org/x/sync v0.20.0`). Key per user session. For API auth (stateless JWT), no refresh needed -- only for dashboard SSO sessions. |
| AUTH-07 | OIDC in new files only (`framework/oidc/`, `handlers/oidc.go`) -- minimize upstream diff | Confirmed: all OIDC code in new files. Integration point: `server.go` Bootstrap function (lines 1302-1345) where middleware chains are assembled. Single modification point: append OIDC middleware to `apiMiddlewares` and `inferenceMiddlewares` before AuthMiddleware. |
</phase_requirements>

## Standard Stack

### Core

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| coreos/go-oidc/v3 | v3.17.0 | OIDC discovery, JWKS management, ID token verification | 1,821+ importers on pkg.go.dev. Used by oauth2-proxy, Dex, Pomerium. The standard Go OIDC library. Keycloak-compatible via issuer URL. |
| golang-jwt/jwt/v5 | v5.3.1 | JWT parsing, custom claims extraction, signature verification | Already indirect dependency (v5.3.0). Bump to v5.3.1 fixes CVE-2025-30204. Provides `ParseWithClaims`, `WithIssuer`, `WithAudience`, custom `Keyfunc`. |
| golang.org/x/sync | v0.20.0 | `singleflight.Group` for token refresh dedup | Already in transports/go.mod. Prevents refresh token races under concurrent requests. |
| golang.org/x/oauth2 | v0.35.0 | OAuth2 token exchange (used by go-oidc internally) | Already present. No version change needed. |

### Supporting (already present, no changes)

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| valyala/fasthttp | v1.68.0 | HTTP framework (middleware context) | All middleware uses `fasthttp.RequestCtx` |
| stretchr/testify | v1.11.1 | Test assertions | All test files |
| gorm.io/gorm | v1.31.1 | ORM for governance entity lookup | Customer/Team resolution from claims |
| gorm.io/driver/sqlite | v1.6.0 | SQLite storage | Config store, governance store |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| coreos/go-oidc + golang-jwt | zitadel/oidc | zitadel is heavier (full OIDC server+client). go-oidc is lighter, more importers, client-only focus. |
| coreos/go-oidc + golang-jwt | lestrrat-go/jwx | Would conflict with golang-jwt already in dependency tree. Two JWT libraries causes confusion. |
| singleflight.Group | sync.Mutex | singleflight deduplicates concurrent calls naturally. Mutex requires manual result sharing. |

**Installation:**
```bash
# In the transports module
cd transports
go get github.com/coreos/go-oidc/v3@v3.17.0
go get github.com/golang-jwt/jwt/v5@v5.3.1
```

**Version verification:**

| Package | Current in go.mod | Target | Verified |
|---------|-------------------|--------|----------|
| coreos/go-oidc/v3 | not present | v3.17.0 | Nov 21, 2025 on GitHub |
| golang-jwt/jwt/v5 | v5.3.0 (indirect) | v5.3.1 | Jan 28, 2025 on GitHub |
| golang.org/x/sync | v0.20.0 | v0.20.0 (no change) | Already present |

## Architecture Patterns

### Recommended Project Structure (new files only)

```
framework/
  oidc/
    provider.go          # OIDC provider wrapper (discovery, JWKS, verifier singleton)
    claims.go            # Custom Keycloak claims struct, extraction helpers
    config.go            # OIDCConfig struct, validation, parsing
    provider_test.go     # Unit tests for provider
    claims_test.go       # Unit tests for claims

transports/bifrost-http/
  handlers/
    oidc.go              # OIDCMiddleware function, Bearer JWT detection, context key setting
    oidc_test.go         # Middleware unit tests (mock OIDC server)
```

### Pattern 1: OIDC Middleware Chain Integration

**What:** The OIDC middleware slots into the existing middleware chain BEFORE AuthMiddleware. It inspects Bearer tokens and, if they are JWTs (3-segment dot-separated strings), validates via JWKS. If valid, sets OIDC context keys and calls next. If invalid, returns 401. If OIDC is not configured or the token is not a JWT, falls through to the existing AuthMiddleware.

**When to use:** Every request to inference (`/v1/*`) and API (`/api/*`) endpoints.

**Integration point in server.go Bootstrap (lines 1302-1345):**

The existing middleware chain assembly is:

```
// Current flow (server.go:1302-1344)
commonMiddlewares = PrepareCommonMiddlewares()    // [PrometheusHTTPMiddleware]
apiMiddlewares = commonMiddlewares
inferenceMiddlewares = commonMiddlewares

// Auth added:
apiMiddlewares = append(apiMiddlewares, AuthMiddleware.APIMiddleware())
inferenceMiddlewares = append(inferenceMiddlewares, AuthMiddleware.InferenceMiddleware())

// Transport interceptor prepended to inference:
inferenceMiddlewares = append([]MW{TransportInterceptorMiddleware}, inferenceMiddlewares...)

// Tracing prepended to inference:
inferenceMiddlewares = append([]MW{TracingMiddleware}, inferenceMiddlewares...)
```

**OIDC insertion -- add BEFORE AuthMiddleware in both chains:**

```go
// NEW: OIDC middleware before auth (only if configured)
if oidcConfig != nil {
    oidcMW := handlers.OIDCMiddleware(oidcProvider, config)
    apiMiddlewares = append(apiMiddlewares, oidcMW)
    inferenceMiddlewares = append(inferenceMiddlewares, oidcMW)
}
// THEN existing auth
apiMiddlewares = append(apiMiddlewares, AuthMiddleware.APIMiddleware())
inferenceMiddlewares = append(inferenceMiddlewares, AuthMiddleware.InferenceMiddleware())
```

**The full request flow with OIDC:**

```
fasthttp.Server.Handler:
  SecurityHeaders -> CORS -> RequestDecompression -> Router
    -> [route-level middlewares]:
       Prometheus -> OIDC(NEW) -> Auth(existing) -> Tracing -> TransportInterceptor
       -> Handler
```

### Pattern 2: JWT Detection vs Session Token Detection

**What:** The existing AuthMiddleware handles `Bearer <token>` where `<token>` is a SQLite session UUID. OIDC JWTs are also Bearer tokens but are structurally different. The OIDC middleware must distinguish JWTs from session tokens.

**How to detect:** JWTs have exactly 3 base64url-encoded segments separated by dots: `header.payload.signature`. Session UUIDs are 36-char strings with dashes (e.g., `550e8400-e29b-41d4-a716-446655440000`). A simple heuristic:

```go
func isJWT(token string) bool {
    parts := strings.SplitN(token, ".", 4)
    return len(parts) == 3 && len(parts[0]) > 0 && len(parts[1]) > 0 && len(parts[2]) > 0
}
```

**Flow:**
1. OIDC middleware extracts Bearer token
2. If `isJWT(token)` is true and OIDC is configured: validate via JWKS, extract claims, set context, call next
3. If `isJWT(token)` is false or OIDC is not configured: do nothing, let AuthMiddleware handle it
4. If JWT validation fails (expired, bad signature): return 401 immediately, do NOT fall through

### Pattern 3: Claims-to-Governance Resolution

**What:** Map OIDC JWT claims to Bifrost's governance hierarchy for budget/rate-limit enforcement.

**Claim paths (verified from Keycloak Pulumi config):**
- `organization_id`: Top-level claim in access token. Added via `UserAttributeProtocolMapper` with `claimName: "organization_id"` and `userAttribute: "organization_id"`. Targets both backend and frontend clients.
- `sub`: Standard OIDC claim. UUID of the Keycloak user.
- `email`: Standard OIDC claim.
- `groups`: Keycloak group membership. Requires "groups" scope or group membership mapper on the client.

**Resolution flow (v1 -- pre-provisioned entities):**

```
1. Extract organization_id from JWT claims
2. Lookup TableCustomer WHERE ID = organization_id
   - Found: attach to request context for governance plugin
   - Not found: return 403 Forbidden ("organization not provisioned")
3. Extract sub from JWT claims
   - Store in context for audit logging (no User entity creation in v1)
4. Extract groups from JWT claims
   - For each group name, lookup TableTeam WHERE Name = group AND CustomerID = customer.ID
   - Matched teams: attach to context for governance
   - Unmatched groups: silently skip (v1)
5. The governance plugin uses the attached Customer/Team to resolve VirtualKey -> budget/rate limits
```

**Key insight:** The Customer `ID` field in Bifrost is a `varchar(255)` primary key set by the admin. For OIDC mapping, the admin pre-provisions a Customer with `ID` matching the Keycloak `organization_id` value (a GUID). This is a direct string match, not a lookup by a separate `external_id` field.

### Pattern 4: OIDC Provider Singleton

**What:** The `oidc.Provider` must be created once at startup and cached as a singleton. It internally manages JWKS key caching and rotation.

```go
type OIDCProvider struct {
    provider   *oidc.Provider
    verifier   *oidc.IDTokenVerifier
    config     *OIDCConfig
    sf         singleflight.Group  // For token refresh dedup
}

func NewOIDCProvider(ctx context.Context, config *OIDCConfig) (*OIDCProvider, error) {
    // Use context.Background() -- NEVER request context (go-oidc issue #339)
    provider, err := oidc.NewProvider(context.Background(), config.IssuerURL)
    if err != nil {
        return nil, fmt.Errorf("OIDC discovery failed for %s: %w", config.IssuerURL, err)
    }
    verifier := provider.Verifier(&oidc.Config{
        ClientID: config.ClientID,
    })
    return &OIDCProvider{
        provider: provider,
        verifier: verifier,
        config:   config,
    }, nil
}
```

### Anti-Patterns to Avoid

- **Modifying `middlewares.go` for OIDC:** OIDC goes in `handlers/oidc.go`. The only modification to existing files is the middleware chain assembly in `server.go` (minimal touch point).
- **Using request context for OIDC Provider creation:** Must use `context.Background()`. Request context cancellation would kill the JWKS cache (go-oidc issue #339).
- **Storing OIDC sessions in SQLite for API auth:** JWT tokens are self-contained. Validate on every request (stateless). Only use sessions for dashboard SSO if needed.
- **Rewriting import paths to `stragix-innovations/bifrost`:** Keep `maximhq/bifrost` everywhere. Use `go.work` for development. This is the single most important fork decision.
- **Mixing OIDC with the existing OAuth2 framework:** `framework/oauth2/` is for MCP tool OAuth (Bifrost authenticating TO external services). OIDC is the reverse flow (users authenticating TO Bifrost). Completely separate concerns.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| OIDC discovery | Manual `.well-known` HTTP fetch + endpoint parsing | `oidc.NewProvider(ctx, issuerURL)` | Handles endpoint caching, metadata validation, key rotation |
| JWKS key management | Custom key cache with TTL refresh | `oidc.RemoteKeySet` (via `IDTokenVerifier`) | Handles unknown-kid refresh, concurrent access, context lifecycle |
| JWT signature verification | Manual RSA/ECDSA verification | `jwt.ParseWithClaims` with go-oidc `RemoteKeySet` as `Keyfunc` | Supports RS256/ES256/PS256/EdDSA, proper header validation |
| Token refresh dedup | Manual mutex + result sharing | `singleflight.Group` | Built for this exact pattern: deduplicate concurrent calls |
| Multi-arch Docker builds | Separate Dockerfiles per arch | `docker buildx build --platform linux/amd64,linux/arm64` | QEMU emulation handles CGO cross-compilation |

## Common Pitfalls

### Pitfall 1: Go Multi-Module Import Path Hell

**What goes wrong:** Changing import paths from `maximhq/bifrost` to `stragix-innovations/bifrost` causes hundreds of merge conflicts on every upstream sync, because every `.go` file has different import lines.

**Why it happens:** Go modules identify themselves by their `go.mod` path. Forking changes the GitHub path but Go does not understand that the fork is the same code.

**How to avoid:** Keep `maximhq/bifrost` module paths in ALL Go source files. Use `go.work` for local development (already supported by `setup-go-workspace.sh`). The Dockerfile already uses `GOWORK=off` and copies only the `transports/` module, so the go.mod `replace` directives are not needed in the current Dockerfile strategy. If new modules need local resolution in Docker, add `replace` directives at build time.

**Warning signs:** Build failures with "module github.com/stragix-innovations/bifrost/core not found". Hundreds of merge conflicts in import statements after upstream merge.

### Pitfall 2: JWKS Key Rotation Window

**What goes wrong:** When Keycloak rotates signing keys, tokens signed with the new key are rejected until `go-oidc`'s `RemoteKeySet` refreshes its cache.

**Why it happens:** `RemoteKeySet` caches JWKS keys and only refreshes when an unknown `kid` is encountered. The HTTP fetch to Keycloak introduces a brief window.

**How to avoid:** (1) Create `oidc.Provider` with `context.Background()`, never request context. (2) In the middleware, on signature verification failure, attempt one retry with a forced JWKS refresh.

**Warning signs:** Sudden spike in 401 errors that self-resolves within seconds. Logs show "failed to verify id token signature".

### Pitfall 3: Bearer Token Ambiguity

**What goes wrong:** Both OIDC JWTs and existing Bifrost session tokens use the `Authorization: Bearer <token>` header. If the OIDC middleware does not correctly distinguish them, it may reject valid session tokens or try to JWKS-validate non-JWT strings.

**Why it happens:** The existing AuthMiddleware treats Bearer tokens as session UUIDs and falls back to base64-decoded `username:password`. Adding OIDC adds a third interpretation of Bearer tokens.

**How to avoid:** OIDC middleware checks `isJWT(token)` first (3 dot-separated segments). Only JWT-shaped tokens are validated via OIDC. Non-JWT Bearer tokens pass through to AuthMiddleware unchanged.

**Warning signs:** Dashboard users (session-based) getting 401 errors. Admin API calls with Basic auth failing.

### Pitfall 4: Context Key Collision

**What goes wrong:** Both OIDC middleware and existing AuthMiddleware set context values. If OIDC sets `BifrostContextKeySessionToken`, the existing AuthMiddleware's session validation interferes.

**Why it happens:** AuthMiddleware explicitly checks `BifrostContextKeySessionToken` to identify authenticated sessions. OIDC-authenticated requests are not session-based.

**How to avoid:** Use dedicated `BifrostContextKeyOIDC*` keys (D-03). Set a flag like `BifrostContextKeyOIDCAuthenticated = true` so downstream code can distinguish OIDC from session auth. When OIDC auth succeeds, call `next(ctx)` without setting `BifrostContextKeySessionToken`.

**Warning signs:** OIDC-authenticated requests returning 401. Governance plugin logging "unknown user" for authenticated requests.

### Pitfall 5: Dockerfile CGO Cross-Compilation for arm64

**What goes wrong:** Multi-arch builds fail because `go-sqlite3` requires CGO, and CGO cross-compilation needs platform-specific C toolchains.

**Why it happens:** The existing Dockerfile uses `CGO_ENABLED=1` with `gcc musl-dev sqlite-dev`. For arm64 on an amd64 builder, you need `gcc-aarch64-linux-gnu` or QEMU emulation.

**How to avoid:** Use `docker buildx` with QEMU emulation (the standard approach). The existing Dockerfile's alpine-based build with `musl` works under QEMU for arm64. Alternatively, use `--platform` flag with buildx and let QEMU handle the cross-compilation transparently.

**Warning signs:** Docker build fails with "exec format error" or CGO linker errors for arm64.

### Pitfall 6: Config Store Not Available During OIDC Initialization

**What goes wrong:** The OIDC provider needs to be created during server Bootstrap, but OIDC config comes from `config.json` which is parsed earlier. The config store (SQLite) may not be ready yet.

**Why it happens:** The Bootstrap function in `server.go` has a specific initialization order: config parsing -> store init -> plugin init -> middleware assembly. OIDC config parsing must happen during config parsing, but OIDC provider creation (which does HTTP to Keycloak) should happen during middleware assembly.

**How to avoid:** Parse OIDC config during config loading (JSON struct). Create the OIDC provider singleton during middleware assembly (after config store is ready, before route registration). If OIDC discovery fails, fail the entire Bootstrap (D-04).

**Warning signs:** OIDC middleware silently not authenticating because provider was nil.

## Code Examples

### OIDC Config Struct

```go
// Source: New file framework/oidc/config.go
type OIDCConfig struct {
    IssuerURL    string   `json:"issuer_url"`
    ClientID     string   `json:"client_id"`
    ClientSecret string   `json:"client_secret,omitempty"`
    Scopes       []string `json:"scopes"`
    OrgClaim     string   `json:"org_claim"`     // default: "organization_id"
    GroupsClaim  string   `json:"groups_claim"`   // default: "groups"
}

func (c *OIDCConfig) Validate() error {
    if c.IssuerURL == "" {
        return fmt.Errorf("oidc: issuer_url is required")
    }
    if c.ClientID == "" {
        return fmt.Errorf("oidc: client_id is required")
    }
    if c.OrgClaim == "" {
        c.OrgClaim = "organization_id"
    }
    if c.GroupsClaim == "" {
        c.GroupsClaim = "groups"
    }
    if len(c.Scopes) == 0 {
        c.Scopes = []string{"openid", "profile", "email"}
    }
    return nil
}
```

### Keycloak Claims Struct

```go
// Source: New file framework/oidc/claims.go
type KeycloakClaims struct {
    jwt.RegisteredClaims
    Email          string   `json:"email"`
    EmailVerified  bool     `json:"email_verified"`
    Name           string   `json:"name"`
    PreferredUser  string   `json:"preferred_username"`
    OrgID          string   `json:"organization_id"`    // Custom claim via protocol mapper
    OrgRole        string   `json:"organization_role"`  // Custom claim via protocol mapper
    Groups         []string `json:"groups"`             // Group membership mapper
    RealmAccess    *struct {
        Roles []string `json:"roles"`
    } `json:"realm_access,omitempty"`
}
```

### OIDC Middleware Function

```go
// Source: New file transports/bifrost-http/handlers/oidc.go
func OIDCMiddleware(oidcProvider *oidc.OIDCProvider, config *lib.Config) schemas.BifrostHTTPMiddleware {
    return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
        return func(ctx *fasthttp.RequestCtx) {
            // Skip if OIDC not configured
            if oidcProvider == nil {
                next(ctx)
                return
            }

            authorization := string(ctx.Request.Header.Peek("Authorization"))
            if authorization == "" {
                next(ctx)  // No auth header -- let AuthMiddleware handle
                return
            }

            scheme, token, ok := strings.Cut(authorization, " ")
            if !ok || scheme != "Bearer" || !isJWT(token) {
                next(ctx)  // Not a JWT Bearer -- let AuthMiddleware handle
                return
            }

            // Validate JWT via OIDC provider
            claims, err := oidcProvider.ValidateToken(ctx, token)
            if err != nil {
                SendError(ctx, fasthttp.StatusUnauthorized, "Invalid or expired token")
                return
            }

            // Set OIDC context values
            ctx.SetUserValue(BifrostContextKeyOIDCAuthenticated, true)
            ctx.SetUserValue(BifrostContextKeyOIDCSub, claims.Subject)
            ctx.SetUserValue(BifrostContextKeyOIDCEmail, claims.Email)
            ctx.SetUserValue(BifrostContextKeyOIDCOrgID, claims.OrgID)
            ctx.SetUserValue(BifrostContextKeyOIDCGroups, claims.Groups)

            next(ctx)
        }
    }
}
```

### Config.json OIDC Section

```json
{
  "oidc": {
    "issuer_url": "https://keycloak.stragix.com/realms/stragixlabs",
    "client_id": "bifrost",
    "client_secret": "${OIDC_CLIENT_SECRET}",
    "scopes": ["openid", "profile", "email", "groups"],
    "org_claim": "organization_id",
    "groups_claim": "groups"
  },
  "providers": { "..." : "..." },
  "auth": { "..." : "..." }
}
```

### CI Workflow for Multi-Arch Docker Build

```yaml
# .github/workflows/docker-build.yml
name: Build and Push Docker Image

on:
  push:
    branches: [main]
    tags: ['v*']
  pull_request:
    branches: [main]

jobs:
  build:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-qemu-action@v3
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        if: github.event_name != 'pull_request'
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: docker/build-push-action@v6
        with:
          context: .
          file: transports/Dockerfile
          platforms: linux/amd64,linux/arm64
          push: ${{ github.event_name != 'pull_request' }}
          tags: |
            ghcr.io/stragix-innovations/bifrost:latest
            ghcr.io/stragix-innovations/bifrost:${{ github.sha }}
          build-args: |
            VERSION=${{ github.ref_name }}
```

### Upstream Merge Check CI Job

```yaml
# .github/workflows/upstream-check.yml
name: Upstream Drift Check

on:
  schedule:
    - cron: '0 9 * * 1'  # Weekly on Monday
  workflow_dispatch:

jobs:
  check-upstream:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - run: |
          git remote add upstream https://github.com/maximhq/bifrost.git || true
          git fetch upstream main
          if git merge upstream/main --no-commit --no-ff 2>/dev/null; then
            echo "Clean merge possible"
            git merge --abort
          else
            echo "::warning::Upstream merge has conflicts"
            git merge --abort
          fi
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| go-oidc v2 (interface-based KeySet) | go-oidc v3 (`*RemoteKeySet` concrete type) | v3 release | Import path change, breaking API change |
| golang-jwt v4 | golang-jwt v5 | 2023 | Claims interface redesign, error handling rework |
| Manual JWKS cache management | go-oidc built-in `RemoteKeySet` with auto-refresh | go-oidc v3.5+ | No longer need to manage cache TTLs |
| Cache-Control based JWKS refresh | Unknown-kid triggered refresh | go-oidc v3 | More reliable, no dependency on HTTP cache headers |

**Deprecated/outdated:**
- `golang-jwt/jwt/v5` v5.0.0-rc.1 through v5.2.1: Vulnerable to CVE-2025-30204 (memory allocation during header parsing). Must use v5.3.1+.
- go-oidc `NewRemoteKeySet()` returning `KeySet` interface: v3 returns `*RemoteKeySet` concrete type.

## Open Questions

1. **Keycloak `organization_id` claim format in stragixlabs realm**
   - What we know: Pulumi tests show `UserAttributeProtocolMapper` with `claimName: "organization_id"` and `userAttribute: "organization_id"`. This places it as a top-level claim in the access token.
   - What's unclear: What is the actual value format? The platform uses GUIDs (e.g., `5998a989-e602-4b44-817c-282ccfd20987` from the test). Need to verify this matches the Bifrost Customer `ID` format used in pre-provisioned governance entities.
   - Recommendation: During implementation, decode an actual Keycloak access token from the stragixlabs realm and inspect the claim path. Use `jwt.io` or `jq` to verify.

2. **`groups` claim availability in access tokens**
   - What we know: Groups are available in Keycloak. The `groups` scope or a group membership protocol mapper must be configured on the Bifrost client.
   - What's unclear: Whether the groups mapper is added to the Bifrost client by default in the Pulumi Keycloak setup, or whether it needs to be explicitly configured.
   - Recommendation: During deployment (Phase 3), ensure the Bifrost Keycloak client has a group membership mapper. For Phase 2 implementation, make the groups claim optional -- if absent, skip Team resolution.

3. **go-oidc `oidctest` package maturity**
   - What we know: Added in v3.14.0 (available in v3.17.0). Provides mock OIDC server for testing.
   - What's unclear: Documentation is minimal. The package may have limited features.
   - Recommendation: Try `oidctest` first. Fall back to hand-rolled mock OIDC server (httptest.Server + static JWKS) if `oidctest` is too limited. The hand-rolled approach is well-documented in oauth2-proxy's test suite.

4. **Governance plugin integration -- how does the existing VirtualKey resolution use Customer/Team?**
   - What we know: The governance plugin resolves VirtualKey from request headers/context. VirtualKeys have `CustomerID` and `TeamID` foreign keys. The plugin enforces budget/rate limits based on the VirtualKey's associated Budget and RateLimit.
   - What's unclear: The exact mechanism by which the governance plugin picks up the Customer from the middleware context. The `lib.ConvertToBifrostContext()` function builds the BifrostContext used by the governance pre-hooks.
   - Recommendation: Trace the governance plugin's `LLMPreHook` to understand how it resolves VirtualKey for a request. The OIDC middleware may need to set a VirtualKey identifier in context (not just Customer/Team), or the governance plugin may need a small extension to resolve VirtualKey from Customer+Team. This is a v1 integration question -- investigate during implementation.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go | Build | YES | 1.25.6 (local) / 1.26.1 (CI) | CI builds use 1.26.1 via setup-go-action |
| Docker + buildx | Multi-arch image | YES | 29.2.1 | -- |
| GHCR | Image registry | YES (via GitHub org) | -- | -- |
| kustomize | Overlay validation | YES | v5.8.1 | -- |
| Keycloak | OIDC issuer | YES (dev + prod clusters) | -- | -- |

**Missing dependencies with no fallback:**
- None

**Missing dependencies with fallback:**
- **Go 1.26.1 locally:** Local machine has Go 1.25.6, Bifrost requires 1.26.1. For local development, either upgrade Go or rely on CI for compilation. Bifrost's `go.mod` declares `go 1.26.1`.

## Sources

### Primary (HIGH confidence)
- Bifrost source code analysis: `transports/bifrost-http/handlers/middlewares.go` (full AuthMiddleware flow, lines 507-735), `server/server.go` (Bootstrap middleware assembly, lines 1300-1362)
- Bifrost source code analysis: `framework/configstore/clientconfig.go` (AuthConfig struct, GovernanceConfig struct, ProviderConfig)
- Bifrost source code analysis: `framework/configstore/tables/customer.go`, `tables/team.go`, `tables/virtualkey.go` (governance entity schemas)
- Bifrost source code analysis: `transports/Dockerfile` (CGO build, GOWORK=off, multi-stage alpine)
- Bifrost source code analysis: `.github/workflows/scripts/setup-go-workspace.sh` (12-module workspace setup)
- Bifrost source code analysis: `transports/go.mod`, `core/go.mod` (dependency versions, golang-jwt v5.3.0 indirect)
- [coreos/go-oidc releases](https://github.com/coreos/go-oidc/releases) -- v3.17.0 verified Nov 21, 2025
- [golang-jwt/jwt releases](https://github.com/golang-jwt/jwt/releases) -- v5.3.1 verified Jan 28, 2025
- [golang-jwt CVE-2025-30204](https://github.com/golang-jwt/jwt/security/advisories/GHSA-mh63-6h87-95cp) -- memory allocation vulnerability
- infra-ctrl Pulumi Keycloak tests (`tests/keycloak.test.js`) -- `organization_id` claim mapper configuration verified

### Secondary (MEDIUM confidence)
- [pkg.go.dev go-oidc/v3](https://pkg.go.dev/github.com/coreos/go-oidc/v3/oidc) -- API reference, 1,821+ importers, oidctest package exists
- [oauth2-proxy Keycloak OIDC docs](https://oauth2-proxy.github.io/oauth2-proxy/configuration/providers/keycloak_oidc/) -- reference implementation patterns
- [Keycloak GitHub Discussion #32364](https://github.com/keycloak/keycloak/discussions/32364) -- organization_id mapping to access token
- Previous project research: `.planning/research/SUMMARY.md`, `ARCHITECTURE.md`, `PITFALLS.md`, `STACK.md`

### Tertiary (LOW confidence)
- go-oidc `oidctest` package -- documented but limited usage examples
- Keycloak 26+ organization mapper with ID inclusion -- may affect future claim format

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH -- all libraries verified on pkg.go.dev and GitHub releases with exact versions
- Architecture: HIGH -- full middleware chain traced through source code, integration points identified at line level
- Pitfalls: HIGH -- each pitfall backed by specific code references or documented issues
- Claims mapping: MEDIUM -- Keycloak claim format verified in Pulumi tests, but not yet validated against live token
- Governance integration: MEDIUM -- entity schemas understood, exact VirtualKey resolution path needs tracing during implementation

**Research date:** 2026-03-28
**Valid until:** 2026-04-28 (30 days -- stable domain, libraries have infrequent releases)
