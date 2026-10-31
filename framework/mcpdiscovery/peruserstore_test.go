package mcpdiscovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/lrucache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeConfigStore keeps per-user discovery rows in memory and counts reads,
// so tests can tell a cache hit from a database read. Two Stores over one
// fakeConfigStore are two processes over one database.
type fakeConfigStore struct {
	configstore.ConfigStore
	mu            sync.Mutex
	rows          map[string]tables.TableMCPPerUserDiscovery
	reads         int
	readErr       error
	readHold      *hold
	bindingReads  int
	bindingsErr   error
	bindingsHold  *hold
	bindingsPanic bool // the next bindings query panics
	toolWrites    []toolWrite
	toolsErr      error
	toolsFails    int // the next this many discovery writes fail, as toolsErr does
	toolsHold     *hold
	toolsPanic    bool // the next tools write panics
	lastWritten   *schemas.MCPDiscovery
}

// toolWrite is one UpdateMCPClientDiscovery call that succeeded.
type toolWrite struct {
	clientID     string
	tools        []string // sorted
	instructions string
}

// hold pauses the next fake read after it has taken its result, so a test can
// land a write between the read and its use.
type hold struct {
	reached chan struct{}
	release chan struct{}
}

func newHold() *hold {
	return &hold{reached: make(chan struct{}), release: make(chan struct{})}
}

func (h *hold) wait() {
	if h == nil {
		return
	}
	close(h.reached)
	<-h.release
}

// waitSignal closes waited the first time a read selects on its Done. On a
// client whose set is loaded, that is the read joining a fill another caller
// leads.
type waitSignal struct {
	context.Context
	once   sync.Once
	waited chan struct{}
}

func newWaitSignal(ctx context.Context) *waitSignal {
	return &waitSignal{Context: ctx, waited: make(chan struct{})}
}

func (c *waitSignal) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waited) })
	return c.Context.Done()
}

func newFakeConfigStore() *fakeConfigStore {
	return &fakeConfigStore{rows: map[string]tables.TableMCPPerUserDiscovery{}}
}

// rowKey keys the fake's rows by their (mode, identity, client) binding.
func rowKey(mode schemas.MCPAuthMode, identity, mcpClientID string) string {
	return lrucache.EncodeKey(string(mode), identity, mcpClientID)
}

func (f *fakeConfigStore) GetMCPPerUserDiscoveryByMode(_ context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (*tables.TableMCPPerUserDiscovery, error) {
	f.mu.Lock()
	f.reads++
	err := f.readErr
	row, ok := f.rows[rowKey(mode, identity, mcpClientID)]
	h := f.readHold
	f.readHold = nil
	f.mu.Unlock()
	h.wait()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &row, nil
}

func (f *fakeConfigStore) GetMCPPerUserDiscoveryBindings(_ context.Context, mcpClientID string) ([]tables.TableMCPPerUserDiscovery, error) {
	f.mu.Lock()
	f.bindingReads++
	err := f.bindingsErr
	var rows []tables.TableMCPPerUserDiscovery
	for _, row := range f.rows {
		if row.MCPClientID == mcpClientID {
			rows = append(rows, tables.TableMCPPerUserDiscovery{AuthMode: row.AuthMode, UserID: row.UserID, VirtualKeyID: row.VirtualKeyID, MCPClientID: row.MCPClientID, DiscoveryHash: row.DiscoveryHash})
		}
	}
	h := f.bindingsHold
	f.bindingsHold = nil
	panics := f.bindingsPanic
	f.bindingsPanic = false
	f.mu.Unlock()
	h.wait()
	if panics {
		panic("bindings query panicked")
	}
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (f *fakeConfigStore) UpsertMCPPerUserDiscovery(_ context.Context, discovery *tables.TableMCPPerUserDiscovery) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[rowKey(schemas.MCPAuthMode(discovery.AuthMode), discovery.Identity(), discovery.MCPClientID)] = *discovery
	return nil
}

func (f *fakeConfigStore) DeleteMCPPerUserDiscovery(_ context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, rowKey(mode, identity, mcpClientID))
	return nil
}

func (f *fakeConfigStore) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func (f *fakeConfigStore) bindingReadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bindingReads
}

func (f *fakeConfigStore) UpdateMCPClientDiscovery(_ context.Context, clientID string, discovery *schemas.MCPDiscovery) error {
	f.mu.Lock()
	err := f.toolsErr
	if f.toolsFails > 0 {
		f.toolsFails--
		err = errors.New("database unavailable")
	}
	h := f.toolsHold
	f.toolsHold = nil
	panics := f.toolsPanic
	f.toolsPanic = false
	f.mu.Unlock()
	h.wait()
	if panics {
		panic("tools write panicked")
	}
	if err != nil {
		return err
	}
	names := make([]string, 0, len(discovery.Tools))
	for name := range discovery.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.toolWrites = append(f.toolWrites, toolWrite{clientID: clientID, tools: names, instructions: discovery.Instructions})
	f.lastWritten = discovery
	return nil
}

func (f *fakeConfigStore) writtenTools() []toolWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]toolWrite(nil), f.toolWrites...)
}

func (f *fakeConfigStore) set(apply func(f *fakeConfigStore)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	apply(f)
}

// dueForReload makes clientID's set due for a reload on its next read, as if
// membershipRefreshInterval had passed.
func dueForReload(t *testing.T, s *PerUserStore, clientID string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	cm := s.clients[clientID]
	require.NotNil(t, cm, "client %s has no set", clientID)
	cm.due.Store(true)
}

// runningLoad returns the load of clientID's set that is running, nil if none.
func runningLoad(s *PerUserStore, clientID string) chan struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cm := s.clients[clientID]; cm != nil {
		return cm.loading
	}
	return nil
}

