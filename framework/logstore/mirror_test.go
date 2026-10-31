package logstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockingObjectStore holds every Put until release is closed, so tests can
// fill the mirror's queues.
type blockingObjectStore struct {
	*objectstore.InMemoryObjectStore
	release chan struct{}
}

// Put waits for release before storing.
func (b *blockingObjectStore) Put(ctx context.Context, key string, data []byte, tags map[string]string) error {
	<-b.release
	return b.InMemoryObjectStore.Put(ctx, key, data, tags)
}

// testMirrorRoute builds a route over an in-memory store.
func testMirrorRoute(name string, match ObjectStorageRouteMatch, store objectstore.ObjectStore, format string) *mirrorRoute {
	return newMirrorRoute(name, match, store, &objectstore.Config{Type: objectstore.StoreTypeS3, Prefix: name, Format: format})
}

// blockedLog returns a chat log a guardrail blocked at input.
func blockedLog(id string, at time.Time) *Log {
	return &Log{
		ID: id, Timestamp: at, Object: "chat.completion", Provider: "openai", Model: "gpt-4o", Status: "error",
		InputHistory: `[{"role":"user","content":"my ssn is 123-45-6789"}]`,
		GuardrailDebugParsed: &schemas.BifrostGuardrailMetadata{Interventions: []schemas.BifrostGuardrailIntervention{
			{Phase: "input", RuleName: "pii", GuardrailProvider: "bedrock"},
		}},
	}
}

// TestObjectMirrorWritesOnlyMatchingRoutes checks the key layout, the added
// id and object type, and that unmatched routes receive nothing.
func TestObjectMirrorWritesOnlyMatchingRoutes(t *testing.T) {
	flagged, errs := objectstore.NewInMemoryObjectStore(), objectstore.NewInMemoryObjectStore()
	mirror := newObjectMirror([]*mirrorRoute{
		testMirrorRoute("flagged", ObjectStorageRouteMatch{GuardrailOutcomes: []string{GuardrailOutcomeBlocked}}, flagged, ""),
		testMirrorRoute("errors", ObjectStorageRouteMatch{StatusCodes: []int{429}}, errs, ""),
	}, hybridTestLogger{})
	at := time.Date(2026, 10, 26, 13, 5, 0, 0, time.UTC)
	l := blockedLog("log-1", at)

	routes := mirror.matching(l)
	require.Len(t, routes, 1)
	mirror.enqueue(routes, l, ExtractPayload(l))
	mirror.close(context.Background())

	key := "flagged/logs/2026/10/26/13/log-1" + jsonObjectExt
	data, err := flagged.Get(context.Background(), key)
	require.NoError(t, err)
	var payload map[string]string
	require.NoError(t, sonic.Unmarshal(data, &payload))
	assert.Equal(t, "log-1", payload["id"])
	assert.Equal(t, "chat.completion", payload["object_type"])
	assert.Equal(t, l.InputHistory, payload["input_history"])
	assert.Equal(t, "openai", flagged.GetTags(key)["provider"])
	assert.Zero(t, errs.Len())
	assert.EqualValues(t, 1, routes[0].written.Load())
}

// TestObjectMirrorParquetRoute checks that a parquet route writes a parquet
// object that decodes back to the payload.
func TestObjectMirrorParquetRoute(t *testing.T) {
	store := objectstore.NewInMemoryObjectStore()
	route := testMirrorRoute("pq", ObjectStorageRouteMatch{GuardrailOutcomes: []string{GuardrailOutcomeBlocked}}, store, objectstore.FormatParquet)
	mirror := newObjectMirror([]*mirrorRoute{route}, hybridTestLogger{})
	l := blockedLog("log-pq", time.Now())
	mirror.enqueue(mirror.matching(l), l, ExtractPayload(l))
	mirror.close(context.Background())

	keys := store.Keys()
	require.Len(t, keys, 1)
	assert.Contains(t, keys[0], parquetObjectExt)
	data, err := store.Get(context.Background(), keys[0])
	require.NoError(t, err)
	require.True(t, objectstore.IsParquet(data))
	decoded, err := objectstore.DecodeParquetToJSONObject(data)
	require.NoError(t, err)
	var payload map[string]string
	require.NoError(t, sonic.Unmarshal(decoded, &payload))
	assert.Equal(t, l.InputHistory, payload["input_history"])
}

