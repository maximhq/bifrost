package mcptools

import (
	"context"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
)

// Query scope: which slice of the deployment's traffic a question is about.
//
// This is a *precision* mechanism, not an access control. Row-level access is
// already enforced by framework/queryscope, which the store applies to every
// read regardless of what a tool asks for - so widening a query can never
// surface data the caller could not fetch from the logs API directly. What
// this solves is a different failure: on a deployment serving many teams and
// customers, "what did we spend last week?" has several correct answers, and
// silently picking the widest one produces a confident number about the wrong
// thing.
//
// The rule is:
//
//   - When the caller's view names a default user, their own traffic is the
//     default. That is the question people usually mean from the dashboard,
//     and it is the one they can always check.
//   - Unless no row-level scope applies to them (CallerView.Unrestricted) - the
//     local admin, or any caller the store does not restrict. Everything such a
//     caller may see is the whole deployment, and their own traffic is usually
//     a few dashboard checks, so "how much have we spent" from them means the
//     deployment. Their default is the deployment, tagged "deployment" so the
//     model can say so; "I" and "my" need the caller named in the filter.
//   - When there is no caller view, there is no narrowing. The tool answers
//     over whatever the row-level scope already permits.
//   - An explicit scope in the question always wins over the default. Asking
//     about another team is a legitimate question; the store decides whether
//     the answer is allowed.
//
// The view is an explicit opt-in owned by this package, not an inference from
// schemas.BifrostContextKeyUserID or the queryscope on the context. Warp stamps
// one per turn (see WithCallerView); the /mcp gateway never does, so an
// external harness whose OAuth token happens to carry a user id is not
// silently narrowed by user on top of the tenant scope it was admitted with.

// callerViewKey is the context key WithCallerView writes. Unexported so
// nothing outside this package can set it without going through the helper.
type callerViewKey struct{}

// CallerView is what a consumer that knows the person asking tells the tools
// about them for one turn. Every field is precision, not access control: the
// store applies the caller's queryscope to every read whatever this says.
//
// Clock exists so every tool call in a turn agrees on the window: relative and
// calendar times ("today", "-24h", a bare date) resolve against it, in the
// asker's zone.
type CallerView struct {
	DefaultUserID string         // whose own traffic an unscoped question defaults to; empty for none
	Unrestricted  bool           // no row-level scope applies, so the default is the whole deployment
	Visibility    string         // what a restricted caller may see, e.g. "their teams' traffic"
	Visible       *LogVisibility // a restricted caller's row ownership, for the semantic prefilter
	Clock         time.Time      // the turn's pinned now in the asker's zone; zero means Now()
}

// WithCallerView returns a context whose tool calls resolve their default
// scope and clock from view.
func WithCallerView(ctx context.Context, view CallerView) context.Context {
	return context.WithValue(ctx, callerViewKey{}, view)
}

// CallerViewFromContext reads the view WithCallerView wrote. ok is false when
// the caller never opted in.
func CallerViewFromContext(ctx context.Context) (CallerView, bool) {
	view, ok := ctx.Value(callerViewKey{}).(CallerView)
	return view, ok
}

// nowFrom is the instant relative times resolve against for this call: the
// view's pinned clock when there is one, Now() otherwise.
func nowFrom(ctx context.Context) time.Time {
	if view, ok := CallerViewFromContext(ctx); ok && !view.Clock.IsZero() {
		return view.Clock
	}
	return Now()
}

// Scope is the resolved default for one tool call.
type Scope struct {
	HasIdentity  bool // a default user was opted in
	UserID       string
	Unrestricted bool
	Visibility   string
	Visible      *LogVisibility
}

// scopeFromContext derives the caller's default scope from the opt-in alone.
func scopeFromContext(ctx context.Context) Scope {
	view, ok := CallerViewFromContext(ctx)
	if !ok {
		return Scope{}
	}
	return Scope{
		HasIdentity:  view.DefaultUserID != "",
		UserID:       view.DefaultUserID,
		Unrestricted: view.Unrestricted,
		Visibility:   view.Visibility,
		Visible:      view.Visible,
	}
}

// applyScope narrows filters to the caller's default when the question named
// no scope of its own.
//
// "Named no scope" means every dimension is empty. A question that mentions
// any one of them is taken as deliberate and left alone - narrowing "how did
// team X do?" to the asker's own traffic would answer a question nobody asked,
// and the answer would look right.
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
// Visibility survives either way: it describes the caller, not the default.
func rankingScope(scope Scope, dimension string) Scope {
	switch logstore.RankingDimension(dimension) {
	case logstore.RankingDimensionUser, logstore.RankingDimensionTeam, logstore.RankingDimensionCustomer,
		logstore.RankingDimensionBusinessUnit, logstore.RankingDimensionProject, logstore.RankingDimensionVirtualKey:
		return Scope{Unrestricted: scope.Unrestricted, Visibility: scope.Visibility, Visible: scope.Visible}
	}
	return scope
}

// filtersNameAScope reports whether the caller asked about a particular
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
// purpose: the consuming prompt spells out what each of the four means, and
// restating that on every result would be the same paragraph paid for again
// on every call, replayed for the rest of the conversation.
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
		// "all" means everything the caller may see, not the whole deployment:
		// the store still applies their queryscope.
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
// Applied where every tool's result passes (see toolHandler) rather than in
// each tool, so a tool added later cannot leave it out.
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