// waitForLoad waits for a running load of clientID's set, if any, to install
// its result.
func waitForLoad(s *PerUserStore, clientID string) {
	if loading := runningLoad(s, clientID); loading != nil {
		<-loading
	}
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

// modeContext is the caller context for a binding in table-driven tests.
func modeContext(mode schemas.MCPAuthMode, identity string) *schemas.BifrostContext {
	if mode == schemas.MCPAuthModeVK {
		return vkContext(identity)
	}
	return userContext(identity)
}

func notesClient() *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{ID: "client-notes", Name: "notes", PerUserDiscovery: schemas.Ptr(true)}
}

// notesDiscovery is shaped the way core produces one: tools keyed and named
// with the client prefix, the mapping keyed by the bare sanitized name.
func notesDiscovery(tools ...string) *schemas.MCPDiscovery {
	discovery := &schemas.MCPDiscovery{
		Tools:           map[string]schemas.ChatTool{},
		ToolNameMapping: map[string]string{},
		Instructions:    "Search before you write.",
		DiscoveredAt:    time.Now().UTC(),
	}
	for _, tool := range tools {
		prefixed := "notes-" + tool
		discovery.Tools[prefixed] = schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: prefixed}}
		discovery.ToolNameMapping[tool] = tool
	}
	return discovery
}

func TestSetDiscovery_StoresBareNamesAndServesThemPrefixed(t *testing.T) {
	db := newFakeConfigStore()
	store := NewPerUserStore(db, nil)

	require.NoError(t, store.SetDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), notesClient(), notesDiscovery("search", "create_page")))

	row := db.rows[rowKey(schemas.MCPAuthModeUser, "alice", "client-notes")]
	require.Contains(t, row.Tools, "search", "rows keep the upstream name without the client prefix")
	assert.Equal(t, "search", row.Tools["search"].Function.Name)
	assert.Equal(t, "alice", *row.UserID)
	assert.NotEmpty(t, row.DiscoveryHash)

	got := store.GetDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), notesClient())
	require.NotNil(t, got)
	require.Contains(t, got.Tools, "notes-search")
	assert.Equal(t, "notes-search", got.Tools["notes-search"].Function.Name)
	assert.Equal(t, "search", got.ToolNameMapping["search"], "the mapping is keyed by the bare sanitized name, as core reads it")
	assert.Equal(t, "Search before you write.", got.Instructions)
}

// A caller's resources, resource templates and prompts are kept with their tools. Prompt names are
// stored bare and served under the client's current name, like tool names, and the lists are part
// of the discovery's hash.
func TestSetDiscovery_KeepsResourcesAndPromptsWithTheTools(t *testing.T) {
	db := newFakeConfigStore()
	discovery := notesDiscovery("search")
	discovery.Resources = []mcpgo.Resource{mcpgo.NewResource("file:///handbook.md", "handbook")}
	discovery.ResourceTemplates = []mcpgo.ResourceTemplate{mcpgo.NewResourceTemplate("notes://{id}", "note")}
	discovery.Prompts = []mcpgo.Prompt{mcpgo.NewPrompt("notes-review")}
	require.NoError(t, NewPerUserStore(db, nil).SetDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), notesClient(), discovery))
	require.NoError(t, NewPerUserStore(db, nil).SetDiscovery(modeContext(schemas.MCPAuthModeUser, "bob"), notesClient(), notesDiscovery("search")))

	row := db.rows[rowKey(schemas.MCPAuthModeUser, "alice", "client-notes")]
	require.Len(t, row.Prompts, 1)
	assert.Equal(t, "review", row.Prompts[0].Name, "rows keep the upstream prompt name without the client prefix")
	assert.NotEqual(t, db.rows[rowKey(schemas.MCPAuthModeUser, "bob", "client-notes")].DiscoveryHash, row.DiscoveryHash,
		"the same tools with other resources and prompts are another discovery")
	assert.Equal(t, "notes-review", discovery.Prompts[0].Name, "stripping the prefix for storage must not rename the caller's prompts")

	renamed := notesClient()
	renamed.Name = "docs"
	got := NewPerUserStore(db, nil).GetDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), renamed)
	require.NotNil(t, got)
	require.Len(t, got.Prompts, 1)
	assert.Equal(t, "docs-review", got.Prompts[0].Name, "prompts are served under the client's current name")
	assert.Equal(t, discovery.Resources, got.Resources)
	assert.Equal(t, discovery.ResourceTemplates, got.ResourceTemplates)
}

func TestSetDiscovery_LeavesTheCallersDiscoveryUntouched(t *testing.T) {
	store := NewPerUserStore(newFakeConfigStore(), nil)
	discovery := notesDiscovery("search")

	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), discovery))
	assert.Equal(t, "notes-search", discovery.Tools["notes-search"].Function.Name, "stripping the prefix for storage must not rename the caller's tools")
}

// A discovery whose tools already carry their cached JSON is stored and hashed
// under the bare names, the same as one whose tools were never serialized.
func TestSetDiscovery_StoresBareNamesForPreSerializedTools(t *testing.T) {
	db := newFakeConfigStore()
	serialized := notesDiscovery("search")
	tool := serialized.Tools["notes-search"]
	require.NoError(t, tool.EnsureSerialized())
	serialized.Tools["notes-search"] = tool
	require.NoError(t, NewPerUserStore(db, nil).SetDiscovery(userContext("alice"), notesClient(), serialized))
	require.NoError(t, NewPerUserStore(db, nil).SetDiscovery(userContext("bob"), notesClient(), notesDiscovery("search")))

	row := db.rows[rowKey(schemas.MCPAuthModeUser, "alice", "client-notes")]
	stored, err := json.Marshal(row.Tools["search"])
	require.NoError(t, err)
	assert.Contains(t, string(stored), `"name":"search"`, "the stored tool must not carry the prefixed name from its cached JSON")
	assert.Equal(t, db.rows[rowKey(schemas.MCPAuthModeUser, "bob", "client-notes")].DiscoveryHash, row.DiscoveryHash,
		"the same tools hash the same whether or not they were serialized before")
}

