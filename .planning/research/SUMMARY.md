# Project Research Summary

**Project:** Stragix Bifrost (LLM Gateway Fork)
**Domain:** Go LLM Gateway -- multi-tenant, multi-provider routing with OIDC auth
**Researched:** 2026-03-26
**Confidence:** HIGH

## Executive Summary

Bifrost is a high-performance Go LLM gateway (<100us overhead) that already provides 95% of what Stragix needs: OpenAI-compatible API routing, virtual key governance (customers/teams/budgets/rate limits), provider failover, Prometheus metrics, and a management UI. The two gaps -- multiple providers of the same type with different base URLs, and Keycloak OIDC SSO -- drove the decision to fork. However, **the architecture research uncovered that Bifrost's existing `CustomProviderConfig` mechanism already solves the multi-provider problem without any core code changes**. This is the single most important finding: what was originally scoped as a "fork core provider registry" effort is actually a config-only change using upstream's built-in escape hatch. The fork scope collapses from "two major features" to "one feature (OIDC) plus config."

The recommended approach is a two-track strategy. Track 1 is immediate: deploy named provider instances (llama-cpp, fireworks, together) using `CustomProviderConfig` with `base_provider_type: "openai"` -- zero Go code changes, just config.json restructuring in infra-ctrl. Track 2 is the actual fork work: add a generic OIDC middleware to the HTTP transport layer using `coreos/go-oidc/v3` (the standard Go OIDC library, 1,821 importers) plus `golang-jwt/jwt/v5` (already a transitive dependency, needs bump to v5.3.1 for CVE fix). The OIDC middleware slots into the existing fasthttp middleware chain alongside (not replacing) the current auth, following established patterns in the codebase.

The key risks are: (1) Go multi-module fork import path management -- the 14-module workspace must keep `maximhq/bifrost` import paths and use `go.work` to avoid merge hell on every upstream sync; (2) Refresh token race conditions under concurrent requests -- `singleflight.Group` must be used from day one; (3) Upstream merge conflicts in hot files -- OIDC code must live in new files (`handlers/oidc.go`, `framework/oidc/`) rather than modifying existing auth files, to minimize the diff surface. The CustomProviderConfig discovery eliminates the riskiest planned changes entirely (sync.Map key scheme, ParseModelString, config reconciliation).

## Key Findings

### Recommended Stack

Bifrost inherits a mature Go stack (fasthttp, sonic JSON, zerolog, GORM/SQLite, Prometheus). Our additions are minimal: one new direct dependency and one version bump.

**Core technologies:**
- **coreos/go-oidc/v3 (v3.17.0):** OIDC discovery + ID token verification -- the standard Go OIDC library, handles JWKS rotation, Keycloak-compatible via issuer URL
- **golang-jwt/jwt/v5 (v5.3.1):** JWT parsing on the hot path -- already an indirect dependency, must bump from v5.3.0 to fix CVE-2025-30204 (memory allocation vulnerability)
- **golang.org/x/oauth2 (v0.35.0):** OAuth2 client for token exchange -- already present, no changes needed
- **No new dependencies for provider instances:** CustomProviderConfig eliminates the need for any core library additions

**Critical version requirement:** golang-jwt v5.3.1 fixes CVE-2025-30204. Versions v5.0.0-rc.1 through v5.2.1 are vulnerable to excessive memory allocation during header parsing.

### Expected Features

**Must have (table stakes):**
- Named provider instances with independent base_url and keys per instance (llama-cpp, fireworks, together all OpenAI-compatible but different endpoints) -- achievable via CustomProviderConfig TODAY
- OIDC discovery and JWT validation at the gateway (Keycloak issuer URL, JWKS, audience/expiry checks)
- Claims-to-Bifrost entity mapping (organization_id -> Customer, sub -> User, groups -> Team)
- Config.json OIDC section alongside existing auth config

**Should have (differentiators):**
- Automatic Customer/VirtualKey provisioning from OIDC claims (zero manual gateway setup per tenant)
- Group-to-allowed-models mapping (Keycloak groups determine model access tiers)
- Per-instance cost tracking with custom pricing ($0 for self-hosted, per-token for cloud)
- Provider health-aware failover across named instances

