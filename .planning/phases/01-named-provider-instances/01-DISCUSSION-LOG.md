# Phase 1: Named Provider Instances - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-03-28
**Phase:** 01-named-provider-instances
**Areas discussed:** Provider naming, Config validation, Rollout strategy
**Mode:** Auto (all decisions auto-selected)

---

## Provider Naming Convention

| Option | Description | Selected |
|--------|-------------|----------|
| Service-descriptive names | e.g., llama-cpp-bf16, fireworks, together — maps 1:1 with services | ✓ |
| Generic names | e.g., local-1, cloud-1 — abstract from service details | |
| Model-based names | e.g., qwen3-coder, deepseek-r1 — named after primary model | |

**User's choice:** [auto] Service-descriptive names (recommended default)
**Notes:** Clear mapping to upstream K8s services makes debugging easier

---

## Config Validation Approach

| Option | Description | Selected |
|--------|-------------|----------|
| Test via Bifrost API | curl each named provider on running dev instance before committing | ✓ |
| Kustomize build only | Validate YAML structure locally, trust Bifrost to handle the rest | |

**User's choice:** [auto] Test via Bifrost API (recommended default)
**Notes:** CustomProviderConfig with is_key_less needs hands-on validation

---

## Rollout Strategy

| Option | Description | Selected |
|--------|-------------|----------|
| Dev first, then prod | Validate routing in dev before touching prod | ✓ |
| Dev + prod simultaneously | Apply to both at once for faster rollout | |

**User's choice:** [auto] Dev first, then prod (recommended default)
**Notes:** Self-hosted models only exist in prod — dev validates config parsing, prod validates actual routing

---

## Claude's Discretion

- Model name strings
- Network timeout values per provider
- Exact base_url paths

## Deferred Ideas

None
