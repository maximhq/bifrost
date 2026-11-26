package mcpdiscovery

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeConfigStore records the shared discovery writes, so tests can see what reached the
// database, and can fail, hold or panic the next one.
type fakeConfigStore struct {
	configstore.ConfigStore
	mu          sync.Mutex
	toolWrites  []toolWrite
	toolsErr    error
	toolsFails  int // the next this many discovery writes fail, as toolsErr does
	toolsHold   *hold
	toolsPanic  bool // the next tools write panics
	lastWritten *schemas.MCPDiscovery
}

// toolWrite is one UpdateMCPClientDiscovery call that succeeded.
type toolWrite struct {
	clientID     string
	tools        []string // sorted
	instructions string
}

// hold pauses the next fake write until released, so a test can act while it is in flight.
type hold struct {
	reached chan struct{}
	release chan struct{}
}

// newHold returns a hold not reached yet.
func newHold() *hold {
	return &hold{reached: make(chan struct{}), release: make(chan struct{})}
}

// wait signals that the hold was reached and blocks until it is released; a nil hold does nothing.
func (h *hold) wait() {
	if h == nil {
		return
	}
	close(h.reached)
	<-h.release
}

// newFakeConfigStore returns a fake with nothing written.
func newFakeConfigStore() *fakeConfigStore {
	return &fakeConfigStore{}
}

// UpdateMCPClientDiscovery records a shared discovery write, failing, holding or panicking it when
// the test asked for that.
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

// writtenTools returns the writes recorded so far.
func (f *fakeConfigStore) writtenTools() []toolWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]toolWrite(nil), f.toolWrites...)
}

// set changes the fake under its lock.
func (f *fakeConfigStore) set(apply func(f *fakeConfigStore)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	apply(f)
}

func wikiClient() *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{ID: "client-wiki", Name: "wiki"}
}

// wikiDiscovery is a fresh discovery, as an upstream tools/list produces one.
func wikiDiscovery(tools ...string) *schemas.MCPDiscovery {
	discovery := &schemas.MCPDiscovery{
		Tools:        map[string]schemas.ChatTool{},
		Instructions: "Read before you write.",
		DiscoveredAt: time.Now(),
	}
	for _, tool := range tools {
		prefixed := "wiki-" + tool
		discovery.Tools[prefixed] = schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: prefixed}}
	}
	return discovery
}

// restoredWikiDiscovery is a discovery the manager restored from the row.
func restoredWikiDiscovery(tools ...string) *schemas.MCPDiscovery {
	discovery := wikiDiscovery(tools...)
	discovery.DiscoveredAt = time.Time{}
	return discovery
}

func wikiWrite(tools ...string) toolWrite {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = "wiki-" + tool
	}
	return toolWrite{clientID: "client-wiki", tools: names, instructions: "Read before you write."}
}

// settle waits for clientID's background writer, if any, to finish.
func settle(t *testing.T, s *SharedStore, clientID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		w := s.writes[clientID]
		return w == nil || !w.running
	}, 5*time.Second, time.Millisecond)
}

func TestSharedStore_ServesFromMemoryAndWritesTheRowInTheBackground(t *testing.T) {
	db := newFakeConfigStore()
	store := newTestSharedStore(t, db)

	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	assert.Contains(t, store.GetDiscovery(userContext("alice"), wikiClient()).GetTools(), "wiki-read", "every caller is served the client's one discovery")
	settle(t, store, "client-wiki")

	assert.Equal(t, []toolWrite{wikiWrite("read")}, db.writtenTools())
}

