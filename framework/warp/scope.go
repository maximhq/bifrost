package warp

import (
	"context"
	"fmt"
	"strings"
	"sync"

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
func applyScope(filters *logstore.SearchFilters, scope Scope, mode ScopeMode) {
	if filters == nil || !scope.HasIdentity {
		return
	}
	// An explicit request for the whole deployment is a different question from
	// one that simply did not say, and the two were indistinguishable while both
	// arrived as an empty filter. Row-level queryscope still bounds what "all"
	// can return, so this widens the question, never the permission.
	if mode == ScopeModeAll {
		return
	}
	// Checked before the named-dimension bail-out. An explicit "caller" is a
	// deliberate statement about whose traffic is meant, so it has to apply even
	// when a dimension is also named - the store writes UserIDs and each
	// dimension as separate WHERE clauses, so the two intersect, which is
	// exactly what "my traffic in that team" means. Returning early made
	// scope:"caller" with team_ids a team-wide answer reported as the caller's
	// own, which is the one shape of wrong answer that reads as right.
	if mode == ScopeModeCaller {
		filters.UserIDs = []string{scope.UserID}
		return
	}
	if filtersNameAScope(filters) {
		return
	}
	filters.UserIDs = []string{scope.UserID}
}

// ScopeMode is how the model said whose traffic it means.
type ScopeMode string

const (
	// ScopeModeUnset is an omitted scope: the caller default applies.
	ScopeModeUnset ScopeMode = ""
	// ScopeModeCaller asks for the caller's own traffic, explicitly.
	ScopeModeCaller ScopeMode = "caller"
	// ScopeModeAll asks for the whole deployment, as far as the caller is
	// permitted to see it.
	ScopeModeAll ScopeMode = "all"
)

// ParseScopeMode reads the scope marker off a filter object.
//
// An unrecognised value is rejected rather than read as the default: guessing
// would answer a different question than the one asked, and the answer would
// look right.
func ParseScopeMode(raw any) (ScopeMode, error) {
	if raw == nil {
		return ScopeModeUnset, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("scope must be a string, one of %q or %q", ScopeModeCaller, ScopeModeAll)
	}
	switch ScopeMode(strings.TrimSpace(value)) {
	case ScopeModeUnset:
		return ScopeModeUnset, nil
	case ScopeModeCaller:
		return ScopeModeCaller, nil
	case ScopeModeAll:
		return ScopeModeAll, nil
	}
	return "", fmt.Errorf("unknown scope %q. Use %q for the caller's own traffic or %q for the whole deployment", value, ScopeModeCaller, ScopeModeAll)
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

// scopeNote describes, in one line, what a result actually covers.
//
// Returned alongside every scoped result so the model can say so in its answer.
// A number whose scope is invisible is the failure this whole mechanism exists
// to prevent, and the model cannot report a scope it was never told about.
func scopeNote(filters *logstore.SearchFilters, scope Scope) string {
	switch {
	// Only when the caller is the whole story. parseFilters fills each dimension
	// independently, so a filter can carry the caller's own id and a team as
	// well - and calling that "the person asking" tells the model to describe
	// something narrower than the query covers, which is the exact failure this
	// note exists to prevent.
	case len(filters.UserIDs) == 1 && scope.HasIdentity && filters.UserIDs[0] == scope.UserID &&
		len(filters.TeamIDs) == 0 && len(filters.CustomerIDs) == 0 &&
		len(filters.BusinessUnitIDs) == 0 && len(filters.VirtualKeyIDs) == 0:
		return "Scoped to the person asking. Say so in your answer, and mention that a team, customer or business unit can be named to widen it."
	case filtersNameAScope(filters):
		return "Scoped to the dimensions named in the filters. State which ones in your answer."
	default:
		// Not "the whole deployment": ScopedDB still applies the caller's
		// queryscope, so this is everything they are permitted to see and no more.
		// Claiming deployment-wide coverage would put an assertion in the answer
		// that the data behind it does not support.
		return "Covers all traffic the caller is permitted to see - every user, team and customer within their access. Say so plainly, because it is rarely what someone means by 'we'."
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
			// Deliberately not caller_user_id. This payload goes to the configured
			// model, which is usually a third-party provider, and the id is only
			// ever used server-side by applyScope - so sending it is identity data
			// leaving the deployment in exchange for nothing the model can use.

			// Dimensions come from logged traffic, so they list what actually
			// exists rather than what is merely configured. A team with no requests
			// cannot be the answer to a usage question anyway.
			//
			// These are the distinct-value lookups the Logs filter bar uses: one
			// indexed DISTINCT each. The rankings that used to stand here rank by
			// spend, which nothing downstream needs, and on the enterprise
			// hierarchy path fan every row out through JSON-array columns - tens
			// of seconds on a large table before Warp could even ask its question.
			//
			// The four run concurrently. Each is a scan of the log table, and on a
			// large SQLite file with cold pages that is I/O-bound, so running them
			// one after another paid the disk four times over.
			lookups := []struct {
				key    string
				lookup func(context.Context, int, string) ([]KeyPair, error)
			}{
				{"virtual_keys", deps.logManager.GetAvailableVirtualKeys},
				{"teams", deps.logManager.GetAvailableTeams},
				{"customers", deps.logManager.GetAvailableCustomers},
				{"business_units", deps.logManager.GetAvailableBusinessUnits},
			}
			results := make([][]KeyPair, len(lookups))
			errs := make([]error, len(lookups))
			var wg sync.WaitGroup
			for index, entry := range lookups {
				wg.Add(1)
				go func() {
					defer wg.Done()
					results[index], errs[index] = entry.lookup(ctx, limit, "")
				}()
			}
			wg.Wait()
			for index, entry := range lookups {
				if errs[index] != nil {
					return nil, errs[index]
				}
				if entry.key == "virtual_keys" {
					out[entry.key] = results[index]
					continue
				}
				out[entry.key] = keyPairLabels(results[index])
			}
			return out, nil
		},
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
