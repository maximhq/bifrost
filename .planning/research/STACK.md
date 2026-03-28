# Technology Stack

**Project:** Stragix Bifrost (LLM Gateway Fork)
**Researched:** 2026-03-26

## Existing Stack (Upstream Bifrost)

The fork inherits a well-defined Go stack. Our additions must integrate seamlessly, not replace existing libraries.

| Category | Library | Version | Notes |
|----------|---------|---------|-------|
| Language | Go | 1.26.1 | Multi-module workspace |
| HTTP Framework | valyala/fasthttp | v1.68.0 | NOT net/http -- all middleware uses `fasthttp.RequestCtx` |
| Router | fasthttp/router | v1.5.4 | Path-parameter routing |
| JSON | bytedance/sonic | v1.15.0 | High-performance JSON (used throughout) |
| JSON query | tidwall/gjson / sjson | v1.18.0 / v1.2.5 | JSON path get/set without full unmarshal |
| JWT | golang-jwt/jwt/v5 | v5.3.0 | Already a transitive dependency |
| OAuth2 | golang.org/x/oauth2 | v0.35.0 | Already used in core for provider OAuth flows |
| ORM | gorm.io/gorm | v1.31.1 | Config store, governance |
| DB | gorm.io/driver/sqlite | v1.6.0 | Default storage (SQLite) |
| Logging | rs/zerolog | v1.34.0 | Structured logging via custom Logger interface |
| Metrics | prometheus/client_golang | v1.23.2 | Prometheus metrics |
| Testing | stretchr/testify | v1.11.1 | assert/require throughout |
| UUID | google/uuid | v1.6.0 | Standard UUID generation |
| Compression | andybalholm/brotli, klauspost/compress | v1.2.0, v1.18.2 | Request decompression |

## Recommended Additions

### 1. OIDC Discovery and Token Verification

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| coreos/go-oidc/v3 | v3.17.0 | OIDC discovery, ID token verification, JWKS rotation | The standard Go OIDC library. 1,821 known importers. Used by oauth2-proxy, Dex, and every major Go OIDC integration. Handles `.well-known/openid-configuration` discovery, JWKS fetching, key rotation, and ID token claims extraction. Keycloak-compatible out of the box via `{server}/realms/{realm}` issuer URL. |

**Confidence:** HIGH -- verified via pkg.go.dev (v3.17.0 published Nov 2025), oauth2-proxy reference implementation, and Keycloak official documentation.

**Why not alternatives:**

| Library | Why Not |
|---------|---------|
| Nerzal/gocloak | Admin SDK for managing Keycloak realms/users. Overkill -- we only need token validation, not admin operations. Platform-service handles user provisioning via Keycloak admin API through auth-proxy. |
| erajayatech/go-keycloak-middleware | Gin-specific. We use fasthttp, not Gin. |
| hugocortes/go-keycloak | Service-account focused. Low adoption (<50 importers). |
| Roll our own JWKS fetcher | Fragile. Key rotation, caching, clock skew -- go-oidc handles all of this correctly. |

### 2. JWT Validation (Already Present)

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| golang-jwt/jwt/v5 | v5.3.0 (upgrade to v5.3.1) | JWT parsing, claims extraction, signature verification | Already an indirect dependency in Bifrost. Upgrade to v5.3.1 (Jan 2025) which includes CVE-2025-30204 fix for excessive memory allocation during header parsing. IMPORTANT: v5.2.1 and earlier are vulnerable. |

**Confidence:** HIGH -- verified v5.3.1 release on GitHub (Jan 28, 2025). CVE-2025-30204 affects v5.0.0-rc.1 through v5.2.1.

