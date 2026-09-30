package warp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// VirtualKeyDecorator fills in what a key row alone cannot say about a key's
// governance, before describe_virtual_key projects it.
//
// A key row is not the whole story of what caps a key. A standalone key keeps
// its budgets and rate limit in key-scoped model configs the dashboard
// rehydrates before serialising, and on an enterprise deployment a key that an
// access profile hands a user is minted bare on purpose - its budgets, rate
// limit and providers live on the owner's profile, and the key's own rows are
// stripped when it joins one. Read raw, such a key reports "no budget" while
// the dashboard shows $450 with 85% used. The decorator is the same set of
// overlays the dashboard's key read paths apply, handed to Warp by the HTTP
// layer that owns them; the package itself never imports them.
//
// It mutates vk in place (Budgets, RateLimit, IsAccessProfileManaged,
// AssignedUser) and returns the names of whatever governs the key from outside
// - "access profile admin" - or nothing when the key governs itself. An error
// means the governance could not be resolved, and the tool reports that
// rather than a confidently empty key.
type VirtualKeyDecorator func(ctx context.Context, vk *tables.TableVirtualKey) (governedBy []string, err error)

// ErrUserNotFound is what a UserGovernanceReader returns for a user id the
// caller cannot see, whether it does not exist or is outside their scope. The
// two are deliberately one error: telling them apart would let a caller probe
// for ids.
var ErrUserNotFound = errors.New("user not found")

// UserGovernanceReader answers what governs one person's spend. It is optional:
// an OSS deployment has no per-user governance and leaves it nil, and
// describe_user_limits is then not offered at all. An enterprise deployment
// implements it over access profiles and hands it to the service, so the
// Warp package never imports enterprise code.
//
// Implementations apply the caller's own scope: a user the caller may not see
// is ErrUserNotFound, never a profile. Usage counters should be the live ones
// where the deployment has them, since the persisted copy lags.
type UserGovernanceReader interface {
	DescribeUserGovernance(ctx context.Context, userID string) (*UserGovernance, error)
}

// UserGovernance is everything that caps one person's spend, by profile.
type UserGovernance struct {
	UserID string
	Name   string
	// Profiles is every profile attached to the user, active or not. Empty
	// means nothing governs them at the user level.
	Profiles []UserGovernanceProfile
}

// UserGovernanceProfile is one access profile's hold on a user: the budgets
// and rate limit that apply to everything they do, and the per-provider ones
// under it. The budget and rate-limit rows are the store's own types so the
// same summaries describe them as a key's.
type UserGovernanceProfile struct {
	Name              string
	Active            bool
	ExpiresAt         *time.Time
	AllowAllProviders bool
	Budgets           []tables.TableBudget
	RateLimit         *tables.TableRateLimit
	Providers         []UserGovernanceProvider
}

// UserGovernanceProvider is a profile's rules for one provider.
type UserGovernanceProvider struct {
	Provider          string
	AllowedModels     []string
	BlacklistedModels []string
	Budgets           []tables.TableBudget
	RateLimit         *tables.TableRateLimit
	ModelBudgets      []UserGovernanceModelBudget
}

// UserGovernanceModelBudget is a per-model cap under a provider.
type UserGovernanceModelBudget struct {
	Model     string
	Budgets   []tables.TableBudget
	RateLimit *tables.TableRateLimit
}

// GovernanceReader is the slice of the config store describe_virtual_key is
// allowed to read.
//
// Like LogReader, this is deliberately one method: reviewing what Warp can see
// about how the deployment is governed means reading this interface, not
// auditing configstore.ConfigStore's whole surface. GetVirtualKey in
// particular is scope-aware - a caller's ctx narrows which rows it can return,
// the same row-level enforcement every logstore query gets - so this tool
// inherits that for free rather than needing its own access check.
type GovernanceReader interface {
	GetVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error)
}

