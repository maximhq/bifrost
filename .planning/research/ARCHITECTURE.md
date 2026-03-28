# Architecture Patterns

**Domain:** Go LLM Gateway (Bifrost fork)
**Researched:** 2026-03-26
**Confidence:** HIGH (source code analysis of entire request path)

## Recommended Architecture

### Overview

Bifrost is a high-performance Go LLM gateway built on `fasthttp` with a channel-per-provider concurrency model. Requests flow through middleware chains to HTTP handlers, then into the core `Bifrost` struct which manages provider worker pools via `sync.Map` keyed by `ModelProvider` (a `string` type). The two modifications we need (named provider instances, OIDC auth) touch orthogonal parts of the codebase, which is fortunate for phasing.

### Component Map

```
HTTP Layer (fasthttp)
  |
  v
Middleware Chain (SecurityHeaders -> CORS -> RequestDecompression -> Router)
  |
  v
Route-Level Middlewares (TracingMW -> TransportInterceptorMW -> PrometheusHTTPMW -> AuthMW)
  |
  v
Handler (CompletionHandler.chatCompletion, etc.)
  |
  v
Core Bifrost (handleRequest -> tryRequest -> ProviderQueue -> requestWorker)
  |
  v
Provider (openai.Provider, anthropic.Provider, etc. via schemas.Provider interface)
  |
  v
Upstream API (OpenAI, Fireworks, llama-cpp, etc.)
```

### Component Boundaries

| Component | Responsibility | Key Files | Communicates With |
|-----------|---------------|-----------|-------------------|
| **HTTP Server** | Server lifecycle, middleware assembly, route registration | `server/server.go` (Bootstrap, Start, RegisterInferenceRoutes, RegisterAPIRoutes) | Router, Config, Bifrost core |
| **Middleware Chain** | Auth, CORS, decompression, tracing, transport interceptors | `handlers/middlewares.go` | All handlers |
| **Inference Handlers** | Parse HTTP body, extract provider/model, call Bifrost core | `handlers/inference.go` (chatCompletion, responses, etc.) | Bifrost core, Config |
| **Config (lib.Config)** | In-memory provider registry, auth config, plugin storage, SQLite persistence | `lib/config.go` (LoadConfig, GetProviderConfigRaw, GetAllProviders, AddProvider) | ConfigStore (SQLite/GORM), Account interface |
| **Account (lib.BaseAccount)** | Adapter from Config to core's Account interface | `lib/account.go` (GetConfiguredProviders, GetKeysForProvider, GetConfigForProvider) | Config |
| **Bifrost Core** | Provider lifecycle, request queuing, fallback routing, plugin hooks | `core/bifrost.go` (Init, handleRequest, tryRequest, prepareProvider, createBaseProvider, getProviderQueue) | Account, Provider instances, sync.Map storage |
| **Provider Interface** | Upstream API communication per provider type | `core/schemas/provider.go` (Provider interface), `core/providers/{name}/` | Upstream HTTP APIs |
| **Schema Types** | ModelProvider enum, ParseModelString, known provider registry | `core/schemas/bifrost.go`, `core/schemas/utils.go` | Everything |
| **Auth Middleware** | Session/Basic/Bearer validation for API and inference routes | `handlers/middlewares.go` (AuthMiddleware) | ConfigStore (session table) |
| **OAuth2 Framework** | MCP OAuth2 flows (token exchange, refresh) -- NOT SSO/OIDC | `framework/oauth2/`, `handlers/oauth2.go` | ConfigStore (oauth_configs, oauth_tokens tables) |
| **Session Handler** | Username/password login, session creation | `handlers/session.go` | ConfigStore (sessions table) |

## Data Flow: Request Lifecycle

### Full Request Trace: HTTP -> Provider -> Response

