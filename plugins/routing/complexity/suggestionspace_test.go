package complexity

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A suggestion run sees the serving phrases with the vectors requests were
// routed against, and the requests captured in its window with theirs. The
// phrase bank is embedded while captures go to the configured store, so the
// run must read each from its own store.
func TestSuggestionSpaceReadsPhrasesAndCapturedRequests(t *testing.T) {
	classifier := readySemanticClassifierWithStore(t, newCaptureTestStore(t))
	capture := newCaptureTest(t)
	capture.Configure(&CaptureConfig{SampleRate: 1})
	classifier.SetRequestCapture(capture)
	for _, id := range []string{"r1", "r2", "r3"} {
		_, err := classifier.Classify(requestCtx(id), ComplexityInput{LastUserText: "medium request"})
		require.NoError(t, err)
	}
	waitWritten(t, capture, 3)

	ctx := context.Background()
	space, err := classifier.SuggestionSpace(ctx)
	require.NoError(t, err)
	defer space.Close()
	assert.Equal(t, CaptureNamespace(testCaptureRouter, "openai", "test-embedding-model", testSemanticDimension, 1), space.CaptureNamespace)
	assert.NotEmpty(t, space.Version)
	assert.Equal(t, classifier.ServingVersion(), space.Version, "the space names the phrase bank it was taken from")
	var texts []string
	for _, p := range space.Phrases {
		texts = append(texts, p.Tier+":"+p.Text)
		assert.Len(t, p.Vector, testSemanticDimension)
	}
	sort.Strings(texts)
	assert.Equal(t, []string{"COMPLEX:complex exemplar", "MEDIUM:medium exemplar", "SIMPLE:simple exemplar"}, texts)

	vectors, err := space.Embed(ctx, []string{"simple request", "medium request"})
	require.NoError(t, err)
	assert.Equal(t, [][]float32{{1, 0}, {0, 1}}, vectors)

	var got []string
	now := time.Now()
	require.NoError(t, space.EachCaptured(ctx, now.Add(-time.Hour), now.Add(time.Minute), func(page []CapturedRequest) error {
		for _, r := range page {
			got = append(got, r.LogID)
			assert.Equal(t, []float32{0, 1}, r.Vector)
			assert.WithinDuration(t, now, r.At, time.Minute)
		}
		return nil
	}))
	sort.Strings(got)
	assert.Equal(t, []string{"r1", "r2", "r3"}, got)

	got = nil
	require.NoError(t, space.EachCaptured(ctx, now.Add(-2*time.Hour), now.Add(-time.Hour), func(page []CapturedRequest) error {
		got = append(got, page[0].LogID)
		return nil
	}))
	assert.Empty(t, got, "nothing was captured before the window")
	space.Close()
	space.Close()
}

func TestSuggestionSpaceNeedsAServingGeneration(t *testing.T) {
	classifier := NewSemanticClassifier(context.Background(), nil)
	t.Cleanup(func() { _ = classifier.Close() })
	_, err := classifier.SuggestionSpace(context.Background())
	require.ErrorIs(t, err, ErrNoServingGeneration)
}

// dimensionlessStore is a store that, like Pinecone after a restart, cannot
// read without vectors until CreateNamespace has told it the dimension.
type dimensionlessStore struct {
	vectorstore.VectorStore
	created bool
}

func (s *dimensionlessStore) CreateNamespace(ctx context.Context, namespace string, dimension int, properties map[string]vectorstore.VectorStoreProperties) error {
	s.created = true
	return s.VectorStore.CreateNamespace(ctx, namespace, dimension, properties)
}

func (s *dimensionlessStore) GetAll(ctx context.Context, namespace string, queries []vectorstore.Query, fields []string, cursor *string, limit int64) ([]vectorstore.SearchResult, *string, error) {
	if !s.created && !vectorstore.IncludeVectorsRequested(ctx) {
		return nil, nil, errors.New("dimension not set: CreateNamespace must be called before GetAll")
	}
	return s.VectorStore.GetAll(ctx, namespace, queries, fields, cursor, limit)
}

// An estimate made before anything was captured since a restart counts the
// window, rather than failing on a store that needs the namespace set up
// first.
func TestSuggestionSpaceCountsBeforeTheFirstCapture(t *testing.T) {
	store := &dimensionlessStore{VectorStore: newCaptureTestStore(t)}
	space := &SuggestionSpace{Store: store, CaptureNamespace: CaptureNamespace(testCaptureRouter, "openai", "m", 2, 1), Dimension: 2}
	now := time.Now()
	count, err := space.CountCaptured(context.Background(), now.Add(-time.Hour), now)
	require.NoError(t, err)
	assert.Zero(t, count)
	assert.True(t, store.created)
}

// Testing phrases needs the serving bank and its embedding, not captured
// traffic: a router without a shared capture store can still test phrases,
// while a run, an estimate and a replay keep needing one.
func TestServingSpaceNeedsNoCaptureStore(t *testing.T) {
	classifier := readySemanticClassifier(t) // embedded phrase bank, no capture store
	_, err := classifier.SuggestionSpace(context.Background())
	require.ErrorIs(t, err, ErrNoCaptureStore)

	space, err := classifier.ServingSpace(context.Background())
	require.NoError(t, err)
	defer space.Close()
	assert.Len(t, space.Phrases, 3)
	vectors, err := space.Embed(context.Background(), []string{"simple request"})
	require.NoError(t, err)
	assert.Equal(t, [][]float32{{1, 0}}, vectors)
	require.ErrorIs(t, space.EachCaptured(context.Background(), time.Now().Add(-time.Hour), time.Now(), func([]CapturedRequest) error { return nil }), ErrNoCaptureStore)
}
