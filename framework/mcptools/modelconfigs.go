package mcptools

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

func listModelConfigsTool() Tool {
	return Tool{
		name:        "list_model_configs",
		description: "Per-model budgets and rate limits (id, model, provider, scope). Use describe_model_config for the attached budget and rate limit.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "search": {"type": "string"},
    "scope": {"type": "string", "description": "global or virtual_key."},
    "scope_id": {"type": "string"},
    "provider": {"type": "string"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 20}
  }
}`,
		noLogs:       true,
		tenantScoped: true,
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
			scope, _, err := optionalStringArg(args, "scope")
			if err != nil {
				return nil, err
			}
			scopeID, _, err := optionalStringArg(args, "scope_id")
			if err != nil {
				return nil, err
			}
			provider, _, err := optionalStringArg(args, "provider")
			if err != nil {
				return nil, err
			}
			var scopes []string
			if scope != "" {
				scopes = []string{scope}
			}
			var rows []tables.TableModelConfig
			var total int64
			if t, known, err := readTenant(ctx, false); err != nil {
				return nil, err
			} else if known {
				// No row filter in the store; a scoped caller lists the
				// configs on its own keys, filtered here.
				index, err := t.index(ctx, gov)
				if err != nil {
					return nil, err
				}
				for _, mc := range index.modelConfigs {
					if scope != "" && mc.Scope != scope {
						continue
					}
					if scopeID != "" && (mc.ScopeID == nil || *mc.ScopeID != scopeID) {
						continue
					}
					if provider != "" && (mc.Provider == nil || *mc.Provider != provider) {
						continue
					}
					if !matchesSearch(mc.ModelName, search) {
						continue
					}
					total++
					if len(rows) < limit {
						rows = append(rows, mc)
					}
				}
			} else {
				rows, total, err = gov.GetModelConfigsPaginated(ctx, configstore.ModelConfigsQueryParams{
					Limit: limit, Search: search, Scopes: scopes, ScopeID: scopeID, Provider: provider,
				})
				if err != nil {
					return nil, fmt.Errorf("list model configs failed: %w", err)
				}
			}
			out := make([]map[string]any, 0, len(rows))
			for i := range rows {
				item := describeModelConfig(&rows[i])
				delete(item, "budgets")
				delete(item, "rate_limit")
				out = append(out, item)
			}
			return map[string]any{"model_configs": out, "total": total, "returned": len(out)}, nil
		},
	}
}

func describeModelConfigTool() Tool {
	return Tool{
		name:        "describe_model_config",
		description: "One model config: scope, attached budgets and rate limit.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "model_config_id": {"type": "string", "minLength": 1}
  },
  "required": ["model_config_id"]
}`,
		noLogs:       true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := stringArg(args, "model_config_id")
			if err != nil {
				return nil, err
			}
			notFound := fmt.Errorf("no model config with id %q", id)
			row, err := gov.GetModelConfigByID(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, notFound
				}
				return nil, fmt.Errorf("model config lookup failed: %w", err)
			}
			if err := hiddenOutsideTenant(ctx, false, func(t tenant) (bool, error) {
				return t.ownsModelConfig(ctx, gov, row)
			}, notFound); err != nil {
				return nil, err
			}
			return describeModelConfig(row), nil
		},
	}
}

