package mcptools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

func listProvidersTool() Tool {
	return Tool{
		name:        "list_providers",
		description: "Configured providers and how many keys each has. Never returns key material.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "search": {"type": "string"}
  }
}`,
		noLogs: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			search, _, err := optionalStringArg(args, "search")
			if err != nil {
				return nil, err
			}
			rows, err := gov.GetProviders(ctx)
			if err != nil {
				return nil, fmt.Errorf("list providers failed: %w", err)
			}
			out := make([]map[string]any, 0, len(rows))
			for i := range rows {
				name := rows[i].Name
				if search != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(search)) {
					continue
				}
				// The provider rows come without their keys, so they are
				// counted from the provider's own key read.
				keys, err := gov.GetProviderKeys(ctx, schemas.ModelProvider(name))
				if err != nil && !errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("list keys for provider %q failed: %w", name, err)
				}
				item := map[string]any{
					"name":      name,
					"key_count": len(keys),
					"status":    rows[i].Status,
				}
				if rows[i].Description != "" {
					item["description"] = rows[i].Description
				}
				out = append(out, item)
			}
			return map[string]any{"providers": out, "returned": len(out)}, nil
		},
	}
}

func addProviderTool() Tool {
	return Tool{
		name:        "add_provider",
		description: "Register a provider (openai, anthropic, ...). Add keys afterwards with create_provider_key. Does not delete an existing provider.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "provider": {"type": "string", "minLength": 1, "description": "Provider name, e.g. openai."}
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
			if _, err := callAdmin(ctx, deps, http.MethodPost, "/api/providers", adminBody{"provider": name}); err != nil {
				return nil, err
			}
			return map[string]any{"provider": name, "live": true}, nil
		},
	}
}

func listProviderKeysTool() Tool {
	return Tool{
		name:        "list_provider_keys",
		description: "Keys configured for one provider. Returns id, name, models and enabled — never the secret value.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "provider": {"type": "string", "minLength": 1}
  },
  "required": ["provider"]
}`,
		noLogs: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			name, err := stringArg(args, "provider")
			if err != nil {
				return nil, err
			}
			keys, err := gov.GetProviderKeys(ctx, schemas.ModelProvider(name))
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no provider named %q", name)
				}
				return nil, fmt.Errorf("list provider keys failed: %w", err)
			}
			out := make([]map[string]any, 0, len(keys))
			for i := range keys {
				out = append(out, projectProviderKey(&keys[i]))
			}
			return map[string]any{"provider": name, "keys": out, "returned": len(out)}, nil
		},
	}
}

func createProviderKeyTool() Tool {
	return Tool{
		name: "create_provider_key",
		description: "Add a key to a provider. The secret goes in and is never returned. Names must be unique across providers. " +
			"Most providers take value (the API key). Bedrock (or a custom provider built on it) can authenticate with bedrock_key_config instead, and Vertex with vertex_key_config using the gateway's default Google credentials; value may then be omitted. " +
			"A key that names where it dials - an OAuth-minted key (oauth_key_config), an Azure endpoint, an Ollama/SGL/vLLM URL, a Databricks workspace, Vertex auth_credentials or Bedrock endpoints - needs an authenticated admin session, which a tool call is not, so create those in the dashboard with authentication enabled.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "provider": {"type": "string", "minLength": 1},
    "name": {"type": "string", "minLength": 1},
    "value": {"type": "string", "minLength": 1, "description": "The provider API key. Stored, never returned. Optional with bedrock_key_config or vertex_key_config."},
    "weight": {"type": "number", "minimum": 0, "description": "Load-balancing weight. Defaults to 1; 0 keeps the key but never selects it."},
    "bedrock_key_config": {"type": "object", "description": "AWS credentials for a Bedrock key. Stored, never returned.", "properties": {
      "access_key": {"type": "string"},
      "secret_key": {"type": "string"},
      "session_token": {"type": "string"},
      "region": {"type": "string"},
      "arn": {"type": "string"},
      "role_arn": {"type": "string"},
      "external_id": {"type": "string"},
      "session_name": {"type": "string"},
      "batch_role_arn": {"type": "string"}
    }},
    "vertex_key_config": {"type": "object", "description": "A Vertex key that uses the gateway's default Google credentials.", "properties": {
      "project_id": {"type": "string"},
      "project_number": {"type": "string"},
      "region": {"type": "string"},
      "force_single_region": {"type": "boolean"}
    }, "required": ["project_id", "region"]}
  },
  "required": ["provider", "name"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			providerName, err := stringArg(args, "provider")
			if err != nil {
				return nil, err
			}
			if _, err := stringArg(args, "name"); err != nil {
				return nil, err
			}
			for _, field := range dialingKeyConfigs {
				if value, present := args[field]; present && value != nil {
					return nil, fmt.Errorf("%s names where the key dials, which the key route only accepts from an authenticated admin session - a tool call is not one; create this key in the dashboard with authentication enabled", field)
				}
			}
			if err := keyWeightArg(args); err != nil {
				return nil, err
			}
			path, err := adminPath("/api/providers/{provider}/keys", providerName)
			if err != nil {
				return nil, err
			}
			body := adminBody{"weight": 1.0}.copySet(args, "name", "value", "weight")
			for field, allowed := range keyConfigFields {
				config, ok, err := keyConfigArg(args, field, allowed)
				if err != nil {
					return nil, err
				}
				if ok {
					body[field] = config
				}
			}
			_, hasValue := body["value"]
			_, hasBedrock := body["bedrock_key_config"]
			_, hasVertex := body["vertex_key_config"]
			if !hasValue && !hasBedrock && !hasVertex {
				return nil, fmt.Errorf("value is required, unless the key authenticates with bedrock_key_config or vertex_key_config")
			}
			response, err := callAdmin(ctx, deps, http.MethodPost, path, body)
			if err != nil {
				return nil, err
			}
			var key schemas.Key
			if err := decodeAdmin(response, &key); err != nil {
				return nil, err
			}
			return map[string]any{
				"provider": providerName,
				"key":      projectProviderKey(&key),
				"note":     "the secret was stored and will never be returned",
			}, nil
		},
	}
}

