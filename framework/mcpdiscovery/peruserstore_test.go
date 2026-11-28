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
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// testDB is one config database that several stores share, as several processes do. It counts
// the reads of the per-user discovery table and can fail them.
type testDB struct {
	store configstore.ConfigStore
	reads atomic.Int64
	fail  atomic.Bool
	// failAfterWrite turns fail on as the next write of the table lands, so the reads after a
	// committed write fail while the write itself succeeds.
	failAfterWrite atomic.Bool
}

// newTestDB returns a SQLite config database holding the notes and docs MCP clients.
func newTestDB(t testing.TB) *testDB {
	t.Helper()
	ctx := context.Background()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: t.TempDir() + "/config.db"},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })
	db := &testDB{store: store}
	require.NoError(t, store.DB().Callback().Query().Before("gorm:query").Register("test:per_user_discovery_reads", func(tx *gorm.DB) {
		if tx.Statement.Table != (tables.TableMCPPerUserDiscovery{}).TableName() {
			return
		}
		db.reads.Add(1)
		if db.fail.Load() {
			_ = tx.AddError(errors.New("database unreachable"))
		}
	}))
	afterWrite := func(tx *gorm.DB) {
		if tx.Statement.Table == (tables.TableMCPPerUserDiscovery{}).TableName() && db.failAfterWrite.Swap(false) {
			db.fail.Store(true)
		}
	}
	require.NoError(t, store.DB().Callback().Create().After("gorm:create").Register("test:fail_after_create", afterWrite))
	require.NoError(t, store.DB().Callback().Update().After("gorm:update").Register("test:fail_after_update", afterWrite))
	for _, c := range []*schemas.MCPClientConfig{notesClient(), docsClient()} {
		c.ConnectionType = schemas.MCPConnectionTypeHTTP
		c.ConnectionString = schemas.NewSecretVar("http://upstream.invalid/mcp")
		require.NoError(t, store.CreateMCPClientConfig(ctx, c))
	}
	return db
}

// process starts a process over db: a live store of its own, loaded from the database as a
// process loads it at boot, and its per-user store.
func (db *testDB) process(t testing.TB) *PerUserStore {
	t.Helper()
	live, err := livestore.New()
	require.NoError(t, err)
	t.Cleanup(live.Close)
	s, err := NewPerUserStore(db.store, live, nil)
	require.NoError(t, err)
	require.NoError(t, live.LoadAll(context.Background(), db.store.DB()))
	return s
}

// row reads a caller's stored row, or nil when there is none.
func (db *testDB) row(t testing.TB, mode schemas.MCPAuthMode, identity, mcpClientID string) *tables.TableMCPPerUserDiscovery {
	t.Helper()
	row, err := db.store.GetMCPPerUserDiscoveryByMode(context.Background(), mode, identity, mcpClientID)
	require.NoError(t, err)
	return row
}

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

func docsClient() *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{ID: "client-docs", Name: "docs", PerUserDiscovery: schemas.Ptr(true)}
}

// notesDiscovery is a discovery of the notes client, shaped the way core
// produces one: tools keyed and named with the client prefix.
func notesDiscovery(tools ...string) *schemas.MCPDiscovery {
	return discoveryFor(notesClient(), tools...)
}

// discoveryFor is a discovery of client, its tools keyed and named with the
// client's prefix.
func discoveryFor(client *schemas.MCPClientConfig, tools ...string) *schemas.MCPDiscovery {
	discovery := &schemas.MCPDiscovery{
		Tools:        map[string]schemas.ChatTool{},
		Instructions: "Search before you write.",
		DiscoveredAt: time.Now().UTC(),
	}
	for _, tool := range tools {
		prefixed := client.Name + "-" + tool
		discovery.Tools[prefixed] = schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: prefixed}}
	}
	return discovery
}

