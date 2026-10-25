//go:build !tinygo && !wasm

package mcp

import (
	"context"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePerUserDiscoveryStore keeps one discovery per (caller, client), with the
// caller read off ctx the way a per-caller store reads it, and records the
// bindings it was written and deleted under.
type fakePerUserDiscoveryStore struct {
	mu          sync.Mutex
	discoveries map[string]*schemas.MCPDiscovery
	written     []string
}

func newFakePerUserDiscoveryStore() *fakePerUserDiscoveryStore {
	return &fakePerUserDiscoveryStore{discoveries: map[string]*schemas.MCPDiscovery{}}
}

func perUserBinding(mode schemas.MCPAuthMode, identity, clientID string) string {
	return string(mode) + "/" + identity + "/" + clientID
}

// callerBinding is the binding of the user or virtual-key caller on ctx.
func callerBinding(ctx context.Context, clientID string) (string, bool) {
	bctx, ok := ctx.(*schemas.BifrostContext)
	if !ok {
		return "", false
	}
	mode := bctx.MCPAuthMode()
	if mode != schemas.MCPAuthModeUser && mode != schemas.MCPAuthModeVK {
		return "", false
	}
	return perUserBinding(mode, bctx.MCPIdentity(mode), clientID), true
}

func (s *fakePerUserDiscoveryStore) GetDiscovery(ctx context.Context, client *schemas.MCPClientConfig) *schemas.MCPDiscovery {
	binding, ok := callerBinding(ctx, client.ID)
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.discoveries[binding]
}

func (s *fakePerUserDiscoveryStore) SetDiscovery(ctx context.Context, client *schemas.MCPClientConfig, discovery *schemas.MCPDiscovery) error {
	binding, ok := callerBinding(ctx, client.ID)
	if !ok {
		return errNoPerUserDiscoveryStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discoveries[binding] = discovery
	s.written = append(s.written, "set "+binding)
	return nil
}

func (s *fakePerUserDiscoveryStore) DeleteDiscovery(ctx context.Context, client *schemas.MCPClientConfig) error {
	binding, ok := callerBinding(ctx, client.ID)
	if !ok {
		return errNoPerUserDiscoveryStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.discoveries, binding)
	s.written = append(s.written, "delete "+binding)
	return nil
}

// callerContext returns a request context carrying one identity value, the
// way the HTTP layer and governance stamp it.
func callerContext(key schemas.BifrostContextKey, value string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(key, value)
	return ctx
}

func userContext(userID string) *schemas.BifrostContext {
	return callerContext(schemas.BifrostContextKeyUserID, userID)
}

func vkContext(vkID string) *schemas.BifrostContext {
	return callerContext(schemas.BifrostContextKeyGovernanceVirtualKeyID, vkID)
}

func discoveryOf(clientName string, tools ...string) *schemas.MCPDiscovery {
	discovery := &schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{}, ToolNameMapping: map[string]string{}}
	for _, tool := range tools {
		prefixed := clientName + "-" + tool
		discovery.Tools[prefixed] = schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: prefixed}}
		discovery.ToolNameMapping[tool] = tool
	}
	return discovery
}

func sharedClient() *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{ID: "wiki-id", Name: "wiki", ToolsToExecute: []string{"*"}}
}

func perUserClient() *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{ID: "notes-id", Name: "notes", ToolsToExecute: []string{"*"}, PerUserDiscovery: schemas.Ptr(true)}
}

// newPerUserTestManager returns a manager whose built-in store hands clients
// with per_user_discovery on to perUser, holding a shared client "wiki" and a
// per-user client "notes", both open to every tool.
func newPerUserTestManager(t *testing.T, perUser schemas.MCPDiscoveryStore) (*MCPManager, *schemas.MCPClientConfig, *schemas.MCPClientConfig) {
	t.Helper()
	m := NewMCPManager(context.Background(), schemas.MCPConfig{PerUserDiscoveryStore: perUser}, nil, &MockLogger{}, nil)
	shared, perUserConfig := sharedClient(), perUserClient()
	m.mu.Lock()
	for _, config := range []*schemas.MCPClientConfig{shared, perUserConfig} {
		m.clientMap[config.ID] = &schemas.MCPClientState{Name: config.Name, ExecutionConfig: config, State: schemas.MCPConnectionStateHealthy}
	}
	m.mu.Unlock()
	require.NoError(t, m.discoveryStore.SetDiscovery(context.Background(), shared, discoveryOf("wiki", "read")))
	return m, shared, perUserConfig
}

