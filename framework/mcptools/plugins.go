package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
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
		name: "create_plugin",
		description: "Register or configure a built-in plugin. placement is pre_builtin or post_builtin. Custom plugins (a filesystem path to load) cannot be registered over MCP; use the dashboard or config.json. " +
			pluginDialTargetNote,
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
			config, _, err := optionalObjectArg(args, "config")
			if err != nil {
				return nil, err
			}
			if err := refusePluginDialTargetChange(nil, config); err != nil {
				return nil, err
			}
			// The route reads a missing enabled as off; here a plugin is
			// registered to be used, so it is on unless the caller says not.
			body := adminBody{"name": name, "enabled": true}
			body.copySet(args, "enabled", "config", "placement", "order")
			if _, err := callAdmin(ctx, deps, http.MethodPost, "/api/plugins", body); err != nil {
				return nil, err
			}
			return describeStoredPlugin(ctx, gov, name)
		},
	}
}

func updatePluginTool() Tool {
	return Tool{
		name: "update_plugin",
		description: "Enable/disable a plugin or replace its config, placement or order. config is merged over the stored config key by key. A plugin's path cannot be changed over MCP. " +
			pluginDialTargetNote,
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
			path, err := adminPath("/api/plugins/{name}", name)
			if err != nil {
				return nil, err
			}
			// Looked up first: the route creates a plugin it does not find,
			// and an update of a mistyped name must not register one.
			plugin, err := gov.GetPlugin(ctx, name)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no plugin named %q", name)
				}
				return nil, fmt.Errorf("plugin lookup failed: %w", err)
			}
			// The route stores the path it is sent, and none sent clears it.
			// A path cannot be sent from here, so a plugin loaded from one
			// cannot be updated from here without unloading it.
			if plugin.Path != nil && *plugin.Path != "" {
				return nil, fmt.Errorf("%q is a custom plugin loaded from a path, and a path cannot be sent over MCP; change it through the dashboard or config.json", name)
			}
			// enabled, placement and order are replaced by the route, not
			// patched, so what the caller did not name is sent as stored.
			body := adminBody{"enabled": plugin.Enabled}
			if plugin.Placement != nil {
				body["placement"] = string(*plugin.Placement)
			}
			if plugin.Order != nil {
				body["order"] = *plugin.Order
			}
			body.copySet(args, "enabled", "placement", "order")
			// config is merged over the stored one by the route. The
			// placeholders describe_plugin hands out are this package's own,
			// so they are swapped back for the stored values here.
			if config, ok, err := optionalObjectArg(args, "config"); err != nil {
				return nil, err
			} else if ok {
				restored, err := restoreRedactedPluginConfig(config, plugin.Config)
				if err != nil {
					return nil, err
				}
				if err := refusePluginDialTargetChange(plugin.Config, restored); err != nil {
					return nil, err
				}
				body["config"] = restored
			}
			if _, err := callAdmin(ctx, deps, http.MethodPut, path, body); err != nil {
				return nil, err
			}
			return describeStoredPlugin(ctx, gov, name)
		},
	}
}