// sortedToolNames lists a discovery's tool names in order.
func sortedToolNames(d *schemas.MCPDiscovery) []string {
	names := make([]string, 0, len(d.Tools))
	for name := range d.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// held returns the binding a store holds for a caller on a client.
func held(s *PerUserStore, mode schemas.MCPAuthMode, identity, mcpClientID string) (*registry.MCPUserDiscovery, bool) {
	return s.live.Collections.MCPUserDiscoveries.Get(registry.MCPUserDiscoveryKey(string(mode), identity, mcpClientID))
}

func TestSetDiscovery_StoresBareNamesAndServesThemPrefixed(t *testing.T) {
	db := newTestDB(t)
	store := db.process(t)

	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search", "create_page")))

	row := db.row(t, schemas.MCPAuthModeUser, "alice", "client-notes")
	require.NotNil(t, row)
	require.Contains(t, row.Tools, "search", "rows keep the upstream name without the client prefix")
	assert.Equal(t, "search", row.Tools["search"].Function.Name)
	assert.NotEmpty(t, row.DiscoveryHash)

	got := store.GetDiscovery(userContext("alice"), notesClient())
	require.NotNil(t, got)
	require.Contains(t, got.Tools, "notes-search")
	assert.Equal(t, "notes-search", got.Tools["notes-search"].Function.Name)
	assert.Equal(t, "Search before you write.", got.Instructions)
}

// A caller's resources, resource templates and prompts are kept with their tools. Prompt names are
// stored bare and served under the client's current name, like tool names, and the lists are part
// of the discovery's hash.
func TestSetDiscovery_KeepsResourcesAndPromptsWithTheTools(t *testing.T) {
	db := newTestDB(t)
	store := db.process(t)
	discovery := notesDiscovery("search")
	discovery.Resources = []mcpgo.Resource{mcpgo.NewResource("file:///handbook.md", "handbook")}
	discovery.ResourceTemplates = []mcpgo.ResourceTemplate{mcpgo.NewResourceTemplate("notes://{id}", "note")}
	discovery.Prompts = []mcpgo.Prompt{mcpgo.NewPrompt("notes-review")}
	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), discovery))
	require.NoError(t, store.SetDiscovery(userContext("bob"), notesClient(), notesDiscovery("search")))

	row := db.row(t, schemas.MCPAuthModeUser, "alice", "client-notes")
	require.Len(t, row.Prompts, 1)
	assert.Equal(t, "review", row.Prompts[0].Name, "rows keep the upstream prompt name without the client prefix")
	assert.NotEqual(t, db.row(t, schemas.MCPAuthModeUser, "bob", "client-notes").DiscoveryHash, row.DiscoveryHash,
		"the same tools with other resources and prompts are another discovery")
	assert.Equal(t, "notes-review", discovery.Prompts[0].Name, "stripping the prefix for storage must not rename the caller's prompts")

	renamed := notesClient()
	renamed.Name = "docs-renamed"
	got := db.process(t).GetDiscovery(userContext("alice"), renamed)
	require.NotNil(t, got, "a process booted from the database holds the discovery")
	require.Len(t, got.Prompts, 1)
	assert.Equal(t, "docs-renamed-review", got.Prompts[0].Name, "prompts are served under the client's current name")
	assert.Equal(t, discovery.Resources, got.Resources)
	want, err := json.Marshal(discovery.ResourceTemplates)
	require.NoError(t, err)
	served, err := json.Marshal(got.ResourceTemplates)
	require.NoError(t, err)
	assert.JSONEq(t, string(want), string(served), "templates compare by content: a stored one compiles its URI template again")
}

func TestSetDiscovery_LeavesTheCallersDiscoveryUntouched(t *testing.T) {
	store := newTestDB(t).process(t)
	discovery := notesDiscovery("search")

	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), discovery))
	assert.Equal(t, "notes-search", discovery.Tools["notes-search"].Function.Name, "stripping the prefix for storage must not rename the caller's tools")
}

