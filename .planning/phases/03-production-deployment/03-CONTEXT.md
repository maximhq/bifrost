# Phase 3: Production Deployment - Context

**Gathered:** 2026-03-28
**Status:** Ready for planning

<domain>
## Phase Boundary

Deploy the forked Bifrost image to dev and prod clusters, replacing the upstream `maximhq/bifrost:v1.4.16` image. Update infra-ctrl manifests with OIDC configuration and CustomProviderConfig. Create the Keycloak OIDC client for Bifrost via Pulumi. Validate end-to-end: Keycloak user authenticates, OIDC claims map to Bifrost Customer/Team, LLM request is rate-limited by org.

This is an infrastructure/deployment phase -- changes are in infra-ctrl manifests and tf-infra Pulumi, NOT in the Bifrost Go codebase.

</domain>

<decisions>
## Implementation Decisions

### Image Swap Strategy
- **D-01:** Deploy to dev first, validate OIDC and routing end-to-end, then deploy to prod
- **D-02:** Pin fork image to a specific semver tag or SHA digest, NOT `:latest` -- production safety requires deterministic image references
- **D-03:** Both initContainer and main container images must be updated from `maximhq/bifrost:v1.4.16` to `ghcr.io/stragix-innovations/bifrost:{tag}` (the initContainer uses the same image for `cp` command)
- **D-04:** Reloader annotations already exist (`configmap.reloader.stakater.com/reload: "bifrost-config"`, `secret.reloader.stakater.com/reload: "bifrost-llm-providers"`) -- pod will restart on ConfigMap/Secret changes automatically

### OIDC Configuration in config.json
- **D-05:** Add `oidc` section to the ConfigMap's config.json alongside existing provider config. Fields: `issuer_url` (Keycloak realm URL), `client_id`, `scopes` (default: `["openid", "profile", "email"]`), `org_claim` (default: `"organization_id"`), `groups_claim` (default: `"groups"`)
- **D-06:** `client_secret` is NOT in config.json -- it comes from ExternalSecret/Vault as an env var, referenced by the OIDC middleware at runtime
- **D-07:** Dev issuer_url: `https://keycloak-dev.tail15b586.ts.net/realms/stragixlabs`. Prod issuer_url: `https://keycloak-prod.tail15b586.ts.net/realms/stragixlabs`. The issuer_url MUST match the `iss` claim in Keycloak-issued tokens -- Keycloak tokens use the frontend URL, which is the Tailscale hostname in this setup. Same realm, same client_id, different client_secret per environment.

### Keycloak Client Provisioning
- **D-08:** Create Bifrost OIDC client in Pulumi (tf-infra repo), stragixlabs realm -- consistent with existing Keycloak management pattern
- **D-09:** Client type: confidential (server-side, not public). Grant types: authorization_code, client_credentials
- **D-10:** Redirect URIs: Bifrost's OAuth callback URL per environment (derived from Tailscale ingress hostname)
- **D-11:** Client secret stored in Vault at `infrastructure/auth/keycloak/clients/bifrost` (standard createPlatformClients path), pulled into pod via ExternalSecret. Vault payload: `{ client_id, client_secret, realm }`