// dialingKeyConfigs are the key configs whose fields name a host the key's
// credentials are sent to. The key route refuses setting one without a
// genuine admin session, and a tool call never has one.
var dialingKeyConfigs = []string{"oauth_key_config", "azure_key_config", "ollama_key_config", "sgl_key_config", "vllm_key_config", "databricks_key_config", "github_copilot_key_config"}

// keyConfigFields are the key configs create_provider_key takes, and the
// fields of each it sends. Anything else is refused rather than dropped.
var keyConfigFields = map[string]map[string]string{
	"bedrock_key_config": {
		"access_key": "string", "secret_key": "string", "session_token": "string", "region": "string", "arn": "string",
		"role_arn": "string", "external_id": "string", "session_name": "string", "batch_role_arn": "string",
	},
	"vertex_key_config": {"project_id": "string", "project_number": "string", "region": "string", "force_single_region": "bool"},
}

// keyConfigArg reads one key config object field by field.
func keyConfigArg(args map[string]any, field string, allowed map[string]string) (map[string]any, bool, error) {
	object, ok, err := optionalObjectArg(args, field)
	if err != nil || !ok {
		return nil, false, err
	}
	out := make(map[string]any, len(object))
	for key := range object {
		kind, known := allowed[key]
		if !known {
			return nil, false, fmt.Errorf("%s has unknown field %q", field, key)
		}
		switch kind {
		case "bool":
			flag, err := optionalBoolArg(object, key)
			if err != nil {
				return nil, false, fmt.Errorf("%s.%w", field, err)
			}
			if flag != nil {
				out[key] = *flag
			}
		default:
			text, set, err := optionalStringArg(object, key)
			if err != nil {
				return nil, false, fmt.Errorf("%s.%w", field, err)
			}
			if set {
				out[key] = text
			}
		}
	}
	return out, true, nil
}

// keyWeightArg checks weight the way the key route does: finite, not negative.
func keyWeightArg(args map[string]any) error {
	value, present := args["weight"]
	if !present || value == nil {
		return nil
	}
	weight, ok := value.(float64)
	if !ok {
		return fmt.Errorf("weight must be a number, got %T", value)
	}
	if weight < 0 || math.IsNaN(weight) || math.IsInf(weight, 0) {
		return fmt.Errorf("weight must be a finite number of at least 0, got %v", weight)
	}
	return nil
}

