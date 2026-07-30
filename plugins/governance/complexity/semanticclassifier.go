package complexity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// SemanticVectorStoreNamespace is the prefix for immutable, fingerprinted
// complexity-routing generations. It isolates routing exemplars from every
// other VectorStore consumer, including the semantic cache plugin.
const SemanticVectorStoreNamespace = "BifrostComplexityRouter"

const (
	semanticMetadataTier        = "tier"
	semanticMetadataKind        = "kind"
	semanticMetadataFingerprint = "fingerprint"
	semanticMetadataKindExample = "example"
	semanticMetadataKindMarker  = "marker"
	semanticWarmupBatchSize     = 32
)

// ErrBatchEmbeddingsUnsupported lets an embedding adapter report that a
// provider/model accepted a multi-input request but did not return one vector
// per input. Warmup then safely falls back to one request per exemplar.
var ErrBatchEmbeddingsUnsupported = errors.New("batch embeddings are unsupported")

// SemanticStatus describes whether the semantic classifier can serve routing
// requests for the current complexity configuration.
type SemanticStatus string

const (
	// SemanticStatusDisabled means semantic routing is not configured.
	SemanticStatusDisabled SemanticStatus = "disabled"
	// SemanticStatusWarming means exemplar embeddings are being prepared.
	SemanticStatusWarming SemanticStatus = "warming"
	// SemanticStatusReady means semantic requests can query the current exemplars.
	SemanticStatusReady SemanticStatus = "ready"
	// SemanticStatusFailed means the desired generation failed to warm. The
	// previous generation may still be serving when ServingPrevious is true.
	SemanticStatusFailed SemanticStatus = "failed"
)

// SemanticStatusInfo is the safe runtime state exposed to Governance handlers
// and UI clients; it never contains prompts, embeddings, or provider secrets.
type SemanticStatusInfo struct {
	State           SemanticStatus `json:"state"`
	Loaded          int            `json:"loaded"`
	Total           int            `json:"total"`
	ServingPrevious bool           `json:"serving_previous,omitempty"`
	Error           string         `json:"error,omitempty"`
}

// SemanticResult is the tier selected by the nearest labelled exemplar.
// Score is the VectorStore backend's similarity value, not a lexical score.
type SemanticResult struct {
	Tier  string
	Score float64
	// MatchedExemplar is the tier phrase this request landed on. Tier and Score
	// report what the classifier decided and how confidently; without the phrase
	// itself a routing log cannot distinguish a sound match from an accidental
	// one, which is the question anyone reading that log is actually asking.
	// Empty when the serving generation carries no exemplar index or the backend
	// returns a record ID it was not warmed with.
	MatchedExemplar string
}

// EmbeddingFunc creates one embedding for semantic complexity classification.
// The Governance package supplies the adapter so this package has no provider client.
type EmbeddingFunc func(context.Context, *SemanticConfig, string) ([]float32, error)

// BatchEmbeddingFunc creates one ordered embedding per input. Warmup uses it in
// bounded batches when available and retains EmbeddingFunc as the compatibility
// fallback for providers or models without true multi-input support.
type BatchEmbeddingFunc func(context.Context, *SemanticConfig, []string) ([][]float32, error)

// semanticGeneration is the immutable serving snapshot selected only after its
// namespace has a completion marker. Its semantic config and measured vector
// width must stay paired with the vectors produced by its provider and model.
type semanticGeneration struct {
	store     vectorstore.VectorStore
	namespace string
	semantic  *SemanticConfig
	dimension int
	embed     EmbeddingFunc
	// exemplars maps this generation's record IDs back to the phrases they were
	// built from. Keeping the index in process rather than as a fourth stored
	// property preserves semanticVectorStoreProperties' rule that no phrase text
	// reaches the backend, and costs no re-warm: the IDs derive from the same
	// fingerprint the namespace does, so an already-warmed store stays valid.
	// Published once with the generation and never mutated, so Classify may read
	// it after releasing the mutex.
	exemplars map[string]string
}

