# Phase 1: Named Provider Instances - Research

**Researched:** 2026-03-26
**Domain:** Bifrost CustomProviderConfig + infra-ctrl Kustomize overlays
**Confidence:** HIGH

## Summary

Phase 1 is a config-only change: update the Bifrost `config.json` ConfigMap in the infra-ctrl repo to use `CustomProviderConfig` for each self-hosted model service and OpenAI-compatible cloud provider. Zero Go code changes are needed -- the upstream `maximhq/bifrost:v1.4.16` image already supports this mechanism.

The current dev config has `fireworks` and `together` as top-level provider names without `custom_provider_config`. Since neither is a standard Bifrost provider, `createBaseProvider` would hit the `default` case and log a warning (provider init failure does NOT crash Bifrost -- it continues serving other providers). This phase fixes that by adding proper `custom_provider_config` with `base_provider_type: "openai"` for each non-standard provider.

**Primary recommendation:** Define each self-hosted service and cloud-compat provider as a named entry in the `providers` map with a `custom_provider_config` block specifying `base_provider_type` and `is_key_less` (where applicable). Keep Groq and Cohere as native providers since they are in Bifrost's `StandardProviders` list.

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions
- **D-01:** Use service-descriptive names that map 1:1 with upstream services: `llama-cpp-bf16`, `llama-cpp-vision`, `llama-cpp-coder-quant`, `llama-cpp-gptoss`, `ollama-embed`, `fireworks`, `together`
- **D-02:** Each name is backed by `base_provider_type: "openai"` (or `"ollama"` for embeddings) via CustomProviderConfig
- **D-03:** Self-hosted providers use `is_key_less: true` -- no API key needed for llama-cpp and ollama
- **D-04:** Use CustomProviderConfig in config.json with `custom_provider_configs` section. Each entry specifies `base_provider_type`, `base_url`, and optionally `is_key_less`
- **D-05:** Cloud providers (Groq, Cohere) stay as native providers -- no CustomProviderConfig needed for them
- **D-06:** Cloud providers via OpenAI-compat (Fireworks, Together) use CustomProviderConfig with their respective API base URLs
- **D-07:** Update dev overlay first, validate routing via curl tests, then update prod
- **D-08:** Keep existing Bifrost deployment running during config update -- Reloader will restart the pod on ConfigMap change

### Claude's Discretion
- Exact model name strings in config -- match what llama-cpp serves via its `/v1/models` endpoint
- Network timeout values per provider (600s for local inference, 30s for cloud)

### Deferred Ideas (OUT OF SCOPE)
None -- discussion stayed within phase scope
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| PROV-01 | Configure multiple OpenAI-compatible backends via CustomProviderConfig in config.json | Full CustomProviderConfig schema and Go struct documented; exact JSON format verified from source code |
| PROV-02 | Validate model routing works correctly -- requests to `llama-cpp-bf16/str-qwen3-coder-30b-bf16` route to the correct backend | ParseModelString + RegisterKnownProvider flow traced end-to-end; custom provider names auto-registered |
| PROV-03 | Validate `is_key_less: true` works for self-hosted models | OpenAI provider handles keyless: creates empty key `[]schemas.Key{{}}` when `IsKeyLess` is true; confirmed in 7+ code paths |
| PROV-04 | Update infra-ctrl dev and prod overlays to use CustomProviderConfig format | Dev overlay exists at known path; prod overlay does NOT exist yet (must be created); file list documented |
| PROV-05 | Each self-hosted model service gets its own named provider instance | LiteLLM config provides exact model names and service URLs for all 4 llama-cpp services + ollama |
</phase_requirements>

## Standard Stack

This phase requires no libraries. It is a ConfigMap update in infra-ctrl.

### Tools Required
| Tool | Purpose | Already Available |
|------|---------|-------------------|
| `kustomize` | Validate overlay builds | Yes (CI requirement) |
| `kubectl` | Apply and verify on cluster | Yes (via `str cl dev`) |
| `curl` | Test model routing after deploy | Yes |

## Architecture Patterns

### CustomProviderConfig JSON Format (Verified from Source)

The `providers` section in `config.json` accepts arbitrary names as keys (JSON schema has `additionalProperties: true`). Each entry is a `ProviderConfig` struct.

