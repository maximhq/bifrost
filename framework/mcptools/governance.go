package mcptools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// describeVirtualKeyTool looks up one virtual key's budget, rate limit and
// allowed providers - the natural follow-up to "how much has this key spent"
// (query_usage_by) that has no way to answer before: whether there is room
// left, not just how much has gone by.
//
// It never returns tables.TableVirtualKey (or any of its relations) directly.
// That struct carries the key's own secret value and its rotation history -
// exactly the kind of key material a tool must never surface - so
// describeVirtualKey below hand-picks only the budget/limit/provider fields
// onto a fresh map instead of ever serializing the row itself.
func describeVirtualKeyTool() Tool {
	return Tool{
		name: "describe_virtual_key",
		description: "Look up one virtual key's budget, rate limit and allowed providers/models - its configured room, not its traffic. " +
			"Use query_usage_by with dimension virtual_key for what it has actually spent; use this for what it is allowed to spend or call before it is throttled. " +
			"Needs the key's id, as returned by describe_filter_space's virtual_keys list - a name is not enough.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "virtual_key_id": {"type": "string", "description": "The virtual key's id, from describe_filter_space."}
  },
  "required": ["virtual_key_id"]
}`,
		noLogs:       true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			if deps.Governance == nil {
				return nil, fmt.Errorf("virtual key detail is not available on this deployment")
			}
			id, _ := args["virtual_key_id"].(string)
			id = strings.TrimSpace(id)
			if id == "" {
				return nil, fmt.Errorf("virtual_key_id is required")
			}
			notFound := fmt.Errorf("no virtual key with id %q - describe_filter_space lists the real ones", id)
			vk, err := deps.Governance.GetVirtualKey(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, notFound
				}
				return nil, fmt.Errorf("virtual key lookup failed: %w", err)
			}
			if err := hiddenOutsideTenant(ctx, true, func(t tenant) (bool, error) {
				return t.ownsVirtualKey(ctx, deps.Governance, vk)
			}, notFound); err != nil {
				return nil, err
			}
			return describeVirtualKey(vk), nil
		},
	}
}

// describeVirtualKey projects the safe subset of a virtual key row. Every
// field it reads is picked by name - there is no struct marshal of vk or any
// of its relations anywhere in this function, which is what keeps Value,
// PreviousValue, ValueHash and EncryptionStatus (and every provider key
// beneath ProviderConfigs) out of a tool result by construction rather than by
// remembering to strip them.
func describeVirtualKey(vk *tables.TableVirtualKey) map[string]any {
	out := map[string]any{
		"id":                  vk.ID,
		"name":                vk.Name,
		"is_active":           vk.IsActiveValue(),
		"allow_all_providers": vk.AllowAllProviders,
	}
	if vk.Description != "" {
		out["description"] = vk.Description
	}
	if vk.ExpiresAt != nil {
		out["expires_at"] = *vk.ExpiresAt
	}
	if vk.TeamID != nil {
		out["team_id"] = *vk.TeamID
	}
	if vk.CustomerID != nil {
		out["customer_id"] = *vk.CustomerID
	}
	if len(vk.Budgets) > 0 {
		budgets := make([]map[string]any, len(vk.Budgets))
		for i, budget := range vk.Budgets {
			budgets[i] = budgetSummary(budget)
		}
		out["budgets"] = budgets
	}
	if vk.RateLimit != nil {
		out["rate_limit"] = rateLimitSummary(*vk.RateLimit)
	}
	if len(vk.ProviderConfigs) > 0 {
		providers := make([]map[string]any, len(vk.ProviderConfigs))
		for i, config := range vk.ProviderConfigs {
			providers[i] = providerConfigSummary(config)
		}
		out["providers"] = providers
	}
	return out
}

// budgetSummary reports a budget the way an operator reads it: what it is
// capped at right now (EffectiveMaxLimit, which folds in an active override
// rather than the raw MaxLimit an override has already changed), what has
// been spent against that cap, and when it next resets.
func budgetSummary(budget tables.TableBudget) map[string]any {
	out := map[string]any{
		"id":             budget.ID,
		"max_limit":      budget.EffectiveMaxLimit(),
		"current_usage":  budget.CurrentUsage,
		"reset_duration": budget.ResetDuration,
		"last_reset":     budget.LastReset,
	}
	if budget.HasActiveOverride() {
		out["override_active"] = true
	}
	return out
}

// rateLimitSummary reports only the limit family (token, request) that is
// actually configured - a limit whose MaxLimit is nil is unset, not zero, and
// including it anyway would read as a rate limit of zero requests allowed.
func rateLimitSummary(limit tables.TableRateLimit) map[string]any {
	out := map[string]any{}
	if limit.TokenMaxLimit != nil {
		out["token_max_limit"] = *limit.TokenMaxLimit
		out["token_current_usage"] = limit.TokenCurrentUsage
		if limit.TokenResetDuration != nil {
			out["token_reset_duration"] = *limit.TokenResetDuration
		}
	}
	if limit.RequestMaxLimit != nil {
		out["request_max_limit"] = *limit.RequestMaxLimit
		out["request_current_usage"] = limit.RequestCurrentUsage
		if limit.RequestResetDuration != nil {
			out["request_reset_duration"] = *limit.RequestResetDuration
		}
	}
	return out
}

// providerConfigSummary reports which models a provider is scoped to under
// this key. Keys is deliberately never touched: even the narrower preload
// used elsewhere (id, name, key_id, provider) is more than a chat tool needs
// to say, and the actual credential is never in reach of this struct at all.
func providerConfigSummary(config tables.TableVirtualKeyProviderConfig) map[string]any {
	out := map[string]any{"provider": config.Provider}
	if len(config.AllowedModels) > 0 {
		out["allowed_models"] = []string(config.AllowedModels)
	}
	if len(config.BlacklistedModels) > 0 {
		out["blacklisted_models"] = []string(config.BlacklistedModels)
	}
	if len(config.Budgets) > 0 {
		budgets := make([]map[string]any, len(config.Budgets))
		for i, budget := range config.Budgets {
			budgets[i] = budgetSummary(budget)
		}
		out["budgets"] = budgets
	}
	if config.RateLimit != nil {
		out["rate_limit"] = rateLimitSummary(*config.RateLimit)
	}
	return out
}

func generateVirtualKeyValue() string {
	return VirtualKeyPrefix + uuid.NewString()
}

func listLimit(args map[string]any) (int, error) {
	return intArg(args, "limit", 10, MaxGovernanceRows)
}

func listVirtualKeysTool() Tool {
	return Tool{
		name:        "list_virtual_keys",
		description: "List virtual keys (id, name, active, team/customer). Never returns the secret value. Use describe_virtual_key for budget and providers.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "search": {"type": "string"},
    "team_id": {"type": "string"},
    "customer_id": {"type": "string"},
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
			teamID, _, err := optionalStringArg(args, "team_id")
			if err != nil {
				return nil, err
			}
			customerID, _, err := optionalStringArg(args, "customer_id")
			if err != nil {
				return nil, err
			}
			var rows []tables.TableVirtualKey
			var total int64
			if t, known, err := readTenant(ctx, true); err != nil {
				return nil, err
			} else if known {
				// A scoped caller lists its tenant's keys, filtered here: the
				// store's own filters cannot say "this team, or any team under
				// this customer".
				index, err := t.index(ctx, gov)
				if err != nil {
					return nil, err
				}
				for _, vk := range index.keys {
					if teamID != "" && (vk.TeamID == nil || *vk.TeamID != teamID) {
						continue
					}
					if customerID != "" && (vk.CustomerID == nil || *vk.CustomerID != customerID) {
						continue
					}
					if !matchesSearch(vk.Name, search) {
						continue
					}
					total++
					if len(rows) < limit {
						rows = append(rows, vk)
					}
				}
			} else {
				rows, total, err = gov.GetVirtualKeysPaginated(ctx, configstore.VirtualKeyQueryParams{
					Limit: limit, Search: search, TeamID: teamID, CustomerID: customerID,
				})
				if err != nil {
					return nil, fmt.Errorf("list virtual keys failed: %w", err)
				}
			}
			out := make([]map[string]any, 0, len(rows))
			for i := range rows {
				item := describeVirtualKey(&rows[i])
				delete(item, "budgets")
				delete(item, "rate_limit")
				delete(item, "providers")
				out = append(out, item)
			}
			return map[string]any{"virtual_keys": out, "total": total, "returned": len(out)}, nil
		},
	}
}

func listTeamsTool() Tool {
	return Tool{
		name:        "list_teams",
		description: "List teams (id, name, customer, budget summaries). Use describe_team for one team.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "search": {"type": "string"},
    "customer_id": {"type": "string"},
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
			customerID, _, err := optionalStringArg(args, "customer_id")
			if err != nil {
				return nil, err
			}
			var rows []tables.TableTeam
			var total int64
			if t, known, err := readTenant(ctx, true); err != nil {
				return nil, err
			} else if known {
				index, err := t.index(ctx, gov)
				if err != nil {
					return nil, err
				}
				for _, team := range index.teams {
					if customerID != "" && (team.CustomerID == nil || *team.CustomerID != customerID) {
						continue
					}
					if !matchesSearch(team.Name, search) {
						continue
					}
					total++
					if len(rows) < limit {
						rows = append(rows, team)
					}
				}
			} else {
				rows, total, err = gov.GetTeamsPaginated(ctx, configstore.TeamsQueryParams{
					Limit: limit, Search: search, CustomerID: customerID,
				})
				if err != nil {
					return nil, fmt.Errorf("list teams failed: %w", err)
				}
			}
			out := make([]map[string]any, 0, len(rows))
			for i := range rows {
				out = append(out, describeTeam(&rows[i]))
			}
			return map[string]any{"teams": out, "total": total, "returned": len(out)}, nil
		},
	}
}

func describeTeamTool() Tool {
	return Tool{
		name:        "describe_team",
		description: "One team's budgets, rate limit and virtual-key count.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "team_id": {"type": "string"}
  },
  "required": ["team_id"]
}`,
		noLogs:       true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := stringArg(args, "team_id")
			if err != nil {
				return nil, err
			}
			notFound := fmt.Errorf("no team with id %q", id)
			team, err := gov.GetTeam(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, notFound
				}
				return nil, fmt.Errorf("team lookup failed: %w", err)
			}
			if err := hiddenOutsideTenant(ctx, true, func(t tenant) (bool, error) {
				return t.ownsTeam(ctx, gov, team.ID)
			}, notFound); err != nil {
				return nil, err
			}
			return describeTeam(team), nil
		},
	}
}

