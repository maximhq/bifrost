package mcptools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
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
		name:        "create_model_config",
		description: "Create a model-level budget/rate-limit config. scope is global (default) or virtual_key (requires scope_id). model_name may be * for every model on the provider.",
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
			modelName, err := stringArg(args, "model_name")
			if err != nil {
				return nil, err
			}
			scope := tables.ModelConfigScopeGlobal
			if s, ok, err := optionalStringArg(args, "scope"); err != nil {
				return nil, err
			} else if ok {
				scope = s
			}
			if !tables.IsValidModelConfigScope(scope) {
				return nil, fmt.Errorf("invalid scope %q", scope)
			}
			scopeID, hasScopeID, err := optionalStringArg(args, "scope_id")
			if err != nil {
				return nil, err
			}
			// A scoped caller only sets limits on keys inside its tenant; a
			// global or other-scope config reaches past it.
			if err := checkTenant(ctx, func(t tenant) error {
				if scope != tables.ModelConfigScopeVirtualKey || !hasScopeID {
					return fmt.Errorf("a virtual key scoped to %s can only create model configs with scope virtual_key on one of its own keys", t)
				}
				owned, err := t.ownsVirtualKeyID(ctx, gov, scopeID)
				return requireOwned(owned, err, t, fmt.Sprintf("virtual key %q", scopeID))
			}); err != nil {
				return nil, err
			}
			var scopeIDPtr *string
			calendarAligned := false
			if scope == tables.ModelConfigScopeGlobal {
				if hasScopeID {
					return nil, fmt.Errorf("scope_id must be omitted when scope is global")
				}
			} else {
				if !hasScopeID {
					return nil, fmt.Errorf("scope_id is required when scope is %q", scope)
				}
				scopeIDPtr = &scopeID
				if scope == tables.ModelConfigScopeVirtualKey {
					vk, err := gov.GetVirtualKey(ctx, scopeID)
					if err != nil {
						if errors.Is(err, configstore.ErrNotFound) {
							return nil, fmt.Errorf("no virtual key with id %q", scopeID)
						}
						return nil, fmt.Errorf("virtual key lookup failed: %w", err)
					}
					calendarAligned = vk.CalendarAligned
				}
			}
			provider, hasProvider, err := optionalStringArg(args, "provider")
			if err != nil {
				return nil, err
			}
			var providerPtr *string
			if hasProvider {
				providerPtr = &provider
			}
			now := time.Now()
			mc := &tables.TableModelConfig{
				ID:              uuid.NewString(),
				ModelName:       modelName,
				Provider:        providerPtr,
				Scope:           scope,
				ScopeID:         scopeIDPtr,
				CalendarAligned: calendarAligned,
				CreatedAt:       now,
				UpdatedAt:       now,
			}
			// Both nested inputs are validated before the first write, and the
			// three rows go in one transaction, so a bad budget or a duplicate
			// config never leaves an orphaned rate limit behind.
			rateLimit, err := optionalNestedRateLimit(args)
			if err != nil {
				return nil, err
			}
			budget, err := optionalNestedBudget(args, mc.ID, calendarAligned)
			if err != nil {
				return nil, err
			}
			err = gov.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
				if rateLimit != nil {
					if err := gov.CreateRateLimit(ctx, rateLimit, tx); err != nil {
						return fmt.Errorf("create rate limit failed: %w", err)
					}
					mc.RateLimitID = &rateLimit.ID
					mc.RateLimit = rateLimit
				}
				if err := gov.CreateModelConfig(ctx, mc, tx); err != nil {
					if errors.Is(err, configstore.ErrAlreadyExists) {
						return fmt.Errorf("a model config for %q already exists in this scope", modelName)
					}
					return fmt.Errorf("create model config failed: %w", err)
				}
				if budget != nil {
					if err := gov.CreateBudget(ctx, budget, tx); err != nil {
						return fmt.Errorf("create budget failed: %w", err)
					}
					mc.Budgets = []tables.TableBudget{*budget}
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			if reloader := requireReloader(deps); reloader != nil {
				if reloaded, err := reloader.ReloadModelConfig(ctx, mc.ID); err == nil && reloaded != nil {
					mc = reloaded
				}
			}
			return describeModelConfig(mc), nil
		},
	}
}

