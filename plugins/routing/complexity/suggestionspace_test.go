package complexity

import (
	"context"
	"errors"
	"sort"
	"strings"
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

// decisionTestEmbedding embeds into two dimensions and records each batch it
// was asked for.
type decisionTestEmbedding struct {
	batches       [][]string
	refuseBatches bool
}

func (e *decisionTestEmbedding) one(_ context.Context, model *SemanticConfig, text string) ([]float32, error) {
	if model.Provider != "openai" || model.EmbeddingModel != "text-embedding-3-small" {
		return nil, errors.New("unexpected embedding model")
	}
	if strings.Contains(text, "hard") {
		return []float32{0, 1}, nil
	}
	return []float32{1, 0}, nil
}

func (e *decisionTestEmbedding) batch(ctx context.Context, model *SemanticConfig, texts []string) ([][]float32, error) {
	e.batches = append(e.batches, append([]string{}, texts...))
	if e.refuseBatches && len(texts) > 1 {
		return nil, ErrBatchEmbeddingsUnsupported
	}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vector, err := e.one(ctx, model, text)
		if err != nil {
			return nil, err
		}
		out[i] = vector
	}
	return out, nil
}

func decisionTestSpaceConfig(store vectorstore.VectorStore, embedding *decisionTestEmbedding) DecisionSpaceConfig {
	return DecisionSpaceConfig{
		RouterID: testCaptureRouter, Store: store, Provider: "openai", Model: "text-embedding-3-small", MessageHistoryCount: 2,
		Examples: map[string][]string{
			TierSimple:  {"say hi", " ", "say hi"},
			TierComplex: {"a hard proof", "reuse me"},
		},
		Embed: embedding.one, EmbedBatch: embedding.batch,
	}
}

// A decision-model router's examples stand where a semantic router's phrases
// do: embedded with the suggestions model in one batch, in tier order, blank
// ones dropped, each text embedded once however often it is listed, and one
// already known reused. The capture namespace is the router's, named by that
// model, the dimension it returned and the turns the decision model reads.
func TestDecisionSuggestionSpaceEmbedsTheRoutersExamples(t *testing.T) {
	embedding := &decisionTestEmbedding{}
	cfg := decisionTestSpaceConfig(newCaptureTestStore(t), embedding)
	cfg.Known = map[string][]float32{"reuse me": {0.6, 0.8}}
	space, err := NewDecisionSuggestionSpace(context.Background(), cfg)
	require.NoError(t, err)

	assert.Equal(t, [][]string{{"say hi", "a hard proof"}}, embedding.batches)
	assert.Equal(t, []SuggestionPhrase{
		{Tier: TierSimple, Text: "say hi", Vector: []float32{1, 0}},
		{Tier: TierSimple, Text: "say hi", Vector: []float32{1, 0}},
		{Tier: TierComplex, Text: "a hard proof", Vector: []float32{0, 1}},
		{Tier: TierComplex, Text: "reuse me", Vector: []float32{0.6, 0.8}},
	}, space.LivePhrases())
	assert.Equal(t, 2, space.Dimension)
	assert.Equal(t, CaptureNamespace(testCaptureRouter, "openai", "text-embedding-3-small", 2, 2), space.Namespace())
	assert.Equal(t, CaptureTarget{Store: cfg.Store, Namespace: space.Namespace(), Dimension: 2, MessageHistoryCount: 2}, space.Target())

	// A model that takes one input per request is asked one text at a time.
	refusing := &decisionTestEmbedding{refuseBatches: true}
	space, err = NewDecisionSuggestionSpace(context.Background(), decisionTestSpaceConfig(nil, refusing))
	require.NoError(t, err)
	assert.Len(t, space.LivePhrases(), 4)
	vectors, err := space.Embed(context.Background(), []string{"a hard ask", "an easy one"})
	require.NoError(t, err)
	assert.Equal(t, [][]float32{{0, 1}, {1, 0}}, vectors)
}