// SemanticClassifier embeds the shared tier phrase lists and classifies a
// request against their exact nearest neighbour in an isolated VectorStore namespace.
type SemanticClassifier struct {
	ctx    context.Context
	logger schemas.Logger

	mu               sync.Mutex
	configuredStore  vectorstore.VectorStore
	ownedStore       vectorstore.VectorStore
	embed            EmbeddingFunc
	embedBatch       BatchEmbeddingFunc
	config           *AnalyzerConfig
	store            vectorstore.VectorStore
	active           *semanticGeneration
	status           SemanticStatusInfo
	revision         uint64
	warmCancel       context.CancelFunc
	warming          bool
	embeddedInFlight map[string]int
	wg               sync.WaitGroup
}

// NewSemanticClassifier creates a disabled classifier. Callers configure it
// and supply an embedding function independently because the Bifrost client is
// wired after Governance construction.
func NewSemanticClassifier(ctx context.Context, logger schemas.Logger) *SemanticClassifier {
	if ctx == nil {
		ctx = context.Background()
	}
	return &SemanticClassifier{
		ctx:    ctx,
		logger: logger,
		status: SemanticStatusInfo{State: SemanticStatusDisabled},
	}
}

// Configure snapshots the current analyzer configuration and starts a fresh
// warmup when an embedding function and usable store are available.
func (c *SemanticClassifier) Configure(config *AnalyzerConfig) {
	c.mu.Lock()
	c.config = cloneAnalyzerConfig(config)
	c.revision++
	c.resetForCurrentConfigLocked()
	c.requestWarmupLocked()
	c.mu.Unlock()
}

// SetConfiguredStore supplies Bifrost's configured shared VectorStore. Embedded
// mode ignores it, auto prefers it, and external requires it.
func (c *SemanticClassifier) SetConfiguredStore(store vectorstore.VectorStore) {
	c.mu.Lock()
	c.configuredStore = store
	c.revision++
	c.resetForCurrentConfigLocked()
	c.requestWarmupLocked()
	c.mu.Unlock()
}

// SetEmbeddingFunc supplies or clears the post-construction embedding adapter.
// Changing it restarts preparation for the current configuration.
func (c *SemanticClassifier) SetEmbeddingFunc(embed EmbeddingFunc) {
	c.SetEmbeddingFunctions(embed, nil)
}

// SetEmbeddingFunctions supplies the request-time single-input adapter and the
// optional warmup batch adapter in one state transition.
func (c *SemanticClassifier) SetEmbeddingFunctions(embed EmbeddingFunc, embedBatch BatchEmbeddingFunc) {
	c.mu.Lock()
	c.embed = embed
	c.embedBatch = embedBatch
	c.revision++
	c.resetForCurrentConfigLocked()
	c.requestWarmupLocked()
	c.mu.Unlock()
}

// ValidateConfig rejects semantic modes that cannot be satisfied by the current
// process dependencies before a handler persists the configuration.
func (c *SemanticClassifier) ValidateConfig(config *AnalyzerConfig) error {
	resolved, err := ValidateAndNormalize(config)
	if err != nil {
		return err
	}
	if resolved == nil || resolved.Semantic == nil {
		return nil
	}
	if resolved.Semantic.VectorStore != configstore.ComplexitySemanticVectorStoreExternal {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.configuredStore == nil {
		return fmt.Errorf("semantic vector_store %q requires a configured Bifrost VectorStore", configstore.ComplexitySemanticVectorStoreExternal)
	}
	return nil
}

// Status returns a stable snapshot of the current semantic readiness state.
func (c *SemanticClassifier) Status() SemanticStatusInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Fallback returns the configured behavior when semantic classification cannot
// serve the current request. Disabled semantic routing always falls back to lexical.
func (c *SemanticClassifier) Fallback() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.config == nil || c.config.Semantic == nil {
		return configstore.ComplexitySemanticFallbackLexical
	}
	return c.config.Semantic.Fallback
}

