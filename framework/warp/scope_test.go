package warp

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestWarpScopeFromContext(t *testing.T) {
	t.Run("identified caller", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyUserID, "user-7")
		scope := ScopeFromContext(ctx)
		require.True(t, scope.HasIdentity)
		require.Equal(t, "user-7", scope.UserID)
	})

	t.Run("no identity", func(t *testing.T) {
		scope := ScopeFromContext(context.Background())
		require.False(t, scope.HasIdentity)
		require.Empty(t, scope.UserID)
	})
}

// With an identity and no scope in the question, the caller's own traffic is
// the default - the question people usually mean, and the one they can check.
func TestWarpScopeDefaultsToCaller(t *testing.T) {
	filters := &logstore.SearchFilters{}
	applyScope(filters, Scope{HasIdentity: true, UserID: "user-7"}, false)
	require.Equal(t, []string{"user-7"}, filters.UserIDs)
}

// Without an identity there is no default. Silently widening to the whole
// deployment would answer a different question with a confident number.
func TestWarpScopeDoesNotDefaultWithoutIdentity(t *testing.T) {
	filters := &logstore.SearchFilters{}
	applyScope(filters, Scope{}, false)
	require.Empty(t, filters.UserIDs)
}

// An explicit scope always wins. Narrowing "how did team X do?" to the asker's
// own traffic would answer a question nobody asked, and the answer would look
// right.
func TestWarpScopeNeverOverridesAnExplicitScope(t *testing.T) {
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
func TestWarpScopeUnrestrictedCallerDefaultsToTheDeployment(t *testing.T) {
	t.Run("derived from the context", func(t *testing.T) {
		identified := context.WithValue(context.Background(), schemas.BifrostContextKeyUserID, "admin-1")
		scope := ScopeFromContext(identified)
		require.True(t, scope.HasIdentity)
		require.Equal(t, "admin-1", scope.UserID)
		require.True(t, scope.Unrestricted, "no row-level scope on the context means the caller sees everything")

		restricted := queryscope.WithQueryScope(identified, func(db *gorm.DB) *gorm.DB { return db })
		require.False(t, ScopeFromContext(restricted).Unrestricted, "a row-level scope means the caller sees a slice")

		// The local admin bypasses RBAC by definition, whatever else the context carries.
		admin := context.WithValue(restricted, schemas.IsLocalAdminContextKey, true)
		require.True(t, ScopeFromContext(admin).Unrestricted)
	})

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
		deps := &ToolDeps{logManager: &fakeLogReader{}, scope: unrestricted}
		out := resultMap(t, mustRunTool(t, "describe_filter_space", deps, map[string]any{}))
		require.Equal(t, true, out["caller_is_identified"])
		require.Equal(t, "admin-1", out["caller_user_id"])
		require.Contains(t, out["default_scope"], "whole deployment")
		require.Contains(t, out["default_scope"], `user_ids: ["admin-1"]`)
	})
}

// Scoping happens inside the shared filter parser, so a flow added later gets
// it by construction rather than by its author remembering to ask.
func TestWarpFilterArgAppliesScope(t *testing.T) {
	now := time.Now().UTC()
	filters, err := filterArg(map[string]any{"filters": map[string]any{}}, now, Scope{HasIdentity: true, UserID: "user-7"})
	require.NoError(t, err)
	require.Equal(t, []string{"user-7"}, filters.UserIDs)
}

