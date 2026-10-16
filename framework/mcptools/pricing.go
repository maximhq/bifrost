package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

func listPricingOverridesTool() Tool {
	return Tool{
		name:        "list_pricing_overrides",
		description: "Governance pricing overrides (id, name, scope, pattern). Use describe_pricing_override for the cost patch.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "search": {"type": "string"},
    "scope_kind": {"type": "string"},
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
			params := configstore.PricingOverridesQueryParams{Limit: limit, Search: search}
			if kind, ok, err := optionalStringArg(args, "scope_kind"); err != nil {
				return nil, err
			} else if ok {
				params.ScopeKind = &kind
			}
			rows, total, err := gov.GetPricingOverridesPaginated(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("list pricing overrides failed: %w", err)
			}
			out := make([]map[string]any, 0, len(rows))
			for i := range rows {
				item := projectPricingOverride(&rows[i])
				delete(item, "patch")
				out = append(out, item)
			}
			return map[string]any{"pricing_overrides": out, "total": total, "returned": len(out)}, nil
		},
	}
}

func describePricingOverrideTool() Tool {
	return Tool{
		name:        "describe_pricing_override",
		description: "One pricing override including the cost patch that replaces catalog rates.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "override_id": {"type": "string", "minLength": 1}
  },
  "required": ["override_id"]
}`,
		noLogs: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := stringArg(args, "override_id")
			if err != nil {
				return nil, err
			}
			row, err := gov.GetPricingOverrideByID(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no pricing override with id %q", id)
				}
				return nil, fmt.Errorf("pricing override lookup failed: %w", err)
			}
			return projectPricingOverride(row), nil
		},
	}
}

func createPricingOverrideTool() Tool {
	return Tool{
		name:        "create_pricing_override",
		description: "Create a scoped pricing override. patch is a cost-field object (e.g. input_cost_per_token). match_type is exact or wildcard. scope_kind is global, provider, virtual_key, user, and the provider/user/key variants.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 1},
    "scope_kind": {"type": "string", "minLength": 1},
    "match_type": {"type": "string", "enum": ["exact", "wildcard"]},
    "pattern": {"type": "string", "minLength": 1, "description": "Model name or wildcard, e.g. gpt-4o or gpt-*."},
    "patch": {"type": "object", "description": "Cost fields to override, e.g. {\"input_cost_per_token\": 0.000001}."},
    "virtual_key_id": {"type": "string"},
    "provider_id": {"type": "string"},
    "provider_key_id": {"type": "string"},
    "user_id": {"type": "string"},
    "request_types": {"type": "array", "minItems": 1, "items": {"type": "string"}, "description": "Request types the override prices, e.g. chat_completion, embedding."}
  },
  "required": ["name", "scope_kind", "match_type", "pattern", "patch", "request_types"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			for _, required := range []string{"name", "scope_kind", "match_type", "pattern"} {
				if _, err := stringArg(args, required); err != nil {
					return nil, err
				}
			}
			if _, ok, err := optionalObjectArg(args, "patch"); err != nil {
				return nil, err
			} else if !ok {
				return nil, fmt.Errorf("patch is required")
			}
			if _, present := args["request_types"]; !present {
				return nil, fmt.Errorf("request_types is required")
			}
			body := adminBody{}.copyPresent(args, "name", "scope_kind", "match_type", "pattern", "patch",
				"virtual_key_id", "provider_id", "provider_key_id", "user_id", "request_types")
			response, err := callAdmin(ctx, deps, http.MethodPost, "/api/governance/pricing-overrides", body)
			if err != nil {
				return nil, err
			}
			return projectPricingOverrideResponse(response)
		},
	}
}

func updatePricingOverrideTool() Tool {
	return Tool{
		name:        "update_pricing_override",
		description: "Patch a pricing override. Sending patch replaces the whole cost object.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "override_id": {"type": "string", "minLength": 1},
    "name": {"type": "string"},
    "pattern": {"type": "string"},
    "match_type": {"type": "string", "enum": ["exact", "wildcard"]},
    "patch": {"type": "object"}
  },
  "required": ["override_id"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			id, err := stringArg(args, "override_id")
			if err != nil {
				return nil, err
			}
			path, err := adminPath("/api/governance/pricing-overrides/{id}", id)
			if err != nil {
				return nil, err
			}
			body := adminBody{}.copyPresent(args, "name", "pattern", "match_type", "patch")
			response, err := callAdmin(ctx, deps, http.MethodPut, path, body)
			if err != nil {
				return nil, err
			}
			return projectPricingOverrideResponse(response)
		},
	}
}

// projectPricingOverrideResponse projects the override a pricing route answers with.
func projectPricingOverrideResponse(response []byte) (any, error) {
	var row tables.TablePricingOverride
	if err := decodeAdminField(response, "pricing_override", &row); err != nil {
		return nil, err
	}
	return projectPricingOverride(&row), nil
}

func projectPricingOverride(row *tables.TablePricingOverride) map[string]any {
	out := map[string]any{
		"id":         row.ID,
		"name":       row.Name,
		"scope_kind": row.ScopeKind,
		"match_type": row.MatchType,
		"pattern":    row.Pattern,
	}
	if row.VirtualKeyID != nil {
		out["virtual_key_id"] = *row.VirtualKeyID
	}
	if row.ProviderID != nil {
		out["provider_id"] = *row.ProviderID
	}
	if row.ProviderKeyID != nil {
		out["provider_key_id"] = *row.ProviderKeyID
	}
	if row.UserID != nil {
		out["user_id"] = *row.UserID
	}
	if len(row.RequestTypes) > 0 {
		types := make([]string, len(row.RequestTypes))
		for i, t := range row.RequestTypes {
			types[i] = string(t)
		}
		out["request_types"] = types
	}
	if row.PricingPatchJSON != "" {
		var patch any
		if err := json.Unmarshal([]byte(row.PricingPatchJSON), &patch); err == nil {
			out["patch"] = patch
		}
	}
	return out
}
