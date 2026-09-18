package mcptools

import (
	"context"
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
	applyScope(filters, Scope{HasIdentity: true, UserID: "user-7"})
	require.Equal(t, []string{"user-7"}, filters.UserIDs)
}

// Without an identity there is no default. Silently widening to the whole
// deployment would answer a different question with a confident number.
func TestScopeDoesNotDefaultWithoutIdentity(t *testing.T) {
	filters := &logstore.SearchFilters{}
	applyScope(filters, Scope{})
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
		applyScope(filters, scope)
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
