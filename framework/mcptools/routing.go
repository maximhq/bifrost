package mcptools

import (
	"context"
	"errors"
	"fmt"
	"github.com/maximhq/bifrost/core/schemas"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

var validRoutingScopes = map[string]bool{
	"global":      true,
	"team":        true,
	"customer":    true,
	"virtual_key": true,
	"user":        true,
}

func listRoutingRulesTool() Tool {
	return Tool{
		name:        "list_routing_rules",
		description: "Routing rules (id, name, scope, priority, enabled). Use describe_routing_rule for CEL, targets and fallbacks.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "search": {"type": "string"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 20}
  }
}`,
		noLogs: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			limit, err := listLimit(args)
			if err != nil {
				return nil, err
			}
			search, _, err := optionalStringArg(args, "search")
			if err != nil {
				return nil, err
			}
			rows, total, err := gov.GetRoutingRulesPaginated(ctx, configstore.RoutingRulesQueryParams{
				Limit: limit, Search: search,
			})
			if err != nil {
				return nil, fmt.Errorf("list routing rules failed: %w", err)
			}
			out := make([]map[string]any, 0, len(rows))
			for i := range rows {
				item := describeRoutingRule(&rows[i])
				delete(item, "cel_expression")
				delete(item, "targets")
				delete(item, "fallbacks")
				out = append(out, item)
			}
			return map[string]any{"routing_rules": out, "total": total, "returned": len(out)}, nil
		},
	}
}

func describeRoutingRuleTool() Tool {
	return Tool{
		name:        "describe_routing_rule",
		description: "One routing rule: CEL expression, targets, fallbacks, scope and priority.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "rule_id": {"type": "string", "minLength": 1}
  },
  "required": ["rule_id"]
}`,
		noLogs: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := stringArg(args, "rule_id")
			if err != nil {
				return nil, err
			}
			rule, err := gov.GetRoutingRule(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no routing rule with id %q", id)
				}
				return nil, fmt.Errorf("routing rule lookup failed: %w", err)
			}
			return describeRoutingRule(rule), nil
		},
	}
}

