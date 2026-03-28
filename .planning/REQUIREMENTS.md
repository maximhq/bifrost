# Requirements — Stragix Bifrost

## v1 Requirements

### Named Provider Instances (Config-Only)

Research discovered that Bifrost's `CustomProviderConfig` already supports registering arbitrary provider names backed by known provider types. This eliminates the need for core code changes.

- [ ] **PROV-01**: Configure multiple OpenAI-compatible backends via CustomProviderConfig in config.json (e.g., `llama-cpp` backed by `openai` with llama-cpp base_url, `fireworks` backed by `openai` with Fireworks base_url)
- [ ] **PROV-02**: Validate model routing works correctly — requests to `llama-cpp/str-qwen3-coder-30b-bf16` route to the correct backend
- [ ] **PROV-03**: Validate `is_key_less: true` works for self-hosted models (llama-cpp, ollama) that don't need API keys
- [ ] **PROV-04**: Update infra-ctrl dev and prod overlays to use CustomProviderConfig format
- [ ] **PROV-05**: Each self-hosted model service (llama-cpp-service, llama-cpp-vision-service, llama-cpp-coder-quant-service, llama-cpp-gptoss-service) gets its own named provider instance

### Fork Setup

- [x] **FORK-01**: Establish fork with `go.work` strategy — keep `maximhq/bifrost` import paths to minimize upstream merge conflicts
- [x] **FORK-02**: Bump `golang-jwt/jwt/v5` from v5.3.0 to v5.3.1 (CVE-2025-30204 fix)
- [x] **FORK-03**: CI pipeline for building multi-arch Docker image (amd64 + arm64) and pushing to GHCR
- [x] **FORK-04**: Upstream merge tracking — document process for periodically merging from `maximhq/bifrost`

### Keycloak OIDC Authentication

- [ ] **AUTH-01**: OIDC discovery via `.well-known/openid-configuration` — auto-configure endpoints from issuer URL
- [ ] **AUTH-02**: JWT token validation in AuthMiddleware — validate Bearer tokens against Keycloak JWKS, check audience + expiry
- [ ] **AUTH-03**: Claims extraction — extract `sub`, `email`, `groups`, `organization_id` from ID token
- [ ] **AUTH-04**: Claims-to-governance mapping — `organization_id` → Bifrost Customer, `sub` → User, groups → Team
- [ ] **AUTH-05**: Config.json OIDC section — issuer URL, client ID/secret, scopes, claim name mappings
- [ ] **AUTH-06**: Token refresh with `singleflight.Group` — prevent refresh token races under concurrent requests
- [ ] **AUTH-07**: OIDC provider implemented in new files only (`framework/oidc/`, `handlers/oidc.go`) — minimize diff with upstream

### Deployment

- [ ] **DEPLOY-01**: Docker image built from fork and pushed to GHCR (`ghcr.io/stragix-innovations/bifrost`)
- [ ] **DEPLOY-02**: infra-ctrl manifests updated to use our image with OIDC config + CustomProviderConfig
- [ ] **DEPLOY-03**: Keycloak client created for Bifrost via Pulumi (redirect URIs, client secret in Vault)

## v2 Requirements (Deferred)

- [ ] Auto-provisioning of Bifrost Customer + VirtualKey from OIDC claims (zero manual setup per tenant)
- [ ] Group-to-allowed-models mapping (Keycloak group → model access tier)
- [ ] Provider health-aware failover across named instances
- [ ] Per-instance cost tracking with custom pricing ($0 for self-hosted)
- [ ] PKCE support for public clients
- [ ] Semantic caching evaluation

## Out of Scope

- Bifrost Enterprise features (clustering, guardrails, custom plugins) — not needed for v1
- Vault-native key management in Bifrost — we use ExternalSecrets at K8s layer
- SAML/SCIM enterprise SSO — OIDC only, Keycloak bridges SAML if needed
- MCP Gateway extensions — leave upstream untouched
- Multi-IdP framework (Google/GitHub/Keycloak picker) — Keycloak only, existing Google/GitHub stays as-is
- User management UI in Bifrost — users managed in Keycloak, Bifrost shows governance only
- `type:instance` key scheme in core code — CustomProviderConfig achieves the same goal without core changes

## Traceability

| Requirement | Phase | Status |
|-------------|-------|--------|
| PROV-01 | Phase 1 | Pending |
| PROV-02 | Phase 1 | Pending |
| PROV-03 | Phase 1 | Pending |
| PROV-04 | Phase 1 | Pending |
| PROV-05 | Phase 1 | Pending |
| FORK-01 | Phase 2 | Complete |
| FORK-02 | Phase 2 | Complete |
| FORK-03 | Phase 2 | Complete |
| FORK-04 | Phase 2 | Complete |
| AUTH-01 | Phase 2 | Pending |
| AUTH-02 | Phase 2 | Pending |
| AUTH-03 | Phase 2 | Pending |
| AUTH-04 | Phase 2 | Pending |
| AUTH-05 | Phase 2 | Pending |
| AUTH-06 | Phase 2 | Pending |
| AUTH-07 | Phase 2 | Pending |
| DEPLOY-01 | Phase 3 | Pending |
| DEPLOY-02 | Phase 3 | Pending |
| DEPLOY-03 | Phase 3 | Pending |