// A space cannot be built without an embedding model, nor from examples
// whose vectors disagree on their dimension, which a known vector from a
// different model would.
func TestDecisionSuggestionSpaceRefusesWhatItCannotEmbed(t *testing.T) {
	embedding := &decisionTestEmbedding{}
	for name, mutate := range map[string]func(*DecisionSpaceConfig){
		"no model":     func(c *DecisionSpaceConfig) { c.Model = "" },
		"no router":    func(c *DecisionSpaceConfig) { c.RouterID = "" },
		"no embedding": func(c *DecisionSpaceConfig) { c.Embed, c.EmbedBatch = nil, nil },
	} {
		cfg := decisionTestSpaceConfig(nil, embedding)
		mutate(&cfg)
		_, err := NewDecisionSuggestionSpace(context.Background(), cfg)
		assert.ErrorIs(t, err, ErrNoDecisionEmbedding, name)
	}
	cfg := decisionTestSpaceConfig(nil, embedding)
	cfg.Known = map[string][]float32{"reuse me": {1, 0, 0}}
	_, err := NewDecisionSuggestionSpace(context.Background(), cfg)
	assert.ErrorContains(t, err, "disagree on their dimension")
	cfg = decisionTestSpaceConfig(nil, embedding)
	cfg.Examples = map[string][]string{TierSimple: {" "}}
	_, err = NewDecisionSuggestionSpace(context.Background(), cfg)
	assert.ErrorContains(t, err, "no tier examples")
}

// A decision-model router's captured requests are read back with the tier
// its decision model logged for each, from the logs over the same window; a
// request logged without one, or whose log is gone, is left out.
func TestDecisionSuggestionSpaceReadsLoggedTiers(t *testing.T) {
	ctx := context.Background()
	store := newCaptureTestStore(t)
	cfg := decisionTestSpaceConfig(store, &decisionTestEmbedding{})
	var window [2]time.Time
	cfg.LoggedTiers = func(_ context.Context, from, to time.Time) (map[string]string, error) {
		window = [2]time.Time{from, to}
		return map[string]string{"routed-simple": TierSimple, "routed-complex": TierComplex}, nil
	}
	space, err := NewDecisionSuggestionSpace(ctx, cfg)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	capture := NewRequestCapture(nil)
	t.Cleanup(capture.Close)
	var requests []CapturedRequest
	for _, id := range []string{"routed-simple", "routed-complex", "no-tier"} {
		requests = append(requests, CapturedRequest{LogID: id, Vector: []float32{1, 0}, At: now.Add(-time.Minute)})
	}
	ids := []string{"routed-simple", "routed-complex", "no-tier"}
	capture.HoldForBackfill(ids)
	require.NoError(t, capture.WriteBackfilled(ctx, store, space.Namespace(), space.Dimension, requests))

	from := now.Add(-time.Hour)
	got := map[string]string{}
	require.NoError(t, space.EachCaptured(ctx, from, now, func(page []CapturedRequest) error {
		for _, r := range page {
			got[r.LogID] = r.Tier
		}
		return nil
	}))
	assert.Equal(t, map[string]string{"routed-simple": TierSimple, "routed-complex": TierComplex}, got)
	assert.Equal(t, [2]time.Time{from, now}, window, "the tiers are read over the run's window")

	cfg.LoggedTiers = func(context.Context, time.Time, time.Time) (map[string]string, error) {
		return nil, errors.New("logs down")
	}
	failing, err := NewDecisionSuggestionSpace(ctx, cfg)
	require.NoError(t, err)
	assert.ErrorContains(t, failing.EachCaptured(ctx, from, now, func([]CapturedRequest) error { return nil }), "logs down")

	cfg.Store = nil
	storeless, err := NewDecisionSuggestionSpace(ctx, cfg)
	require.NoError(t, err)
	assert.ErrorIs(t, storeless.EachCaptured(ctx, from, now, func([]CapturedRequest) error { return nil }), ErrNoCaptureStore)
}

// A semantic router's space embeds through the same path as a decision
// router's: a model that refuses several inputs in one request is asked one
// text at a time, and a vector of the wrong length is refused.
func TestSuggestionSpaceEmbedsOneTextAtATimeWhenBatchesAreRefused(t *testing.T) {
	embedding := &decisionTestEmbedding{refuseBatches: true}
	model := &SemanticConfig{Provider: "openai", EmbeddingModel: "text-embedding-3-small"}
	space := &SuggestionSpace{Dimension: 2, semantic: model, embed: embedding.one, embedBatch: embedding.batch}
	vectors, err := space.Embed(context.Background(), []string{"a hard ask", "an easy one"})
	require.NoError(t, err)
	assert.Equal(t, [][]float32{{0, 1}, {1, 0}}, vectors)

	space.Dimension = 3
	_, err = space.Embed(context.Background(), []string{"an easy one"})
	assert.ErrorContains(t, err, "does not match the space's dimension 3")
}