func TestSharedStore_DoesNotWriteARestoredDiscovery(t *testing.T) {
	db := newFakeConfigStore()
	store := newTestSharedStore(t, db)

	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), restoredWikiDiscovery("read")))
	settle(t, store, "client-wiki")
	assert.Empty(t, db.writtenTools(), "a restored discovery is what the row already holds")
	assert.Contains(t, store.GetDiscovery(context.Background(), wikiClient()).GetTools(), "wiki-read")

	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	settle(t, store, "client-wiki")
	assert.Empty(t, db.writtenTools(), "nor is a rediscovery that matches it")

	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read", "edit")))
	settle(t, store, "client-wiki")
	assert.Equal(t, []toolWrite{wikiWrite("edit", "read")}, db.writtenTools())
}

func TestSharedStore_DoesNotWriteAnUnchangedRediscovery(t *testing.T) {
	db := newFakeConfigStore()
	store := newTestSharedStore(t, db)

	for _, tools := range [][]string{{"read"}, {"read"}, {"read", "edit"}, {"read", "edit"}} {
		require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery(tools...)))
		settle(t, store, "client-wiki")
	}
	assert.Equal(t, []toolWrite{wikiWrite("read"), wikiWrite("edit", "read")}, db.writtenTools())
}

func TestSharedStore_SetNeverWaitsAndTheLatestDiscoveryWins(t *testing.T) {
	db := newFakeConfigStore()
	store := newTestSharedStore(t, db)
	h := newHold()
	db.set(func(f *fakeConfigStore) { f.toolsHold = h })

	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	<-h.reached // the writer is waiting on the database
	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read", "edit")))
	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read", "edit", "delete")))
	assert.Contains(t, store.GetDiscovery(context.Background(), wikiClient()).GetTools(), "wiki-delete", "memory has the latest while the write is in flight")
	close(h.release)
	settle(t, store, "client-wiki")

	assert.Equal(t, []toolWrite{wikiWrite("read"), wikiWrite("delete", "edit", "read")}, db.writtenTools(), "a discovery replaced while it waited is never written")
}

func TestSharedStore_ARestoreDropsAPendingWrite(t *testing.T) {
	db := newFakeConfigStore()
	store := newTestSharedStore(t, db)
	h := newHold()
	db.set(func(f *fakeConfigStore) { f.toolsHold = h })

	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	<-h.reached
	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read", "edit")))
	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), restoredWikiDiscovery("read", "admin")))
	close(h.release)
	settle(t, store, "client-wiki")
	assert.Equal(t, []toolWrite{wikiWrite("read")}, db.writtenTools(), "the row was rewritten with what was restored; the older pending discovery must not overwrite it")

	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read", "admin")))
	settle(t, store, "client-wiki")
	assert.Len(t, db.writtenTools(), 1, "the row holds the restored discovery, not the write that was in flight")
}

func TestSharedStore_AWriteOvertakenByARestoreIsDropped(t *testing.T) {
	db := newFakeConfigStore()
	store := newTestSharedStore(t, db)
	h := newHold()
	var asked atomic.Bool
	store.SetWriteGate(func() bool {
		if asked.CompareAndSwap(false, true) {
			h.wait() // the writer has taken the discovery and not yet written it
		}
		return true
	})

	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read", "edit")))
	<-h.reached
	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), restoredWikiDiscovery("read")))
	close(h.release)
	settle(t, store, "client-wiki")

	assert.Empty(t, db.writtenTools(), "the row now holds what was restored, which the older discovery must not overwrite")
}

// A failed write is retried, backing off from a second, until it lands: nothing else would ask
// again before the client's next sync.
func TestSharedStore_RetriesAFailedWriteUntilItLands(t *testing.T) {
	db := newFakeConfigStore()
	store := newTestSharedStore(t, db)

	db.set(func(f *fakeConfigStore) { f.toolsFails = 2 })
	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	require.Eventually(t, func() bool { return len(db.writtenTools()) == 1 }, 10*time.Second, 10*time.Millisecond, "the write must be retried without another discovery")
	settle(t, store, "client-wiki")
	assert.Equal(t, []toolWrite{wikiWrite("read")}, db.writtenTools())

	// What was written counts as written: an unchanged rediscovery writes nothing more.
	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	settle(t, store, "client-wiki")
	assert.Len(t, db.writtenTools(), 1)
}

