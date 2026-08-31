package governance

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
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

// accessFor builds the access a key carries on its own, with the given clients open to every
// key. The rules these tests pin (an explicit config owning its client, an open client granting
// everything, a wildcard pattern) live in the permit and the fold, so they are asked of the
// request's access rather than of the key directly.
func accessFor(vk *configstoreTables.TableVirtualKey, openClients map[string]string) schemas.Access {
	return grant.NewAccess([]schemas.Permit{vkPermit(vk, openClients)}, nil, "", nil)
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
// per-tool checks: AllowOnAllVirtualKeys scenarios
// ============================================================================

// VK with no MCPConfigs + AllowOnAllVirtualKeys client → tools allowed
func TestToolChecks_NoVKConfig_AllowAllEnabled(t *testing.T) {
	vk := buildVKNoMCPConfigs()

	assert.True(t, accessFor(vk, map[string]string{"client-1": "youtube"}).IsMCPToolAllowed("youtube-search"),
		"specific tool should be allowed when AllowOnAllVirtualKeys is set and VK has no explicit config")

	assert.True(t, accessFor(vk, map[string]string{"client-1": "youtube"}).IsMCPToolAllowed("youtube-*"),
		"wildcard pattern should be allowed when AllowOnAllVirtualKeys is set and VK has no explicit config")
}

// VK with explicit empty tools config for an AllowOnAllVirtualKeys client → tools blocked
func TestToolChecks_ExplicitEmptyConfig_Blocks(t *testing.T) {
	vk := buildVKWithMCPConfigs("client-1", "youtube", []string{"search"})

	assert.True(t, accessFor(vk, map[string]string{"client-1": "youtube"}).IsMCPToolAllowed("youtube-search"),
		"explicitly listed tool should be allowed")

	assert.False(t, accessFor(vk, map[string]string{"client-1": "youtube"}).IsMCPToolAllowed("youtube-upload"),
		"non-listed tool should be blocked even when AllowOnAllVirtualKeys is set")
}

// No open clients at all → nothing is granted, so every tool is blocked
func TestToolChecks_NoOpenClients_AllBlocked(t *testing.T) {
	vk := buildVKNoMCPConfigs()

	allowed := accessFor(vk, nil).IsMCPToolAllowed("youtube-search")
	assert.False(t, allowed,
		"nil inMemoryStore means no AllowOnAllVirtualKeys clients; tool should be blocked")
}

// Wildcard pattern (clientName-*) with AllowOnAllVirtualKeys client and no VK config → allowed
func TestToolChecks_WildcardPattern_AllowAll_NoVKConfig(t *testing.T) {
	vk := buildVKNoMCPConfigs()

	assert.True(t, accessFor(vk, map[string]string{"client-1": "youtube"}).IsMCPToolAllowed("youtube-*"),
		"clientName-* wildcard should match AllowOnAllVirtualKeys fallback")
}

// Explicit unrestricted config (["*"]) for AllowOnAllVirtualKeys client → all tools allowed
func TestToolChecks_ExplicitUnrestrictedConfig_AllowsAll(t *testing.T) {
	vk := buildVKWithMCPConfigs("client-1", "youtube", []string{"*"})

	assert.True(t, accessFor(vk, map[string]string{"client-1": "youtube"}).IsMCPToolAllowed("youtube-search"),
		"unrestricted explicit config should allow all tools")

	assert.True(t, accessFor(vk, map[string]string{"client-1": "youtube"}).IsMCPToolAllowed("youtube-*"),
		"wildcard should match when explicit config is unrestricted")
}

// Tool belonging to a different client is not allowed via AllowOnAllVirtualKeys of another client
func TestToolChecks_DifferentClient_Blocked(t *testing.T) {
	vk := buildVKNoMCPConfigs()

	assert.False(t, accessFor(vk, map[string]string{"client-1": "youtube"}).IsMCPToolAllowed("github-list_repos"),
		"tool from a different client should not be allowed via another client's AllowOnAllVirtualKeys")
}

// the store's view of open clients reaches the grant it builds
func TestIsMCPToolAllowedByVK_UsesInMemoryStore(t *testing.T) {
	store := &mockInMemoryStore{
		allowAllClients: map[string]string{"client-1": "youtube"},
	}
	permit := (&LocalGovernanceStore{inMemoryStore: store}).permitForVirtualKey(emptyCtx(), buildVKNoMCPConfigs())
	access := grant.NewAccess([]schemas.Permit{permit}, nil, "", nil)

	assert.True(t, access.IsMCPToolAllowed("youtube-search"),
		"the store resolves AllowOnAllVirtualKeys clients when it builds the key's permit")
}

// A store with no view of open clients → nothing is granted, so the tool is blocked
func TestToolChecks_StoreWithoutOpenClients_Blocked(t *testing.T) {
	permit := (&LocalGovernanceStore{}).permitForVirtualKey(emptyCtx(), buildVKNoMCPConfigs())
	access := grant.NewAccess([]schemas.Permit{permit}, nil, "", nil)

	assert.False(t, access.IsMCPToolAllowed("youtube-search"),
		"no open clients means no permit for the client, so no tool is allowed")
}