// IsConfigured reports whether semantic classification is enabled in the
// current complexity configuration, independently from warmup readiness.
func (c *SemanticClassifier) IsConfigured() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.config != nil && c.config.Semantic != nil
}

// Classify embeds text once and returns the tier from the global nearest
// exemplar in the last completely warmed generation. A newer desired
// generation can warm or fail without disrupting this serving snapshot.
func (c *SemanticClassifier) Classify(ctx context.Context, text string) (*SemanticResult, error) {
	c.mu.Lock()
	if c.config == nil || c.config.Semantic == nil || c.active == nil || c.active.embed == nil {
		c.mu.Unlock()
		return nil, nil
	}
	semantic := cloneSemanticConfig(c.active.semantic)
	generation := c.active
	dimension := generation.dimension
	store := generation.store
	namespace := generation.namespace
	embed := generation.embed
	exemplars := generation.exemplars
	embeddedGeneration := c.isOwnedStoreLocked(store)
	if embeddedGeneration {
		if c.embeddedInFlight == nil {
			c.embeddedInFlight = make(map[string]int)
		}
		c.embeddedInFlight[namespace]++
	}
	c.mu.Unlock()
	if embeddedGeneration {
		defer c.releaseGeneration(generation)
	}

	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	embedding, err := embed(ctx, semantic, text)
	if err != nil {
		return nil, fmt.Errorf("embed complexity input: %w", err)
	}
	if len(embedding) != dimension {
		return nil, fmt.Errorf("embedding dimension %d does not match the active semantic generation dimension %d", len(embedding), dimension)
	}

	results, err := store.GetNearest(ctx, namespace, embedding,
		[]vectorstore.Query{{Field: semanticMetadataKind, Operator: vectorstore.QueryOperatorEqual, Value: semanticMetadataKindExample}},
		[]string{semanticMetadataTier}, -1, 1)
	if err != nil {
		return nil, fmt.Errorf("query complexity exemplars: %w", err)
	}
	if len(results) == 0 || results[0].Score == nil {
		return nil, nil
	}
	tier, ok := results[0].Properties[semanticMetadataTier].(string)
	if !ok || !isComplexityTier(tier) {
		return nil, fmt.Errorf("nearest complexity exemplar has invalid tier metadata")
	}
	return &SemanticResult{
		Tier:            tier,
		Score:           *results[0].Score,
		MatchedExemplar: exemplars[results[0].ID],
	}, nil
}

// Close cancels active warmup and waits for the classifier's single worker to
// exit. It only closes the embedded store owned by this classifier.
func (c *SemanticClassifier) Close() error {
	c.mu.Lock()
	if c.warmCancel != nil {
		c.warmCancel()
	}
	ownedStore := c.ownedStore
	c.mu.Unlock()
	c.wg.Wait()
	if ownedStore == nil {
		return nil
	}
	return ownedStore.Close(context.Background(), SemanticVectorStoreNamespace)
}

// resetForCurrentConfigLocked recalculates readiness after a dependency or
// configuration change. The caller must hold c.mu.
func (c *SemanticClassifier) resetForCurrentConfigLocked() {
	if c.warmCancel != nil {
		c.warmCancel()
		c.warmCancel = nil
	}
	c.store = nil
	if c.config == nil || c.config.Semantic == nil {
		previous := c.active
		c.active = nil
		c.status = SemanticStatusInfo{State: SemanticStatusDisabled}
		if previous != nil {
			c.cleanupEmbeddedNamespaceLocked(previous.store, previous.namespace)
		}
		return
	}

	semantic := c.config.Semantic
	c.status = SemanticStatusInfo{
		State:           SemanticStatusWarming,
		Total:           len(semanticExemplars(c.config)),
		ServingPrevious: c.active != nil,
	}
	// Governance dependencies are injected after plugin construction. Waiting
	// for the embedding adapter prevents auto mode from creating an embedded
	// store that is immediately superseded by the configured shared store.
	if c.embed == nil {
		return
	}
	store, err := c.resolveStoreLocked(semantic)
	if err != nil {
		c.status.State = SemanticStatusFailed
		c.status.Error = "semantic classifier is unavailable; check server logs"
		c.logWarmupError(err)
		return
	}
	c.store = store
}

