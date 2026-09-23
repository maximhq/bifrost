package mcptools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// Tenant scoping for write tools.
//
// A virtual key that belongs to a team or customer can do, through these tools,
// what it could do on Bifrost - and on Bifrost it acts inside its own tenant.
// The read side already holds to that through queryscope; the configstore write
// methods do not filter by it, so each write tool that touches tenant-owned
// rows checks ownership here, and every other write tool is refused outright
// for a scoped caller (see toolHandler and Tool.tenantScoped).
//
// The tenant is the narrowest one governance resolved for the key: its team
// when it has one (a team key does not reach sibling teams under the same
// customer), otherwise its customer. A key with neither is unscoped - the
// admin-style key queryscope also leaves unfiltered - as is a request with no
// key at all, which is governed by the admin-API rule instead (writeAllowed).

// tenant is the caller's write boundary. Exactly one field is set.
type tenant struct {
	teamID     string
	customerID string
}

func (t tenant) String() string {
	if t.teamID != "" {
		return fmt.Sprintf("team %q", t.teamID)
	}
	return fmt.Sprintf("customer %q", t.customerID)
}

// errUnknownTenant refuses a caller whose scope cannot be expressed as one team
// or customer - a scope some other layer set, or several teams at once - since
// there is nothing to check ownership against.
var errUnknownTenant = errors.New("this request is scoped in a way the write tools cannot check ownership against, so writes are refused; use a virtual key that belongs to one team or customer, or an admin credential")

// callerTenant resolves the write boundary for this call. scoped is false for
// an unscoped caller, who may write anywhere its grants allow.
func callerTenant(ctx context.Context) (t tenant, scoped bool, err error) {
	if teamID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceTeamID).(string); teamID != "" {
		return tenant{teamID: teamID}, true, nil
	}
	if customerID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceCustomerID).(string); customerID != "" {
		return tenant{customerID: customerID}, true, nil
	}
	// Scoped by something other than one team or customer: fail closed.
	if queryscope.FromContext(ctx) != nil {
		return tenant{}, true, errUnknownTenant
	}
	if ids, _ := ctx.Value(schemas.BifrostContextKeyGovernanceTeamIDs).([]string); len(ids) > 0 {
		return tenant{}, true, errUnknownTenant
	}
	if ids, _ := ctx.Value(schemas.BifrostContextKeyGovernanceCustomerIDs).([]string); len(ids) > 0 {
		return tenant{}, true, errUnknownTenant
	}
	return tenant{}, false, nil
}

// outsideTenant is the one refusal every ownership check returns, so a caller
// probing ids learns only that the row is not theirs to change.
func outsideTenant(t tenant, what string) error {
	return fmt.Errorf("%s is outside this virtual key's %s; a scoped key can only change rows its own tenant owns", what, t)
}

// ownsTeam reports whether teamID is inside the tenant: the tenant's own team,
// or any team under the tenant's customer.
func (t tenant) ownsTeam(ctx context.Context, gov GovernanceReader, teamID string) (bool, error) {
	if t.teamID != "" {
		return teamID == t.teamID, nil
	}
	team, err := gov.GetTeam(ctx, teamID)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("team lookup failed: %w", err)
	}
	return team.CustomerID != nil && *team.CustomerID == t.customerID, nil
}

// ownsCustomer reports whether customerID is the tenant's customer. A team
// tenant owns no customer, not even its own team's.
func (t tenant) ownsCustomer(customerID string) bool {
	return t.customerID != "" && customerID == t.customerID
}

// ownsVirtualKey reports whether vk belongs to a team or customer inside the
// tenant. A key attached to neither is deployment-wide and owned by no tenant.
func (t tenant) ownsVirtualKey(ctx context.Context, gov GovernanceReader, vk *tables.TableVirtualKey) (bool, error) {
	switch {
	case vk.TeamID != nil && *vk.TeamID != "":
		return t.ownsTeam(ctx, gov, *vk.TeamID)
	case vk.CustomerID != nil && *vk.CustomerID != "":
		return t.ownsCustomer(*vk.CustomerID), nil
	default:
		return false, nil
	}
}