// A discovery whose tools already carry their cached JSON is stored and hashed
// under the bare names, the same as one whose tools were never serialized.
func TestSetDiscovery_StoresBareNamesForPreSerializedTools(t *testing.T) {
	db := newTestDB(t)
	store := db.process(t)
	serialized := notesDiscovery("search")
	tool := serialized.Tools["notes-search"]
	require.NoError(t, tool.EnsureSerialized())
	serialized.Tools["notes-search"] = tool
	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), serialized))
	require.NoError(t, store.SetDiscovery(userContext("bob"), notesClient(), notesDiscovery("search")))

	row := db.row(t, schemas.MCPAuthModeUser, "alice", "client-notes")
	stored, err := json.Marshal(row.Tools["search"])
	require.NoError(t, err)
	assert.Contains(t, string(stored), `"name":"search"`, "the stored tool must not carry the prefixed name from its cached JSON")
	assert.Equal(t, db.row(t, schemas.MCPAuthModeUser, "bob", "client-notes").DiscoveryHash, row.DiscoveryHash,
		"the same tools hash the same whether or not they were serialized before")
}

// A read is answered from memory: neither a caller with a discovery nor one without reads the
// database.
func TestGetDiscovery_ReadsNothingFromTheDatabase(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.process(t).SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	store := db.process(t)

	before := db.reads.Load()
	require.NotNil(t, store.GetDiscovery(userContext("alice"), notesClient()))
	assert.Nil(t, store.GetDiscovery(userContext("bob"), notesClient()), "a caller with no binding has no discovery")
	assert.Nil(t, store.GetDiscovery(userContext("alice"), docsClient()), "nor on a client it never discovered")
	assert.Equal(t, before, db.reads.Load(), "reads come from memory")
}

func TestGetDiscovery_RenamedClientIsServedUnderTheNewName(t *testing.T) {
	store := newTestDB(t).process(t)
	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	require.NotNil(t, store.GetDiscovery(userContext("alice"), notesClient()))

	renamed := notesClient()
	renamed.Name = "handbook"
	got := store.GetDiscovery(userContext("alice"), renamed)
	require.NotNil(t, got)
	assert.Contains(t, got.Tools, "handbook-search", "a rename rewrites no row: the prefix comes from the current name")
	assert.NotContains(t, got.Tools, "notes-search")
}

func TestGetDiscovery_CallersWithTheSameDiscoveryShareOneCopy(t *testing.T) {
	db := newTestDB(t)
	callers := []*schemas.BifrostContext{userContext("alice"), userContext("bob"), vkContext("vk-1")}
	for _, ctx := range callers {
		require.NoError(t, db.process(t).SetDiscovery(ctx, notesClient(), notesDiscovery("search")))
	}
	store := db.process(t)

	first := store.GetDiscovery(callers[0], notesClient())
	require.NotNil(t, first)
	for _, ctx := range callers[1:] {
		assert.Same(t, first, store.GetDiscovery(ctx, notesClient()), "callers holding one discovery are served one copy")
	}
	alice, _ := held(store, schemas.MCPAuthModeUser, "alice", "client-notes")
	vk, _ := held(store, schemas.MCPAuthModeVK, "vk-1", "client-notes")
	assert.Same(t, alice.Discovery, vk.Discovery, "their bindings hold one stored copy")
}

func TestSetDiscovery_AChangedDiscoveryLeavesTheOldOneToItsOtherHolders(t *testing.T) {
	store := newTestDB(t).process(t)
	for _, caller := range []string{"alice", "bob", "carol"} {
		require.NoError(t, store.SetDiscovery(userContext(caller), notesClient(), notesDiscovery("search")))
	}
	shared := store.GetDiscovery(userContext("bob"), notesClient())
	require.NotNil(t, shared)
	bob, _ := held(store, schemas.MCPAuthModeUser, "bob", "client-notes")
	carol, _ := held(store, schemas.MCPAuthModeUser, "carol", "client-notes")
	require.Same(t, bob.Discovery, carol.Discovery, "bindings written apart, each re-read on its own, hold one stored copy")

	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search", "create_page")))

	assert.Same(t, shared, store.GetDiscovery(userContext("bob"), notesClient()))
	assert.Same(t, shared, store.GetDiscovery(userContext("carol"), notesClient()))
	assert.Contains(t, store.GetDiscovery(userContext("alice"), notesClient()).Tools, "notes-create_page")
	alice, _ := held(store, schemas.MCPAuthModeUser, "alice", "client-notes")
	assert.NotSame(t, bob.Discovery, alice.Discovery, "alice moved to a copy of her own")
}