// requestWarmupLocked starts the single serial warmup worker when the current
// config can be embedded. The caller must hold c.mu.
func (c *SemanticClassifier) requestWarmupLocked() {
	if c.config == nil || c.config.Semantic == nil || c.store == nil || c.embed == nil {
		return
	}
	if c.warming {
		return
	}
	c.warming = true
	c.wg.Add(1)
	go c.runWarmupWorker()
}

// runWarmupWorker serializes namespace mutation so cancelled generations cannot
// write vectors into the namespace selected by a newer configuration.
func (c *SemanticClassifier) runWarmupWorker() {
	defer c.wg.Done()
	for {
		c.mu.Lock()
		if c.config == nil || c.config.Semantic == nil || c.store == nil || c.embed == nil {
			c.warming = false
			c.mu.Unlock()
			return
		}
		revision := c.revision
		config := cloneAnalyzerConfig(c.config)
		store := c.store
		embed := c.embed
		embedBatch := c.embedBatch
		warmCtx, cancel := context.WithCancel(c.ctx)
		c.warmCancel = cancel
		c.status = SemanticStatusInfo{
			State:           SemanticStatusWarming,
			Total:           len(semanticExemplars(config)),
			ServingPrevious: c.active != nil,
		}
		c.mu.Unlock()

		loaded, namespace, err := warmSemanticExemplars(warmCtx, store, config, embed, embedBatch)
		cancel()

		c.mu.Lock()
		if c.revision != revision {
			c.cleanupEmbeddedNamespaceLocked(store, namespace)
			c.mu.Unlock()
			continue
		}
		c.warmCancel = nil
		c.warming = false
		if err != nil {
			c.status = SemanticStatusInfo{
				State:           SemanticStatusFailed,
				Loaded:          loaded,
				Total:           len(semanticExemplars(config)),
				ServingPrevious: c.active != nil,
				Error:           "semantic warmup failed; check server logs",
			}
			c.logWarmupError(err)
			c.cleanupEmbeddedNamespaceLocked(store, namespace)
		} else {
			previous := c.active
			c.active = &semanticGeneration{
				store:     store,
				namespace: namespace,
				semantic:  cloneSemanticConfig(config.Semantic),
				dimension: dimension,
				embed:     embed,
				exemplars: semanticExemplarIndex(config, dimension),
			}
			c.status = SemanticStatusInfo{State: SemanticStatusReady, Loaded: loaded, Total: len(semanticExemplars(config))}
			if previous != nil {
				c.cleanupEmbeddedNamespaceLocked(previous.store, previous.namespace)
			}
		}
		c.mu.Unlock()
		return
	}
}

// releaseGeneration drops the request's hold on an embedded generation. A
// retired namespace is removed as soon as its final classification finishes.
func (c *SemanticClassifier) releaseGeneration(generation *semanticGeneration) {
	if generation == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.isOwnedStoreLocked(generation.store) {
		return
	}
	if count := c.embeddedInFlight[generation.namespace]; count > 1 {
		c.embeddedInFlight[generation.namespace] = count - 1
		return
	}
	delete(c.embeddedInFlight, generation.namespace)
	c.cleanupEmbeddedNamespaceLocked(generation.store, generation.namespace)
}

