package mcptools

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			if deps.Governance == nil {
				return nil, fmt.Errorf("virtual key detail is not available on this deployment")
			}
			id, _ := args["virtual_key_id"].(string)
			id = strings.TrimSpace(id)
			if id == "" {
				return nil, fmt.Errorf("virtual_key_id is required")
			}
			vk, err := deps.Governance.GetVirtualKey(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no virtual key with id %q - describe_filter_space lists the real ones", id)
				}
				return nil, fmt.Errorf("virtual key lookup failed: %w", err)
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