func createRoutingRuleTool() Tool {
	return Tool{
		name:        "create_routing_rule",
		description: "Create a routing rule. targets is a list of {provider, model, key_id, weight}; weights must sum to 1. scope is global (default), team, customer, virtual_key or user.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 1},
    "cel_expression": {"type": "string", "description": "CEL match expression. Empty matches every request in scope."},
    "targets": {"type": "array", "minItems": 1, "items": {
      "type": "object",
      "properties": {
        "provider": {"type": "string"},
        "model": {"type": "string"},
        "key_id": {"type": "string"},
        "weight": {"type": "number", "minimum": 0}
      }
    }},
    "description": {"type": "string"},
    "scope": {"type": "string", "enum": ["global", "team", "customer", "virtual_key", "user"]},
    "scope_id": {"type": "string", "description": "Required when scope is not global."},
    "priority": {"type": "integer", "description": "Lower is evaluated first within a scope. Defaults to 0."},
    "enabled": {"type": "boolean"},
    "chain_rule": {"type": "boolean"},
    "fallbacks": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["name", "targets"]
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
			targets, err := parseRoutingTargets(args["targets"])
			if err != nil {
				return nil, err
			}
			cel, _, err := optionalStringArgAllowEmpty(args, "cel_expression")
			if err != nil {
				return nil, err
			}
			description, _, err := optionalStringArg(args, "description")
			if err != nil {
				return nil, err
			}
			scope := "global"
			if s, ok, err := optionalStringArg(args, "scope"); err != nil {
				return nil, err
			} else if ok {
				scope = s
			}
			if !validRoutingScopes[scope] {
				return nil, fmt.Errorf("scope must be one of global, team, customer, virtual_key, user")
			}
			scopeID, hasScopeID, err := optionalStringArg(args, "scope_id")
			if err != nil {
				return nil, err
			}
			var scopeIDPtr *string
			if scope == "global" {
				if hasScopeID {
					return nil, fmt.Errorf("scope_id must be omitted when scope is global")
				}
			} else {
				if !hasScopeID {
					return nil, fmt.Errorf("scope_id is required when scope is %q", scope)
				}
				scopeIDPtr = &scopeID
			}
			priority := 0
			if p, err := optionalIntArg(args, "priority"); err != nil {
				return nil, err
			} else if p != nil {
				priority = *p
			}
			enabled := true
			if flag, err := optionalBoolArg(args, "enabled"); err != nil {
				return nil, err
			} else if flag != nil {
				enabled = *flag
			}
			chain := false
			if flag, err := optionalBoolArg(args, "chain_rule"); err != nil {
				return nil, err
			} else if flag != nil {
				chain = *flag
			}
			fallbacks, err := optionalStringSlice(args, "fallbacks")
			if err != nil {
				return nil, err
			}
			if err := validateRoutingFallbacks(fallbacks); err != nil {
				return nil, err
			}
			if err := validateRoutingCEL(deps, cel); err != nil {
				return nil, err
			}
			if scopeIDPtr != nil {
				if err := validateRoutingScopeID(ctx, gov, scope, scopeID); err != nil {
					return nil, err
				}
			}
			rule := &tables.TableRoutingRule{
				ID:              uuid.NewString(),
				Name:            name,
				Description:     description,
				Enabled:         &enabled,
				ChainRule:       chain,
				CelExpression:   cel,
				Targets:         targets,
				Scope:           scope,
				ScopeID:         scopeIDPtr,
				Priority:        priority,
				ParsedFallbacks: routingFallbacksFromStrings(fallbacks),
			}
			if err := gov.CreateRoutingRule(ctx, rule); err != nil {
				if errors.Is(err, configstore.ErrAlreadyExists) {
					return nil, fmt.Errorf("a routing rule named %q already exists in this scope", name)
				}
				return nil, fmt.Errorf("create routing rule failed: %w", err)
			}
			if reloader := requireReloader(deps); reloader != nil {
				if err := reloader.ReloadRoutingRule(ctx, rule.ID); err != nil {
					return describeRoutingRule(rule), fmt.Errorf("stored but live reload failed: %w", err)
				}
			}
			return describeRoutingRule(rule), nil
		},
	}
}

func updateRoutingRuleTool() Tool {
	return Tool{
		name:        "update_routing_rule",
		description: "Patch a routing rule. Sending targets replaces the whole target list; weights must still sum to 1.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "rule_id": {"type": "string", "minLength": 1},
    "name": {"type": "string"},
    "cel_expression": {"type": "string", "description": "CEL match expression. Omit to keep the current one; an empty string clears it so the rule matches every request in scope."},
    "targets": {"type": "array", "minItems": 1, "items": {
      "type": "object",
      "properties": {
        "provider": {"type": "string"},
        "model": {"type": "string"},
        "key_id": {"type": "string"},
        "weight": {"type": "number", "minimum": 0}
      }
    }},
    "description": {"type": "string"},
    "priority": {"type": "integer"},
    "enabled": {"type": "boolean"},
    "chain_rule": {"type": "boolean"},
    "fallbacks": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["rule_id"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := stringArg(args, "rule_id")
			if err != nil {
				return nil, err
			}
			rule, err := gov.GetRoutingRule(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no routing rule with id %q", id)
				}
				return nil, fmt.Errorf("routing rule lookup failed: %w", err)
			}
			if name, ok, err := optionalStringArg(args, "name"); err != nil {
				return nil, err
			} else if ok {
				rule.Name = name
			}
			if cel, ok, err := optionalStringArgAllowEmpty(args, "cel_expression"); err != nil {
				return nil, err
			} else if ok {
				rule.CelExpression = cel
			}
			if description, ok, err := optionalStringArg(args, "description"); err != nil {
				return nil, err
			} else if ok {
				rule.Description = description
			}
			if _, present := args["targets"]; present {
				targets, err := parseRoutingTargets(args["targets"])
				if err != nil {
					return nil, err
				}
				rule.Targets = targets
			}
			if p, err := optionalIntArg(args, "priority"); err != nil {
				return nil, err
			} else if p != nil {
				rule.Priority = *p
			}
			if flag, err := optionalBoolArg(args, "enabled"); err != nil {
				return nil, err
			} else if flag != nil {
				rule.Enabled = flag
			}
			if flag, err := optionalBoolArg(args, "chain_rule"); err != nil {
				return nil, err
			} else if flag != nil {
				rule.ChainRule = *flag
			}
			if _, present := args["fallbacks"]; present {
				fallbacks, err := optionalStringSlice(args, "fallbacks")
				if err != nil {
					return nil, err
				}
				if err := validateRoutingFallbacks(fallbacks); err != nil {
					return nil, err
				}
				rule.ParsedFallbacks = routingFallbacksFromStrings(fallbacks)
			}
			if _, present := args["cel_expression"]; present {
				if err := validateRoutingCEL(deps, rule.CelExpression); err != nil {
					return nil, err
				}
			}
			if err := gov.UpdateRoutingRule(ctx, rule); err != nil {
				return nil, fmt.Errorf("update routing rule failed: %w", err)
			}
			if reloader := requireReloader(deps); reloader != nil {
				if err := reloader.ReloadRoutingRule(ctx, rule.ID); err != nil {
					return describeRoutingRule(rule), fmt.Errorf("stored but live reload failed: %w", err)
				}
			}
			return describeRoutingRule(rule), nil
		},
	}
}

