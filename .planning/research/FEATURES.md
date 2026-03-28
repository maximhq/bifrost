# Feature Landscape

**Domain:** LLM Gateway fork (Bifrost) -- named provider instances + Keycloak OIDC
**Researched:** 2026-03-26
**Confidence:** HIGH (based on Bifrost source code analysis + competitive landscape research)

## Table Stakes

Features that are mandatory for the fork to be functional in our use case. Without these, the fork has no reason to exist -- we would stay on LiteLLM or find an alternative.

### Named Provider Instances

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| `type:instance` provider key format (e.g., `openai:llama-cpp`, `openai:fireworks`) | Core blocker. Bifrost's `sync.Map` is keyed by `ModelProvider` (a string like `"openai"`), so you can only have one `openai` provider. Every competing gateway (LiteLLM, TensorZero, Portkey) supports this natively. | Medium | Touches `Bifrost.providers`, `requestQueues`, `waitGroups`, `providerMutexes` -- all sync.Map keyed by `ModelProvider`. Backward compat: no `:` = default instance. |
| Config.json `providers` map accepts `type:instance` keys | Config parsing must resolve `"openai:llama-cpp"` into the correct provider type (`openai`) with its own independent `base_url`, keys, and network config. | Medium | `ConfigData.Providers` is `map[string]configstore.ProviderConfig`. The key is cast to `schemas.ModelProvider`. The `CustomProviderConfig.BaseProviderType` pattern already exists -- named instances is the simpler version (same type, different endpoint). |
| Independent `base_url` per instance | Each named instance must have its own `NetworkConfig.BaseURL`. This is the entire point -- llama-cpp at `http://llama-cpp.llm.svc:8080/v1`, Fireworks at `https://api.fireworks.ai/inference/v1`, Together at `https://api.together.xyz/v1`. | Low | `NetworkConfig.BaseURL` already exists per provider. Just needs to be per-instance instead of per-type. |
| Independent API keys per instance | Each named instance needs its own `Keys []schemas.Key` array. Self-hosted (llama-cpp) is keyless; cloud providers (Fireworks, Together) have API keys. | Low | Already per-provider. Becomes per-instance naturally. |
| Model-to-instance routing | When a request comes in for model `meta-llama/Llama-3.3-70B-Instruct`, the gateway must resolve which named instance(s) serve that model and route accordingly. | Medium-High | Today Bifrost routes by `ModelProvider` type. Virtual key `provider_configs[].provider` would reference `"openai:llama-cpp"` instead of `"openai"`. The model catalog and key-model mapping already handle model filtering. |
| API CRUD for named instances | `/api/providers` endpoints must handle named instances -- create, read, update, delete. UI needs to display them correctly. | Medium | Existing provider CRUD in `config.go` (`AddNewProvider`, `UpdateProvider`, `RemoveProvider`) all key by `ModelProvider`. Widen to accept `type:instance` format. |

### OIDC / Keycloak SSO Integration

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| OIDC Discovery (`.well-known/openid-configuration`) | Table stakes for any OIDC integration. Auto-discovers authorization endpoint, token endpoint, JWKS URI, userinfo endpoint from a single issuer URL. Every OIDC client library does this. | Low | Go `github.com/coreos/go-oidc/v3` handles this. oauth2-proxy's keycloak_oidc provider is the reference implementation. |
| JWT token validation at the gateway | Incoming Bearer tokens (from Keycloak) must be validated: signature check against JWKS, audience check, expiry check. Invalid tokens get 401. | Medium | Replaces or augments the existing `AuthMiddleware.middleware()` which currently only does session-token + basic-auth. Need a new code path: if Bearer token is a JWT (not a session UUID), validate via OIDC. |
| ID token claims extraction (sub, email, groups, org) | After JWT validation, extract standard OIDC claims to identify the user and their organization. `sub` = user ID, `email` = user email, `groups` or custom claim = Keycloak groups/roles. | Low | Standard JWT parsing. `go-oidc` extracts claims into a struct. |
| Claims-to-Bifrost entity mapping | Map OIDC claims to Bifrost's governance hierarchy: `organization_id` claim -> Customer, group claim -> Team, `sub` -> User. This is what makes the gateway multi-tenant. | Medium-High | This is the integration glue. Bifrost has Customer/Team/User/VirtualKey hierarchy. OIDC users need to be auto-resolved to the correct Customer (org) and Team. Can be config-driven: claim names are configurable. |
| Config.json OIDC provider configuration | `config.json` must accept OIDC configuration: issuer URL, client ID, client secret, scopes, claim mappings. | Low | New section in `ConfigData`, similar to how `AuthConfig` already works. |
| Token refresh (server-side) | For dashboard SSO sessions, the gateway must handle token refresh transparently. Access tokens are short-lived (5-15 min); refresh tokens extend the session. | Medium | The existing session system uses 30-day opaque tokens. OIDC sessions need refresh token rotation. Store refresh tokens server-side, refresh before access token expiry. |