func TestGetDiscovery_ACallerWithoutADiscoveryCostsNoRead(t *testing.T) {
	db := newFakeConfigStore()
	require.NoError(t, NewPerUserStore(db, nil).SetDiscovery(userContext("bob"), notesClient(), notesDiscovery("search")))
	store := NewPerUserStore(db, nil)

	assert.Nil(t, store.GetDiscovery(userContext("alice"), notesClient()))
	assert.Nil(t, store.GetDiscovery(userContext("alice"), notesClient()))
	assert.Zero(t, db.readCount(), "a caller outside the client's set is answered from memory")
	assert.Equal(t, 1, db.bindingReadCount(), "the client's set is loaded once, on its first read")
}

func TestSetDiscovery_AddsTheCallerToALoadedClient(t *testing.T) {
	db := newFakeConfigStore()
	store := NewPerUserStore(db, nil)

	require.Nil(t, store.GetDiscovery(modeContext(schemas.MCPAuthModeVK, "vk-1"), notesClient()))
	require.NoError(t, store.SetDiscovery(modeContext(schemas.MCPAuthModeVK, "vk-1"), notesClient(), notesDiscovery("search")))

	got := store.GetDiscovery(modeContext(schemas.MCPAuthModeVK, "vk-1"), notesClient())
	require.NotNil(t, got, "a write puts the caller in the set it was missing from")
	assert.Contains(t, got.Tools, "notes-search")
	assert.Equal(t, 1, db.bindingReadCount(), "without reloading the set")
}

func TestGetDiscovery_RenamedClientIsServedUnderTheNewName(t *testing.T) {
	store := NewPerUserStore(newFakeConfigStore(), nil)
	require.NoError(t, store.SetDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), notesClient(), notesDiscovery("search")))
	require.NotNil(t, store.GetDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), notesClient()))

	renamed := notesClient()
	renamed.Name = "docs"
	got := store.GetDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), renamed)
	require.NotNil(t, got)
	assert.Contains(t, got.Tools, "docs-search", "a rename rewrites no row: the prefix comes from the current name")
	assert.NotContains(t, got.Tools, "notes-search")
}

func TestGetDiscovery_CallersWithTheSameDiscoveryShareOneCopy(t *testing.T) {
	db := newFakeConfigStore()
	callers := []*schemas.BifrostContext{userContext("alice"), userContext("bob"), vkContext("vk-1")}
	for _, ctx := range callers {
		require.NoError(t, NewPerUserStore(db, nil).SetDiscovery(ctx, notesClient(), notesDiscovery("search")))
	}
	store := NewPerUserStore(db, nil)

	first := store.GetDiscovery(callers[0], notesClient())
	require.NotNil(t, first)
	for _, ctx := range callers[1:] {
		assert.Same(t, first, store.GetDiscovery(ctx, notesClient()))
	}
	assert.Equal(t, 1, db.readCount(), "only the first caller's row is read")
	assert.Equal(t, 1, store.cache.Len(), "the cache holds one copy per distinct discovery, not one per caller")
}

func TestSetDiscovery_AChangedDiscoveryLeavesTheOldOneToItsOtherHolders(t *testing.T) {
	db := newFakeConfigStore()
	store := NewPerUserStore(db, nil)
	for _, caller := range []string{"alice", "bob", "carol"} {
		require.NoError(t, store.SetDiscovery(userContext(caller), notesClient(), notesDiscovery("search")))
	}
	shared := store.GetDiscovery(userContext("bob"), notesClient())
	require.NotNil(t, shared)

	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search", "create_page")))

	before := db.readCount()
	assert.Same(t, shared, store.GetDiscovery(userContext("bob"), notesClient()))
	assert.Same(t, shared, store.GetDiscovery(userContext("carol"), notesClient()), "carol is served the copy bob's read made")
	assert.Equal(t, before, db.readCount(), "callers still holding the old discovery are answered from memory")
	assert.Contains(t, store.GetDiscovery(userContext("alice"), notesClient()).Tools, "notes-create_page")
}