func describeRoutingRule(rule *tables.TableRoutingRule) map[string]any {
	out := map[string]any{
		"id":             rule.ID,
		"name":           rule.Name,
		"scope":          rule.Scope,
		"priority":       rule.Priority,
		"enabled":        rule.EnabledValue(),
		"chain_rule":     rule.ChainRule,
		"cel_expression": rule.CelExpression,
	}
	if rule.Description != "" {
		out["description"] = rule.Description
	}
	if rule.ScopeID != nil {
		out["scope_id"] = *rule.ScopeID
	}
	if len(rule.Targets) > 0 {
		targets := make([]map[string]any, 0, len(rule.Targets))
		for _, t := range rule.Targets {
			item := map[string]any{"weight": t.Weight}
			if t.Provider != nil {
				item["provider"] = *t.Provider
			}
			if t.Model != nil {
				item["model"] = *t.Model
			}
			if t.KeyID != nil {
				item["key_id"] = *t.KeyID
			}
			targets = append(targets, item)
		}
		out["targets"] = targets
	}
	if len(rule.ParsedFallbacks) > 0 {
		out["fallbacks"] = append([]tables.RoutingFallback{}, rule.ParsedFallbacks...)
	}
	return out
}

func parseRoutingTargets(raw any) ([]tables.TableRoutingTarget, error) {
	if raw == nil {
		return nil, fmt.Errorf("targets is required")
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("targets must be an array")
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("at least one target is required")
	}
	out := make([]tables.TableRoutingTarget, 0, len(items))
	seen := make(map[string]bool, len(items))
	var sum float64
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("targets[%d] must be an object", i)
		}
		t := tables.TableRoutingTarget{Weight: 1}
		if w, present := obj["weight"]; present && w != nil {
			wf, ok := w.(float64)
			if !ok {
				return nil, fmt.Errorf("targets[%d].weight must be a number", i)
			}
			if wf < 0 {
				return nil, fmt.Errorf("targets[%d].weight must not be negative", i)
			}
			t.Weight = wf
		}
		if p, ok, err := optionalStringArg(obj, "provider"); err != nil {
			return nil, fmt.Errorf("targets[%d].provider: %w", i, err)
		} else if ok {
			t.Provider = &p
		}
		if m, ok, err := optionalStringArg(obj, "model"); err != nil {
			return nil, fmt.Errorf("targets[%d].model: %w", i, err)
		} else if ok {
			t.Model = &m
		}
		if k, ok, err := optionalStringArg(obj, "key_id"); err != nil {
			return nil, fmt.Errorf("targets[%d].key_id: %w", i, err)
		} else if ok {
			t.KeyID = &k
		}
		if t.KeyID != nil && *t.KeyID != "" && (t.Provider == nil || *t.Provider == "") {
			return nil, fmt.Errorf("targets[%d].key_id requires provider to be set", i)
		}
		// Same identity rule as the HTTP handler: provider and model compare
		// case-insensitively, and an absent field equals an empty one.
		identity := strings.ToLower(derefString(t.Provider)) + "|" + strings.ToLower(derefString(t.Model)) + "|" + derefString(t.KeyID)
		if seen[identity] {
			return nil, fmt.Errorf("targets[%d] duplicates an earlier target (provider=%q model=%q key_id=%q)", i, derefString(t.Provider), derefString(t.Model), derefString(t.KeyID))
		}
		seen[identity] = true
		sum += t.Weight
		out = append(out, t)
	}
	if math.Abs(sum-1) > 0.001 {
		return nil, fmt.Errorf("target weights must sum to 1, got %v", sum)
	}
	return out, nil
}