### Secret Management
- **D-12:** Create new ExternalSecret `bifrost-oidc` (or extend existing `bifrost-llm-providers`) to pull OIDC client_secret from Vault
- **D-13:** Add `BIFROST_OIDC_CLIENT_SECRET` env var to deployment, sourced from the ExternalSecret-created Secret
- **D-14:** The OIDC middleware reads client_secret from env var (already supported by the config loading in Phase 2's server.go wiring)

### Environment Coverage
- **D-15:** Deploy to dev and prod only -- staging follows when staging cluster is ready. Matches current overlay structure
- **D-16:** The infra-ctrl-bifrost-eval worktree already has dev and prod overlays that can be used as the base for updates

### Network Policy
- **D-17:** Add egress rule to allow Bifrost to reach Keycloak for OIDC discovery and JWKS fetching (HTTPS port 443 to Keycloak service or external endpoint)

### Claude's Discretion
- Whether to extend `bifrost-llm-providers` ExternalSecret or create a separate `bifrost-oidc` ExternalSecret
- Config.json formatting and field ordering
- Network policy specifics for Keycloak egress

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Current Bifrost Deployment (infra-ctrl)
- `/Users/shawnwalker/code/stragix/infra-ctrl/apps/platform-services/bifrost/overlays/dev/deployment.yaml` -- Current dev deployment using `maximhq/bifrost:v1.4.16`, must be updated to fork image
- `/Users/shawnwalker/code/stragix/infra-ctrl/apps/platform-services/bifrost/overlays/dev/configmap.yaml` -- Current dev ConfigMap, needs OIDC section added
- `/Users/shawnwalker/code/stragix/infra-ctrl/apps/platform-services/bifrost/overlays/dev/kustomization.yaml` -- Dev overlay structure
- `/Users/shawnwalker/code/stragix/infra-ctrl/apps/platform-services/bifrost/overlays/dev/llm-providers-externalsecret.yaml` -- Existing ExternalSecret pattern for Vault secrets
- `/Users/shawnwalker/code/stragix/infra-ctrl/apps/platform-services/bifrost/overlays/dev/network-policy.yaml` -- Existing network policy, needs Keycloak egress

### Bifrost Eval Overlays (reference for prod)
- `/Users/shawnwalker/code/stragix/infra-ctrl-bifrost-eval/apps/platform-services/bifrost/overlays/prod/deployment.yaml` -- Prod deployment to update
- `/Users/shawnwalker/code/stragix/infra-ctrl-bifrost-eval/apps/platform-services/bifrost/overlays/prod/configmap.yaml` -- Prod ConfigMap to update

### Phase 2 Artifacts (what was built)
- `/Users/shawnwalker/code/stragix/bifrost/framework/oidc/config.go` -- OIDCConfig struct showing expected config.json fields
- `/Users/shawnwalker/code/stragix/bifrost/transports/bifrost-http/lib/config.go` -- How OIDCConfigRaw is loaded from config.json
- `/Users/shawnwalker/code/stragix/bifrost/transports/bifrost-http/server/server.go` -- How OIDC provider is initialized from config
- `/Users/shawnwalker/code/stragix/bifrost/.github/workflows/docker-build.yml` -- CI pipeline that produces the fork image

### infra-ctrl Patterns
- `/Users/shawnwalker/code/stragix/infra-ctrl/CLAUDE.md` -- infra-ctrl conventions: ExternalSecrets v1, Vault paths, ArgoCD patterns, network policy templates

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- Existing Bifrost deployment manifests (dev overlay) -- update in place, don't recreate
- ExternalSecret pattern from `bifrost-llm-providers` -- reuse for OIDC client secret
- Reloader annotations already configured -- no changes needed for auto-restart
- CiliumNetworkPolicy template from infra-ctrl CLAUDE.md -- use for Keycloak egress rule

### Established Patterns
- infra-ctrl ConfigMap: YAML with inline JSON `config.json` data field
- ExternalSecret: `external-secrets.io/v1` API version, Vault `kv/infrastructure/...` paths
- Kustomize overlay per environment with env-specific labels and cluster names
- ArgoCD ApplicationSet auto-discovers overlays -- no registration needed

### Integration Points
- ConfigMap `bifrost-config` -- OIDC section added here, mounted at `/app/data/config.json`
- ExternalSecret -- new secret for OIDC client_secret from Vault
- Deployment env vars -- add `BIFROST_OIDC_CLIENT_SECRET` from ExternalSecret
- Network policy -- add Keycloak egress alongside existing cloud/llama-cpp egress rules
- Pulumi in tf-infra -- Keycloak client resource for Bifrost

</code_context>

<specifics>
## Specific Ideas

- The infra-ctrl repo has TWO locations for Bifrost: the main `apps/platform-services/bifrost/` and the `infra-ctrl-bifrost-eval` worktree. Plans need to work across both or consolidate
- The current deployment uses `emptyDir` for data volume -- SQLite databases Bifrost creates at runtime live here. The fork image should be drop-in compatible
- Keycloak realm URL format: `https://keycloak-{env}.tail15b586.ts.net/realms/stragixlabs` -- the OIDC discovery endpoint appends `/.well-known/openid-configuration` automatically
- The docker-build.yml CI workflow pushes to GHCR -- the first successful push to main will produce the image tag needed for deployment

</specifics>

<deferred>
## Deferred Ideas

- Staging environment deployment -- deploy when staging cluster is ready
- Automated smoke test after deployment (curl Bifrost health + OIDC endpoint)
- Kargo promotion pipeline for Bifrost image across environments
- Monitoring/alerting for OIDC auth failures in Grafana

</deferred>

---

*Phase: 03-production-deployment*
*Context gathered: 2026-03-28*