func listCustomersTool() Tool {
	return Tool{
		name:        "list_customers",
		description: "List customers (id, name, budget summaries).",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "search": {"type": "string"},
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
			var rows []tables.TableCustomer
			var total int64
			if t, known, err := readTenant(ctx, true); err != nil {
				return nil, err
			} else if known {
				// Only a customer key has a customer of its own to list; a team
				// key owns none, not even its team's.
				index, err := t.index(ctx, gov)
				if err != nil {
					return nil, err
				}
				if index.customer != nil && matchesSearch(index.customer.Name, search) {
					rows, total = []tables.TableCustomer{*index.customer}, 1
				}
			} else {
				rows, total, err = gov.GetCustomersPaginated(ctx, configstore.CustomersQueryParams{
					Limit: limit, Search: search,
				})
				if err != nil {
					return nil, fmt.Errorf("list customers failed: %w", err)
				}
			}
			out := make([]map[string]any, 0, len(rows))
			for i := range rows {
				out = append(out, describeCustomer(&rows[i]))
			}
			return map[string]any{"customers": out, "total": total, "returned": len(out)}, nil
		},
	}
}

func describeCustomerTool() Tool {
	return Tool{
		name:        "describe_customer",
		description: "One customer's budgets, rate limit and virtual-key count.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "customer_id": {"type": "string"}
  },
  "required": ["customer_id"]
}`,
		noLogs:       true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := stringArg(args, "customer_id")
			if err != nil {
				return nil, err
			}
			notFound := fmt.Errorf("no customer with id %q", id)
			customer, err := gov.GetCustomer(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, notFound
				}
				return nil, fmt.Errorf("customer lookup failed: %w", err)
			}
			if err := hiddenOutsideTenant(ctx, true, func(t tenant) (bool, error) {
				return t.ownsCustomer(customer.ID), nil
			}, notFound); err != nil {
				return nil, err
			}
			return describeCustomer(customer), nil
		},
	}
}

func listBudgetsTool() Tool {
	return Tool{
		name:        "list_budgets",
		description: "List budgets (id, cap, usage, reset, owner). VK-owned budgets live on model configs and may not appear here.",
		schemaJSON: `{
  "type": "object",
  "properties": {
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
			rows, err := gov.GetBudgets(ctx)
			if err != nil {
				return nil, fmt.Errorf("list budgets failed: %w", err)
			}
			// The budget store applies no row filter, so a scoped caller's
			// list is cut to its tenant here, before the limit.
			if t, known, err := readTenant(ctx, false); err != nil {
				return nil, err
			} else if known {
				index, err := t.index(ctx, gov)
				if err != nil {
					return nil, err
				}
				rows = slices.DeleteFunc(rows, func(b tables.TableBudget) bool { return !index.ownsBudget(&b) })
			}
			if len(rows) > limit {
				rows = rows[:limit]
			}
			out := make([]map[string]any, 0, len(rows))
			for i := range rows {
				out = append(out, describeBudget(&rows[i]))
			}
			return map[string]any{"budgets": out, "returned": len(out)}, nil
		},
	}
}

