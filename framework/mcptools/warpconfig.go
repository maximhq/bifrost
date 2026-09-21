package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/maximhq/bifrost/framework/configstore/tables"
)

func getWarpConfigTool() Tool {
	return Tool{
		name:        "get_warp_config",
		description: "Warp dashboard-agent settings: enabled, model, the additional models the panel may switch to, embedding, semantic-search knobs. Never returns credentials — only key ids.",
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
		name: "update_warp_config",
		description: "Update Warp settings. api_key_id and embedding_api_key_id are references to configured provider keys, not secrets. Changing the embedding provider, model or dimension requires a new log_vector_store_namespace. " +
			"additional_models replaces the whole list of extra models the panel may switch to: pass every model that should remain (get_warp_config lists them), or [] to remove them all; omit it to keep the stored list.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "enabled": {"type": "boolean"},
    "provider": {"type": "string"},
    "model": {"type": "string"},
    "api_key_id": {"type": "string"},
    "additional_models": {
      "type": "array",
      "description": "The whole list of other models the panel may switch to; replaces the stored list. Omit to keep it, [] to clear it.",
      "items": {
        "type": "object",
        "properties": {
          "provider": {"type": "string"},
          "model": {"type": "string"},
          "api_key_id": {"type": "string", "description": "One of the provider's configured key ids; omit for a provider that needs none."}
        },
        "required": ["provider", "model"]
      }
    },
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
			// The route replaces the list whole and reads an omitted one as
			// none, so the stored list is sent unless the caller replaced it.
			additional, err := storedWarpModels(row)
			if err != nil {
				return nil, err
			}
			if value, present := args["additional_models"]; present && value != nil {
				if additional, err = warpModelsArg(value); err != nil {
					return nil, err
				}
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
			// Sent to the route the dashboard saves through, which is where
			// Warp's validation runs: an unknown provider or key, enabling
			// Warp with no vector store, and a changed embedding model inside
			// an existing namespace are all refused there. The route takes a
			// whole configuration, so the body is the stored row with this
			// patch applied; embedding fields are named only when the caller
			// set them, so the rest are filled from the stored row, not reset.
			body := adminBody{
				"enabled":                 row.Enabled,
				"provider":                row.Provider,
				"model":                   row.Model,
				"api_key_id":              row.APIKeyID,
				"additional_models":       additional,
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
			if _, err := callAdmin(ctx, deps, http.MethodPut, "/api/warp/config", body); err != nil {
				return nil, err
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
	if row.LogVectorStoreNamespace != "" {
		out["log_vector_store_namespace"] = row.LogVectorStoreNamespace
	}
	if models, err := storedWarpModels(row); err == nil && len(models) > 0 {
		out["additional_models"] = models
	}
	return out
}

// storedWarpModels decodes the row's additional_models column into the shape
// the route takes, never nil so the body always carries a list.
func storedWarpModels(row *tables.TableWarpConfig) ([]map[string]any, error) {
	out := []map[string]any{}
	if row == nil || row.AdditionalModels == nil || strings.TrimSpace(*row.AdditionalModels) == "" {
		return out, nil
	}
	var models []struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		APIKeyID string `json:"api_key_id"`
	}
	if err := json.Unmarshal([]byte(*row.AdditionalModels), &models); err != nil {
		// Saving over an unreadable list would silently drop it.
		return nil, fmt.Errorf("the stored additional_models could not be read, so an update would lose them: %w", err)
	}
	for _, m := range models {
		out = append(out, warpModel(m.Provider, m.Model, m.APIKeyID))
	}
	return out, nil
}

// warpModelsArg reads an additional_models argument field by field.
func warpModelsArg(value any) ([]map[string]any, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("additional_models must be an array of {provider, model, api_key_id}")
	}
	out := make([]map[string]any, 0, len(items))
	for i, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("additional_models[%d] must be an object", i)
		}
		fields := make([]string, 3)
		for j, key := range []string{"provider", "model", "api_key_id"} {
			v, _, err := optionalStringArg(entry, key)
			if err != nil {
				return nil, fmt.Errorf("additional_models[%d]: %w", i, err)
			}
			fields[j] = strings.TrimSpace(v)
		}
		if fields[0] == "" || fields[1] == "" {
			return nil, fmt.Errorf("additional_models[%d] needs a provider and a model", i)
		}
		out = append(out, warpModel(fields[0], fields[1], fields[2]))
	}
	return out, nil
}

func warpModel(provider, model, keyID string) map[string]any {
	m := map[string]any{"provider": provider, "model": model}
	if keyID != "" {
		m["api_key_id"] = keyID
	}
	return m
}