func TestPerUserDiscovery_BindingsAreIsolated(t *testing.T) {
	store := newTestDB(t).process(t)
	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	require.NoError(t, store.SetDiscovery(vkContext("alice"), notesClient(), notesDiscovery("create_page")))
	require.NoError(t, store.SetDiscovery(userContext("alice"), docsClient(), discoveryFor(docsClient(), "export")))

	assert.Equal(t, []string{"notes-search"}, sortedToolNames(store.GetDiscovery(userContext("alice"), notesClient())))
	assert.Equal(t, []string{"notes-create_page"}, sortedToolNames(store.GetDiscovery(vkContext("alice"), notesClient())),
		"a user and a virtual key with one ID are two callers")
	assert.Equal(t, []string{"docs-export"}, sortedToolNames(store.GetDiscovery(userContext("alice"), docsClient())))
}

func TestPerUserDiscovery_TwoUsersThroughOneVirtualKeyEachKeepTheirOwn(t *testing.T) {
	store := newTestDB(t).process(t)
	alice, bob := userContext("alice"), userContext("bob")
	for _, ctx := range []*schemas.BifrostContext{alice, bob} {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-shared")
	}
	require.NoError(t, store.SetDiscovery(alice, notesClient(), notesDiscovery("search")))
	require.NoError(t, store.SetDiscovery(bob, notesClient(), notesDiscovery("create_page")))

	assert.Equal(t, []string{"notes-search"}, sortedToolNames(store.GetDiscovery(alice, notesClient())))
	assert.Equal(t, []string{"notes-create_page"}, sortedToolNames(store.GetDiscovery(bob, notesClient())))
}

func TestDiscovery_OnlyUserAndVirtualKeyCallersHaveOne(t *testing.T) {
	db := newTestDB(t)
	store := db.process(t)
	anonymous := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	assert.ErrorIs(t, store.SetDiscovery(anonymous, notesClient(), notesDiscovery("search")), errNoCaller)
	assert.ErrorIs(t, store.DeleteDiscovery(anonymous, notesClient()), errNoCaller)
	assert.Nil(t, store.GetDiscovery(anonymous, notesClient()))
	assert.Nil(t, store.GetDiscovery(context.Background(), notesClient()), "a plain context has no caller either")
}

func TestGetDiscovery_ReadsTheCallerThroughADerivedContext(t *testing.T) {
	store := newTestDB(t).process(t)
	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	derived, cancel := context.WithCancel(userContext("alice"))
	defer cancel()
	assert.NotNil(t, store.GetDiscovery(derived, notesClient()))
}

func TestDeleteDiscovery_RemovesTheCallerWithoutARead(t *testing.T) {
	db := newTestDB(t)
	store := db.process(t)
	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))

	before := db.reads.Load()
	require.NoError(t, store.DeleteDiscovery(userContext("alice"), notesClient()))
	assert.Equal(t, before, db.reads.Load(), "memory drops the binding without reading the database")
	assert.Nil(t, store.GetDiscovery(userContext("alice"), notesClient()))
	assert.Nil(t, db.row(t, schemas.MCPAuthModeUser, "alice", "client-notes"))
}

// A write whose re-read keeps failing reports that the discovery was saved but not reloaded, and
// memory keeps what it held before.
func TestSetDiscovery_AFailedReloadSaysSavedButNotReloaded(t *testing.T) {
	db := newTestDB(t)
	store := db.process(t)
	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	db.failAfterWrite.Store(true)
	ctx, cancel := context.WithTimeout(userContext("alice"), 200*time.Millisecond)
	defer cancel()
	err := store.SetDiscovery(schemas.NewBifrostContext(ctx, schemas.NoDeadline), notesClient(), notesDiscovery("search", "create_page"))
	db.fail.Store(false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "saved but not reloaded")
	assert.Equal(t, []string{"notes-search"}, sortedToolNames(store.GetDiscovery(userContext("alice"), notesClient())),
		"memory keeps the discovery it held")
}