// routingFallbacksFromStrings turns the tools' "provider/model" fallbacks into
// rule entries. The tools never pin a key, so each entry is the legacy form.
func routingFallbacksFromStrings(fallbacks []string) []tables.RoutingFallback {
	if fallbacks == nil {
		return nil
	}
	out := make([]tables.RoutingFallback, 0, len(fallbacks))
	for _, fallback := range fallbacks {
		provider, model := schemas.ParseModelString(fallback, "")
		out = append(out, tables.RoutingFallback{Fallback: schemas.Fallback{Provider: provider, Model: model}})
	}
	return out
}

// validateRoutingFallbacks requires each fallback to name a known provider, as
// the HTTP handler does ("openai/gpt-4o", or "azure/" for the incoming model).
func validateRoutingFallbacks(fallbacks []string) error {
	for i, fallback := range fallbacks {
		if provider, _ := schemas.ParseModelString(fallback, ""); provider == "" {
			return fmt.Errorf("fallbacks[%d] %q is invalid: it must start with a known provider, like \"openai/gpt-4o\" or \"azure/\" for the incoming model", i, fallback)
		}
	}
	return nil
}

// validateRoutingCEL rejects a malformed expression at write time; stored, it
// would only fail at the first request it was evaluated against.
func validateRoutingCEL(deps *Deps, expression string) error {
	if deps.RoutingCEL == nil {
		return fmt.Errorf("routing expressions cannot be validated on this deployment, so the rule is not changed here")
	}
	if err := deps.RoutingCEL.ValidateCELExpression(expression); err != nil {
		return fmt.Errorf("invalid CEL expression: %w", err)
	}
	return nil
}

// validateRoutingScopeID checks the scope's owner exists; a rule scoped to a
// typo matches nothing and says nothing. User ids live outside the config
// store and are matched at evaluation time, so they are not checked.
func validateRoutingScopeID(ctx context.Context, gov GovernanceReader, scope, scopeID string) error {
	var err error
	switch scope {
	case "virtual_key":
		_, err = gov.GetVirtualKey(ctx, scopeID)
	case "team":
		_, err = gov.GetTeam(ctx, scopeID)
	case "customer":
		_, err = gov.GetCustomer(ctx, scopeID)
	default:
		return nil
	}
	if errors.Is(err, configstore.ErrNotFound) {
		return fmt.Errorf("no %s with id %q", strings.ReplaceAll(scope, "_", " "), scopeID)
	}
	if err != nil {
		return fmt.Errorf("scope_id lookup failed: %w", err)
	}
	return nil
}

func optionalStringSlice(args map[string]any, key string) ([]string, error) {
	value, present := args[key]
	if !present || value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	out := make([]string, 0, len(items))
	for i, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be a string", key, i)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return nil, fmt.Errorf("%s[%d] must not be empty", key, i)
		}
		out = append(out, text)
	}
	return out, nil
}
