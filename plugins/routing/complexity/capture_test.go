package complexity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCaptureTestStore is an in-memory store that, like Qdrant and Weaviate,
// accepts only UUID point IDs. It is Chromem underneath but not of Chromem's
// type, which capture refuses: Chromem keeps vectors in one gateway's memory.
func newCaptureTestStore(t testing.TB) vectorstore.VectorStore {
	t.Helper()
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Enabled: true, Type: vectorstore.VectorStoreTypeChromem, Config: vectorstore.ChromemConfig{},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	return &uuidOnlyStore{store}
}

type uuidOnlyStore struct{ vectorstore.VectorStore }

func requireUUID(id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return fmt.Errorf("invalid id format (must be UUID): %w", err)
	}
	return nil
}

func (s uuidOnlyStore) Add(ctx context.Context, namespace, id string, embedding []float32, metadata map[string]any) error {
	if err := requireUUID(id); err != nil {
		return err
	}
	return s.VectorStore.Add(ctx, namespace, id, embedding, metadata)
}

func (s uuidOnlyStore) Delete(ctx context.Context, namespace, id string) error {
	if err := requireUUID(id); err != nil {
		return err
	}
	return s.VectorStore.Delete(ctx, namespace, id)
}

func (s uuidOnlyStore) GetChunk(ctx context.Context, namespace, id string) (vectorstore.SearchResult, error) {
	if err := requireUUID(id); err != nil {
		return vectorstore.SearchResult{}, err
	}
	return s.VectorStore.GetChunk(ctx, namespace, id)
}

// testCaptureRouter is the model router test captures belong to.
const testCaptureRouter = "router-under-test"

func newCaptureTest(t testing.TB) *RequestCapture {
	t.Helper()
	c := NewRequestCapture(bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(c.Close)
	return c
}

// newStoppedCapture returns a capture with no writer, so every sample stays
// countable as queued or dropped.
func newStoppedCapture() *RequestCapture {
	ctx, cancel := context.WithCancel(context.Background())
	return &RequestCapture{queue: make(chan captureItem, captureQueueSize), done: make(chan struct{}), ctx: ctx, cancel: cancel, drainBudget: time.Second,
		pending: make(map[string]int), forgottenIDs: make(map[string]struct{})}
}

func requestCtx(id string) context.Context {
	return context.WithValue(context.Background(), schemas.BifrostContextKeyRequestID, id)
}

func waitWritten(t *testing.T, c *RequestCapture, n int64) {
	t.Helper()
	require.Eventually(t, func() bool { return c.Stats().Written >= n }, 2*time.Second, 5*time.Millisecond,
		"capture wrote %d of %d (stats %+v)", c.Stats().Written, n, c.Stats())
}

func TestCaptureNamespaceIsKeyedByEmbeddingSpace(t *testing.T) {
	a := CaptureNamespace(testCaptureRouter, "openai", "text-embedding-3-small", 1536, 1)
	assert.Equal(t, a, CaptureNamespace(testCaptureRouter, "openai", "text-embedding-3-small", 1536, 1))
	assert.NotEqual(t, a, CaptureNamespace(testCaptureRouter, "openai", "text-embedding-3-large", 1536, 1))
	assert.NotEqual(t, a, CaptureNamespace(testCaptureRouter, "openai", "text-embedding-3-small", 512, 1))
	assert.NotEqual(t, a, CaptureNamespace(testCaptureRouter, "openai", "text-embedding-3-small", 1536, 3), "embedding more history is a different input")
	assert.Equal(t, a, CaptureNamespace(testCaptureRouter, "openai", "text-embedding-3-small", 1536, 0), "an unset history count embeds the last message, as 1 does")
	assert.True(t, strings.HasPrefix(a, CaptureNamespacePrefix+"_"))
	// The generation registry reclaims unclaimed namespaces under the
	// semantic prefix; capture must never be mistaken for one.
	assert.False(t, strings.HasPrefix(a, SemanticVectorStoreNamespace))
}

// Each model router captures into namespaces of its own, so a router's runs
// read only the requests it classified and its captures can be dropped with
// it. Without a router there is no namespace, so nothing is captured.
func TestCaptureNamespaceIsKeyedByRouter(t *testing.T) {
	a := CaptureNamespace("router-a", "openai", "text-embedding-3-small", 1536, 1)
	b := CaptureNamespace("router-b", "openai", "text-embedding-3-small", 1536, 1)
	assert.NotEqual(t, a, b, "two routers on one embedding space capture apart")
	assert.True(t, strings.HasPrefix(a, CaptureRouterPrefix("router-a")))
	assert.False(t, strings.HasPrefix(b, CaptureRouterPrefix("router-a")))
	assert.Empty(t, CaptureNamespace("", "openai", "text-embedding-3-small", 1536, 1))

	key, ok := CaptureNamespaceRouterKey(a)
	require.True(t, ok)
	assert.Equal(t, CaptureRouterKey("router-a"), key)
	// A namespace written before captures were per router carries no router.
	_, ok = CaptureNamespaceRouterKey(CaptureNamespacePrefix + "_" + strings.Repeat("a", 32))
	assert.False(t, ok)
	_, ok = CaptureNamespaceRouterKey("SomethingElse_" + strings.Repeat("a", 16) + "_" + strings.Repeat("b", 32))
	assert.False(t, ok)
}

// Dropping a router deletes its capture namespaces, in every embedding space
// it used, and leaves other routers' alone.
func TestDropRouterDeletesOnlyThatRoutersNamespaces(t *testing.T) {
	ctx := context.Background()
	store := newCaptureTestStore(t)
	c := newCaptureTest(t)
	c.Configure(&CaptureConfig{SampleRate: 1})
	oldA := CaptureNamespace("router-a", "openai", "old", 2, 1)
	newA := CaptureNamespace("router-a", "openai", "new", 2, 1)
	b := CaptureNamespace("router-b", "openai", "new", 2, 1)
	c.offer(requestCtx("r1"), store, oldA, 2, []float32{1, 0})
	c.offer(requestCtx("r2"), store, newA, 2, []float32{1, 0})
	c.offer(requestCtx("r3"), store, b, 2, []float32{0, 1})
	waitWritten(t, c, 3)

	require.NoError(t, c.DropRouter(ctx, store, "router-a"))
	left, err := store.ListNamespaces(ctx, CaptureNamespacePrefix+"_")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{b}, left)

	// The writer creates a dropped namespace again for the next sample.
	c.offer(requestCtx("r4"), store, newA, 2, []float32{1, 0})
	waitWritten(t, c, 4)
	_, err = store.GetChunk(ctx, newA, capturePointID("r4"))
	require.NoError(t, err)
}