**Keyless self-hosted provider (llama-cpp):**
```json
{
  "llama-cpp-bf16": {
    "custom_provider_config": {
      "base_provider_type": "openai",
      "is_key_less": true
    },
    "network_config": {
      "base_url": "http://llama-cpp-service.llama-cpp-system.svc.cluster.local:8080",
      "default_request_timeout_in_seconds": 600
    }
  }
}
```

**Cloud OpenAI-compat provider (Fireworks):**
```json
{
  "fireworks": {
    "custom_provider_config": {
      "base_provider_type": "openai"
    },
    "keys": [
      {
        "name": "fireworks-primary",
        "value": "env.FIREWORKS_AI_API_KEY",
        "models": [
          "accounts/fireworks/models/qwen3-235b-a22b-instruct-2507"
        ]
      }
    ],
    "network_config": {
      "base_url": "https://api.fireworks.ai/inference",
      "default_request_timeout_in_seconds": 30
    }
  }
}
```

**Native Ollama provider for embeddings:**
```json
{
  "ollama-embed": {
    "custom_provider_config": {
      "base_provider_type": "ollama",
      "is_key_less": true
    },
    "network_config": {
      "base_url": "http://ollama-service.ollama-system.svc.cluster.local:11434"
    }
  }
}
```

### How CustomProviderConfig Works (Code Path Trace)

**Registration flow** (verified from `core/bifrost.go` and `core/schemas/utils.go`):

1. `LoadConfig` reads `config.json` -- `processProvider` stores each entry in `map[ModelProvider]ProviderConfig`
2. `Bifrost.Init` iterates providers, calls `prepareProvider(providerKey, config)`
3. `prepareProvider` calls `createBaseProvider(providerKey, config)`
4. `createBaseProvider` checks `config.CustomProviderConfig != nil` -- if yes, overrides `targetProviderKey = BaseProviderType`
5. `switch targetProviderKey` creates the correct provider implementation (OpenAI, Ollama, etc.)
6. `prepareProvider` calls `schemas.RegisterKnownProvider(providerKey)` -- adds e.g., `"llama-cpp-bf16"` to the known providers set
7. Workers start consuming from the provider queue

**Request flow** (verified from `core/schemas/utils.go`):

1. Client sends `POST /v1/chat/completions` with `"model": "llama-cpp-bf16/str-qwen3-coder-30b-bf16"`
2. `ParseModelString` splits on first `/`, checks `IsKnownProvider("llama-cpp-bf16")` -- returns `true` (registered in step 6 above)
3. Returns `(ModelProvider("llama-cpp-bf16"), "str-qwen3-coder-30b-bf16")`
4. `getProviderQueue("llama-cpp-bf16")` finds the queue
5. Request is dispatched to the OpenAI provider instance backed by `http://llama-cpp-service...:8080`

**Keyless flow** (verified from `core/utils.go` and `core/providers/openai/openai.go`):

1. `providerRequiresKey` checks `customConfig.IsKeyLess && BaseProviderType != Bedrock` -- returns `false` (no key required)
2. On request dispatch, when `len(keys) == 0` and `customProviderConfig.IsKeyLess`, the OpenAI provider creates a dummy key: `keys = []schemas.Key{{}}`
3. The HTTP request is built with an empty Authorization header (no `Bearer` token sent)

### Base URL Rules (Critical -- Source of Double /v1 Bug)

The OpenAI provider builds URLs as: `baseURL + "/v1/chat/completions"` (or `/v1/models`, `/v1/embeddings`, etc.)

| Service | Correct base_url | Resulting URL |
|---------|-----------------|---------------|
| llama-cpp | `http://llama-cpp-service...:8080` | `.../v1/chat/completions` (correct) |
| Fireworks | `https://api.fireworks.ai/inference` | `.../inference/v1/chat/completions` (correct) |
| Together | `https://api.together.xyz` | `.../v1/chat/completions` (correct) |
| **BAD** Fireworks | `https://api.fireworks.ai/inference/v1` | `.../inference/v1/v1/chat/completions` (BROKEN) |

The Ollama provider builds URLs as: `baseURL + "/v1/embeddings"` (or `/api/tags` for models)

| Service | Correct base_url | Resulting URL |
|---------|-----------------|---------------|
| Ollama | `http://ollama-service...:11434` | `.../v1/embeddings` (correct) |
| **BAD** Ollama | `http://ollama-service...:11434/v1` | `.../v1/v1/embeddings` (BROKEN) |

