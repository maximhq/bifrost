package warp

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// What the scope does to a query - the default narrowing, the "scope" tag,
// describe_filter_space's default_scope - lives on Bifrost's MCP server and is
// pinned in mcptools. These tests cover the half that stays in Warp: deriving
// the caller's scope from the request context, folding in the deployment's
// restriction resolver, and what the prompt says about it.

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

// An admin's own traffic is nearly always dashboard testing, and "how much
// have we spent" from the deployment's owner means the deployment. A caller
// with no row-level query scope sees the whole deployment, so that is their
// default rather than themselves. The store still applies whatever scope it
// has, so this widens the question and never the permission.
func TestWarpScopeUnrestrictedCallerDefaultsToTheDeployment(t *testing.T) {
	identified := context.WithValue(context.Background(), schemas.BifrostContextKeyUserID, "admin-1")
	scope := ScopeFromContext(identified)
	require.True(t, scope.HasIdentity)
	require.Equal(t, "admin-1", scope.UserID)
	require.True(t, scope.Unrestricted, "no row-level scope on the context means the caller sees everything")
	require.Equal(t, identityDeployment, identityFor(scope), "the prompt states the deployment default")

	restricted := queryscope.WithQueryScope(identified, func(db *gorm.DB) *gorm.DB { return db })
	require.False(t, ScopeFromContext(restricted).Unrestricted, "a row-level scope means the caller sees a slice")
	require.Equal(t, identityKnown, identityFor(ScopeFromContext(restricted)))

	// The local admin bypasses RBAC by definition, whatever else the context carries.
	admin := context.WithValue(restricted, schemas.IsLocalAdminContextKey, true)
	require.True(t, ScopeFromContext(admin).Unrestricted)
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

// A store that scopes per read leaves no queryscope on the context, so the
// context alone cannot tell a team-scoped user from an admin. The resolver is
// what does: without it that user's team total was tagged "deployment".
func TestWarpScopeCallerRestrictionResolver(t *testing.T) {
	identified := context.WithValue(context.Background(), schemas.BifrostContextKeyUserID, "user-7")
	visible := &LogVisibility{TeamIDs: []string{"team-1"}}
	restricted := func(context.Context) CallerRestriction {
		return CallerRestriction{Restricted: true, Visibility: "their teams' traffic", Logs: visible}
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
		require.Same(t, visible, scope.Visible)
		require.Equal(t, identityKnown, identityFor(scope), "the prompt states their own traffic is the default")

		// What the MCP server is told for this caller: the default user, and
		// the slice they may see, so the server can narrow and say so.
		view := (&Agent{scope: scope}).callerView(time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC))
		require.Equal(t, "user-7", view.DefaultUserID)
		require.False(t, view.Unrestricted)
		require.Equal(t, "their teams' traffic", view.Visibility)
		require.Same(t, visible, view.Visible)
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
}

// The resolver has to be consulted with the context the turn runs under: that
// snapshot is the only identity the agent's goroutine holds.
func TestWarpRunTurnAsksTheCallerRestrictionResolver(t *testing.T) {
	model := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{TextTurn("hello")}}
	service := chatService(t, model, &fakeLogReader{})
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
	require.Contains(t, model.lastInstructions, "The person asking is identified",
		"a restricted caller is told their own traffic is the default, not the deployment")
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