func describeBudgetTool() Tool {
	return Tool{
		name:        "describe_budget",
		description: "One budget: cap, current usage, reset window and owner.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "budget_id": {"type": "string"}
  },
  "required": ["budget_id"]
}`,
		noLogs:       true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := stringArg(args, "budget_id")
			if err != nil {
				return nil, err
			}
			notFound := fmt.Errorf("no budget with id %q", id)
			budget, err := gov.GetBudget(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, notFound
				}
				return nil, fmt.Errorf("budget lookup failed: %w", err)
			}
			if err := hiddenOutsideTenant(ctx, false, func(t tenant) (bool, error) {
				return t.ownsBudget(ctx, gov, budget)
			}, notFound); err != nil {
				return nil, err
			}
			return describeBudget(budget), nil
		},
	}
}

func createVirtualKeyTool() Tool {
	return Tool{
		name:        "create_virtual_key",
		description: "Create a virtual key. Returns the sk-bf- secret once; store it. Later get/list calls never include it. Attach to a team or a customer, not both.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 1},
    "description": {"type": "string"},
    "team_id": {"type": "string"},
    "customer_id": {"type": "string"},
    "allow_all_providers": {"type": "boolean", "description": "Defaults to true so the key can reach every configured provider."}
  },
  "required": ["name"]
}`,
		noLogs:       true,
		mutating:     true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			name, err := stringArg(args, "name")
			if err != nil {
				return nil, err
			}
			description, _, err := optionalStringArg(args, "description")
			if err != nil {
				return nil, err
			}
			teamID, hasTeam, err := optionalStringArg(args, "team_id")
			if err != nil {
				return nil, err
			}
			customerID, hasCustomer, err := optionalStringArg(args, "customer_id")
			if err != nil {
				return nil, err
			}
			if hasTeam && hasCustomer {
				return nil, fmt.Errorf("virtual key cannot be attached to both team and customer")
			}
			// A scoped caller creates keys inside its own tenant: in it by
			// default, and never in a team or customer outside it.
			if err := checkTenant(ctx, func(t tenant) error {
				if !hasTeam && !hasCustomer {
					if t.teamID != "" {
						teamID, hasTeam = t.teamID, true
					} else {
						customerID, hasCustomer = t.customerID, true
					}
				}
				if hasTeam {
					owned, err := t.ownsTeam(ctx, gov, teamID)
					return requireOwned(owned, err, t, fmt.Sprintf("team %q", teamID))
				}
				return requireOwned(t.ownsCustomer(customerID), nil, t, fmt.Sprintf("customer %q", customerID))
			}); err != nil {
				return nil, err
			}
			allowAll := true
			if _, present := args["allow_all_providers"]; present {
				allowAll, err = boolArg(args, "allow_all_providers")
				if err != nil {
					return nil, err
				}
			}
			active := true
			secret := generateVirtualKeyValue()
			vk := &tables.TableVirtualKey{
				ID:                uuid.NewString(),
				Name:              name,
				Description:       description,
				Value:             *schemas.NewSecretVar(secret),
				IsActive:          &active,
				AllowAllProviders: allowAll,
			}
			if hasTeam {
				vk.TeamID = &teamID
			}
			if hasCustomer {
				vk.CustomerID = &customerID
			}
			if err := gov.CreateVirtualKey(ctx, vk); err != nil {
				if errors.Is(err, configstore.ErrAlreadyExists) {
					return nil, fmt.Errorf("a virtual key named %q already exists", name)
				}
				return nil, fmt.Errorf("create virtual key failed: %w", err)
			}
			var reloadErr error
			if reloader := requireReloader(deps); reloader != nil {
				if reloaded, err := reloader.ReloadVirtualKey(ctx, vk.ID); err != nil {
					reloadErr = err
				} else if reloaded != nil {
					vk = reloaded
				}
			}
			out := describeVirtualKey(vk)
			out["value"] = secret
			out["note"] = "this is the only time the secret is returned; store it now"
			// A warning, not an error: the key is stored, and an error result
			// would drop the one copy of its secret.
			if reloadErr != nil {
				out["warning"] = fmt.Sprintf("stored, but the live reload failed, so this key will not authenticate until the next restart: %v", reloadErr)
			}
			return out, nil
		},
	}
}