// describeVirtualKeyTool looks up one virtual key's budget, rate limit and
// allowed providers - the natural follow-up to "how much has this key spent"
// (query_usage_by) that Warp had no way to answer before: whether there is
// room left, not just how much has gone by.
//
// It never returns tables.TableVirtualKey (or any of its relations) directly.
// That struct carries the key's own secret value and its rotation history -
// exactly the kind of key material LogReader's own doc comment says a Warp
// tool must never surface - so describeVirtualKey below hand-picks only the
// budget/limit/provider fields onto a fresh map instead of ever serializing
// the row itself.
func describeVirtualKeyTool() Tool {
	return Tool{
		name: "describe_virtual_key",
		description: "Look up one virtual key's budget, rate limit and allowed providers/models - its configured room, not its traffic. " +
			"Use query_usage_by with dimension virtual_key for what it has actually spent; use this for what it is allowed to spend or call before it is throttled. " +
			"Needs the key's id, as returned by describe_filter_space's virtual_keys list - a name is not enough. " +
			"A key managed by an access profile reports the profile's budgets and rate limit, and names its owner; the person's full limits are then a describe_user_limits question.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "virtual_key_id": {"type": "string", "description": "The virtual key's id, from describe_filter_space."}
  },
  "required": ["virtual_key_id"]
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			if deps.governance == nil {
				return nil, fmt.Errorf("virtual key detail is not available on this deployment")
			}
			id, _ := args["virtual_key_id"].(string)
			id = strings.TrimSpace(id)
			if id == "" {
				return nil, fmt.Errorf("virtual_key_id is required")
			}
			vk, err := deps.governance.GetVirtualKey(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no virtual key with id %q - describe_filter_space lists the real ones", id)
				}
				return nil, fmt.Errorf("virtual key lookup failed: %w", err)
			}
			var governedBy []string
			if deps.vkDecorator != nil {
				// Failing loudly rather than projecting the bare row: the bare row
				// of a managed key reads as "no budget", which is the wrong answer
				// with a straight face.
				if governedBy, err = deps.vkDecorator(ctx, vk); err != nil {
					return nil, fmt.Errorf("could not resolve what governs virtual key %q: %w", id, err)
				}
			}
			out := describeVirtualKey(vk)
			annotateVirtualKeyGovernance(out, vk, governedBy, deps.userGovernance != nil)
			return out, nil
		},
	}
}

