package mcptools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

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
		name: "create_routing_rule",
		description: "Create a routing rule. targets is a list of {provider, model, key_id, weight, ttft_timeout_ms}; every weight must be above 0 and they must sum to 1. scope is global (default), team, customer, virtual_key or user. " +
			"Two rules in the same scope cannot share a priority: that is refused with 409, so pick an unused priority (list_routing_rules shows them).",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 1},
    "cel_expression": {"type": "string", "description": "CEL match expression. Empty matches every request in scope."},
    "targets": {"type": "array", "minItems": 1, "items": {
      "type": "object",
      "properties": {
        "provider": {"type": "string", "description": "Omit to use the incoming request's provider."},
        "model": {"type": "string", "description": "Omit to use the incoming request's model."},
        "key_id": {"type": "string", "description": "Pins one of the provider's keys; requires provider."},
        "weight": {"type": "number", "exclusiveMinimum": 0},
        "ttft_timeout_ms": {"type": "integer", "minimum": 0, "maximum": 300000, "description": "Streaming time-to-first-token deadline in milliseconds; 0 means none."}
      },
      "required": ["weight"]
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
			if _, err := stringArg(args, "name"); err != nil {
				return nil, err
			}
			if _, present := args["targets"]; !present {
				return nil, fmt.Errorf("targets is required")
			}
			// The route drops a scope_id sent with the global scope and stores
			// a global rule. A caller who named an owner meant a narrower rule
			// than that, so it is refused here rather than widened there.
			if scopeID, _ := args["scope_id"].(string); scopeID != "" {
				if scope, _ := args["scope"].(string); scope == "" || scope == "global" {
					return nil, fmt.Errorf("scope_id must be omitted when scope is global; to scope the rule, set scope to team, customer, virtual_key or user")
				}
			}
			targets, err := routingTargetsArg(args["targets"], nil)
			if err != nil {
				return nil, err
			}
			body := adminBody{}.copyPresent(args, "name", "cel_expression", "description",
				"scope", "scope_id", "priority", "enabled", "chain_rule", "fallbacks").set("targets", targets)
			response, err := callAdmin(ctx, deps, http.MethodPost, "/api/routing/rules", body)
			if err != nil {
				return nil, err
			}
			return describeRoutingRuleResponse(response)
		},
	}
}