func updateVirtualKeyTool() Tool {
	return Tool{
		name:        "update_virtual_key",
		description: "Update a virtual key's name, description, active flag, or team/customer. Does not rotate the secret.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "virtual_key_id": {"type": "string"},
    "name": {"type": "string"},
    "description": {"type": "string"},
    "is_active": {"type": "boolean"},
    "team_id": {"type": "string"},
    "customer_id": {"type": "string"},
    "allow_all_providers": {"type": "boolean"}
  },
  "required": ["virtual_key_id"]
}`,
		noLogs:       true,
		mutating:     true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := stringArg(args, "virtual_key_id")
			if err != nil {
				return nil, err
			}
			vk, err := gov.GetVirtualKey(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no virtual key with id %q", id)
				}
				return nil, fmt.Errorf("virtual key lookup failed: %w", err)
			}
			if err := checkTenant(ctx, func(t tenant) error {
				owned, err := t.ownsVirtualKey(ctx, gov, vk)
				return requireOwned(owned, err, t, fmt.Sprintf("virtual key %q", id))
			}); err != nil {
				return nil, err
			}
			if name, ok, err := optionalStringArg(args, "name"); err != nil {
				return nil, err
			} else if ok {
				vk.Name = name
			}
			if description, ok, err := optionalStringArg(args, "description"); err != nil {
				return nil, err
			} else if ok {
				vk.Description = description
			}
			if _, present := args["is_active"]; present {
				active, err := boolArg(args, "is_active")
				if err != nil {
					return nil, err
				}
				vk.IsActive = &active
			}
			if _, present := args["allow_all_providers"]; present {
				allowAll, err := boolArg(args, "allow_all_providers")
				if err != nil {
					return nil, err
				}
				vk.AllowAllProviders = allowAll
			}
			teamID, hasTeam, err := optionalStringArg(args, "team_id")
			if err != nil {
				return nil, err
			}
			customerID, hasCustomer, err := optionalStringArg(args, "customer_id")
			if err != nil {
				return nil, err
			}
			if hasTeam && hasCustomer {
				return nil, fmt.Errorf("virtual key cannot be attached to both team and customer")
			}
			// Moving a key is checked on both ends: it must be the caller's to
			// move, and it may only land inside the caller's tenant.
			if err := checkTenant(ctx, func(t tenant) error {
				if hasTeam {
					owned, err := t.ownsTeam(ctx, gov, teamID)
					return requireOwned(owned, err, t, fmt.Sprintf("team %q", teamID))
				}
				if hasCustomer {
					return requireOwned(t.ownsCustomer(customerID), nil, t, fmt.Sprintf("customer %q", customerID))
				}
				return nil
			}); err != nil {
				return nil, err
			}
			if hasTeam {
				vk.TeamID = &teamID
				vk.CustomerID = nil
			}
			if hasCustomer {
				vk.CustomerID = &customerID
				vk.TeamID = nil
			}
			if err := gov.UpdateVirtualKey(ctx, vk); err != nil {
				return nil, fmt.Errorf("update virtual key failed: %w", err)
			}
			// The in-memory key is what authenticates. A failed reload after a
			// deactivate leaves it working, so it is an error, as it is on the
			// HTTP handler - not a success over a stale cache.
			if reloader := requireReloader(deps); reloader != nil {
				reloaded, err := reloader.ReloadVirtualKey(ctx, vk.ID)
				if err != nil {
					return nil, fmt.Errorf("updated in the database, but the live key was not reloaded, so its previous state (including whether it is active) holds until the next restart: %w", err)
				}
				if reloaded != nil {
					vk = reloaded
				}
			}
			return describeVirtualKey(vk), nil
		},
	}
}

func deactivateVirtualKeyTool() Tool {
	return Tool{
		name:        "deactivate_virtual_key",
		description: "Deactivate a virtual key so it stops authenticating. Does not delete the row or rotate the secret.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "virtual_key_id": {"type": "string"}
  },
  "required": ["virtual_key_id"]
}`,
		noLogs:       true,
		mutating:     true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			args["is_active"] = false
			return updateVirtualKeyTool().execute(ctx, deps, args)
		},
	}
}

