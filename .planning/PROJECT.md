# Stragix Bifrost — LLM Gateway Fork

## What This Is

A fork of [maximhq/bifrost](https://github.com/maximhq/bifrost) (Go, Apache 2.0) — a high-performance AI gateway with <100µs overhead at 5k RPS. We're adding two targeted features to make it production-ready as the LLM gateway for Stragix's RAG product, replacing LiteLLM (Python, slow, recent supply chain attack).

## Core Value

**Multi-tenant LLM gateway that lets each customer org get their own key, budget, and rate limits — routing to both self-hosted and cloud models through a single fast API.**

## Context

- **Why fork Bifrost**: LiteLLM PyPI supply chain attack (v1.82.7/1.82.8, March 2026). Our Docker v1.81.3 was unaffected but LiteLLM is Python (~8ms P95 overhead) and the supply chain risk is a pattern. Bifrost is Go (<100µs overhead), has governance (customers, virtual keys, budgets, rate limiting), and is Apache 2.0.
- **Why not use Bifrost as-is**: Two blockers:
  1. **Single provider per type**: Can't have multiple `openai` providers with different `base_url`s. We need llama-cpp (self-hosted), Fireworks (cloud), Together (cloud), all OpenAI-compatible but different endpoints.
  2. **No Keycloak OIDC**: SSO is enterprise-only (Google/GitHub in OSS). We need Keycloak integration for our platform's auth system.
- **Product use case**: RAG customers sign up → platform creates Keycloak user + Bifrost Customer with Virtual Key → customer's app authenticates via SSO → Bifrost enforces per-org limits → routes to correct backend.
- **Eval status**: Bifrost deployed to dev (PR #1374) and prod (PR #1378) in infra-ctrl. Confirmed the single-provider limitation hands-on.

## Architecture (existing Bifrost)

- **Language**: Go
- **Entry point**: `/transports/bifrost-http/` (HTTP transport layer)
- **Core**: `/core/bifrost.go` (provider registry, request routing, sync.Map keyed by provider name)
- **Providers**: `/core/providers/{name}/` (openai, anthropic, groq, cohere, etc.)
- **Schemas**: `/core/schemas/` (Provider interface, NetworkConfig, OAuth2 types)
- **Config**: `/transports/bifrost-http/lib/config.go` (config.json parsing, provider reconciliation)
- **Auth**: `/transports/bifrost-http/handlers/oauth2.go`, `session.go`, `middlewares.go`
- **Storage**: SQLite by default (`config.db`, `logs.db` in APP_DIR)
- **Image**: `maximhq/bifrost:v1.4.16` on Docker Hub

## Requirements

### Validated

- ✓ OpenAI-compatible API gateway — existing
- ✓ Virtual keys with budget/rate limits — existing
- ✓ Customer/Team/User governance hierarchy — existing
- ✓ Prometheus metrics + request logging — existing
- ✓ Provider failover + load balancing — existing
- ✓ Web UI for management — existing
- ✓ Groq, Cohere native providers — existing
- ✓ Ollama native provider — existing

### Active

- [ ] **PROV-01**: Named provider instances — multiple `openai` providers with independent `base_url` and config (e.g., `openai:llama-cpp`, `openai:fireworks`, `openai:together`)
- [ ] **PROV-02**: Request routing resolves model → correct named instance automatically
- [ ] **PROV-03**: Config.json supports `type:instance` provider naming
- [ ] **PROV-04**: API endpoints (`/api/providers`) support CRUD for named instances
- [ ] **PROV-05**: Web UI displays named instances correctly
- [ ] **AUTH-01**: Generic OIDC provider for Keycloak SSO (discovery, token validation, user claims)
- [ ] **AUTH-02**: OIDC user claims (sub, email, groups) map to Bifrost user/customer/team
- [ ] **AUTH-03**: Config.json supports OIDC provider configuration (server URL, client ID/secret, scopes)
- [ ] **DEPLOY-01**: Docker image built and pushed to Harbor/GHCR from our fork
- [ ] **DEPLOY-02**: infra-ctrl manifests updated to use our image with named providers + OIDC config

### Out of Scope

- Bifrost Enterprise features (clustering, guardrails, custom plugins) — not needed for v1
- Vault-native key management in Bifrost — we use ExternalSecrets + Vault at the K8s layer
- Semantic caching — evaluate later
- MCP Gateway support — not needed for RAG product

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Fork Bifrost, not build from scratch | Bifrost has 95% of what we need. Two targeted Go changes vs. building a full gateway | Fork |
| Named instances via `type:instance` key | Minimal change to provider registry. Backward-compatible (no `:` = default instance) | Pending |
| Reference oauth2-proxy for OIDC | Battle-tested Go OIDC implementation, MIT licensed, Keycloak-specific provider | Pending |
| Deploy from our own Docker image | Need to control the binary. Push to Harbor/GHCR, deploy via infra-ctrl | Pending |
| Skip Bifrost clustering for v1 | Single replica is fine for eval. Add clustering later if needed | Skip |

## Constraints

- Must remain backward-compatible with upstream Bifrost config format (no `:` in name = default instance)
- Must track upstream — periodically merge from `maximhq/bifrost` to get security fixes and new features
- Go codebase — all changes in Go
- Docker image must be multi-arch (amd64 + arm64) for Talos cluster nodes

## Evolution

This document evolves at phase transitions and milestone boundaries.

**After each phase transition** (via `/gsd:transition`):
1. Requirements invalidated? → Move to Out of Scope with reason
2. Requirements validated? → Move to Validated with phase reference
3. New requirements emerged? → Add to Active
4. Decisions to log? → Add to Key Decisions
5. "What This Is" still accurate? → Update if drifted

**After each milestone** (via `/gsd:complete-milestone`):
1. Full review of all sections
2. Core Value check — still the right priority?
3. Audit Out of Scope — reasons still valid?
4. Update Context with current state

---
*Last updated: 2026-03-28 after initialization*
