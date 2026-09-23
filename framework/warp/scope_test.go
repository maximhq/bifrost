package warp

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
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