func TestGetDiscovery_AReadThatEndsWhileTheCallersRowIsReadReturnsAtOnce(t *testing.T) {
	db := newFakeConfigStore()
	require.NoError(t, NewPerUserStore(db, nil).SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	store := NewPerUserStore(db, nil)
	require.Nil(t, store.GetDiscovery(userContext("bob"), notesClient()))
	h := newHold()
	db.set(func(f *fakeConfigStore) { f.readHold = h })
	done := make(chan *schemas.MCPDiscovery, 1)
	go func() { done <- store.GetDiscovery(userContext("alice"), notesClient()) }()
	<-h.reached

	ended, cancel := context.WithCancel(userContext("alice"))
	cancel()
	assert.Nil(t, store.GetDiscovery(ended, notesClient()))
	assert.Equal(t, 1, db.readCount(), "a second read of the same caller waits on the first rather than reading the row again")

	close(h.release)
	assert.NotNil(t, <-done)
}

func TestGetDiscovery_AWaiterWhoseLeadersRequestEndsReadsItsOwnRow(t *testing.T) {
	db := newFakeConfigStore()
	for _, caller := range []string{"alice", "bob"} {
		require.NoError(t, NewPerUserStore(db, nil).SetDiscovery(userContext(caller), notesClient(), notesDiscovery("search")))
	}
	store := NewPerUserStore(db, nil)
	require.Nil(t, store.GetDiscovery(userContext("carol"), notesClient()))
	h := newHold()
	db.set(func(f *fakeConfigStore) { f.readHold, f.readErr = h, context.Canceled })
	led := make(chan *schemas.MCPDiscovery, 1)
	go func() { led <- store.GetDiscovery(userContext("alice"), notesClient()) }()
	<-h.reached
	// alice's read has taken the error her ended request gets; later reads succeed.
	db.set(func(f *fakeConfigStore) { f.readErr = nil })

	bob := newWaitSignal(userContext("bob"))
	waited := make(chan *schemas.MCPDiscovery, 1)
	go func() { waited <- store.GetDiscovery(bob, notesClient()) }()
	<-bob.waited
	close(h.release)

	assert.Nil(t, <-led)
	got := <-waited
	require.NotNil(t, got, "a read still waiting is not failed by the request that led the fill ending")
	assert.Contains(t, got.Tools, "notes-search")
}

// Callers whose hashes match are served one shared copy, so the hash must cover everything a
// discovery serves. A field added to MCPDiscovery, or a ChatTool field the tool JSON leaves out,
// fails here until discoveryHash covers it.
func TestDiscoveryHash_CoversEveryFieldADiscoveryServes(t *testing.T) {
	base := func() *schemas.MCPDiscovery {
		discovery := notesDiscovery("search")
		discovery.DiscoveredAt = time.Time{}
		return discovery
	}
	hash := func(discovery *schemas.MCPDiscovery) string {
		t.Helper()
		h, err := discoveryHash(discovery)
		require.NoError(t, err)
		return h
	}
	baseHash := hash(base())

	changes := map[string]func(d *schemas.MCPDiscovery){
		"Tools": func(d *schemas.MCPDiscovery) {
			d.Tools["notes-create_page"] = schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "notes-create_page"}}
		},
		"ToolNameMapping": func(d *schemas.MCPDiscovery) { d.ToolNameMapping["find"] = "search" },
		"Instructions":    func(d *schemas.MCPDiscovery) { d.Instructions = "Write before you search." },
		"Resources": func(d *schemas.MCPDiscovery) {
			d.Resources = []mcpgo.Resource{mcpgo.NewResource("file:///handbook.md", "handbook")}
		},
		"ResourceTemplates": func(d *schemas.MCPDiscovery) {
			d.ResourceTemplates = []mcpgo.ResourceTemplate{mcpgo.NewResourceTemplate("notes://{id}", "note")}
		},
		"Prompts":      func(d *schemas.MCPDiscovery) { d.Prompts = []mcpgo.Prompt{mcpgo.NewPrompt("review")} },
		"DiscoveredAt": nil, // when, not what: the same discovery made at another time is shared
	}
	fields := reflect.TypeOf(schemas.MCPDiscovery{})
	for i := 0; i < fields.NumField(); i++ {
		name := fields.Field(i).Name
		change, known := changes[name]
		if !assert.True(t, known, "MCPDiscovery.%s is new: hash it in discoveryHash, store it in the row and give it a case here", name) {
			continue
		}
		discovery := base()
		if change == nil {
			discovery.DiscoveredAt = time.Now()
			assert.Equal(t, baseHash, hash(discovery), "%s must not change the hash", name)
			continue
		}
		change(discovery)
		assert.NotEqual(t, baseHash, hash(discovery), "a discovery differing only in %s must hash differently", name)
	}

	details := map[string]func(tool *schemas.ChatTool){
		"Annotations": func(tool *schemas.ChatTool) { tool.Annotations = &schemas.MCPToolAnnotations{Title: "Search"} },
		"MCP":         func(tool *schemas.ChatTool) { tool.MCP = &schemas.MCPToolMetadata{Title: "Search"} },
	}
	toolFields := reflect.TypeOf(schemas.ChatTool{})
	for i := 0; i < toolFields.NumField(); i++ {
		field := toolFields.Field(i)
		if !field.IsExported() || strings.Split(field.Tag.Get("json"), ",")[0] != "-" {
			continue
		}
		change, known := details[field.Name]
		if !assert.True(t, known, "ChatTool.%s is left out of the tool JSON the hash reads: hash it with the tool details and give it a case here", field.Name) {
			continue
		}
		discovery := base()
		tool := discovery.Tools["notes-search"]
		change(&tool)
		discovery.Tools["notes-search"] = tool
		assert.NotEqual(t, baseHash, hash(discovery), "tools differing only in %s must hash differently", field.Name)
	}
}

func TestGetDiscovery_ReadErrorFailsClosedAndIsNotCached(t *testing.T) {
	db := newFakeConfigStore()
	store := NewPerUserStore(db, nil)
	require.NoError(t, store.SetDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), notesClient(), notesDiscovery("search")))

	db.set(func(f *fakeConfigStore) { f.readErr = errors.New("database unavailable") })
	assert.Nil(t, store.GetDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), notesClient()), "a failed read serves no tools")

	db.set(func(f *fakeConfigStore) { f.readErr = nil })
	assert.NotNil(t, store.GetDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), notesClient()), "the failure was not remembered")
}