func TestRequestCaptureOffCapturesNothing(t *testing.T) {
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	c.offer(requestCtx("r1"), store, "ns", 2, []float32{1, 0})
	c.Configure(&CaptureConfig{SampleRate: 0})
	c.offer(requestCtx("r2"), store, "ns", 2, []float32{1, 0})
	assert.Equal(t, CaptureStats{}, c.Stats())

	var nilCapture *RequestCapture
	nilCapture.offer(requestCtx("r3"), store, "ns", 2, []float32{1, 0}) // must not panic
	assert.False(t, nilCapture.Enabled())
}

// A captured vector carries no routing verdict: that is one router's view of
// the request and lives on its log, while the vector can serve every router
// with the same embedding space.
func TestRequestCaptureWritesVectorWithoutRoutingVerdict(t *testing.T) {
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	ns := CaptureNamespace(testCaptureRouter, "openai", "m", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("req-1"), store, ns, 2, []float32{0.6, 0.8})
	waitWritten(t, c, 1)

	got, err := store.GetChunk(context.Background(), ns, capturePointID("req-1"))
	require.NoError(t, err)
	assert.Equal(t, captureMetadataKindValue, got.Properties[captureMetadataKind])
	assert.NotZero(t, got.Properties[captureMetadataTimestamp])
	assert.Equal(t, "req-1", got.Properties[captureMetadataLogID], "the log ID is kept for lookup")
	for _, key := range []string{"tier", "score_bp", "accepted"} {
		assert.NotContains(t, got.Properties, key)
	}
}

func TestRequestCaptureSkipsRequestsWithoutID(t *testing.T) {
	c := newCaptureTest(t)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(context.Background(), newCaptureTestStore(t), "ns", 2, []float32{1, 0})
	assert.Zero(t, c.Stats().Queued, "a vector without a request ID can never be joined to its log")
}

func TestRequestCaptureSamplesAtConfiguredRate(t *testing.T) {
	c := newStoppedCapture()
	c.Configure(&CaptureConfig{SampleRate: 0.25})
	store := newCaptureTestStore(t)
	const n = 4000
	for i := range n {
		c.offer(requestCtx(fmt.Sprintf("r-%d", i)), store, "ns", 2, []float32{1, 0})
	}
	got := c.Stats().Queued + c.Stats().Dropped
	assert.InDelta(t, n/4, got, n/20, "sampled %d of %d at rate 0.25", got, n)
}

// The sample is a function of the request ID: a request is always in or out
// at a given rate, as a backfill from logs computes it, and a request in at a
// lower rate stays in at a higher one.
func TestCaptureSampleIsDeterministicByRequest(t *testing.T) {
	c := newStoppedCapture()
	c.Configure(&CaptureConfig{SampleRate: 0.3})
	store := newCaptureTestStore(t)
	var wantIn int
	for i := range 2000 {
		id := fmt.Sprintf("11111111-2222-4333-8444-%012d", i)
		in := CaptureSampled(id, 0.3)
		assert.Equal(t, in, CaptureSampled(id, 0.3), "the same request gets the same draw")
		if in {
			wantIn++
			assert.True(t, CaptureSampled(id, 0.6), "%s in at 0.3 stays in at 0.6", id)
		}
		c.offer(requestCtx(id), store, "ns", 2, []float32{1, 0})
	}
	assert.EqualValues(t, wantIn, c.Stats().Queued+c.Stats().Dropped, "live capture keeps exactly the requests CaptureSampled picks")
	assert.InDelta(t, 600, wantIn, 100)
	assert.False(t, CaptureSampled("any", 0))
	assert.True(t, CaptureSampled("any", 1))
}

// A backfilled request is stored where live capture would have put it, under
// its log's time, so writing it again replaces it.
func TestWriteBackfilledStoresUnderTheLogTime(t *testing.T) {
	ctx := context.Background()
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	ns := CaptureNamespace(testCaptureRouter, "openai", "m", 2, 1)
	logged := time.Now().Add(-3 * 24 * time.Hour).Truncate(time.Second)
	req := CapturedRequest{LogID: "req-1", Vector: []float32{1, 0}, At: logged}
	require.NoError(t, c.WriteBackfilled(ctx, store, ns, 2, []CapturedRequest{req}))
	require.NoError(t, c.WriteBackfilled(ctx, store, ns, 2, []CapturedRequest{req}), "writing it again replaces it")

	var got []CapturedRequest
	require.NoError(t, EachCaptured(ctx, store, ns, logged.Add(-time.Minute), logged.Add(time.Minute), func(page []CapturedRequest) error {
		got = append(got, page...)
		return nil
	}))
	require.Len(t, got, 1)
	assert.Equal(t, "req-1", got[0].LogID)
	assert.True(t, got[0].At.Equal(logged), "stamped with the log's time, not the backfill's")
}