**Why golang-jwt and not go-oidc alone:** go-oidc handles ID token verification. But for the Bifrost middleware, we need to validate access tokens on every API request (hot path). golang-jwt gives us direct control over:
- Custom claims structs (Keycloak's `realm_access.roles`, `resource_access.{client}.roles`, `organization_id`)
- Parser options (`WithIssuer`, `WithAudience`, `WithLeeway`)
- JWKS key function integration (feed go-oidc's `RemoteKeySet` as the `jwt.Keyfunc`)

### 3. OAuth2 (Already Present)

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| golang.org/x/oauth2 | v0.35.0 | OAuth2 client, token exchange, PKCE | Already used by Bifrost for provider OAuth flows. go-oidc extends this with OIDC-specific functionality. No version change needed. |

**Confidence:** HIGH -- already in go.mod, no change required.

### 4. No New Dependencies for Provider Registry

The named provider instances feature requires **zero new dependencies**. The changes are to Bifrost's internal data structures:

- `core/schemas/bifrost.go`: `ModelProvider` type (currently `string`) -- extend to support `type:instance` composite keys
- `core/bifrost.go`: Provider lookup via `atomic.Pointer[[]schemas.Provider]` -- modify `getProviderByKey` to match composite keys
- `transports/bifrost-http/lib/config.go`: Config parsing -- split provider names on `:` delimiter

The existing `sync.Map` for request queues and the `atomic.Pointer[[]schemas.Provider]` for the provider slice are well-suited for the named instances pattern because:
- Provider registrations are infrequent (config load/reload), reads are on every request
- `sync.Map` excels at read-heavy workloads with stable key sets
- No concurrency primitive changes needed

**Confidence:** HIGH -- verified by reading `core/bifrost.go` (lines 61-86), `core/schemas/bifrost.go` (lines 34-60), and the provider lookup at line 3824.

### 5. Testing Libraries

| Technology | Version | Purpose | Why |
|------------|---------|---------|-----|
| stretchr/testify | v1.11.1 | Assertions, require | Already the testing standard in the codebase. 100+ test files use it. |
| valyala/fasthttp/fasthttputil | (bundled) | In-memory listener for middleware tests | Already available -- fasthttp ships with `fasthttputil.NewInmemoryListener()`. The existing middleware tests use a simpler pattern: construct `&fasthttp.RequestCtx{}` directly, set headers, call middleware chain. Follow this pattern. |
| coreos/go-oidc/v3/oidctest | v3.17.0 | Mock OIDC provider for testing | Added in go-oidc v3.14.0. Provides test helpers to create mock OIDC providers with configurable responses. Critical for testing the OIDC middleware without a running Keycloak. |

**Confidence:** HIGH for testify (already used), MEDIUM for oidctest (added in v3.14.0, verified on pkg.go.dev but limited docs).

**Testing pattern for fasthttp middleware** (established by the codebase):

```go
// Pattern from middlewares_test.go -- construct RequestCtx directly
func TestOIDCMiddleware_ValidToken(t *testing.T) {
    ctx := &fasthttp.RequestCtx{}
    ctx.Request.Header.Set("Authorization", "Bearer <token>")

    nextCalled := false
    next := func(ctx *fasthttp.RequestCtx) { nextCalled = true }

    middleware := OIDCMiddleware(config)
    handler := middleware(next)
    handler(ctx)

    assert.True(t, nextCalled)
    assert.Equal(t, "user-sub", ctx.UserValue("oidc-sub"))
}
```

For integration tests requiring a full OIDC server, use `oidctest` to spin up a mock issuer that serves `.well-known/openid-configuration` and JWKS endpoints.

## Full Dependency Addition Summary

Only **one new direct dependency** is needed:

```bash
# In the transports module (where middleware lives)
cd transports
go get github.com/coreos/go-oidc/v3@v3.17.0
```

And one version bump:

```bash
# Upgrade golang-jwt to fix CVE-2025-30204
go get github.com/golang-jwt/jwt/v5@v5.3.1
```

Everything else (oauth2, fasthttp, testify, sonic, zerolog) is already present at current versions.

## What NOT to Use

| Library | Why Avoid |
|---------|-----------|
| net/http middleware (chi, gorilla, echo) | Bifrost uses fasthttp exclusively. Mixing net/http handlers would require adapter overhead and break the <100us performance guarantee. |
| casbin / ladon / OPA | Over-engineered for our auth needs. We need OIDC token validation, not a policy engine. Bifrost's existing governance plugin handles authorization (budgets, rate limits, VK permissions). |
| gin-gonic/gin | Wrong HTTP framework. Bifrost is fasthttp. |
| ory/fosite | Full OAuth2 server implementation. We are a client/resource server, not an authorization server. Keycloak is the IdP. |
| lestrrat-go/jwx | Alternative JWT library. Would conflict with golang-jwt which is already an indirect dependency. Two JWT libraries in one project causes confusion. |
| zitadel/oidc | Full OIDC server + client library. Heavy. coreos/go-oidc is lighter and more widely used for client-only needs. |

## Architecture Fit

### How New Libraries Integrate with Existing Bifrost

```
Request Flow (current):
  fasthttp.Server
    -> SecurityHeadersMiddleware
    -> CorsMiddleware
    -> RequestDecompressionMiddleware
    -> TransportInterceptorMiddleware (plugins)
    -> AuthMiddleware (Basic/Bearer session)
    -> TracingMiddleware
    -> Handler (inference, API, etc.)

Request Flow (with OIDC):
  fasthttp.Server
    -> SecurityHeadersMiddleware
    -> CorsMiddleware
    -> RequestDecompressionMiddleware
    -> TransportInterceptorMiddleware (plugins)
    -> OIDCMiddleware (NEW -- validates JWT, extracts claims, sets ctx values)
       OR AuthMiddleware (existing -- for non-OIDC fallback)
    -> TracingMiddleware
    -> Handler
```

The OIDC middleware follows the exact same pattern as `AuthMiddleware`:
- Implements `schemas.BifrostHTTPMiddleware` (type `func(next fasthttp.RequestHandler) fasthttp.RequestHandler`)
- Reads `Authorization: Bearer <jwt>` header from `fasthttp.RequestCtx`
- Validates token using go-oidc verifier + golang-jwt claims parsing
- Sets user values on context (`ctx.SetUserValue(...)`) for downstream handlers
- Skips configured whitelisted routes (health, login, OAuth callback)

### Config Integration

OIDC config goes in the existing `config.json` alongside current SSO config:

```json
{
  "auth": {
    "oidc": {
      "enabled": true,
      "issuer_url": "https://keycloak.stragix.com/realms/stragixlabs",
      "client_id": "bifrost",
      "client_secret": "${OIDC_CLIENT_SECRET}",
      "scopes": ["openid", "profile", "email"],
      "claims_mapping": {
        "org_id": "organization_id",
        "roles": "realm_access.roles"
      }
    }
  }
}
```

This integrates with the existing `configstore.AuthConfig` and the `lib.Config` struct that all middleware receives.

## Go Module Strategy

Bifrost uses a multi-module workspace. The key modules we modify:

| Module | go.mod path | Changes |
|--------|-------------|---------|
| `core` | `core/go.mod` | ModelProvider type changes (no new deps) |
| `transports` | `transports/go.mod` | Add go-oidc, bump golang-jwt |
| `framework` | `framework/go.mod` | Possibly AuthConfig struct changes (no new deps) |

This means go-oidc only enters the `transports` module, keeping `core` dependency-free of OIDC concerns. Clean separation.

## Version Verification Summary

| Library | Claimed | Verified | Source |
|---------|---------|----------|--------|
| coreos/go-oidc/v3 | v3.17.0 | v3.17.0 (Nov 21, 2025) | [GitHub Releases](https://github.com/coreos/go-oidc/releases) |
| golang-jwt/jwt/v5 | v5.3.1 | v5.3.1 (Jan 28, 2025) | [GitHub Releases](https://github.com/golang-jwt/jwt/releases) |
| golang.org/x/oauth2 | v0.35.0 | Already in go.mod | Bifrost core/go.mod line 29 |
| stretchr/testify | v1.11.1 | Already in go.mod | Bifrost core/go.mod line 24 |
| valyala/fasthttp | v1.68.0 | Already in go.mod | Bifrost core/go.mod line 27 |

## Sources

- [coreos/go-oidc releases](https://github.com/coreos/go-oidc/releases) -- v3.17.0 verified
- [golang-jwt/jwt releases](https://github.com/golang-jwt/jwt/releases) -- v5.3.1 verified, CVE-2025-30204 fix
- [golang-jwt/jwt CVE-2025-30204](https://github.com/golang-jwt/jwt/security/advisories/GHSA-mh63-6h87-95cp) -- memory allocation vulnerability
- [oauth2-proxy Keycloak OIDC docs](https://oauth2-proxy.github.io/oauth2-proxy/configuration/providers/keycloak_oidc/) -- reference implementation patterns
- [pkg.go.dev coreos/go-oidc/v3/oidc](https://pkg.go.dev/github.com/coreos/go-oidc/v3/oidc) -- 1,821 importers
- [fasthttp testing patterns](https://github.com/valyala/fasthttp/issues/36) -- InmemoryListener and direct RequestCtx construction
- [VictoriaMetrics sync.Map analysis](https://victoriametrics.com/blog/go-sync-map/) -- sync.Map performance characteristics
- Bifrost source code: `core/bifrost.go`, `core/schemas/bifrost.go`, `transports/bifrost-http/handlers/middlewares.go`, `transports/bifrost-http/handlers/middlewares_test.go`