func TestEvictDiscovery_AppliesAnotherProcessesWriteAndDelete(t *testing.T) {
	db := newTestDB(t)
	local, elsewhere := db.process(t), db.process(t)

	require.NoError(t, elsewhere.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	require.Nil(t, local.GetDiscovery(userContext("alice"), notesClient()), "until told, this process does not know of the other's write")
	local.EvictDiscovery(schemas.MCPAuthModeUser, "alice", "client-notes")
	require.Contains(t, local.GetDiscovery(userContext("alice"), notesClient()).Tools, "notes-search")

	require.NoError(t, elsewhere.DeleteDiscovery(userContext("alice"), notesClient()))
	local.EvictDiscovery(schemas.MCPAuthModeUser, "alice", "client-notes")
	assert.Nil(t, local.GetDiscovery(userContext("alice"), notesClient()), "the other's delete reaches this process")
}

func TestEvictDiscovery_OutOfOrderMessagesNeverHideADiscovery(t *testing.T) {
	db := newTestDB(t)
	local, deleter, writer := db.process(t), db.process(t), db.process(t)

	require.NoError(t, deleter.DeleteDiscovery(userContext("alice"), notesClient()))
	require.NoError(t, writer.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	// The writer's message arrives first and the deleter's last.
	local.EvictDiscovery(schemas.MCPAuthModeUser, "alice", "client-notes")
	local.EvictDiscovery(schemas.MCPAuthModeUser, "alice", "client-notes")

	assert.NotNil(t, local.GetDiscovery(userContext("alice"), notesClient()), "a delete delivered after a later write does not hide the discovery")
}

func TestEvictDiscoveries_ReadOnlyTheMatchingBindingsAgain(t *testing.T) {
	bindings := []struct {
		mode     schemas.MCPAuthMode
		identity string
		client   *schemas.MCPClientConfig
	}{
		{schemas.MCPAuthModeUser, "alice", notesClient()},
		{schemas.MCPAuthModeVK, "vk-1", notesClient()},
		{schemas.MCPAuthModeUser, "alice", docsClient()},
	}
	// Every row is deleted on another process; an eviction re-reads only what it names, so those
	// bindings go and the others are still served.
	cases := []struct {
		name  string
		evict func(*PerUserStore)
		gone  []bool
	}{
		{name: "by client", evict: func(s *PerUserStore) { s.EvictDiscoveriesByMCPClient("client-notes") }, gone: []bool{true, true, false}},
		{name: "by virtual key", evict: func(s *PerUserStore) { s.EvictDiscoveriesByVirtualKey("vk-1") }, gone: []bool{false, true, false}},
		{name: "by user", evict: func(s *PerUserStore) { s.EvictDiscoveriesByUser("alice") }, gone: []bool{true, false, true}},
		{name: "one binding", evict: func(s *PerUserStore) { s.EvictDiscovery(schemas.MCPAuthModeVK, "vk-1", "client-notes") }, gone: []bool{false, true, false}},
		{name: "flush", evict: func(s *PerUserStore) { s.FlushDiscoveryCache() }, gone: []bool{true, true, true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			local, elsewhere := db.process(t), db.process(t)
			for _, b := range bindings {
				require.NoError(t, local.SetDiscovery(modeContext(b.mode, b.identity), b.client, notesDiscovery("search")))
				elsewhere.EvictDiscovery(b.mode, b.identity, b.client.ID)
			}
			for _, b := range bindings {
				require.NoError(t, elsewhere.DeleteDiscovery(modeContext(b.mode, b.identity), b.client))
			}
			tc.evict(local)
			for i, b := range bindings {
				got := local.GetDiscovery(modeContext(b.mode, b.identity), b.client)
				assert.Equal(t, tc.gone[i], got == nil, "binding %d (%s %s %s)", i, b.mode, b.identity, b.client.ID)
			}
		})
	}
}

// A flush reads every binding again by its own key: it finds one another process wrote that this
// process was never told of, drops one whose row is gone, and keeps the rest.
func TestFlushDiscoveryCache_ReadsEveryBindingAgain(t *testing.T) {
	db := newTestDB(t)
	local, elsewhere := db.process(t), db.process(t)
	require.NoError(t, local.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	require.NoError(t, local.SetDiscovery(userContext("bob"), notesClient(), notesDiscovery("search")))
	elsewhere.EvictDiscovery(schemas.MCPAuthModeUser, "bob", "client-notes")
	require.NoError(t, elsewhere.SetDiscovery(userContext("carol"), notesClient(), notesDiscovery("create_page")))
	require.NoError(t, elsewhere.DeleteDiscovery(userContext("bob"), notesClient()))
	require.Nil(t, local.GetDiscovery(userContext("carol"), notesClient()), "until told, this process does not know of carol")

	local.FlushDiscoveryCache()
	assert.NotNil(t, local.GetDiscovery(userContext("alice"), notesClient()), "an unchanged binding stays")
	assert.Nil(t, local.GetDiscovery(userContext("bob"), notesClient()), "a binding whose row is gone is dropped")
	assert.Contains(t, local.GetDiscovery(userContext("carol"), notesClient()).Tools, "notes-create_page", "a binding written elsewhere is found")
}

// A client's removal removes its bindings with it, without reading them.
func TestRemovingAClientRemovesItsBindings(t *testing.T) {
	db := newTestDB(t)
	store := db.process(t)
	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	require.NoError(t, store.SetDiscovery(userContext("alice"), docsClient(), discoveryFor(docsClient(), "export")))

	require.NoError(t, store.live.Refresh(context.Background(), []livestore.Ref{{Kind: livestore.KindMCPClient, ID: "client-notes", Remove: true}}))
	assert.Nil(t, store.GetDiscovery(userContext("alice"), notesClient()))
	assert.NotNil(t, store.GetDiscovery(userContext("alice"), docsClient()), "another client's bindings stay")
}

func TestSetOnDiscoveryWritten_FiresForWritesAndDeletesOnly(t *testing.T) {
	store := newTestDB(t).process(t)
	var mu sync.Mutex
	var fired []string
	store.SetOnDiscoveryWritten(func(_ context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) {
		mu.Lock()
		defer mu.Unlock()
		fired = append(fired, fmt.Sprintf("%s/%s/%s", mode, identity, mcpClientID))
	})
	require.NoError(t, store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search")))
	require.NoError(t, store.DeleteDiscovery(userContext("alice"), notesClient()))
	store.EvictDiscovery(schemas.MCPAuthModeUser, "alice", "client-notes")
	assert.Equal(t, []string{"user/alice/client-notes", "user/alice/client-notes"}, fired, "an eviction applies another process's change and announces nothing")
}

func TestStore_NilIsSafe(t *testing.T) {
	var store *PerUserStore
	ctx := userContext("alice")
	assert.Nil(t, store.GetDiscovery(ctx, notesClient()))
	assert.Error(t, store.SetDiscovery(ctx, notesClient(), notesDiscovery("search")))
	assert.Error(t, store.DeleteDiscovery(ctx, notesClient()))
	store.EvictDiscovery(schemas.MCPAuthModeUser, "alice", "client-notes")
	store.EvictDiscoveriesByMCPClient("client-notes")
	store.EvictDiscoveriesByVirtualKey("vk-1")
	store.EvictDiscoveriesByUser("alice")
	store.FlushDiscoveryCache()
	store.SetOnDiscoveryWritten(nil)
}

func TestPerUserStore_ManyCallersInParallelNeverReadAnothersDiscovery(t *testing.T) {
	db := newTestDB(t)
	store := db.process(t)
	const callers, rounds = 12, 40
	type caller struct {
		mode     schemas.MCPAuthMode
		identity string
		tool     string
	}
	all := make([]caller, callers)
	for i := range all {
		mode := schemas.MCPAuthModeUser
		if i%3 == 0 {
			mode = schemas.MCPAuthModeVK
		}
		tool := "shared"
		if i%2 == 1 {
			tool = fmt.Sprintf("only_%d", i)
		}
		all[i] = caller{mode: mode, identity: fmt.Sprintf("caller-%d", i), tool: tool}
	}
	var wg sync.WaitGroup
	var failures sync.Map
	for _, c := range all {
		wg.Add(1)
		go func(c caller) {
			defer wg.Done()
			ctx := modeContext(c.mode, c.identity)
			want := []string{"notes-" + c.tool}
			for r := 0; r < rounds; r++ {
				switch r % 5 {
				case 0:
					if err := store.SetDiscovery(ctx, notesClient(), notesDiscovery(c.tool)); err != nil {
						failures.Store(c.identity+": write", err.Error())
					}
				case 3:
					store.EvictDiscovery(c.mode, c.identity, "client-notes")
				default:
					if got := store.GetDiscovery(ctx, notesClient()); got != nil && !reflect.DeepEqual(sortedToolNames(got), want) {
						failures.Store(fmt.Sprintf("%s: round %d", c.identity, r), sortedToolNames(got))
					}
				}
			}
		}(c)
	}
	wg.Wait()
	failures.Range(func(k, v any) bool {
		t.Errorf("%v read %v", k, v)
		return true
	})
	for _, c := range all {
		got := store.GetDiscovery(modeContext(c.mode, c.identity), notesClient())
		require.NotNil(t, got, c.identity)
		assert.Equal(t, []string{"notes-" + c.tool}, sortedToolNames(got), c.identity)
	}
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
		"Instructions": func(d *schemas.MCPDiscovery) { d.Instructions = "Write before you search." },
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

// BenchmarkGetDiscovery_Hit measures a caller's read of its discovery, served from memory.
func BenchmarkGetDiscovery_Hit(b *testing.B) {
	db := newTestDB(b)
	store := db.process(b)
	if err := store.SetDiscovery(userContext("alice"), notesClient(), notesDiscovery("search", "create_page")); err != nil {
		b.Fatal(err)
	}
	ctx, client := userContext("alice"), notesClient()
	b.ReportAllocs()
	for b.Loop() {
		if store.GetDiscovery(ctx, client) == nil {
			b.Fatal("no discovery")
		}
	}
}

// BenchmarkGetDiscovery_NotAMember measures the read of a caller with no binding.
func BenchmarkGetDiscovery_NotAMember(b *testing.B) {
	store := newTestDB(b).process(b)
	ctx, client := userContext("nobody"), notesClient()
	b.ReportAllocs()
	for b.Loop() {
		if store.GetDiscovery(ctx, client) != nil {
			b.Fatal("a discovery for no binding")
		}
	}
}

// BenchmarkBoot_ThousandCallersOneDiscovery measures a process loading a thousand callers who hold
// one discovery, and reports how many distinct copies it keeps.
func BenchmarkBoot_ThousandCallersOneDiscovery(b *testing.B) {
	db := newTestDB(b)
	writer := db.process(b)
	for i := range 1000 {
		if err := writer.SetDiscovery(userContext(fmt.Sprint("user-", i)), notesClient(), notesDiscovery("search")); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	var copies int
	for b.Loop() {
		// Each pass boots a store of its own and closes it before the next, so passes do not
		// pile up stores until the benchmark ends.
		live, err := livestore.New()
		if err != nil {
			b.Fatal(err)
		}
		if err := live.LoadAll(context.Background(), db.store.DB()); err != nil {
			b.Fatal(err)
		}
		seen := map[*registry.MCPUserDiscoveryContent]struct{}{}
		live.Collections.MCPUserDiscoveries.Range(func(d *registry.MCPUserDiscovery) bool {
			seen[d.Discovery] = struct{}{}
			return true
		})
		copies = len(seen)
		live.Close()
	}
	b.ReportMetric(float64(copies), "copies")
}
