package complexity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCaptureTestStore(t testing.TB) vectorstore.VectorStore {
	t.Helper()
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Enabled: true, Type: vectorstore.VectorStoreTypeChromem, Config: vectorstore.ChromemConfig{},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	return store
}

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
	return &RequestCapture{queue: make(chan captureItem, captureQueueSize), done: make(chan struct{}), ctx: ctx, cancel: cancel, drainBudget: time.Second}
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
	a := CaptureNamespace("openai", "text-embedding-3-small", 1536, 1)
	assert.Equal(t, a, CaptureNamespace("openai", "text-embedding-3-small", 1536, 1))
	assert.NotEqual(t, a, CaptureNamespace("openai", "text-embedding-3-large", 1536, 1))
	assert.NotEqual(t, a, CaptureNamespace("openai", "text-embedding-3-small", 512, 1))
	assert.NotEqual(t, a, CaptureNamespace("openai", "text-embedding-3-small", 1536, 3), "embedding more history is a different input")
	assert.Equal(t, a, CaptureNamespace("openai", "text-embedding-3-small", 1536, 0), "an unset history count embeds the last message, as 1 does")
	assert.True(t, strings.HasPrefix(a, CaptureNamespacePrefix+"_"))
	// The generation registry reclaims unclaimed namespaces under the
	// semantic prefix; capture must never be mistaken for one.
	assert.False(t, strings.HasPrefix(a, SemanticVectorStoreNamespace))
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
	ns := CaptureNamespace("openai", "m", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("req-1"), store, ns, 2, []float32{0.6, 0.8})
	waitWritten(t, c, 1)

	got, err := store.GetChunk(context.Background(), ns, "req-1")
	require.NoError(t, err)
	assert.Equal(t, captureMetadataKindValue, got.Properties[captureMetadataKind])
	assert.NotZero(t, got.Properties[captureMetadataTimestamp])
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
	for range n {
		c.offer(requestCtx("r"), store, "ns", 2, []float32{1, 0})
	}
	got := c.Stats().Queued + c.Stats().Dropped
	assert.InDelta(t, n/4, got, n/20, "sampled %d of %d at rate 0.25", got, n)
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

func TestRequestCaptureDeletesStaleEmbeddingSpaces(t *testing.T) {
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	oldNS, newNS := CaptureNamespace("openai", "old", 2, 1), CaptureNamespace("openai", "new", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("a"), store, oldNS, 2, []float32{1, 0})
	waitWritten(t, c, 1)
	c.offer(requestCtx("b"), store, newNS, 2, []float32{0, 1})
	waitWritten(t, c, 2)

	names, err := store.ListNamespaces(context.Background(), CaptureNamespacePrefix)
	require.NoError(t, err)
	assert.Equal(t, []string{newNS}, names, "vectors from an old embedding model are not comparable and must go")
}

func TestRequestCaptureForgetAndSweep(t *testing.T) {
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	ns := CaptureNamespace("openai", "m", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	for _, id := range []string{"keep", "forget", "old"} {
		c.offer(requestCtx(id), store, ns, 2, []float32{1, 0})
	}
	waitWritten(t, c, 3)
	ctx := context.Background()

	c.Forget(ctx, store, ns, []string{"forget", "never-captured"})
	_, err := store.GetChunk(ctx, ns, "forget")
	assert.Error(t, err, "a deleted log's vector must go")

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
	n, err = c.Sweep(ctx, store, CaptureNamespace("openai", "never-written", 2, 1), time.Now())
	assert.NoError(t, err)
	assert.Zero(t, n)
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

func (m *memoryDeletes) DeleteCaptureDeletes(_ context.Context, ids []string) error {
	gone := map[string]bool{}
	for _, id := range ids {
		gone[id] = true
	}
	kept := m.rows[:0]
	for _, r := range m.rows {
		if !gone[r.LogID] {
			kept = append(kept, r)
		}
	}
	m.rows = kept
	return nil
}

// flakyStore refuses deletes while down.
type flakyStore struct {
	vectorstore.VectorStore
	down    bool
	deletes int
}

func (f *flakyStore) Delete(ctx context.Context, namespace, id string) error {
	f.deletes++
	if f.down {
		return errors.New("vector store unavailable")
	}
	return f.VectorStore.Delete(ctx, namespace, id)
}

func TestRequestCaptureRecordsFailedDeletesAndRetriesThem(t *testing.T) {
	c := newCaptureTest(t)
	inner := newCaptureTestStore(t)
	ns := CaptureNamespace("openai", "m", 2, 1)
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
	assert.Equal(t, 1, store.deletes, "after the first refusal the rest are recorded without trying")
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
		_, err := inner.GetChunk(ctx, ns, id)
		assert.Error(t, err, "%s should be gone after the retry", id)
	}
}

func TestRequestCaptureRetryResolvesUnknownNamespace(t *testing.T) {
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	ns := CaptureNamespace("openai", "m", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("a"), store, ns, 2, []float32{1, 0})
	waitWritten(t, c, 1)
	recorder := &memoryDeletes{}
	c.SetDeleteRecorder(recorder)

	// Nothing was serving when the log was deleted, so no namespace was known.
	c.Forget(context.Background(), store, "", []string{"a"})
	require.Equal(t, []CaptureDelete{{LogID: "a"}}, recorder.rows)
	n, err := c.RetryDeletes(context.Background(), store, ns)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestRequestCaptureCloseDrainsQueuedSamples(t *testing.T) {
	store := newCaptureTestStore(t)
	ns := CaptureNamespace("openai", "m", 2, 1)
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
