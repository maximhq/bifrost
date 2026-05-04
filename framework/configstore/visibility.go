package configstore

import (
	"context"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// applyVirtualKeyVisibility narrows a virtual-keys query to rows the caller
// is allowed to see. Reads the VisibilityFilter for EntityVirtualKey from
// the per-request provider stashed on context.
//
// When no provider is on context (DAC disabled / OSS-only deployment), or
// the provider returns a nil/unrestricted filter (all-data scope), the
// query is returned unchanged. Otherwise the filter contributes a single
// OR-joined WHERE expression mapping VirtualKeyIDs → governance_virtual_keys.id
// and TeamIDs → governance_virtual_keys.team_id. Empty slices match no rows
// for that dimension; nil dimensions impose no restriction.
func applyVirtualKeyVisibility(ctx context.Context, db *gorm.DB) *gorm.DB {
	f := schemas.FilterForEntity(ctx, schemas.EntityVirtualKey)
	if f.IsUnrestricted() {
		return db
	}
	var (
		clauses []string
		args    []any
	)
	if f.VirtualKeyIDs != nil {
		if ids := *f.VirtualKeyIDs; len(ids) == 0 {
			clauses = append(clauses, "1 = 0")
		} else {
			clauses = append(clauses, "governance_virtual_keys.id IN ?")
			args = append(args, ids)
		}
	}
	if f.TeamIDs != nil {
		if ids := *f.TeamIDs; len(ids) == 0 {
			clauses = append(clauses, "1 = 0")
		} else {
			clauses = append(clauses, "governance_virtual_keys.team_id IN ?")
			args = append(args, ids)
		}
	}
	if len(clauses) == 0 {
		return db
	}
	return db.Where("("+strings.Join(clauses, " OR ")+")", args...)
}

// applyPromptVisibility narrows a prompts query to rows the caller is
// allowed to see. Maps UserIDs → prompts.user_id and TeamIDs →
// prompts.team_id.
func applyPromptVisibility(ctx context.Context, db *gorm.DB) *gorm.DB {
	f := schemas.FilterForEntity(ctx, schemas.EntityPrompt)
	if f.IsUnrestricted() {
		return db
	}
	var (
		clauses []string
		args    []any
	)
	if f.UserIDs != nil {
		if ids := *f.UserIDs; len(ids) == 0 {
			clauses = append(clauses, "1 = 0")
		} else {
			clauses = append(clauses, "prompts.user_id IN ?")
			args = append(args, ids)
		}
	}
	if f.TeamIDs != nil {
		if ids := *f.TeamIDs; len(ids) == 0 {
			clauses = append(clauses, "1 = 0")
		} else {
			clauses = append(clauses, "prompts.team_id IN ?")
			args = append(args, ids)
		}
	}
	if len(clauses) == 0 {
		return db
	}
	return db.Where("("+strings.Join(clauses, " OR ")+")", args...)
}

// applyRoutingRuleVisibility narrows a routing-rules query to rules the
// caller is allowed to see. The "global" scope is always implicitly
// allowed — RoutingScopes only enumerates the user/team/virtual_key
// entries.
func applyRoutingRuleVisibility(ctx context.Context, db *gorm.DB) *gorm.DB {
	f := schemas.FilterForEntity(ctx, schemas.EntityRoutingRule)
	if f.IsUnrestricted() || f.RoutingScopes == nil {
		return db
	}
	clauses := []string{"routing_rules.scope = ?"}
	args := []any{"global"}
	for _, m := range *f.RoutingScopes {
		clauses = append(clauses, "(routing_rules.scope = ? AND routing_rules.scope_id = ?)")
		args = append(args, m.Scope, m.ScopeID)
	}
	return db.Where("("+strings.Join(clauses, " OR ")+")", args...)
}

// applyTeamVisibility narrows a teams query to rows the caller is allowed
// to see. Maps OwnTeamIDs → governance_teams.id.
//
// OwnTeamIDs is the principal's own team-membership set in both own-data
// and team-data modes (it doesn't widen for team-data — a user only sees
// the teams they personally belong to; team-data widens visibility for
// users/roles/logs/etc., not for the teams list itself).
func applyTeamVisibility(ctx context.Context, db *gorm.DB) *gorm.DB {
	f := schemas.FilterForEntity(ctx, schemas.EntityTeam)
	if f.IsUnrestricted() || f.OwnTeamIDs == nil {
		return db
	}
	if ids := *f.OwnTeamIDs; len(ids) == 0 {
		return db.Where("1 = 0")
	} else {
		return db.Where("governance_teams.id IN ?", ids)
	}
}

// applyCustomerVisibility narrows a customers query to rows the caller is
// allowed to see. Maps CustomerIDs → governance_customers.id.
//
// Customers are scoped via team membership: the principal sees the
// customers attached to any team they belong to (via
// governance_teams.customer_id). Empty CustomerIDs means the user is in
// no teams that have customers, so no rows are visible.
func applyCustomerVisibility(ctx context.Context, db *gorm.DB) *gorm.DB {
	f := schemas.FilterForEntity(ctx, schemas.EntityCustomer)
	if f.IsUnrestricted() || f.CustomerIDs == nil {
		return db
	}
	if ids := *f.CustomerIDs; len(ids) == 0 {
		return db.Where("1 = 0")
	} else {
		return db.Where("governance_customers.id IN ?", ids)
	}
}
