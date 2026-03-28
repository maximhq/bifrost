# Phase 2: Fork and OIDC Authentication - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md -- this log preserves the alternatives considered.

**Date:** 2026-03-28
**Phase:** 02-fork-and-oidc-authentication
**Areas discussed:** OIDC integration approach, Claims mapping strategy, Fork repository strategy, CI/CD pipeline design
**Mode:** auto (all areas auto-selected, recommended defaults chosen)

---

## OIDC Integration Approach

| Option | Description | Selected |
|--------|-------------|----------|
| New middleware before AuthMiddleware | OIDC in new files, slots before existing auth | [auto] |
| Extend AuthMiddleware | Add OIDC branch inside existing auth | |
| Replace AuthMiddleware | Remove existing auth, OIDC only | |

**User's choice:** [auto] New OIDC middleware in new files before AuthMiddleware
**Notes:** Minimizes upstream diff per AUTH-07 and research PITFALL #3. OIDC takes precedence when present, short-circuits existing auth. Dedicated `BifrostContextKeyOIDC*` keys avoid collision. Fail-fast at startup if OIDC configured but discovery fails.

---

## Claims Mapping Strategy

| Option | Description | Selected |
|--------|-------------|----------|
| Pre-provisioned lookup | Lookup Customer by organization_id, 403 if not found | [auto] |
| Auto-provision on first login | Create Customer/VirtualKey from claims automatically | |
| Hybrid | Lookup first, auto-provision if not found | |

**User's choice:** [auto] Pre-provisioned lookup (v1), auto-provisioning deferred to v2
**Notes:** organization_id from custom Keycloak protocol mapper. sub -> User, groups -> Team by name. Unmapped groups silently skipped in v1.

---

## Fork Repository Strategy

| Option | Description | Selected |
|--------|-------------|----------|
| GitHub stragix-innovations/bifrost | Public fork on GitHub | [auto] |
| In-house git server | Private git, manual sync | |

**User's choice:** [auto] GitHub stragix-innovations/bifrost
**Notes:** Keep maximhq/bifrost import paths, use go.work for dev and replace directives for Docker builds. Weekly upstream drift CI check. setup-go-workspace.sh already covers all 14 modules.

---

## CI/CD Pipeline Design

| Option | Description | Selected |
|--------|-------------|----------|
| GitHub Actions + docker buildx + GHCR | Standard multi-arch CI, push to GHCR | [auto] |
| GitHub Actions + Harbor | Push to self-hosted Harbor | |

**User's choice:** [auto] GitHub Actions + docker buildx + GHCR
**Notes:** Push to main triggers image build. PRs trigger tests only. Semantic versioning for tags. ghcr.io/stragix-innovations/bifrost.

---

## Claude's Discretion

- Group-to-Team mapping implementation details
- OIDC middleware error response format
- Test strategy for OIDC middleware
- Upstream merge conflict resolution approach

## Deferred Ideas

- Auto-provisioning of Customer/VirtualKey from OIDC claims (v2)
- Group-to-allowed-models mapping (v2)
- PKCE for public clients (v2)
- Per-instance cost tracking (v2)
- Clustering/HA (v2+)