```
1. HTTP POST /v1/chat/completions
   Body: { "model": "openai/gpt-4o", "messages": [...] }

2. fasthttp.Server.Handler chain:
   SecurityHeadersMiddleware -> CorsMiddleware -> RequestDecompressionMiddleware -> Router.Handler

3. Router dispatches to:
   TracingMiddleware -> TransportInterceptorMiddleware -> PrometheusHTTPMiddleware
   -> AuthMiddleware.InferenceMiddleware -> RegisterRequestTypeMiddleware
   -> CompletionHandler.chatCompletion

4. chatCompletion handler:
   a. prepareChatCompletionRequest() -- JSON unmarshal
   b. schemas.ParseModelString("openai/gpt-4o", "") -> ("openai", "gpt-4o")
      - Splits on first "/" only if prefix is in knownProviders map
   c. Creates BifrostChatRequest{Provider: "openai", Model: "gpt-4o", ...}
   d. lib.ConvertToBifrostContext(ctx) -- extracts headers, auth, direct keys
   e. Calls client.ChatCompletionRequest(bifrostCtx, bifrostChatReq)

5. Bifrost.ChatCompletionRequest -> makeChatCompletionRequest -> handleRequest:
   a. req.GetRequestFields() -> (provider="openai", model="gpt-4o", fallbacks=[])
   b. validateRequest(req)

6. Bifrost.tryRequest:
   a. getProviderQueue("openai") -> looks up sync.Map[ModelProvider]*ProviderQueue
      - If not found: calls account.GetConfigForProvider("openai") -> prepareProvider()
   b. Plugin pipeline: RunLLMPreHooks -> (may short-circuit with cached response)
   c. Creates ChannelMessage, sends to pq.queue channel
   d. Waits on response/error channels

7. requestWorker (goroutine consuming from queue):
   a. Receives ChannelMessage from queue
   b. account.GetKeysForProvider(ctx, "openai") -> []Key (filtered by governance)
   c. keySelector selects key (weighted random by default)
   d. Dispatches to provider.ChatCompletion(ctx, key, request)
   e. Provider builds HTTP request to upstream (base_url + /v1/chat/completions)
   f. Sends response back on ChannelMessage.Response channel

8. Response flows back through plugin PostHooks, handler sends JSON to HTTP client
```

### Critical Observation: The Single-Provider Bottleneck

**The `Providers` map is `map[schemas.ModelProvider]configstore.ProviderConfig`** where `ModelProvider` is a `string` type. In the current config:

```json
{
  "providers": {
    "openai": {
      "keys": [{"name": "fireworks-ai", "value": "env.FIREWORKS_AI_API_KEY"}],
      "network_config": {"base_url": "https://api.fireworks.ai/inference"}
    }
  }
}
```

There is ONE "openai" entry. The `base_url` is set at the provider level, not per-key. This means:
- All `openai/*` model requests go to `https://api.fireworks.ai/inference`
- There is no way to simultaneously route `openai/gpt-4o` to `api.openai.com` and `openai/llama-3` to `http://llama-cpp.llm-system:8080`
- This is the core limitation PROV-01 through PROV-05 address

## Provider Registry Deep Dive

### Current Key Structure (5 interconnected maps)

```
1. Config.Providers: map[ModelProvider]configstore.ProviderConfig
   - In-memory, guarded by Config.Mu (RWMutex)
   - Key: "openai", "anthropic", "groq", etc.
   - Source of truth for config.json-loaded and API-added providers

2. Bifrost.requestQueues: sync.Map[ModelProvider]*ProviderQueue
   - Concurrent-safe, lock-free reads
   - Key: same ModelProvider string
   - Each ProviderQueue has a buffered channel + lifecycle (done/closing)

3. Bifrost.providers: atomic.Pointer[[]schemas.Provider]
   - Append-only slice of initialized provider instances
   - Matched by GetProviderKey() == providerKey

4. Bifrost.waitGroups: sync.Map[ModelProvider]*sync.WaitGroup
   - Tracks worker goroutines per provider

5. Bifrost.providerMutexes: sync.Map[ModelProvider]*sync.RWMutex
   - Per-provider mutex for safe init/update

6. schemas.knownProviders: map[string]bool (module-level, mutex-protected)
   - Used by ParseModelString to detect provider prefixes in "provider/model" strings
```

### What Changes with `type:instance` Keys

**Proposed key format:** `openai:llama-cpp`, `openai:fireworks`, `openai:together`

Backward compatibility: A key without `:` (e.g., `"openai"`) is the default instance.

**What would break if we change the key:**

1. **`ParseModelString` (CRITICAL)** -- Currently splits `"openai/gpt-4o"` on `/` and checks `IsKnownProvider("openai")`. With named instances, requests must specify instance: `"openai:fireworks/gpt-4o"`. ParseModelString needs to handle `type:instance/model` -> `(ModelProvider("openai:fireworks"), "gpt-4o")`. This is the most delicate change because it affects ALL request parsing.

2. **`knownProviders` registry** -- Must include `"openai:fireworks"` as a known provider so ParseModelString recognizes it. `RegisterKnownProvider` already supports arbitrary strings.