**Rule: Never include `/v1` in `base_url` -- Bifrost always appends it.**

### Provider Name to K8s Service Mapping

Derived from LiteLLM config (source of truth for service URLs):

| Provider Name | K8s Service | Port | Model Names |
|---------------|-------------|------|-------------|
| `llama-cpp-bf16` | `llama-cpp-service.llama-cpp-system.svc.cluster.local` | 8080 | `str-qwen3-coder-30b-bf16` |
| `llama-cpp-vision` | `llama-cpp-vision-service.llama-cpp-system.svc.cluster.local` | 8080 | `str-qwen3.5-9b-vl` |
| `llama-cpp-coder-quant` | `llama-cpp-coder-quant-service.llama-cpp-system.svc.cluster.local` | 8080 | `str-qwen3-coder-30b-q4km` |
| `llama-cpp-gptoss` | `llama-cpp-gptoss-service.llama-cpp-system.svc.cluster.local` | 8080 | `str-gpt-oss-20b` |
| `ollama-embed` | `ollama-service.ollama-system.svc.cluster.local` | 11434 | `nomic-embed-text` |
| `fireworks` | `https://api.fireworks.ai/inference` | 443 | 7 models (see Fireworks section below) |
| `together` | `https://api.together.xyz` | 443 | `deepseek-ai/DeepSeek-R1` |

**Note on model names:** The model names in the `models` array on keys (for cloud providers) and in client requests must match exactly what the upstream server reports via `/v1/models`. For llama-cpp services, these names are set at server startup and match the `model_name` in the LiteLLM config. For cloud providers, the model names are the upstream-specific identifiers (e.g., `accounts/fireworks/models/qwen3-235b-a22b-instruct-2507`).

### Files to Modify in infra-ctrl

| File | Change | Environment |
|------|--------|-------------|
| `apps/platform-services/bifrost/overlays/dev/configmap.yaml` | Replace entire `config.json` with new CustomProviderConfig-based format | dev |
| `apps/platform-services/bifrost/overlays/prod/` (NEW directory + files) | Create prod overlay by copying dev and adjusting env labels | prod |

**Files that need NO changes:**
- `deployment.yaml` -- already has Reloader annotations, image stays `maximhq/bifrost:v1.4.16`
- `llm-providers-externalsecret.yaml` -- already provides all 4 cloud API keys (GROQ, TOGETHERAI, COHERE, FIREWORKS_AI)
- `network-policy.yaml` -- already allows egress to `llama-cpp-system:8080`, `ollama-system:11434`, and `0.0.0.0/0:443`
- `kustomization.yaml` -- already includes all resources

### Existing ExternalSecret Keys (No Changes Needed)

The `bifrost-llm-providers` ExternalSecret already provides:
- `GROQ_API_KEY` (from `apps/litellm/groq`)
- `TOGETHERAI_API_KEY` (from `apps/litellm/together`)
- `COHERE_API_KEY` (from `apps/litellm/cohere`)
- `FIREWORKS_AI_API_KEY` (from `apps/litellm/fireworks_ai`)

These are referenced in the deployment env vars and in `config.json` via `env.VARIABLE_NAME`.

### Prod Overlay Status

The prod overlay does NOT exist yet. The ApplicationSet auto-discovers `apps/*/*/overlays/{env}` directories. To deploy Bifrost to prod, a `prod` overlay must be created under `apps/platform-services/bifrost/overlays/prod/` with:
- `kustomization.yaml` (env: prod, cluster: prod-sks)
- `configmap.yaml` (same config.json structure, possibly different model catalog)
- `deployment.yaml` (same image)
- `service.yaml`
- `namespace.yaml`
- `llm-providers-externalsecret.yaml` (same Vault paths, prod ClusterSecretStore)
- `network-policy.yaml` (same rules)
- `tailscale-ingress.yaml`

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Multiple OpenAI-compatible backends | Custom fork of ParseModelString or provider registry | `custom_provider_config` in config.json | Upstream already supports this; zero code changes needed |
| Keyless auth bypass for self-hosted | Custom middleware to strip auth headers | `is_key_less: true` in CustomProviderConfig | OpenAI provider already handles empty keys when flag is set |
| Provider-to-base-type mapping | Custom switch/case logic | `base_provider_type` field | `createBaseProvider` already reads this field and redirects |

**Key insight:** The entire named provider instances feature is already built into upstream Bifrost via `CustomProviderConfig`. This phase is purely a config change.