func rotateVirtualKeyTool() Tool {
	return Tool{
		name:        "rotate_virtual_key",
		description: "Issue a new sk-bf- secret for a virtual key. Returns the new value once. The previous value stays valid for vk_rotation_cooldown (or stops immediately when that is 0).",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "virtual_key_id": {"type": "string"}
  },
  "required": ["virtual_key_id"]
}`,
		noLogs:       true,
		mutating:     true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := stringArg(args, "virtual_key_id")
			if err != nil {
				return nil, err
			}
			vk, err := gov.GetVirtualKey(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no virtual key with id %q", id)
				}
				return nil, fmt.Errorf("virtual key lookup failed: %w", err)
			}
			if err := checkTenant(ctx, func(t tenant) error {
				owned, err := t.ownsVirtualKey(ctx, gov, vk)
				return requireOwned(owned, err, t, fmt.Sprintf("virtual key %q", id))
			}); err != nil {
				return nil, err
			}
			// Read before anything is changed. A failed read must not quietly
			// become a zero cooldown, which would cut the old secret off the
			// moment this returns.
			cooldown := time.Duration(0)
			cfg, err := gov.GetClientConfig(ctx)
			if err != nil {
				return nil, fmt.Errorf("reading vk_rotation_cooldown failed, so the key was not rotated: %w", err)
			}
			if cfg != nil {
				cooldown = cfg.VKRotationCooldown.D()
			}
			oldValue := vk.Value.GetValue()
			secret := generateVirtualKeyValue()
			if secret == oldValue {
				return nil, fmt.Errorf("generated virtual key matched existing value")
			}
			vk.Value = *schemas.NewSecretVar(secret)
			now := Now()
			vk.RotatedAt = &now
			if cooldown > 0 {
				vk.PreviousValue = *schemas.NewSecretVar(oldValue)
				expires := now.Add(cooldown)
				vk.PreviousValueExpiresAt = &expires
			} else {
				vk.ClearPreviousValue()
			}
			if err := gov.UpdateVirtualKey(ctx, vk); err != nil {
				return nil, fmt.Errorf("rotate virtual key failed: %w", err)
			}
			var reloadErr error
			if reloader := requireReloader(deps); reloader != nil {
				if reloaded, err := reloader.ReloadVirtualKey(ctx, vk.ID); err != nil {
					reloadErr = err
				} else if reloaded != nil {
					vk = reloaded
				}
			}
			out := describeVirtualKey(vk)
			out["value"] = secret
			out["note"] = "this is the only time the new secret is returned; store it now"
			// The new secret is stored and must reach the caller, so a failed
			// reload is a loud warning rather than an error that would drop it.
			if reloadErr != nil {
				out["warning"] = fmt.Sprintf("rotated in the database, but the live key was not reloaded: until the next restart the OLD secret keeps authenticating and the new one does not: %v", reloadErr)
			}
			if vk.PreviousValueExpiresAt != nil {
				out["previous_value_expires_at"] = *vk.PreviousValueExpiresAt
			}
			return out, nil
		},
	}
}

func createTeamTool() Tool {
	return Tool{
		name:        "create_team",
		description: "Create a team. Optionally attach it to a customer.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 1},
    "customer_id": {"type": "string"}
  },
  "required": ["name"]
}`,
		noLogs:       true,
		mutating:     true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			name, err := stringArg(args, "name")
			if err != nil {
				return nil, err
			}
			customerID, hasCustomer, err := optionalStringArg(args, "customer_id")
			if err != nil {
				return nil, err
			}
			// A team key cannot create sibling teams; a customer key creates
			// teams under its own customer, by default and only there.
			if err := checkTenant(ctx, func(t tenant) error {
				if t.teamID != "" {
					return fmt.Errorf("a virtual key scoped to %s cannot create teams", t)
				}
				if !hasCustomer {
					customerID, hasCustomer = t.customerID, true
				}
				return requireOwned(t.ownsCustomer(customerID), nil, t, fmt.Sprintf("customer %q", customerID))
			}); err != nil {
				return nil, err
			}
			team := &tables.TableTeam{ID: uuid.NewString(), Name: name}
			if hasCustomer {
				team.CustomerID = &customerID
			}
			if err := gov.CreateTeam(ctx, team); err != nil {
				if errors.Is(err, configstore.ErrAlreadyExists) {
					return nil, fmt.Errorf("a team named %q already exists", name)
				}
				return nil, fmt.Errorf("create team failed: %w", err)
			}
			if reloader := requireReloader(deps); reloader != nil {
				if reloaded, err := reloader.ReloadTeam(ctx, team.ID); err == nil && reloaded != nil {
					team = reloaded
				}
			}
			return describeTeam(team), nil
		},
	}
}

