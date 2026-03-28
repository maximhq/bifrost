# Phase 1: Named Provider Instances - Context

**Gathered:** 2026-03-28
**Status:** Ready for planning

<domain>
## Phase Boundary

Configure multiple OpenAI-compatible backends via Bifrost's existing CustomProviderConfig mechanism. This is a config-only change in infra-ctrl overlays — zero Go code changes, uses the upstream `maximhq/bifrost:v1.4.16` image.

Each self-hosted model service and cloud provider gets its own named provider instance with independent base_url and API key configuration.

</domain>

<decisions>
## Implementation Decisions

### Provider Naming Convention
- **D-01:** Use service-descriptive names that map 1:1 with upstream services: `llama-cpp-bf16`, `llama-cpp-vision`, `llama-cpp-coder-quant`, `llama-cpp-gptoss`, `ollama-embed`, `fireworks`, `together`
- **D-02:** Each name is backed by `base_provider_type: "openai"` (or `"ollama"` for embeddings) via CustomProviderConfig
- **D-03:** Self-hosted providers use `is_key_less: true` — no API key needed for llama-cpp and ollama

### Config Format
- **D-04:** Use CustomProviderConfig in config.json with `custom_provider_configs` section. Each entry specifies `base_provider_type`, `base_url`, and optionally `is_key_less`
- **D-05:** Cloud providers (Groq, Cohere) stay as native providers — no CustomProviderConfig needed for them
- **D-06:** Cloud providers via OpenAI-compat (Fireworks, Together) use CustomProviderConfig with their respective API base URLs

### Rollout Strategy
- **D-07:** Update dev overlay first, validate routing via curl tests, then update prod
- **D-08:** Keep existing Bifrost deployment running during config update — Reloader will restart the pod on ConfigMap change

### Claude's Discretion
- Exact model name strings in config — match what llama-cpp serves via its `/v1/models` endpoint
- Network timeout values per provider (600s for local inference, 30s for cloud)

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Bifrost CustomProviderConfig
- `/Users/shawnwalker/code/stragix/bifrost/core/schemas/account.go` — CustomProviderConfig struct definition
- `/Users/shawnwalker/code/stragix/bifrost/transports/config.schema.json` — Config schema with custom_provider_configs section

### LiteLLM Provider Config (reference for model names and endpoints)
- `/Users/shawnwalker/code/stragix/infra-ctrl/apps/platform-services/litellm/base/configmap.yaml` — Current model list with api_base URLs per model
- `/Users/shawnwalker/code/stragix/infra-ctrl/apps/platform-services/litellm/overlays/dev/kustomization.yaml` — Dev overlay pattern
- `/Users/shawnwalker/code/stragix/infra-ctrl/apps/platform-services/litellm/overlays/prod/kustomization.yaml` — Prod overlay pattern

### Current Bifrost Deployment (infra-ctrl)
- `/Users/shawnwalker/code/stragix/infra-ctrl/apps/platform-services/bifrost/overlays/dev/configmap.yaml` — Current dev config to update
- `/Users/shawnwalker/code/stragix/infra-ctrl/apps/platform-services/bifrost/overlays/prod/configmap.yaml` — Current prod config to update

### Research
- `/Users/shawnwalker/code/stragix/bifrost/.planning/research/ARCHITECTURE.md` — CustomProviderConfig discovery and usage pattern

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- CustomProviderConfig in Bifrost core — registers arbitrary provider names backed by known types
- `RegisterKnownProvider` — makes `ParseModelString` recognize custom names automatically
- Existing infra-ctrl Bifrost overlays (dev + prod) — update in place

### Established Patterns
- infra-ctrl ConfigMap pattern: YAML with inline JSON `config.json` data field
- Kustomize overlay per environment (dev, prod) with env-specific values
- Reloader annotations trigger pod restart on ConfigMap/Secret changes
- ExternalSecrets pull API keys from Vault → K8s Secrets → env vars in pod

### Integration Points
- ConfigMap `bifrost-config` in each overlay → mounted into pod at `/app/data/config.json` via initContainer
- ExternalSecret `bifrost-llm-providers` → provides cloud API keys as env vars
- Network policies already allow egress to llama-cpp-system (8080), ollama-system (11434), and HTTPS (443)

</code_context>

<specifics>
## Specific Ideas

- Model names must match exactly what llama-cpp serves — check via `curl llama-cpp-service:8080/v1/models` on prod cluster
- Each llama-cpp service runs a different model on a different GPU — they are NOT interchangeable
- Fireworks base_url must NOT include `/v1` — Bifrost appends it (confirmed during eval: `/v1/v1/` double-path bug)

</specifics>

<deferred>
## Deferred Ideas

None — discussion stayed within phase scope

</deferred>

---

*Phase: 01-named-provider-instances*
*Context gathered: 2026-03-28*