## Common Pitfalls

### Pitfall 1: Double /v1 in base_url
**What goes wrong:** Bifrost appends `/v1/chat/completions` to the `base_url`. If `base_url` already ends with `/v1`, the final URL becomes `.../v1/v1/chat/completions`.
**Why it happens:** Different provider APIs have different URL structures. Fireworks uses `/inference/v1/...`, so it's tempting to include the full path.
**How to avoid:** Never include `/v1` in `base_url`. For Fireworks: `https://api.fireworks.ai/inference`. For Together: `https://api.together.xyz`.
**Warning signs:** 404 errors from upstream providers; check logs for the full request URL.

### Pitfall 2: JSON Schema says `keys` is required with minItems: 1
**What goes wrong:** The JSON schema definition for `provider` lists `"required": ["keys"]` with `"minItems": 1`. A keyless provider with no `keys` array would fail schema validation.
**Why it happens:** The schema was designed for API-key-based providers. Keyless providers are a newer feature.
**How to avoid:** For keyless self-hosted providers, omit the `keys` field entirely. The Go code does NOT enforce schema validation at the JSON level -- it unmarshals directly into Go structs where `Keys` is `[]schemas.Key` (no `required` tag). The JSON schema is for IDE tooling, not runtime enforcement. The `providerRequiresKey` function handles the keyless path.
**Warning signs:** IDE warnings about missing `keys` -- these can be safely ignored for keyless providers.

### Pitfall 3: Using standard provider names for custom providers
**What goes wrong:** `ValidateCustomProvider` rejects `custom_provider_config` on standard provider names like `"openai"` or `"groq"`.
**Why it happens:** Custom providers cannot override built-in providers. The validation explicitly checks `IsStandardProvider(provider)` and returns an error.
**How to avoid:** Use non-standard names: `llama-cpp-bf16`, `fireworks`, `together`, `ollama-embed`. The names `groq` and `cohere` must stay as native providers (no `custom_provider_config`).
**Warning signs:** Provider init error: "custom provider validation failed: cannot be created on standard providers".

### Pitfall 4: Fireworks model name mismatch
**What goes wrong:** Requests fail with "no keys found that support model" because the model name in the request doesn't match the `models` array in the key config.
**Why it happens:** Fireworks model names include the full path (e.g., `accounts/fireworks/models/qwen3-235b-a22b-instruct-2507`). If the client sends just `qwen3-235b`, it won't match.
**How to avoid:** The `models` array in the key config must match the exact model identifier the upstream API uses. Check the LiteLLM config for the canonical names. Alternatively, use an empty `models: []` array to allow all models through the key.
**Warning signs:** 400 errors with "no keys found that support model/deployment".

### Pitfall 5: Together API base_url
**What goes wrong:** Together's API base URL is `https://api.together.xyz`, not `https://api.together.ai`.
**Why it happens:** Together rebranded but the API endpoint stayed at the `.xyz` domain.
**How to avoid:** Verify the base URL against Together's current documentation.
**Warning signs:** DNS resolution failures or 404s.

### Pitfall 6: Prod overlay not created yet
**What goes wrong:** Bifrost is only deployed to dev, not prod. D-07 says to validate on dev first, then update prod.
**Why it happens:** The prod overlay directory doesn't exist in infra-ctrl.
**How to avoid:** The plan must include creating the prod overlay as a separate task after dev validation.
**Warning signs:** ArgoCD shows no Bifrost application for prod environment.

## Code Examples

### Complete config.json for Dev Overlay

Based on the LiteLLM config (source of truth for model names and service URLs), the current Bifrost dev config, and the locked decisions:

