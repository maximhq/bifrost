package warp

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// Query scope: which slice of the deployment's traffic a question is about.
//
// This is a *precision* mechanism, not an access control. Row-level access is
// already enforced by framework/queryscope, which the store applies to every
// read regardless of what Warp asks for - so widening a query can never surface
// data the caller could not fetch from the logs API directly. What this solves
// is a different failure: on a deployment serving many teams and customers,
// "what did we spend last week?" has several correct answers, and silently
// picking the widest one produces a confident number about the wrong thing.
//
// The rule is:
//
//   - When the caller has an identity, their own traffic is the default. That is
//     the question people usually mean, and it is the one they can always check.
//   - When there is no identity, there is no sensible default, so Warp is told
//     to ask which team, customer or business unit is meant before querying.
//   - An explicit scope in the question always wins over the default. Asking
//     about another team is a legitimate question; the store decides whether the
//     answer is allowed.
type Scope struct {
	// HasIdentity reports whether the caller is a known user. It drives whether
	// Warp defaults or asks.
	HasIdentity bool
	UserID      string
}

// ScopeFromContext derives the caller's scope.
//
// Read from the context, never from the request: a scope the caller can name in
// the body would be a suggestion, and this needs to be a fact about who asked.
func ScopeFromContext(ctx context.Context) Scope {
	userID, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string)
	if userID == "" {
		return Scope{}
	}
	return Scope{HasIdentity: true, UserID: userID}
}

// applyScope narrows filters to the caller's default when the question named
// no scope of its own.
//
// "Named no scope" means every dimension is empty. A question that mentions any
// one of them is taken as deliberate and left alone - narrowing "how did team X
// do?" to the asker's own traffic would answer a question nobody asked, and the
// answer would look right.
func applyScope(filters *logstore.SearchFilters, scope Scope) {
	if filters == nil || !scope.HasIdentity {
		return
	}
	if filtersNameAScope(filters) {
		return
	}
	filters.UserIDs = []string{scope.UserID}
}

// filtersNameAScope reports whether the model asked about a particular
// slice of traffic.
//
// Virtual keys count: asking about a key is asking about whoever uses it, and
// layering the caller's own id on top would return the intersection - usually
// nothing, reported as a confident zero.
func filtersNameAScope(filters *logstore.SearchFilters) bool {
	return len(filters.UserIDs) > 0 ||
		len(filters.TeamIDs) > 0 ||
		len(filters.CustomerIDs) > 0 ||
		len(filters.BusinessUnitIDs) > 0 ||
		len(filters.VirtualKeyIDs) > 0
}

// scopeNote describes, in one line, what a result actually covers.
//
// Returned alongside every scoped result so the model can say so in its answer.
// A number whose scope is invisible is the failure this whole mechanism exists
// to prevent, and the model cannot report a scope it was never told about.
func scopeNote(filters *logstore.SearchFilters, scope Scope) string {
	switch {
	case len(filters.UserIDs) == 1 && scope.HasIdentity && filters.UserIDs[0] == scope.UserID:
		return "Scoped to the person asking. Say so in your answer, and mention that a team, customer or business unit can be named to widen it."
	case filtersNameAScope(filters):
		return "Scoped to the dimensions named in the filters. State which ones in your answer."
	default:
		return "Covers the whole deployment - every user, team and customer. Say so plainly, because it is rarely what someone means by 'we'."
	}
}

// describeScopeTool lets Warp find out who is asking and what it could
// narrow to, so it can ask a specific question rather than a vague one.
func describeScopeTool() Tool {
	return Tool{
		name: "describe_scope",
		description: "Report who is asking and which teams, customers, business units and virtual keys exist. " +
			"Call this first when a question about usage, spend or performance does not say whose traffic it means. " +
			"With a known user, their own traffic is the default. Without one there is no default, so ask which team, customer or business unit is meant before querying.",
		schemaJSON: `{"type": "object", "properties": {}}`,
		execute: func(ctx context.Context, deps *ToolDeps, _ map[string]any) (any, error) {
			const limit = 50
			out := map[string]any{
				"caller_is_identified": deps.scope.HasIdentity,
				"default_scope": func() string {
					if deps.scope.HasIdentity {
						return "the person asking"
					}
					return "none - ask which team, customer or business unit is meant"
				}(),
			}
			if deps.scope.HasIdentity {
				out["caller_user_id"] = deps.scope.UserID
			}

			// Dimensions come from logged traffic, so they list what actually
			// exists rather than what is merely configured. A team with no requests
			// cannot be the answer to a usage question anyway.
			virtualKeys, err := deps.logManager.GetAvailableVirtualKeys(ctx, limit, "")
			if err != nil {
				return nil, err
			}
			out["virtual_keys"] = virtualKeys
			for key, dimension := range map[string]logstore.RankingDimension{
				"teams":          logstore.RankingDimensionTeam,
				"customers":      logstore.RankingDimensionCustomer,
				"business_units": logstore.RankingDimensionBusinessUnit,
			} {
				names, err := dimensionNames(ctx, deps, dimension)
				if err != nil {
					return nil, err
				}
				out[key] = names
			}
			return out, nil
		},
	}
}

// dimensionNames lists the entities seen on a dimension.
//
// Read from rankings over a wide window rather than from the governance tables:
// the point is to offer scopes that have traffic, and a configured-but-unused
// team is a dead end for every question this tool precedes.
func dimensionNames(ctx context.Context, deps *ToolDeps, dimension logstore.RankingDimension) ([]string, error) {
	limit := 25
	filters := &logstore.SearchFilters{RankingLimit: &limit}
	now := Now()
	start := now.Add(-30 * 24 * time.Hour)
	filters.StartTime, filters.EndTime = &start, &now

	result, err := deps.logManager.GetDimensionRankings(ctx, filters, dimension)
	if err != nil {
		return nil, err
	}
	labels := make([]string, 0, len(result.Rankings))
	for _, entry := range result.Rankings {
		// Prefer the name; fall back to the id so an unnamed entity is still
		// something the model can put in a filter.
		if entry.Name != "" {
			labels = append(labels, entry.Name+" ("+entry.ID+")")
			continue
		}
		labels = append(labels, entry.ID)
	}
	return labels, nil
}