## Differentiators

Features that give our fork competitive advantage or align with Stragix platform needs. Not blockers, but high-value additions.

| Feature | Value Proposition | Complexity | Notes |
|---------|-------------------|------------|-------|
| Automatic Customer/VirtualKey provisioning from OIDC | When a new org authenticates via Keycloak, auto-create a Bifrost Customer + VirtualKey with default budget/rate limits. Zero manual gateway setup per tenant. | Medium | LiteLLM and Portkey require manual virtual key creation. Auto-provisioning from OIDC claims (especially `organization_id`) is a differentiator for multi-tenant SaaS. |
| Group-to-allowed-models mapping | Keycloak group membership determines which models a user can access. E.g., "premium" group gets GPT-4o; "basic" group gets only llama-cpp. | Medium | Maps to VirtualKeyProviderConfig's `allowed_models` field. Group claim from JWT drives which provider_configs are active for that user's virtual key. |
| Provider health-aware routing across instances | When llama-cpp is down, automatically failover requests to Fireworks for the same model family. Per-instance health checks. | Low | Bifrost already has provider failover + load balancing. Named instances just need to participate in the same failover pool. The `VirtualKeyProviderConfig.weight` field enables weighted distribution. |
| Per-instance cost tracking with custom pricing | Self-hosted models (llama-cpp) have $0 inference cost. Cloud models have per-token pricing. Track costs accurately per named instance. | Low | `ProviderPricingOverride` already exists. Set `input_cost_per_token: 0` for self-hosted instances. The governance plugin's budget tracking will work correctly per-instance. |
| PKCE support for public clients | If the Bifrost UI is ever exposed as a public client (SPA), PKCE is required for secure OIDC flows. | Low | `golang.org/x/oauth2` supports PKCE. oauth2-proxy does it automatically. |
| Session stickiness across named instances | For multi-turn conversations, route subsequent requests to the same provider instance that handled the first request. | Low | Bifrost already has `kvStore` for session stickiness. Extend to be instance-aware. |

## Anti-Features

Features to explicitly NOT build. These either duplicate platform-layer functionality, add unnecessary complexity, or conflict with the fork's upstream-tracking strategy.