// cleanupEmbeddedNamespaceLocked removes a private Chromem generation only
// when no request can still query it. Shared stores are intentionally retained:
// another Bifrost replica may still serve the previous generation.
func (c *SemanticClassifier) cleanupEmbeddedNamespaceLocked(store vectorstore.VectorStore, namespace string) {
	if namespace == "" || !c.isOwnedStoreLocked(store) {
		return
	}
	if c.active != nil && c.isOwnedStoreLocked(c.active.store) && c.active.namespace == namespace {
		return
	}
	if c.embeddedInFlight[namespace] > 0 {
		return
	}
	if err := c.ownedStore.DeleteNamespace(context.Background(), namespace); err != nil && c.logger != nil {
		c.logger.Warn("[Governance] Failed to clean retired semantic complexity namespace %s: %v", namespace, err)
	}
	delete(c.embeddedInFlight, namespace)
}

func (c *SemanticClassifier) isOwnedStoreLocked(store vectorstore.VectorStore) bool {
	return c.ownedStore != nil && store == c.ownedStore
}

// logWarmupError records provider and backend details without exposing them
// through the public semantic classifier status endpoint.
func (c *SemanticClassifier) logWarmupError(err error) {
	if c.logger != nil {
		c.logger.Error("[Governance] Semantic complexity warmup failed: %v", err)
	}
}

// resolveStoreLocked selects the backing store without ever taking ownership of
// Bifrost's configured shared store. The caller must hold c.mu.
func (c *SemanticClassifier) resolveStoreLocked(semantic *SemanticConfig) (vectorstore.VectorStore, error) {
	switch semantic.VectorStore {
	case configstore.ComplexitySemanticVectorStoreEmbedded:
		return c.embeddedStoreLocked()
	case configstore.ComplexitySemanticVectorStoreAuto:
		if c.configuredStore != nil {
			return c.configuredStore, nil
		}
		return c.embeddedStoreLocked()
	case configstore.ComplexitySemanticVectorStoreExternal:
		if c.configuredStore == nil {
			return nil, fmt.Errorf("semantic vector_store %q requires a configured Bifrost VectorStore", semantic.VectorStore)
		}
		return c.configuredStore, nil
	default:
		return nil, fmt.Errorf("unsupported semantic vector_store %q", semantic.VectorStore)
	}
}

// embeddedStoreLocked creates the private in-process Chromem store once. The
// caller must hold c.mu.
func (c *SemanticClassifier) embeddedStoreLocked() (vectorstore.VectorStore, error) {
	if c.ownedStore != nil {
		return c.ownedStore, nil
	}
	store, err := vectorstore.NewVectorStore(c.ctx, &vectorstore.Config{
		Enabled: true,
		Type:    vectorstore.VectorStoreTypeChromem,
		Config:  vectorstore.ChromemConfig{},
	}, c.logger)
	if err != nil {
		return nil, fmt.Errorf("create embedded semantic VectorStore: %w", err)
	}
	c.ownedStore = store
	return store, nil
}