func TestDiscoveryStore_RoutesByTheClientsFlag(t *testing.T) {
	perUser := newFakePerUserDiscoveryStore()
	store := NewDiscoveryStore(nil, perUser)
	shared := sharedClient()
	require.NoError(t, store.SetDiscovery(context.Background(), shared, discoveryOf("wiki", "read")))
	require.NoError(t, store.SetDiscovery(userContext("alice"), perUserClient(), discoveryOf("notes", "search")))
	require.NoError(t, store.SetDiscovery(vkContext("vk-1"), perUserClient(), discoveryOf("notes", "admin")))

	assert.Contains(t, store.GetDiscovery(userContext("alice"), perUserClient()).GetTools(), "notes-search")
	assert.Contains(t, store.GetDiscovery(vkContext("vk-1"), perUserClient()).GetTools(), "notes-admin")
	assert.Nil(t, store.GetDiscovery(userContext("bob"), perUserClient()), "a caller with no discovery has none")
	assert.Nil(t, store.GetDiscovery(callerContext(schemas.BifrostContextKeyMCPSessionID, "sess-1"), perUserClient()), "a session-only caller keeps no per-user discovery")
	assert.Nil(t, store.GetDiscovery(context.Background(), perUserClient()), "an anonymous caller keeps no per-user discovery")

	for _, ctx := range []context.Context{userContext("alice"), vkContext("vk-1"), context.Background()} {
		assert.Contains(t, store.GetDiscovery(ctx, shared).GetTools(), "wiki-read", "a shared client serves every caller the shared discovery")
	}
	assert.Equal(t, []string{"set user/alice/notes-id", "set vk/vk-1/notes-id"}, perUser.written, "a shared write never reaches the per-user store")
}

func TestDiscoveryStore_SharedEntryNeverServesAPerUserClient(t *testing.T) {
	perUser := newFakePerUserDiscoveryStore()
	store := NewDiscoveryStore(nil, perUser)
	// Recorded while the client was shared, before it turned per_user_discovery on.
	before := perUserClient()
	before.PerUserDiscovery = nil
	require.NoError(t, store.SetDiscovery(context.Background(), before, discoveryOf("notes", "legacy")))

	assert.Nil(t, store.GetDiscovery(userContext("alice"), perUserClient()), "the leftover shared entry must not be served")

	require.NoError(t, store.DeleteDiscovery(userContext("alice"), perUserClient()))
	assert.Nil(t, store.GetDiscovery(context.Background(), before), "deleting the per-user client also drops the leftover shared entry")
	assert.Equal(t, []string{"delete user/alice/notes-id"}, perUser.written)
}

// recordingSharedDiscoveryStore keeps one discovery per client and records the
// clients it was written and deleted for.
type recordingSharedDiscoveryStore struct {
	mu          sync.Mutex
	discoveries map[string]*schemas.MCPDiscovery
	written     []string
}

func (s *recordingSharedDiscoveryStore) GetDiscovery(_ context.Context, client *schemas.MCPClientConfig) *schemas.MCPDiscovery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.discoveries[client.ID]
}

func (s *recordingSharedDiscoveryStore) SetDiscovery(_ context.Context, client *schemas.MCPClientConfig, discovery *schemas.MCPDiscovery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discoveries[client.ID] = discovery
	s.written = append(s.written, "set "+client.ID)
	return nil
}

func (s *recordingSharedDiscoveryStore) DeleteDiscovery(_ context.Context, client *schemas.MCPClientConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.discoveries, client.ID)
	s.written = append(s.written, "delete "+client.ID)
	return nil
}

