package mcptools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/framework/configstore/tables"
)

func getWarpConfigTool() Tool {
	return Tool{
		name:        "get_warp_config",
		description: "Warp dashboard-agent settings: enabled, model, embedding, semantic-search knobs. Never returns credentials — only key ids.",
		schemaJSON: `{
  "type": "object",
  "properties": {}
}`,
		noLogs: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			if deps.Warp == nil {
				return nil, fmt.Errorf("warp configuration is not available on this deployment")
			}
			row, err := deps.Warp.GetWarpConfig(ctx)
			if err != nil {
				return nil, fmt.Errorf("get warp config failed: %w", err)
			}
			if row == nil {
				return map[string]any{"configured": false}, nil
			}
			return projectWarpConfig(row), nil
		},
	}
}

func updateWarpConfigTool() Tool {
	return Tool{
		name:        "update_warp_config",
		description: "Update Warp settings. api_key_id and embedding_api_key_id are references to configured provider keys, not secrets. Changing the embedding provider, model or dimension requires a new log_vector_store_namespace.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "enabled": {"type": "boolean"},
    "provider": {"type": "string"},
    "model": {"type": "string"},
    "api_key_id": {"type": "string"},
    "base_url": {"type": "string"},
    "max_iterations": {"type": "integer", "minimum": 1},
    "request_timeout_seconds": {"type": "integer", "minimum": 1},
    "embedding_provider": {"type": "string"},
    "embedding_model": {"type": "string"},
    "embedding_api_key_id": {"type": "string"},
    "embedding_dimension": {"type": "integer", "minimum": 1, "description": "Vector size the embedding model produces."},
    "log_vector_store_namespace": {"type": "string", "description": "Vector store namespace for log embeddings. Must change whenever the embedding provider, model or dimension changes."},
    "semantic_search_threshold": {"type": "number"},
    "semantic_search_limit": {"type": "integer", "minimum": 1}
  }
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			if deps.Warp == nil {
				return nil, fmt.Errorf("warp configuration is not available on this deployment")
			}
			row, err := deps.Warp.GetWarpConfig(ctx)
			if err != nil {
				return nil, fmt.Errorf("get warp config failed: %w", err)
			}
			if row == nil {
				row = &tables.TableWarpConfig{}
			}
			if flag, err := optionalBoolArg(args, "enabled"); err != nil {
				return nil, err
			} else if flag != nil {
				row.Enabled = *flag
			}
			if v, ok, err := optionalStringArg(args, "provider"); err != nil {
				return nil, err
			} else if ok {
				row.Provider = v
			}
			if v, ok, err := optionalStringArg(args, "model"); err != nil {
				return nil, err
			} else if ok {
				row.Model = v
			}
			if v, ok, err := optionalStringArg(args, "api_key_id"); err != nil {
				return nil, err
			} else if ok {
				row.APIKeyID = v
			}
			if v, ok, err := optionalStringArg(args, "base_url"); err != nil {
				return nil, err
			} else if ok {
				row.BaseURL = v
			}
			if n, err := optionalIntArg(args, "max_iterations"); err != nil {
				return nil, err
			} else if n != nil {
				row.MaxIterations = *n
			}
			if n, err := optionalIntArg(args, "request_timeout_seconds"); err != nil {
				return nil, err
			} else if n != nil {
				row.RequestTimeoutSeconds = *n
			}
			if v, ok, err := optionalStringArg(args, "embedding_provider"); err != nil {
				return nil, err
			} else if ok {
				row.EmbeddingProvider = v
			}
			if v, ok, err := optionalStringArg(args, "embedding_model"); err != nil {
				return nil, err
			} else if ok {
				row.EmbeddingModel = v
			}
			if v, ok, err := optionalStringArg(args, "embedding_api_key_id"); err != nil {
				return nil, err
			} else if ok {
				row.EmbeddingAPIKeyID = v
			}
			if n, err := optionalIntArg(args, "embedding_dimension"); err != nil {
				return nil, err
			} else if n != nil {
				if *n < 1 {
					return nil, fmt.Errorf("embedding_dimension must be at least 1, got %d", *n)
				}
				row.EmbeddingDimension = *n
			}
			if v, ok, err := optionalStringArg(args, "log_vector_store_namespace"); err != nil {
				return nil, err
			} else if ok {
				row.LogVectorStoreNamespace = v
			}
			if value, present := args["semantic_search_threshold"]; present && value != nil {
				n, err := floatArg(args, "semantic_search_threshold")
				if err != nil {
					return nil, err
				}
				row.SemanticSearchThreshold = n
			}
			if n, err := optionalIntArg(args, "semantic_search_limit"); err != nil {
				return nil, err
			} else if n != nil {
				row.SemanticSearchLimit = *n
			}
			// Saved through Warp's own validation, as the dashboard does: a
			// direct upsert accepted a base_url with credentials in it or of
			// any scheme, enabled Warp with no vector store, and changed the
			// embedding model inside an existing namespace, mixing two vector
			// spaces. The body is the stored row with this patch applied;
			// embedding fields are named only when the caller set them, so
			// the rest are filled from the stored row, not reset.
			if deps.WarpConfig == nil {
				return nil, fmt.Errorf("warp configuration cannot be validated on this deployment, so it is not changed here")
			}
			body := map[string]any{
				"enabled":                 row.Enabled,
				"provider":                row.Provider,
				"model":                   row.Model,
				"base_url":                row.BaseURL,
				"api_key_id":              row.APIKeyID,
				"max_iterations":          row.MaxIterations,
				"request_timeout_seconds": row.RequestTimeoutSeconds,
				"history_retention_days":  row.HistoryRetentionDays,
				"reasoning_effort":        row.ReasoningEffort,
			}
			if row.SystemPromptSuffix != nil {
				body["system_prompt_suffix"] = *row.SystemPromptSuffix
			}
			if row.Temperature != nil {
				body["temperature"] = *row.Temperature
			}
			for key, value := range map[string]any{
				"embedding_provider":         row.EmbeddingProvider,
				"embedding_model":            row.EmbeddingModel,
				"embedding_api_key_id":       row.EmbeddingAPIKeyID,
				"embedding_dimension":        row.EmbeddingDimension,
				"log_vector_store_namespace": row.LogVectorStoreNamespace,
				"semantic_search_threshold":  row.SemanticSearchThreshold,
				"semantic_search_limit":      row.SemanticSearchLimit,
			} {
				// A null counts as unset, as it does for every optional arg above.
				if sent, set := args[key]; set && sent != nil {
					body[key] = value
				}
			}
			encoded, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			if err := deps.WarpConfig.SaveConfigJSON(ctx, encoded); err != nil {
				return nil, fmt.Errorf("update warp config failed: %w", err)
			}
			if saved, err := deps.Warp.GetWarpConfig(ctx); err == nil && saved != nil {
				row = saved
			}
			return projectWarpConfig(row), nil
		},
	}
}

func projectWarpConfig(row *tables.TableWarpConfig) map[string]any {
	out := map[string]any{
		"configured":                true,
		"enabled":                   row.Enabled,
		"provider":                  row.Provider,
		"model":                     row.Model,
		"max_iterations":            row.MaxIterations,
		"request_timeout_seconds":   row.RequestTimeoutSeconds,
		"embedding_provider":        row.EmbeddingProvider,
		"embedding_model":           row.EmbeddingModel,
		"embedding_dimension":       row.EmbeddingDimension,
		"semantic_search_threshold": row.SemanticSearchThreshold,
		"semantic_search_limit":     row.SemanticSearchLimit,
	}
	if row.APIKeyID != "" {
		out["api_key_id"] = row.APIKeyID
	}
	if row.EmbeddingAPIKeyID != "" {
		out["embedding_api_key_id"] = row.EmbeddingAPIKeyID
	}
	if row.BaseURL != "" {
		out["base_url"] = row.BaseURL
	}
	if row.LogVectorStoreNamespace != "" {
		out["log_vector_store_namespace"] = row.LogVectorStoreNamespace
	}
	return out
}
