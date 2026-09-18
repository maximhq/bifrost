package mcptools

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// The default scope is an explicit opt-in owned by this package. A user id
// that merely happens to be on the context - as an OAuth user-mode token on
// /mcp would stamp - must not narrow anything: that caller was admitted by
// tenant, and quietly adding a user filter on top would answer a different
// question than the one asked.
func TestScopeFromContextIsAnOptIn(t *testing.T) {
	t.Run("opted in", func(t *testing.T) {
		scope := scopeFromContext(scoped("user-7"))
		require.True(t, scope.HasIdentity)
		require.Equal(t, "user-7", scope.UserID)
	})

	t.Run("nothing on the context", func(t *testing.T) {
		scope := scopeFromContext(context.Background())
		require.False(t, scope.HasIdentity)
		require.Empty(t, scope.UserID)
	})

	t.Run("a bare BifrostContextKeyUserID is not an opt-in", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyUserID, "user-7")
		scope := scopeFromContext(ctx)
		require.False(t, scope.HasIdentity, "the user id alone must not become a default filter")
	})

	t.Run("a view naming no user is no default", func(t *testing.T) {
		scope := scopeFromContext(WithCallerView(context.Background(), CallerView{}))
		require.False(t, scope.HasIdentity)
		require.Empty(t, scope.UserID)
	})

	t.Run("the view carries what it says about the caller", func(t *testing.T) {
		visible := &LogVisibility{TeamIDs: []string{"team-1"}}
		scope := scopeFromContext(WithCallerView(context.Background(), CallerView{
			DefaultUserID: "user-7", Visibility: "their teams' traffic", Visible: visible,
		}))
		require.Equal(t, Scope{HasIdentity: true, UserID: "user-7", Visibility: "their teams' traffic", Visible: visible}, scope)
	})
}

// filterArg is the one place the default is applied, so this is where the
// opt-in rule has to hold end to end.
func TestFilterArgNarrowsOnlyWhenOptedIn(t *testing.T) {
	now := time.Now().UTC()
	args := map[string]any{"filters": map[string]any{}}

	t.Run("user id on the context without the opt-in leaves UserIDs alone", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyUserID, "user-7")
		filters, scope, err := filterArg(ctx, args, now)
		require.NoError(t, err)
		require.Empty(t, filters.UserIDs)
		require.False(t, scope.HasIdentity)
		require.Equal(t, "all", scopeNote(filters, scope))
	})

	t.Run("opted in narrows to the user", func(t *testing.T) {
		filters, scope, err := filterArg(scoped("user-7"), args, now)
		require.NoError(t, err)
		require.Equal(t, []string{"user-7"}, filters.UserIDs)
		require.Equal(t, "self", scopeNote(filters, scope))
	})
}

// With an identity and no scope in the question, the caller's own traffic is
// the default - the question people usually mean, and the one they can check.
func TestScopeDefaultsToCaller(t *testing.T) {
	filters := &logstore.SearchFilters{}
	applyScope(filters, Scope{HasIdentity: true, UserID: "user-7"}, false)
	require.Equal(t, []string{"user-7"}, filters.UserIDs)
}

// Without an identity there is no default. Silently widening to the whole
// deployment would answer a different question with a confident number.
func TestScopeDoesNotDefaultWithoutIdentity(t *testing.T) {
	filters := &logstore.SearchFilters{}
	applyScope(filters, Scope{}, false)
	require.Empty(t, filters.UserIDs)
}

// An explicit scope always wins. Narrowing "how did team X do?" to the asker's
// own traffic would answer a question nobody asked, and the answer would look
// right.
func TestScopeNeverOverridesAnExplicitScope(t *testing.T) {
	scope := Scope{HasIdentity: true, UserID: "user-7"}

	for name, filters := range map[string]*logstore.SearchFilters{
		"team":          {TeamIDs: []string{"team-1"}},
		"customer":      {CustomerIDs: []string{"cust-1"}},
		"business unit": {BusinessUnitIDs: []string{"bu-1"}},
		"another user":  {UserIDs: []string{"user-9"}},
		// Asking about a key is asking about whoever uses it; layering the
		// caller's id on top returns the intersection, usually nothing, reported
		// as a confident zero.
		"virtual key": {VirtualKeyIDs: []string{"vk-1"}},
	} {
		before := *filters
		applyScope(filters, scope, false)
		require.Equal(t, before.TeamIDs, filters.TeamIDs, name)
		require.Equal(t, before.CustomerIDs, filters.CustomerIDs, name)
		require.Equal(t, before.BusinessUnitIDs, filters.BusinessUnitIDs, name)
		require.Equal(t, before.VirtualKeyIDs, filters.VirtualKeyIDs, name)
		require.Equal(t, before.UserIDs, filters.UserIDs, name)
	}
}