// TestObjectMirrorCountsFailedWrites checks that a failed Put is dropped and
// counted, not retried.
func TestObjectMirrorCountsFailedWrites(t *testing.T) {
	store := objectstore.NewInMemoryObjectStore()
	store.PutErr = errors.New("bucket unavailable")
	route := testMirrorRoute("flagged", ObjectStorageRouteMatch{GuardrailOutcomes: []string{GuardrailOutcomeBlocked}}, store, "")
	mirror := newObjectMirror([]*mirrorRoute{route}, hybridTestLogger{})
	l := blockedLog("log-fail", time.Now())
	mirror.enqueue(mirror.matching(l), l, ExtractPayload(l))
	mirror.close(context.Background())

	assert.EqualValues(t, 1, route.dropped.Load())
	assert.Zero(t, route.written.Load())
	assert.Equal(t, []ObjectStorageRouteStats{{Name: "flagged", Dropped: 1}}, mirror.stats())
}

// TestObjectMirrorDropsWhenQueueFull checks that a stalled bucket fills the
// queue and later copies are dropped without blocking the caller.
func TestObjectMirrorDropsWhenQueueFull(t *testing.T) {
	store := &blockingObjectStore{InMemoryObjectStore: objectstore.NewInMemoryObjectStore(), release: make(chan struct{})}
	route := testMirrorRoute("flagged", ObjectStorageRouteMatch{GuardrailOutcomes: []string{GuardrailOutcomeBlocked}}, store, "")
	mirror := newObjectMirror([]*mirrorRoute{route}, hybridTestLogger{})
	l := blockedLog("same-shard", time.Now())

	// Every copy of one id lands on one shard: one in the worker, the rest queued.
	total := defaultMirrorQueueSize/defaultMirrorWorkers + 10
	done := make(chan struct{})
	go func() {
		for range total {
			mirror.enqueue([]*mirrorRoute{route}, l, ExtractPayload(l))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue blocked on a full queue")
	}
	close(store.release)
	mirror.close(context.Background())

	assert.Positive(t, route.dropped.Load())
	assert.EqualValues(t, total, route.dropped.Load()+route.written.Load())
}

// TestObjectMirrorByteBudgetUnderConcurrentSubmits checks that concurrent
// submitters together never queue more bytes than the budget, while a
// stalled bucket keeps every accepted copy pending.
func TestObjectMirrorByteBudgetUnderConcurrentSubmits(t *testing.T) {
	store := &blockingObjectStore{InMemoryObjectStore: objectstore.NewInMemoryObjectStore(), release: make(chan struct{})}
	route := testMirrorRoute("flagged", ObjectStorageRouteMatch{GuardrailOutcomes: []string{GuardrailOutcomeBlocked}}, store, "")
	mirror := newObjectMirror([]*mirrorRoute{route}, hybridTestLogger{})

	// One shared 1 MiB payload: the budget fits 256 copies and the queues 2000.
	payload := make([]byte, 1<<20)
	fits := defaultMaxMirrorQueueBytes / len(payload)
	total := defaultMirrorQueueSize
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range total {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			mirror.submit(&mirrorWork{route: route, key: fmt.Sprintf("k-%d", i), logID: fmt.Sprintf("log-%d", i), payload: payload})
		}()
	}
	close(start)
	wg.Wait()

	pending := mirror.pendingBytes.Load()
	accepted := int64(total) - route.dropped.Load()
	close(store.release)
	mirror.close(context.Background())

	assert.LessOrEqual(t, pending, int64(defaultMaxMirrorQueueBytes), "pending bytes over budget")
	assert.LessOrEqual(t, accepted, int64(fits), "accepted copies over budget")
	assert.Zero(t, mirror.pendingBytes.Load(), "every reservation is released")
}

// TestObjectMirrorAfterClose checks that copies offered after close are
// dropped and a nil mirror is inert.
func TestObjectMirrorAfterClose(t *testing.T) {
	store := objectstore.NewInMemoryObjectStore()
	route := testMirrorRoute("flagged", ObjectStorageRouteMatch{GuardrailOutcomes: []string{GuardrailOutcomeBlocked}}, store, "")
	mirror := newObjectMirror([]*mirrorRoute{route}, hybridTestLogger{})
	mirror.close(context.Background())
	l := blockedLog("late", time.Now())
	mirror.enqueue([]*mirrorRoute{route}, l, ExtractPayload(l))
	assert.Zero(t, store.Len())

	var none *objectMirror
	assert.Nil(t, newObjectMirror(nil, hybridTestLogger{}))
	assert.Nil(t, none.stats())
	assert.Empty(t, none.matching(l))
	none.enqueue(nil, l, nil)
	none.close(context.Background())
}