// describeStoredPlugin reads a plugin back after a write and projects it the
// way describe_plugin does, so its config carries this package's redaction.
func describeStoredPlugin(ctx context.Context, gov GovernanceReader, name string) (any, error) {
	plugin, err := gov.GetPlugin(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("the change was made, but the plugin could not be read back: %w", err)
	}
	return projectPlugin(plugin), nil
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

// pluginDialTargetNote is the refusal both plugin write tools describe.
const pluginDialTargetNote = "Changing where a plugin sends data (collector_url, metrics_endpoint, push_gateway_url, endpoint, url, base_url) or adding or changing an env. or vault. secret reference anywhere in its config needs an authenticated admin session, which a tool call never is, so such a change is refused: make it in the dashboard with auth enabled, or in config.json. Keeping the stored values and editing anything else works."

// pluginEndpointKeys mirror the plugin route's: keys whose value is a host
// the plugin dials with what the rest of its config tells it to send.
var pluginEndpointKeys = []string{"collector_url", "metrics_endpoint", "push_gateway_url", "endpoint", "url", "base_url"}

func isPluginEndpointKey(key string) bool {
	return slices.ContainsFunc(pluginEndpointKeys, func(k string) bool { return strings.EqualFold(k, key) })
}

func isSecretReference(value string) bool {
	return strings.HasPrefix(value, "env.") || strings.HasPrefix(value, "vault.")
}

// refusePluginDialTargetChange refuses what the plugin routes refuse for a
// request that was not genuinely authenticated - and the admin dispatcher's
// requests never are: a new or changed endpoint, or env./vault. reference.
// The route merges config over the stored one key by key, so incoming is
// compared over that merge. The route remains the authority; this exists so
// the model hears why, and what to do instead, before anything is sent.
func refusePluginDialTargetChange(stored any, incoming map[string]any) error {
	if len(incoming) == 0 {
		return nil
	}
	var storedMap map[string]any
	if stored != nil {
		if encoded, err := json.Marshal(stored); err == nil {
			_ = json.Unmarshal(encoded, &storedMap)
		}
	}
	merged := make(map[string]any, len(storedMap)+len(incoming))
	maps.Copy(merged, storedMap)
	maps.Copy(merged, incoming)
	before, after := map[string]string{}, map[string]string{}
	pluginDialTargets("", storedMap, false, before)
	pluginDialTargets("", merged, false, after)
	var changed []string
	for path, value := range after {
		if before[path] != value {
			changed = append(changed, path)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	slices.Sort(changed)
	return fmt.Errorf("this would set or change a plugin endpoint or env./vault. reference (%s), which the plugin route only accepts from an authenticated admin session - a tool call is not one. Make this change in the dashboard with authentication enabled, or in config.json", strings.Join(changed, ", "))
}

// pluginDialTargets flattens config into path -> value for every endpoint
// value and every secret reference, the latter also when written as a
// SecretVar object ({"type": "env"|"vault", "ref": ...}, {"ref": "env.X"} or
// {"env_var": "X", "from_env": true}).
func pluginDialTargets(path string, value any, endpoint bool, out map[string]string) {
	switch v := value.(type) {
	case map[string]any:
		if target := secretVarTarget(v, endpoint); target != "" {
			out[path] = target
		}
		for key, item := range v {
			child := key
			if path != "" {
				child = path + "." + key
			}
			pluginDialTargets(child, item, isPluginEndpointKey(key), out)
		}
	case []any:
		for i, item := range v {
			pluginDialTargets(fmt.Sprintf("%s[%d]", path, i), item, false, out)
		}
	case string:
		if v != "" && (endpoint || isSecretReference(v)) {
			out[path] = v
		}
	}
}

// secretVarTarget is the reference or endpoint value a SecretVar-shaped
// object stands for, or "" when it stands for neither.
func secretVarTarget(object map[string]any, endpoint bool) string {
	field := func(name string) any {
		for key, value := range object {
			if strings.EqualFold(key, name) {
				return value
			}
		}
		return nil
	}
	ref, _ := field("ref").(string)
	ref = strings.TrimSpace(ref)
	switch kind, _ := field("type").(string); {
	case ref != "" && (kind == "env" || kind == "vault"):
		if isSecretReference(ref) {
			return ref
		}
		return kind + "." + ref
	case ref != "" && isSecretReference(ref):
		return ref
	}
	if envVar, _ := field("env_var").(string); envVar != "" {
		if fromEnv, _ := field("from_env").(bool); fromEnv {
			return "env." + strings.TrimPrefix(envVar, "env.")
		}
	}
	if endpoint {
		if value, _ := field("value").(string); value != "" {
			return value
		}
	}
	return ""
}