// A client deleted while its write keeps failing stops being retried: it has nothing left to save.
func TestSharedStore_StopsRetryingADeletedClient(t *testing.T) {
	db := newFakeConfigStore()
	store := newTestSharedStore(t, db)

	db.set(func(f *fakeConfigStore) { f.toolsErr = errors.New("database unavailable") })
	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	require.NoError(t, store.DeleteDiscovery(context.Background(), wikiClient()))
	settle(t, store, "client-wiki")
	assert.Empty(t, db.writtenTools())
}

// The row is written with everything the upstream answered: tool annotations and metadata,
// resources, resource templates and prompts. A change to only those is still a change.
func TestSharedStore_WritesTheWholeDiscovery(t *testing.T) {
	db := newFakeConfigStore()
	store := newTestSharedStore(t, db)

	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	settle(t, store, "client-wiki")

	changed := wikiDiscovery("read")
	read := changed.Tools["wiki-read"]
	read.Annotations = &schemas.MCPToolAnnotations{Title: "Read"}
	changed.Tools["wiki-read"] = read
	for _, change := range []struct {
		name  string
		apply func(d *schemas.MCPDiscovery)
	}{
		{"tool details", func(*schemas.MCPDiscovery) {}},
		{"resources", func(d *schemas.MCPDiscovery) {
			d.Resources = []mcpgo.Resource{mcpgo.NewResource("file:///handbook.md", "handbook")}
		}},
		{"resource templates", func(d *schemas.MCPDiscovery) {
			d.ResourceTemplates = []mcpgo.ResourceTemplate{mcpgo.NewResourceTemplate("repo://{name}", "repo")}
		}},
		{"prompts", func(d *schemas.MCPDiscovery) { d.Prompts = []mcpgo.Prompt{mcpgo.NewPrompt("wiki-review")} }},
	} {
		change.apply(changed)
		before := len(db.writtenTools())
		next := *changed
		next.DiscoveredAt = time.Now()
		require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), &next))
		settle(t, store, "client-wiki")
		require.Len(t, db.writtenTools(), before+1, "a change to only the %s must be written", change.name)
	}
	db.mu.Lock()
	written := db.lastWritten
	db.mu.Unlock()
	assert.Equal(t, "Read", written.Tools["wiki-read"].Annotations.Title)
	assert.Len(t, written.Resources, 1)
	assert.Len(t, written.ResourceTemplates, 1)
	assert.Len(t, written.Prompts, 1)
}

func TestSharedStore_APanickingWriteDoesNotStopLaterOnes(t *testing.T) {
	db := newFakeConfigStore()
	store := newTestSharedStore(t, db)

	db.set(func(f *fakeConfigStore) { f.toolsPanic = true })
	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	settle(t, store, "client-wiki")

	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	settle(t, store, "client-wiki")
	assert.Equal(t, []toolWrite{wikiWrite("read")}, db.writtenTools())
}

func TestSharedStore_WriteGate(t *testing.T) {
	db := newFakeConfigStore()
	store := newTestSharedStore(t, db)
	var allowed atomic.Bool
	store.SetWriteGate(allowed.Load)

	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	settle(t, store, "client-wiki")
	require.Empty(t, db.writtenTools(), "a refused write is skipped")
	assert.Contains(t, store.GetDiscovery(context.Background(), wikiClient()).GetTools(), "wiki-read", "and memory is unaffected")

	allowed.Store(true)
	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	settle(t, store, "client-wiki")
	assert.Equal(t, []toolWrite{wikiWrite("read")}, db.writtenTools(), "the next discovery writes once allowed, even an unchanged one")
}

