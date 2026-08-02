package schemas

import "context"

// Entity identifies the target row family that a VisibilityFilter is being
// resolved for. The provider closure stashed on the request context returns
// only the dimensions relevant to the requested entity, leaving every other
// dimension nil. This keeps OSS query helpers from having to reason about
// dimensions their target table cannot consume.
type Entity string

// Entity values cover every list/read endpoint that participates in
// row-level visibility filtering. Add a new constant only when a query
// helper for a new table is introduced.
const (
	EntityVirtualKey       Entity = "virtual_key"
	EntityPrompt           Entity = "prompt"
	EntityLog              Entity = "log"
	EntityRoutingRule      Entity = "routing_rule"
	EntityUser             Entity = "user"
	EntityRole             Entity = "role"
	EntityTeam             Entity = "team"
	EntityBusinessUnit     Entity = "business_unit"
	EntityCustomer         Entity = "customer"
	EntityAuditLog         Entity = "audit_log"
	EntityAccessProfile    Entity = "access_profile"
	EntityAPIKey           Entity = "api_key"
	EntityMCPToolGroup     Entity = "mcp_tool_group"
	EntityPromptDeployment Entity = "prompt_deployment"
	EntityProvider         Entity = "provider"
)

// RoutingScopeMatch is a single (scope, scope_id) pair the caller is
// allowed to see in routing_rules. The "global" scope is always implicitly
// allowed and does not need to be enumerated.
type RoutingScopeMatch struct {
	Scope   string
	ScopeID string
}

// VisibilityFilter restricts list and read queries to rows the caller is
// allowed to see.
//
// Semantics: a row is visible iff ANY non-nil dimension matches its
// corresponding column. A nil dimension means that dimension imposes no
// restriction — but other non-nil dimensions still apply. An empty slice
// means the dimension matches no rows (callers that need "everything except
// this dimension" should use a nil pointer instead). A filter with all
// dimensions nil is equivalent to "no filter" (full visibility).
//
// A VisibilityFilter is the response shape of the per-request provider
// closure (see VisibilityFilterProvider). The provider returns a filter
// with only the dimensions relevant to the requested Entity populated;
// every other dimension is nil.
//
// Each dimension lines up with a real column on the target table:
//
//   - UserIDs         → table.user_id  OR  governance_users.id (when
//     scoping the users table itself)
//   - TeamIDs         → table.team_id  (logs / prompts / VKs)
//   - OwnTeamIDs      → governance_teams.id (when scoping the teams
//     table itself; populated from the principal's own
//     team membership for both own-data and team-data)
//   - VirtualKeyIDs   → governance_virtual_keys.id (when scoping the
//     virtual_keys table itself) or table.virtual_key_id
//     (when scoping logs)
//   - RoutingScopes   → routing_rules.(scope, scope_id) tuples
//   - RoleIDs         → enterprise_governance_roles.id
//   - BusinessUnitIDs → governance_business_units.id
//   - CustomerIDs     → governance_customers.id
type VisibilityFilter struct {
	UserIDs         *[]string
	TeamIDs         *[]string
	OwnTeamIDs      *[]string
	VirtualKeyIDs   *[]string
	RoutingScopes   *[]RoutingScopeMatch
	RoleIDs         *[]uint
	BusinessUnitIDs *[]string
	CustomerIDs     *[]string
}

// IsUnrestricted reports whether the filter has no dimensions set, in which
// case query builders may skip applying any WHERE clause. A nil receiver is
// also unrestricted, so a nil filter on context is equivalent to no filter.
func (f *VisibilityFilter) IsUnrestricted() bool {
	if f == nil {
		return true
	}
	return f.UserIDs == nil &&
		f.TeamIDs == nil &&
		f.OwnTeamIDs == nil &&
		f.VirtualKeyIDs == nil &&
		f.RoutingScopes == nil &&
		f.RoleIDs == nil &&
		f.BusinessUnitIDs == nil &&
		f.CustomerIDs == nil
}

// VisibilityFilterProvider returns the visibility filter to apply for a
// given Entity. Returning nil signals full visibility (no filter applied).
//
// The provider is set on the request context by the upstream
// (enterprise) middleware. Each call returns only the dimensions of the
// VisibilityFilter that the requested Entity's table can consume — for
// example, EntityCustomer returns only CustomerIDs populated; every other
// field is nil. This lets OSS query helpers stay agnostic of how the
// filter is computed and keeps per-request allocations minimal.
type VisibilityFilterProvider func(Entity) *VisibilityFilter

// VisibilityFilterProviderFromContext returns the provider closure
// stashed on the context, or nil when none is present (background jobs,
// internal queries, requests that bypassed the upstream middleware,
// OSS-only deployments without DAC). A nil provider is equivalent to
// full visibility — query builders apply no WHERE clause.
func VisibilityFilterProviderFromContext(ctx context.Context) VisibilityFilterProvider {
	if ctx == nil {
		return nil
	}
	if v := ctx.Value(BifrostContextKeyVisibilityFilterProvider); v != nil {
		if p, ok := v.(VisibilityFilterProvider); ok {
			return p
		}
	}
	return nil
}

// FilterForEntity is a convenience that fetches the provider from ctx
// and resolves the filter for entity in one call. Returns nil when no
// provider is present (full visibility) or when the provider returns nil
// for the requested entity (e.g. all-data scope).
func FilterForEntity(ctx context.Context, entity Entity) *VisibilityFilter {
	p := VisibilityFilterProviderFromContext(ctx)
	if p == nil {
		return nil
	}
	return p(entity)
}