func (t tenant) ownsVirtualKeyID(ctx context.Context, gov GovernanceReader, id string) (bool, error) {
	vk, err := gov.GetVirtualKey(ctx, id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("virtual key lookup failed: %w", err)
	}
	return t.ownsVirtualKey(ctx, gov, vk)
}

// ownsModelConfig reports whether mc applies to a virtual key inside the
// tenant. Global and other scopes reach past any one tenant.
func (t tenant) ownsModelConfig(ctx context.Context, gov GovernanceReader, mc *tables.TableModelConfig) (bool, error) {
	if mc.Scope != tables.ModelConfigScopeVirtualKey || mc.ScopeID == nil || *mc.ScopeID == "" {
		return false, nil
	}
	return t.ownsVirtualKeyID(ctx, gov, *mc.ScopeID)
}

// ownsBudget follows the budget to whichever row owns it. A budget on a
// virtual key's provider config has no lookup here, so it fails closed.
func (t tenant) ownsBudget(ctx context.Context, gov GovernanceReader, budget *tables.TableBudget) (bool, error) {
	switch {
	case budget.TeamID != nil && *budget.TeamID != "":
		return t.ownsTeam(ctx, gov, *budget.TeamID)
	case budget.CustomerID != nil && *budget.CustomerID != "":
		return t.ownsCustomer(*budget.CustomerID), nil
	case budget.VirtualKeyID != nil && *budget.VirtualKeyID != "":
		return t.ownsVirtualKeyID(ctx, gov, *budget.VirtualKeyID)
	case budget.ModelConfigID != nil && *budget.ModelConfigID != "":
		mc, err := gov.GetModelConfigByID(ctx, *budget.ModelConfigID)
		if err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				return false, nil
			}
			return false, fmt.Errorf("model config lookup failed: %w", err)
		}
		return t.ownsModelConfig(ctx, gov, mc)
	default:
		return false, nil
	}
}

// ownsRateLimit reports whether rateLimitID belongs to the tenant.
func (t tenant) ownsRateLimit(ctx context.Context, gov GovernanceReader, rateLimitID string) (bool, error) {
	index, err := t.index(ctx, gov)
	if err != nil {
		return false, err
	}
	return index.rateLimits[rateLimitID], nil
}

// tenantIndex is every governance row the tenant owns, gathered in one walk so
// a list can be filtered row by row without a lookup each. Rate limits carry no
// owner column, so they are found through the rows that point at them.
type tenantIndex struct {
	customer     *tables.TableCustomer
	teams        []tables.TableTeam
	keys         []tables.TableVirtualKey
	modelConfigs []tables.TableModelConfig
	teamIDs      map[string]bool
	keyIDs       map[string]bool
	configIDs    map[string]bool
	rateLimits   map[string]bool
}

// ownsBudget answers from the index: a budget on one of the tenant's teams,
// its customer, its keys or its keys' model configs.
func (x *tenantIndex) ownsBudget(budget *tables.TableBudget) bool {
	has := func(id *string, set map[string]bool) bool { return id != nil && set[*id] }
	switch {
	case budget.TeamID != nil && *budget.TeamID != "":
		return has(budget.TeamID, x.teamIDs)
	case budget.CustomerID != nil && *budget.CustomerID != "":
		return x.customer != nil && *budget.CustomerID == x.customer.ID
	case budget.VirtualKeyID != nil && *budget.VirtualKeyID != "":
		return has(budget.VirtualKeyID, x.keyIDs)
	case budget.ModelConfigID != nil && *budget.ModelConfigID != "":
		return has(budget.ModelConfigID, x.configIDs)
	default:
		return false
	}
}