// warmSemanticExemplars ensures one complete labelled vector generation is
// present before it writes the marker that permits a persistent store reuse.
func warmSemanticExemplars(
	ctx context.Context,
	store vectorstore.VectorStore,
	config *AnalyzerConfig,
	embed EmbeddingFunc,
	embedBatch BatchEmbeddingFunc,
) (int, string, int, error) {
	if config == nil || config.Semantic == nil {
		return 0, "", 0, nil
	}
	exemplars := semanticExemplars(config)
	if len(exemplars) == 0 {
		return 0, "", 0, fmt.Errorf("semantic complexity classifier has no exemplars")
	}

	// Vectors are reused only for the same provider/model. Their common width is
	// the runtime dimension for this generation, so no operator-entered width is
	// needed in config.json or the management API.
	cache.useIdentity(semanticEmbeddingIdentity(config.Semantic))
	vectors := make([][]float32, len(exemplars))
	pending := make([]int, 0, len(exemplars))
	dimension := 0
	for index, exemplar := range exemplars {
		if vector, ok := cache.get(exemplar.Phrase); ok {
			vectors[index] = vector
			dimension = len(vector)
			continue
		}
		pending = append(pending, index)
	}
	if dimension == 0 {
		// Namespace creation needs a width. Prefer the first warmup batch so
		// providers that support batching do not pay an extra single-input call.
		if embedBatch != nil && len(pending) > 1 {
			batchEnd := min(len(pending), semanticWarmupBatchSize)
			batch := pending[:batchEnd]
			phrases := make([]string, len(batch))
			for index, exemplarIndex := range batch {
				phrases[index] = exemplars[exemplarIndex].Phrase
			}
			embeddings, err := embedBatch(ctx, config.Semantic, phrases)
			if err == nil && len(embeddings) != len(batch) {
				err = fmt.Errorf("%w: expected %d vectors, got %d", ErrBatchEmbeddingsUnsupported, len(batch), len(embeddings))
			}
			if err != nil && !errors.Is(err, ErrBatchEmbeddingsUnsupported) {
				return 0, "", 0, fmt.Errorf("detect semantic embedding dimension: %w", err)
			}
			if errors.Is(err, ErrBatchEmbeddingsUnsupported) {
				embedBatch = nil
			} else {
				dimension = len(embeddings[0])
				for index, exemplarIndex := range batch {
					if len(embeddings[index]) != dimension {
						return 0, "", dimension, fmt.Errorf("%s exemplar %d returned dimension %d, expected %d", strings.ToLower(exemplars[exemplarIndex].Tier), exemplarIndex+1, len(embeddings[index]), dimension)
					}
					vectors[exemplarIndex] = embeddings[index]
					cache.put(exemplars[exemplarIndex].Phrase, embeddings[index])
				}
				pending = pending[batchEnd:]
			}
		}
		if dimension == 0 {
			// The scalar result is retained as useful warmup work rather than a
			// throwaway probe request.
			first := pending[0]
			embedding, err := embed(ctx, config.Semantic, exemplars[first].Phrase)
			if err != nil {
				return 0, "", 0, fmt.Errorf("detect semantic embedding dimension: %w", err)
			}
			dimension = len(embedding)
			vectors[first] = embedding
			cache.put(exemplars[first].Phrase, embedding)
			pending = pending[1:]
		}
	}
	if dimension < 2 {
		return 0, "", dimension, fmt.Errorf("semantic embedding dimension must be at least 2, got %d", dimension)
	}

	fingerprint := semanticFingerprint(config, exemplars, dimension)
	namespace := semanticGenerationNamespace(fingerprint)
	markerID := semanticMarkerID(fingerprint)
	// Every VectorStore implementation treats CreateNamespace as idempotent.
	// Creating first makes a brand-new generation work on Qdrant and Weaviate,
	// whose reads return backend-specific errors for missing namespaces.
	if err := store.CreateNamespace(ctx, namespace, dimension, semanticVectorStoreProperties); err != nil {
		return 0, namespace, dimension, fmt.Errorf("create complexity generation namespace: %w", err)
	}
	markers, err := store.GetChunks(ctx, namespace, []string{markerID})
	if err == nil && len(markers) > 0 {
		if markers[0].Properties[semanticMetadataFingerprint] == fingerprint {
			return len(exemplars), namespace, dimension, nil
		}
	} else if err != nil && !errors.Is(err, vectorstore.ErrNotFound) {
		return 0, namespace, fmt.Errorf("check complexity warmup marker: %w", err)
	}

	var markerEmbedding []float32
	for batchStart := 0; batchStart < len(exemplars); batchStart += semanticWarmupBatchSize {
		batchEnd := batchStart + semanticWarmupBatchSize
		if batchEnd > len(exemplars) {
			batchEnd = len(exemplars)
		}
		if err := ctx.Err(); err != nil {
			return batchStart, namespace, err
		}

		batch := exemplars[batchStart:batchEnd]
		var embeddings [][]float32
		if embedBatch != nil && len(batch) > 1 {
			phrases := make([]string, len(batch))
			for index, exemplar := range batch {
				phrases[index] = exemplar.Phrase
			}
			var err error
			embeddings, err = embedBatch(ctx, config.Semantic, phrases)
			if err == nil && len(embeddings) != len(batch) {
				err = fmt.Errorf("%w: expected %d vectors, got %d", ErrBatchEmbeddingsUnsupported, len(batch), len(embeddings))
			}
			if err != nil && !errors.Is(err, ErrBatchEmbeddingsUnsupported) {
				return batchStart, namespace, fmt.Errorf("embed exemplar batch %d-%d: %w", batchStart+1, batchEnd, err)
			}
			if errors.Is(err, ErrBatchEmbeddingsUnsupported) {
				// Do not retry later batches through a provider/model that has
				// already demonstrated single-input-only behavior.
				embedBatch = nil
				embeddings = nil
			}
		}

		if embedBatch == nil || len(batch) == 1 {
			embeddings = make([][]float32, len(batch))
			for index, exemplar := range batch {
				embedding, err := embed(ctx, config.Semantic, exemplar.Phrase)
				if err != nil {
					absoluteIndex := batchStart + index
					return absoluteIndex, namespace, fmt.Errorf("embed %s exemplar %d: %w", strings.ToLower(exemplar.Tier), absoluteIndex+1, err)
				}
				embeddings[index] = embedding
			}
		}

		for index, exemplar := range batch {
			absoluteIndex := batchStart + index
			embedding := embeddings[index]
			if len(embedding) != config.Semantic.Dimension {
				return absoluteIndex, namespace, fmt.Errorf("%s exemplar %d returned dimension %d, expected %d", strings.ToLower(exemplar.Tier), absoluteIndex+1, len(embedding), config.Semantic.Dimension)
			}
			if err := store.Add(ctx, namespace, semanticExemplarID(fingerprint, exemplar), embedding, map[string]interface{}{
				semanticMetadataTier:        exemplar.Tier,
				semanticMetadataKind:        semanticMetadataKindExample,
				semanticMetadataFingerprint: fingerprint,
			}); err != nil {
				return absoluteIndex, namespace, fmt.Errorf("store %s exemplar %d: %w", strings.ToLower(exemplar.Tier), absoluteIndex+1, err)
			}
			markerEmbedding = embedding
		}
	}
	if err := store.Add(ctx, namespace, markerID, markerEmbedding, map[string]interface{}{
		semanticMetadataKind:        semanticMetadataKindMarker,
		semanticMetadataFingerprint: fingerprint,
	}); err != nil {
		return len(exemplars), namespace, dimension, fmt.Errorf("store complexity warmup marker: %w", err)
	}
	return len(exemplars), namespace, dimension, nil
}

