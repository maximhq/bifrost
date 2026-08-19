package governance

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
)

// mockInMemoryStore is a test double for InMemoryStore.
type mockInMemoryStore struct {
	allowAllClients     map[string]string // clientID → clientName
	configuredProviders map[schemas.ModelProvider]configstore.ProviderConfig
}

func (m *mockInMemoryStore) GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig {
	return m.configuredProviders
}

func (m *mockInMemoryStore) GetMCPClientsAllowingAllVirtualKeys() map[string]string {
	return m.allowAllClients
}

// newPluginWithInMemoryStore builds a minimal GovernancePlugin wired with a mock InMemoryStore.
func newPluginWithInMemoryStore(store InMemoryStore) *GovernancePlugin {
	return &GovernancePlugin{inMemoryStore: store}
}

// buildVKWithMCPConfigs returns a VK that has explicit MCPConfigs for the given client.
func buildVKWithMCPConfigs(clientID, clientName string, tools []string) *configstoreTables.TableVirtualKey {
	return &configstoreTables.TableVirtualKey{
		ID:   "vk-1",
		Name: "test-vk",
		MCPConfigs: []configstoreTables.TableVirtualKeyMCPConfig{
			{
				MCPClient: configstoreTables.TableMCPClient{
					ClientID: clientID,
					Name:     clientName,
				},
				ToolsToExecute: tools,
			},
		},
	}
}

// buildVKNoMCPConfigs returns a VK with no MCPConfigs at all.
func buildVKNoMCPConfigs() *configstoreTables.TableVirtualKey {
	return &configstoreTables.TableVirtualKey{
		ID:   "vk-2",
		Name: "test-vk-empty",
	}
}

// ============================================================================
// isMCPToolAllowedByVKWith — AllowOnAllVirtualKeys scenarios
// ============================================================================

// VK with no MCPConfigs + AllowOnAllVirtualKeys client → tools allowed
func TestIsMCPToolAllowedByVKWith_NoVKConfig_AllowAllEnabled(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{
		allowAllClients: map[string]string{"client-1": "youtube"},
	})
	vk := buildVKNoMCPConfigs()

	assert.True(t, p.isMCPToolAllowedByVKWith(vk, "youtube-search", map[string]string{"client-1": "youtube"}),
		"specific tool should be allowed when AllowOnAllVirtualKeys is set and VK has no explicit config")

	assert.True(t, p.isMCPToolAllowedByVKWith(vk, "youtube-*", map[string]string{"client-1": "youtube"}),
		"wildcard pattern should be allowed when AllowOnAllVirtualKeys is set and VK has no explicit config")
}

// VK with explicit empty tools config for an AllowOnAllVirtualKeys client → tools blocked
func TestIsMCPToolAllowedByVKWith_ExplicitEmptyConfig_Blocks(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{
		allowAllClients: map[string]string{"client-1": "youtube"},
	})
	// Explicit VK config with empty tools list (deny-all for this client)
	vk := buildVKWithMCPConfigs("client-1", "youtube", []string{})

	assert.False(t, p.isMCPToolAllowedByVKWith(vk, "youtube-search", map[string]string{"client-1": "youtube"}),
		"explicit empty tools list should block access even when AllowOnAllVirtualKeys is set")

	assert.False(t, p.isMCPToolAllowedByVKWith(vk, "youtube-*", map[string]string{"client-1": "youtube"}),
		"wildcard should be blocked when explicit config has empty tools list")
}

// VK with explicit ["tool1"] config for an AllowOnAllVirtualKeys client → only tool1 allowed
func TestIsMCPToolAllowedByVKWith_ExplicitPartialConfig_OnlyListedToolsAllowed(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{
		allowAllClients: map[string]string{"client-1": "youtube"},
	})
	vk := buildVKWithMCPConfigs("client-1", "youtube", []string{"search"})

	assert.True(t, p.isMCPToolAllowedByVKWith(vk, "youtube-search", map[string]string{"client-1": "youtube"}),
		"explicitly listed tool should be allowed")

	assert.False(t, p.isMCPToolAllowedByVKWith(vk, "youtube-upload", map[string]string{"client-1": "youtube"}),
		"non-listed tool should be blocked even when AllowOnAllVirtualKeys is set")
}