func TestDiscoveryStore_HandsSharedClientsToTheSharedStore(t *testing.T) {
	shared := &recordingSharedDiscoveryStore{discoveries: map[string]*schemas.MCPDiscovery{}}
	perUser := newFakePerUserDiscoveryStore()
	m := NewMCPManager(context.Background(), schemas.MCPConfig{SharedDiscoveryStore: shared, PerUserDiscoveryStore: perUser}, nil, &MockLogger{}, nil)

	require.NoError(t, m.discoveryStore.SetDiscovery(context.Background(), sharedClient(), discoveryOf("wiki", "read")))
	require.NoError(t, m.discoveryStore.SetDiscovery(userContext("alice"), perUserClient(), discoveryOf("notes", "search")))
	assert.Contains(t, m.discoveryStore.GetDiscovery(userContext("alice"), sharedClient()).GetTools(), "wiki-read", "a shared client is served from the shared store")
	assert.Contains(t, m.discoveryStore.GetDiscovery(userContext("alice"), perUserClient()).GetTools(), "notes-search")

	require.NoError(t, m.discoveryStore.DeleteDiscovery(userContext("alice"), perUserClient()))
	assert.Equal(t, []string{"set wiki-id", "delete notes-id"}, shared.written, "only shared writes reach the shared store, and a per-user client's delete also drops any shared entry it left")
	assert.Equal(t, []string{"set user/alice/notes-id", "delete user/alice/notes-id"}, perUser.written)
}

// TestDiscoveryStore_TakesTheStoresAsAPair: a shared store is used only together
// with a per-user store. Given one without the other, shared clients are kept in
// memory and the store passed in is never written.
func TestDiscoveryStore_TakesTheStoresAsAPair(t *testing.T) {
	shared := &recordingSharedDiscoveryStore{discoveries: map[string]*schemas.MCPDiscovery{}}
	m := NewMCPManager(context.Background(), schemas.MCPConfig{SharedDiscoveryStore: shared}, nil, &MockLogger{}, nil)

	require.NoError(t, m.discoveryStore.SetDiscovery(context.Background(), sharedClient(), discoveryOf("wiki", "read")))
	assert.Contains(t, m.discoveryStore.GetDiscovery(context.Background(), sharedClient()).GetTools(), "wiki-read", "kept in memory")
	assert.Empty(t, shared.written, "a shared store without a per-user store is not used")
}

func TestDiscoveryStore_WithoutAPerUserStoreAPerUserClientHasNoTools(t *testing.T) {
	store := NewDiscoveryStore(nil, nil)

	assert.Nil(t, store.GetDiscovery(userContext("alice"), perUserClient()))
	assert.ErrorIs(t, store.SetDiscovery(userContext("alice"), perUserClient(), discoveryOf("notes", "search")), errNoPerUserDiscoveryStore)
	assert.NoError(t, store.DeleteDiscovery(userContext("alice"), perUserClient()), "nothing to delete is not an error")
}

func TestGetToolPerClient_ServesEachCallerItsOwnTools(t *testing.T) {
	perUser := newFakePerUserDiscoveryStore()
	m, _, notes := newPerUserTestManager(t, perUser)
	require.NoError(t, m.discoveryStore.SetDiscovery(userContext("alice"), notes, discoveryOf("notes", "search")))
	require.NoError(t, m.discoveryStore.SetDiscovery(userContext("bob"), notes, discoveryOf("notes", "admin")))

	names := func(ctx context.Context) map[string][]string {
		out := map[string][]string{}
		for client, tools := range m.GetToolPerClient(ctx) {
			for _, tool := range tools {
				out[client] = append(out[client], tool.Function.Name)
			}
		}
		return out
	}
	assert.Equal(t, map[string][]string{"notes": {"notes-search"}, "wiki": {"wiki-read"}}, names(userContext("alice")))
	assert.Equal(t, map[string][]string{"notes": {"notes-admin"}, "wiki": {"wiki-read"}}, names(userContext("bob")))
	assert.Equal(t, map[string][]string{"wiki": {"wiki-read"}}, names(context.Background()))
}