```json
{
  "providers": {
    "llama-cpp-bf16": {
      "custom_provider_config": {
        "base_provider_type": "openai",
        "is_key_less": true
      },
      "network_config": {
        "base_url": "http://llama-cpp-service.llama-cpp-system.svc.cluster.local:8080",
        "default_request_timeout_in_seconds": 600
      }
    },
    "llama-cpp-vision": {
      "custom_provider_config": {
        "base_provider_type": "openai",
        "is_key_less": true
      },
      "network_config": {
        "base_url": "http://llama-cpp-vision-service.llama-cpp-system.svc.cluster.local:8080",
        "default_request_timeout_in_seconds": 600
      }
    },
    "llama-cpp-coder-quant": {
      "custom_provider_config": {
        "base_provider_type": "openai",
        "is_key_less": true
      },
      "network_config": {
        "base_url": "http://llama-cpp-coder-quant-service.llama-cpp-system.svc.cluster.local:8080",
        "default_request_timeout_in_seconds": 600
      }
    },
    "llama-cpp-gptoss": {
      "custom_provider_config": {
        "base_provider_type": "openai",
        "is_key_less": true
      },
      "network_config": {
        "base_url": "http://llama-cpp-gptoss-service.llama-cpp-system.svc.cluster.local:8080",
        "default_request_timeout_in_seconds": 600
      }
    },
    "ollama-embed": {
      "custom_provider_config": {
        "base_provider_type": "ollama",
        "is_key_less": true
      },
      "network_config": {
        "base_url": "http://ollama-service.ollama-system.svc.cluster.local:11434"
      }
    },
    "groq": {
      "keys": [
        {
          "name": "groq-primary",
          "value": "env.GROQ_API_KEY",
          "models": [
            "meta-llama/llama-4-maverick-17b-128e-instruct",
            "llama-3.3-70b-versatile",
            "llama-3.1-8b-instant",
            "meta-llama/llama-4-scout-17b-16e-instruct",
            "whisper-large-v3-turbo"
          ]
        }
      ]
    },
    "cohere": {
      "keys": [
        {
          "name": "cohere-primary",
          "value": "env.COHERE_API_KEY",
          "models": [
            "command-a-03-2025",
            "command-r-plus-08-2024",
            "embed-english-v3.0",
            "embed-multilingual-v3.0",
            "rerank-english-v3.0"
          ]
        }
      ]
    },
    "fireworks": {
      "custom_provider_config": {
        "base_provider_type": "openai"
      },
      "keys": [
        {
          "name": "fireworks-primary",
          "value": "env.FIREWORKS_AI_API_KEY",
          "models": [
            "accounts/fireworks/models/qwen3-235b-a22b-instruct-2507",
            "accounts/fireworks/models/qwen3-coder-480b-a35b-instruct",
            "accounts/fireworks/models/glm-5",
            "accounts/fireworks/models/minimax-m2p5",
            "accounts/fireworks/models/deepseek-v3p2",
            "accounts/fireworks/models/kimi-k2p5",
            "accounts/fireworks/models/nvidia-nemotron-3-super-120b-a12b-fp8"
          ]
        }
      ],
      "network_config": {
        "base_url": "https://api.fireworks.ai/inference",
        "default_request_timeout_in_seconds": 30
      }
    },
    "together": {
      "custom_provider_config": {
        "base_provider_type": "openai"
      },
      "keys": [
        {
          "name": "together-primary",
          "value": "env.TOGETHERAI_API_KEY",
          "models": [
            "deepseek-ai/DeepSeek-R1"
          ]
        }
      ],
      "network_config": {
        "base_url": "https://api.together.xyz",
        "default_request_timeout_in_seconds": 30
      }
    }
  },
  "cluster_config": {
    "enabled": false
  }
}
```

### Curl Validation Commands

After deploying the updated ConfigMap:

```bash
# Verify pod restarted (Reloader should trigger within 60s)
kubectl -n bifrost-system get pods -w

# Test self-hosted model routing (keyless)
curl -X POST http://bifrost.bifrost-system.svc.cluster.local:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "llama-cpp-bf16/str-qwen3-coder-30b-bf16",
    "messages": [{"role": "user", "content": "Hello"}],
    "max_tokens": 10
  }'

# Test cloud provider routing (with API key)
curl -X POST http://bifrost.bifrost-system.svc.cluster.local:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "groq/llama-3.1-8b-instant",
    "messages": [{"role": "user", "content": "Hello"}],
    "max_tokens": 10
  }'

# Test Fireworks routing (custom provider with key)
curl -X POST http://bifrost.bifrost-system.svc.cluster.local:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "fireworks/accounts/fireworks/models/glm-5",
    "messages": [{"role": "user", "content": "Hello"}],
    "max_tokens": 10
  }'

# Test list models (verifies provider registration)
curl http://bifrost.bifrost-system.svc.cluster.local:8080/v1/models

# Test embedding via Ollama
curl -X POST http://bifrost.bifrost-system.svc.cluster.local:8080/v1/embeddings \
  -H "Content-Type: application/json" \
  -d '{
    "model": "ollama-embed/nomic-embed-text",
    "input": "test embedding"
  }'
```

