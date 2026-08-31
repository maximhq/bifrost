package governance

import (
	"maps"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/stretchr/testify/assert"
)

// mockInMemoryStore is a test double for InMemoryStore.
type mockInMemoryStore struct {
	allowedByDefaultClients map[string]string // clientID → clientName
	clientNames             map[string]string // clientID → clientName, configured clients that are not allowed by default
	configuredProviders     map[schemas.ModelProvider]configstore.ProviderConfig
}

func (m *mockInMemoryStore) GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig {
	return m.configuredProviders
}

func (m *mockInMemoryStore) GetMCPClientsAllowedByDefault() map[string]string {
	return m.allowedByDefaultClients
}

// GetMCPClientNames answers as the production store does: every configured client, of which the ones
// allowed by default are a subset.
func (m *mockInMemoryStore) GetMCPClientNames() map[string]string {
	names := make(map[string]string, len(m.clientNames)+len(m.allowedByDefaultClients))
	maps.Copy(names, m.clientNames)
	maps.Copy(names, m.allowedByDefaultClients)
	return names
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
// per-tool checks: AllowByDefault scenarios
// ============================================================================

// VK with no MCPConfigs + AllowByDefault client → tools allowed
func TestToolChecks_NoVKConfig_AllowAllEnabled(t *testing.T) {
	vk := buildVKNoMCPConfigs()

	assert.True(t, accessFor(vk, map[string]string{"client-1": "youtube"}).IsMCPToolAllowed("youtube-search"),
		"specific tool should be allowed when AllowByDefault is set and VK has no explicit config")

	assert.True(t, accessFor(vk, map[string]string{"client-1": "youtube"}).IsMCPToolAllowed("youtube-*"),
		"wildcard pattern should be allowed when AllowByDefault is set and VK has no explicit config")
}

// VK with explicit empty tools config for an AllowByDefault client → tools blocked
func TestToolChecks_ExplicitEmptyConfig_Blocks(t *testing.T) {
	vk := buildVKWithMCPConfigs("client-1", "youtube", []string{"search"})

	assert.True(t, accessFor(vk, map[string]string{"client-1": "youtube"}).IsMCPToolAllowed("youtube-search"),
		"explicitly listed tool should be allowed")

	assert.False(t, accessFor(vk, map[string]string{"client-1": "youtube"}).IsMCPToolAllowed("youtube-upload"),
		"non-listed tool should be blocked even when AllowByDefault is set")
}

// No open clients at all → nothing is granted, so every tool is blocked
func TestToolChecks_NoOpenClients_AllBlocked(t *testing.T) {
	vk := buildVKNoMCPConfigs()

	allowed := accessFor(vk, nil).IsMCPToolAllowed("youtube-search")
	assert.False(t, allowed,
		"nil inMemoryStore means no AllowByDefault clients; tool should be blocked")
}

// Wildcard pattern (clientName-*) with AllowByDefault client and no VK config → allowed
func TestToolChecks_WildcardPattern_AllowAll_NoVKConfig(t *testing.T) {
	vk := buildVKNoMCPConfigs()

	assert.True(t, accessFor(vk, map[string]string{"client-1": "youtube"}).IsMCPToolAllowed("youtube-*"),
		"clientName-* wildcard should match AllowByDefault fallback")
}

// Explicit unrestricted config (["*"]) for AllowByDefault client → all tools allowed
func TestToolChecks_ExplicitUnrestrictedConfig_AllowsAll(t *testing.T) {
	vk := buildVKWithMCPConfigs("client-1", "youtube", []string{"*"})

	assert.True(t, accessFor(vk, map[string]string{"client-1": "youtube"}).IsMCPToolAllowed("youtube-search"),
		"unrestricted explicit config should allow all tools")

	assert.True(t, accessFor(vk, map[string]string{"client-1": "youtube"}).IsMCPToolAllowed("youtube-*"),
		"wildcard should match when explicit config is unrestricted")
}

// Tool belonging to a different client is not allowed via AllowByDefault of another client
func TestToolChecks_DifferentClient_Blocked(t *testing.T) {
	vk := buildVKNoMCPConfigs()

	assert.False(t, accessFor(vk, map[string]string{"client-1": "youtube"}).IsMCPToolAllowed("github-list_repos"),
		"tool from a different client should not be allowed via another client's AllowByDefault")
}

// the store's view of open clients reaches the grant it builds
func TestIsMCPToolAllowedByVK_UsesInMemoryStore(t *testing.T) {
	store := &mockInMemoryStore{
		allowedByDefaultClients: map[string]string{"client-1": "youtube"},
	}
	permit := (&LocalGovernanceStore{inMemoryStore: store}).permitForVirtualKey(emptyCtx(), buildVKNoMCPConfigs())
	access := grant.NewAccess([]schemas.Permit{permit}, nil, "", nil)

	assert.True(t, access.IsMCPToolAllowed("youtube-search"),
		"the store resolves AllowByDefault clients when it builds the key's permit")
}

// A store with no view of open clients → nothing is granted, so the tool is blocked
func TestToolChecks_StoreWithoutOpenClients_Blocked(t *testing.T) {
	permit := (&LocalGovernanceStore{}).permitForVirtualKey(emptyCtx(), buildVKNoMCPConfigs())
	access := grant.NewAccess([]schemas.Permit{permit}, nil, "", nil)

	assert.False(t, access.IsMCPToolAllowed("youtube-search"),
		"no open clients means no permit for the client, so no tool is allowed")
}

// The mock has to keep the two questions apart the way the production store does: a client that is
// configured but not allowed by default is still resolvable by name.
func TestMockInMemoryStore_ClientNamesCoverEveryConfiguredClient(t *testing.T) {
	store := &mockInMemoryStore{
		allowedByDefaultClients: map[string]string{"open-id": "open"},
		clientNames:             map[string]string{"private-id": "private"},
	}

	names := store.GetMCPClientNames()
	if names["open-id"] != "open" || names["private-id"] != "private" || len(names) != 2 {
		t.Fatalf("GetMCPClientNames should name every configured client, got %v", names)
	}
	if allowAll := store.GetMCPClientsAllowedByDefault(); len(allowAll) != 1 || allowAll["open-id"] != "open" {
		t.Fatalf("GetMCPClientsAllowedByDefault should stay the allowed-by-default subset, got %v", allowAll)
	}
}

// TestAppendMCPPermitsAllowedByDefault pins the rule every holder appends by: a client the holder
// configured is never widened, the rest are granted every tool, in id order.
func TestAppendMCPPermitsAllowedByDefault(t *testing.T) {
	configured := map[string]struct{}{"client-b": {}}
	own := []schemas.MCPPermit{{Client: "client-b", ClientName: "b", Tools: []string{}}}
	got := AppendMCPPermitsAllowedByDefault(own, configured, map[string]string{"client-c": "c", "client-a": "a", "client-b": "b"})
	assert.Equal(t, []schemas.MCPPermit{
		{Client: "client-b", ClientName: "b", Tools: []string{}},
		{Client: "client-a", ClientName: "a", Tools: []string{grant.Wildcard}},
		{Client: "client-c", ClientName: "c", Tools: []string{grant.Wildcard}},
	}, got)

	assert.Equal(t, own, AppendMCPPermitsAllowedByDefault(own, configured, nil), "nothing allowed by default leaves the list as it was")
}