**Defer (v2+):**
- Semantic caching (RAG product has its own caching layer)
- MCP Gateway support (not needed for RAG)
- Clustering/HA (single replica + K8s restarts sufficient for current scale)
- PKCE for public clients (not needed until UI is exposed as SPA)
- Session stickiness across instances (not needed for stateless chat completions)

### Architecture Approach

Bifrost is a fasthttp-based gateway with a channel-per-provider concurrency model. Requests flow through a middleware chain (SecurityHeaders -> CORS -> Decompression -> Router -> Tracing -> TransportInterceptor -> Prometheus -> Auth -> Handler) into the core `Bifrost` struct, which manages provider worker pools via `sync.Map` keyed by `ModelProvider` (a string). The `CustomProviderConfig` mechanism already allows registering arbitrary provider names backed by known provider types -- `createBaseProvider()` reads `CustomProviderConfig.BaseProviderType` to determine which constructor to call while using the custom name as the registry key. OIDC slots in as a new middleware that validates JWTs and extracts claims before the existing auth middleware, using dedicated context keys to avoid collision.

**Major components (for our work):**
1. **Config Layer (infra-ctrl overlays)** -- CustomProviderConfig entries for llama-cpp, fireworks, together with independent base_url/keys
2. **OIDC Middleware (new: `framework/oidc/` + `handlers/oidc.go`)** -- JWKS management, JWT validation, claims extraction, integrated into AuthMiddleware flow
3. **Claims-to-Governance Mapper** -- Maps OIDC sub/email/org claims to Bifrost Customer/Team/User entities for budget/rate limit enforcement
4. **Docker Build Pipeline** -- Multi-arch (amd64+arm64) image to Harbor/GHCR, extending existing Bifrost Dockerfile

### Critical Pitfalls

1. **Multi-module import path hell (CRITICAL)** -- Bifrost's 14 Go modules all import via `github.com/maximhq/bifrost/*`. Rewriting to `stragix-innovations/bifrost/*` would cause hundreds of merge conflicts on every upstream sync. **Prevention:** Keep `maximhq/bifrost` paths everywhere, use `go.work` for development, `replace` directives for Docker builds.

2. **Refresh token race condition (CRITICAL)** -- Concurrent requests with an expired access token all try to refresh simultaneously. With Keycloak's refresh token rotation, only the first succeeds; others get `invalid_grant` and destroy the session. **Prevention:** Use `singleflight.Group` to serialize refresh attempts. For API auth, validate JWT via JWKS (stateless, no refresh needed) and only use refresh for dashboard sessions.

3. **Upstream merge conflicts in hot files (CRITICAL)** -- `core/bifrost.go` (6400+ lines), `config.go`, `middlewares.go` are the most frequently changed upstream files. **Prevention:** Add all OIDC code in NEW files (`handlers/oidc.go`, `framework/oidc/`), never modify existing auth files. CustomProviderConfig eliminates the need to touch core files at all.

4. **JWKS key rotation window (MODERATE)** -- Tokens signed with a new key are rejected until go-oidc refreshes its cache. **Prevention:** Create `oidc.Provider` with `context.Background()` (never request context), retry once on unknown kid.

5. **OIDC context key collision (MODERATE)** -- Both OIDC middleware and existing auth middleware set context values. Name collisions silently overwrite routing data. **Prevention:** Use dedicated `BifrostContextKeyOIDC*` keys, short-circuit existing auth when OIDC token is present.

## Implications for Roadmap

### Phase 1: Named Instances via CustomProviderConfig (Config-Only)

**Rationale:** This is the highest-value, lowest-risk change. CustomProviderConfig already exists in upstream Bifrost. Zero Go code changes. Unblocks the entire multi-provider routing requirement immediately. Can be deployed and validated before any fork work begins.

**Delivers:** Multiple OpenAI-compatible providers (llama-cpp self-hosted, Fireworks cloud, Together cloud) routing through a single Bifrost instance, each with independent base_url, API keys, and model catalogs.

**Addresses:** PROV-01 (named instances), PROV-02 (model-to-instance routing), PROV-03 (config support). Partially addresses PROV-04/PROV-05 (API/UI work with custom provider names natively).

**Avoids:** Pitfall 2 (sync.Map key change), Pitfall 6 (model name collision -- requests use `llama-cpp/model` or `fireworks/model`), Pitfall 9 (config reconciliation -- no key scheme change).