func (t tenant) index(ctx context.Context, gov GovernanceReader) (*tenantIndex, error) {
	x := &tenantIndex{teamIDs: map[string]bool{}, keyIDs: map[string]bool{}, configIDs: map[string]bool{}, rateLimits: map[string]bool{}}
	addLimit := func(id *string) {
		if id != nil && *id != "" {
			x.rateLimits[*id] = true
		}
	}
	if t.customerID != "" {
		customer, err := gov.GetCustomer(ctx, t.customerID)
		if err != nil && !errors.Is(err, configstore.ErrNotFound) {
			return nil, fmt.Errorf("customer lookup failed: %w", err)
		}
		if customer != nil {
			x.customer = customer
			addLimit(customer.RateLimitID)
		}
	}
	if t.teamID != "" {
		team, err := gov.GetTeam(ctx, t.teamID)
		if err != nil && !errors.Is(err, configstore.ErrNotFound) {
			return nil, fmt.Errorf("team lookup failed: %w", err)
		}
		if team != nil {
			x.teams = append(x.teams, *team)
		}
	} else {
		for offset := 0; ; {
			teams, total, err := gov.GetTeamsPaginated(ctx, configstore.TeamsQueryParams{CustomerID: t.customerID, Limit: 100, Offset: offset})
			if err != nil {
				return nil, fmt.Errorf("list teams failed: %w", err)
			}
			for i := range teams {
				// The filter is the store's; ownership is checked here.
				if teams[i].CustomerID != nil && *teams[i].CustomerID == t.customerID {
					x.teams = append(x.teams, teams[i])
				}
			}
			offset += len(teams)
			if len(teams) == 0 || int64(offset) >= total {
				break
			}
		}
	}
	queries := make([]configstore.VirtualKeyQueryParams, 0, len(x.teams)+1)
	for i := range x.teams {
		x.teamIDs[x.teams[i].ID] = true
		addLimit(x.teams[i].RateLimitID)
		queries = append(queries, configstore.VirtualKeyQueryParams{TeamID: x.teams[i].ID, Export: true})
	}
	if t.customerID != "" {
		queries = append(queries, configstore.VirtualKeyQueryParams{CustomerID: t.customerID, Export: true})
	}
	for _, params := range queries {
		keys, _, err := gov.GetVirtualKeysPaginated(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("list virtual keys failed: %w", err)
		}
		for i := range keys {
			vk := &keys[i]
			inside := (vk.TeamID != nil && x.teamIDs[*vk.TeamID]) ||
				(vk.TeamID == nil && vk.CustomerID != nil && t.customerID != "" && *vk.CustomerID == t.customerID)
			if !inside || x.keyIDs[vk.ID] {
				continue
			}
			x.keyIDs[vk.ID] = true
			x.keys = append(x.keys, *vk)
			addLimit(vk.RateLimitID)
		}
	}
	if len(x.keyIDs) == 0 {
		return x, nil
	}
	for offset := 0; ; {
		configs, total, err := gov.GetModelConfigsPaginated(ctx, configstore.ModelConfigsQueryParams{Scopes: []string{tables.ModelConfigScopeVirtualKey}, Limit: 100, Offset: offset})
		if err != nil {
			return nil, fmt.Errorf("list model configs failed: %w", err)
		}
		for i := range configs {
			mc := configs[i]
			if mc.Scope == tables.ModelConfigScopeVirtualKey && mc.ScopeID != nil && x.keyIDs[*mc.ScopeID] {
				x.configIDs[mc.ID] = true
				x.modelConfigs = append(x.modelConfigs, mc)
				addLimit(mc.RateLimitID)
			}
		}
		offset += len(configs)
		if len(configs) == 0 || int64(offset) >= total {
			break
		}
	}
	return x, nil
}

// readTenant resolves the tenant for a read of governance rows. known is false
// for an unscoped caller, who sees everything. storeScoped says whether the
// store applies the request's own row filter to these rows (virtual keys,
// teams, customers do; budgets, rate limits and model configs do not): a scope
// that is not one team or customer can lean on that filter where it exists and
// is refused where it does not.
func readTenant(ctx context.Context, storeScoped bool) (t tenant, known bool, err error) {
	t, scoped, err := callerTenant(ctx)
	if !scoped {
		return tenant{}, false, nil
	}
	if err != nil {
		if storeScoped {
			return tenant{}, false, nil
		}
		return tenant{}, false, err
	}
	return t, true, nil
}