// semanticExemplar binds one normalized shared tier phrase to its routing tier.
type semanticExemplar struct {
	Tier   string
	Phrase string
}

// semanticExemplars converts the shared tier lists into labelled semantic examples.
func semanticExemplars(config *AnalyzerConfig) []semanticExemplar {
	if config == nil {
		return nil
	}
	exemplars := make([]semanticExemplar, 0, len(config.Keywords.SimpleKeywords)+len(config.Keywords.MediumKeywords)+len(config.Keywords.ComplexKeywords))
	appendTier := func(tier string, phrases []string) {
		for _, phrase := range phrases {
			exemplars = append(exemplars, semanticExemplar{Tier: tier, Phrase: phrase})
		}
	}
	appendTier(TierSimple, config.Keywords.SimpleKeywords)
	appendTier(TierMedium, config.Keywords.MediumKeywords)
	appendTier(TierComplex, config.Keywords.ComplexKeywords)
	return exemplars
}

// semanticFingerprint identifies the embeddings implied by one configuration.
// Phrase order does not affect nearest-neighbour classification, so hash a
// sorted copy to avoid creating a new generation when an admin only reorders
// otherwise identical tier phrases.
func semanticFingerprint(config *AnalyzerConfig, exemplars []semanticExemplar, dimension int) string {
	canonical := append([]semanticExemplar(nil), exemplars...)
	sort.Slice(canonical, func(i, j int) bool {
		if canonical[i].Tier != canonical[j].Tier {
			return canonical[i].Tier < canonical[j].Tier
		}
		return canonical[i].Phrase < canonical[j].Phrase
	})

	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "semantic-router-v1\x00%s\x00%s\x00%d\x00", config.Semantic.Provider, config.Semantic.EmbeddingModel, dimension)
	for _, exemplar := range canonical {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", exemplar.Tier, exemplar.Phrase)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// semanticGenerationNamespace maps one embedding fingerprint to an immutable
// namespace. The underscore + hexadecimal suffix is accepted by every current
// backend, including Weaviate's class-name validation.
func semanticGenerationNamespace(fingerprint string) string {
	return SemanticVectorStoreNamespace + "_" + fingerprint
}

// semanticExemplarID deterministically identifies one labelled exemplar inside
// the fingerprinted routing namespace with a UUID accepted by every backend.
func semanticExemplarID(fingerprint string, exemplar semanticExemplar) string {
	return semanticRecordID("example", fingerprint, exemplar.Tier, exemplar.Phrase)
}

// semanticExemplarIndex maps the record IDs one configuration implies back to
// the phrases they were derived from. It repeats warmSemanticExemplars' own
// exemplar and fingerprint derivation over the same config snapshot, so the two
// agree by construction on every ID that warmup wrote.
func semanticExemplarIndex(config *AnalyzerConfig, dimension int) map[string]string {
	exemplars := semanticExemplars(config)
	if len(exemplars) == 0 {
		return nil
	}
	fingerprint := semanticFingerprint(config, exemplars, dimension)
	index := make(map[string]string, len(exemplars))
	for _, exemplar := range exemplars {
		index[semanticExemplarID(fingerprint, exemplar)] = exemplar.Phrase
	}
	return index
}

// semanticMarkerID deterministically identifies one configuration generation
// marker with a backend-compatible UUID.
func semanticMarkerID(fingerprint string) string {
	return semanticRecordID("marker", fingerprint)
}

// semanticRecordID derives a stable UUIDv5 from one namespaced routing record.
func semanticRecordID(parts ...string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(strings.Join(parts, "\x00"))).String()
}