func createCustomerTool() Tool {
	return Tool{
		name:        "create_customer",
		description: "Create a customer.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 1}
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
			customer := &tables.TableCustomer{ID: uuid.NewString(), Name: name}
			if err := gov.CreateCustomer(ctx, customer); err != nil {
				if errors.Is(err, configstore.ErrAlreadyExists) {
					return nil, fmt.Errorf("a customer named %q already exists", name)
				}
				return nil, fmt.Errorf("create customer failed: %w", err)
			}
			if reloader := requireReloader(deps); reloader != nil {
				if reloaded, err := reloader.ReloadCustomer(ctx, customer.ID); err == nil && reloaded != nil {
					customer = reloaded
				}
			}
			return describeCustomer(customer), nil
		},
	}
}

func createBudgetTool() Tool {
	return Tool{
		name:        "create_budget",
		description: "Create a budget on a team or a customer. Virtual-key budgets live on model configs and are not created here. reset_duration uses forms like 1d, 1w, 1M.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "max_limit": {"type": "number", "minimum": 0},
    "reset_duration": {"type": "string", "minLength": 1},
    "team_id": {"type": "string"},
    "customer_id": {"type": "string"}
  },
  "required": ["max_limit", "reset_duration"]
}`,
		noLogs:       true,
		mutating:     true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
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
			teamID, hasTeam, err := optionalStringArg(args, "team_id")
			if err != nil {
				return nil, err
			}
			customerID, hasCustomer, err := optionalStringArg(args, "customer_id")
			if err != nil {
				return nil, err
			}
			if hasTeam == hasCustomer {
				return nil, fmt.Errorf("budget must belong to exactly one of team_id or customer_id")
			}
			if err := checkTenant(ctx, func(t tenant) error {
				if hasTeam {
					owned, err := t.ownsTeam(ctx, gov, teamID)
					return requireOwned(owned, err, t, fmt.Sprintf("team %q", teamID))
				}
				return requireOwned(t.ownsCustomer(customerID), nil, t, fmt.Sprintf("customer %q", customerID))
			}); err != nil {
				return nil, err
			}
			budget := &tables.TableBudget{
				ID:            uuid.NewString(),
				MaxLimit:      maxLimit,
				ResetDuration: reset,
				LastReset:     Now(),
			}
			if hasTeam {
				budget.TeamID = &teamID
			} else {
				budget.CustomerID = &customerID
			}
			if err := gov.CreateBudget(ctx, budget); err != nil {
				return nil, fmt.Errorf("create budget failed: %w", err)
			}
			out := describeBudget(budget)
			warnBudgetOwnerReload(out, reloadBudgetOwner(ctx, requireReloader(deps), budget))
			return out, nil
		},
	}
}

func updateBudgetTool() Tool {
	return Tool{
		name:        "update_budget",
		description: "Change a budget's cap or reset window. Does not reset current usage.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "budget_id": {"type": "string"},
    "max_limit": {"type": "number", "minimum": 0},
    "reset_duration": {"type": "string"}
  },
  "required": ["budget_id"]
}`,
		noLogs:       true,
		mutating:     true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := stringArg(args, "budget_id")
			if err != nil {
				return nil, err
			}
			budget, err := gov.GetBudget(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no budget with id %q", id)
				}
				return nil, fmt.Errorf("budget lookup failed: %w", err)
			}
			if err := checkTenant(ctx, func(t tenant) error {
				owned, err := t.ownsBudget(ctx, gov, budget)
				return requireOwned(owned, err, t, fmt.Sprintf("budget %q", id))
			}); err != nil {
				return nil, err
			}
			if _, present := args["max_limit"]; present {
				maxLimit, err := floatArg(args, "max_limit")
				if err != nil {
					return nil, err
				}
				if maxLimit < 0 {
					return nil, fmt.Errorf("max_limit cannot be negative")
				}
				budget.MaxLimit = maxLimit
			}
			if reset, ok, err := optionalStringArg(args, "reset_duration"); err != nil {
				return nil, err
			} else if ok {
				if d, err := tables.ParseDuration(reset); err != nil || d <= 0 {
					return nil, fmt.Errorf("invalid reset_duration %q", reset)
				}
				budget.ResetDuration = reset
			}
			if err := gov.UpdateBudget(ctx, budget); err != nil {
				return nil, fmt.Errorf("update budget failed: %w", err)
			}
			reloader := requireReloader(deps)
			// Virtual-key budgets now hang off model configs; without this
			// a new cap on one is not enforced until restart.
			if reloader != nil && budget.ModelConfigID != nil {
				if _, err := reloader.ReloadModelConfig(ctx, *budget.ModelConfigID); err != nil {
					return nil, fmt.Errorf("updated in the database, but the live model config was not reloaded, so the previous cap is enforced until the next restart: %w", err)
				}
			}
			out := describeBudget(budget)
			warnBudgetOwnerReload(out, reloadBudgetOwner(ctx, reloader, budget))
			return out, nil
		},
	}
}