func TestGetClientForTool_ResolvesThroughTheCallersDiscovery(t *testing.T) {
	perUser := newFakePerUserDiscoveryStore()
	m, shared, notes := newPerUserTestManager(t, perUser)
	alices := discoveryOf("notes", "search", "create-page")
	require.NoError(t, m.discoveryStore.SetDiscovery(userContext("alice"), notes, alices))

	state, discovery := m.GetClientForTool(userContext("alice"), "notes-search")
	require.NotNil(t, state)
	assert.Equal(t, notes.ID, state.ExecutionConfig.ID)
	assert.Same(t, alices, discovery, "execution uses the name mapping of the discovery the tool was found in")

	state, _ = m.GetClientForTool(userContext("alice"), "notes-create-page")
	require.NotNil(t, state, "an upstream name with a hyphen still resolves: client names never contain one")
	assert.Equal(t, notes.ID, state.ExecutionConfig.ID)

	state, _ = m.GetClientForTool(userContext("bob"), "notes-search")
	assert.Nil(t, state, "a tool outside the caller's own discovery is not available to it")

	state, discovery = m.GetClientForTool(userContext("alice"), "wiki-read")
	require.NotNil(t, state)
	assert.Equal(t, shared.ID, state.ExecutionConfig.ID)
	assert.Contains(t, discovery.GetTools(), "wiki-read")

	for _, unknown := range []string{"wiki-missing", "nobody-read", "nohyphen"} {
		state, _ = m.GetClientForTool(userContext("alice"), unknown)
		assert.Nil(t, state, unknown)
	}
}

func TestGetToolsAndInstructionsForClient_ServeTheCallersOwn(t *testing.T) {
	perUser := newFakePerUserDiscoveryStore()
	m, _, notes := newPerUserTestManager(t, perUser)
	alices := discoveryOf("notes", "search")
	alices.Instructions = "Alice's rule."
	require.NoError(t, m.discoveryStore.SetDiscovery(userContext("alice"), notes, alices))

	assert.Contains(t, m.GetToolsForClient(userContext("alice"), notes.ID), "notes-search")
	assert.Equal(t, "Alice's rule.", m.GetInstructionsForClient(userContext("alice"), notes.ID))
	assert.Empty(t, m.GetToolsForClient(context.Background(), notes.ID))
	assert.Empty(t, m.GetToolsForClient(userContext("alice"), "missing-id"))
}

// staticPerUserDiscoveryStore serves one prebuilt discovery per client to any caller, with no
// allocation of its own, so a benchmark measures the manager's routing and not the fake.
type staticPerUserDiscoveryStore struct {
	byClient map[string]*schemas.MCPDiscovery
}

func (s *staticPerUserDiscoveryStore) GetDiscovery(_ context.Context, client *schemas.MCPClientConfig) *schemas.MCPDiscovery {
	return s.byClient[client.ID]
}

func (s *staticPerUserDiscoveryStore) SetDiscovery(context.Context, *schemas.MCPClientConfig, *schemas.MCPDiscovery) error {
	return nil
}

func (s *staticPerUserDiscoveryStore) DeleteDiscovery(context.Context, *schemas.MCPClientConfig) error {
	return nil
}

// benchmarkPerUserManager is benchmarkSharedManager with every client on per_user_discovery,
// its tools served from the per-user store.
func benchmarkPerUserManager(clients, tools int) (*MCPManager, *schemas.BifrostContext) {
	shared, ctx := benchmarkSharedManager(clients, tools)
	perUser := &staticPerUserDiscoveryStore{byClient: map[string]*schemas.MCPDiscovery{}}
	m := NewMCPManager(context.Background(), schemas.MCPConfig{PerUserDiscoveryStore: perUser}, nil, &MockLogger{}, nil)
	for id, client := range shared.clientMap {
		perUser.byClient[id] = shared.discoveryStore.GetDiscovery(context.Background(), client.ExecutionConfig)
		config := *client.ExecutionConfig
		config.PerUserDiscovery = schemas.Ptr(true)
		m.clientMap[id] = &schemas.MCPClientState{Name: client.Name, ExecutionConfig: &config, State: client.State}
	}
	return m, ctx
}

// The tool list of a request when every client is on per_user_discovery and the caller's
// discoveries are cached.
func BenchmarkGetToolPerClient_PerUserClients(b *testing.B) {
	m, ctx := benchmarkPerUserManager(10, 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.GetToolPerClient(ctx)
	}
}

// Resolving a per-user client's tool for execution.
func BenchmarkGetClientForTool_PerUserClient(b *testing.B) {
	m, ctx := benchmarkPerUserManager(10, 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = m.GetClientForTool(ctx, "client7-tool13")
	}
}
