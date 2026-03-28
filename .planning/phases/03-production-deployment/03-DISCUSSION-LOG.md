# Phase 3: Production Deployment - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md -- this log preserves the alternatives considered.

**Date:** 2026-03-28
**Phase:** 03-production-deployment
**Areas discussed:** Image swap strategy, Keycloak client provisioning, Secret management, Environment coverage
**Mode:** auto (all areas auto-selected, recommended defaults chosen)

---

## Image Swap Strategy

| Option | Description | Selected |
|--------|-------------|----------|
| Dev first, then prod | Validate in dev before prod cutover | [auto] |
| Simultaneous | Deploy to both at once | |
| Blue-green | Run upstream and fork side by side | |

**User's choice:** [auto] Dev first, then prod
**Notes:** Pin to specific semver tag or SHA digest, not :latest. Both initContainer and main container images need updating.

---

## Keycloak Client Provisioning

| Option | Description | Selected |
|--------|-------------|----------|
| Pulumi in tf-infra | Consistent with existing Keycloak management | [auto] |
| Manual in Keycloak UI | Quick but not reproducible | |
| Keycloak CRD in infra-ctrl | K8s-native but CRD has known issues | |

**User's choice:** [auto] Pulumi in tf-infra, stragixlabs realm
**Notes:** Confidential client, authorization_code + client_credentials grants. Client secret stored in Vault.

---

## Secret Management

| Option | Description | Selected |
|--------|-------------|----------|
| ExternalSecret from Vault | Consistent with existing pattern | [auto] |
| SealedSecret | Works but harder to rotate | |
| Env var in deployment | Insecure, not for secrets | |

**User's choice:** [auto] ExternalSecret from Vault
**Notes:** New or extended ExternalSecret for OIDC client_secret. BIFROST_OIDC_CLIENT_SECRET env var.

---

## Environment Coverage

| Option | Description | Selected |
|--------|-------------|----------|
| Dev and prod only | Matches current overlay structure | [auto] |
| All three (dev, staging, prod) | Full coverage | |
| Dev only first | Most conservative | |

**User's choice:** [auto] Dev and prod only
**Notes:** Staging follows when staging cluster is ready.

---

## Claude's Discretion

- Image tag format (SHA vs semver)
- ExternalSecret extension vs creation
- Config.json formatting
- Network policy Keycloak egress specifics

## Deferred Ideas

- Staging environment deployment
- Automated smoke tests post-deploy
- Kargo promotion pipeline for Bifrost
- Monitoring/alerting for OIDC failures
