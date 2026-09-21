package mcptools

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/tidwall/gjson"
	"gorm.io/gorm"
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
// overlays the dashboard's key read paths apply, handed to this package by
// the HTTP layer that owns them; the package itself never imports them.
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
// implements it over access profiles and hands it over on Deps, so this
// package never imports enterprise code.
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

// GovernanceReader is the slice of the config store these tools may reach,
// and it is reads only: a write is a request to an admin route, made through
// AdminDispatcher. configstore.ConfigStore satisfies it structurally. Only some
// methods apply the request's row filter (virtual keys, teams, customers read
// through ScopedDB; budgets, rate limits and model configs do not), so the
// tools enforce the caller's tenant themselves - see tenant.go.
type GovernanceReader interface {
	GetVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error)
	GetVirtualKeysPaginated(ctx context.Context, params configstore.VirtualKeyQueryParams) ([]tables.TableVirtualKey, int64, error)

	GetTeam(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableTeam, error)
	GetTeamsPaginated(ctx context.Context, params configstore.TeamsQueryParams) ([]tables.TableTeam, int64, error)

	GetCustomer(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableCustomer, error)
	GetCustomersPaginated(ctx context.Context, params configstore.CustomersQueryParams) ([]tables.TableCustomer, int64, error)

	GetBudget(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableBudget, error)
	GetBudgets(ctx context.Context) ([]tables.TableBudget, error)

	GetProviders(ctx context.Context) ([]tables.TableProvider, error)
	GetProviderKeys(ctx context.Context, provider schemas.ModelProvider) ([]schemas.Key, error)
	GetProviderConfig(ctx context.Context, provider schemas.ModelProvider) (*configstore.ProviderConfig, error)

	GetMCPClientByID(ctx context.Context, id string) (*tables.TableMCPClient, error)
	GetMCPClientsPaginated(ctx context.Context, params configstore.MCPClientsQueryParams) ([]tables.TableMCPClient, int64, error)

	GetVirtualMCPByID(ctx context.Context, id uint) (*tables.TableVirtualMCP, error)
	GetVirtualMCPsPaginated(ctx context.Context, params configstore.VirtualMCPsQueryParams) ([]tables.TableVirtualMCP, int64, error)
	GetVirtualKeyIDsForVirtualMCP(ctx context.Context, vmcpID uint) ([]string, error)

	GetRoutingRule(ctx context.Context, id string) (*tables.TableRoutingRule, error)
	GetRoutingRulesPaginated(ctx context.Context, params configstore.RoutingRulesQueryParams) ([]tables.TableRoutingRule, int64, error)

	GetRateLimit(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableRateLimit, error)
	GetRateLimits(ctx context.Context) ([]tables.TableRateLimit, error)

	GetModelConfigByID(ctx context.Context, id string) (*tables.TableModelConfig, error)
	GetModelConfig(ctx context.Context, scope string, scopeID *string, modelName string, provider *string) (*tables.TableModelConfig, error)
	GetModelConfigsPaginated(ctx context.Context, params configstore.ModelConfigsQueryParams) ([]tables.TableModelConfig, int64, error)

	GetPlugin(ctx context.Context, name string) (*tables.TablePlugin, error)
	GetPlugins(ctx context.Context) ([]*tables.TablePlugin, error)

	GetPricingOverrideByID(ctx context.Context, id string) (*tables.TablePricingOverride, error)
	GetPricingOverridesPaginated(ctx context.Context, params configstore.PricingOverridesQueryParams) ([]tables.TablePricingOverride, int64, error)

	GetWebhookEndpointByID(ctx context.Context, id string) (*tables.TableWebhookEndpoint, error)
	GetWebhookEndpointsPaginated(ctx context.Context, params configstore.WebhookEndpointsQueryParams) ([]tables.TableWebhookEndpoint, int64, error)

	ListFeatureFlags(ctx context.Context) ([]tables.TableFeatureFlag, error)

	ListOauthUserTokens(ctx context.Context, params configstore.MCPSessionsFilterParams) ([]tables.TableMCPOauthToken, error)
	ListMCPPerUserHeaderCredentials(ctx context.Context, params configstore.MCPSessionsFilterParams) ([]tables.TableMCPPerUserHeaderCredential, error)
}

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
			"Takes the key's id (from describe_filter_space's virtual_keys list, or a ranking row) or its exact name. " +
			"This reads configuration, not traffic: a key that exists but has never been used is not in describe_filter_space and is still found here by name, so a name missing from that list is not proof the key does not exist. " +
			"A key managed by an access profile reports the profile's budgets and rate limit, and names its owner; the person's full limits are then a describe_user_limits question. " +
			"Budget amounts are US dollars: write them with a $ sign.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "virtual_key_id": {"type": "string", "description": "The virtual key's id, from describe_filter_space or a ranking row. Preferred when known."},
    "name": {"type": "string", "description": "The key's exact name, when the id is not known. Case-insensitive; must match the whole name."}
  }
}`,
		noLogs:       true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			if deps.Governance == nil {
				return nil, fmt.Errorf("virtual key detail is not available on this deployment")
			}
			id, _ := args["virtual_key_id"].(string)
			id = strings.TrimSpace(id)
			name, _ := args["name"].(string)
			name = strings.TrimSpace(name)
			if id == "" && name == "" {
				return nil, fmt.Errorf("virtual_key_id or name is required")
			}
			if id == "" {
				resolved, err := resolveVirtualKeyByName(ctx, deps.Governance, name)
				if err != nil {
					return nil, err
				}
				id = resolved
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
			var governedBy []string
			if deps.VKDecorator != nil {
				// Failing loudly rather than projecting the bare row: the bare row
				// of a managed key reads as "no budget", which is the wrong answer
				// with a straight face.
				if governedBy, err = deps.VKDecorator(ctx, vk); err != nil {
					return nil, fmt.Errorf("could not resolve what governs virtual key %q: %w", id, err)
				}
			}
			out := describeVirtualKey(vk)
			annotateVirtualKeyGovernance(out, vk, governedBy, deps.UserGovernance != nil)
			return out, nil
		},
	}
}

// virtualKeyNameSearchLimit bounds the name search. The store's search also
// matches keys by their team's or customer's name, so a page can hold keys
// that are not candidates at all; the exact-name pass below sorts that out.
const virtualKeyNameSearchLimit = 50

// resolveVirtualKeyByName turns an exact, case-insensitive key name into an
// id. Anything short of exactly one whole-name match is an error that says
// what was found, so the model can ask for the id rather than guess between
// keys.
//
// It reads configuration rather than traffic: describe_filter_space lists keys
// from logged requests, and a key that was just created or has not been used
// is not there, so "the key X" has to be answerable from the store. The store's
// search applies the request's row filter, which for a known tenant has been
// stripped (see tenantCallContext) - so a tenant's candidates come from its own
// key index instead, and every error text names only keys it owns.
func resolveVirtualKeyByName(ctx context.Context, reader GovernanceReader, name string) (string, error) {
	var keys []tables.TableVirtualKey
	var total int64
	if t, known, err := readTenant(ctx, true); err != nil {
		return "", err
	} else if known {
		index, err := t.index(ctx, reader)
		if err != nil {
			return "", err
		}
		for _, vk := range index.keys {
			if matchesSearch(vk.Name, name) {
				keys = append(keys, vk)
			}
		}
		total = int64(len(keys))
	} else {
		keys, total, err = reader.GetVirtualKeysPaginated(ctx, configstore.VirtualKeyQueryParams{Search: name, Limit: virtualKeyNameSearchLimit})
		if err != nil {
			return "", fmt.Errorf("virtual key search failed: %w", err)
		}
	}
	var exact []tables.TableVirtualKey
	var near []string
	for _, key := range keys {
		switch {
		case strings.EqualFold(strings.TrimSpace(key.Name), name):
			exact = append(exact, key)
		case strings.Contains(strings.ToLower(key.Name), strings.ToLower(name)):
			near = append(near, key.Name)
		}
	}
	switch len(exact) {
	case 1:
		return exact[0].ID, nil
	case 0:
		// A page that ran out is not evidence of absence. The search also
		// matches team and customer names, so a common word can fill the page
		// with keys that are not candidates while the exact key sits beyond it
		// - and "does not exist" here would be relayed as a fact the model
		// never checked.
		if total > int64(len(keys)) {
			return "", fmt.Errorf("no virtual key named exactly %q in the first %d of %d keys the search matched (it also matches team and customer names) - the key may exist beyond that page; pass virtual_key_id from describe_filter_space", name, len(keys), total)
		}
		if len(near) > 0 {
			return "", fmt.Errorf("no virtual key named exactly %q that you can see; names containing it: %s. Pass the exact name, or the id", name, strings.Join(near, ", "))
		}
		return "", fmt.Errorf("no virtual key named %q that you can see - the name must match the whole name, and a key you cannot see reads the same as one that does not exist", name)
	default:
		ids := make([]string, len(exact))
		for i, key := range exact {
			ids[i] = key.ID
		}
		return "", fmt.Errorf("%d virtual keys are named %q (%s) - pass virtual_key_id to pick one", len(exact), name, strings.Join(ids, ", "))
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
	if len(vk.Metadata) > 0 {
		out["metadata"] = maps.Clone(vk.Metadata)
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
//
// The amounts carry their unit in the key. A budget is stored in US dollars,
// but a bare "max_limit": 5000 says so nowhere, and a model that is not told
// reports "5,000 units". The key is the one place the unit cannot be read
// apart from the number it belongs to.
func budgetSummary(budget tables.TableBudget) map[string]any {
	limit := budget.EffectiveMaxLimit()
	out := map[string]any{
		"id":                budget.ID,
		"max_limit_usd":     limit,
		"current_usage_usd": budget.CurrentUsage,
		"remaining_usd":     max(limit-budget.CurrentUsage, 0),
		"reset_duration":    budget.ResetDuration,
		"last_reset":        budget.LastReset,
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
// always declared, and reports itself unavailable when Deps carries no
// UserGovernanceReader; a consumer that knows the deployment has none (Warp)
// leaves it out of what it offers the model.
func describeUserLimitsTool() Tool {
	return Tool{
		name: UserLimitsToolName,
		description: "Look up what governs one person's spend: the budgets, per-provider budgets and rate limits their access profile puts on them, with live usage - their configured room, not their traffic. " +
			"Use it for \"how much budget do I have left\", a person's limit or allowance, and any budget question about a key that describe_virtual_key reports as managed by an access profile. " +
			"Needs the user's id: caller_user_id from describe_filter_space for the person asking, or the id on a user ranking row (query_usage_by with dimension user) for someone else - a name is not enough. " +
			"A virtual key that belongs to a team or customer can only look up the person it is calling for. " +
			"Budget amounts are US dollars: write them with a $ sign.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "user_id": {"type": "string", "description": "The user's id - caller_user_id for the person asking, or a user ranking row's id."}
  },
  "required": ["user_id"]
}`,
		noLogs:       true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			if deps.UserGovernance == nil {
				return nil, fmt.Errorf("per-user limits are not available on this deployment")
			}
			id, _ := args["user_id"].(string)
			id = strings.TrimSpace(id)
			if id == "" {
				return nil, fmt.Errorf("user_id is required")
			}
			notFound := fmt.Errorf("no user with id %q that you can see - describe_filter_space gives the caller's id, and a user ranking gives everyone else's", id)
			// A known tenant reads governance with the row filter stripped
			// (tenantCallContext), and the user store has no team or customer
			// column to check ownership against afterwards. The one person a
			// team or customer key can be shown to own is its own caller, so
			// anyone else - or anyone at all when the caller is unidentified -
			// reads as a user it cannot see. A scope that is not one team or
			// customer is left to the reader, which applies the caller's own.
			if _, known, err := readTenant(ctx, true); err != nil {
				return nil, err
			} else if known {
				if callerID, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string); callerID == "" || callerID != id {
					return nil, notFound
				}
			}
			gov, err := deps.UserGovernance.DescribeUserGovernance(ctx, id)
			if err != nil {
				if errors.Is(err, ErrUserNotFound) {
					return nil, notFound
				}
				return nil, fmt.Errorf("user limits lookup failed: %w", err)
			}
			return describeUserGovernance(gov), nil
		},
	}
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
		out["guidance"] = "remaining_usd is max_limit_usd minus current_usage_usd, per budget, in US dollars, and resets reset_duration after last_reset. An inactive or expired profile does not cap anything. Report each profile by name."
	}
	return out
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
    "allow_all_providers": {"type": "boolean", "description": "Defaults to true so the key can reach every configured provider."},
    "token_max_limit": {"type": "integer", "minimum": 1},
    "token_reset_duration": {"type": "string"},
    "request_max_limit": {"type": "integer", "minimum": 1},
    "request_reset_duration": {"type": "string"}
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
			if _, err := stringArg(args, "name"); err != nil {
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
			// The route does not know the caller's tenant. A scoped caller
			// creates keys inside its own tenant: in it by default, and never
			// in a team or customer outside it. The owner checked here is the
			// owner sent.
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
			body := adminBody{"allow_all_providers": true}.copySet(args, "name", "description", "allow_all_providers")
			if hasTeam {
				body["team_id"] = teamID
			}
			if hasCustomer {
				body["customer_id"] = customerID
			}
			if rateLimit := nestedRateLimitBody(args, nil); rateLimit != nil {
				body["rate_limit"] = rateLimit
			}
			response, err := callAdmin(ctx, deps, http.MethodPost, "/api/governance/virtual-keys", body)
			if err != nil {
				return nil, err
			}
			return describeVirtualKeyResponse(response, "this is the only time the secret is returned; store it now")
		},
	}
}

