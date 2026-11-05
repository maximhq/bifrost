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
	Store               vectorstore.VectorStore
	CaptureNamespace    string
	Dimension           int
	MessageHistoryCount int
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
func (c *SemanticClassifier) SuggestionSpace(ctx context.Context) (*SuggestionSpace, error) {
	c.mu.Lock()
	generation := c.active
	if c.closed || generation == nil || generation.embed == nil || generation.captureNamespace == "" {
		c.mu.Unlock()
		return nil, ErrNoServingGeneration
	}
	embedBatch := c.embedBatch
	if isLocalChromemStore(generation.store) {
		if c.localInFlight == nil {
			c.localInFlight = make(map[*semanticGeneration]int)
		}
		c.localInFlight[generation]++
	}
	c.mu.Unlock()

	space := &SuggestionSpace{
		Store:               generation.store,
		CaptureNamespace:    generation.captureNamespace,
		Dimension:           generation.dimension,
		MessageHistoryCount: generation.semantic.MessageHistoryCount,
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
