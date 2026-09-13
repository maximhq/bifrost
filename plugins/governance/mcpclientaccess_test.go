package governance

import (
	"testing"

	"github.com/maximhq/bifrost/framework/grant"
	"github.com/stretchr/testify/assert"
)

// TestMCPClientToolAccess covers /mcp/<slug> resolution for a single MCP client: the slug must name a
// known client the request's access grants (else refused), and the served tools are the client's,
// narrowed to what access permits.
func TestMCPClientToolAccess(t *testing.T) {
	store := &LocalGovernanceStore{
		inMemoryStore: &mockInMemoryStore{
			clientNames: map[string]string{"cA": "alpha"},
			clientSlugs: map[string]string{"alpha-svc": "cA"},
		},
		logger: NewMockLogger(),
	}

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
		access := accessFor(buildVKNoMCPConfigs(), map[string]string{"cA": "alpha"})
		served, ok := store.MCPClientToolAccess(gatewayCtx(), "alpha-svc", access)
		assert.True(t, ok)
		assert.Equal(t, []string{"alpha-" + grant.Wildcard}, served)
	})

	t.Run("a client the key cannot reach is refused", func(t *testing.T) {
		access := accessFor(buildVKNoMCPConfigs(), nil)
		served, ok := store.MCPClientToolAccess(gatewayCtx(), "alpha-svc", access)
		assert.False(t, ok)
		assert.Empty(t, served)
	})
}