// tenantQueryScope is the row filter for the tenant's log reads: the same
// columns stampQueryScope filters on at the /mcp gateway, team first.
func tenantQueryScope(t tenant) queryscope.QueryScope {
	if t.teamID != "" {
		teamID := t.teamID
		return func(db *gorm.DB) *gorm.DB { return db.Where("team_id = ?", teamID) }
	}
	customerID := t.customerID
	return func(db *gorm.DB) *gorm.DB { return db.Where("customer_id = ?", customerID) }
}

// withoutQueryScope hides the request's row filter from the config store. The
// filter is a blind WHERE on team_id or customer_id, which fits the log tables
// and the virtual key table but not teams (no team_id) or customers (neither),
// where it fails the query. Governance tools filter by tenant explicitly
// instead - see the owns* checks - so they read through this.
func withoutQueryScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, schemas.BifrostContextKeyQueryScope, queryscope.QueryScope(nil))
}

// tenantCallContext decides whether a scoped caller may run tool at all, and
// what context it runs with. refusal is non-empty when it may not.
//
//   - A tool not marked tenantScoped reads or changes deployment-wide
//     configuration and is refused.
//   - A scope that is not one team or customer (errUnknownTenant) refuses
//     writes; reads keep running under the scope already on the request,
//     which is what the dashboard's own data-access scope relies on.
//   - For a known tenant, log tools (and readsLogRows tools, which hydrate
//     log rows without LogManager) run under the tenant's row filter - set
//     here when no route set one, as /v1/mcp/tool/execute does not - and
//     governance tools read unfiltered and check ownership themselves.
func tenantCallContext(ctx context.Context, tool Tool) (context.Context, string) {
	t, scoped, err := callerTenant(ctx)
	if !scoped {
		return ctx, ""
	}
	if !tool.tenantScoped {
		return ctx, tool.name + " reads or changes deployment-wide configuration, and this request is scoped to one team or customer; only an unscoped key or an admin can run it."
	}
	if err != nil {
		if tool.mutating {
			return ctx, err.Error()
		}
		return ctx, ""
	}
	if tool.noLogs && !tool.readsLogRows {
		return withoutQueryScope(ctx), ""
	}
	if queryscope.FromContext(ctx) == nil {
		return queryscope.WithQueryScope(ctx, tenantQueryScope(t)), ""
	}
	return ctx, ""
}

// requireOwned turns an ownership answer into the tool's error.
func requireOwned(owned bool, err error, t tenant, what string) error {
	if err != nil {
		return err
	}
	if !owned {
		return outsideTenant(t, what)
	}
	return nil
}

// checkTenant runs check with the caller's tenant, and only for a scoped
// caller; an unscoped one passes.
func checkTenant(ctx context.Context, check func(t tenant) error) error {
	t, scoped, err := callerTenant(ctx)
	if err != nil {
		return err
	}
	if !scoped {
		return nil
	}
	return check(t)
}

// hiddenOutsideTenant returns notFound when a scoped caller does not own the
// row, so another tenant's ids read exactly like ids that do not exist.
func hiddenOutsideTenant(ctx context.Context, storeScoped bool, owns func(t tenant) (bool, error), notFound error) error {
	t, known, err := readTenant(ctx, storeScoped)
	if err != nil {
		return err
	}
	if !known {
		return nil
	}
	owned, err := owns(t)
	if err != nil {
		return err
	}
	if !owned {
		return notFound
	}
	return nil
}

// matchesSearch is the case-insensitive substring match the stores apply to
// a search argument, for lists built from the tenant index in memory.
func matchesSearch(value, search string) bool {
	return search == "" || strings.Contains(strings.ToLower(value), strings.ToLower(search))
}
