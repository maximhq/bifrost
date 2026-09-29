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
	clientSlugs             map[string]string // endpoint slug → clientID
	configuredProviders     map[schemas.ModelProvider]configstore.ProviderConfig
}

func (m *mockInMemoryStore) GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig {
	return m.configuredProviders
}

func (m *mockInMemoryStore) GetConfiguredProviderNames() []string {
	names := make([]string, 0, len(m.configuredProviders))
	for provider := range m.configuredProviders {
		names = append(names, string(provider))
	}
	return names
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

func (m *mockInMemoryStore) GetMCPClientBySlug(slug string) (string, string, bool) {
	id, ok := m.clientSlugs[slug]
	if !ok {
		return "", "", false
	}
	return id, m.GetMCPClientNames()[id], true
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

// A permit that grants every provider names none, so the providers it grants by the flag alone are
// materialised onto it at construction. Every consumer then reads one list and cannot disagree with
// the permit about what it grants — which is what let a listing refuse a provider the request path
// admitted.
func TestAppendAllProviderPermits(t *testing.T) {
	configured := []string{"openai", "anthropic", "bedrock", ""}

	t.Run("materialises the providers no config names", func(t *testing.T) {
		permits := AppendAllProviderPermits([]schemas.ProviderPermit{
			{Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o"}, KeyIDs: schemas.WhiteList{"key-1"}},
		}, configured)

		byProvider := map[string]schemas.ProviderPermit{}
		for _, permit := range permits {
			byProvider[permit.Provider] = permit
		}
		if len(byProvider) != 3 {
			t.Fatalf("expected openai, anthropic and bedrock, got %v", permits)
		}
		// The empty name the deployment reported is not a provider and must not become a permit.
		if _, ok := byProvider[""]; ok {
			t.Error("an empty provider name must not be materialised")
		}
		// A provider the permit already named keeps its own rules: the flag widens the set, it does
		// not relax the overrides.
		if got := byProvider["openai"].AllowedModels; len(got) != 1 || got[0] != "gpt-4o" {
			t.Errorf("openai override was replaced: %v", got)
		}
		// One it did not name is granted with nothing narrowed, and no weight, since a weight is a
		// routing preference a provider config expresses and this one expresses none.
		anthropic := byProvider["anthropic"]
		if !anthropic.AllowedModels.IsUnrestricted() {
			t.Errorf("expected all models allowed, got %v", anthropic.AllowedModels)
		}
		if !schemas.WhiteList(anthropic.KeyIDs).IsUnrestricted() {
			t.Errorf("expected all keys allowed, got %v", anthropic.KeyIDs)
		}
		if len(anthropic.BlacklistedModels) != 0 {
			t.Errorf("expected nothing blocked, got %v", anthropic.BlacklistedModels)
		}
		if anthropic.Weight != nil {
			t.Errorf("expected no weight, got %v", *anthropic.Weight)
		}
	})

	t.Run("a deployment with no providers adds nothing", func(t *testing.T) {
		permits := AppendAllProviderPermits(nil, nil)
		if len(permits) != 0 {
			t.Fatalf("expected no permits, got %v", permits)
		}
	})
}

// The end the fix is for: a key that grants every provider must answer for one it holds no config
// for, in the same list every consumer reads. Asked through the access, because that is what the
// listing routes and the routing allowlist ask.
func TestPermitForVirtualKeyGrantsEveryConfiguredProvider(t *testing.T) {
	store := &LocalGovernanceStore{inMemoryStore: &mockInMemoryStore{
		configuredProviders: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {}, schemas.Anthropic: {}, schemas.Bedrock: {},
		},
	}}
	// The customer's shape: allow-all, plus one provider config carrying an override.
	vk := &configstoreTables.TableVirtualKey{
		ID: "vk-1", Name: "allow-all", AllowAllProviders: true,
		ProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{{
			Provider: string(schemas.Bedrock), AllowedModels: schemas.WhiteList{"anthropic.claude-haiku-4-5"}, AllowAllKeys: true,
		}},
	}
	access := grant.NewAccess([]schemas.Permit{store.permitForVirtualKey(emptyCtx(), vk)}, nil, "", nil)

	assert.ElementsMatch(t, []string{"openai", "anthropic", "bedrock"}, access.GrantedProvidersForModel(""),
		"a provider the key names no config for is still granted")
	// The override survives: the flag widens the provider set, it does not relax what a config says.
	assert.True(t, access.IsModelAllowed("bedrock", "anthropic.claude-haiku-4-5"))
	assert.False(t, access.IsModelAllowed("bedrock", "amazon.titan-embed-text-v2:0"))
	// And a provider granted by the flag alone narrows nothing.
	assert.True(t, access.IsModelAllowed("anthropic", "claude-haiku-4-5"))

	t.Run("without the flag only the named providers are granted", func(t *testing.T) {
		vk.AllowAllProviders = false
		access := grant.NewAccess([]schemas.Permit{store.permitForVirtualKey(emptyCtx(), vk)}, nil, "", nil)

		assert.Equal(t, []string{"bedrock"}, access.GrantedProvidersForModel(""))
		assert.False(t, access.IsModelAllowed("anthropic", "claude-haiku-4-5"))
	})
}