func updateRoutingRuleTool() Tool {
	return Tool{
		name: "update_routing_rule",
		description: "Patch a routing rule. Sending targets replaces the whole target list, so list every target that should remain; weights must still each be above 0 and sum to 1. " +
			"A target that omits ttft_timeout_ms keeps the deadline of the stored target with the same provider, model and key_id; pass 0 to clear it. " +
			"A priority another rule in the same scope already has is refused with 409.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "rule_id": {"type": "string", "minLength": 1},
    "name": {"type": "string"},
    "cel_expression": {"type": "string", "description": "CEL match expression. Omit to keep the current one; an empty string clears it so the rule matches every request in scope."},
    "targets": {"type": "array", "minItems": 1, "items": {
      "type": "object",
      "properties": {
        "provider": {"type": "string", "description": "Omit to use the incoming request's provider."},
        "model": {"type": "string", "description": "Omit to use the incoming request's model."},
        "key_id": {"type": "string", "description": "Pins one of the provider's keys; requires provider."},
        "weight": {"type": "number", "exclusiveMinimum": 0},
        "ttft_timeout_ms": {"type": "integer", "minimum": 0, "maximum": 300000, "description": "Streaming time-to-first-token deadline in milliseconds; 0 means none."}
      },
      "required": ["weight"]
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
			id, err := stringArg(args, "rule_id")
			if err != nil {
				return nil, err
			}
			path, err := adminPath("/api/routing/rules/{rule_id}", id)
			if err != nil {
				return nil, err
			}
			body := adminBody{}.copyPresent(args, "name", "cel_expression", "description",
				"priority", "enabled", "chain_rule", "fallbacks")
			if raw, present := args["targets"]; present && raw != nil {
				// The route replaces the whole list, so the stored rule is read
				// for the deadlines a reweighted target did not restate.
				gov, err := requireGovernance(deps)
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
				targets, err := routingTargetsArg(raw, rule.Targets)
				if err != nil {
					return nil, err
				}
				body.set("targets", targets)
			}
			response, err := callAdmin(ctx, deps, http.MethodPut, path, body)
			if err != nil {
				return nil, err
			}
			return describeRoutingRuleResponse(response)
		},
	}
}

// describeRoutingRuleResponse projects the rule a routing route answers with.
func describeRoutingRuleResponse(response []byte) (any, error) {
	var rule tables.TableRoutingRule
	if err := decodeAdminField(response, "rule", &rule); err != nil {
		return nil, err
	}
	return describeRoutingRule(&rule), nil
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
			if t.TTFTTimeoutMs != nil && *t.TTFTTimeoutMs > 0 {
				item["ttft_timeout_ms"] = *t.TTFTTimeoutMs
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

// maxRoutingTTFTTimeoutMs mirrors the route's cap on a target's deadline.
const maxRoutingTTFTTimeoutMs = 300000

// routingTargetsArg builds the targets list field by field. A target that does
// not name ttft_timeout_ms takes the deadline of the stored target with the
// same identity (provider and model compared case-insensitively, as the route
// does), so replacing the list to reweight it does not drop deadlines.
func routingTargetsArg(raw any, stored []tables.TableRoutingTarget) ([]map[string]any, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("targets must be a non-empty array of {provider, model, key_id, weight, ttft_timeout_ms}")
	}
	identity := func(provider, model, keyID string) string {
		return strings.ToLower(provider) + "|" + strings.ToLower(model) + "|" + keyID
	}
	deadlines := make(map[string]int, len(stored))
	for _, t := range stored {
		if t.TTFTTimeoutMs != nil && *t.TTFTTimeoutMs > 0 {
			deadlines[identity(derefString(t.Provider), derefString(t.Model), derefString(t.KeyID))] = *t.TTFTTimeoutMs
		}
	}
	out := make([]map[string]any, 0, len(items))
	for i, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("targets[%d] must be an object", i)
		}
		for key := range entry {
			switch key {
			case "provider", "model", "key_id", "weight", "ttft_timeout_ms":
			default:
				return nil, fmt.Errorf("targets[%d] has unknown field %q; a target takes provider, model, key_id, weight and ttft_timeout_ms", i, key)
			}
		}
		target := map[string]any{}
		fields := map[string]string{}
		for _, key := range []string{"provider", "model", "key_id"} {
			v, set, err := optionalStringArg(entry, key)
			if err != nil {
				return nil, fmt.Errorf("targets[%d]: %w", i, err)
			}
			if set {
				target[key] = v
				fields[key] = v
			}
		}
		weight, err := floatArg(entry, "weight")
		if err != nil {
			return nil, fmt.Errorf("targets[%d]: %w", i, err)
		}
		if !(weight > 0) {
			return nil, fmt.Errorf("targets[%d]: weight must be greater than 0, got %v", i, weight)
		}
		target["weight"] = weight
		ttft, err := optionalIntArg(entry, "ttft_timeout_ms")
		if err != nil {
			return nil, fmt.Errorf("targets[%d]: %w", i, err)
		}
		switch {
		case ttft != nil:
			if *ttft < 0 || *ttft > maxRoutingTTFTTimeoutMs {
				return nil, fmt.Errorf("targets[%d]: ttft_timeout_ms must be between 1 and %d (0 disables it)", i, maxRoutingTTFTTimeoutMs)
			}
			target["ttft_timeout_ms"] = *ttft
		default:
			if ms, ok := deadlines[identity(fields["provider"], fields["model"], fields["key_id"])]; ok {
				target["ttft_timeout_ms"] = ms
			}
		}
		out = append(out, target)
	}
	return out, nil
}