func updateModelConfigTool() Tool {
	return Tool{
		name:        "update_model_config",
		description: "Rename a model config's model/provider or replace its nested rate limit. Scope cannot be changed — create a new config instead.",
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
			mc, err := gov.GetModelConfigByID(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no model config with id %q", id)
				}
				return nil, fmt.Errorf("model config lookup failed: %w", err)
			}
			if err := checkTenant(ctx, func(t tenant) error {
				owned, err := t.ownsModelConfig(ctx, gov, mc)
				return requireOwned(owned, err, t, fmt.Sprintf("model config %q", id))
			}); err != nil {
				return nil, err
			}
			if name, ok, err := optionalStringArg(args, "model_name"); err != nil {
				return nil, err
			} else if ok {
				mc.ModelName = name
			}
			if provider, ok, err := optionalStringArg(args, "provider"); err != nil {
				return nil, err
			} else if ok {
				mc.Provider = &provider
			}
			patch, err := optionalNestedRateLimit(args)
			if err != nil {
				return nil, err
			}
			// One transaction, so a failed model-config write cannot leave a new
			// or changed rate limit behind.
			err = gov.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
				if patch != nil {
					if mc.RateLimitID != nil && *mc.RateLimitID != "" {
						existing, err := gov.GetRateLimit(ctx, *mc.RateLimitID, tx)
						if err != nil {
							return fmt.Errorf("rate limit lookup failed: %w", err)
						}
						if patch.TokenMaxLimit != nil {
							existing.TokenMaxLimit = patch.TokenMaxLimit
						}
						if patch.TokenResetDuration != nil {
							existing.TokenResetDuration = patch.TokenResetDuration
						}
						if patch.RequestMaxLimit != nil {
							existing.RequestMaxLimit = patch.RequestMaxLimit
						}
						if patch.RequestResetDuration != nil {
							existing.RequestResetDuration = patch.RequestResetDuration
						}
						if err := gov.UpdateRateLimit(ctx, existing, tx); err != nil {
							return fmt.Errorf("update rate limit failed: %w", err)
						}
						mc.RateLimit = existing
					} else {
						if err := gov.CreateRateLimit(ctx, patch, tx); err != nil {
							return fmt.Errorf("create rate limit failed: %w", err)
						}
						mc.RateLimitID = &patch.ID
						mc.RateLimit = patch
					}
				}
				mc.UpdatedAt = time.Now()
				if err := gov.UpdateModelConfig(ctx, mc, tx); err != nil {
					return fmt.Errorf("update model config failed: %w", err)
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			if reloader := requireReloader(deps); reloader != nil {
				if reloaded, err := reloader.ReloadModelConfig(ctx, mc.ID); err == nil && reloaded != nil {
					mc = reloaded
				}
			}
			return describeModelConfig(mc), nil
		},
	}
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

func optionalNestedRateLimit(args map[string]any) (*tables.TableRateLimit, error) {
	_, hasToken := args["token_max_limit"]
	_, hasRequest := args["request_max_limit"]
	if !hasToken && !hasRequest {
		return nil, nil
	}
	rl, err := rateLimitFromArgs(args)
	if err != nil {
		return nil, err
	}
	if rl.TokenMaxLimit == nil && rl.RequestMaxLimit == nil {
		return nil, nil
	}
	rl.ID = uuid.NewString()
	now := time.Now()
	rl.TokenLastReset = now
	rl.RequestLastReset = now
	return rl, nil
}

func optionalNestedBudget(args map[string]any, modelConfigID string, calendarAligned bool) (*tables.TableBudget, error) {
	_, hasLimit := args["max_limit"]
	_, hasReset := args["reset_duration"]
	if !hasLimit && !hasReset {
		return nil, nil
	}
	maxLimit, err := floatArg(args, "max_limit")
	if err != nil {
		return nil, err
	}
	if maxLimit < 0 {
		return nil, fmt.Errorf("max_limit cannot be negative")
	}
	reset, err := stringArg(args, "reset_duration")
	if err != nil {
		return nil, err
	}
	if d, err := tables.ParseDuration(reset); err != nil || d <= 0 {
		return nil, fmt.Errorf("invalid reset_duration %q (use forms like 1d, 1w, 1M)", reset)
	}
	now := time.Now()
	if calendarAligned {
		now = tables.GetCalendarPeriodStart(reset, now, time.January)
	}
	return &tables.TableBudget{
		ID:            uuid.NewString(),
		MaxLimit:      maxLimit,
		ResetDuration: reset,
		ModelConfigID: &modelConfigID,
		LastReset:     now,
	}, nil
}