func updateVirtualKeyTool() Tool {
	return Tool{
		name: "update_virtual_key",
		description: "Update a virtual key's name, description, active flag, team/customer, or rate limit. Does not rotate the secret. " +
			"Rate-limit fields you do not name keep their stored values. A key managed by an access profile takes its limits from the profile, so rate-limit edits on it are refused.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "virtual_key_id": {"type": "string"},
    "name": {"type": "string"},
    "description": {"type": "string"},
    "is_active": {"type": "boolean"},
    "team_id": {"type": "string"},
    "customer_id": {"type": "string"},
    "allow_all_providers": {"type": "boolean"},
    "token_max_limit": {"type": "integer", "minimum": 1},
    "token_reset_duration": {"type": "string"},
    "request_max_limit": {"type": "integer", "minimum": 1},
    "request_reset_duration": {"type": "string"}
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
			path, err := adminPath("/api/governance/virtual-keys/{vk_id}", id)
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
			// The route does not know the caller's tenant. Moving a key is
			// checked on both ends: it must be the caller's to move, and it
			// may only land inside the caller's tenant. The owner checked
			// here is the owner sent, and no other field below names one.
			if err := checkTenant(ctx, func(t tenant) error {
				owned, err := t.ownsVirtualKey(ctx, gov, vk)
				if err := requireOwned(owned, err, t, fmt.Sprintf("virtual key %q", id)); err != nil {
					return err
				}
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
			body := adminBody{}.copySet(args, "name", "description", "is_active", "allow_all_providers")
			// A key has one owner, so naming a new one clears the other: the
			// route refuses a key left with both.
			if hasTeam {
				body["team_id"], body["customer_id"] = teamID, nil
			}
			if hasCustomer {
				body["customer_id"], body["team_id"] = customerID, nil
			}
			if namesRateLimit(args) {
				stored, err := virtualKeyRateLimit(ctx, deps, vk)
				if err != nil {
					return nil, err
				}
				body["rate_limit"] = nestedRateLimitBody(args, stored)
			}
			response, err := callAdmin(ctx, deps, http.MethodPut, path, body)
			if err != nil {
				return nil, err
			}
			return describeVirtualKeyResponse(response, "")
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
		name: "rotate_virtual_key",
		description: "Issue a new sk-bf- secret for a virtual key. Returns the new value once. The previous value stays valid for vk_rotation_cooldown (or stops immediately when that is 0). " +
			"Rotating also revokes the MCP OAuth grants obtained with the key (its refresh tokens and any consented but unexchanged authorization codes), so MCP clients signed in through it must sign in again with the new value.",
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
			path, err := adminPath("/api/governance/virtual-keys/{vk_id}/rotate", id)
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
			// The route does not know the caller's tenant.
			if err := checkTenant(ctx, func(t tenant) error {
				owned, err := t.ownsVirtualKey(ctx, gov, vk)
				return requireOwned(owned, err, t, fmt.Sprintf("virtual key %q", id))
			}); err != nil {
				return nil, err
			}
			response, err := callAdmin(ctx, deps, http.MethodPost, path, nil)
			if err != nil {
				return nil, err
			}
			out, err := describeVirtualKeyResponse(response, "this is the only time the new secret is returned; store it now")
			if err != nil {
				return nil, err
			}
			// The grace window is not in the route's answer; the stored key has it.
			if rotated, err := gov.GetVirtualKey(ctx, id); err == nil && rotated != nil && rotated.PreviousValueExpiresAt != nil {
				out["previous_value_expires_at"] = *rotated.PreviousValueExpiresAt
			}
			return out, nil
		},
	}
}

// describeVirtualKeyResponse projects the key a virtual key route answers with.
// note is set on the two answers that carry the secret, create and rotate, and
// is what puts the secret in the result; every other answer leaves it out.
func describeVirtualKeyResponse(response []byte, note string) (map[string]any, error) {
	var vk tables.TableVirtualKey
	if err := decodeAdminField(response, "virtual_key", &vk); err != nil {
		return nil, err
	}
	out := describeVirtualKey(&vk)
	if note != "" {
		out["value"] = gjson.GetBytes(response, "virtual_key.value").String()
		out["note"] = note
	}
	return out, nil
}

// namesRateLimit reports whether the call set any rate-limit field.
func namesRateLimit(args map[string]any) bool {
	for _, field := range rateLimitFields {
		if value, present := args[field]; present && value != nil {
			return true
		}
	}
	return false
}

// virtualKeyRateLimit is the rate limit an update_virtual_key edit merges over.
//
// The route keeps a key's own rate limit on its key-scoped, provider-less "*"
// model config and replaces that tier's limit with exactly the fields it is
// sent, so a partial edit has to carry the tier's other fields. A key with no
// tier yet still has its legacy rate-limit row, which the route adopts.
//
// A key an access profile manages is refused: its limits are the profile's,
// and a write here would land on the key's untracked mirror rows - accepted,
// and never enforced.
func virtualKeyRateLimit(ctx context.Context, deps *Deps, vk *tables.TableVirtualKey) (*tables.TableRateLimit, error) {
	if deps.VKDecorator != nil {
		decorated := *vk
		if _, err := deps.VKDecorator(ctx, &decorated); err != nil {
			return nil, fmt.Errorf("could not resolve what governs virtual key %q: %w", vk.ID, err)
		}
		if decorated.IsAccessProfileManaged {
			return nil, fmt.Errorf("virtual key %q is managed by an access profile, so its rate limit and budgets are the profile's and cannot be changed on the key; change the profile instead (describe_user_limits shows it)", vk.ID)
		}
	}
	tier, err := deps.Governance.GetModelConfig(ctx, tables.ModelConfigScopeVirtualKey, &vk.ID, tables.ModelConfigAllModels, nil)
	switch {
	case err == nil && tier != nil:
		if limit, err := storedRateLimit(ctx, deps.Governance, tier.RateLimit, tier.RateLimitID); err != nil || limit != nil {
			return limit, err
		}
	case err != nil && !errors.Is(err, configstore.ErrNotFound):
		return nil, fmt.Errorf("model config lookup failed: %w", err)
	}
	return storedRateLimit(ctx, deps.Governance, vk.RateLimit, vk.RateLimitID)
}

// storedRateLimit returns an owner's rate limit, loading it when the owner was
// read without it.
func storedRateLimit(ctx context.Context, gov GovernanceReader, loaded *tables.TableRateLimit, id *string) (*tables.TableRateLimit, error) {
	if loaded != nil || id == nil || *id == "" {
		return loaded, nil
	}
	stored, err := gov.GetRateLimit(ctx, *id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("rate limit lookup failed: %w", err)
	}
	return stored, nil
}

// ownerLimitsBody adds the budgets and the rate limit a team or customer tool
// was given to body. The routes replace the rate limit with what they are
// sent, so the limits the caller did not name are sent as stored.
func ownerLimitsBody(body adminBody, args map[string]any, stored *tables.TableRateLimit) {
	body.copySet(args, "budgets")
	if rateLimit := nestedRateLimitBody(args, stored); rateLimit != nil {
		body["rate_limit"] = rateLimit
	}
}

func createTeamTool() Tool {
	return Tool{
		name:        "create_team",
		description: "Create a team, optionally under a customer, with budgets and a rate limit of its own.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 1},
    "customer_id": {"type": "string"},
    "budgets": {"type": "array", "description": "The full set of budgets: it replaces every budget already there, and [] removes them all. Each needs its own reset_duration.", "items": {
      "type": "object",
      "properties": {
        "max_limit": {"type": "number", "minimum": 0},
        "reset_duration": {"type": "string", "description": "Budget window, e.g. 1d, 1w, 1M."}
      },
      "required": ["max_limit", "reset_duration"]
    }},
    "token_max_limit": {"type": "integer", "minimum": 1},
    "token_reset_duration": {"type": "string"},
    "request_max_limit": {"type": "integer", "minimum": 1},
    "request_reset_duration": {"type": "string"}
  },
  "required": ["name"]
}`,
		noLogs:       true,
		mutating:     true,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			if _, err := stringArg(args, "name"); err != nil {
				return nil, err
			}
			customerID, hasCustomer, err := optionalStringArg(args, "customer_id")
			if err != nil {
				return nil, err
			}
			// The route does not know the caller's tenant. A team key cannot
			// create sibling teams; a customer key creates teams under its
			// own customer, by default and only there.
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
			body := adminBody{}.copySet(args, "name")
			if hasCustomer {
				body["customer_id"] = customerID
			}
			ownerLimitsBody(body, args, nil)
			response, err := callAdmin(ctx, deps, http.MethodPost, "/api/governance/teams", body)
			if err != nil {
				return nil, err
			}
			return describeTeamResponse(response)
		},
	}
}

func updateTeamTool() Tool {
	return Tool{
		name:        "update_team",
		description: "Rename a team, move it to another customer, or set its budgets and rate limit. Does not reset current usage.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "team_id": {"type": "string", "minLength": 1},
    "name": {"type": "string"},
    "customer_id": {"type": "string"},
    "budgets": {"type": "array", "description": "The full set of budgets: it replaces every budget already there, and [] removes them all. Each needs its own reset_duration.", "items": {
      "type": "object",
      "properties": {
        "max_limit": {"type": "number", "minimum": 0},
        "reset_duration": {"type": "string", "description": "Budget window, e.g. 1d, 1w, 1M."}
      },
      "required": ["max_limit", "reset_duration"]
    }},
    "token_max_limit": {"type": "integer", "minimum": 1},
    "token_reset_duration": {"type": "string"},
    "request_max_limit": {"type": "integer", "minimum": 1},
    "request_reset_duration": {"type": "string"}
  },
  "required": ["team_id"]
}`,
		noLogs:       true,
		mutating:     true,
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
			path, err := adminPath("/api/governance/teams/{team_id}", id)
			if err != nil {
				return nil, err
			}
			team, err := gov.GetTeam(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no team with id %q", id)
				}
				return nil, fmt.Errorf("team lookup failed: %w", err)
			}
			customerID, hasCustomer, err := optionalStringArg(args, "customer_id")
			if err != nil {
				return nil, err
			}
			// The route does not know the caller's tenant. The team must be
			// the caller's, and it may only be moved inside the caller's
			// tenant - which for a team key is nowhere, since a team key owns
			// no customer.
			if err := checkTenant(ctx, func(t tenant) error {
				owned, err := t.ownsTeam(ctx, gov, id)
				if err := requireOwned(owned, err, t, fmt.Sprintf("team %q", id)); err != nil {
					return err
				}
				if hasCustomer {
					return requireOwned(t.ownsCustomer(customerID), nil, t, fmt.Sprintf("customer %q", customerID))
				}
				return nil
			}); err != nil {
				return nil, err
			}
			body := adminBody{}.copySet(args, "name")
			if hasCustomer {
				body["customer_id"] = customerID
			}
			stored, err := storedRateLimit(ctx, gov, team.RateLimit, team.RateLimitID)
			if err != nil {
				return nil, err
			}
			ownerLimitsBody(body, args, stored)
			response, err := callAdmin(ctx, deps, http.MethodPut, path, body)
			if err != nil {
				return nil, err
			}
			return describeTeamResponse(response)
		},
	}
}