// An admin's own traffic is nearly always dashboard testing, and "how much
// have we spent" from the deployment's owner means the deployment. A caller
// with no row-level query scope sees the whole deployment, so that is their
// default rather than themselves. The store still applies whatever scope it
// has, so this widens the question and never the permission.
func TestScopeUnrestrictedCallerDefaultsToTheDeployment(t *testing.T) {
	unrestricted := Scope{HasIdentity: true, UserID: "admin-1", Unrestricted: true}

	t.Run("no narrowing by default", func(t *testing.T) {
		filters := &logstore.SearchFilters{}
		applyScope(filters, unrestricted, false)
		require.Empty(t, filters.UserIDs)
	})

	// "How much have I spent" still works: the model names the caller, and the
	// result is tagged as theirs.
	t.Run("own traffic on request", func(t *testing.T) {
		filters := &logstore.SearchFilters{UserIDs: []string{"admin-1"}}
		applyScope(filters, unrestricted, false)
		require.Equal(t, []string{"admin-1"}, filters.UserIDs)
		require.Equal(t, "self", scopeNote(filters, unrestricted))
	})

	// Its own tag, not "all": for this caller the default really is the whole
	// deployment, and the prompt asks for a breakdown on exactly that tag.
	t.Run("tag", func(t *testing.T) {
		require.Equal(t, "deployment", scopeNote(&logstore.SearchFilters{}, unrestricted))
		require.Equal(t, "named", scopeNote(&logstore.SearchFilters{TeamIDs: []string{"team-1"}}, unrestricted))
		require.Equal(t, "all", scopeNote(&logstore.SearchFilters{}, Scope{HasIdentity: true, UserID: "user-7"}),
			"a restricted caller's widest result is what they may see, not the deployment")
	})

	// describe_filter_space is where the model learns the default, so it has to
	// say the deployment is it, and how to ask about the person alone.
	t.Run("describe_filter_space says so", func(t *testing.T) {
		ctx := WithCallerView(context.Background(), CallerView{DefaultUserID: "admin-1", Unrestricted: true})
		out := resultMap(t, mustRunToolCtx(t, ctx, "describe_filter_space", &Deps{LogManager: &fakeLogReader{}}, map[string]any{}))
		require.Equal(t, true, out["caller_is_identified"])
		require.Equal(t, "admin-1", out["caller_user_id"])
		require.Contains(t, out["default_scope"], "whole deployment")
		require.Contains(t, out["default_scope"], `user_ids: ["admin-1"]`)
	})
}

// Scoping happens inside the shared filter parser, so a flow added later gets
// it by construction rather than by its author remembering to ask.
func TestFilterArgAppliesScope(t *testing.T) {
	now := time.Now().UTC()
	filters, _, err := filterArg(scoped("user-7"), map[string]any{"filters": map[string]any{}}, now)
	require.NoError(t, err)
	require.Equal(t, []string{"user-7"}, filters.UserIDs)
}

func TestFilterArgKeepsExplicitScope(t *testing.T) {
	now := time.Now().UTC()
	filters, _, err := filterArg(scoped("user-7"),
		map[string]any{"filters": map[string]any{"team_ids": []any{"team-1"}}},
		now,
	)
	require.NoError(t, err)
	require.Equal(t, []string{"team-1"}, filters.TeamIDs)
	require.Empty(t, filters.UserIDs)
}