func TestSharedStore_DeleteForgetsTheClientAndLeavesTheRow(t *testing.T) {
	db := newFakeConfigStore()
	store := newTestSharedStore(t, db)
	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	settle(t, store, "client-wiki")

	require.NoError(t, store.DeleteDiscovery(context.Background(), wikiClient()))
	assert.Nil(t, store.GetDiscovery(context.Background(), wikiClient()))
	assert.Len(t, db.writtenTools(), 1, "deleting writes nothing")
	store.mu.RLock()
	defer store.mu.RUnlock()
	assert.NotContains(t, store.writes, "client-wiki", "nothing is kept for a forgotten client")
}

func TestSharedStore_WithoutAConfigStoreKeepsMemoryOnly(t *testing.T) {
	store := newTestSharedStore(t, nil)
	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	assert.Contains(t, store.GetDiscovery(context.Background(), wikiClient()).GetTools(), "wiki-read")
	assert.Empty(t, store.writes)
}

func TestSharedStore_NilIsSafe(t *testing.T) {
	var store *SharedStore
	assert.Nil(t, store.GetDiscovery(context.Background(), wikiClient()))
	assert.Error(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	assert.NoError(t, store.DeleteDiscovery(context.Background(), wikiClient()))
	store.SetWriteGate(func() bool { return true })
}

// A request's read of one shared client.
func BenchmarkSharedStore_GetDiscovery(b *testing.B) {
	store := newTestSharedStore(b, newFakeConfigStore())
	client := wikiClient()
	if err := store.SetDiscovery(context.Background(), client, restoredWikiDiscovery("read", "edit")); err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = store.GetDiscovery(ctx, client)
	}
}

// The manager's write, under its lock, on a periodic tick that rediscovered
// the same tools: in memory only, and with the row written in the background.
func BenchmarkSharedStore_SetDiscovery(b *testing.B) {
	for _, bc := range []struct {
		name string
		db   *fakeConfigStore
	}{{"memory only", nil}, {"persisted", newFakeConfigStore()}} {
		b.Run(bc.name, func(b *testing.B) {
			var store *SharedStore
			if bc.db == nil {
				store = newTestSharedStore(b, nil)
			} else {
				store = newTestSharedStore(b, bc.db)
			}
			client := wikiClient()
			discovery := wikiDiscovery("read", "edit")
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = store.SetDiscovery(ctx, client, discovery)
			}
		})
	}
}

// newTestSharedStore returns a SharedStore writing through configStore, with its discoveries in a
// state table of a new live store.
func newTestSharedStore(tb testing.TB, configStore configstore.ConfigStore) *SharedStore {
	tb.Helper()
	live, err := livestore.New()
	require.NoError(tb, err)
	tb.Cleanup(live.Close)
	store, err := NewSharedStore(configStore, live, nil)
	require.NoError(tb, err)
	return store
}

// rowsConfigStore is a fakeConfigStore whose MCP client rows are clients.
type rowsConfigStore struct {
	*fakeConfigStore
	clients   []*schemas.MCPClientConfig
	byIDReads atomic.Int32
}

// GetMCPClientConfigs returns the fake's client rows.
func (r *rowsConfigStore) GetMCPClientConfigs(context.Context) ([]*schemas.MCPClientConfig, error) {
	return r.clients, nil
}

// GetMCPClientConfigByID returns the fake's client row with this ID, and counts the read.
func (r *rowsConfigStore) GetMCPClientConfigByID(_ context.Context, id string) (*schemas.MCPClientConfig, error) {
	r.byIDReads.Add(1)
	for _, client := range r.clients {
		if client.ID == id {
			return client, nil
		}
	}
	return nil, configstore.ErrNotFound
}

