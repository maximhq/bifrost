package mcptools

import (
	"context"
	"fmt"
	"net/http"

	"github.com/maximhq/bifrost/core/schemas"
)

func describeModelTool() Tool {
	return Tool{
		name:        "describe_model",
		description: "Catalog metadata for one model: context length, token limits, listed cost fields and whether it is deprecated. A custom provider is looked up by the provider type it is built on (base_provider), and a key alias by the model it stands for (alias_of).",
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
			// The catalog knows providers by their own names and models by
			// theirs: a custom provider is looked up by the provider type it
			// is built on, and a key alias by the model it stands for.
			base := schemas.ModelProvider(provider)
			if deps.Governance != nil {
				if config, err := deps.Governance.GetProviderConfig(ctx, base); err == nil && config != nil &&
					config.CustomProviderConfig != nil && config.CustomProviderConfig.BaseProviderType != "" {
					base = config.CustomProviderConfig.BaseProviderType
				}
			}
			aliasOf := ""
			if owner, ok := deps.ModelCatalog.ResolveAlias(schemas.ModelProvider(provider), model); ok {
				switch {
				case owner.Config.ModelName != nil && *owner.Config.ModelName != "":
					aliasOf = *owner.Config.ModelName
				case owner.Config.ModelID != "":
					aliasOf = owner.Config.ModelID
				}
			}
			var info *schemas.Model
			if aliasOf != "" {
				info = deps.ModelCatalog.GetModelInfo(base, aliasOf)
			}
			if info == nil {
				info = deps.ModelCatalog.GetModelInfo(base, model)
				if info != nil {
					aliasOf = ""
				}
			}
			if info == nil {
				return nil, fmt.Errorf("no catalog entry for %s/%s", provider, model)
			}
			out := map[string]any{
				"name":     model,
				"provider": provider,
			}
			if base != schemas.ModelProvider(provider) {
				out["base_provider"] = string(base)
			}
			if aliasOf != "" {
				out["alias_of"] = aliasOf
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
		description: "Re-run list-models discovery for a provider (or one of its keys) so the catalog picks up newly listed models. Reports each key's discovery status; a keyless provider reports its own status and error instead.",
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
			name, err := stringArg(args, "provider")
			if err != nil {
				return nil, err
			}
			keyID, hasKey, err := optionalStringArg(args, "key_id")
			if err != nil {
				return nil, err
			}
			if hasKey {
				path, err := adminPath("/api/providers/{provider}/keys/{key_id}/refresh-models", name, keyID)
				if err != nil {
					return nil, err
				}
				response, err := callAdmin(ctx, deps, http.MethodPost, path, nil)
				if err != nil {
					return nil, err
				}
				var key schemas.Key
				if err := decodeAdmin(response, &key); err != nil {
					return nil, err
				}
				return map[string]any{"provider": name, "key_id": keyID, "refreshed": true, "key": projectProviderKey(&key)}, nil
			}
			path, err := adminPath("/api/providers/{provider}/refresh-models", name)
			if err != nil {
				return nil, err
			}
			response, err := callAdmin(ctx, deps, http.MethodPost, path, nil)
			if err != nil {
				return nil, err
			}
			var keys []schemas.Key
			if err := decodeAdminField(response, "keys", &keys); err != nil {
				return nil, err
			}
			out := make([]map[string]any, 0, len(keys))
			for i := range keys {
				out = append(out, projectProviderKey(&keys[i]))
			}
			result := map[string]any{"provider": name, "refreshed": true, "keys": out}
			// A keyless provider discovers without a key and records the
			// outcome on the provider, so with no keys to report the
			// provider's own status is the answer.
			if len(keys) == 0 && deps.Governance != nil {
				if config, err := deps.Governance.GetProviderConfig(ctx, schemas.ModelProvider(name)); err == nil && config != nil {
					if config.Status != "" {
						result["status"] = config.Status
					}
					if config.Description != "" {
						result["description"] = config.Description
					}
				}
			}
			return result, nil
		},
	}
}