// The model cannot report a scope it was never told about, so every result
// carries a tag describing what it covers. The tag is compact on purpose -
// the prompt carries the phrasing advice once, not repeated per result - so
// this only has to prove the right one of the three comes back, not that a
// sentence explaining it does.
func TestScopeNoteDescribesWhatTheResultCovers(t *testing.T) {
	scope := Scope{HasIdentity: true, UserID: "user-7"}

	require.Equal(t, "self",
		scopeNote(&logstore.SearchFilters{UserIDs: []string{"user-7"}}, scope))
	require.Equal(t, "named",
		scopeNote(&logstore.SearchFilters{TeamIDs: []string{"team-1"}}, scope))
	// The caller's own id alongside a project is not caller-only: calling it
	// "self" would tell the model the answer is narrower than the query.
	require.Equal(t, "named",
		scopeNote(&logstore.SearchFilters{UserIDs: []string{"user-7"}, ProjectIDs: []string{"proj-1"}}, scope))
	require.Equal(t, "all",
		scopeNote(&logstore.SearchFilters{}, Scope{}))
}

// Every scoped flow must return the note, or the instruction to report scope
// has nothing to report.
func TestFlowsReportScope(t *testing.T) {
	deps := &Deps{LogManager: &fakeLogReader{}}
	ctx := scoped("user-7")

	for _, name := range []string{"query_logs", "query_model_performance"} {
		result, err := runToolCtx(t, ctx, name, deps, map[string]any{"filters": map[string]any{}})
		require.NoError(t, err, name)
		payload, ok := result.(map[string]any)
		require.True(t, ok, name)
		require.Equal(t, "self", payload["scope"], "%s must report what its result covers", name)
	}

	// A ranking of what the traffic was keeps the caller's default; a ranking
	// of people takes none (see TestRankingAcrossPeopleIsNotScopedToTheCaller).
	usage, err := runToolCtx(t, ctx, "query_usage_by", deps, map[string]any{"dimension": "app", "filters": map[string]any{}})
	require.NoError(t, err)
	require.Equal(t, "self", usage.(map[string]any)["scope"], "query_usage_by must report what its result covers")
	usage, err = runToolCtx(t, ctx, "query_usage_by", deps, map[string]any{"dimension": "user", "filters": map[string]any{}})
	require.NoError(t, err)
	require.Equal(t, "all", usage.(map[string]any)["scope"], "a ranking of people covers everyone the caller may see")

	result, err := runToolCtx(t, ctx, "query_metrics", deps, map[string]any{
		"filters": map[string]any{}, "metrics": []any{"summary"},
	})
	require.NoError(t, err)
	require.Equal(t, "self", result.(map[string]any)["scope"])
}

// userFilteringLogReader applies the one filter scoping touches - UserIDs - to
// a seeded set of rows, so a test can check who actually ends up in a result
// rather than only which filter was passed.
type userFilteringLogReader struct {
	fakeLogReader
	rows []logstore.Log
}

func (r *userFilteringLogReader) Search(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error) {
	r.searchFilters = filters
	matched := []logstore.Log{}
	for _, row := range r.rows {
		if len(filters.UserIDs) == 0 || (row.UserID != nil && slices.Contains(filters.UserIDs, *row.UserID)) {
			matched = append(matched, row)
		}
	}
	return &logstore.SearchResult{Logs: matched, Pagination: logstore.PaginationOptions{TotalCount: int64(len(matched))}}, nil
}

// An identified caller who asks about everyone - "across everyone", or the
// "whole deployment" option on ask_user - has to get everyone. Without an
// explicit marker the request is indistinguishable from one that named no
// scope, and the caller default quietly answered about them alone.
func TestScopeAllReachesEveryUser(t *testing.T) {
	user := func(id string) *string { return &id }
	reader := &userFilteringLogReader{rows: []logstore.Log{
		{ID: "a", UserID: user("user-7"), Timestamp: time.Now().UTC()},
		{ID: "b", UserID: user("user-9"), Timestamp: time.Now().UTC()},
		{ID: "c", UserID: user("user-12"), Timestamp: time.Now().UTC()},
	}}
	deps := &Deps{LogManager: reader}
	ctx := scoped("user-7")

	usersIn := func(result any) []string {
		out := []string{}
		for _, row := range result.(map[string]any)["rows"].([]LogRow) {
			out = append(out, row.UserID)
		}
		return out
	}

	t.Run("default narrows to the caller", func(t *testing.T) {
		result, err := runToolCtx(t, ctx, "query_logs", deps, map[string]any{"filters": map[string]any{}})
		require.NoError(t, err)
		require.Equal(t, []string{"user-7"}, reader.searchFilters.UserIDs)
		require.Equal(t, "self", result.(map[string]any)["scope"])
		require.ElementsMatch(t, []string{"user-7"}, usersIn(result))
	})

	t.Run("scope all reaches every user", func(t *testing.T) {
		result, err := runToolCtx(t, ctx, "query_logs", deps, map[string]any{"filters": map[string]any{"scope": "all"}})
		require.NoError(t, err)
		require.Empty(t, reader.searchFilters.UserIDs, "all must not carry the caller default")
		require.Equal(t, "all", result.(map[string]any)["scope"])
		require.ElementsMatch(t, []string{"user-7", "user-9", "user-12"}, usersIn(result))
	})

	t.Run("scope all still honours a named dimension", func(t *testing.T) {
		_, err := runToolCtx(t, ctx, "query_logs", deps, map[string]any{"filters": map[string]any{"scope": "all", "team_ids": []any{"team-1"}}})
		require.NoError(t, err)
		require.Equal(t, []string{"team-1"}, reader.searchFilters.TeamIDs)
		require.Empty(t, reader.searchFilters.UserIDs)
	})

	t.Run("an unknown scope is rejected, not read as the default", func(t *testing.T) {
		for _, bad := range []any{"caller", "everyone", "", 1, nil} {
			_, err := runToolCtx(t, ctx, "query_logs", deps, map[string]any{"filters": map[string]any{"scope": bad}})
			require.ErrorContains(t, err, `scope must be "all"`, "%v", bad)
		}
	})
}