func updateProviderKeyTool() Tool {
	return Tool{
		name:        "update_provider_key",
		description: "Rename a provider key, change its weight, enable or disable it, or replace its secret. Fields you do not name keep their stored values. The new secret is never returned.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "provider": {"type": "string", "minLength": 1},
    "key_id": {"type": "string", "minLength": 1},
    "name": {"type": "string"},
    "value": {"type": "string", "description": "Replacement secret. Stored, never returned."},
    "weight": {"type": "number", "minimum": 0},
    "enabled": {"type": "boolean"}
  },
  "required": ["provider", "key_id"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			providerName, err := stringArg(args, "provider")
			if err != nil {
				return nil, err
			}
			keyID, err := stringArg(args, "key_id")
			if err != nil {
				return nil, err
			}
			if err := keyWeightArg(args); err != nil {
				return nil, err
			}
			path, err := adminPath("/api/providers/{provider}/keys/{key_id}", providerName, keyID)
			if err != nil {
				return nil, err
			}
			keys, err := gov.GetProviderKeys(ctx, schemas.ModelProvider(providerName))
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no provider named %q", providerName)
				}
				return nil, fmt.Errorf("list provider keys failed: %w", err)
			}
			var stored *schemas.Key
			for i := range keys {
				if keys[i].ID == keyID {
					stored = &keys[i]
					break
				}
			}
			if stored == nil {
				return nil, fmt.Errorf("no key %q on provider %q", keyID, providerName)
			}
			// The route is a patch: a field the body leaves out keeps its
			// stored value, so only what the caller named is sent. The name is
			// the exception - the route does not carry it over when absent -
			// so it goes as stored unless renamed.
			body := adminBody{"name": stored.Name}.copySet(args, "name", "value", "weight", "enabled")
			response, err := callAdmin(ctx, deps, http.MethodPut, path, body)
			if err != nil {
				return nil, err
			}
			var key schemas.Key
			if err := decodeAdmin(response, &key); err != nil {
				return nil, err
			}
			return map[string]any{"provider": providerName, "key": projectProviderKey(&key)}, nil
		},
	}
}

func updateProviderTool() Tool {
	return Tool{
		name:        "update_provider",
		description: "Update a provider's concurrency or buffer size. Does not manage keys — use create_provider_key / update_provider_key.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "provider": {"type": "string", "minLength": 1},
    "concurrency": {"type": "integer", "minimum": 1, "description": "Worker pool size."},
    "buffer_size": {"type": "integer", "minimum": 1, "description": "Request queue buffer; must be at least concurrency."}
  },
  "required": ["provider"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			name, err := stringArg(args, "provider")
			if err != nil {
				return nil, err
			}
			path, err := adminPath("/api/providers/{provider}", name)
			if err != nil {
				return nil, err
			}
			// Looked up first: the route creates a provider it does not find.
			config, err := gov.GetProviderConfig(ctx, schemas.ModelProvider(name))
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no provider named %q", name)
				}
				return nil, fmt.Errorf("get provider config failed: %w", err)
			}
			if config == nil {
				config = &configstore.ProviderConfig{}
			}
			concurrency, err := optionalIntArg(args, "concurrency")
			if err != nil {
				return nil, err
			}
			buffer, err := optionalIntArg(args, "buffer_size")
			if err != nil {
				return nil, err
			}
			// The route takes concurrency and buffer size as a pair, so the
			// half the caller did not name is sent as stored. Whether the pair
			// is valid is the route's to say.
			sizes := schemas.DefaultConcurrencyAndBufferSize
			if config.ConcurrencyAndBufferSize != nil {
				sizes = *config.ConcurrencyAndBufferSize
			}
			if concurrency != nil {
				sizes.Concurrency = *concurrency
			}
			if buffer != nil {
				sizes.BufferSize = *buffer
			}
			// The route also replaces the network config with what it is sent,
			// refuses a custom provider sent without its custom config, and
			// takes the two send-back-raw flags only from the body (absent
			// reads as off), so all of these go back as a read returns them.
			// The other blocks are left out, which the route reads as "leave
			// alone".
			redacted := config.Redacted()
			body := adminBody{
				"concurrency_and_buffer_size": sizes,
				"send_back_raw_request":       config.SendBackRawRequest,
				"send_back_raw_response":      config.SendBackRawResponse,
			}
			if redacted.NetworkConfig != nil {
				body["network_config"] = redacted.NetworkConfig
			}
			if redacted.CustomProviderConfig != nil {
				body["custom_provider_config"] = redacted.CustomProviderConfig
			}
			if _, err := callAdmin(ctx, deps, http.MethodPut, path, body); err != nil {
				return nil, err
			}
			return map[string]any{"provider": name, "concurrency": sizes.Concurrency, "buffer_size": sizes.BufferSize}, nil
		},
	}
}

func projectProviderKey(key *schemas.Key) map[string]any {
	out := map[string]any{
		"id":     key.ID,
		"name":   key.Name,
		"weight": key.Weight,
	}
	if key.Enabled != nil {
		out["enabled"] = *key.Enabled
	}
	if len(key.Models) > 0 {
		out["models"] = []string(key.Models)
	}
	if key.Status != "" {
		out["status"] = key.Status
	}
	return out
}
