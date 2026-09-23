package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

func listPluginsTool() Tool {
	return Tool{
		name:        "list_plugins",
		description: "Configured plugins (name, enabled, custom, placement). Use describe_plugin for config.",
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
			rows, err := gov.GetPlugins(ctx)
			if err != nil {
				return nil, fmt.Errorf("list plugins failed: %w", err)
			}
			out := make([]map[string]any, 0, len(rows))
			for _, row := range rows {
				if row == nil {
					continue
				}
				if search != "" && !strings.Contains(strings.ToLower(row.Name), strings.ToLower(search)) {
					continue
				}
				item := projectPlugin(row)
				delete(item, "config")
				out = append(out, item)
			}
			if len(out) > MaxGovernanceRows {
				out = out[:MaxGovernanceRows]
			}
			return map[string]any{"plugins": out, "returned": len(out)}, nil
		},
	}
}

func describePluginTool() Tool {
	return Tool{
		name:        "describe_plugin",
		description: "One plugin: enabled, placement, order and stored config. Credential-shaped config values (keys, tokens, secrets, passwords) are redacted; env.* references are shown as-is.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 1}
  },
  "required": ["name"]
}`,
		noLogs: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			name, err := stringArg(args, "name")
			if err != nil {
				return nil, err
			}
			row, err := gov.GetPlugin(ctx, name)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no plugin named %q", name)
				}
				return nil, fmt.Errorf("plugin lookup failed: %w", err)
			}
			return projectPlugin(row), nil
		},
	}
}

func createPluginTool() Tool {
	return Tool{
		name:        "create_plugin",
		description: "Register or configure a built-in plugin. placement is pre_builtin or post_builtin. Custom plugins (a filesystem path to load) cannot be registered over MCP; use the dashboard or config.json.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 1},
    "enabled": {"type": "boolean"},
    "config": {"type": "object"},
    "placement": {"type": "string", "enum": ["pre_builtin", "post_builtin"]},
    "order": {"type": "integer"}
  },
  "required": ["name"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			name, err := stringArg(args, "name")
			if err != nil {
				return nil, err
			}
			enabled := true
			if flag, err := optionalBoolArg(args, "enabled"); err != nil {
				return nil, err
			} else if flag != nil {
				enabled = *flag
			}
			config, hasConfig, err := optionalObjectArg(args, "config")
			if err != nil {
				return nil, err
			}
			if hasConfig {
				if config, err = normalizePluginConfig(deps, name, config); err != nil {
					return nil, err
				}
			}
			if err := rejectPluginPath(args); err != nil {
				return nil, err
			}
			placement, err := optionalPluginPlacement(args)
			if err != nil {
				return nil, err
			}
			order, err := optionalIntArg(args, "order")
			if err != nil {
				return nil, err
			}
			plugin := &tables.TablePlugin{
				Name:      name,
				Enabled:   enabled,
				Config:    config,
				Placement: placement,
				Order:     order,
			}
			if err := gov.CreatePlugin(ctx, plugin); err != nil {
				if errors.Is(err, configstore.ErrAlreadyExists) {
					return nil, fmt.Errorf("a plugin named %q already exists", name)
				}
				return nil, fmt.Errorf("create plugin failed: %w", err)
			}
			if err := applyPluginRuntime(ctx, deps, plugin); err != nil {
				return projectPlugin(plugin), err
			}
			return projectPlugin(plugin), nil
		},
	}
}

func updatePluginTool() Tool {
	return Tool{
		name:        "update_plugin",
		description: "Enable/disable a plugin or replace its config, placement or order. A plugin's path cannot be changed over MCP.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 1},
    "enabled": {"type": "boolean"},
    "config": {"type": "object"},
    "placement": {"type": "string", "enum": ["pre_builtin", "post_builtin"]},
    "order": {"type": "integer"}
  },
  "required": ["name"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			name, err := stringArg(args, "name")
			if err != nil {
				return nil, err
			}
			if err := rejectPluginPath(args); err != nil {
				return nil, err
			}
			plugin, err := gov.GetPlugin(ctx, name)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no plugin named %q", name)
				}
				return nil, fmt.Errorf("plugin lookup failed: %w", err)
			}
			if flag, err := optionalBoolArg(args, "enabled"); err != nil {
				return nil, err
			} else if flag != nil {
				plugin.Enabled = *flag
			}
			if config, ok, err := optionalObjectArg(args, "config"); err != nil {
				return nil, err
			} else if ok {
				incoming, err := restoreRedactedPluginConfig(config, plugin.Config)
				if err != nil {
					return nil, err
				}
				// Merged over the stored config at the top level, as the HTTP
				// handler does: a key the caller did not send (a setting made
				// from another screen, say) is kept, not dropped.
				merged := map[string]any{}
				if stored, ok := plugin.Config.(map[string]any); ok {
					maps.Copy(merged, stored)
				}
				maps.Copy(merged, incoming)
				normalized, err := normalizePluginConfig(deps, plugin.Name, merged)
				if err != nil {
					return nil, err
				}
				plugin.Config = normalized
			}
			if placement, err := optionalPluginPlacement(args); err != nil {
				return nil, err
			} else if placement != nil {
				plugin.Placement = placement
			}
			if order, err := optionalIntArg(args, "order"); err != nil {
				return nil, err
			} else if order != nil {
				plugin.Order = order
			}
			if err := gov.UpdatePlugin(ctx, plugin); err != nil {
				return nil, fmt.Errorf("update plugin failed: %w", err)
			}
			if err := applyPluginRuntime(ctx, deps, plugin); err != nil {
				return projectPlugin(plugin), err
			}
			return projectPlugin(plugin), nil
		},
	}
}

// applyPluginRuntime makes the live process match the stored row: an enabled
// plugin is (re)loaded, a disabled one is stopped. Reloading regardless of the
// flag started a plugin the caller had just switched off.
func applyPluginRuntime(ctx context.Context, deps *Deps, plugin *tables.TablePlugin) error {
	if deps.PluginRuntime == nil {
		return nil
	}
	if plugin.Enabled {
		if err := deps.PluginRuntime.ReloadPlugin(ctx, plugin.Name, plugin.Path, plugin.Config, plugin.Placement, plugin.Order); err != nil {
			return fmt.Errorf("stored but live reload failed: %w", err)
		}
		return nil
	}
	if err := deps.PluginRuntime.DisablePlugin(ctx, plugin.Name); err != nil {
		return fmt.Errorf("stored but stopping the live plugin failed: %w", err)
	}
	return nil
}

func projectPlugin(plugin *tables.TablePlugin) map[string]any {
	out := map[string]any{
		"name":      plugin.Name,
		"enabled":   plugin.Enabled,
		"is_custom": plugin.IsCustom,
	}
	if plugin.Path != nil {
		out["path"] = *plugin.Path
	}
	if plugin.Placement != nil {
		out["placement"] = string(*plugin.Placement)
	}
	if plugin.Order != nil {
		out["order"] = *plugin.Order
	}
	if plugin.Config != nil {
		out["config"] = redactPluginConfig(plugin.Config)
	}
	return out
}

// pluginSecretKeyParts mark a config key whose string value is a credential.
// Matched as substrings of the lowercased key, so api_key, apiKey, x-api-key
// and access_token all hit.
var pluginSecretKeyParts = []string{"key", "secret", "token", "password", "passwd", "credential", "auth", "cookie", "private", "signature", "dsn"}

func isPluginSecretKey(name string) bool {
	lower := strings.ToLower(name)
	for _, part := range pluginSecretKeyParts {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

// redactPluginConfig returns a copy of a plugin's stored config with the value
// under every credential-shaped key replaced. Plugin config is free-form, so it
// is normalised to plain JSON values first to walk nested objects and arrays.
// Numbers and booleans under such keys stay visible (max_tokens is a count, not
// a secret), as do env.* references, which name a variable rather than hold it.
func redactPluginConfig(config any) any {
	var generic any
	encoded, err := json.Marshal(config)
	if err != nil || json.Unmarshal(encoded, &generic) != nil {
		return schemas.RedactedAttrValue
	}
	return redactPluginValue(generic, false)
}

// restoreRedactedPluginConfig puts the stored secret back wherever an incoming
// config carries the redaction placeholder describe_plugin handed out, so a
// caller that edits one field of a described config does not overwrite every
// credential in it with the literal placeholder. A placeholder with no stored
// value at the same path is refused: there is nothing to restore it from.
func restoreRedactedPluginConfig(incoming map[string]any, stored any) (map[string]any, error) {
	var existing any
	if stored != nil {
		encoded, err := json.Marshal(stored)
		if err != nil || json.Unmarshal(encoded, &existing) != nil {
			existing = nil
		}
	}
	restored, err := restoreRedactedValue(incoming, existing, "config")
	if err != nil {
		return nil, err
	}
	return restored.(map[string]any), nil
}

func restoreRedactedValue(value, existing any, path string) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		prior, _ := existing.(map[string]any)
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			restored, err := restoreRedactedValue(child, prior[key], path+"."+key)
			if err != nil {
				return nil, err
			}
			out[key] = restored
		}
		return out, nil
	case []any:
		prior, _ := existing.([]any)
		// Restoration is by index, so it is only sound when the array lines
		// up with the stored one. A dropped, added or reordered item would
		// shift every later index and restore a secret into the wrong entry.
		if len(typed) != len(prior) && containsRedactedPlaceholder(typed) {
			return nil, fmt.Errorf("%s has %d items but the stored value has %d, so its %q placeholders cannot be matched to stored values; send the real values", path, len(typed), len(prior), schemas.RedactedAttrValue)
		}
		out := make([]any, len(typed))
		for i, child := range typed {
			var priorChild any
			if i < len(prior) {
				priorChild = prior[i]
			}
			restored, err := restoreRedactedValue(child, priorChild, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = restored
		}
		return out, nil
	case string:
		if typed != schemas.RedactedAttrValue {
			return typed, nil
		}
		if prior, ok := existing.(string); ok && prior != "" {
			return prior, nil
		}
		return nil, fmt.Errorf("%s is the redaction placeholder %q but no stored value exists to keep; send the real value", path, schemas.RedactedAttrValue)
	default:
		return typed, nil
	}
}

// containsRedactedPlaceholder reports whether the placeholder appears anywhere
// inside value, at any depth.
func containsRedactedPlaceholder(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			if containsRedactedPlaceholder(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsRedactedPlaceholder(child) {
				return true
			}
		}
	case string:
		return typed == schemas.RedactedAttrValue
	}
	return false
}

func redactPluginValue(value any, sensitive bool) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = redactPluginValue(child, sensitive || isPluginSecretKey(key))
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = redactPluginValue(child, sensitive)
		}
		return out
	case string:
		if sensitive && typed != "" && !strings.HasPrefix(typed, "env.") {
			return schemas.RedactedAttrValue
		}
		return typed
	default:
		return typed
	}
}

// normalizePluginConfig runs a config through the plugin's typed config, as
// the HTTP handler does, so a malformed field is refused before it is stored.
// Without a plugin runtime there is no typed config to use.
func normalizePluginConfig(deps *Deps, name string, config map[string]any) (map[string]any, error) {
	if deps.PluginRuntime == nil {
		return config, nil
	}
	normalized, err := deps.PluginRuntime.NormalizePluginConfig(name, config)
	if err != nil {
		return nil, fmt.Errorf("invalid plugin configuration: %w", err)
	}
	if normalized == nil {
		return config, nil
	}
	return normalized, nil
}

// rejectPluginPath refuses a caller-supplied plugin path. The path is handed to
// the plugin loader, which executes whatever shared object it names, and there
// is no configured plugin directory to confine it to - so letting an MCP caller
// set it would turn a config tool into arbitrary code execution on the host.
func rejectPluginPath(args map[string]any) error {
	if _, present := args["path"]; present {
		return fmt.Errorf("path cannot be set over MCP; register or move custom plugins through the dashboard or config.json")
	}
	return nil
}

func optionalPluginPlacement(args map[string]any) (*schemas.PluginPlacement, error) {
	text, ok, err := optionalStringArg(args, "placement")
	if err != nil || !ok {
		return nil, err
	}
	placement := schemas.PluginPlacement(text)
	if placement != schemas.PluginPlacementPreBuiltin && placement != schemas.PluginPlacementPostBuiltin {
		return nil, fmt.Errorf("placement must be pre_builtin or post_builtin")
	}
	return &placement, nil
}