3. **`createBaseProvider` switch statement** -- Currently switches on `providerKey` directly. With `"openai:fireworks"`, needs to extract base type `"openai"` and use that for the switch. **CustomProviderConfig already does this** -- it has `BaseProviderType` that overrides the switch target. This is the escape hatch.

4. **`Config.Providers` map** -- Key changes from `"openai"` to `"openai:fireworks"`. No structural change, just different key strings.

5. **All 5 sync.Map/map lookups** -- All use ModelProvider as key. The type is `string`, so `"openai:fireworks"` works transparently. No structural change.

6. **API endpoints (`/api/providers`)** -- CRUD operations use provider name as path param. Must accept `:` in names or use query params.

7. **UI** -- Provider display names need to handle `type:instance` formatting. The base icon should come from the base type, not the full key.

8. **Config.json format** -- Currently `"providers": {"openai": {...}}`. New format: `"providers": {"openai:fireworks": {...}}`.

### Upstream CustomProviderConfig: The Existing Escape Hatch

Bifrost ALREADY has a `CustomProviderConfig` mechanism:

```go
type CustomProviderConfig struct {
    CustomProviderKey    string        `json:"-"`
    IsKeyLess            bool          `json:"is_key_less"`
    BaseProviderType     ModelProvider `json:"base_provider_type"`
    AllowedRequests      *AllowedRequests `json:"allowed_requests,omitempty"`
    RequestPathOverrides map[RequestType]string `json:"request_path_overrides,omitempty"`
}
```

In `createBaseProvider`, when `CustomProviderConfig` is set:
```go
if config.CustomProviderConfig != nil {
    targetProviderKey = config.CustomProviderConfig.BaseProviderType
}
switch targetProviderKey { ... } // Uses base type for provider creation
```

**This means we can define named instances TODAY using custom providers:**

```json
{
  "providers": {
    "llama-cpp": {
      "custom_provider_config": {
        "base_provider_type": "openai",
        "is_key_less": true
      },
      "network_config": {
        "base_url": "http://llama-cpp.llm-system.svc.cluster.local:8080"
      }
    },
    "fireworks": {
      "custom_provider_config": {
        "base_provider_type": "openai"
      },
      "keys": [{"name": "fw-key", "value": "env.FIREWORKS_AI_API_KEY"}],
      "network_config": {
        "base_url": "https://api.fireworks.ai/inference"
      }
    }
  }
}
```

Requests would be: `"model": "llama-cpp/llama-3"` and `"model": "fireworks/gpt-4o"`.

**Impact:** This works WITHOUT forking core code. It uses the existing `CustomProviderConfig.BaseProviderType` path. The `RegisterKnownProvider` call in `prepareProvider` automatically adds `"llama-cpp"` and `"fireworks"` to the known providers map. ParseModelString then correctly splits `"llama-cpp/llama-3"` into `(ModelProvider("llama-cpp"), "llama-3")`.

**Validation needed:** Confirm `SupportedBaseProviders` includes `OpenAI` (it does -- verified in source). Confirm keyless mode works with the OpenAI provider (the `is_key_less` flag is checked, and OpenAI provider handles it).

### Decision: CustomProviderConfig vs. Core Fork for Named Instances

| Approach | Pros | Cons |
|----------|------|------|
| **CustomProviderConfig (no fork)** | Zero core changes, works today, backward-compatible, stays on upstream | Provider names like "llama-cpp" instead of "openai:llama-cpp", no visual connection to base type in UI |
| **Core fork with type:instance** | Cleaner semantic model, UI can show "openai (llama-cpp)", explicit base type in name | Touches ParseModelString, knownProviders, createBaseProvider, UI, API handlers -- 15+ files, merge conflicts with upstream |

**Recommendation:** Use CustomProviderConfig first. It achieves the functional requirement (multiple OpenAI-compatible endpoints) without a fork. If the UI naming is unacceptable, a thin wrapper in the fork can add display-name aliasing without touching core routing.

## Auth Architecture Deep Dive

### Current Auth Flow

```
AuthMiddleware.middleware():
  1. Load authConfig (atomic.Pointer)
  2. If !authConfig.IsEnabled -> pass through (auth disabled)
  3. If shouldSkip(authConfig, url) -> pass through (whitelisted route)
  4. Check Authorization header:
     - "Basic base64(user:pass)" -> validate against authConfig.AdminUserName/AdminPassword
     - "Bearer token" -> validateSession(store, token) -> lookup session in SQLite
     - No header -> check Cookie("token") -> validateSession
     - WebSocket -> check ticket/token/cookie
  5. All fail -> 401 Unauthorized
```