// annotateVirtualKeyGovernance says in words what the projected fields cannot:
// an absent budget used to be an absent key, and the model read "no budgets
// field" as "couldn't retrieve the budget". A managed key gets its provenance
// and where to look for the rest; a key that genuinely has no cap of its own
// says so, and says what may still apply.
func annotateVirtualKeyGovernance(out map[string]any, vk *tables.TableVirtualKey, governedBy []string, userLimits bool) {
	if vk.IsAccessProfileManaged {
		note := "This key is managed by an access profile"
		if len(governedBy) > 0 {
			note += " (" + strings.Join(governedBy, ", ") + ")"
		}
		note += ": the budgets and rate limit here are the profile's, with live usage, and the key has none of its own."
		if vk.AssignedUser != nil && vk.AssignedUser.ID != "" {
			note += fmt.Sprintf(" Its owner is user %s.", vk.AssignedUser.ID)
			if userLimits {
				note += " For everything that person may spend, including per-provider budgets, call describe_user_limits with that user id."
			}
		}
		out["guidance"] = note
		return
	}
	var missing []string
	if len(vk.Budgets) == 0 {
		missing = append(missing, "budget")
	}
	if vk.RateLimit == nil {
		missing = append(missing, "rate limit")
	}
	if len(missing) == 0 {
		return
	}
	out["guidance"] = fmt.Sprintf("No %s is configured on this key itself, so nothing caps it at the key level. A budget on its team, customer or business unit, if any, still applies and is not read here.", strings.Join(missing, " or "))
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
	if vk.BusinessUnitID != nil {
		out["business_unit_id"] = *vk.BusinessUnitID
	}
	if vk.DisableContentLogging != nil {
		out["disable_content_logging"] = *vk.DisableContentLogging
	}
	if vk.IsAccessProfileManaged {
		out["access_profile_managed"] = true
	}
	if vk.AssignedUser != nil && vk.AssignedUser.ID != "" {
		out["assigned_user"] = map[string]any{"id": vk.AssignedUser.ID, "name": vk.AssignedUser.Name}
	}
	// Always present, empty or null when there is none. Left out, the model
	// could not tell "no budget" from "the lookup returned nothing", and
	// reported the latter.
	out["budgets"] = budgetSummaries(vk.Budgets)
	if vk.RateLimit != nil {
		out["rate_limit"] = rateLimitSummary(*vk.RateLimit)
	} else {
		out["rate_limit"] = nil
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
	limit := budget.EffectiveMaxLimit()
	out := map[string]any{
		"id":             budget.ID,
		"max_limit":      limit,
		"current_usage":  budget.CurrentUsage,
		"remaining":      max(limit-budget.CurrentUsage, 0),
		"reset_duration": budget.ResetDuration,
		"last_reset":     budget.LastReset,
	}
	if budget.HasActiveOverride() {
		out["override_active"] = true
	}
	return out
}

// budgetSummaries is budgetSummary over a slice, never nil: an absent list is
// what the model read as a failed lookup.
func budgetSummaries(budgets []tables.TableBudget) []map[string]any {
	out := make([]map[string]any, len(budgets))
	for i, budget := range budgets {
		out[i] = budgetSummary(budget)
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

// describeUserLimitsTool answers "how much budget do I have left" for a
// person rather than a key. On a deployment with access profiles the cap sits
// on the user - $450 a month, 1K requests an hour - and every key the profile
// hands them inherits it, so the key is the wrong thing to ask about. The tool
// is only offered when the service was given a UserGovernanceReader; without
// one the question has no answer here and the prompt does not mention it.
func describeUserLimitsTool() Tool {
	return Tool{
		name: UserLimitsToolName,
		description: "Look up what governs one person's spend: the budgets, per-provider budgets and rate limits their access profile puts on them, with live usage - their configured room, not their traffic. " +
			"Use it for \"how much budget do I have left\", a person's limit or allowance, and any budget question about a key that describe_virtual_key reports as managed by an access profile. " +
			"Needs the user's id: caller_user_id from describe_filter_space for the person asking, or the id on a user ranking row (query_usage_by with dimension user) for someone else - a name is not enough.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "user_id": {"type": "string", "description": "The user's id - caller_user_id for the person asking, or a user ranking row's id."}
  },
  "required": ["user_id"]
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			if deps.userGovernance == nil {
				return nil, fmt.Errorf("per-user limits are not available on this deployment")
			}
			id, _ := args["user_id"].(string)
			id = strings.TrimSpace(id)
			if id == "" {
				return nil, fmt.Errorf("user_id is required")
			}
			gov, err := deps.userGovernance.DescribeUserGovernance(ctx, id)
			if err != nil {
				if errors.Is(err, ErrUserNotFound) {
					return nil, fmt.Errorf("no user with id %q that you can see - describe_filter_space gives the caller's id, and a user ranking gives everyone else's", id)
				}
				return nil, fmt.Errorf("user limits lookup failed: %w", err)
			}
			return describeUserGovernance(gov), nil
		},
	}
}

// UserLimitsToolName is describe_user_limits, exported so the prompt and the
// declarations name the same tool.
const UserLimitsToolName = "describe_user_limits"

// describeUserGovernance projects a person's governance the way
// describeVirtualKey projects a key's, with the same budget and rate-limit
// summaries so a number means the same thing in both.
func describeUserGovernance(gov *UserGovernance) map[string]any {
	out := map[string]any{"user_id": gov.UserID}
	if gov.Name != "" {
		out["name"] = gov.Name
	}
	profiles := make([]map[string]any, len(gov.Profiles))
	for i, profile := range gov.Profiles {
		entry := map[string]any{
			"name":                profile.Name,
			"active":              profile.Active,
			"allow_all_providers": profile.AllowAllProviders,
			"budgets":             budgetSummaries(profile.Budgets),
		}
		if profile.ExpiresAt != nil {
			entry["expires_at"] = *profile.ExpiresAt
		}
		if profile.RateLimit != nil {
			entry["rate_limit"] = rateLimitSummary(*profile.RateLimit)
		} else {
			entry["rate_limit"] = nil
		}
		providers := make([]map[string]any, len(profile.Providers))
		for j, provider := range profile.Providers {
			p := map[string]any{"provider": provider.Provider, "budgets": budgetSummaries(provider.Budgets)}
			if len(provider.AllowedModels) > 0 {
				p["allowed_models"] = provider.AllowedModels
			}
			if len(provider.BlacklistedModels) > 0 {
				p["blacklisted_models"] = provider.BlacklistedModels
			}
			if provider.RateLimit != nil {
				p["rate_limit"] = rateLimitSummary(*provider.RateLimit)
			}
			if len(provider.ModelBudgets) > 0 {
				models := make([]map[string]any, len(provider.ModelBudgets))
				for k, mb := range provider.ModelBudgets {
					m := map[string]any{"model": mb.Model, "budgets": budgetSummaries(mb.Budgets)}
					if mb.RateLimit != nil {
						m["rate_limit"] = rateLimitSummary(*mb.RateLimit)
					}
					models[k] = m
				}
				p["model_budgets"] = models
			}
			providers[j] = p
		}
		entry["providers"] = providers
		profiles[i] = entry
	}
	out["profiles"] = profiles
	if len(profiles) == 0 {
		out["guidance"] = "No access profile is attached to this user, so nothing caps their spend at the user level. A budget on a key they use, or on their team, customer or business unit, may still apply."
	} else {
		out["guidance"] = "remaining is max_limit minus current_usage, per budget, and resets reset_duration after last_reset. An inactive or expired profile does not cap anything. Report each profile by name."
	}
	return out
}
