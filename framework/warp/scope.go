package warp

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/queryscope"
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
//   - Unless no row-level scope applies to them - the local admin, or any caller
//     the store does not restrict. Everything such a caller may see is the whole
//     deployment, and their own traffic is usually a few dashboard checks, so
//     "how much have we spent" from them means the deployment. Their default
//     is the deployment, tagged "deployment" so the model can say so and add a
//     breakdown; "I" and "my" need the caller named in the filter.
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
	// Unrestricted reports that no row-level query scope applies to the caller,
	// so everything they may see is the whole deployment.
	Unrestricted bool
	// Visibility says, in a few words, which slice a restricted caller may see
	// ("their teams' traffic"). Empty when the caller is unrestricted or the
	// deployment has no resolver to say.
	Visibility string
	// Visible is the row ownership a restricted caller's reads are narrowed
	// to, when the deployment can say. Semantic search prefilters the index
	// with it; nil leaves the search unfiltered, as it is for everyone else.
	Visible *LogVisibility
}

// CallerRestriction is what the deployment's row-level access control says
// about one caller.
type CallerRestriction struct {
	// Restricted reports that the store will narrow this caller's reads.
	Restricted bool
	// Visibility is a short phrase for what the caller may see, written to
	// follow "the person asking may see ...". Optional.
	Visibility string
	// Logs is the row ownership the store grants the caller on log reads.
	// Optional: it only sharpens semantic search (see LogVisibility).
	Logs *LogVisibility
}

// CallerRestrictionResolver reports whether row-level access control narrows
// the caller's reads.
//
// It exists because ScopeFromContext can only see a queryscope that is already
// on the context, and a deployment whose store wrapper attaches the scope per
// read - the enterprise one does - never puts it there. Warp then took every
// caller for unrestricted: a team-scoped user's total was their team's rows,
// correctly filtered by the store, and described as the whole deployment's.
//
// Like the scope itself this is precision, not access control. The resolver is
// given the snapshotted context the tools run under, so it answers from the
// same identity the store will.
type CallerRestrictionResolver func(ctx context.Context) CallerRestriction

// withCallerRestriction folds a resolver's answer into a context-derived scope.
//
// It only ever narrows: a caller the context already shows as restricted stays
// restricted whatever the resolver says, so a resolver that knows nothing
// cannot widen a default. The local admin is left alone - they bypass RBAC by
// definition, and the store does not scope them either.
func withCallerRestriction(ctx context.Context, scope Scope, resolve CallerRestrictionResolver) Scope {
	if resolve == nil {
		return scope
	}
	if isLocalAdmin, _ := ctx.Value(schemas.IsLocalAdminContextKey).(bool); isLocalAdmin {
		return scope
	}
	restriction := resolve(ctx)
	if !restriction.Restricted {
		return scope
	}
	scope.Unrestricted = false
	scope.Visibility = restriction.Visibility
	scope.Visible = restriction.Logs
	return scope
}

// ScopeFromContext derives the caller's scope.
//
// Read from the context, never from the request: a scope the caller can name in
// the body would be a suggestion, and this needs to be a fact about who asked.
func ScopeFromContext(ctx context.Context) Scope {
	// A nil queryscope is the store's own convention for "no restriction". The
	// local admin bypasses RBAC by definition, so the flag counts on its own.
	isLocalAdmin, _ := ctx.Value(schemas.IsLocalAdminContextKey).(bool)
	scope := Scope{Unrestricted: isLocalAdmin || queryscope.FromContext(ctx) == nil}
	userID, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string)
	if userID == "" {
		return scope
	}
	scope.HasIdentity, scope.UserID = true, userID
	return scope
}

// applyScope narrows filters to the caller's default when the question named
// no scope of its own.
//
// "Named no scope" means every dimension is empty. A question that mentions any
// one of them is taken as deliberate and left alone - narrowing "how did team X
// do?" to the asker's own traffic would answer a question nobody asked, and the
// answer would look right.
//
// all is an explicit scope: "all" on the filter object. It is a different
// question from one that simply named nothing, and the two are
// indistinguishable once both arrive as an empty filter, so it has to be
// carried here rather than inferred. It widens the question, never the
// permission: the store still applies the caller's queryscope, so "all" is
// everything the caller may see.
//
// An unrestricted caller is never narrowed: the deployment is their default
// (see the package note), and the model names them in user_ids when the
// question is about them alone.
func applyScope(filters *logstore.SearchFilters, scope Scope, all bool) {
	if filters == nil || !scope.HasIdentity || scope.Unrestricted || all {
		return
	}
	if filtersNameAScope(filters) {
		return
	}
	filters.UserIDs = []string{scope.UserID}
}