func describeTeam(team *tables.TableTeam) map[string]any {
	out := map[string]any{
		"id":                team.ID,
		"name":              team.Name,
		"virtual_key_count": team.VirtualKeyCount,
	}
	if team.CustomerID != nil {
		out["customer_id"] = *team.CustomerID
	}
	if len(team.Budgets) > 0 {
		budgets := make([]map[string]any, len(team.Budgets))
		for i, budget := range team.Budgets {
			budgets[i] = budgetSummary(budget)
		}
		out["budgets"] = budgets
	}
	if team.RateLimit != nil {
		out["rate_limit"] = rateLimitSummary(*team.RateLimit)
	}
	return out
}

func describeCustomer(customer *tables.TableCustomer) map[string]any {
	out := map[string]any{
		"id":                customer.ID,
		"name":              customer.Name,
		"virtual_key_count": customer.VirtualKeyCount,
	}
	if len(customer.Budgets) > 0 {
		budgets := make([]map[string]any, len(customer.Budgets))
		for i, budget := range customer.Budgets {
			budgets[i] = budgetSummary(budget)
		}
		out["budgets"] = budgets
	}
	if customer.RateLimit != nil {
		out["rate_limit"] = rateLimitSummary(*customer.RateLimit)
	}
	return out
}

// reloadBudgetOwner refreshes the live copy of whichever team, customer or
// virtual key a budget belongs to, so the new cap is enforced without a restart.
func reloadBudgetOwner(ctx context.Context, reloader GovernanceReloader, budget *tables.TableBudget) error {
	if reloader == nil {
		return nil
	}
	var errs []error
	if budget.TeamID != nil {
		if _, err := reloader.ReloadTeam(ctx, *budget.TeamID); err != nil {
			errs = append(errs, fmt.Errorf("team %q: %w", *budget.TeamID, err))
		}
	}
	if budget.CustomerID != nil {
		if _, err := reloader.ReloadCustomer(ctx, *budget.CustomerID); err != nil {
			errs = append(errs, fmt.Errorf("customer %q: %w", *budget.CustomerID, err))
		}
	}
	if budget.VirtualKeyID != nil {
		if _, err := reloader.ReloadVirtualKey(ctx, *budget.VirtualKeyID); err != nil {
			errs = append(errs, fmt.Errorf("virtual key %q: %w", *budget.VirtualKeyID, err))
		}
	}
	return errors.Join(errs...)
}

// warnBudgetOwnerReload reports a failed owner reload as a warning, not an
// error: the budget is already stored, and an error would read as nothing
// having been written.
func warnBudgetOwnerReload(out map[string]any, err error) {
	if err != nil {
		out["warning"] = fmt.Sprintf("stored, but its owner was not reloaded, so the previous cap is enforced until the next restart: %v", err)
	}
}

func describeBudget(budget *tables.TableBudget) map[string]any {
	out := budgetSummary(*budget)
	if budget.TeamID != nil {
		out["team_id"] = *budget.TeamID
	}
	if budget.CustomerID != nil {
		out["customer_id"] = *budget.CustomerID
	}
	if budget.VirtualKeyID != nil {
		out["virtual_key_id"] = *budget.VirtualKeyID
	}
	return out
}