// TestSharedStore_LoadServesTheRowsDiscoveries: a restart serves the discoveries the clients' rows
// hold before any client is dialed. A client with per_user_discovery on, or with nothing
// discovered, is left out, and nothing loaded is written back to its row.
func TestSharedStore_LoadServesTheRowsDiscoveries(t *testing.T) {
	ctx := context.Background()
	wiki := wikiDiscovery("search")
	db := &rowsConfigStore{fakeConfigStore: newFakeConfigStore(), clients: []*schemas.MCPClientConfig{
		{ID: "client-wiki", Name: "wiki", DiscoveredTools: wiki.Tools, DiscoveredInstructions: "Read before you write."},
		{ID: "client-notes", Name: "notes", PerUserDiscovery: schemas.Ptr(true), DiscoveredTools: wikiDiscovery("export").Tools},
		{ID: "client-empty", Name: "empty"},
	}}
	store := newTestSharedStore(t, db)
	require.NoError(t, store.Load(ctx))

	loaded := store.GetDiscovery(ctx, &schemas.MCPClientConfig{ID: "client-wiki"})
	require.NotNil(t, loaded)
	assert.Equal(t, wiki.Tools, loaded.Tools)
	assert.Equal(t, "Read before you write.", loaded.Instructions)
	assert.True(t, loaded.DiscoveredAt.IsZero(), "restored, not freshly discovered")
	assert.Nil(t, store.GetDiscovery(ctx, &schemas.MCPClientConfig{ID: "client-notes"}), "a per-caller client keeps no shared discovery")
	assert.Nil(t, store.GetDiscovery(ctx, &schemas.MCPClientConfig{ID: "client-empty"}))
	assert.Empty(t, db.writtenTools(), "what the rows hold is not written back")
}

// TestSharedStore_LoadClientServesTheRowWhenTheStoreHasNone: a client connected while Bifrost runs
// is served the discovery its row holds, which is not written back. A discovery the store already
// holds is as new as the row or newer, so it is kept and the row is not read. A client with
// per_user_discovery on, or with no row, is left out.
func TestSharedStore_LoadClientServesTheRowWhenTheStoreHasNone(t *testing.T) {
	ctx := context.Background()
	wiki := wikiDiscovery("search")
	db := &rowsConfigStore{fakeConfigStore: newFakeConfigStore(), clients: []*schemas.MCPClientConfig{
		{ID: "client-wiki", Name: "wiki", DiscoveredTools: wiki.Tools, DiscoveredInstructions: "Read before you write."},
		{ID: "client-notes", Name: "notes", PerUserDiscovery: schemas.Ptr(true), DiscoveredTools: wikiDiscovery("export").Tools},
	}}
	store := newTestSharedStore(t, db)

	require.NoError(t, store.LoadClient(ctx, "client-wiki"))
	loaded := store.GetDiscovery(ctx, wikiClient())
	require.NotNil(t, loaded)
	assert.Equal(t, wiki.Tools, loaded.Tools)
	assert.Equal(t, "Read before you write.", loaded.Instructions)
	assert.True(t, loaded.DiscoveredAt.IsZero(), "restored, not freshly discovered")
	settle(t, store, "client-wiki")
	assert.Empty(t, db.writtenTools(), "what the row holds is not written back")

	require.NoError(t, store.SetDiscovery(ctx, wikiClient(), wikiDiscovery("search", "edit")))
	reads := db.byIDReads.Load()
	require.NoError(t, store.LoadClient(ctx, "client-wiki"))
	assert.Contains(t, store.GetDiscovery(ctx, wikiClient()).GetTools(), "wiki-edit", "the newer discovery the store holds is kept")
	assert.Equal(t, reads, db.byIDReads.Load(), "the row is not read while the store holds a discovery")

	require.NoError(t, store.LoadClient(ctx, "client-notes"))
	assert.Nil(t, store.GetDiscovery(ctx, &schemas.MCPClientConfig{ID: "client-notes"}), "a per-caller client keeps no shared discovery")
	require.NoError(t, store.LoadClient(ctx, "client-gone"), "a client with no row is not an error")
	assert.Nil(t, store.GetDiscovery(ctx, &schemas.MCPClientConfig{ID: "client-gone"}))

	var unset *SharedStore
	assert.NoError(t, unset.LoadClient(ctx, "client-wiki"), "a nil store loads nothing")
}