func TestGetDiscovery_LoadFailureFailsTheRequestAndTheNextOneTriesAgain(t *testing.T) {
	db := newFakeConfigStore()
	require.NoError(t, NewPerUserStore(db, nil).SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	store := NewPerUserStore(db, nil)

	db.set(func(f *fakeConfigStore) { f.bindingsErr = errors.New("database unavailable") })
	assert.Nil(t, store.GetDiscovery(userContext("alice"), notesClient()), "a client whose set cannot be loaded serves no tools")
	assert.Nil(t, store.GetDiscovery(userContext("alice"), notesClient()))
	assert.Equal(t, 2, db.bindingReadCount(), "each request tries the load once")

	db.set(func(f *fakeConfigStore) { f.bindingsErr = nil })
	assert.NotNil(t, store.GetDiscovery(userContext("alice"), notesClient()))
}

func TestRefresh_FailureKeepsTheSet(t *testing.T) {
	db := newFakeConfigStore()
	store := NewPerUserStore(db, nil)
	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	require.NotNil(t, store.GetDiscovery(userContext("alice"), notesClient()))

	db.set(func(f *fakeConfigStore) { f.bindingsErr = errors.New("database unavailable") })
	dueForReload(t, store, "client-notes")
	require.NotNil(t, store.GetDiscovery(userContext("alice"), notesClient()))
	waitForLoad(store, "client-notes")

	assert.NotNil(t, store.GetDiscovery(userContext("alice"), notesClient()), "a failed reload keeps what the set had")
	waitForLoad(store, "client-notes")
	assert.Equal(t, 3, db.bindingReadCount(), "and leaves it due, so the next read tries again")

	db.set(func(f *fakeConfigStore) { f.bindingsErr = nil })
	require.NotNil(t, store.GetDiscovery(userContext("alice"), notesClient()))
	waitForLoad(store, "client-notes")
	require.NotNil(t, store.GetDiscovery(userContext("alice"), notesClient()))
	waitForLoad(store, "client-notes")
	assert.Equal(t, 4, db.bindingReadCount(), "a reload that succeeds is not due again until the interval passes")
}

func TestLoad_AQueryThatPanicsDoesNotWedgeTheClient(t *testing.T) {
	t.Run("first load", func(t *testing.T) {
		db := newFakeConfigStore()
		require.NoError(t, NewPerUserStore(db, nil).SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
		store := NewPerUserStore(db, nil)
		db.set(func(f *fakeConfigStore) { f.bindingsPanic = true })

		assert.Panics(t, func() { store.GetDiscovery(userContext("alice"), notesClient()) }, "the request that ran the load sees the panic")
		ctx, cancel := context.WithTimeout(userContext("alice"), time.Second)
		defer cancel()
		assert.NotNil(t, store.GetDiscovery(ctx, notesClient()), "the next read loads the set rather than waiting on the panicked load")
	})
	t.Run("refresh", func(t *testing.T) {
		db := newFakeConfigStore()
		local, peer := NewPerUserStore(db, nil), NewPerUserStore(db, nil)
		require.NoError(t, local.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
		require.NotNil(t, local.GetDiscovery(userContext("alice"), notesClient()))
		require.NoError(t, peer.SetDiscovery(userContext("bob"), notesClient(), notesDiscovery("search")))
		db.set(func(f *fakeConfigStore) { f.bindingsPanic = true })

		dueForReload(t, local, "client-notes")
		require.NotNil(t, local.GetDiscovery(userContext("alice"), notesClient()))
		waitForLoad(local, "client-notes")
		require.NotNil(t, local.GetDiscovery(userContext("alice"), notesClient()), "a reload that panicked keeps the set, and leaves it due")
		waitForLoad(local, "client-notes")

		assert.NotNil(t, local.GetDiscovery(userContext("bob"), notesClient()), "the next reload ran")
	})
}

func TestPerUserDiscovery_BindingsAreIsolated(t *testing.T) {
	store := NewPerUserStore(newFakeConfigStore(), nil)
	require.NoError(t, store.SetDiscovery(modeContext(schemas.MCPAuthModeUser, "a"), notesClient(), notesDiscovery("search")))

	assert.Nil(t, store.GetDiscovery(modeContext(schemas.MCPAuthModeVK, "a"), notesClient()), "the same identity string in another mode is another caller")
	assert.Nil(t, store.GetDiscovery(modeContext(schemas.MCPAuthModeUser, "b"), notesClient()))
	other := notesClient()
	other.ID = "client-other"
	assert.Nil(t, store.GetDiscovery(modeContext(schemas.MCPAuthModeUser, "a"), other))
}

func TestDiscovery_OnlyUserAndVirtualKeyCallersHaveOne(t *testing.T) {
	db := newFakeConfigStore()
	store := NewPerUserStore(db, nil)
	noCaller := map[string]context.Context{
		"session-only":   callerContext(schemas.BifrostContextKeyMCPSessionID, "sess-1"),
		"anonymous":      context.Background(),
		"empty identity": userContext(""),
	}
	for name, ctx := range noCaller {
		assert.ErrorIs(t, store.SetDiscovery(ctx, notesClient(), notesDiscovery("search")), errNoCaller, name)
		assert.ErrorIs(t, store.DeleteDiscovery(ctx, notesClient()), errNoCaller, name)
		assert.Nil(t, store.GetDiscovery(ctx, notesClient()), name)
	}
	assert.Zero(t, db.readCount()+db.bindingReadCount(), "a context with no caller never reaches the database")
}

func TestGetDiscovery_ReadsTheCallerThroughADerivedContext(t *testing.T) {
	store := NewPerUserStore(newFakeConfigStore(), nil)
	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))

	type unrelated struct{}
	derived, cancel := context.WithCancel(context.WithValue(userContext("alice"), unrelated{}, 1))
	defer cancel()
	got := store.GetDiscovery(derived, notesClient())
	require.NotNil(t, got, "a context derived from the request still names its caller")
	assert.Contains(t, got.Tools, "notes-search")
}

func TestDeleteDiscovery_TakesTheCallerOutOfTheSet(t *testing.T) {
	db := newFakeConfigStore()
	store := NewPerUserStore(db, nil)
	require.NoError(t, store.SetDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), notesClient(), notesDiscovery("search")))
	require.NotNil(t, store.GetDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), notesClient()))

	require.NoError(t, store.DeleteDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), notesClient()))
	before := db.readCount()
	assert.Nil(t, store.GetDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), notesClient()))
	assert.Equal(t, before, db.readCount(), "a caller this process deleted is answered from memory")
}