func createModelConfigTool() Tool {
	return Tool{
		name: "create_model_config",
		description: "Create a model-level budget/rate-limit config. scope is global (default) or virtual_key (requires scope_id). model_name may be * for every model on the provider. " +
			"Model names are compared case-insensitively with any provider prefix stripped, and a provider-less config is keyed by its base model (a dated alias counts as its base), so a config that would key the same as an existing one in the scope is refused with 409 - update that one instead (list_model_configs shows it).",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "model_name": {"type": "string", "minLength": 1},
    "provider": {"type": "string", "description": "Omit to apply across every provider."},
    "scope": {"type": "string", "enum": ["global", "virtual_key"]},
    "scope_id": {"type": "string", "description": "Virtual key id when scope is virtual_key."},
    "max_limit": {"type": "number", "minimum": 0, "description": "Budget cap. Requires reset_duration."},
    "reset_duration": {"type": "string", "description": "Budget window, e.g. 1d, 1M."},
    "token_max_limit": {"type": "integer", "minimum": 1},
    "token_reset_duration": {"type": "string"},
    "request_max_limit": {"type": "integer", "minimum": 1},
    "request_reset_duration": {"type": "string"}
  },
  "required": ["model_name"]
}`,
		noLogs:       true,
		mutating:     true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			if _, err := stringArg(args, "model_name"); err != nil {
				return nil, err
			}
			scope := tables.ModelConfigScopeGlobal
			if s, ok, err := optionalStringArg(args, "scope"); err != nil {
				return nil, err
			} else if ok {
				scope = s
			}
			scopeID, hasScopeID, err := optionalStringArg(args, "scope_id")
			if err != nil {
				return nil, err
			}
			// The route does not know the caller's tenant. A scoped caller
			// only sets limits on keys inside its tenant; a global or
			// other-scope config reaches past it. The scope and scope_id
			// checked here are the ones sent.
			if err := checkTenant(ctx, func(t tenant) error {
				if scope != tables.ModelConfigScopeVirtualKey || !hasScopeID {
					return fmt.Errorf("a virtual key scoped to %s can only create model configs with scope virtual_key on one of its own keys", t)
				}
				owned, err := t.ownsVirtualKeyID(ctx, gov, scopeID)
				return requireOwned(owned, err, t, fmt.Sprintf("virtual key %q", scopeID))
			}); err != nil {
				return nil, err
			}
			body := adminBody{"scope": scope}.copySet(args, "model_name", "provider")
			if hasScopeID {
				body["scope_id"] = scopeID
			}
			if budget := nestedBudgetBody(args); budget != nil {
				body["budgets"] = []any{budget}
			}
			if rateLimit := nestedRateLimitBody(args, nil); rateLimit != nil {
				body["rate_limit"] = rateLimit
			}
			response, err := callAdmin(ctx, deps, http.MethodPost, "/api/governance/model-configs", body)
			if err != nil {
				return nil, err
			}
			return describeModelConfigResponse(response)
		},
	}
}

func updateModelConfigTool() Tool {
	return Tool{
		name: "update_model_config",
		description: "Rename a model config's model/provider or replace its nested rate limit. Scope cannot be changed — create a new config instead. " +
			"Model names are compared case-insensitively with any provider prefix stripped, and a provider-less config is keyed by its base model (a dated alias counts as its base), so a config that would key the same as an existing one in the scope is refused with 409 - update that one instead (list_model_configs shows it).",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "model_config_id": {"type": "string", "minLength": 1},
    "model_name": {"type": "string"},
    "provider": {"type": "string"},
    "token_max_limit": {"type": "integer", "minimum": 1},
    "token_reset_duration": {"type": "string"},
    "request_max_limit": {"type": "integer", "minimum": 1},
    "request_reset_duration": {"type": "string"}
  },
  "required": ["model_config_id"]
}`,
		noLogs:       true,
		mutating:     true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := stringArg(args, "model_config_id")
			if err != nil {
				return nil, err
			}
			path, err := adminPath("/api/governance/model-configs/{mc_id}", id)
			if err != nil {
				return nil, err
			}
			mc, err := gov.GetModelConfigByID(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no model config with id %q", id)
				}
				return nil, fmt.Errorf("model config lookup failed: %w", err)
			}
			// The route does not know the caller's tenant. Nothing sent below
			// can move the config to another owner: scope is not editable.
			if err := checkTenant(ctx, func(t tenant) error {
				owned, err := t.ownsModelConfig(ctx, gov, mc)
				return requireOwned(owned, err, t, fmt.Sprintf("model config %q", id))
			}); err != nil {
				return nil, err
			}
			body := adminBody{}.copySet(args, "model_name", "provider")
			// The route replaces the rate limit with what it is sent, so the
			// fields the caller did not name are sent as stored.
			stored := mc.RateLimit
			if stored == nil && mc.RateLimitID != nil && *mc.RateLimitID != "" {
				if stored, err = gov.GetRateLimit(ctx, *mc.RateLimitID); err != nil {
					return nil, fmt.Errorf("rate limit lookup failed: %w", err)
				}
			}
			if rateLimit := nestedRateLimitBody(args, stored); rateLimit != nil {
				body["rate_limit"] = rateLimit
			}
			response, err := callAdmin(ctx, deps, http.MethodPut, path, body)
			if err != nil {
				return nil, err
			}
			return describeModelConfigResponse(response)
		},
	}
}

// describeModelConfigResponse projects the config a model config route answers with.
func describeModelConfigResponse(response []byte) (any, error) {
	var mc tables.TableModelConfig
	if err := decodeAdminField(response, "model_config", &mc); err != nil {
		return nil, err
	}
	return describeModelConfig(&mc), nil
}

func describeModelConfig(mc *tables.TableModelConfig) map[string]any {
	out := map[string]any{
		"id":         mc.ID,
		"model_name": mc.ModelName,
		"scope":      mc.Scope,
	}
	if mc.Provider != nil {
		out["provider"] = *mc.Provider
	}
	if mc.ScopeID != nil {
		out["scope_id"] = *mc.ScopeID
	}
	if len(mc.Budgets) > 0 {
		budgets := make([]map[string]any, len(mc.Budgets))
		for i, budget := range mc.Budgets {
			budgets[i] = budgetSummary(budget)
		}
		out["budgets"] = budgets
	}
	if mc.RateLimit != nil {
		out["rate_limit"] = rateLimitSummary(*mc.RateLimit)
	} else if mc.RateLimitID != nil {
		out["rate_limit_id"] = *mc.RateLimitID
	}
	return out
}

// rateLimitFields are the arguments a tool takes for a rate limit, which are
// also the fields the routes take it by.
var rateLimitFields = []string{"token_max_limit", "token_reset_duration", "request_max_limit", "request_reset_duration"}

// nestedRateLimitBody builds the rate_limit a route takes from the flat
// arguments, over stored when the route replaces rather than patches. It is nil
// when the caller named no rate-limit argument, which the routes read as "leave
// alone".
func nestedRateLimitBody(args map[string]any, stored *tables.TableRateLimit) adminBody {
	named := false
	for _, field := range rateLimitFields {
		if value, present := args[field]; present && value != nil {
			named = true
		}
	}
	if !named {
		return nil
	}
	body := adminBody{}
	if stored != nil {
		if stored.TokenMaxLimit != nil {
			body["token_max_limit"] = *stored.TokenMaxLimit
		}
		if stored.TokenResetDuration != nil {
			body["token_reset_duration"] = *stored.TokenResetDuration
		}
		if stored.RequestMaxLimit != nil {
			body["request_max_limit"] = *stored.RequestMaxLimit
		}
		if stored.RequestResetDuration != nil {
			body["request_reset_duration"] = *stored.RequestResetDuration
		}
	}
	return body.copySet(args, rateLimitFields...)
}

// nestedBudgetBody builds the one budget a tool's flat arguments describe, or
// nil when the caller named neither half of it.
func nestedBudgetBody(args map[string]any) adminBody {
	body := adminBody{}.copySet(args, "max_limit", "reset_duration")
	if len(body) == 0 {
		return nil
	}
	return body
}
