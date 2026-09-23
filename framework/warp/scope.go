package warp

import (
	"context"

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
//
// all is an explicit scope: "all" on the filter object. It is a different
// question from one that simply named nothing, and the two are
// indistinguishable once both arrive as an empty filter, so it has to be
// carried here rather than inferred. It widens the question, never the
// permission: the store still applies the caller's queryscope, so "all" is
// everything the caller may see.
func applyScope(filters *logstore.SearchFilters, scope Scope, all bool) {
	if filters == nil || !scope.HasIdentity || all {
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
		len(filters.ProjectIDs) > 0 ||
		len(filters.VirtualKeyIDs) > 0
}

// scopeNote reports which of three shapes a result's scope takes: "self"
// (defaulted to the person asking), "named" (whatever the filters specified),
// or "all" (the whole deployment).
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
	default:
		// "all" is the tag; the prompt spells out that ScopedDB still applies the
		// caller's queryscope, so it means everything they may see, not the whole
		// deployment.
		return "all"
	}
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
