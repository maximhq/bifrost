# Domain Pitfalls

**Domain:** Go LLM Gateway Fork (Bifrost) -- Named Provider Instances + OIDC Integration
**Researched:** 2026-03-26

---

## Critical Pitfalls

Mistakes that cause rewrites, data loss, or production outages.

### Pitfall 1: Multi-Module Import Path Hell on Fork

**What goes wrong:** Bifrost is a 14-module Go monorepo where `transports` imports `core`, `framework`, and 8 plugin modules -- all via `github.com/maximhq/bifrost/*` import paths. Changing the module path to `github.com/stragix-innovations/bifrost/*` requires rewriting import paths in every `.go` file across all 14 modules, every `go.mod`, and every `go.sum`. This is hundreds of files. And every upstream merge conflicts on every single one of those changed lines.

**Why it happens:** Go modules identify themselves by their `go.mod` module path. Intra-repo imports use the full module path (e.g., `github.com/maximhq/bifrost/core/schemas`). Forking changes the GitHub path but Go does not understand that `stragix-innovations/bifrost` is the same as `maximhq/bifrost`.

**Consequences:**
- If you rewrite all import paths: every upstream merge becomes a nightmare. Every file they touch conflicts because every import line is different.
- If you use `replace` directives: works for local builds but breaks `go install` for consumers, and you need `replace` in every one of 14 `go.mod` files.
- If you do nothing: code compiles against the published upstream versions, not your local changes.

**Prevention:** Do NOT rewrite import paths. Use the Go workspace approach (`go.work`) that Bifrost already supports via `make setup-workspace`. The `go.work` file is gitignored and maps all `github.com/maximhq/bifrost/*` modules to local directories. For the Docker build (CI), use `replace` directives injected at build time, or better: use `go.work` in the Dockerfile since this is a binary (not a library). The module paths stay as `maximhq/bifrost` everywhere in source -- your fork just happens to host the same paths.

**Detection:** Build failures after upstream merge with errors like "module github.com/stragix-innovations/bifrost/core not found". Hundreds of merge conflicts in import statements.

**Phase:** Must be decided in Phase 1 (fork setup) before any code changes. Wrong choice here poisons every subsequent merge.

---

### Pitfall 2: sync.Map Key Scheme Change Breaks Concurrent Request Routing

**What goes wrong:** The core `Bifrost` struct uses three `sync.Map` instances keyed by `schemas.ModelProvider` (a string type): `requestQueues`, `waitGroups`, and `providerMutexes`. Today the key is a bare provider name like `"openai"`. The named-instance feature changes keys to `"openai:llama-cpp"`, `"openai:fireworks"`, etc. If the old key `"openai"` and new key `"openai:fireworks"` coexist in the maps during a hot-reload or config reconciliation, requests route to the wrong provider queue. Worse: `getProviderQueue()` does a `LoadOrStore` with double-check locking -- if one goroutine resolves `"openai"` while another resolves `"openai:fireworks"`, the old queue could serve new requests.

**Why it happens:** `sync.Map` provides per-key atomicity but no cross-key transaction guarantees. `Range` does not provide a consistent snapshot. The `getProviderMutex()` function creates a per-provider `sync.RWMutex` via `LoadOrStore(providerKey, &sync.RWMutex{})` -- changing the key scheme means old mutexes (keyed by `"openai"`) are never cleaned up and new mutexes (keyed by `"openai:fireworks"`) provide no mutual exclusion with the old ones.

**Consequences:** Race conditions during config reload. Requests sent to wrong provider. Goroutine leaks from orphaned worker channels. Potential panic from sending on closed channel if old ProviderQueue is closed but new key resolves to old queue during transition.

**Prevention:**
1. Make the key change backward-compatible: if no `:` in the key, behavior is identical to upstream. The `ModelProvider` type is already a string, so `"openai:fireworks"` is a valid value without type changes.
2. The `createBaseProvider()` switch already extracts the base provider type from `CustomProviderConfig.BaseProviderType` -- mirror this pattern for named instances: strip the instance suffix to determine which provider constructor to call.
3. Add integration tests that hot-reload config from single-provider to named-instance and verify no request is lost during transition.
4. Never store both `"openai"` and `"openai:fireworks"` simultaneously -- migration must be atomic (remove old, add new within the same `providerMutex` lock).

