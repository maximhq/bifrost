package mcptools

import (
	"context"
	"errors"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

func describeModelTool() Tool {
	return Tool{
		name:        "describe_model",
		description: "Catalog metadata for one model: context length, token limits, listed cost fields and whether it is deprecated.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "provider": {"type": "string", "minLength": 1},
    "model": {"type": "string", "minLength": 1}
  },
  "required": ["provider", "model"]
}`,
		noLogs:       true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			if deps.ModelCatalog == nil {
				return nil, fmt.Errorf("model catalog is not available on this deployment")
			}
			provider, err := stringArg(args, "provider")
			if err != nil {
				return nil, err
			}
			model, err := stringArg(args, "model")
			if err != nil {
				return nil, err
			}
			info := deps.ModelCatalog.GetModelInfo(schemas.ModelProvider(provider), model)
			if info == nil {
				return nil, fmt.Errorf("no catalog entry for %s/%s", provider, model)
			}
			out := map[string]any{
				"name":     info.ID,
				"provider": provider,
			}
			if info.ContextLength != nil {
				out["context_length"] = *info.ContextLength
			}
			if info.MaxInputTokens != nil {
				out["max_input_tokens"] = *info.MaxInputTokens
			}
			if info.MaxOutputTokens != nil {
				out["max_output_tokens"] = *info.MaxOutputTokens
			}
			if info.IsDeprecated {
				out["is_deprecated"] = true
			}
			if info.Pricing != nil {
				pricing := map[string]any{}
				if info.Pricing.Prompt != nil {
					pricing["input_cost_per_token"] = *info.Pricing.Prompt
				}
				if info.Pricing.Completion != nil {
					pricing["output_cost_per_token"] = *info.Pricing.Completion
				}
				if info.Pricing.InputCacheWrite != nil {
					pricing["cache_creation_input_token_cost"] = *info.Pricing.InputCacheWrite
				}
				if info.Pricing.InputCacheRead != nil {
					pricing["cache_read_input_token_cost"] = *info.Pricing.InputCacheRead
				}
				if len(pricing) > 0 {
					out["pricing"] = pricing
				}
			}
			if len(info.AdditionalAttributes) > 0 {
				out["additional_attributes"] = info.AdditionalAttributes
			}
			return out, nil
		},
	}
}

func refreshProviderModelsTool() Tool {
	return Tool{
		name:        "refresh_provider_models",
		description: "Re-run list-models discovery for a provider (or one of its keys) so the catalog picks up newly listed models.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "provider": {"type": "string", "minLength": 1},
    "key_id": {"type": "string", "description": "Refresh this key only. Omit to refresh every enabled key."}
  },
  "required": ["provider"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			if deps.ModelsRuntime == nil {
				return nil, fmt.Errorf("model refresh is not available on this deployment")
			}
			name, err := stringArg(args, "provider")
			if err != nil {
				return nil, err
			}
			provider := schemas.ModelProvider(name)
			if gov := deps.Governance; gov != nil {
				if _, err := gov.GetProviderConfig(ctx, provider); err != nil {
					if errors.Is(err, configstore.ErrNotFound) {
						return nil, fmt.Errorf("no provider named %q", name)
					}
					return nil, fmt.Errorf("get provider failed: %w", err)
				}
			}
			keyID, hasKey, err := optionalStringArg(args, "key_id")
			if err != nil {
				return nil, err
			}
			if hasKey {
				if err := deps.ModelsRuntime.RefreshLiveModelsForKey(ctx, provider, keyID); err != nil {
					return nil, fmt.Errorf("refresh provider models failed: %w", err)
				}
				return map[string]any{"provider": name, "key_id": keyID, "refreshed": true}, nil
			}
			if err := deps.ModelsRuntime.RefreshLiveModelsForAllKeys(ctx, provider); err != nil {
				return nil, fmt.Errorf("refresh provider models failed: %w", err)
			}
			return map[string]any{"provider": name, "refreshed": true}, nil
		},
	}
}