func TestRequestCaptureDropsWhenQueueIsFull(t *testing.T) {
	c := newStoppedCapture()
	c.Configure(&CaptureConfig{SampleRate: 1})
	store := newCaptureTestStore(t)
	for range captureQueueSize + 10 {
		c.offer(requestCtx("r"), store, "ns", 2, []float32{1, 0})
	}
	st := c.Stats()
	assert.EqualValues(t, captureQueueSize, st.Queued)
	assert.EqualValues(t, 10, st.Dropped, "a full queue must drop, never block the request")
}

// Around an embedding-space switch the writer sees old- and new-space samples
// interleaved, and two nodes can serve different spaces at once. Writing to
// one space must never delete another: its vectors may still be live.
func TestRequestCaptureKeepsOtherEmbeddingSpacesOnWrite(t *testing.T) {
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	oldNS, newNS := CaptureNamespace(testCaptureRouter, "openai", "old", 2, 1), CaptureNamespace(testCaptureRouter, "openai", "new", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("a"), store, oldNS, 2, []float32{1, 0})
	waitWritten(t, c, 1)
	c.offer(requestCtx("b"), store, newNS, 2, []float32{0, 1})
	waitWritten(t, c, 2)
	c.offer(requestCtx("c"), store, oldNS, 2, []float32{1, 0})
	waitWritten(t, c, 3)

	ctx := context.Background()
	for ns, id := range map[string]string{oldNS: "a", newNS: "b"} {
		_, err := store.GetChunk(ctx, ns, capturePointID(id))
		assert.NoError(t, err, "vector %s in %s must survive writes to the other space", id, ns)
	}
}