// An admin asked "who are my top 5 users by cost" and got one row: themselves.
// The default scope had narrowed a ranking of users to the asker's own traffic,
// so it could only ever rank one person - while the dashboard beside it listed
// nineteen. A ranking across people, org units or keys is a question about more than
// the asker, so it is not defaulted to them; the store's queryscope still
// limits it to what they may see.
func TestRankingAcrossPeopleIsNotScopedToTheCaller(t *testing.T) {
	caller := scoped("u-admin")
	for _, dimension := range []string{"user", "team", "customer", "business_unit", "project", "virtual_key"} {
		t.Run(dimension, func(t *testing.T) {
			fake := &fakeLogReader{}
			out, err := runToolCtx(t, caller, "query_usage_by", &Deps{LogManager: fake}, map[string]any{
				"dimension": dimension, "filters": map[string]any{"start_time": "-7d"},
			})
			require.NoError(t, err)
			require.Empty(t, fake.rankingFilters.UserIDs, "a %s ranking narrowed to the asker ranks one entity", dimension)
			require.Equal(t, "all", out.(map[string]any)["scope"])

			fake = &fakeLogReader{}
			_, err = runToolCtx(t, caller, RenderChartToolName, &Deps{LogManager: fake}, map[string]any{
				"kind": "bar", "metric": "cost", "group": dimension, "title": "Spend", "filters": map[string]any{"start_time": "-7d"},
			})
			require.NoError(t, err)
			require.Empty(t, fake.rankingFilters.UserIDs, "a bar per %s narrowed to the asker draws one bar", dimension)
		})
	}

	// A scope the question named still wins.
	fake := &fakeLogReader{}
	_, err := runToolCtx(t, caller, "query_usage_by", &Deps{LogManager: fake}, map[string]any{
		"dimension": "user", "filters": map[string]any{"start_time": "-7d", "team_ids": []any{"team-platform"}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"team-platform"}, fake.rankingFilters.TeamIDs)
	require.Empty(t, fake.rankingFilters.UserIDs)

	// A ranking of what the traffic was, not whose it was, keeps the default:
	// "what errors am I seeing" is about the asker.
	fake = &fakeLogReader{}
	_, err = runToolCtx(t, caller, "query_usage_by", &Deps{LogManager: fake}, map[string]any{
		"dimension": "error_type", "filters": map[string]any{"start_time": "-7d"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"u-admin"}, fake.rankingFilters.UserIDs)
}

// rankingScope drops only the default user. What the caller may see describes
// them, not the default, so it has to survive into a ranking's result.
func TestRankingScopeKeepsWhatTheCallerMaySee(t *testing.T) {
	visible := &LogVisibility{TeamIDs: []string{"team-1"}}
	restricted := Scope{HasIdentity: true, UserID: "user-7", Visibility: "their teams' traffic", Visible: visible}
	require.Equal(t, Scope{Visibility: "their teams' traffic", Visible: visible}, rankingScope(restricted, "user"))
	require.Equal(t, Scope{Unrestricted: true}, rankingScope(Scope{HasIdentity: true, UserID: "admin-1", Unrestricted: true}, "team"))
	require.Equal(t, restricted, rankingScope(restricted, "error_type"))
	require.Equal(t, restricted, rankingScope(restricted, ""), "no dimension is not a ranking of people")
}

// A virtual_key ranking takes no default scope (rankingScope), so for an
// identified caller it ranks everyone's keys - and the model read its top row
// as "your traffic is associated with the virtual key X". The result now says
// whose keys it ranked and how to get the caller's own, with the id to copy.
// A ranking already narrowed to the caller, or asked by nobody in particular,
// has nothing to add.
func TestVirtualKeyRankingSaysWhoseKeysItRanks(t *testing.T) {
	identified := CallerView{DefaultUserID: "user-7"}
	cases := []struct {
		name     string
		ctx      context.Context
		filters  map[string]any
		guidance bool
	}{
		{"identified, unscoped", WithCallerView(context.Background(), identified), map[string]any{"start_time": "-24h"}, true},
		{"identified, explicit all", WithCallerView(context.Background(), identified), map[string]any{"start_time": "-24h", "scope": "all"}, true},
		{"unrestricted, unscoped", WithCallerView(context.Background(), CallerView{DefaultUserID: "user-7", Unrestricted: true}), map[string]any{"start_time": "-24h"}, true},
		{"identified, own traffic", WithCallerView(context.Background(), identified), map[string]any{"start_time": "-24h", "user_ids": []any{"user-7"}}, false},
		{"anonymous", context.Background(), map[string]any{"start_time": "-24h"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := mustRunToolCtx(t, tc.ctx, "query_usage_by", &Deps{LogManager: &fakeLogReader{}}, map[string]any{"dimension": "virtual_key", "filters": tc.filters})
			guidance, present := resultMap(t, out)["guidance"].(string)
			require.Equal(t, tc.guidance, present, "guidance: %q", guidance)
			if tc.guidance {
				require.Contains(t, guidance, "not the person asking")
				require.Contains(t, guidance, `user_ids: ["user-7"]`)
			}
		})
	}
	// Only a ranking of keys: a ranking of what the traffic was keeps the
	// caller's default and says nothing.
	out := mustRunToolCtx(t, scoped("user-7"), "query_usage_by", &Deps{LogManager: &fakeLogReader{}}, map[string]any{"dimension": "app", "filters": map[string]any{"start_time": "-24h"}})
	_, present := resultMap(t, out)["guidance"]
	require.False(t, present)
}

// A restricted caller defaults to themselves and is never the deployment, and
// describe_filter_space is where the model learns what "all" covers for them.
func TestRestrictedCallerScope(t *testing.T) {
	restricted := WithCallerView(context.Background(), CallerView{DefaultUserID: "user-7", Visibility: "their teams' traffic"})
	scope := scopeFromContext(restricted)
	require.False(t, scope.Unrestricted)

	filters := &logstore.SearchFilters{}
	applyScope(filters, scope, false)
	require.Equal(t, []string{"user-7"}, filters.UserIDs)
	require.Equal(t, "self", scopeNote(filters, scope))
	require.Equal(t, "all", scopeNote(&logstore.SearchFilters{}, scope))

	out := resultMap(t, mustRunToolCtx(t, restricted, "describe_filter_space", &Deps{LogManager: &fakeLogReader{}}, map[string]any{}))
	require.Equal(t, "the person asking", out["default_scope"])
	require.Equal(t, "their teams' traffic", out["caller_can_see"])

	unrestricted := WithCallerView(context.Background(), CallerView{DefaultUserID: "user-7", Unrestricted: true})
	out = resultMap(t, mustRunToolCtx(t, unrestricted, "describe_filter_space", &Deps{LogManager: &fakeLogReader{}}, map[string]any{}))
	require.NotContains(t, out, "caller_can_see")
}

// callTool runs one tool through toolHandler - the path every MCP result takes,
// where the caller's visibility note is applied - and returns the text it
// answered with and whether it was an error.
func callTool(t *testing.T, ctx context.Context, deps *Deps, name string, args map[string]any) (string, bool) {
	t.Helper()
	tool, ok := toolByName(buildTools(), name)
	require.True(t, ok, "tool %s should exist", name)
	result, err := toolHandler(StaticDeps(deps), *tool)(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok, "content is %T", result.Content[0])
	return text.Text, result.IsError
}

// A restricted caller's result has to say what it covers on the result itself.
// caller_can_see used to come back from describe_filter_space alone, so a turn
// that never called it had only the "all" tag to go on - and described a team's
// rows as "all users, teams and customers", the provenance template's own words.
func TestToolResultsSayWhatARestrictedCallerSees(t *testing.T) {
	restricted := WithCallerView(context.Background(), CallerView{DefaultUserID: "user-7", Visibility: "their teams' traffic"})
	deps := &Deps{LogManager: &fakeLogReader{getLogFunc: func(context.Context, string) (*logstore.Log, error) {
		return nil, nil
	}}}

	t.Run("a result over everything they may see names it", func(t *testing.T) {
		result, failed := callTool(t, restricted, deps, "query_logs", map[string]any{"filters": map[string]any{"scope": "all"}})
		require.False(t, failed, result)
		require.Contains(t, result, `"scope":"all"`)
		require.Contains(t, result, `"caller_can_see":"their teams' traffic"`)
	})

	// A team they cannot see comes back empty, exactly like a team with no
	// traffic. Without this the model called the id mistyped.
	t.Run("a result narrowed to a named team names it too", func(t *testing.T) {
		result, failed := callTool(t, restricted, deps, "query_logs", map[string]any{"filters": map[string]any{"team_ids": []any{"team-elsewhere"}}})
		require.False(t, failed, result)
		require.Contains(t, result, `"scope":"named"`)
		require.Contains(t, result, `"caller_can_see":"their teams' traffic"`)
	})

	t.Run("their own traffic needs no such note", func(t *testing.T) {
		result, failed := callTool(t, restricted, deps, "query_logs", map[string]any{"filters": map[string]any{}})
		require.False(t, failed, result)
		require.Contains(t, result, `"scope":"self"`)
		require.NotContains(t, result, "caller_can_see")
	})

	t.Run("an unrestricted caller's results are unchanged", func(t *testing.T) {
		unrestricted := WithCallerView(context.Background(), CallerView{DefaultUserID: "user-7", Unrestricted: true})
		result, failed := callTool(t, unrestricted, deps, "query_logs", map[string]any{"filters": map[string]any{"scope": "all"}})
		require.False(t, failed, result)
		require.Contains(t, result, `"scope":"deployment"`)
		require.NotContains(t, result, "caller_can_see")
	})

	t.Run("a result with no scope tag is left alone", func(t *testing.T) {
		require.Equal(t, map[string]any{"rows": 1}, noteCallerVisibility(map[string]any{"rows": 1}, Scope{Visibility: "their teams' traffic"}))
		require.Equal(t, "text", noteCallerVisibility("text", Scope{Visibility: "their teams' traffic"}))
	})

	// A row outside their view is indistinguishable from one that does not
	// exist, and must stay so - but "that id does not exist" is a claim the
	// lookup cannot support for someone who sees a slice.
	t.Run("a log they cannot load is not called nonexistent", func(t *testing.T) {
		for _, tool := range []string{"get_log_detail", "get_request_trace"} {
			result, failed := callTool(t, restricted, deps, tool, map[string]any{"log_id": "log-1"})
			require.True(t, failed, tool)
			require.Contains(t, result, "no log found with id log-1", tool)
			require.Contains(t, result, "their teams' traffic", tool)

			unrestricted := WithCallerView(context.Background(), CallerView{Unrestricted: true})
			result, _ = callTool(t, unrestricted, deps, tool, map[string]any{"log_id": "log-1"})
			require.NotContains(t, result, "may see", tool)
		}
	})
}

// An unrestricted caller is never narrowed, whatever the filter says or omits:
// the deployment is their default, and "all" for them is tagged as such.
func TestApplyScopeSkipsAnUnrestrictedCaller(t *testing.T) {
	unrestricted := Scope{HasIdentity: true, UserID: "admin-1", Unrestricted: true}
	for _, all := range []bool{false, true} {
		filters := &logstore.SearchFilters{}
		applyScope(filters, unrestricted, all)
		require.Empty(t, filters.UserIDs)
		require.Equal(t, "deployment", scopeNote(filters, unrestricted))
	}
}
