package complexity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/framework/vectorstore"
)

// ErrNoServingGeneration is returned when there is no warmed phrase bank to
// suggest against: the semantic classifier is off, warming, or failed.
var ErrNoServingGeneration = errors.New("the semantic router has no warmed phrase bank to suggest against")

// SuggestionPhrase is one live phrase with the vector it is served with.
type SuggestionPhrase struct {
	Tier   string
	Text   string
	Vector []float32
}

// SuggestionSpace is the serving embedding space, held for one suggestion
// run: where its captured requests are, the phrases they were routed
// against, and the embedding function that put both there. A run must use
// one space throughout, so a reload during the run changes nothing it reads;
// Close releases the hold.
type SuggestionSpace struct {
	Store               vectorstore.VectorStore // where captured requests are; nil without a configured store
	CaptureNamespace    string
	Dimension           int
	MessageHistoryCount int
	MinSimilarity       float64 // the router's acceptance floor; 0 accepts any nearest phrase
	Version             string  // the phrase bank served, as ServingVersion names it
	Phrases             []SuggestionPhrase

	semantic   *SemanticConfig
	embed      EmbeddingFunc
	embedBatch BatchEmbeddingFunc
	release    func()
}

// suggestionPhrasePage bounds one phrase read; the bank holds at most a few
// hundred phrases.
const suggestionPhrasePage = 500

// SuggestionSpace snapshots the serving generation. The caller must Close it.
// Phrases are read from the generation's own store, captured requests from
// Bifrost's configured store: capture never writes to the embedded one (see
// captureTarget), so an embedded phrase bank still has its traffic there.
func (c *SemanticClassifier) SuggestionSpace(ctx context.Context) (*SuggestionSpace, error) {
	return c.snapshotSpace(ctx, true)
}

// ServingSpace snapshots the serving generation's phrase bank and embedding
// function only, for testing phrases against it: no captured traffic is read,
// so no capture store is needed, and EachCaptured and CountCaptured answer
// ErrNoCaptureStore. The caller must Close it.
func (c *SemanticClassifier) ServingSpace(ctx context.Context) (*SuggestionSpace, error) {
	return c.snapshotSpace(ctx, false)
}

func (c *SemanticClassifier) snapshotSpace(ctx context.Context, needCapture bool) (*SuggestionSpace, error) {
	c.mu.Lock()
	generation := c.active
	if c.closed || generation == nil || generation.embed == nil || (needCapture && generation.captureNamespace == "") {
		c.mu.Unlock()
		return nil, ErrNoServingGeneration
	}
	embedBatch := c.embedBatch
	var captureStore vectorstore.VectorStore
	if needCapture {
		if captureStore = c.captureStoreLocked(); captureStore == nil {
			c.mu.Unlock()
			return nil, ErrNoCaptureStore
		}
	}
	if isLocalChromemStore(generation.store) {
		if c.localInFlight == nil {
			c.localInFlight = make(map[*semanticGeneration]int)
		}
		c.localInFlight[generation]++
	}
	c.mu.Unlock()

	space := &SuggestionSpace{
		Store:               captureStore,
		CaptureNamespace:    generation.captureNamespace,
		Dimension:           generation.dimension,
		MessageHistoryCount: generation.semantic.MessageHistoryCount,
		MinSimilarity:       generation.semantic.MinSimilarity,
		Version:             servingVersion(generation.namespace),
		semantic:            cloneSemanticConfig(generation.semantic),
		embed:               generation.embed,
		embedBatch:          embedBatch,
		release:             func() { c.releaseGeneration(generation) },
	}
	phrases, err := readServingPhrases(ctx, generation)
	if err != nil {
		space.Close()
		return nil, err
	}
	space.Phrases = phrases
	return space, nil
}