// rankingScope is the scope a ranking by dimension defaults to.
//
// A ranking across people, org units or keys is a question about more than the
// asker: narrowed to their own traffic, "top 5 users by cost" ranks one user,
// and an admin was told only one user had traffic while the dashboard beside
// the answer listed nineteen. Those rankings take no default, so they cover
// everything the store's queryscope lets the caller see. A ranking of what the
// traffic was - error type, app, routing rule - keeps the caller's default.
func rankingScope(scope Scope, dimension string) Scope {
	switch logstore.RankingDimension(dimension) {
	case logstore.RankingDimensionUser, logstore.RankingDimensionTeam, logstore.RankingDimensionCustomer,
		logstore.RankingDimensionBusinessUnit, logstore.RankingDimensionProject, logstore.RankingDimensionVirtualKey:
		return Scope{}
	}
	return scope
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
		len(filters.ProjectIDs) > 0 ||
		len(filters.VirtualKeyIDs) > 0
}

// scopeNote reports which of four shapes a result's scope takes: "self"
// (defaulted to the person asking), "named" (whatever the filters specified),
// "deployment" (an unrestricted caller's default: genuinely everything), or
// "all" (everything a restricted caller may see).
//
// Returned alongside every scoped result so the model can say so in its
// answer - a number whose scope is invisible is the failure this whole
// mechanism exists to prevent. It is a compact tag rather than a sentence on
// purpose: the system prompt already spells out what each of the three means
// and how to phrase it, so restating that advice on every single result would
// be the same paragraph paid for again on every call - and it compounds,
// since a result stays in the replayed conversation for the rest of the loop,
// not just the step it was returned on.
func scopeNote(filters *logstore.SearchFilters, scope Scope) string {
	switch {
	// Only when the caller is the whole story. parseFilters fills each dimension
	// independently, so a filter can carry the caller's own id and a team as
	// well - and calling that "self" tells the model the answer is narrower
	// than the query covers, which is the exact failure this note prevents.
	case len(filters.UserIDs) == 1 && scope.HasIdentity && filters.UserIDs[0] == scope.UserID &&
		len(filters.TeamIDs) == 0 && len(filters.CustomerIDs) == 0 &&
		len(filters.BusinessUnitIDs) == 0 && len(filters.ProjectIDs) == 0 &&
		len(filters.VirtualKeyIDs) == 0:
		return "self"
	case filtersNameAScope(filters):
		return "named"
	case scope.Unrestricted:
		// Its own tag rather than "all": for this caller the result really is the
		// whole deployment, and it was the default rather than a choice - which
		// is what the prompt keys the breakdown on.
		return "deployment"
	default:
		// "all" is the tag; the prompt spells out that ScopedDB still applies the
		// caller's queryscope, so it means everything they may see, not the whole
		// deployment.
		return "all"
	}
}

// noteCallerVisibility adds caller_can_see to a result whose scope tag does not
// already say what a restricted caller's answer covers.
//
// describe_filter_space reports it once, but a turn that never calls that tool
// had only the tag to go on, and "all" - everything the person may see - was
// then written up as the whole deployment. "named" carries it as well: a team
// outside the caller's view comes back empty exactly as a team with no traffic
// does, and the model needs to know that an empty result is not evidence the
// team is idle or the id wrong. "self" is the caller's own traffic and says so.
//
// Applied where every tool's result passes (see Agent.executeTool) rather than
// in each tool, so a tool added later cannot leave it out.
func noteCallerVisibility(result any, scope Scope) any {
	if scope.Visibility == "" {
		return result
	}
	out, ok := result.(map[string]any)
	if !ok {
		return result
	}
	if tag, _ := out["scope"].(string); tag == "all" || tag == "named" {
		out["caller_can_see"] = scope.Visibility
	}
	return out
}

// logNotFound is the refusal for a log id that loaded nothing.
//
// For a restricted caller a row outside their view and a row that does not
// exist look the same, and must: saying which would disclose that the row
// exists. But "no such log" is then a claim the lookup cannot support, and the
// model went on to call the id mistyped. The error says what was searched.
func logNotFound(id string, scope Scope) error {
	if scope.Visibility == "" {
		return fmt.Errorf("no log found with id %s", id)
	}
	return fmt.Errorf("no log found with id %s among the rows the person asking may see (%s). A log outside that is not visible to them, so say their view is limited to it rather than that the id does not exist or is mistyped", id, scope.Visibility)
}

// keyPairLabels renders id/name pairs the way the model puts them in a
// filter: the name for reading, the id in brackets for the query. An unnamed
// entity is still listed by id so it can be chosen.
func keyPairLabels(pairs []KeyPair) []string {
	labels := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		if pair.Name != "" {
			labels = append(labels, pair.Name+" ("+pair.ID+")")
			continue
		}
		labels = append(labels, pair.ID)
	}
	return labels
}