**The existing auth is admin-only:** One username/password pair in `authConfig`. Sessions are created by `POST /api/session/login` with that one credential. There is no user management, no RBAC, no SSO.

**The OAuth2 framework (`framework/oauth2/`, `handlers/oauth2.go`) is for MCP tool OAuth**, not user SSO. It handles OAuth flows for connecting to external services (e.g., GitHub API for MCP tools), storing tokens in `oauth_configs`/`oauth_tokens` tables.

### Where OIDC Would Slot In

The auth middleware has two entry points:
- `AuthMiddleware.InferenceMiddleware()` -- for `/v1/*` inference routes
- `AuthMiddleware.APIMiddleware()` -- for `/api/*` management routes

OIDC needs to add a fourth auth scheme alongside Basic/Bearer/Cookie:

```
New flow in middleware():
  4a. Check Authorization header:
      - "Bearer <JWT>" -> if JWT signature valid (OIDC) -> extract claims -> create/find Bifrost user
      - "Bearer <session-token>" -> existing session validation
      - "Basic base64(user:pass)" -> existing admin validation
```

**Key files to modify for OIDC:**

| File | Change |
|------|--------|
| `framework/configstore/clientconfig.go` | Add `OIDCConfig` struct (issuer, client_id, audience, jwks_uri, claim mappings) |
| `transports/bifrost-http/lib/config.go` | Parse OIDC config from `config.json`, add to `GovernanceConfig` or new top-level field |
| `transports/bifrost-http/handlers/middlewares.go` | Add OIDC JWT validation in `AuthMiddleware.middleware()` -- decode JWT, verify signature via JWKS, extract claims |
| `transports/bifrost-http/handlers/session.go` | (Optional) Add OIDC-initiated session creation endpoint |
| `core/schemas/` | Add OIDC-related types if needed |

**OIDC does NOT need:**
- Changes to the OAuth2 framework (that is for MCP tool auth, different concern)
- Changes to the Provider interface or routing layer
- Changes to the governance plugin (virtual keys, customers, teams remain as-is)

### OIDC Implementation Strategy

Reference implementation: `oauth2-proxy` (Go, MIT, battle-tested with Keycloak). Key patterns to borrow:

1. **JWKS caching:** Fetch `/.well-known/openid-configuration` on startup, cache JWKS keys with TTL refresh
2. **JWT validation:** Signature verification, audience check, issuer check, expiry check
3. **Claim mapping:** `sub` -> Bifrost user ID, `email` -> display, `groups` -> team/role mapping
4. **Token introspection fallback:** If JWT validation fails, try Keycloak token introspection endpoint

## Suggested Build Order

### Phase 1: Named Instances via CustomProviderConfig (zero core changes)

**Dependencies:** None
**Files to modify:**

| File | Change |
|------|--------|
| `infra-ctrl` overlay `configmap.yaml` | Restructure config.json with custom providers for llama-cpp, fireworks, etc. |
| (No Bifrost source changes needed) | |

**Validation:** Deploy with new config, confirm `llama-cpp/model-name` and `fireworks/model-name` route correctly.

### Phase 2: OIDC Auth Middleware

**Dependencies:** Phase 1 (need working named instances to test E2E)
**Files to modify:**

| File | Change | Complexity |
|------|--------|------------|
| `framework/configstore/clientconfig.go` | Add `OIDCConfig` struct | Low |
| `transports/bifrost-http/lib/config.go` | Parse OIDC config in `loadAuthConfig` or new `loadOIDCConfig` | Medium |
| `transports/bifrost-http/handlers/middlewares.go` | Add OIDC JWT validation in `AuthMiddleware.middleware()` | High (security-critical) |
| `transports/bifrost-http/handlers/session.go` | Add OIDC-based session endpoint (optional, for UI login) | Medium |
| New: `framework/oidc/` package | JWKS fetching, JWT validation, claim extraction | High |
| Config.json | Add OIDC config section | Low |
| infra-ctrl overlay | Add Keycloak OIDC config | Low |

**Key dependency:** JWKS library. Use `github.com/MicahParks/keyfunc/v3` (Go, MIT, well-maintained, auto-refresh JWKS). For JWT parsing, use `github.com/golang-jwt/jwt/v5` (standard Go JWT library, MIT).