// readServingPhrases reads the generation's phrase vectors back, naming each
// from the generation's own index, so the vectors are exactly the ones
// requests were routed against.
func readServingPhrases(ctx context.Context, generation *semanticGeneration) ([]SuggestionPhrase, error) {
	ctx = vectorstore.WithIncludeVectors(ctx)
	queries := []vectorstore.Query{{Field: semanticMetadataKind, Operator: vectorstore.QueryOperatorEqual, Value: semanticMetadataKindExample}}
	var phrases []SuggestionPhrase
	var cursor *string
	for {
		results, next, err := generation.store.GetAll(ctx, generation.namespace, queries, []string{semanticMetadataTier}, cursor, suggestionPhrasePage)
		if err != nil {
			return nil, fmt.Errorf("read the serving phrase bank: %w", err)
		}
		for _, r := range results {
			text, known := generation.exemplars[r.ID]
			tier, _ := r.Properties[semanticMetadataTier].(string)
			if !known || len(r.Vector) == 0 || !isComplexityTier(tier) {
				continue
			}
			phrases = append(phrases, SuggestionPhrase{Tier: tier, Text: text, Vector: r.Vector})
		}
		if next == nil || *next == "" || len(results) == 0 {
			return phrases, nil
		}
		cursor = next
	}
}

// Embed embeds texts in the space, in order.
func (s *SuggestionSpace) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	var vectors [][]float32
	if s.embedBatch != nil {
		batch, err := s.embedBatch(ctx, s.semantic, texts)
		if err != nil {
			return nil, err
		}
		vectors = batch
	} else {
		vectors = make([][]float32, len(texts))
		for i, text := range texts {
			vector, err := s.embed(ctx, s.semantic, text)
			if err != nil {
				return nil, err
			}
			vectors[i] = vector
		}
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("embedded %d of %d texts", len(vectors), len(texts))
	}
	for _, vector := range vectors {
		if len(vector) != s.Dimension {
			return nil, fmt.Errorf("embedding dimension %d does not match the serving dimension %d", len(vector), s.Dimension)
		}
	}
	return vectors, nil
}

// EachCaptured reads the space's captured requests from from through to; see the
// package function of the same name.
func (s *SuggestionSpace) EachCaptured(ctx context.Context, from, to time.Time, fn func([]CapturedRequest) error) error {
	if s.Store == nil {
		return ErrNoCaptureStore
	}
	return EachCaptured(ctx, s.Store, s.CaptureNamespace, from, to, fn)
}

// Close releases the hold on the generation. It is safe to call twice.
func (s *SuggestionSpace) Close() {
	if s == nil || s.release == nil {
		return
	}
	s.release()
	s.release = nil
}

// Namespace names the space's captured requests; suggestions record it as
// their embedding signature.
func (s *SuggestionSpace) Namespace() string { return s.CaptureNamespace }

// LivePhrases returns the serving phrases.
func (s *SuggestionSpace) LivePhrases() []SuggestionPhrase { return s.Phrases }

// ServedVersion names the phrase bank the space was taken from.
func (s *SuggestionSpace) ServedVersion() string { return s.Version }

// CountCaptured counts the space's captured requests from from through to.
//
// The namespace is created first, as the capture writer does before its first
// write. Some stores need that before they can count: Pinecone keeps the
// dimension a read without vectors queries with in memory, set only by
// CreateNamespace, so after a restart an estimate made before anything was
// captured would fail. Creating a namespace that exists changes nothing.
func (s *SuggestionSpace) CountCaptured(ctx context.Context, from, to time.Time) (int, error) {
	if s.Store == nil {
		return 0, ErrNoCaptureStore
	}
	if s.CaptureNamespace != "" {
		if err := s.Store.CreateNamespace(ctx, s.CaptureNamespace, s.Dimension, captureProperties); err != nil {
			return 0, fmt.Errorf("create capture namespace: %w", err)
		}
	}
	return CountCaptured(ctx, s.Store, s.CaptureNamespace, from, to)
}