// isComplexityTier reports whether tier is one of the supported routing values.
func isComplexityTier(tier string) bool {
	return tier == TierSimple || tier == TierMedium || tier == TierComplex
}

// cloneAnalyzerConfig copies the mutable slices and semantic settings used by
// an asynchronous warmup so later handler writes cannot mutate its snapshot.
func cloneAnalyzerConfig(config *AnalyzerConfig) *AnalyzerConfig {
	if config == nil {
		return nil
	}
	cloned := config.Normalized()
	return &cloned
}

// cloneSemanticConfig copies the semantic settings used by one embedding call.
func cloneSemanticConfig(config *SemanticConfig) *SemanticConfig {
	if config == nil {
		return nil
	}
	cloned := *config
	return &cloned
}

// semanticVectorStoreProperties declares the minimal metadata schema needed by
// semantic routing. It intentionally excludes prompts and embedding contents.
var semanticVectorStoreProperties = map[string]vectorstore.VectorStoreProperties{
	semanticMetadataTier: {
		DataType:    vectorstore.VectorStorePropertyTypeString,
		Description: "Complexity tier represented by this exemplar",
	},
	semanticMetadataKind: {
		DataType:    vectorstore.VectorStorePropertyTypeString,
		Description: "Semantic routing record kind: example or marker",
	},
	semanticMetadataFingerprint: {
		DataType:    vectorstore.VectorStorePropertyTypeString,
		Description: "Fingerprint of the semantic routing configuration",
	},
}