func createCustomerTool() Tool {
	return Tool{
		name:        "create_customer",
		description: "Create a customer, with budgets and a rate limit of its own.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 1},
    "budgets": {"type": "array", "description": "The full set of budgets: it replaces every budget already there, and [] removes them all. Each needs its own reset_duration.", "items": {
      "type": "object",
      "properties": {
        "max_limit": {"type": "number", "minimum": 0},
        "reset_duration": {"type": "string", "description": "Budget window, e.g. 1d, 1w, 1M."}
      },
      "required": ["max_limit", "reset_duration"]
    }},
    "token_max_limit": {"type": "integer", "minimum": 1},
    "token_reset_duration": {"type": "string"},
    "request_max_limit": {"type": "integer", "minimum": 1},
    "request_reset_duration": {"type": "string"}
  },
  "required": ["name"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			if _, err := stringArg(args, "name"); err != nil {
				return nil, err
			}
			body := adminBody{}.copySet(args, "name")
			ownerLimitsBody(body, args, nil)
			response, err := callAdmin(ctx, deps, http.MethodPost, "/api/governance/customers", body)
			if err != nil {
				return nil, err
			}
			return describeCustomerResponse(response)
		},
	}
}

func updateCustomerTool() Tool {
	return Tool{
		name:        "update_customer",
		description: "Rename a customer, or set its budgets and rate limit. Does not reset current usage.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "customer_id": {"type": "string", "minLength": 1},
    "name": {"type": "string"},
    "budgets": {"type": "array", "description": "The full set of budgets: it replaces every budget already there, and [] removes them all. Each needs its own reset_duration.", "items": {
      "type": "object",
      "properties": {
        "max_limit": {"type": "number", "minimum": 0},
        "reset_duration": {"type": "string", "description": "Budget window, e.g. 1d, 1w, 1M."}
      },
      "required": ["max_limit", "reset_duration"]
    }},
    "token_max_limit": {"type": "integer", "minimum": 1},
    "token_reset_duration": {"type": "string"},
    "request_max_limit": {"type": "integer", "minimum": 1},
    "request_reset_duration": {"type": "string"}
  },
  "required": ["customer_id"]
}`,
		noLogs:       true,
		mutating:     true,
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
			path, err := adminPath("/api/governance/customers/{customer_id}", id)
			if err != nil {
				return nil, err
			}
			// The route does not know the caller's tenant. Checked before the
			// lookup, so another tenant's customer reads like a missing one.
			if err := checkTenant(ctx, func(t tenant) error {
				return requireOwned(t.ownsCustomer(id), nil, t, fmt.Sprintf("customer %q", id))
			}); err != nil {
				return nil, err
			}
			customer, err := gov.GetCustomer(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no customer with id %q", id)
				}
				return nil, fmt.Errorf("customer lookup failed: %w", err)
			}
			body := adminBody{}.copySet(args, "name")
			stored, err := storedRateLimit(ctx, gov, customer.RateLimit, customer.RateLimitID)
			if err != nil {
				return nil, err
			}
			ownerLimitsBody(body, args, stored)
			response, err := callAdmin(ctx, deps, http.MethodPut, path, body)
			if err != nil {
				return nil, err
			}
			return describeCustomerResponse(response)
		},
	}
}

func describeTeamResponse(response []byte) (any, error) {
	var team tables.TableTeam
	if err := decodeAdminField(response, "team", &team); err != nil {
		return nil, err
	}
	return describeTeam(&team), nil
}

func describeCustomerResponse(response []byte) (any, error) {
	var customer tables.TableCustomer
	if err := decodeAdminField(response, "customer", &customer); err != nil {
		return nil, err
	}
	return describeCustomer(&customer), nil
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
