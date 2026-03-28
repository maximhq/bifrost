# Roadmap: Stragix Bifrost

## Overview

Replace LiteLLM with a forked Bifrost LLM gateway that routes to multiple OpenAI-compatible backends (self-hosted and cloud) with Keycloak OIDC authentication. Phase 1 delivers multi-provider routing immediately using upstream Bifrost's existing CustomProviderConfig (zero code changes). Phase 2 forks Bifrost and implements OIDC authentication end-to-end. Phase 3 deploys the forked image to production with full Keycloak integration.

## Phases

**Phase Numbering:**
- Integer phases (1, 2, 3): Planned milestone work
- Decimal phases (2.1, 2.2): Urgent insertions (marked with INSERTED)

Decimal phases appear between their surrounding integers in numeric order.

- [ ] **Phase 1: Named Provider Instances** - Configure multiple OpenAI-compatible backends via CustomProviderConfig using upstream Bifrost image
- [ ] **Phase 2: Fork and OIDC Authentication** - Establish Bifrost fork with Go workspace strategy and implement Keycloak OIDC end-to-end
- [ ] **Phase 3: Production Deployment** - Deploy forked image to dev/staging/prod with Keycloak client and infra-ctrl manifests

## Phase Details

### Phase 1: Named Provider Instances
**Goal**: Each self-hosted and cloud LLM backend routes through a single Bifrost instance with independent configuration
**Depends on**: Nothing (first phase)
**Requirements**: PROV-01, PROV-02, PROV-03, PROV-04, PROV-05
**Success Criteria** (what must be TRUE):
  1. A request to `llama-cpp/str-qwen3-coder-30b-bf16` routes to the llama-cpp-service backend and returns a completion
  2. A request to a Fireworks-hosted model routes to the Fireworks API with the correct API key
  3. Self-hosted models (llama-cpp, ollama) work without API keys (`is_key_less: true`)
  4. Each self-hosted model service (llama-cpp-service, llama-cpp-vision-service, llama-cpp-coder-quant-service, llama-cpp-gptoss-service) has its own named provider instance in config
  5. The Bifrost web UI displays all named provider instances and their status
**Plans:** 1 plan

Plans:
- [ ] 01-01-PLAN.md -- Update dev and prod ConfigMaps with CustomProviderConfig for 9 named providers

### Phase 2: Fork and OIDC Authentication
**Goal**: A Keycloak-authenticated user can make LLM requests through Bifrost with their identity mapped to the correct Customer/Team for budget and rate limit enforcement
**Depends on**: Phase 1 (config format validated, routing confirmed)
**Requirements**: FORK-01, FORK-02, FORK-03, FORK-04, AUTH-01, AUTH-02, AUTH-03, AUTH-04, AUTH-05, AUTH-06, AUTH-07
**Success Criteria** (what must be TRUE):
  1. Fork builds and tests pass with `go.work` workspace strategy, keeping `maximhq/bifrost` import paths
  2. CI pipeline produces a multi-arch Docker image (amd64 + arm64) and pushes to GHCR
  3. A user with a valid Keycloak JWT can authenticate to Bifrost API endpoints
  4. OIDC claims (`sub`, `email`, `organization_id`, `groups`) are extracted and mapped to Bifrost Customer/User/Team entities
  5. An expired or invalid JWT is rejected with an appropriate error before reaching any handler
**Plans:** 4 plans

Plans:
- [x] 02-01-PLAN.md -- Fork infrastructure: go.work validation, golang-jwt CVE fix, upstream merge docs
- [x] 02-02-PLAN.md -- CI pipelines: multi-arch Docker build + upstream drift check
- [x] 02-03-PLAN.md -- OIDC core package: config, claims, provider singleton with JWKS
- [x] 02-04-PLAN.md -- OIDC middleware: JWT validation handler + server.go wiring

### Phase 3: Production Deployment
**Goal**: Bifrost fork is live in dev and prod with OIDC authentication and named provider routing working end-to-end
**Depends on**: Phase 2 (fork image exists with OIDC)
**Requirements**: DEPLOY-01, DEPLOY-02, DEPLOY-03
**Success Criteria** (what must be TRUE):
  1. Forked Bifrost image is deployed in dev and prod, replacing the upstream image
  2. infra-ctrl manifests reference the fork image with CustomProviderConfig and OIDC configuration
  3. A Keycloak user from the stragixlabs realm can authenticate to Bifrost and make an LLM request that is rate-limited by their org
  4. Keycloak client for Bifrost exists in Pulumi with correct redirect URIs and client secret in Vault
**Plans**: TBD

Plans:
- [ ] 03-01: TBD

## Progress

**Execution Order:**
Phases execute in numeric order: 1 -> 2 -> 3

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Named Provider Instances | 0/1 | Planning complete | - |
| 2. Fork and OIDC Authentication | 0/4 | Planning complete | - |
| 3. Production Deployment | 0/0 | Not started | - |
