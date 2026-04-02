- feat: add support for chaining routing rules
- feat: add routing tree UI to better visualize routing rules
- feat: add model alias — keys now support a top-level `aliases` field mapping any model name to a provider-specific identifier (Azure deployment names, Bedrock inference profile ARNs, Vertex endpoints, Replicate model slugs, fine-tuned model IDs, etc.). The original model name is preserved and returned alongside the resolved identifier in every response. Breaking changes: see below.

<Warning>
**This release contains 4 breaking changes** related to model aliasing. See the [v1.5.0 Migration Guide](/migration-guides/v1.5.0#breaking-change-9-provider-deployments-removed-migrate-to-aliases) for full before/after examples and migration instructions.
</Warning>

| # | Breaking Change | Affected |
|---|---|---|
| [9](/migration-guides/v1.5.0#breaking-change-9-provider-deployments-removed-migrate-to-aliases) | `deployments` removed from `azure_key_config`, `vertex_key_config`, `bedrock_key_config`, `replicate_key_config` — use top-level `aliases` | `config.json` |
| [9](/migration-guides/v1.5.0#breaking-change-9-provider-deployments-removed-migrate-to-aliases) | `replicate_key_config.deployments` replaced by `replicate_key_config.use_deployments_endpoint` (bool) | `config.json` |
| [10](/migration-guides/v1.5.0#breaking-change-10-go-sdk-extrafields-model-fields-renamed) | `BifrostResponseExtraFields.ModelRequested` → `OriginalModelRequested` + `ResolvedModelUsed` | Go SDK |
| [11](/migration-guides/v1.5.0#breaking-change-11-go-sdk-streamaccumulatorresult-field-renamed) | `StreamAccumulatorResult.Model` → `RequestedModel` + `ResolvedModel` | Go SDK |