| Anti-Feature | Why Avoid | What to Do Instead |
|--------------|-----------|-------------------|
| Vault-native key management in Bifrost | We use ExternalSecrets + Vault at the K8s layer. Bifrost managing Vault directly would duplicate secret injection and create two sources of truth. | Continue using K8s ExternalSecrets to inject provider API keys as environment variables. Bifrost reads them via `schemas.EnvVar`. |
| Custom OIDC provider implementation from scratch | oauth2-proxy's Go OIDC implementation is battle-tested, MIT-licensed, and has a Keycloak-specific provider with group claim mapping. Rolling our own would be slower and buggier. | Reference oauth2-proxy's `keycloak_oidc` provider implementation. Use `coreos/go-oidc/v3` + `golang.org/x/oauth2` libraries directly. |
| SAML/SCIM enterprise SSO in the fork | Bifrost Enterprise already has SAML/SCIM behind a commercial license. Adding it to our fork creates license conflict and maintenance burden. | Stick to OIDC only. Keycloak can bridge SAML IdPs if customers need them. |
| Semantic caching | Complex feature (vector similarity on prompts), immature in the ecosystem, and our RAG product has its own caching layer. Adding it to the gateway doubles caching complexity. | Defer. Evaluate later if P95 latency warrants it. |
| MCP Gateway support | Bifrost has MCP integration, but our RAG product doesn't need it yet. MCP is still evolving rapidly and adds significant surface area. | Leave upstream MCP code untouched. Don't extend it, don't remove it. |
| Clustering/HA in the fork | Bifrost Enterprise has clustering. For our use case, a single replica with K8s restart-on-failure is sufficient. Adding clustering is high complexity for minimal gain at our scale. | Single replica. K8s handles restarts. Scale vertically first. |
| Multi-IdP support (Google, GitHub) | Bifrost OSS already has Google/GitHub SSO. We only need Keycloak. Adding generic multi-IdP complicates the auth middleware. | Keep existing Google/GitHub SSO untouched. Add Keycloak OIDC as a parallel option. Don't build a generic IdP framework. |
| User management UI in Bifrost | Our platform already has user management in auth-proxy/Keycloak. Bifrost should not become another user management surface. | Users are managed in Keycloak. Bifrost consumes JWT claims. The Bifrost UI shows customers/teams/virtual-keys (governance), not users. |

## Feature Dependencies

```
Named Provider Instances (PROV-01)
  |
  +-> Config.json type:instance parsing (PROV-03) -- must come first
  |     |
  |     +-> Provider CRUD API updates (PROV-04) -- needs parsing
  |     |
  |     +-> UI display updates (PROV-05) -- needs API
  |
  +-> Model-to-instance routing (PROV-02) -- needs registry working
  |
  +-> Per-instance cost tracking -- needs instances registered

OIDC / Keycloak SSO (AUTH-01)
  |
  +-> JWT validation middleware -- foundational
  |     |
  |     +-> Claims extraction (AUTH-02) -- needs validation working
  |     |     |
  |     |     +-> Claims-to-Customer/Team mapping -- needs claims
  |     |     |
  |     |     +-> Group-to-allowed-models mapping -- needs claims + instances
  |     |
  |     +-> Token refresh -- needs validation working
  |
  +-> Config.json OIDC section (AUTH-03) -- parallel to validation
  |
  +-> Auto-provisioning from OIDC -- needs claims mapping + governance working

Cross-cutting:
  PROV-01 + AUTH-02 together enable the full product flow:
    "OIDC user from org X authenticates -> resolves to Customer X ->
     Customer X's virtual key routes to openai:llama-cpp and openai:fireworks
     with per-instance budgets and rate limits"
```

## MVP Recommendation

### Phase 1: Named Provider Instances (PROV-01 through PROV-05)

Prioritize:
1. **Config parsing for `type:instance` keys** (PROV-03) -- unlocks everything else
2. **Provider registry widening** (PROV-01) -- sync.Map key changes, provider creation
3. **Model-to-instance routing** (PROV-02) -- virtual key provider_configs reference instances
4. **Provider API updates** (PROV-04) -- CRUD endpoints accept named instances
5. **UI display** (PROV-05) -- show instance names in provider list

Rationale: This is the hard fork-level change that touches core Bifrost internals. Get it right first, in isolation, before adding auth complexity. It's also independently testable -- deploy and verify with API keys before adding SSO.

### Phase 2: Keycloak OIDC (AUTH-01 through AUTH-03)

Prioritize:
1. **OIDC config section** (AUTH-03) -- define the config format
2. **JWT validation middleware** (AUTH-01) -- core auth flow
3. **Claims-to-entity mapping** (AUTH-02) -- the multi-tenant glue