func TestEvictDiscovery_AnotherProcessesWriteIsReadFromTheDatabase(t *testing.T) {
	db := newFakeConfigStore()
	local, peer := NewPerUserStore(db, nil), NewPerUserStore(db, nil)
	require.Nil(t, local.GetDiscovery(userContext("alice"), notesClient()))

	require.NoError(t, peer.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	require.Nil(t, local.GetDiscovery(userContext("alice"), notesClient()), "until told, this process does not know of the other's write")

	local.EvictDiscovery(schemas.MCPAuthModeUser, "alice", "client-notes")
	got := local.GetDiscovery(userContext("alice"), notesClient())
	require.NotNil(t, got)
	assert.Contains(t, got.Tools, "notes-search")
}

func TestEvictDiscovery_AnotherProcessesDeleteIsSettledByOneRead(t *testing.T) {
	db := newFakeConfigStore()
	local, peer := NewPerUserStore(db, nil), NewPerUserStore(db, nil)
	require.NoError(t, peer.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	require.NotNil(t, local.GetDiscovery(userContext("alice"), notesClient()))

	require.NoError(t, peer.DeleteDiscovery(userContext("alice"), notesClient()))
	local.EvictDiscovery(schemas.MCPAuthModeUser, "alice", "client-notes")

	before := db.readCount()
	assert.Nil(t, local.GetDiscovery(userContext("alice"), notesClient()))
	assert.Equal(t, before+1, db.readCount(), "one read finds the row gone")
	assert.Nil(t, local.GetDiscovery(userContext("alice"), notesClient()))
	assert.Equal(t, before+1, db.readCount(), "and takes the caller out of the set, so later reads cost none")
}

func TestEvictDiscovery_OutOfOrderMessagesNeverHideADiscovery(t *testing.T) {
	db := newFakeConfigStore()
	local, deleter, writer := NewPerUserStore(db, nil), NewPerUserStore(db, nil), NewPerUserStore(db, nil)
	require.Nil(t, local.GetDiscovery(userContext("alice"), notesClient()))

	require.NoError(t, deleter.DeleteDiscovery(userContext("alice"), notesClient()))
	require.NoError(t, writer.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	// The writer's message arrives first and the deleter's last.
	local.EvictDiscovery(schemas.MCPAuthModeUser, "alice", "client-notes")
	local.EvictDiscovery(schemas.MCPAuthModeUser, "alice", "client-notes")

	assert.NotNil(t, local.GetDiscovery(userContext("alice"), notesClient()), "a delete delivered after a later write does not hide the discovery")
}

func TestRefresh_PicksUpAWriteThisProcessWasNotToldOf(t *testing.T) {
	db := newFakeConfigStore()
	local, peer := NewPerUserStore(db, nil), NewPerUserStore(db, nil)
	require.Nil(t, local.GetDiscovery(userContext("alice"), notesClient()))
	require.NoError(t, peer.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))

	dueForReload(t, local, "client-notes")
	require.Nil(t, local.GetDiscovery(userContext("alice"), notesClient()), "the read that finds the set due is answered from it as it stands")
	waitForLoad(local, "client-notes")

	assert.NotNil(t, local.GetDiscovery(userContext("alice"), notesClient()))
}

func TestRefresh_DropsADeleteThisProcessWasNotToldOf(t *testing.T) {
	db := newFakeConfigStore()
	local, peer := NewPerUserStore(db, nil), NewPerUserStore(db, nil)
	require.NoError(t, peer.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	require.NotNil(t, local.GetDiscovery(userContext("alice"), notesClient()))
	require.NoError(t, peer.DeleteDiscovery(userContext("alice"), notesClient()))

	dueForReload(t, local, "client-notes")
	require.NotNil(t, local.GetDiscovery(userContext("alice"), notesClient()))
	waitForLoad(local, "client-notes")

	before := db.readCount()
	assert.Nil(t, local.GetDiscovery(userContext("alice"), notesClient()))
	assert.Equal(t, before, db.readCount(), "the reload took the caller out of the set")
}

func TestRefresh_ReplacesADiscoveryThatChangedOnAnotherProcess(t *testing.T) {
	db := newFakeConfigStore()
	local, peer := NewPerUserStore(db, nil), NewPerUserStore(db, nil)
	require.NoError(t, peer.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	require.NotNil(t, local.GetDiscovery(userContext("alice"), notesClient()))
	require.NoError(t, peer.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search", "create_page")))

	dueForReload(t, local, "client-notes")
	require.NotContains(t, local.GetDiscovery(userContext("alice"), notesClient()).Tools, "notes-create_page")
	waitForLoad(local, "client-notes")

	got := local.GetDiscovery(userContext("alice"), notesClient())
	require.NotNil(t, got)
	assert.Contains(t, got.Tools, "notes-create_page", "a cached discovery whose row hash changed is read again")
}

func TestGetDiscovery_LearnsANewerHashThanTheSetHas(t *testing.T) {
	db := newFakeConfigStore()
	local, peer := NewPerUserStore(db, nil), NewPerUserStore(db, nil)
	require.NoError(t, peer.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	require.Nil(t, local.GetDiscovery(userContext("bob"), notesClient()), "loads the set, holding alice's first hash")
	// alice's discovery changes on the other process, and its message never arrives.
	require.NoError(t, peer.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search", "create_page")))

	before := db.readCount()
	require.Contains(t, local.GetDiscovery(userContext("alice"), notesClient()).Tools, "notes-create_page")
	require.Contains(t, local.GetDiscovery(userContext("alice"), notesClient()).Tools, "notes-create_page")
	assert.Equal(t, before+1, db.readCount(), "the read that found a newer row records its hash, so the next is served from memory")
}

func TestLoad_KeepsAWriteThatLandsWhileTheQueryRuns(t *testing.T) {
	t.Run("first load", func(t *testing.T) {
		db := newFakeConfigStore()
		store := NewPerUserStore(db, nil)
		h := newHold()
		db.set(func(f *fakeConfigStore) { f.bindingsHold = h })

		done := make(chan struct{})
		go func() {
			defer close(done)
			store.GetDiscovery(userContext("bob"), notesClient())
		}()
		<-h.reached
		require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
		close(h.release)
		<-done

		assert.NotNil(t, store.GetDiscovery(userContext("alice"), notesClient()), "the load's older result does not erase the write")
	})
	t.Run("refresh", func(t *testing.T) {
		db := newFakeConfigStore()
		store := NewPerUserStore(db, nil)
		require.Nil(t, store.GetDiscovery(userContext("bob"), notesClient()))
		h := newHold()
		db.set(func(f *fakeConfigStore) { f.bindingsHold = h })

		dueForReload(t, store, "client-notes")
		store.GetDiscovery(userContext("bob"), notesClient())
		<-h.reached
		require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
		close(h.release)
		waitForLoad(store, "client-notes")

		assert.NotNil(t, store.GetDiscovery(userContext("alice"), notesClient()), "the reload's older result does not erase the write")
	})
}

func TestLoad_AReadDuringTheFirstLoadWaitsForIt(t *testing.T) {
	db := newFakeConfigStore()
	store := NewPerUserStore(db, nil)
	h := newHold()
	db.set(func(f *fakeConfigStore) { f.bindingsHold = h })
	done := make(chan struct{})
	go func() {
		defer close(done)
		store.GetDiscovery(userContext("alice"), notesClient())
	}()
	<-h.reached

	// A read whose request ends while it waits returns at once.
	ended, cancel := context.WithCancel(userContext("bob"))
	cancel()
	assert.Nil(t, store.GetDiscovery(ended, notesClient()))
	assert.Equal(t, 1, db.bindingReadCount(), "a read during the first load waits for it rather than running its own")

	close(h.release)
	<-done
	assert.Equal(t, 1, db.bindingReadCount())
}

func TestRefresh_ReadsDuringAReloadDoNotStartAnother(t *testing.T) {
	db := newFakeConfigStore()
	store := NewPerUserStore(db, nil)
	require.Nil(t, store.GetDiscovery(userContext("alice"), notesClient()))
	h := newHold()
	db.set(func(f *fakeConfigStore) { f.bindingsHold = h })
	dueForReload(t, store, "client-notes")
	store.GetDiscovery(userContext("alice"), notesClient())
	<-h.reached
	running := runningLoad(store, "client-notes")
	require.NotNil(t, running)

	store.GetDiscovery(userContext("alice"), notesClient())
	store.refresh("client-notes") // a read that saw the set due just before the reload started
	assert.True(t, running == runningLoad(store, "client-notes"), "the reload running is still the first one")

	close(h.release)
	waitForLoad(store, "client-notes")
	assert.Equal(t, 2, db.bindingReadCount())
}

func TestGetDiscovery_AWriteDuringAReadThatFindsNoRowKeepsTheCaller(t *testing.T) {
	db := newFakeConfigStore()
	store := NewPerUserStore(db, nil)
	require.Nil(t, store.GetDiscovery(userContext("alice"), notesClient()))
	store.EvictDiscovery(schemas.MCPAuthModeUser, "alice", "client-notes")
	h := newHold()
	db.set(func(f *fakeConfigStore) { f.readHold = h })

	done := make(chan *schemas.MCPDiscovery, 1)
	go func() { done <- store.GetDiscovery(userContext("alice"), notesClient()) }()
	<-h.reached
	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	close(h.release)
	require.Nil(t, <-done)

	assert.NotNil(t, store.GetDiscovery(userContext("alice"), notesClient()), "a write landing after the read found no row keeps the caller in the set")
}

func TestEvictDiscoveriesByVirtualKey_ADeletedKeyLeavesTheSetOnItsNextRead(t *testing.T) {
	db := newFakeConfigStore()
	store := NewPerUserStore(db, nil)
	require.NoError(t, store.SetDiscovery(vkContext("vk-1"), notesClient(), notesDiscovery("search")))
	require.NotNil(t, store.GetDiscovery(vkContext("vk-1"), notesClient()))

	// Deleting the key removes its rows without going through the store.
	require.NoError(t, db.DeleteMCPPerUserDiscovery(context.Background(), schemas.MCPAuthModeVK, "vk-1", "client-notes"))
	store.EvictDiscoveriesByVirtualKey("vk-1")

	assert.Nil(t, store.GetDiscovery(vkContext("vk-1"), notesClient()))
	before := db.readCount()
	assert.Nil(t, store.GetDiscovery(vkContext("vk-1"), notesClient()))
	assert.Equal(t, before, db.readCount(), "the read that found the row gone took the key out of the set")
}

func TestEvictDiscoveriesByMCPClient_ReloadsTheClientsSet(t *testing.T) {
	db := newFakeConfigStore()
	store := NewPerUserStore(db, nil)
	require.Nil(t, store.GetDiscovery(userContext("alice"), notesClient()))

	store.EvictDiscoveriesByMCPClient("client-notes")
	require.Nil(t, store.GetDiscovery(userContext("alice"), notesClient()))
	assert.Equal(t, 2, db.bindingReadCount())
}

func TestEvictDiscoveries_DropOnlyTheMatchingBindings(t *testing.T) {
	other := notesClient()
	other.ID = "client-other"
	bindings := []struct {
		mode     schemas.MCPAuthMode
		identity string
		client   *schemas.MCPClientConfig
	}{
		{schemas.MCPAuthModeUser, "alice", notesClient()},
		{schemas.MCPAuthModeVK, "vk-1", notesClient()},
		{schemas.MCPAuthModeUser, "alice", other},
	}
	// Bindings 0 and 1 hold the same discovery on one client, so after an eviction that drops their
	// copy, the first of them to read it again makes the copy the second is served.
	cases := []struct {
		name  string
		evict func(*PerUserStore)
		reads []bool // per binding, in order: read its row again after the eviction
	}{
		{name: "by client", evict: func(s *PerUserStore) { s.EvictDiscoveriesByMCPClient("client-notes") }, reads: []bool{true, false, false}},
		{name: "by virtual key", evict: func(s *PerUserStore) { s.EvictDiscoveriesByVirtualKey("vk-1") }, reads: []bool{false, true, false}},
		{name: "by user", evict: func(s *PerUserStore) { s.EvictDiscoveriesByUser("alice") }, reads: []bool{true, false, true}},
		{name: "one binding", evict: func(s *PerUserStore) { s.EvictDiscovery(schemas.MCPAuthModeVK, "vk-1", "client-notes") }, reads: []bool{false, true, false}},
		{name: "flush", evict: func(s *PerUserStore) { s.FlushDiscoveryCache() }, reads: []bool{true, false, true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newFakeConfigStore()
			store := NewPerUserStore(db, nil)
			for _, b := range bindings {
				require.NoError(t, store.SetDiscovery(modeContext(b.mode, b.identity), b.client, notesDiscovery("search")))
				require.NotNil(t, store.GetDiscovery(modeContext(b.mode, b.identity), b.client))
			}
			tc.evict(store)
			for i, b := range bindings {
				before := db.readCount()
				require.NotNil(t, store.GetDiscovery(modeContext(b.mode, b.identity), b.client))
				assert.Equal(t, tc.reads[i], db.readCount() > before, "binding %d (%s %s %s)", i, b.mode, b.identity, b.client.ID)
			}
		})
	}
}

func TestSetOnDiscoveryWritten_FiresForWritesAndDeletesOnly(t *testing.T) {
	store := NewPerUserStore(newFakeConfigStore(), nil)
	var written []string
	store.SetOnDiscoveryWritten(func(_ context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) {
		written = append(written, string(mode)+"/"+identity+"/"+mcpClientID)
	})

	require.NoError(t, store.SetDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), notesClient(), notesDiscovery("search")))
	require.NoError(t, store.DeleteDiscovery(modeContext(schemas.MCPAuthModeVK, "vk-1"), notesClient()))
	store.EvictDiscovery(schemas.MCPAuthModeUser, "alice", "client-notes")

	assert.Equal(t, []string{"user/alice/client-notes", "vk/vk-1/client-notes"}, written, "applying an eviction must not announce it again")
}

func TestStore_NilIsSafe(t *testing.T) {
	var store *PerUserStore
	assert.Nil(t, store.GetDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), notesClient()))
	assert.Error(t, store.SetDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), notesClient(), notesDiscovery("search")))
	assert.Error(t, store.DeleteDiscovery(modeContext(schemas.MCPAuthModeUser, "alice"), notesClient()))
	store.EvictDiscovery(schemas.MCPAuthModeUser, "alice", "client-notes")
	store.EvictDiscoveriesByMCPClient("client-notes")
	store.EvictDiscoveriesByVirtualKey("vk-1")
	store.EvictDiscoveriesByUser("alice")
	store.FlushDiscoveryCache()
}

// A request's read of one per-user client when the caller's discovery is cached.
func BenchmarkGetDiscovery_CacheHit(b *testing.B) {
	store := NewPerUserStore(newFakeConfigStore(), nil)
	ctx := userContext("alice")
	if err := store.SetDiscovery(ctx, notesClient(), notesDiscovery("search", "create_page")); err != nil {
		b.Fatal(err)
	}
	client := notesClient()
	store.GetDiscovery(ctx, client)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = store.GetDiscovery(ctx, client)
	}
}