// Stale embedding spaces age out through Sweep like the serving one, even
// when nothing is serving, so no captured vector outlives log retention.
func TestRequestCaptureSweepCoversStaleEmbeddingSpaces(t *testing.T) {
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	oldNS, newNS := CaptureNamespace(testCaptureRouter, "openai", "old", 2, 1), CaptureNamespace(testCaptureRouter, "openai", "new", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("a"), store, oldNS, 2, []float32{1, 0})
	c.offer(requestCtx("b"), store, newNS, 2, []float32{0, 1})
	waitWritten(t, c, 2)
	ctx := context.Background()

	n, err := c.Sweep(ctx, store, newNS, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Zero(t, n, "vectors younger than retention stay in every space")

	n, err = c.Sweep(ctx, store, "", time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 2, n, "with nothing serving, every space still ages out")
	_, err = store.GetChunk(ctx, oldNS, capturePointID("a"))
	assert.Error(t, err)
}

func TestRequestCaptureForgetAndSweep(t *testing.T) {
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	ns := CaptureNamespace(testCaptureRouter, "openai", "m", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	for _, id := range []string{"keep", "forget", "old"} {
		c.offer(requestCtx(id), store, ns, 2, []float32{1, 0})
	}
	waitWritten(t, c, 3)
	ctx := context.Background()

	c.Forget(ctx, store, ns, []string{"forget", "never-captured"})
	_, err := store.GetChunk(ctx, ns, capturePointID("forget"))
	assert.Error(t, err, "a deleted log's vector must go")
	_, err = store.GetChunk(ctx, ns, capturePointID("keep"))
	assert.NoError(t, err, "a kept log's vector must stay")

	// Everything was written just now, so a cutoff in the past keeps it and
	// one in the future removes it.
	n, err := c.Sweep(ctx, store, ns, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = c.Sweep(ctx, store, ns, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	// Nothing to act on is not an error.
	c.Forget(ctx, nil, "", []string{"x"})
	n, err = c.Sweep(ctx, store, CaptureNamespace(testCaptureRouter, "openai", "never-written", 2, 1), time.Now())
	assert.NoError(t, err)
	assert.Zero(t, n)
}

// A namespace that fails to sweep does not stop the namespaces after it from
// aging out; the failure is still reported once every namespace was tried.
func TestRequestCaptureSweepContinuesPastFailedNamespace(t *testing.T) {
	c := newCaptureTest(t)
	inner := newCaptureTestStore(t)
	nsA, nsB := CaptureNamespace(testCaptureRouter, "openai", "a", 2, 1), CaptureNamespace(testCaptureRouter, "openai", "b", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("x"), inner, nsA, 2, []float32{1, 0})
	c.offer(requestCtx("y"), inner, nsB, 2, []float32{0, 1})
	waitWritten(t, c, 2)
	ctx := context.Background()
	// Sweep walks namespaces in listing order; fail whichever comes first.
	namespaces, err := inner.ListNamespaces(ctx, CaptureNamespacePrefix+"_")
	require.NoError(t, err)
	require.Len(t, namespaces, 2)
	failNS, okNS := namespaces[0], namespaces[1]
	failID, okID := "x", "y"
	if failNS != nsA {
		failID, okID = okID, failID
	}
	store := &flakyStore{VectorStore: inner, failDeleteAll: map[string]bool{failNS: true}}

	n, err := c.Sweep(ctx, store, "", time.Now().Add(time.Hour))
	require.Error(t, err)
	assert.Contains(t, err.Error(), failNS)
	assert.Equal(t, 1, n, "the later namespace is still swept")
	_, err = inner.GetChunk(ctx, okNS, capturePointID(okID))
	assert.Error(t, err, "the later namespace's expired vector must go")
	_, err = inner.GetChunk(ctx, failNS, capturePointID(failID))
	assert.NoError(t, err)
}

// DeleteAll can succeed as a call while some deletes failed; Sweep counts only
// the ones that went and reports the rest.
func TestRequestCaptureSweepCountsOnlySuccessfulDeletes(t *testing.T) {
	c := newCaptureTest(t)
	inner := newCaptureTestStore(t)
	ns := CaptureNamespace(testCaptureRouter, "openai", "m", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	for _, id := range []string{"a", "b"} {
		c.offer(requestCtx(id), inner, ns, 2, []float32{1, 0})
	}
	waitWritten(t, c, 2)
	store := &flakyStore{VectorStore: inner, failResults: map[string]bool{ns: true}}

	n, err := c.Sweep(context.Background(), store, ns, time.Now().Add(time.Hour))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "locked")
	assert.Equal(t, 2, n, "only successful deletes count")
}

// ceilingStore behaves like Redis and Weaviate under a large window: a
// filtered read stops at a result ceiling and then reports nothing more,
// which looks exactly like the end of the data. Its unfiltered walk is
// complete but, like a Redis key walk, may hand an entry back twice.
type ceilingStore struct {
	vectorstore.VectorStore
	ceiling int
}

func (c *ceilingStore) GetAll(ctx context.Context, namespace string, queries []vectorstore.Query, selectFields []string, cursor *string, limit int64) ([]vectorstore.SearchResult, *string, error) {
	results, next, err := c.VectorStore.GetAll(ctx, namespace, queries, selectFields, cursor, limit)
	if err != nil {
		return nil, nil, err
	}
	if len(queries) > 0 {
		if len(results) > c.ceiling {
			results = results[:c.ceiling]
		}
		return results, nil, nil
	}
	return append(results, results...), next, nil
}

// A suggestion run must see every request captured in its window, however
// many there are: a backend's cap on filtered reads must not cut the window
// short, and an entry a walk returns twice is still one request.
func TestEachCapturedReadsTheWholeWindowPastAFilteredReadCeiling(t *testing.T) {
	c := newCaptureTest(t)
	inner := newCaptureTestStore(t)
	ns := CaptureNamespace(testCaptureRouter, "openai", "m", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	ids := []string{"r1", "r2", "r3", "r4", "r5"}
	for _, id := range ids {
		c.offer(requestCtx(id), inner, ns, 2, []float32{1, 0})
	}
	waitWritten(t, c, int64(len(ids)))
	store := &ceilingStore{VectorStore: inner, ceiling: 2}

	now := time.Now()
	var got []string
	require.NoError(t, EachCaptured(context.Background(), store, ns, now.Add(-time.Hour), now.Add(time.Minute), func(page []CapturedRequest) error {
		for _, r := range page {
			got = append(got, r.LogID)
			assert.Equal(t, []float32{1, 0}, r.Vector)
		}
		return nil
	}))
	assert.ElementsMatch(t, ids, got, "every captured request, once")

	got = nil
	require.NoError(t, EachCaptured(context.Background(), store, ns, now.Add(-2*time.Hour), now.Add(-time.Hour), func(page []CapturedRequest) error {
		for _, r := range page {
			got = append(got, r.LogID)
		}
		return nil
	}))
	assert.Empty(t, got, "the window still applies")
}

// vanishingStore serves one page of a namespace, then finds it gone, as when
// the routing plugin's removal drops it mid-read.
type vanishingStore struct{ vectorstore.VectorStore }

func (vanishingStore) GetAll(_ context.Context, namespace string, _ []vectorstore.Query, _ []string, cursor *string, _ int64) ([]vectorstore.SearchResult, *string, error) {
	if cursor != nil {
		return nil, nil, fmt.Errorf("%w: namespace %s", vectorstore.ErrNotFound, namespace)
	}
	next := "page-2"
	return []vectorstore.SearchResult{{
		ID:     capturePointID("first"),
		Vector: []float32{1, 0},
		Properties: map[string]any{
			captureMetadataKind: captureMetadataKindValue, captureMetadataTimestamp: time.Now().Unix(), captureMetadataLogID: "first",
		},
	}}, &next, nil
}

// A namespace missing from the start holds nothing yet, but one that goes
// after a page was read leaves the window partial: that is an error, so a run
// never reconciles suggestions against traffic it only half read, and an
// estimate never counts half a window as all of it.
func TestEachCapturedFailsWhenTheNamespaceGoesMidRead(t *testing.T) {
	ns := CaptureNamespace(testCaptureRouter, "openai", "m", 2, 1)
	now := time.Now()
	var pages int
	err := EachCaptured(context.Background(), vanishingStore{}, ns, now.Add(-time.Hour), now.Add(time.Minute), func([]CapturedRequest) error {
		pages++
		return nil
	})
	require.ErrorIs(t, err, vectorstore.ErrNotFound)
	assert.Equal(t, 1, pages, "the first page was read before the namespace went")

	_, err = CountCaptured(context.Background(), vanishingStore{}, ns, now.Add(-time.Hour), now.Add(time.Minute))
	require.ErrorIs(t, err, vectorstore.ErrNotFound, "a count cut short is not passed off as the whole window")
	count, err := CountCaptured(context.Background(), newCaptureTestStore(t), ns, now.Add(-time.Hour), now)
	require.NoError(t, err, "a namespace missing from the start counts nothing")
	assert.Zero(t, count)

	require.NoError(t, EachCaptured(context.Background(), newCaptureTestStore(t), ns, now.Add(-time.Hour), now, func([]CapturedRequest) error {
		t.Fatal("a namespace that was never written has no pages")
		return nil
	}), "a namespace missing from the start is an empty window")
}

// BenchmarkRequestCaptureOffer isolates the hook's request-path cost from the
// writer: off, sampled onto the queue (a drainer discards), and sampled with
// the queue full (dropped).
func BenchmarkRequestCaptureOffer(b *testing.B) {
	store := newCaptureTestStore(b)
	ctx := requestCtx("bench")
	vec := make([]float32, 1536)
	run := func(b *testing.B, c *RequestCapture) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			c.offer(ctx, store, "ns", 1536, vec)
		}
	}
	b.Run("off", func(b *testing.B) {
		c := newCaptureTest(b)
		run(b, c)
	})
	b.Run("on_queued", func(b *testing.B) {
		c := newStoppedCapture()
		c.Configure(&CaptureConfig{SampleRate: 1})
		stop := make(chan struct{})
		go func() {
			for {
				select {
				case <-c.queue:
				case <-stop:
					return
				}
			}
		}()
		defer close(stop)
		run(b, c)
	})
	b.Run("on_dropped", func(b *testing.B) {
		c := newStoppedCapture()
		c.Configure(&CaptureConfig{SampleRate: 1})
		for range captureQueueSize {
			c.offer(ctx, store, "ns", 1536, vec)
		}
		run(b, c)
	})
	b.Run("sampled_out_1pct", func(b *testing.B) {
		c := newStoppedCapture()
		c.Configure(&CaptureConfig{SampleRate: 0.01})
		run(b, c)
	})
}

// memoryDeletes is an in-memory CaptureDeleteRecorder.
type memoryDeletes struct{ rows []CaptureDelete }

func (m *memoryDeletes) AddCaptureDeletes(_ context.Context, rows []CaptureDelete) error {
	m.rows = append(m.rows, rows...)
	return nil
}

func (m *memoryDeletes) ListCaptureDeletes(_ context.Context, limit int) ([]CaptureDelete, error) {
	return m.rows[:min(limit, len(m.rows))], nil
}

func (m *memoryDeletes) DeleteCaptureDeletes(_ context.Context, rows []CaptureDelete) error {
	gone := map[CaptureDelete]bool{}
	for _, r := range rows {
		gone[r] = true
	}
	kept := m.rows[:0]
	for _, r := range m.rows {
		if !gone[r] {
			kept = append(kept, r)
		}
	}
	m.rows = kept
	return nil
}

// flakyStore refuses deletes while down. failDelete names namespaces whose
// Delete fails; failDeleteAll names namespaces whose DeleteAll returns an
// error; failResults names namespaces whose DeleteAll succeeds as a call but
// reports one failed result alongside the real ones. listDown fails
// ListNamespaces.
type flakyStore struct {
	vectorstore.VectorStore
	down          bool
	listDown      bool
	deletes       atomic.Int64
	failDelete    map[string]bool
	failDeleteAll map[string]bool
	failResults   map[string]bool
}

func (f *flakyStore) ListNamespaces(ctx context.Context, prefix string) ([]string, error) {
	if f.listDown {
		return nil, errors.New("vector store unavailable")
	}
	return f.VectorStore.ListNamespaces(ctx, prefix)
}

func (f *flakyStore) DeleteAll(ctx context.Context, namespace string, queries []vectorstore.Query) ([]vectorstore.DeleteResult, error) {
	if f.failDeleteAll[namespace] {
		return nil, errors.New("vector store unavailable")
	}
	results, err := f.VectorStore.DeleteAll(ctx, namespace, queries)
	if err == nil && f.failResults[namespace] {
		results = append(results, vectorstore.DeleteResult{ID: "stuck", Status: vectorstore.DeleteStatusError, Error: "locked"})
	}
	return results, err
}

func (f *flakyStore) Delete(ctx context.Context, namespace, id string) error {
	f.deletes.Add(1)
	if f.down || f.failDelete[namespace] {
		return errors.New("vector store unavailable")
	}
	return f.VectorStore.Delete(ctx, namespace, id)
}

func TestRequestCaptureRecordsFailedDeletesAndRetriesThem(t *testing.T) {
	c := newCaptureTest(t)
	inner := newCaptureTestStore(t)
	ns := CaptureNamespace(testCaptureRouter, "openai", "m", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	for _, id := range []string{"a", "b", "c"} {
		c.offer(requestCtx(id), inner, ns, 2, []float32{1, 0})
	}
	waitWritten(t, c, 3)
	ctx := context.Background()
	recorder := &memoryDeletes{}
	c.SetDeleteRecorder(recorder)
	store := &flakyStore{VectorStore: inner, down: true}

	c.Forget(ctx, store, ns, []string{"a", "b", "c"})
	assert.EqualValues(t, 1, store.deletes.Load(), "after the first refusal the rest are recorded without trying")
	require.Len(t, recorder.rows, 3)
	assert.Equal(t, ns, recorder.rows[0].Namespace)

	// Still down: the pass stops at the first failure and keeps every row.
	n, err := c.RetryDeletes(ctx, store, ns)
	assert.Error(t, err)
	assert.Zero(t, n)
	assert.Len(t, recorder.rows, 3)

	store.down = false
	recorder.rows = append(recorder.rows, CaptureDelete{LogID: "a", Namespace: ns}) // recorded twice
	n, err = c.RetryDeletes(ctx, store, ns)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Empty(t, recorder.rows)
	for _, id := range []string{"a", "b", "c"} {
		_, err := inner.GetChunk(ctx, ns, capturePointID(id))
		assert.Error(t, err, "%s should be gone after the retry", id)
	}
}

// concurrencyStore counts the deletes running at once and refuses every
// delete in failNS. Calls into the in-memory store are serialized, since
// chromem-go reads its document map unlocked in Delete; the deletes still
// overlap in the sleep before it, which is what peak measures.
type concurrencyStore struct {
	vectorstore.VectorStore
	running, peak, calls atomic.Int64
	failNS               string
	inner                sync.Mutex
}

func (s *concurrencyStore) Delete(ctx context.Context, namespace, id string) error {
	s.calls.Add(1)
	n := s.running.Add(1)
	defer s.running.Add(-1)
	for {
		peak := s.peak.Load()
		if n <= peak || s.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	time.Sleep(time.Millisecond)
	if namespace == s.failNS {
		return errors.New("vector store unavailable")
	}
	s.inner.Lock()
	defer s.inner.Unlock()
	return s.VectorStore.Delete(ctx, namespace, id)
}

// Forget deletes a large batch a few at a time, and once a delete fails it
// records every pair it has not tried instead of trying it.
func TestRequestCaptureForgetBoundsConcurrentDeletes(t *testing.T) {
	ctx := context.Background()
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = fmt.Sprintf("log-%d", i)
	}

	t.Run("every delete runs, a bounded number at once", func(t *testing.T) {
		c := newCaptureTest(t)
		inner := newCaptureTestStore(t)
		ns := CaptureNamespace(testCaptureRouter, "openai", "m", 2, 1)
		require.NoError(t, inner.CreateNamespace(ctx, ns, 2, captureProperties))
		for _, id := range ids {
			require.NoError(t, inner.Add(ctx, ns, capturePointID(id), []float32{1, 0}, map[string]any{captureMetadataKind: captureMetadataKindValue}))
		}
		recorder := &memoryDeletes{}
		c.SetDeleteRecorder(recorder)
		store := &concurrencyStore{VectorStore: inner}

		c.Forget(ctx, store, ns, ids)
		assert.EqualValues(t, len(ids), store.calls.Load())
		assert.LessOrEqual(t, store.peak.Load(), int64(captureForgetWorkers))
		assert.Greater(t, store.peak.Load(), int64(1), "deletes run side by side")
		assert.Empty(t, recorder.rows)
		for _, id := range ids {
			_, err := inner.GetChunk(ctx, ns, capturePointID(id))
			assert.Error(t, err, "%s should be gone", id)
		}
	})

	t.Run("after a failure the rest are recorded untried", func(t *testing.T) {
		c := newCaptureTest(t)
		inner := newCaptureTestStore(t)
		okNS, badNS := CaptureNamespace(testCaptureRouter, "openai", "a", 2, 1), CaptureNamespace(testCaptureRouter, "openai", "b", 2, 1)
		for _, ns := range []string{okNS, badNS} {
			require.NoError(t, inner.CreateNamespace(ctx, ns, 2, captureProperties))
		}
		recorder := &memoryDeletes{}
		c.SetDeleteRecorder(recorder)
		store := &concurrencyStore{VectorStore: inner, failNS: badNS}

		c.Forget(ctx, store, okNS, ids)
		total := 2 * len(ids)
		recorded := map[CaptureDelete]bool{}
		for _, row := range recorder.rows {
			assert.False(t, recorded[row], "%v recorded twice", row)
			recorded[row] = true
		}
		for _, id := range ids {
			assert.True(t, recorded[CaptureDelete{LogID: id, Namespace: badNS}], "%s in the failing namespace must be recorded", id)
		}
		assert.Less(t, store.calls.Load(), int64(total), "pairs after the failure are not tried")
		assert.LessOrEqual(t, store.calls.Load()+int64(len(recorder.rows)), int64(total+captureForgetWorkers),
			"only deletes already running when one failed are tried and then recorded")
	})
}

// When the namespaces cannot be listed the delete is recorded unscoped, and
// the retry removes the vector from every capture namespace, even with
// nothing serving.
func TestRequestCaptureRetryResolvesUnknownNamespace(t *testing.T) {
	c := newCaptureTest(t)
	inner := newCaptureTestStore(t)
	oldNS, newNS := CaptureNamespace(testCaptureRouter, "openai", "old", 2, 1), CaptureNamespace(testCaptureRouter, "openai", "new", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("a"), inner, oldNS, 2, []float32{1, 0})
	c.offer(requestCtx("a"), inner, newNS, 2, []float32{0, 1})
	waitWritten(t, c, 2)
	recorder := &memoryDeletes{}
	c.SetDeleteRecorder(recorder)
	store := &flakyStore{VectorStore: inner, listDown: true}
	ctx := context.Background()

	c.Forget(ctx, store, newNS, []string{"a"})
	require.Equal(t, []CaptureDelete{{LogID: "a"}}, recorder.rows)

	store.listDown = false
	n, err := c.RetryDeletes(ctx, store, "")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Empty(t, recorder.rows)
	for _, ns := range []string{oldNS, newNS} {
		_, err := inner.GetChunk(ctx, ns, capturePointID("a"))
		assert.Error(t, err, "the vector in %s must be gone", ns)
	}
}

// After an embedding-space switch a deleted log's vector still sits in the
// old space; Forget removes it there too, and works with nothing serving.
func TestRequestCaptureForgetCoversEveryEmbeddingSpace(t *testing.T) {
	for _, serving := range []string{CaptureNamespace(testCaptureRouter, "openai", "new", 2, 1), ""} {
		t.Run(fmt.Sprintf("serving=%q", serving), func(t *testing.T) {
			c := newCaptureTest(t)
			store := newCaptureTestStore(t)
			oldNS := CaptureNamespace(testCaptureRouter, "openai", "old", 2, 1)
			c.Configure(&CaptureConfig{SampleRate: 1})
			c.offer(requestCtx("gone"), store, oldNS, 2, []float32{1, 0})
			c.offer(requestCtx("kept"), store, oldNS, 2, []float32{0, 1})
			waitWritten(t, c, 2)
			recorder := &memoryDeletes{}
			c.SetDeleteRecorder(recorder)
			ctx := context.Background()

			c.Forget(ctx, store, serving, []string{"gone"})
			_, err := store.GetChunk(ctx, oldNS, capturePointID("gone"))
			assert.Error(t, err, "the old space's vector of a deleted log must go")
			_, err = store.GetChunk(ctx, oldNS, capturePointID("kept"))
			assert.NoError(t, err)
			assert.Empty(t, recorder.rows, "nothing failed, so nothing is left to retry")
		})
	}
}

// One log can have retry rows in two namespaces. Finishing one must not
// clear the other, which still has a vector to delete.
func TestRequestCaptureRetryKeepsRowsOfOtherNamespaces(t *testing.T) {
	c := newCaptureTest(t)
	inner := newCaptureTestStore(t)
	nsA, nsB := CaptureNamespace(testCaptureRouter, "openai", "a", 2, 1), CaptureNamespace(testCaptureRouter, "openai", "b", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("x"), inner, nsA, 2, []float32{1, 0})
	c.offer(requestCtx("x"), inner, nsB, 2, []float32{0, 1})
	waitWritten(t, c, 2)
	recorder := &memoryDeletes{rows: []CaptureDelete{{LogID: "x", Namespace: nsA}, {LogID: "x", Namespace: nsB}}}
	c.SetDeleteRecorder(recorder)
	store := &flakyStore{VectorStore: inner, failDelete: map[string]bool{nsB: true}}
	ctx := context.Background()

	n, err := c.RetryDeletes(ctx, store, "")
	require.Error(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []CaptureDelete{{LogID: "x", Namespace: nsB}}, recorder.rows, "the other namespace's row must stay")

	store.failDelete = nil
	n, err = c.RetryDeletes(ctx, store, "")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Empty(t, recorder.rows)
	_, err = inner.GetChunk(ctx, nsB, capturePointID("x"))
	assert.Error(t, err)
}

// A log deleted while its sample is still queued: the writer must not write
// it afterwards.
func TestRequestCaptureForgetBeforeWriteSkipsSample(t *testing.T) {
	c := newStoppedCapture()
	store := newCaptureTestStore(t)
	ns := CaptureNamespace(testCaptureRouter, "openai", "m", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("gone"), store, ns, 2, []float32{1, 0})
	c.offer(requestCtx("kept"), store, ns, 2, []float32{0, 1})
	ctx := context.Background()

	c.Forget(ctx, store, ns, []string{"gone"})
	for range 2 {
		c.writeOne(<-c.queue)
	}
	_, err := store.GetChunk(ctx, ns, capturePointID("gone"))
	assert.Error(t, err, "a deleted log's queued sample must not be written")
	_, err = store.GetChunk(ctx, ns, capturePointID("kept"))
	assert.NoError(t, err)
	st := c.Stats()
	assert.EqualValues(t, 1, st.Written)
	assert.EqualValues(t, 1, st.Forgotten)
	assert.Empty(t, c.pending)
	assert.Empty(t, c.forgottenIDs, "marks are cleared once nothing of the ID is pending")
}

// blockingAddStore holds Add until released, so a Forget can run while a
// write is in flight.
type blockingAddStore struct {
	vectorstore.VectorStore
	entered, release chan struct{}
}

func (s *blockingAddStore) Add(ctx context.Context, namespace, id string, embedding []float32, metadata map[string]any) error {
	close(s.entered)
	<-s.release
	return s.VectorStore.Add(ctx, namespace, id, embedding, metadata)
}

// A log deleted while its sample is being written: Forget's delete finds
// nothing, the write then lands, and the writer removes it again.
func TestRequestCaptureForgetDuringWriteRemovesVector(t *testing.T) {
	c := newCaptureTest(t)
	inner := newCaptureTestStore(t)
	ns := CaptureNamespace(testCaptureRouter, "openai", "m", 2, 1)
	require.NoError(t, inner.CreateNamespace(context.Background(), ns, 2, captureProperties))
	store := &blockingAddStore{VectorStore: inner, entered: make(chan struct{}), release: make(chan struct{})}
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("gone"), store, ns, 2, []float32{1, 0})
	<-store.entered
	ctx := context.Background()

	c.Forget(ctx, store, ns, []string{"gone"})
	close(store.release)
	require.Eventually(t, func() bool { return c.Stats().Forgotten == 1 }, 2*time.Second, 5*time.Millisecond)
	_, err := inner.GetChunk(ctx, ns, capturePointID("gone"))
	assert.Error(t, err, "a write that lands after the delete must not keep the vector")
	assert.Zero(t, c.Stats().Written)
}

func TestCapturePointIDIsStableUUID(t *testing.T) {
	gatewayID := uuid.NewString()
	assert.Equal(t, gatewayID, capturePointID(gatewayID), "a UUID request ID is its own point ID")
	assert.Equal(t, gatewayID, capturePointID(strings.ToUpper(gatewayID)), "in canonical form")

	for _, id := range []string{"req-1", "trace/abc:42", "  "} {
		got := capturePointID(id)
		_, err := uuid.Parse(got)
		require.NoError(t, err, "%q must map to a UUID", id)
		assert.Equal(t, got, capturePointID(id), "%q must map to the same point every time", id)
	}
	assert.NotEqual(t, capturePointID("req-1"), capturePointID("req-2"))
}

// A missing namespace means the vector is already gone: RetryDeletes clears
// that row and goes on to the rows after it, instead of stopping on it.
func TestRequestCaptureRetrySkipsAbsentNamespace(t *testing.T) {
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	ns := CaptureNamespace(testCaptureRouter, "openai", "m", 2, 1)
	absent := CaptureNamespace(testCaptureRouter, "openai", "never-written", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("later"), store, ns, 2, []float32{1, 0})
	waitWritten(t, c, 1)
	recorder := &memoryDeletes{rows: []CaptureDelete{
		{LogID: "first", Namespace: absent},
		{LogID: "later", Namespace: ns},
	}}
	c.SetDeleteRecorder(recorder)

	n, err := c.RetryDeletes(context.Background(), store, ns)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Empty(t, recorder.rows)
	_, err = store.GetChunk(context.Background(), ns, capturePointID("later"))
	assert.Error(t, err, "the row after the absent namespace must still be deleted")
}

func TestRequestCaptureCloseDrainsQueuedSamples(t *testing.T) {
	store := newCaptureTestStore(t)
	ns := CaptureNamespace(testCaptureRouter, "openai", "m", 2, 1)
	c := NewRequestCapture(bifrost.NewDefaultLogger(schemas.LogLevelError))
	c.Configure(&CaptureConfig{SampleRate: 1})
	for i := range 50 {
		c.offer(requestCtx(fmt.Sprintf("r%d", i)), store, ns, 2, []float32{1, 0})
	}
	c.Close()
	st := c.Stats()
	assert.EqualValues(t, st.Queued, st.Written+st.Failed, "every accepted sample is written before Close returns")
	assert.EqualValues(t, 50, st.Written)

	c.offer(requestCtx("late"), store, ns, 2, []float32{1, 0})
	assert.EqualValues(t, 50, c.Stats().Queued, "a closed capture refuses new samples")
}

// singlePageStore pages like Pinecone: a read that does not ask for vectors
// returns one page and no cursor, however many entries match; a read that
// does pages to the end.
type singlePageStore struct {
	vectorstore.VectorStore
	vectorReads int
}

func (s *singlePageStore) GetAll(ctx context.Context, namespace string, queries []vectorstore.Query, selectFields []string, cursor *string, limit int64) ([]vectorstore.SearchResult, *string, error) {
	if vectorstore.IncludeVectorsRequested(ctx) {
		s.vectorReads++
		return s.VectorStore.GetAll(ctx, namespace, queries, selectFields, cursor, limit)
	}
	results, _, err := s.VectorStore.GetAll(ctx, namespace, queries, selectFields, nil, limit)
	return results, nil, err
}

// cappedFilterStore behaves like Weaviate under a large window: a filtered
// read pages by offset and fails once it would pass a result ceiling. Its
// unfiltered walk is complete.
type cappedFilterStore struct {
	vectorstore.VectorStore
	ceiling int
	served  int
}

func (c *cappedFilterStore) GetAll(ctx context.Context, namespace string, queries []vectorstore.Query, selectFields []string, cursor *string, limit int64) ([]vectorstore.SearchResult, *string, error) {
	if len(queries) == 0 {
		return c.VectorStore.GetAll(ctx, namespace, queries, selectFields, cursor, limit)
	}
	if cursor == nil {
		c.served = 0
	}
	if c.served >= c.ceiling {
		return nil, nil, errors.New("query maximum results exceeded")
	}
	results, next, err := c.VectorStore.GetAll(ctx, namespace, queries, selectFields, cursor, limit)
	c.served += len(results)
	return results, next, err
}

// searchCappedStore behaves like Redis under a large window: a read without
// vectors is served by search, filtered or not, and answers nothing past a
// result ceiling, which looks exactly like the end of the data. A read with
// vectors walks the keys to the end, and may hand an entry back twice.
type searchCappedStore struct {
	vectorstore.VectorStore
	ceiling     int
	served      int
	vectorReads int
}

func (s *searchCappedStore) GetAll(ctx context.Context, namespace string, queries []vectorstore.Query, selectFields []string, cursor *string, limit int64) ([]vectorstore.SearchResult, *string, error) {
	if vectorstore.IncludeVectorsRequested(ctx) {
		s.vectorReads++
		results, next, err := s.VectorStore.GetAll(ctx, namespace, queries, selectFields, cursor, limit)
		return append(results, results...), next, err
	}
	if cursor == nil {
		s.served = 0
	}
	if s.served >= s.ceiling {
		return nil, nil, nil
	}
	results, next, err := s.VectorStore.GetAll(ctx, namespace, queries, selectFields, cursor, limit)
	if len(results) > s.ceiling-s.served {
		results = results[:s.ceiling-s.served]
	}
	s.served += len(results)
	return results, next, err
}

// CountCaptured must count every capture in the window, not one page of
// them, on a store whose plain read cannot page past its first, on one whose
// filtered read fails past a result ceiling, and on one whose plain read ends
// at that ceiling.
func TestCountCapturedCountsPastOnePage(t *testing.T) {
	ctx := context.Background()
	inner := newCaptureTestStore(t)
	ns := CaptureNamespace(testCaptureRouter, "openai", "m", 2, 1)
	require.NoError(t, inner.CreateNamespace(ctx, ns, 2, captureProperties))
	now := time.Now()
	const n = captureReadPage + 100
	for i := range n {
		id := fmt.Sprintf("req-%d", i)
		require.NoError(t, inner.Add(ctx, ns, capturePointID(id), []float32{1, 0}, map[string]any{
			captureMetadataKind: captureMetadataKindValue, captureMetadataTimestamp: now.Unix(), captureMetadataLogID: id,
		}))
	}
	require.NoError(t, inner.Add(ctx, ns, capturePointID("old"), []float32{1, 0}, map[string]any{
		captureMetadataKind: captureMetadataKindValue, captureMetadataTimestamp: now.Add(-48 * time.Hour).Unix(), captureMetadataLogID: "old",
	}))
	from, to := now.Add(-time.Hour), now.Add(time.Hour)

	got, err := CountCaptured(ctx, inner, ns, from, to)
	require.NoError(t, err)
	assert.Equal(t, n, got, "a store that pages its plain reads is counted without reading vectors, leaving out what is outside the window")

	capped := &cappedFilterStore{VectorStore: inner, ceiling: captureReadPage}
	got, err = CountCaptured(ctx, capped, ns, from, to)
	require.NoError(t, err, "a window larger than a filtered read's ceiling is still counted")
	assert.Equal(t, n, got)

	single := &singlePageStore{VectorStore: inner}
	got, err = CountCaptured(ctx, single, ns, from, to)
	require.NoError(t, err)
	assert.Equal(t, n, got, "a full page with no cursor is not taken as the whole count")
	assert.NotZero(t, single.vectorReads, "the recount pages through the vector read")

	// A plain read that returned a search ceiling's worth of rows may have
	// been cut there, so the window is counted again through the vector
	// walk, where an entry handed back twice is still one request.
	search := &searchCappedStore{VectorStore: inner, ceiling: captureReadPage}
	got, err = countCaptured(ctx, search, ns, from, to, captureReadPage)
	require.NoError(t, err)
	assert.Equal(t, n, got, "a plain read that ends at a search ceiling is not taken as the whole count")
	assert.NotZero(t, search.vectorReads)

	under := &searchCappedStore{VectorStore: inner, ceiling: 10 * n}
	got, err = countCaptured(ctx, under, ns, from, to, 10*n)
	require.NoError(t, err)
	assert.Equal(t, n, got)
	assert.Zero(t, under.vectorReads, "a window under the ceiling is counted without reading vectors")
}