Defer: Auto-provisioning, group-to-model mapping, PKCE. These are differentiators that can come after the basics work.

Rationale: Auth changes are more contained (middleware layer only, no core provider registry changes). They build on top of named instances being already working.

## Competitive Context

### How competitors handle named provider instances

| Gateway | Mechanism | Config Format |
|---------|-----------|---------------|
| **LiteLLM** | `model_list` array with arbitrary `model_name` + `litellm_params.model` prefix (e.g., `openai/my-model`) + per-entry `api_base` | YAML array, each entry is a deployment. Same `model_name` = load-balanced pool. |
| **TensorZero** | `[models.X.providers.unique-name]` with `type` field. Provider name is arbitrary; type determines implementation. | TOML. `routing = ["name-a", "name-b"]` for weighted routing. |
| **Portkey** | Virtual keys map to providers. Config `targets[]` array with `virtual_key` + `override_params`. Migrating to Model Catalog (`@provider-slug/model-name`). | JSON config with strategy + targets. |
| **Bifrost (current)** | `providers` map keyed by `ModelProvider` string (e.g., `"openai"`). One entry per type. `CustomProviderConfig` can remap type but key is still unique. | JSON. `"providers": { "openai": {...} }` |
| **Bifrost (our fork)** | `providers` map keyed by `type:instance` (e.g., `"openai:llama-cpp"`). Parsed to extract base type for provider creation, instance name for registry. | JSON. `"providers": { "openai:llama-cpp": {...}, "openai:fireworks": {...} }` |

Our approach is closest to TensorZero's pattern (arbitrary names with a type field), but simpler: the type is embedded in the key (`type:instance`) rather than a separate field. This keeps backward compatibility (no `:` = default instance) and minimizes config format changes.

### How competitors handle auth/SSO

| Gateway | Auth Model |
|---------|-----------|
| **LiteLLM** | Virtual keys via proxy. SSO via enterprise tier (SAML/OIDC). Team management built-in. |
| **TensorZero** | API key auth. No built-in SSO. Expects external auth (e.g., API gateway). |
| **Portkey** | Portkey API key + virtual keys. SSO via enterprise. |
| **Bifrost (current)** | Admin username/password + session tokens. Google/GitHub SSO in OSS. SAML/SCIM in Enterprise. |
| **Bifrost (our fork)** | Add generic OIDC (Keycloak). Coexists with existing admin auth. OIDC tokens validated at middleware, claims mapped to governance entities. |

## Sources

- Bifrost source code: `core/bifrost.go`, `core/schemas/provider.go`, `framework/configstore/clientconfig.go`, `transports/bifrost-http/lib/config.go`, `transports/bifrost-http/handlers/middlewares.go`, `transports/bifrost-http/handlers/session.go`, `transports/bifrost-http/handlers/oauth2.go`, `ui/lib/types/governance.ts`
- [LiteLLM Proxy Config](https://docs.litellm.ai/docs/proxy/configs) -- model_list multi-deployment pattern
- [LiteLLM OpenAI-Compatible Endpoints](https://docs.litellm.ai/docs/providers/openai_compatible) -- api_base per deployment
- [TensorZero Configuration Reference](https://www.tensorzero.com/docs/gateway/configuration-reference) -- providers.unique-name with type field
- [TensorZero OpenAI-Compatible Guide](https://www.tensorzero.com/docs/gateway/guides/providers/openai-compatible) -- multiple instances same type
- [Portkey Virtual Keys](https://portkey.ai/docs/product/ai-gateway/virtual-keys) -- virtual key to provider mapping
- [Portkey AI Gateway Configs](https://portkey.ai/docs/product/ai-gateway/configs) -- strategy + targets routing
- [OAuth2-Proxy Keycloak OIDC Provider](https://oauth2-proxy.github.io/oauth2-proxy/configuration/providers/keycloak_oidc/) -- group claims mapping reference
- [Portkey Gateway Issue #1190](https://github.com/Portkey-AI/gateway/issues/1190) -- virtual key routing for self-hosted models
