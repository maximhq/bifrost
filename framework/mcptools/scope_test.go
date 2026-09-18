package mcptools

import (
	"context"
	"slices"
	"testing"
	"time"

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
		scope := scopeFromContext(WithDefaultUserScope(context.Background(), "user-7"))
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

	t.Run("an empty opt-in is no opt-in", func(t *testing.T) {
		ctx := WithDefaultUserScope(context.Background(), "")
		require.Empty(t, DefaultUserScope(ctx))
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
		filters, scope, err := filterArg(WithDefaultUserScope(context.Background(), "user-7"), args, now)
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

	usage, err := runToolCtx(t, ctx, "query_usage_by", deps, map[string]any{"dimension": "user", "filters": map[string]any{}})
	require.NoError(t, err)
	require.Equal(t, "self", usage.(map[string]any)["scope"], "query_usage_by must report what its result covers")

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