## State of the Art

| Old Approach (current config) | New Approach (this phase) | Impact |
|-------------------------------|--------------------------|--------|
| `"openai"` with Fireworks base_url | `"fireworks"` with CustomProviderConfig | Each provider is independently routable |
| `"together"` as unknown provider (fails) | `"together"` with `base_provider_type: "openai"` | Together actually works |
| No self-hosted models in Bifrost | 4 llama-cpp + 1 ollama via CustomProviderConfig | Self-hosted models accessible through Bifrost |

## Open Questions

1. **Exact model names from llama-cpp /v1/models**
   - What we know: Model names are set at llama-cpp startup and match what LiteLLM config uses (`str-qwen3-coder-30b-bf16`, `str-qwen3.5-9b-vl`, etc.)
   - What's unclear: Whether the exact string returned by `/v1/models` matches the LiteLLM config entry verbatim (could differ in casing or prefix)
   - Recommendation: After deploying to dev, verify with `curl http://llama-cpp-service...:8080/v1/models` and adjust config if needed

2. **Together.ai base_url**
   - What we know: Together's documented API base is `https://api.together.xyz`
   - What's unclear: Whether this is still current (Together has been rebranding)
   - Recommendation: Verify with a test curl against the base URL; adjust if needed

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| kustomize | Config validation | Yes | CI pipeline | -- |
| kubectl | Cluster verification | Yes | via `str cl dev` | -- |
| curl | Routing validation | Yes | built-in | -- |
| dev cluster | Dev deployment | Yes | dev-sks running | -- |
| prod cluster | Prod deployment | Yes | prod-sks running | -- |

**Missing dependencies with no fallback:** None

## Project Constraints (from CLAUDE.md)

### infra-ctrl Conventions
- Kustomize overlays per environment (dev, staging, prod) with env-specific labels
- ApplicationSet auto-discovers `apps/*/*/overlays/{env}` directories
- Reloader annotations on Deployments trigger pod restarts on ConfigMap/Secret changes
- ExternalSecrets use `external-secrets.io/v1` (not v1beta1)
- Vault paths: `apps/litellm/*` for LLM provider API keys
- Network policies already configured for required egress

### Stragix Global Rules
- Never commit secrets, tokens, or credentials
- Never push directly to `main` -- work in git worktrees
- Conventional commit messages (`feat:`, `fix:`, `chore:`)
- Always validate with `kustomize build --enable-helm` before committing

## Sources

### Primary (HIGH confidence)
- `bifrost/core/schemas/provider.go` lines 396-401 -- CustomProviderConfig struct definition
- `bifrost/core/bifrost.go` lines 3551-3620 -- `createBaseProvider` with CustomProviderConfig dispatch
- `bifrost/core/bifrost.go` lines 3625-3670 -- `prepareProvider` with `RegisterKnownProvider` call
- `bifrost/core/schemas/utils.go` lines 35-95 -- `knownProviders` map, `RegisterKnownProvider`, `ParseModelString`
- `bifrost/core/utils.go` lines 85-93 -- `providerRequiresKey` keyless check
- `bifrost/core/providers/openai/openai.go` lines 59-67 -- BaseURL handling (no /v1)
- `bifrost/core/providers/ollama/ollama.go` lines 48-56 -- Ollama BaseURL handling
- `bifrost/transports/config.schema.json` lines 1885-1961 -- `custom_provider_config` JSON schema
- `bifrost/transports/bifrost-http/lib/config.go` lines 3358-3384 -- `ValidateCustomProvider` validation rules
- `bifrost/core/schemas/bifrost.go` lines 62-97 -- `SupportedBaseProviders` and `StandardProviders` lists

### Secondary (MEDIUM confidence)
- `infra-ctrl/apps/platform-services/litellm/base/configmap.yaml` -- Model names and service URLs (source of truth for model catalog)
- `infra-ctrl/apps/platform-services/bifrost/overlays/dev/` -- Current deployment manifests

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH -- no libraries needed, pure config
- Architecture: HIGH -- all code paths verified from Go source (not documentation)
- Pitfalls: HIGH -- verified from source code (base_url handling, schema vs runtime validation, provider name restrictions)

**Research date:** 2026-03-26
**Valid until:** 2026-04-25 (stable -- upstream Bifrost v1.4.16 is pinned)