### Phase 3: Docker Image Build Pipeline

**Dependencies:** Phase 2 (need source changes to build)
**Files to create/modify:**

| File | Change |
|------|--------|
| `Dockerfile` | Multi-stage build (Go build + distroless) for amd64/arm64 |
| `.github/workflows/build.yml` | CI/CD pipeline for Harbor/GHCR push |
| infra-ctrl overlay | Update image reference to our image |

### Phase 4 (optional): UI Enhancements for Named Instances

**Dependencies:** Phase 1
**Files to modify:** Various files in `ui/` directory -- provider display, model routing display. This is cosmetic and can be deferred.

## Anti-Patterns to Avoid

### Anti-Pattern 1: Modifying ParseModelString for type:instance
**What:** Adding `:` parsing to `ParseModelString` to support `openai:fireworks/gpt-4o` format.
**Why bad:** Touches the most critical parsing function, affects every request, creates merge conflicts with upstream, and CustomProviderConfig already solves the problem.
**Instead:** Use CustomProviderConfig with descriptive provider names (e.g., `"fireworks"`, `"llama-cpp"`).

### Anti-Pattern 2: Forking the OAuth2 framework for OIDC
**What:** Extending `framework/oauth2/` to handle OIDC flows.
**Why bad:** The OAuth2 framework is for MCP tool OAuth (authorization code flows to external services). OIDC user auth is a different concern (JWT validation, no authorization code flow needed for API auth).
**Instead:** Create a separate `framework/oidc/` package focused on JWT validation and JWKS management.

### Anti-Pattern 3: Modifying core/bifrost.go for provider naming
**What:** Changing the provider registry key format in `Bifrost.requestQueues`, `Bifrost.providers`, etc.
**Why bad:** These data structures are correct as-is. The key is `ModelProvider` (string), which already accepts any string. CustomProviderConfig handles the mapping to base provider type.
**Instead:** Only change config.json and deployment manifests.

### Anti-Pattern 4: Building OIDC session management from scratch
**What:** Creating a full session store for OIDC-authenticated users.
**Why bad:** JWT tokens are self-contained; you don't need server-side sessions for API auth. Session storage adds complexity and state management.
**Instead:** Validate JWT on every request (stateless). Use Bifrost's existing session mechanism only if UI login requires it (for the dashboard).

## Scalability Considerations

| Concern | At 100 RPS | At 5K RPS | At 50K RPS |
|---------|-----------|-----------|------------|
| **Provider queues** | Default 5000 buffer, 1000 workers -- overkill | Appropriate sizing | May need tuning per-provider |
| **JWKS caching** | Negligible | Single cached JWKS set, no concern | Ensure JWKS refresh doesn't block hot path |
| **sync.Map contention** | None | None (read-mostly workload) | Provider count is small (<20), no concern |
| **Plugin pipeline** | Pool pre-warmed, ~0 alloc | Pool handles it | May need larger initial pool |

## Sources

- All findings from direct source code analysis of `/Users/shawnwalker/code/stragix/bifrost/` codebase
- `core/bifrost.go` -- provider lifecycle, request routing, queue management (4400+ lines)
- `core/schemas/provider.go` -- Provider interface, ProviderConfig, CustomProviderConfig, NetworkConfig
- `core/schemas/bifrost.go` -- ModelProvider type, constants, BifrostRequest.GetRequestFields
- `core/schemas/utils.go` -- ParseModelString, knownProviders registry
- `core/schemas/account.go` -- Account interface
- `core/schemas/oauth.go` -- OAuth2Provider interface (MCP OAuth, not SSO)
- `transports/bifrost-http/handlers/inference.go` -- HTTP handlers, route registration
- `transports/bifrost-http/handlers/middlewares.go` -- AuthMiddleware, CORS, tracing, transport interceptor
- `transports/bifrost-http/handlers/session.go` -- Login/session management
- `transports/bifrost-http/handlers/oauth2.go` -- MCP OAuth callback handling
- `transports/bifrost-http/lib/config.go` -- Config loading, provider reconciliation, store initialization
- `transports/bifrost-http/lib/account.go` -- BaseAccount (Account interface adapter)
- `transports/bifrost-http/server/server.go` -- Server bootstrap, middleware chain assembly
- `framework/configstore/clientconfig.go` -- ProviderConfig (configstore version)
- `infra-ctrl-bifrost-eval/apps/platform-services/bifrost/overlays/dev/configmap.yaml` -- Current deployment config