**Implementation:** Update `infra-ctrl` overlay `configmap.yaml` to use CustomProviderConfig entries. Deploy upstream Bifrost image (no fork needed). Validate routing.

### Phase 2: Fork Setup and OIDC Foundation

**Rationale:** Before writing OIDC code, establish the fork correctly. Import path strategy is a one-time decision that poisons everything if wrong. Then build the OIDC middleware foundation: config parsing, JWKS management, JWT validation.

**Delivers:** A correctly-structured fork with `go.work` development workflow, CI build pipeline, and the core OIDC middleware that validates Keycloak JWTs and extracts claims.

**Uses:** coreos/go-oidc/v3 (JWKS, discovery), golang-jwt/jwt/v5.3.1 (hot-path JWT parsing), existing fasthttp middleware patterns.

**Implements:** AUTH-01 (OIDC provider), AUTH-03 (config section), DEPLOY-01 (Docker image).

**Avoids:** Pitfall 1 (import path hell -- decided upfront), Pitfall 4 (merge conflicts -- OIDC in new files only), Pitfall 5 (JWKS rotation -- context.Background + retry), Pitfall 8 (context collision -- dedicated OIDC keys).

### Phase 3: Claims Mapping and Governance Integration

**Rationale:** With JWT validation working, wire OIDC identity into Bifrost's governance system. This is the multi-tenant glue: organization_id claim -> Customer, sub -> User, groups -> Team. Without this, OIDC auth works but budget/rate-limit enforcement does not apply per-tenant.

**Delivers:** End-to-end flow: Keycloak user authenticates -> JWT validated -> claims extracted -> Customer resolved -> VirtualKey applied -> request routed to correct named instance with per-org budget and rate limits.

**Addresses:** AUTH-02 (claims mapping), plus differentiators: auto-provisioning, group-to-model mapping.

**Avoids:** Pitfall 3 (refresh token race -- singleflight.Group for dashboard sessions), Pitfall 12 (SQLite contention -- WAL mode, stateless JWT validation for API path).

### Phase 4: Deployment and Integration

**Rationale:** With features complete and tested, deploy the forked image, update infra-ctrl manifests, and validate end-to-end in dev/staging before prod.

**Delivers:** DEPLOY-01 (Docker image on Harbor/GHCR), DEPLOY-02 (infra-ctrl manifests updated), upstream merge CI job, multi-arch build.

**Avoids:** Pitfall 10 (CGO multi-arch -- test both architectures), Pitfall 11 (CORS -- server-side code exchange).

### Phase 5 (Optional): UI Enhancements and Differentiators

**Rationale:** Cosmetic and nice-to-have features that can be deferred without blocking the core use case.

**Delivers:** PROV-05 (UI display for named instances), per-instance cost dashboards, group-to-model management UI.

### Phase Ordering Rationale

- **Phase 1 first because it is zero-risk and immediately unblocks RAG product development.** No fork needed. Config change only. Can be validated in days, not weeks.
- **Phase 2 before Phase 3 because fork setup is a one-time foundation decision.** Getting import paths wrong would require reworking every subsequent commit. OIDC middleware is the largest new code surface and needs to be built before the governance integration.
- **Phase 3 after Phase 2 because claims mapping depends on working JWT validation.** Cannot wire org_id -> Customer without first being able to extract org_id from the token.
- **Phase 4 after Phase 3 because deployment requires complete features.** Partial OIDC (validation without governance) is useful for testing but not production.
- **Phase 5 last because it is cosmetic.** Named instances already display in the UI via CustomProviderConfig. Enhancements are nice-to-have.

### Research Flags

Phases likely needing deeper research during planning:
- **Phase 2 (fork setup):** The Go multi-module workspace interaction with `go.work` vs `replace` directives in Docker builds needs hands-on validation. Bifrost's `make setup-workspace` may need extension.
- **Phase 3 (claims mapping):** The exact Keycloak claim format for `organization_id` and group membership needs validation against the actual Stragix Keycloak realm configuration. The governance plugin's user/customer resolution path needs tracing to identify exact integration points.

