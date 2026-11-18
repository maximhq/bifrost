package complexity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
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
	return embedTexts(ctx, s.semantic, s.embed, s.embedBatch, texts, s.Dimension)
}

// embedTexts embeds texts with a model, in order: in one batch when there is a
// batch function, one text at a time when there is not or when the model
// refuses several inputs in one request. Every vector must be non-empty and,
// when dimension is set, that long.
func embedTexts(ctx context.Context, model *SemanticConfig, embed EmbeddingFunc, embedBatch BatchEmbeddingFunc, texts []string, dimension int) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	var vectors [][]float32
	var err error
	if embedBatch != nil {
		vectors, err = embedBatch(ctx, model, texts)
	}
	if embed != nil && (embedBatch == nil || errors.Is(err, ErrBatchEmbeddingsUnsupported)) {
		vectors, err = make([][]float32, len(texts)), nil
		for i, text := range texts {
			if vectors[i], err = embed(ctx, model, text); err != nil {
				break
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("embedded %d of %d texts", len(vectors), len(texts))
	}
	for _, vector := range vectors {
		if len(vector) == 0 || (dimension > 0 && len(vector) != dimension) {
			return nil, fmt.Errorf("embedding dimension %d does not match the space's dimension %d", len(vector), dimension)
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

// CaptureTarget is where a space's captured requests are kept, and how their
// text is cut from a log, which a backfill from logs writes to.
type CaptureTarget struct {
	Store               vectorstore.VectorStore
	Namespace           string
	Dimension           int
	MessageHistoryCount int
}

// Target is where the space's captured requests are kept.
func (s *SuggestionSpace) Target() CaptureTarget {
	return CaptureTarget{Store: s.Store, Namespace: s.CaptureNamespace, Dimension: s.Dimension, MessageHistoryCount: s.MessageHistoryCount}
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
	return s.Target().count(ctx, from, to)
}

// count counts the requests captured in the target from from through to,
// creating its namespace first; see SuggestionSpace.CountCaptured.
func (t CaptureTarget) count(ctx context.Context, from, to time.Time) (int, error) {
	if t.Store == nil {
		return 0, ErrNoCaptureStore
	}
	if t.Namespace != "" {
		if err := t.Store.CreateNamespace(ctx, t.Namespace, t.Dimension, captureProperties); err != nil {
			return 0, fmt.Errorf("create capture namespace: %w", err)
		}
	}
	return CountCaptured(ctx, t.Store, t.Namespace, from, to)
}

// A decision-model router has no embedding space of its own: it sends each
// request to its decision model, which reads the tiers' definitions, signals
// and examples. Its suggestion runs cluster its requests with an embedding
// model of their own choosing (suggestions.embedding), and its examples are
// embedded with the same model to stand where a semantic router's phrases
// do. Nothing is captured on the request path: a run embeds a sample of the
// router's logged requests first (see the routing plugin's backfill), into a
// capture namespace named like a semantic router's, by the router, the
// embedding model and the slice of each request embedded. Each request is
// read back with the tier the decision model logged for it, which is what a
// run compares its own tier call with.

// LoggedTiers returns the tier the decision model logged for each request a
// router took from from through to, by log ID. A request logged without one
// (the model failed, a session tier stood in) is left out.
type LoggedTiers func(ctx context.Context, from, to time.Time) (map[string]string, error)

// DecisionSpaceConfig is what a decision-model router's suggestion space is
// built from.
type DecisionSpaceConfig struct {
	RouterID            string
	Store               vectorstore.VectorStore // where captured requests are kept; nil without a configured store
	Provider            schemas.ModelProvider   // the suggestions embedding model
	Model               string
	MessageHistoryCount int                  // the user turns the decision model reads: its previous messages and the current one
	Examples            map[string][]string  // tier -> the examples the router classifies with
	Known               map[string][]float32 // example text -> its vector in this model, already embedded; reused rather than embedded again
	Embed               EmbeddingFunc
	EmbedBatch          BatchEmbeddingFunc
	LoggedTiers         LoggedTiers
}

// ErrNoDecisionEmbedding is returned when a decision-model router's
// suggestion space cannot embed: no embedding model is set, or the embedding
// path is not wired yet.
var ErrNoDecisionEmbedding = errors.New("decision-model suggestions need an embedding model to cluster requests with")

// DecisionSuggestionSpace is a decision-model router's suggestion space, held
// for one run: its examples embedded, where its requests are captured, and
// the tiers its decision model logged for them. Unlike a semantic router's,
// it holds no generation, so there is nothing to release.
type DecisionSuggestionSpace struct {
	Store               vectorstore.VectorStore
	CaptureNamespace    string
	Dimension           int
	MessageHistoryCount int
	Examples            []SuggestionPhrase // in tier order, then as configured
	Version             string             // names the examples and model; see DecisionExamplesVersion

	model       *SemanticConfig // the embedding model, as the embedding path takes it
	embed       EmbeddingFunc
	embedBatch  BatchEmbeddingFunc
	loggedTiers LoggedTiers

	// tiers is the last window's logged tiers, kept for the space's life: a
	// run reads its window twice (to sample, then to count the rest in) and
	// its backfill has read the same logs before, so the logs are read once.
	// One entry per request the router logged a tier for in the window.
	tiersMu     sync.Mutex
	tiersWindow [2]time.Time
	tiers       map[string]string
}

// DecisionExamplesVersion names a decision-model router's examples as an
// embedding model sees them, as a semantic router's serving version names
// its phrase bank: a suggestion made against other examples, or another
// model, is shown as made against an earlier version.
func DecisionExamplesVersion(provider schemas.ModelProvider, model string, examples map[string][]string) string {
	h := sha256.New()
	fmt.Fprintf(h, "decision-examples-v1\x00%s\x00%s", provider, model)
	for _, tier := range OrderedTiers() {
		fmt.Fprintf(h, "\x00%s", tier)
		for _, text := range examples[tier] {
			if text = strings.TrimSpace(text); text != "" {
				fmt.Fprintf(h, "\x00%s", text)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// NewDecisionSuggestionSpace embeds the router's examples, those not already
// known, and names the capture namespace by the dimension they come back
// with.
func NewDecisionSuggestionSpace(ctx context.Context, cfg DecisionSpaceConfig) (*DecisionSuggestionSpace, error) {
	if cfg.RouterID == "" || cfg.Provider == "" || cfg.Model == "" || (cfg.Embed == nil && cfg.EmbedBatch == nil) {
		return nil, ErrNoDecisionEmbedding
	}
	space := &DecisionSuggestionSpace{
		Store:               cfg.Store,
		MessageHistoryCount: max(1, cfg.MessageHistoryCount),
		model:               &SemanticConfig{Provider: cfg.Provider, EmbeddingModel: cfg.Model},
		embed:               cfg.Embed,
		embedBatch:          cfg.EmbedBatch,
		loggedTiers:         cfg.LoggedTiers,
		Version:             DecisionExamplesVersion(cfg.Provider, cfg.Model, cfg.Examples),
	}
	var missing []string
	queued := map[string]struct{}{}
	for _, tier := range OrderedTiers() {
		for _, text := range cfg.Examples[tier] {
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			space.Examples = append(space.Examples, SuggestionPhrase{Tier: tier, Text: text, Vector: cfg.Known[text]})
			if _, done := queued[text]; cfg.Known[text] == nil && !done {
				queued[text] = struct{}{}
				missing = append(missing, text)
			}
		}
	}
	if len(space.Examples) == 0 {
		return nil, errors.New("the router has no tier examples to suggest against")
	}
	embedded := make(map[string][]float32, len(missing))
	if len(missing) > 0 {
		vectors, err := space.Embed(ctx, missing)
		if err != nil {
			return nil, fmt.Errorf("embed the router's tier examples: %w", err)
		}
		for i, text := range missing {
			embedded[text] = vectors[i]
		}
	}
	for i := range space.Examples {
		if space.Examples[i].Vector == nil {
			space.Examples[i].Vector = embedded[space.Examples[i].Text]
		}
		if dimension := len(space.Examples[i].Vector); space.Dimension == 0 {
			space.Dimension = dimension
		} else if dimension != space.Dimension {
			return nil, fmt.Errorf("example vectors disagree on their dimension: %d and %d", space.Dimension, dimension)
		}
	}
	if space.Dimension == 0 {
		return nil, errors.New("the embedding model returned empty vectors")
	}
	space.CaptureNamespace = CaptureNamespace(cfg.RouterID, cfg.Provider, cfg.Model, space.Dimension, space.MessageHistoryCount)
	return space, nil
}

// Embed embeds texts with the space's model, in order.
func (s *DecisionSuggestionSpace) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return embedTexts(ctx, s.model, s.embed, s.embedBatch, texts, s.Dimension)
}

// EachCaptured reads the requests captured from from through to, each with
// the tier the decision model logged for it. One logged without a tier, or
// whose log is gone, is left out: there is no call of the router's to compare
// with.
func (s *DecisionSuggestionSpace) EachCaptured(ctx context.Context, from, to time.Time, fn func([]CapturedRequest) error) error {
	if s.Store == nil {
		return ErrNoCaptureStore
	}
	tiers, err := s.windowTiers(ctx, from, to)
	if err != nil {
		return err
	}
	return EachCaptured(ctx, s.Store, s.CaptureNamespace, from, to, func(page []CapturedRequest) error {
		kept := page[:0]
		for _, r := range page {
			if tier := tiers[r.LogID]; tier != "" {
				r.Tier = tier
				kept = append(kept, r)
			}
		}
		if len(kept) == 0 {
			return nil
		}
		return fn(kept)
	})
}

// windowTiers returns the logged tiers of a window, read from the logs the
// first time the window is asked for, or as its backfill recorded them.
func (s *DecisionSuggestionSpace) windowTiers(ctx context.Context, from, to time.Time) (map[string]string, error) {
	s.tiersMu.Lock()
	defer s.tiersMu.Unlock()
	if s.tiers != nil && s.tiersWindow == [2]time.Time{from, to} {
		return s.tiers, nil
	}
	if s.loggedTiers == nil {
		return nil, errors.New("no logs to read the decision model's tiers from")
	}
	tiers, err := s.loggedTiers(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("read the router's logged tiers: %w", err)
	}
	s.tiers, s.tiersWindow = tiers, [2]time.Time{from, to}
	return tiers, nil
}

// RecordLoggedTiers keeps a window's logged tiers that were read already, by
// a backfill that paged the same logs, so reading the window does not page
// them again.
func (s *DecisionSuggestionSpace) RecordLoggedTiers(from, to time.Time, tiers map[string]string) {
	s.tiersMu.Lock()
	defer s.tiersMu.Unlock()
	s.tiers, s.tiersWindow = tiers, [2]time.Time{from, to}
}

// CountCaptured counts the requests captured from from through to; see
// SuggestionSpace.CountCaptured. Every request a decision-model router's
// space holds was backfilled because its decision model logged a tier for
// it, so this is what a run reads unless logs were deleted since.
func (s *DecisionSuggestionSpace) CountCaptured(ctx context.Context, from, to time.Time) (int, error) {
	return s.Target().count(ctx, from, to)
}

// ServedVersion names the examples the space was built from.
func (s *DecisionSuggestionSpace) ServedVersion() string { return s.Version }

// Namespace names the space's captured requests; suggestions record it as
// their embedding signature.
func (s *DecisionSuggestionSpace) Namespace() string { return s.CaptureNamespace }

// LivePhrases returns the router's examples, which stand where a semantic
// router's phrases do.
func (s *DecisionSuggestionSpace) LivePhrases() []SuggestionPhrase { return s.Examples }

// Target is where the space's captured requests are kept.
func (s *DecisionSuggestionSpace) Target() CaptureTarget {
	return CaptureTarget{Store: s.Store, Namespace: s.CaptureNamespace, Dimension: s.Dimension, MessageHistoryCount: s.MessageHistoryCount}
}
