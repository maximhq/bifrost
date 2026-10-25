package mcpdiscovery

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func wikiClient() *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{ID: "client-wiki", Name: "wiki"}
}

// wikiDiscovery is a fresh discovery, as an upstream tools/list produces one.
func wikiDiscovery(tools ...string) *schemas.MCPDiscovery {
	discovery := &schemas.MCPDiscovery{
		Tools:           map[string]schemas.ChatTool{},
		ToolNameMapping: map[string]string{},
		Instructions:    "Read before you write.",
		DiscoveredAt:    time.Now(),
	}
	for _, tool := range tools {
		prefixed := "wiki-" + tool
		discovery.Tools[prefixed] = schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: prefixed}}
		discovery.ToolNameMapping[tool] = tool
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
	store := NewSharedStore(db, nil)

	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	assert.Contains(t, store.GetDiscovery(userContext("alice"), wikiClient()).GetTools(), "wiki-read", "every caller is served the client's one discovery")
	settle(t, store, "client-wiki")

	assert.Equal(t, []toolWrite{wikiWrite("read")}, db.writtenTools())
}

func TestSharedStore_DoesNotWriteARestoredDiscovery(t *testing.T) {
	db := newFakeConfigStore()
	store := NewSharedStore(db, nil)

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
	store := NewSharedStore(db, nil)

	for _, tools := range [][]string{{"read"}, {"read"}, {"read", "edit"}, {"read", "edit"}} {
		require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery(tools...)))
		settle(t, store, "client-wiki")
	}
	assert.Equal(t, []toolWrite{wikiWrite("read"), wikiWrite("edit", "read")}, db.writtenTools())
}

func TestSharedStore_SetNeverWaitsAndTheLatestDiscoveryWins(t *testing.T) {
	db := newFakeConfigStore()
	store := NewSharedStore(db, nil)
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
	store := NewSharedStore(db, nil)
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
	store := NewSharedStore(db, nil)
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
	store := NewSharedStore(db, nil)

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
	store := NewSharedStore(db, nil)

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
	store := NewSharedStore(db, nil)

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
	store := NewSharedStore(db, nil)

	db.set(func(f *fakeConfigStore) { f.toolsPanic = true })
	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	settle(t, store, "client-wiki")

	require.NoError(t, store.SetDiscovery(context.Background(), wikiClient(), wikiDiscovery("read")))
	settle(t, store, "client-wiki")
	assert.Equal(t, []toolWrite{wikiWrite("read")}, db.writtenTools())
}

func TestSharedStore_WriteGate(t *testing.T) {
	db := newFakeConfigStore()
	store := NewSharedStore(db, nil)
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
	store := NewSharedStore(db, nil)
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
	store := NewSharedStore(nil, nil)
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
	store := NewSharedStore(newFakeConfigStore(), nil)
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
				store = NewSharedStore(nil, nil)
			} else {
				store = NewSharedStore(bc.db, nil)
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
