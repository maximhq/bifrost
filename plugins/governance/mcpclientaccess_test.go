package governance

import (
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMCPClientToolAccess covers /mcp/<slug> resolution for a single MCP client: the slug must name a
// known client the request's access grants (else refused), and the served tools are the client's,
// narrowed to what access permits.
func TestMCPClientToolAccess(t *testing.T) {
	store := fixtureStore(t, &liveFixture{
		clientNames: map[string]string{"cA": "alpha"},
		clientSlugs: map[string]string{"alpha-svc": "cA"},
	})

	t.Run("an unknown slug is refused", func(t *testing.T) {
		served, ok := store.MCPClientToolAccess(gatewayCtx(), "nope", nil)
		assert.False(t, ok)
		assert.Nil(t, served)
	})

	t.Run("nil access serves the whole client", func(t *testing.T) {
		served, ok := store.MCPClientToolAccess(gatewayCtx(), "alpha-svc", nil)
		assert.True(t, ok)
		assert.Equal(t, []string{"alpha-" + grant.Wildcard}, served)
	})

	t.Run("a client open to the key is served, narrowed to its access", func(t *testing.T) {
		access := accessFor(t, buildVKNoMCPConfigs(), map[string]string{"cA": "alpha"})
		served, ok := store.MCPClientToolAccess(gatewayCtx(), "alpha-svc", access)
		assert.True(t, ok)
		assert.Equal(t, []string{"alpha-" + grant.Wildcard}, served)
	})

	t.Run("a client the key cannot reach is refused", func(t *testing.T) {
		access := accessFor(t, buildVKNoMCPConfigs(), nil)
		served, ok := store.MCPClientToolAccess(gatewayCtx(), "alpha-svc", access)
		assert.False(t, ok)
		assert.Empty(t, served)
	})
}

// TestMCPClientToolAccessRefusesADisabledClient pins that a disabled client's slug resolves to
// nothing at /mcp/<slug>, while an enabled client's slug is served whole, and that disabling a
// client that was being served stops it once its row is written and refreshed.
func TestMCPClientToolAccessRefusesADisabledClient(t *testing.T) {
	h := newLiveHarness(t, &configstore.GovernanceConfig{}, &liveFixture{
		clientNames: map[string]string{"cA": "alpha", "cB": "beta"},
		clientSlugs: map[string]string{"alpha-svc": "cA", "beta-svc": "cB"},
	})
	store := h.mustGovernanceStore(NewMockLogger(), nil)

	served, ok := store.MCPClientToolAccess(gatewayCtx(), "beta-svc", nil)
	require.True(t, ok, "an enabled client's slug is served")
	assert.Equal(t, []string{"beta-" + grant.Wildcard}, served)

	require.NoError(t, h.db.Model(&configstoreTables.TableMCPClient{}).Where("client_id = ?", "cB").UpdateColumn("disabled", true).Error)
	h.refresh(livestore.Ref{Kind: livestore.KindMCPClient, ID: "cB"})

	served, ok = store.MCPClientToolAccess(gatewayCtx(), "beta-svc", nil)
	assert.False(t, ok, "a disabled client's slug resolves to nothing")
	assert.Nil(t, served)

	served, ok = store.MCPClientToolAccess(gatewayCtx(), "alpha-svc", nil)
	assert.True(t, ok, "the enabled client is still served")
	assert.Equal(t, []string{"alpha-" + grant.Wildcard}, served)
}
