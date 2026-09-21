package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
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
    "request_types": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["name", "scope_kind", "match_type", "pattern", "patch"]
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
			scopeKind, err := stringArg(args, "scope_kind")
			if err != nil {
				return nil, err
			}
			matchType, err := stringArg(args, "match_type")
			if err != nil {
				return nil, err
			}
			pattern, err := stringArg(args, "pattern")
			if err != nil {
				return nil, err
			}
			patch, ok, err := optionalObjectArg(args, "patch")
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("patch is required")
			}
			vkID, _, err := optionalStringArg(args, "virtual_key_id")
			if err != nil {
				return nil, err
			}
			providerID, _, err := optionalStringArg(args, "provider_id")
			if err != nil {
				return nil, err
			}
			keyID, _, err := optionalStringArg(args, "provider_key_id")
			if err != nil {
				return nil, err
			}
			userID, _, err := optionalStringArg(args, "user_id")
			if err != nil {
				return nil, err
			}
			requestTypes, err := optionalRequestTypes(args)
			if err != nil {
				return nil, err
			}
			shape := modelcatalog.PricingOverride{
				ScopeKind:     modelcatalog.ScopeKind(scopeKind),
				UserID:        stringPtrOrNil(userID),
				VirtualKeyID:  stringPtrOrNil(vkID),
				ProviderID:    stringPtrOrNil(providerID),
				ProviderKeyID: stringPtrOrNil(keyID),
				MatchType:     modelcatalog.MatchType(matchType),
				Pattern:       pattern,
				RequestTypes:  requestTypes,
			}
			if err := shape.IsValid(); err != nil {
				return nil, err
			}
			patchJSON, err := json.Marshal(patch)
			if err != nil {
				return nil, fmt.Errorf("invalid patch: %w", err)
			}
			now := time.Now()
			override := &tables.TablePricingOverride{
				ID:               uuid.NewString(),
				Name:             name,
				ScopeKind:        scopeKind,
				UserID:           shape.UserID,
				VirtualKeyID:     shape.VirtualKeyID,
				ProviderID:       shape.ProviderID,
				ProviderKeyID:    shape.ProviderKeyID,
				MatchType:        matchType,
				Pattern:          pattern,
				RequestTypes:     requestTypes,
				PricingPatchJSON: string(patchJSON),
				CreatedAt:        now,
				UpdatedAt:        now,
			}
			if err := gov.CreatePricingOverride(ctx, override); err != nil {
				return nil, fmt.Errorf("create pricing override failed: %w", err)
			}
			if reloader := requireReloader(deps); reloader != nil {
				if err := reloader.UpsertPricingOverride(ctx, override); err != nil {
					return projectPricingOverride(override), fmt.Errorf("stored but live reload failed: %w", err)
				}
			}
			return projectPricingOverride(override), nil
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
			if name, ok, err := optionalStringArg(args, "name"); err != nil {
				return nil, err
			} else if ok {
				row.Name = name
			}
			if pattern, ok, err := optionalStringArg(args, "pattern"); err != nil {
				return nil, err
			} else if ok {
				row.Pattern = pattern
			}
			if matchType, ok, err := optionalStringArg(args, "match_type"); err != nil {
				return nil, err
			} else if ok {
				row.MatchType = matchType
			}
			if patch, ok, err := optionalObjectArg(args, "patch"); err != nil {
				return nil, err
			} else if ok {
				patchJSON, err := json.Marshal(patch)
				if err != nil {
					return nil, fmt.Errorf("invalid patch: %w", err)
				}
				row.PricingPatchJSON = string(patchJSON)
			}
			shape := modelcatalog.PricingOverride{
				ScopeKind:     modelcatalog.ScopeKind(row.ScopeKind),
				UserID:        row.UserID,
				VirtualKeyID:  row.VirtualKeyID,
				ProviderID:    row.ProviderID,
				ProviderKeyID: row.ProviderKeyID,
				MatchType:     modelcatalog.MatchType(row.MatchType),
				Pattern:       row.Pattern,
				RequestTypes:  row.RequestTypes,
			}
			if err := shape.IsValid(); err != nil {
				return nil, err
			}
			row.UpdatedAt = time.Now()
			if err := gov.UpdatePricingOverride(ctx, row); err != nil {
				return nil, fmt.Errorf("update pricing override failed: %w", err)
			}
			if reloader := requireReloader(deps); reloader != nil {
				if err := reloader.UpsertPricingOverride(ctx, row); err != nil {
					return projectPricingOverride(row), fmt.Errorf("stored but live reload failed: %w", err)
				}
			}
			return projectPricingOverride(row), nil
		},
	}
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

func optionalRequestTypes(args map[string]any) ([]schemas.RequestType, error) {
	raw, err := optionalStringSlice(args, "request_types")
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]schemas.RequestType, len(raw))
	for i, t := range raw {
		out[i] = schemas.RequestType(t)
	}
	return out, nil
}

func stringPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