Phases with standard patterns (skip research-phase):
- **Phase 1 (config-only):** CustomProviderConfig is documented in Bifrost source and already works. Standard config change.
- **Phase 4 (deployment):** Standard Docker multi-arch build + infra-ctrl overlay pattern. Well-established in Stragix.
- **Phase 5 (UI):** Bifrost UI is React. Standard component work.

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| Stack | HIGH | All libraries verified on pkg.go.dev with exact versions. Only 1 new dependency. CVE verified. |
| Features | HIGH | Based on direct Bifrost source code analysis and competitive landscape research (LiteLLM, TensorZero, Portkey). |
| Architecture | HIGH | Full request path traced through source code. CustomProviderConfig mechanism verified in `createBaseProvider()`. All 5 sync.Map structures and their key schemes documented from code. |
| Pitfalls | HIGH | Each pitfall backed by specific code references or upstream GitHub issues. Refresh token race documented across 3 independent projects (oauth2-proxy, Claude CLI, Drupal). |

**Overall confidence:** HIGH

The CustomProviderConfig discovery fundamentally de-risks this project. The original scope assumed we needed to modify Bifrost's core provider registry -- a change that would touch 15+ files across 3 modules and create permanent merge conflicts with upstream. Instead, the provider instance requirement is satisfied by existing upstream functionality. The remaining fork work (OIDC) is confined to the transport layer and can be implemented in new files without modifying existing code.

### Gaps to Address

- **Keycloak claim format validation:** The exact JSON path for `organization_id` in Keycloak tokens depends on how the claim is configured in the Stragix realm (custom claim mapper vs protocol mapper). Verify during Phase 3 planning by inspecting an actual token from the stragixlabs realm.
- **CustomProviderConfig + keyless mode:** The `is_key_less: true` flag on CustomProviderConfig has been verified in source to exist, but needs hands-on validation with llama-cpp (does the OpenAI provider correctly skip auth headers when keyless?). Test during Phase 1.
- **Upstream merge cadence:** Bifrost is actively developed (20+ commits in recent weeks). The merge frequency and conflict rate need to be established empirically during Phase 2. Set up the CI merge-check job early.
- **SQLite WAL mode:** Bifrost's GORM setup may or may not already enable WAL mode. Verify before adding OIDC session writes.
- **go-oidc v3 `oidctest` package:** Documented as available since v3.14.0 but has limited documentation. May need to fall back to hand-rolled mock OIDC server for testing. Evaluate during Phase 2.

## Sources

### Primary (HIGH confidence)
- Bifrost source code: `core/bifrost.go`, `core/schemas/bifrost.go`, `core/schemas/provider.go`, `core/schemas/utils.go`, `core/schemas/account.go` -- provider registry, CustomProviderConfig, ModelProvider type, ParseModelString
- Bifrost source code: `transports/bifrost-http/handlers/middlewares.go`, `session.go`, `oauth2.go` -- auth middleware, session management, existing OAuth2 flow
- Bifrost source code: `transports/bifrost-http/lib/config.go`, `account.go` -- config loading, provider reconciliation
- Bifrost source code: `framework/configstore/clientconfig.go` -- ProviderConfig, AuthConfig
- [coreos/go-oidc releases](https://github.com/coreos/go-oidc/releases) -- v3.17.0 verified
- [golang-jwt/jwt CVE-2025-30204](https://github.com/golang-jwt/jwt/security/advisories/GHSA-mh63-6h87-95cp) -- memory allocation vulnerability, fixed in v5.3.1
- [oauth2-proxy Keycloak OIDC docs](https://oauth2-proxy.github.io/oauth2-proxy/configuration/providers/keycloak_oidc/) -- reference OIDC implementation

### Secondary (MEDIUM confidence)
- [oauth2-proxy Issue #1992](https://github.com/oauth2-proxy/oauth2-proxy/issues/1992) -- refresh token race condition pattern
- [coreos/go-oidc Issue #339](https://github.com/coreos/go-oidc/issues/339) -- context caching bug in RemoteKeySet
- [Working With Forks in Go](https://blog.sgmansfield.com/2016/06/working-with-forks-in-go/) -- import path rewriting challenges
- [VictoriaMetrics: Go sync.Map](https://victoriametrics.com/blog/go-sync-map/) -- sync.Map performance characteristics

### Tertiary (LOW confidence)
- [Go Issue #74884](https://github.com/golang/go/issues/74884) -- proposal to simplify fork module management (not yet implemented)
- go-oidc `oidctest` package availability -- limited documentation, needs validation

---
*Research completed: 2026-03-26*
*Ready for roadmap: yes*
