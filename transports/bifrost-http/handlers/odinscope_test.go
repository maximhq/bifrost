package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

func TestOdinScopeFromContext(t *testing.T) {
	t.Run("identified caller", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyUserID, "user-7")
		scope := odinScopeFromContext(ctx)
		require.True(t, scope.HasIdentity)
		require.Equal(t, "user-7", scope.UserID)
	})

	t.Run("no identity", func(t *testing.T) {
		scope := odinScopeFromContext(context.Background())
		require.False(t, scope.HasIdentity)
		require.Empty(t, scope.UserID)
	})
}

// With an identity and no scope in the question, the caller's own traffic is
// the default - the question people usually mean, and the one they can check.
func TestOdinScopeDefaultsToCaller(t *testing.T) {
	filters := &logstore.SearchFilters{}
	applyOdinScope(filters, odinScope{HasIdentity: true, UserID: "user-7"})
	require.Equal(t, []string{"user-7"}, filters.UserIDs)
}

// Without an identity there is no default. Silently widening to the whole
// deployment would answer a different question with a confident number.
func TestOdinScopeDoesNotDefaultWithoutIdentity(t *testing.T) {
	filters := &logstore.SearchFilters{}
	applyOdinScope(filters, odinScope{})
	require.Empty(t, filters.UserIDs)
}

// An explicit scope always wins. Narrowing "how did team X do?" to the asker's
// own traffic would answer a question nobody asked, and the answer would look
// right.
func TestOdinScopeNeverOverridesAnExplicitScope(t *testing.T) {
	scope := odinScope{HasIdentity: true, UserID: "user-7"}

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
		applyOdinScope(filters, scope)
		require.Equal(t, before.TeamIDs, filters.TeamIDs, name)
		require.Equal(t, before.CustomerIDs, filters.CustomerIDs, name)
		require.Equal(t, before.BusinessUnitIDs, filters.BusinessUnitIDs, name)
		require.Equal(t, before.VirtualKeyIDs, filters.VirtualKeyIDs, name)
		require.Equal(t, before.UserIDs, filters.UserIDs, name)
	}
}

// Scoping happens inside the shared filter parser, so a flow added later gets
// it by construction rather than by its author remembering to ask.
func TestOdinFilterArgAppliesScope(t *testing.T) {
	now := time.Now().UTC()
	filters, err := odinFilterArg(map[string]any{"filters": map[string]any{}}, now, odinScope{HasIdentity: true, UserID: "user-7"})
	require.NoError(t, err)
	require.Equal(t, []string{"user-7"}, filters.UserIDs)
}

func TestOdinFilterArgKeepsExplicitScope(t *testing.T) {
	now := time.Now().UTC()
	filters, err := odinFilterArg(
		map[string]any{"filters": map[string]any{"team_ids": []any{"team-1"}}},
		now, odinScope{HasIdentity: true, UserID: "user-7"},
	)
	require.NoError(t, err)
	require.Equal(t, []string{"team-1"}, filters.TeamIDs)
	require.Empty(t, filters.UserIDs)
}

// The model cannot report a scope it was never told about, so every result
// carries a note describing what it covers.
func TestOdinScopeNoteDescribesWhatTheResultCovers(t *testing.T) {
	scope := odinScope{HasIdentity: true, UserID: "user-7"}

	require.Contains(t,
		odinScopeNote(&logstore.SearchFilters{UserIDs: []string{"user-7"}}, scope),
		"person asking")
	require.Contains(t,
		odinScopeNote(&logstore.SearchFilters{TeamIDs: []string{"team-1"}}, scope),
		"named in the filters")
	require.Contains(t,
		odinScopeNote(&logstore.SearchFilters{}, odinScope{}),
		"whole deployment")
}

// Every scoped flow must return the note, or the instruction to report scope
// has nothing to report.
func TestOdinFlowsReportScope(t *testing.T) {
	deps := &odinToolDeps{logManager: &fakeOdinLogManager{}, scope: odinScope{HasIdentity: true, UserID: "user-7"}}

	for _, name := range []string{"query_logs", "query_user_usage", "query_model_performance"} {
		result, err := runOdinTool(t, name, deps, map[string]any{"filters": map[string]any{}})
		require.NoError(t, err, name)
		payload, ok := result.(map[string]any)
		require.True(t, ok, name)
		require.NotEmpty(t, payload["scope"], "%s must report what its result covers", name)
	}

	result, err := runOdinTool(t, "query_metrics", deps, map[string]any{
		"filters": map[string]any{}, "metrics": []any{"summary"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.(map[string]any)["scope"])
}

// The prompt has to actually carry the rules, or the mechanism is inert.
func TestOdinSystemPromptExplainsScoping(t *testing.T) {
	content := odinSystemInstructions(&schemas.OdinConfig{})
	require.Contains(t, content, "describe_scope")
	require.Contains(t, content, "their own traffic is the default")
	require.Contains(t, content, "Ask which team, customer or business unit is meant")
}