func TestWarpFilterArgKeepsExplicitScope(t *testing.T) {
	now := time.Now().UTC()
	filters, err := filterArg(
		map[string]any{"filters": map[string]any{"team_ids": []any{"team-1"}}},
		now, Scope{HasIdentity: true, UserID: "user-7"},
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
func TestWarpScopeNoteDescribesWhatTheResultCovers(t *testing.T) {
	scope := Scope{HasIdentity: true, UserID: "user-7"}

	require.Equal(t, "self",
		scopeNote(&logstore.SearchFilters{UserIDs: []string{"user-7"}}, scope))
	require.Equal(t, "named",
		scopeNote(&logstore.SearchFilters{TeamIDs: []string{"team-1"}}, scope))
	// The caller plus a project is a named scope, not "self".
	require.Equal(t, "named",
		scopeNote(&logstore.SearchFilters{UserIDs: []string{"user-7"}, ProjectIDs: []string{"proj-1"}}, scope))
	require.Equal(t, "all",
		scopeNote(&logstore.SearchFilters{}, Scope{}))
}

// Every scoped flow must return the note, or the instruction to report scope
// has nothing to report.
func TestWarpFlowsReportScope(t *testing.T) {
	deps := &ToolDeps{logManager: &fakeLogReader{}, scope: Scope{HasIdentity: true, UserID: "user-7"}}

	for _, name := range []string{"query_logs", "query_model_performance"} {
		result, err := runTool(t, name, deps, map[string]any{"filters": map[string]any{}})
		require.NoError(t, err, name)
		payload, ok := result.(map[string]any)
		require.True(t, ok, name)
		require.NotEmpty(t, payload["scope"], "%s must report what its result covers", name)
	}

	usage, err := runTool(t, "query_usage_by", deps, map[string]any{"dimension": "user", "filters": map[string]any{}})
	require.NoError(t, err)
	require.NotEmpty(t, usage.(map[string]any)["scope"], "query_usage_by must report what its result covers")

	result, err := runTool(t, "query_metrics", deps, map[string]any{
		"filters": map[string]any{}, "metrics": []any{"summary"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.(map[string]any)["scope"])
}

// The prompt has to actually carry the rules, or the mechanism is inert.
func TestWarpSystemPromptExplainsScoping(t *testing.T) {
	content := systemInstructions(&schemas.WarpConfig{}, true)
	require.Contains(t, content, "describe_filter_space")
	require.Contains(t, content, "their own traffic is the default")
	require.Contains(t, content, "you must ask before querying")
	// An unrestricted caller defaults to the deployment, and a deployment-wide
	// total is more useful with the split under it.
	require.Contains(t, content, `"deployment" means the whole deployment, which is this caller's default`)
	require.Contains(t, content, "add the breakdown: call query_usage_by with dimension team")
	require.Contains(t, content, `"I" and "my" questions need user_ids set to caller_user_id`)
	// ask_user takes at most 8 options, and a mixed list of every team,
	// customer and business unit overflows it - the prompt has to narrow to
	// one dimension first, not hand them all over as options.
	require.Contains(t, content, "ask_user accepts at most 8 options, counting a \"whole deployment\" option")
	require.Contains(t, content, "list only one dimension's values - teams, customers or business units, never a mix")
	require.Contains(t, content, "ask that first and only list that one dimension's values once they answer")
	require.NotContains(t, content, "Call ask_user with the teams, customers and business units")
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
func TestWarpScopeAllReachesEveryUser(t *testing.T) {
	user := func(id string) *string { return &id }
	reader := &userFilteringLogReader{rows: []logstore.Log{
		{ID: "a", UserID: user("user-7"), Timestamp: time.Now().UTC()},
		{ID: "b", UserID: user("user-9"), Timestamp: time.Now().UTC()},
		{ID: "c", UserID: user("user-12"), Timestamp: time.Now().UTC()},
	}}
	deps := &ToolDeps{logManager: reader, scope: Scope{HasIdentity: true, UserID: "user-7"}}

	usersIn := func(result any) []string {
		out := []string{}
		for _, row := range result.(map[string]any)["rows"].([]logRow) {
			out = append(out, row.UserID)
		}
		return out
	}

	t.Run("default narrows to the caller", func(t *testing.T) {
		result, err := runTool(t, "query_logs", deps, map[string]any{"filters": map[string]any{}})
		require.NoError(t, err)
		require.Equal(t, []string{"user-7"}, reader.searchFilters.UserIDs)
		require.Equal(t, "self", result.(map[string]any)["scope"])
		require.ElementsMatch(t, []string{"user-7"}, usersIn(result))
	})

	t.Run("scope all reaches every user", func(t *testing.T) {
		result, err := runTool(t, "query_logs", deps, map[string]any{"filters": map[string]any{"scope": "all"}})
		require.NoError(t, err)
		require.Empty(t, reader.searchFilters.UserIDs, "all must not carry the caller default")
		require.Equal(t, "all", result.(map[string]any)["scope"])
		require.ElementsMatch(t, []string{"user-7", "user-9", "user-12"}, usersIn(result))
	})

	t.Run("scope all still honours a named dimension", func(t *testing.T) {
		_, err := runTool(t, "query_logs", deps, map[string]any{"filters": map[string]any{"scope": "all", "team_ids": []any{"team-1"}}})
		require.NoError(t, err)
		require.Equal(t, []string{"team-1"}, reader.searchFilters.TeamIDs)
		require.Empty(t, reader.searchFilters.UserIDs)
	})

	t.Run("an unknown scope is rejected, not read as the default", func(t *testing.T) {
		for _, bad := range []any{"caller", "everyone", "", 1, nil} {
			_, err := runTool(t, "query_logs", deps, map[string]any{"filters": map[string]any{"scope": bad}})
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
func TestWarpRankingAcrossPeopleIsNotScopedToTheCaller(t *testing.T) {
	caller := Scope{HasIdentity: true, UserID: "u-admin"}
	for _, dimension := range []string{"user", "team", "customer", "business_unit", "project", "virtual_key"} {
		t.Run(dimension, func(t *testing.T) {
			fake := &fakeLogReader{}
			out, err := runTool(t, "query_usage_by", &ToolDeps{logManager: fake, scope: caller}, map[string]any{
				"dimension": dimension, "filters": map[string]any{"start_time": "-7d"},
			})
			require.NoError(t, err)
			require.Empty(t, fake.rankingFilters.UserIDs, "a %s ranking narrowed to the asker ranks one entity", dimension)
			require.Equal(t, "all", out.(map[string]any)["scope"])

			fake = &fakeLogReader{}
			_, err = runTool(t, "render_chart", &ToolDeps{logManager: fake, scope: caller}, map[string]any{
				"kind": "bar", "metric": "cost", "group": dimension, "title": "Spend", "filters": map[string]any{"start_time": "-7d"},
			})
			require.NoError(t, err)
			require.Empty(t, fake.rankingFilters.UserIDs, "a bar per %s narrowed to the asker draws one bar", dimension)
		})
	}

	// A scope the question named still wins.
	fake := &fakeLogReader{}
	_, err := runTool(t, "query_usage_by", &ToolDeps{logManager: fake, scope: caller}, map[string]any{
		"dimension": "user", "filters": map[string]any{"start_time": "-7d", "team_ids": []any{"team-platform"}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"team-platform"}, fake.rankingFilters.TeamIDs)
	require.Empty(t, fake.rankingFilters.UserIDs)

	// A ranking of what the traffic was, not whose it was, keeps the default:
	// "what errors am I seeing" is about the asker.
	fake = &fakeLogReader{}
	_, err = runTool(t, "query_usage_by", &ToolDeps{logManager: fake, scope: caller}, map[string]any{
		"dimension": "error_type", "filters": map[string]any{"start_time": "-7d"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"u-admin"}, fake.rankingFilters.UserIDs)
}

// A virtual_key ranking takes no default scope (rankingScope), so for an
// identified caller it ranks everyone's keys - and the model read its top row
// as "your traffic is associated with the virtual key X". The result now says
// whose keys it ranked and how to get the caller's own, with the id to copy.
// A ranking already narrowed to the caller, or asked by nobody in particular,
// has nothing to add.
func TestWarpVirtualKeyRankingSaysWhoseKeysItRanks(t *testing.T) {
	identified := Scope{HasIdentity: true, UserID: "user-7"}
	cases := []struct {
		name     string
		scope    Scope
		filters  map[string]any
		guidance bool
	}{
		{"identified, unscoped", identified, map[string]any{"start_time": "-24h"}, true},
		{"identified, explicit all", identified, map[string]any{"start_time": "-24h", "scope": "all"}, true},
		{"unrestricted, unscoped", Scope{HasIdentity: true, UserID: "user-7", Unrestricted: true}, map[string]any{"start_time": "-24h"}, true},
		{"identified, own traffic", identified, map[string]any{"start_time": "-24h", "user_ids": []any{"user-7"}}, false},
		{"anonymous", Scope{}, map[string]any{"start_time": "-24h"}, false},
	}
	tool, ok := toolByName(buildToolsFor(nil, false), "query_usage_by")
	require.True(t, ok)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Executed directly: runTool substitutes an identified caller for an
			// empty scope, and the anonymous case is the point of that row.
			deps := &ToolDeps{logManager: &fakeLogReader{}, scope: tc.scope}
			out, err := tool.execute(context.Background(), deps, map[string]any{"dimension": "virtual_key", "filters": tc.filters})
			require.NoError(t, err)
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
	deps := &ToolDeps{logManager: &fakeLogReader{}, scope: identified}
	out, err := runTool(t, "query_usage_by", deps, map[string]any{"dimension": "app", "filters": map[string]any{"start_time": "-24h"}})
	require.NoError(t, err)
	_, present := resultMap(t, out)["guidance"]
	require.False(t, present)
}

// A store that scopes per read leaves no queryscope on the context, so the
// context alone cannot tell a team-scoped user from an admin. The resolver is
// what does: without it that user's team total was tagged "deployment".
func TestWarpScopeCallerRestrictionResolver(t *testing.T) {
	identified := context.WithValue(context.Background(), schemas.BifrostContextKeyUserID, "user-7")
	restricted := func(context.Context) CallerRestriction {
		return CallerRestriction{Restricted: true, Visibility: "their teams' traffic"}
	}

	t.Run("no resolver leaves the context's answer", func(t *testing.T) {
		scope := withCallerRestriction(identified, ScopeFromContext(identified), nil)
		require.True(t, scope.Unrestricted)
		require.Empty(t, scope.Visibility)
	})

	t.Run("a restricted caller defaults to themselves and is never the deployment", func(t *testing.T) {
		scope := withCallerRestriction(identified, ScopeFromContext(identified), restricted)
		require.False(t, scope.Unrestricted)
		require.Equal(t, "their teams' traffic", scope.Visibility)

		filters := &logstore.SearchFilters{}
		applyScope(filters, scope, false)
		require.Equal(t, []string{"user-7"}, filters.UserIDs)
		require.Equal(t, "self", scopeNote(filters, scope))
		require.Equal(t, "all", scopeNote(&logstore.SearchFilters{}, scope))
	})

	t.Run("an unrestricted answer changes nothing", func(t *testing.T) {
		scope := withCallerRestriction(identified, ScopeFromContext(identified), func(context.Context) CallerRestriction {
			return CallerRestriction{Visibility: "ignored"}
		})
		require.True(t, scope.Unrestricted)
		require.Empty(t, scope.Visibility)
	})

	// The resolver only narrows: a scope already on the context is a fact, and
	// a resolver that knows nothing must not talk Warp out of it.
	t.Run("never widens a scope the context carries", func(t *testing.T) {
		scoped := queryscope.WithQueryScope(identified, func(db *gorm.DB) *gorm.DB { return db })
		scope := withCallerRestriction(scoped, ScopeFromContext(scoped), func(context.Context) CallerRestriction {
			return CallerRestriction{}
		})
		require.False(t, scope.Unrestricted)
	})

	t.Run("the local admin is not asked", func(t *testing.T) {
		admin := context.WithValue(identified, schemas.IsLocalAdminContextKey, true)
		scope := withCallerRestriction(admin, ScopeFromContext(admin), func(context.Context) CallerRestriction {
			t.Fatal("the local admin bypasses row-level scoping; the resolver must not run")
			return CallerRestriction{}
		})
		require.True(t, scope.Unrestricted)
	})

	// describe_filter_space is where the model learns what "all" covers.
	t.Run("describe_filter_space names what the caller may see", func(t *testing.T) {
		scope := withCallerRestriction(identified, ScopeFromContext(identified), restricted)
		deps := &ToolDeps{logManager: &fakeLogReader{}, scope: scope}
		out := resultMap(t, mustRunTool(t, "describe_filter_space", deps, map[string]any{}))
		require.Equal(t, "the person asking", out["default_scope"])
		require.Equal(t, "their teams' traffic", out["caller_can_see"])

		unrestricted := &ToolDeps{logManager: &fakeLogReader{}, scope: ScopeFromContext(identified)}
		out = resultMap(t, mustRunTool(t, "describe_filter_space", unrestricted, map[string]any{}))
		require.NotContains(t, out, "caller_can_see")
	})
}

// The resolver has to be consulted with the context the turn runs under: that
// snapshot is the only identity the agent's goroutine holds.
func TestWarpRunTurnAsksTheCallerRestrictionResolver(t *testing.T) {
	model := &scriptedModel{}
	service := chatService(model, &fakeLogReader{})
	var asked []string
	WithCallerRestrictionResolver(func(ctx context.Context) CallerRestriction {
		userID, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string)
		asked = append(asked, userID)
		return CallerRestriction{Restricted: true}
	})(service)

	turn, err := service.NewTurn(context.Background(), &ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}, 32)
	require.NoError(t, err)
	service.RunTurn(ownerCtx("user-7"), turn, nil)
	require.Equal(t, []string{"user-7"}, asked)
}

// A restricted caller's result has to say what it covers on the result itself.
// caller_can_see used to come back from describe_filter_space alone, so a turn
// that never called it had only the "all" tag to go on - and described a team's
// rows as "all users, teams and customers", the provenance template's own words.
func TestWarpToolResultsSayWhatARestrictedCallerSees(t *testing.T) {
	restricted := Scope{HasIdentity: true, UserID: "user-7", Visibility: "their teams' traffic"}
	run := func(t *testing.T, scope Scope, name, arguments string) (string, bool) {
		t.Helper()
		agent := newTestAgent(&scriptedModel{}, &fakeLogReader{getLogFunc: func(context.Context, string) (*logstore.Log, error) {
			return nil, nil
		}}, 8)
		agent.deps.scope = scope
		return agent.executeTool(context.Background(), name, arguments)
	}

	t.Run("a result over everything they may see names it", func(t *testing.T) {
		result, failed := run(t, restricted, "query_logs", `{"filters":{"scope":"all"}}`)
		require.False(t, failed, result)
		require.Contains(t, result, `"scope":"all"`)
		require.Contains(t, result, `"caller_can_see":"their teams' traffic"`)
	})

	// A team they cannot see comes back empty, exactly like a team with no
	// traffic. Without this the model called the id mistyped.
	t.Run("a result narrowed to a named team names it too", func(t *testing.T) {
		result, failed := run(t, restricted, "query_logs", `{"filters":{"team_ids":["team-elsewhere"]}}`)
		require.False(t, failed, result)
		require.Contains(t, result, `"scope":"named"`)
		require.Contains(t, result, `"caller_can_see":"their teams' traffic"`)
	})

	t.Run("their own traffic needs no such note", func(t *testing.T) {
		result, failed := run(t, restricted, "query_logs", `{"filters":{}}`)
		require.False(t, failed, result)
		require.Contains(t, result, `"scope":"self"`)
		require.NotContains(t, result, "caller_can_see")
	})

	t.Run("an unrestricted caller's results are unchanged", func(t *testing.T) {
		result, failed := run(t, Scope{HasIdentity: true, UserID: "user-7", Unrestricted: true}, "query_logs", `{"filters":{"scope":"all"}}`)
		require.False(t, failed, result)
		require.NotContains(t, result, "caller_can_see")
	})

	// A row outside their view is indistinguishable from one that does not
	// exist, and must stay so - but "that id does not exist" is a claim the
	// lookup cannot support for someone who sees a slice.
	t.Run("a log they cannot load is not called nonexistent", func(t *testing.T) {
		for _, tool := range []string{"get_log_detail", "get_request_trace"} {
			result, failed := run(t, restricted, tool, `{"log_id":"log-1"}`)
			require.True(t, failed, tool)
			require.Contains(t, result, "no log found with id log-1", tool)
			require.Contains(t, result, "their teams' traffic", tool)

			result, _ = run(t, Scope{Unrestricted: true}, tool, `{"log_id":"log-1"}`)
			require.NotContains(t, result, "may see", tool)
		}
	})
}

// The provenance block's example used to read "Scope: all users, teams and
// customers", and an example outweighs the rule beside it: a restricted
// caller's answers ended with that line verbatim.
func TestWarpSystemPromptScopeLineIsNotAnAnswer(t *testing.T) {
	content := systemInstructions(&schemas.WarpConfig{}, true)

	require.NotContains(t, content, "Scope: all users, teams and customers")
	require.Contains(t, content, "When a result carries caller_can_see")
	require.Contains(t, content, "never call such a result the whole deployment or all users")
}
