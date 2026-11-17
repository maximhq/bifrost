package routing

import (
	"context"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A routing config override written before phrase suggestions moved to model
// routers keeps its other settings: its suggestions block, which the config no
// longer takes, is left out before validation instead of failing it, and the
// caller's override is not changed.
func TestResolveAnalyzerConfigIgnoresLegacySuggestions(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	// Enabled with no suggestions model and no llm block: invalid on its own.
	legacy := &configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 0.1, LookbackDays: 7}
	override := captureTestConfig(legacy)
	override.LLM = nil
	override.Keywords.SimpleKeywords = []string{"from the override"}

	got := resolveAnalyzerConfigFromStoreOrArg(context.Background(), logger, nil, override)
	require.NotNil(t, got, "the override is used, not dropped for its suggestions block")
	assert.Equal(t, []string{"from the override"}, got.Keywords.SimpleKeywords)
	assert.Nil(t, got.Suggestions)
	assert.Same(t, legacy, override.Suggestions, "the caller's override keeps its block")
}

// governanceResolving answers ResolveAccess with an access that carries no restriction, which is all
// resolveGovernanceScope needs: it reads the identifiers off the context the resolution stamped.
type governanceResolving struct{ Governance }

func (governanceResolving) ResolveAccess(*schemas.BifrostContext) (schemas.Access, error) {
	return struct{ schemas.Access }{}, nil
}

// The scope carries every team and customer the request is attributed to, beside the primary one the
// singular key names, so a rule scoped to any of them can match.
func TestResolveGovernanceScopeCarriesEveryTeamAndCustomer(t *testing.T) {
	p := &RoutingPlugin{governance: governanceResolving{}}

	t.Run("primary team and the full set", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamID, "team-b")
		ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamName, "Team B")
		ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamIDs, []string{"team-a", "team-b"})
		ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, "cust-b")
		ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerName, "Customer B")
		ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerIDs, []string{"cust-a", "cust-b"})

		scope, ok := p.resolveGovernanceScope(ctx)

		require.True(t, ok)
		assert.Equal(t, "team-b", scope.TeamID)
		assert.Equal(t, "Team B", scope.TeamName)
		assert.Equal(t, []string{"team-a", "team-b"}, scope.TeamIDs)
		assert.Equal(t, "cust-b", scope.CustomerID)
		assert.Equal(t, "Customer B", scope.CustomerName)
		assert.Equal(t, []string{"cust-a", "cust-b"}, scope.CustomerIDs)
	})

	t.Run("no sets leave the scope as it was", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamID, "team-a")
		ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, "cust-a")

		scope, ok := p.resolveGovernanceScope(ctx)

		require.True(t, ok)
		assert.Equal(t, "team-a", scope.TeamID)
		assert.Empty(t, scope.TeamIDs)
		assert.Equal(t, "cust-a", scope.CustomerID)
		assert.Empty(t, scope.CustomerIDs)
	})
}