// inMemoryStore is nil → AllowOnAllVirtualKeys clients are treated as not configured (all blocked)
func TestIsMCPToolAllowedByVKWith_NilInMemoryStore_AllBlocked(t *testing.T) {
	p := &GovernancePlugin{inMemoryStore: nil}
	vk := buildVKNoMCPConfigs()

	allowed := p.isMCPToolAllowedByVKWith(vk, "youtube-search", nil)
	assert.False(t, allowed,
		"nil inMemoryStore means no AllowOnAllVirtualKeys clients; tool should be blocked")
}

// Wildcard pattern (clientName-*) with AllowOnAllVirtualKeys client and no VK config → allowed
func TestIsMCPToolAllowedByVKWith_WildcardPattern_AllowAll_NoVKConfig(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{
		allowAllClients: map[string]string{"client-1": "youtube"},
	})
	vk := buildVKNoMCPConfigs()

	assert.True(t, p.isMCPToolAllowedByVKWith(vk, "youtube-*", map[string]string{"client-1": "youtube"}),
		"clientName-* wildcard should match AllowOnAllVirtualKeys fallback")
}

// Explicit unrestricted config (["*"]) for AllowOnAllVirtualKeys client → all tools allowed
func TestIsMCPToolAllowedByVKWith_ExplicitUnrestrictedConfig_AllowsAll(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{
		allowAllClients: map[string]string{"client-1": "youtube"},
	})
	vk := buildVKWithMCPConfigs("client-1", "youtube", []string{"*"})

	assert.True(t, p.isMCPToolAllowedByVKWith(vk, "youtube-search", map[string]string{"client-1": "youtube"}),
		"unrestricted explicit config should allow all tools")

	assert.True(t, p.isMCPToolAllowedByVKWith(vk, "youtube-*", map[string]string{"client-1": "youtube"}),
		"wildcard should match when explicit config is unrestricted")
}

// Tool belonging to a different client is not allowed via AllowOnAllVirtualKeys of another client
func TestIsMCPToolAllowedByVKWith_DifferentClient_Blocked(t *testing.T) {
	p := newPluginWithInMemoryStore(&mockInMemoryStore{
		allowAllClients: map[string]string{"client-1": "youtube"},
	})
	vk := buildVKNoMCPConfigs()

	assert.False(t, p.isMCPToolAllowedByVKWith(vk, "github-list_repos", map[string]string{"client-1": "youtube"}),
		"tool from a different client should not be allowed via another client's AllowOnAllVirtualKeys")
}

// isMCPToolAllowedByVK delegates to inMemoryStore correctly
func TestIsMCPToolAllowedByVK_UsesInMemoryStore(t *testing.T) {
	store := &mockInMemoryStore{
		allowAllClients: map[string]string{"client-1": "youtube"},
	}
	p := newPluginWithInMemoryStore(store)
	vk := buildVKNoMCPConfigs()

	assert.True(t, p.isMCPToolAllowedByVK(vk, "youtube-search"),
		"isMCPToolAllowedByVK should use inMemoryStore to resolve AllowOnAllVirtualKeys")
}

// isMCPToolAllowedByVK with nil inMemoryStore → blocked
func TestIsMCPToolAllowedByVK_NilStore_Blocked(t *testing.T) {
	p := &GovernancePlugin{inMemoryStore: nil}
	vk := buildVKNoMCPConfigs()

	assert.False(t, p.isMCPToolAllowedByVK(vk, "youtube-search"),
		"nil inMemoryStore should result in blocked access")
}