**Detection:** Race detector (`go test -race`) catches most issues. Monitor for "provider X not found in request queues" errors in logs. Watch for goroutine count growth after config reloads.

**Phase:** Phase 2 (named provider instances). Must get this right before shipping to production. Requires thorough concurrent testing.

---

### Pitfall 3: Refresh Token Race Condition Under Concurrent Requests

**What goes wrong:** When multiple API requests arrive simultaneously and the OIDC access token is expired, each request independently tries to refresh the token. With refresh token rotation (where Keycloak invalidates the old refresh token upon use), the first refresh succeeds but the second gets `invalid_grant` because its refresh token was already consumed. The second request's session is destroyed, logging the user out mid-session.

**Why it happens:** This is a well-documented issue across the OAuth ecosystem (oauth2-proxy issues #1992 and #1006, Claude Code CLI issue #27933, Drupal, better-auth). It stems from the fundamental mismatch between single-use refresh tokens and concurrent HTTP requests. Bifrost uses `fasthttp` which handles requests concurrently by design -- any OIDC middleware will face this immediately.

**Consequences:** Intermittent 401 errors. Users randomly logged out during normal usage. Especially bad for the RAG product where frontend makes parallel API calls (chat + model list + status).

**Prevention:**
1. Implement a token refresh mutex: serialize refresh attempts so only one goroutine refreshes while others wait for the result. Use `singleflight.Group` from `golang.org/x/sync/singleflight` -- it is designed exactly for this (deduplicate concurrent calls for the same key).
2. If using Keycloak's refresh token rotation, configure a reuse grace period (Keycloak 26+ supports `reuseInterval` in the realm token settings) so the same refresh token can be used within a short window.
3. Alternatively: validate the access token via JWKS (no network call, no race) and only refresh when the token is truly expired, not on every request. Cache the verified claims.

**Detection:** Intermittent 401 errors in logs correlated with bursts of concurrent requests. Multiple `POST /token` requests to Keycloak within the same millisecond for the same user. Users reporting random logouts.

**Phase:** Phase 3 (OIDC integration). This is the single most likely production bug in the OIDC implementation. Design the token management layer around this constraint from the start.

---

### Pitfall 4: Upstream Merge Conflicts in Hot Files

**What goes wrong:** Upstream Bifrost is actively developed (20+ commits in recent weeks, including UI changes, provider fixes, CORS fixes, and new features). The files we need to modify (`core/bifrost.go` at 6400+ lines, `transports/bifrost-http/lib/config.go`, `handlers/middlewares.go`, `core/schemas/bifrost.go`) are also the files upstream changes most frequently. Every upstream merge will conflict in these files.

**Why it happens:** Bifrost concentrates logic in a few large files rather than many small ones. `bifrost.go` alone is ~6400 lines containing all request routing, provider management, plugin orchestration, and MCP handling. Any upstream change to routing or provider handling conflicts with our named-instance changes. Any upstream change to auth or middleware conflicts with our OIDC changes.

**Consequences:** Merge conflicts that require understanding both our changes and upstream's changes. Risk of silently breaking either our features or upstream features during conflict resolution. Merge fatigue leading to falling behind upstream, which compounds the problem.

**Prevention:**
1. Minimize the diff surface: instead of modifying `bifrost.go` directly, extract named-instance logic into a new file (`bifrost_named_instances.go`) and hook into the existing flow at minimal touch points.
2. Similarly, add OIDC as a new file (`handlers/oidc.go`) rather than modifying the existing `middlewares.go` or `session.go`.
3. Use composition over modification: wrap existing functions rather than changing them. For example, wrap `createBaseProvider()` with a function that strips the instance suffix, then delegates to the original.
4. Set up automated upstream merge testing: a CI job that attempts `git merge upstream/main` weekly and alerts on conflicts.
5. Periodically (monthly) rebase or merge upstream to keep the delta small.

**Detection:** CI merge-check job fails. `git merge upstream/main --no-commit` in a test branch shows conflicts. Conflict count trending up over time.

**Phase:** Affects all phases. Establish the fork strategy and merge discipline in Phase 1. Every subsequent phase must follow the "minimize diff surface" principle.

---

## Moderate Pitfalls

### Pitfall 5: JWKS Key Rotation Window Causes Token Rejection

**What goes wrong:** When Keycloak rotates its signing keys (scheduled or forced), tokens signed with the new key are rejected until the `coreos/go-oidc` `RemoteKeySet` refreshes its cache. The v3 library removed the Cache-Control dependency but still has a race window: tokens signed between key rotation and JWKS refresh fail validation.

**Why it happens:** `coreos/go-oidc` v3 caches JWKS keys and only refreshes when an unknown `kid` is encountered. But the refresh is not instantaneous -- it requires an HTTP call to Keycloak's `/.well-known/openid-configuration` and then to the `jwks_uri`. During this window, all tokens signed with the new key fail. Additionally, `RemoteKeySet` stores the context from creation time (issue #339) -- if the creation context is canceled, all subsequent JWKS fetches fail silently.

**Prevention:**
1. Create the `oidc.Provider` with `context.Background()`, never with a request context. Cache the provider as a long-lived singleton.
2. Implement retry-on-unknown-kid: when token verification fails with "failed to verify id token signature", trigger an immediate JWKS refresh and retry once.
3. In Keycloak, set the key rotation policy to keep old keys valid for a grace period (default is already reasonable, but verify).

**Detection:** Sudden spike in 401 errors that self-resolves within seconds. Errors containing "failed to verify id token signature" in logs.

**Phase:** Phase 3 (OIDC integration). Must be handled in the token verification implementation.

---

### Pitfall 6: Model Name Collision Across Named Instances

**What goes wrong:** Two named instances (e.g., `openai:fireworks` and `openai:together`) might both serve a model called `llama-3.3-70b`. When a request comes in for model `llama-3.3-70b` without specifying the provider instance, the router does not know which instance to use. With the current Bifrost design, the request includes both `provider` and `model` -- but if governance/virtual-keys restrict a customer to specific instances, the routing resolution becomes ambiguous.

**Why it happens:** The existing Bifrost model-to-provider mapping is 1:1 (one model name maps to exactly one provider). Named instances break this assumption because the same model exists on multiple instances. The governance plugin resolves provider from virtual key config, but `ProviderConfig` has a `provider` field that currently holds a simple name like `"openai"`, not `"openai:fireworks"`.

**Consequences:** Requests routed to wrong backend. Customer billed at cloud rates when they should be on self-hosted. Failover chains not working because fallback resolves to same model on same failed instance.

**Prevention:**
1. Require the full `type:instance` key in all routing contexts -- never allow bare `"openai"` when named instances are configured.
2. Update the governance plugin's `ProviderConfig.Provider` field to use the full named-instance key.
3. Build a model catalog that maps `model_name -> [instance1, instance2]` with explicit priority/preference.
4. For backward compatibility: if only one instance of a type exists, bare name still works (this matches PROJECT.md's constraint).

**Detection:** Requests landing on unexpected providers visible in Prometheus metrics (`bifrost_requests_total` by provider label). Cost discrepancies between expected and actual usage.

**Phase:** Phase 2 (named provider instances). Design the routing resolution before implementing, especially the interaction with governance.

---

### Pitfall 7: Streaming Response Passthrough with Different Provider Behaviors

**What goes wrong:** Named instances using the `openai` provider type connect to endpoints with subtly different streaming behaviors. llama-cpp's SSE format may differ from Fireworks or Together in: chunk boundaries, `finish_reason` placement, usage reporting in stream, error mid-stream formatting. The same `openai` provider code processes all of them, but the actual server responses vary.

**Why it happens:** OpenAI-compatible does not mean identical. Each provider adds quirks: some send usage in the last chunk, some send a separate `[DONE]` message, some include `system_fingerprint`, some do not. llama-cpp in particular sends usage differently depending on the `--metrics` flag and may send empty content deltas that upstream OpenAI never sends.

**Consequences:** Streaming responses partially parsed. Usage metrics not captured (budget tracking fails). Client-side JSON parsing errors. Governance plugin miscounts tokens.

**Prevention:**
1. For each named instance, run the integration test suite against the actual endpoint before going to production. Bifrost has a `tests/` directory with provider tests -- extend them for each named instance.
2. Use Bifrost's `send_back_raw_response: true` flag during development to capture exactly what each provider returns.
3. Consider using `CustomProviderConfig.AllowedRequests` to restrict each instance to only the request types it actually supports (e.g., llama-cpp probably does not do image generation).

**Detection:** Partial streaming responses in client. `stream_chunk_parse_error` in logs. Budget enforcement not triggering despite high usage.

**Phase:** Phase 2 (named provider instances), specifically during integration testing.

---

### Pitfall 8: OIDC Context Leak into BifrostContext

**What goes wrong:** Bifrost uses a custom `BifrostContext` that wraps Go's `context.Context` with additional key-value storage. OIDC middleware needs to set user identity (sub, email, org) into the context for the governance plugin to use. But `BifrostContext` stores values by arbitrary string keys -- name collisions with existing keys (like `BifrostContextKeySessionToken`, `BifrostContextKeyRequestID`) can silently overwrite critical routing data.

**Why it happens:** Both the auth middleware and the OIDC middleware need to set `BifrostContextKeySessionToken`. The existing auth flow creates session tokens in SQLite -- the OIDC flow validates JWT tokens. If both paths execute (e.g., OIDC middleware sets the token, then the existing auth middleware tries to validate it as a SQLite session), the request fails auth.

**Consequences:** 401 errors for OIDC-authenticated users. Session token from OIDC overwritten by auth middleware's session check. Governance plugin receives wrong user identity.

**Prevention:**
1. OIDC must integrate with the existing `AuthMiddleware` flow, not beside it. Either replace the auth check for OIDC-configured instances, or create a new auth path that short-circuits before the SQLite session check.
2. Add an OIDC-specific context key (e.g., `BifrostContextKeyOIDCClaims`) that does not conflict with existing keys.
3. Wire OIDC identity into the governance plugin's user resolution: map OIDC `sub` to Bifrost User, `organization` claim to Bifrost Customer.
4. Use the existing `shouldSkip` pattern in `AuthMiddleware.middleware()` to bypass password-based auth when OIDC token is present.

**Detection:** OIDC-authenticated requests returning 401. Governance plugin logging "unknown user" for authenticated requests. Context key dump showing unexpected values.

**Phase:** Phase 3 (OIDC integration). Critical design decision -- must be planned before coding.

---

### Pitfall 9: Config Reconciliation Does Not Know About Named Instances

**What goes wrong:** Bifrost's config system (`config.go`) reconciles providers by comparing a hash of the file-based config with the DB-stored config. Provider keys in the DB are bare names (`"openai"`). Adding named instances (`"openai:fireworks"`) causes the reconciler to treat them as entirely new providers (correct) but also leaves the old `"openai"` entry as an orphan (incorrect). The hash comparison uses the provider name as the primary key for diffing -- changing the key scheme breaks the diff logic.

**Why it happens:** `reconcileProviderKeys()` matches providers by `schemas.ModelProvider` key. If the config file has `"openai:fireworks"` but the DB has `"openai"`, the reconciler sees: one new provider added, one existing provider unchanged. It does not detect that `"openai:fireworks"` replaces `"openai"`.

**Consequences:** Orphaned provider configs in the DB consuming resources. Workers running for providers that should no longer exist. Config drift between file and DB state.

**Prevention:**
1. Add migration logic: when a config file has `"openai:fireworks"` and the DB has bare `"openai"`, detect this as a rename/replacement.
2. Alternatively: use the `CustomProviderConfig` mechanism which already supports arbitrary provider keys via `base_provider_type`. Named instances could be implemented entirely through `CustomProviderConfig` without changing the core key scheme at all -- the key would be `"llama-cpp"` with `base_provider_type: "openai"` and a custom `base_url`. This avoids changing the key scheme entirely.
3. If using the `type:instance` scheme, add a cleanup step to `reconcileProviders` that removes any bare-name provider when named instances of the same type exist.

**Detection:** `GetConfiguredProviders()` returns unexpected provider list. Workers running for providers not in config file. Memory usage higher than expected.

**Phase:** Phase 2 (named provider instances). Consider whether `CustomProviderConfig` is a better approach than changing the core key scheme.

---

## Minor Pitfalls

### Pitfall 10: Docker Multi-Arch Build Breaks on CGO Dependencies

**What goes wrong:** Bifrost uses `gorm.io/driver/sqlite` which depends on CGO (C compiler) for SQLite. Multi-arch Docker builds (`amd64 + arm64`) require cross-compilation, and CGO cross-compilation needs platform-specific C libraries and toolchains.

**Prevention:** Use `CGO_ENABLED=1` with `gcc-aarch64-linux-gnu` for arm64 cross-compilation, or use `mattn/go-sqlite3` with `--tags "sqlite_fk"`. Alternatively, use a multi-stage build with QEMU emulation. The existing Bifrost Dockerfile likely handles this already -- check and extend rather than rewriting.

**Phase:** Phase 4 (deployment). Standard Docker multi-arch concern.

---

### Pitfall 11: CORS Misconfiguration with OIDC Redirect Flow

**What goes wrong:** OIDC login redirects the browser to Keycloak, which redirects back to Bifrost's callback URL. If the CORS middleware does not allow the Keycloak origin, the redirect callback fails. The existing CORS middleware allows localhost and configured origins but knows nothing about Keycloak.

**Prevention:** The OIDC redirect flow is not a CORS issue (it is a full-page redirect, not an XHR). However, if the frontend makes XHR calls to Keycloak's token endpoint (e.g., for silent refresh or PKCE code exchange), CORS applies on the Keycloak side, not the Bifrost side. Ensure the OIDC flow uses server-side token exchange (Bifrost callback handler exchanges the code for tokens) rather than client-side PKCE to avoid CORS complexity.

**Phase:** Phase 3 (OIDC integration). Design choice: server-side code exchange preferred.

---

### Pitfall 12: SQLite Lock Contention with OIDC Session Storage

**What goes wrong:** Bifrost uses SQLite for its config store and session store. Adding OIDC sessions (access tokens, refresh tokens, user mappings) increases write frequency. SQLite allows only one writer at a time -- under load, OIDC token storage competes with governance writes, config updates, and request logging.

**Prevention:** Use WAL mode for SQLite (`PRAGMA journal_mode=WAL`) which allows concurrent readers with a single writer. If write contention becomes measurable, consider storing OIDC sessions in-memory with periodic persistence, or use the existing session table with a `session_type` discriminator.

**Phase:** Phase 3 (OIDC integration). Monitor write latency in dev before production.

---

## Phase-Specific Warnings

| Phase Topic | Likely Pitfall | Mitigation |
|-------------|---------------|------------|
| Phase 1: Fork setup | Import path rewriting (Pitfall 1) | Keep `maximhq/bifrost` module paths, use `go.work` for development |
| Phase 1: Fork setup | Upstream merge strategy (Pitfall 4) | Establish merge cadence, CI merge-check job |
| Phase 2: Named instances | sync.Map key race (Pitfall 2) | Run all tests with `-race`, test hot-reload scenarios |
| Phase 2: Named instances | Model name collision (Pitfall 6) | Require full `type:instance` key in governance configs |
| Phase 2: Named instances | Config reconciliation (Pitfall 9) | Evaluate `CustomProviderConfig` before changing core key scheme |
| Phase 2: Named instances | Streaming quirks (Pitfall 7) | Integration test each endpoint before prod |
| Phase 3: OIDC integration | Refresh token race (Pitfall 3) | Use `singleflight.Group` from day one |
| Phase 3: OIDC integration | JWKS key rotation (Pitfall 5) | Create provider with `context.Background()`, retry on unknown kid |
| Phase 3: OIDC integration | Context key collision (Pitfall 8) | New OIDC context keys, short-circuit existing auth for OIDC path |
| Phase 3: OIDC integration | SQLite contention (Pitfall 12) | Enable WAL mode, monitor write latency |
| Phase 4: Deployment | Multi-arch CGO (Pitfall 10) | Test Docker build for both architectures early |
| Phase 4: Deployment | CORS with OIDC (Pitfall 11) | Server-side code exchange, not client-side PKCE |

---

## Design Insight: CustomProviderConfig May Eliminate the Core Key Change

A critical finding from code analysis: Bifrost already has a `CustomProviderConfig` mechanism that lets you register a provider with an arbitrary name (e.g., `"llama-cpp"`) backed by a known provider type (`base_provider_type: "openai"`). The `createBaseProvider()` function already handles this: it reads `CustomProviderConfig.BaseProviderType` to determine which provider constructor to call, while using the arbitrary name as the key.

This means PROV-01 (multiple providers of the same type with different base URLs) might be achievable WITHOUT changing the sync.Map key scheme at all. Instead of `"openai:fireworks"`, you would register `"fireworks"` with `CustomProviderConfig { base_provider_type: "openai", base_url: "https://api.fireworks.ai" }`. This approach:

- Avoids Pitfall 2 (sync.Map key change) entirely
- Avoids Pitfall 9 (config reconciliation) entirely
- Keeps full backward compatibility
- Reduces the fork diff surface (Pitfall 4)

The tradeoff: the `type:instance` naming convention from PROJECT.md is more explicit. But `CustomProviderConfig` is already battle-tested in the codebase. Evaluate both approaches before committing.

## Sources

- [Bifrost core/bifrost.go](https://github.com/maximhq/bifrost) -- sync.Map usage, provider routing, ProviderQueue lifecycle (verified via codebase analysis)
- [Bifrost core/schemas/bifrost.go](https://github.com/maximhq/bifrost) -- ModelProvider type, CustomProviderConfig (verified via codebase analysis)
- [Bifrost transports/bifrost-http/handlers/middlewares.go](https://github.com/maximhq/bifrost) -- AuthMiddleware, CORS handling (verified via codebase analysis)
- [Bifrost transports/bifrost-http/handlers/oauth2.go](https://github.com/maximhq/bifrost) -- existing OAuth2 flow (verified via codebase analysis)
- [Working With Forks in Go](https://blog.sgmansfield.com/2016/06/working-with-forks-in-go/) -- import path rewriting challenges
- [Go Module Replace Directive](https://dev.to/stevenacoffman/handling-go-modules-replacement-directive-version-lag-and-fork-maintenance-3npj) -- fork maintenance with replace
- [Go Issue #74884](https://github.com/golang/go/issues/74884) -- proposal to make fork module management easier
- [VictoriaMetrics: Go sync.Map](https://victoriametrics.com/blog/go-sync-map/) -- sync.Map optimization cases and pitfalls
- [Go Issue #22490](https://github.com/golang/go/issues/22490) -- sync.Map Range + Store race conditions
- [oauth2-proxy Issue #1992](https://github.com/oauth2-proxy/oauth2-proxy/issues/1992) -- refresh token race condition
- [oauth2-proxy Issue #1006](https://github.com/oauth2-proxy/oauth2-proxy/issues/1006) -- parallel requests with refresh token rotation
- [coreos/go-oidc Issue #339](https://github.com/coreos/go-oidc/issues/339) -- context caching bug in RemoteKeySet
- [coreos/go-oidc Issue #214](https://github.com/coreos/go-oidc/issues/214) -- updateKeys uses wrong context
- [coreos/go-oidc Issue #372](https://github.com/coreos/go-oidc/issues/372) -- no retry on JWKS fetch errors
- [Keycloak 26.2.0 Release Notes](https://www.keycloak.org/2025/04/keycloak-2620-released) -- OIDC edge cases in recent Keycloak versions
- [LiteLLM Issue #9551](https://github.com/BerriAI/litellm/issues/9551) -- 504 timeout for non-streaming requests
- [Portkey: Failover Routing Strategies](https://portkey.ai/blog/failover-routing-strategies-for-llms-in-production/) -- LLM gateway routing pitfalls