// A fresh process reading 1,000 callers who hold one 20-tool discovery: the rows each pass reads
// and the decoded copies its cache keeps.
func BenchmarkGetDiscovery_ThousandCallersOneDiscovery(b *testing.B) {
	db := newFakeConfigStore()
	tools := make([]string, 20)
	for i := range tools {
		tools[i] = fmt.Sprintf("tool_%d", i)
	}
	callers := make([]*schemas.BifrostContext, 1000)
	for i := range callers {
		callers[i] = userContext(fmt.Sprintf("user-%d", i))
		if err := NewPerUserStore(db, nil).SetDiscovery(callers[i], notesClient(), notesDiscovery(tools...)); err != nil {
			b.Fatal(err)
		}
	}
	client := notesClient()
	pass := func() *PerUserStore {
		store := NewPerUserStore(db, nil)
		for _, ctx := range callers {
			if store.GetDiscovery(ctx, client) == nil {
				b.Fatal("a caller was served no discovery")
			}
		}
		return store
	}

	readsBefore := db.readCount()
	var entries int
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entries = pass().cache.Len()
	}
	b.StopTimer()
	b.ReportMetric(float64(db.readCount()-readsBefore)/float64(b.N), "rows/op")
	b.ReportMetric(float64(entries), "entries")
}

// A request's read of one per-user client by a caller with no discovery on it.
func BenchmarkGetDiscovery_NotAMember(b *testing.B) {
	store := NewPerUserStore(newFakeConfigStore(), nil)
	if err := store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search", "create_page")); err != nil {
		b.Fatal(err)
	}
	ctx := userContext("bob")
	client := notesClient()
	store.GetDiscovery(ctx, client)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = store.GetDiscovery(ctx, client)
	}
}
