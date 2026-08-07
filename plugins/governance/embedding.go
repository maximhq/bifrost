package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/governance/complexity"
)

// ErrEmbeddingTimeout reports that a classification embed exhausted its
// configured budget instead of failing for a provider or configuration reason.
// Callers need the distinction because the two mean opposite things to an
// operator: a timeout says semantic routing works but is too slow for the
// budget it was given, while every other failure says it is not working at all.
var ErrEmbeddingTimeout = errors.New("embedding request timed out")

// EmbeddingRequestExecutor invokes the embedding endpoint on the bifrost
// client. The plugin calls it to embed request text for semantic complexity
// classification. It mirrors the signature of bifrost.Client.EmbeddingRequest.
type EmbeddingRequestExecutor func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError)

// EmbeddingExecutorSetter is implemented by governance plugins that accept an
// embedding request executor. The HTTP server wires the executor after the
// bifrost client is constructed (the plugin itself is built while the client
// is still being assembled, so it cannot be passed at Init). Wrappers that
// embed *GovernancePlugin satisfy this via method promotion.
type EmbeddingExecutorSetter interface {
	SetEmbeddingRequestExecutor(EmbeddingRequestExecutor)
}

// ComplexityVectorStoreSetter is implemented by governance plugins that accept
// Bifrost's configured VectorStore for semantic complexity routing.
type ComplexityVectorStoreSetter interface {
	SetComplexityVectorStore(vectorstore.VectorStore)
}

// SetEmbeddingRequestExecutor wires up the function used to call out to the
// embedding provider. Without it, semantic complexity classification publishes
// no tier. Safe for concurrent use with classification and plugin reloads.
func (p *GovernancePlugin) SetEmbeddingRequestExecutor(executor EmbeddingRequestExecutor) {
	if executor == nil {
		p.embeddingRequestExecutor.Store(nil)
		if p.semanticClassifier != nil {
			p.semanticClassifier.SetEmbeddingFunctions(nil, nil)
		}
		return
	}
	p.embeddingRequestExecutor.Store(&executor)
	if p.semanticClassifier != nil {
		p.semanticClassifier.SetEmbeddingFunctions(p.embedComplexityText, p.embedComplexityTexts)
	}
}

// SetComplexityVectorStore supplies the configured shared VectorStore. The
// classifier chooses it only for auto or external mode; embedded mode retains
// its private Chromem store.
func (p *GovernancePlugin) SetComplexityVectorStore(store vectorstore.VectorStore) {
	if p.semanticClassifier != nil {
		p.semanticClassifier.SetConfiguredStore(store)
	}
}

// embedComplexityText adapts Governance's Bifrost-aware embedding path to the
// classifier's context-based dependency without attributing token usage here.
func (p *GovernancePlugin) embedComplexityText(ctx context.Context, semantic *complexity.SemanticConfig, text string) ([]float32, error) {
	embeddingCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	defer embeddingCtx.Cancel()
	embedding, _, err := p.generateEmbedding(embeddingCtx, semantic, text)
	if err != nil {
		return nil, err
	}
	return embedding, nil
}

// embedComplexityTexts adapts the same internal embedding path for bounded
// warmup batches. It preserves response order by EmbeddingData.Index.
func (p *GovernancePlugin) embedComplexityTexts(ctx context.Context, semantic *complexity.SemanticConfig, texts []string) ([][]float32, error) {
	embeddingCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	defer embeddingCtx.Cancel()
	embeddings, _, err := p.generateEmbeddings(embeddingCtx, semantic, texts)
	if err != nil {
		return nil, err
	}
	return embeddings, nil
}

// embeddingExecutor returns the currently wired executor, or nil.
func (p *GovernancePlugin) embeddingExecutor() EmbeddingRequestExecutor {
	if ptr := p.embeddingRequestExecutor.Load(); ptr != nil {
		return *ptr
	}
	return nil
}

// CanClassifySemantically reports whether semantic classification is currently
// viable. The executor alone is not a sufficient gate — the server wires it
// unconditionally; the semantic config decides whether classification is
// actually configured.
func (p *GovernancePlugin) CanClassifySemantically(semantic *complexity.SemanticConfig) bool {
	return p.embeddingExecutor() != nil &&
		semantic != nil &&
		semantic.Provider != "" &&
		semantic.EmbeddingModel != ""
}

// generateEmbedding embeds text with the configured semantic provider/model and
// returns the vector plus the input token count (fed to routing-cost
// attribution). The call is bounded by the configured semantic timeout: the
// router hot path must never wait on a slow embedding provider.
func (p *GovernancePlugin) generateEmbedding(ctx *schemas.BifrostContext, semantic *complexity.SemanticConfig, text string) ([]float32, int, error) {
	embeddings, inputTokens, err := p.generateEmbeddings(ctx, semantic, []string{text})
	if err != nil {
		return nil, 0, err
	}
	if len(embeddings) != 1 {
		return nil, 0, fmt.Errorf("expected one embedding, got %d", len(embeddings))
	}
	return embeddings[0], inputTokens, nil
}

// generateEmbeddings sends one embedding request for an ordered set of texts.
// A multi-input response must contain exactly one uniquely indexed vector per
// input; otherwise warmup can safely retry through the single-input adapter.
func (p *GovernancePlugin) generateEmbeddings(ctx *schemas.BifrostContext, semantic *complexity.SemanticConfig, texts []string) ([][]float32, int, error) {
	executor := p.embeddingExecutor()
	if executor == nil {
		return nil, 0, fmt.Errorf("embedding request executor is not configured")
	}
	if semantic == nil || semantic.Provider == "" || semantic.EmbeddingModel == "" {
		return nil, 0, fmt.Errorf("semantic classification is not configured")
	}
	if len(texts) == 0 {
		return nil, 0, fmt.Errorf("embedding input is empty")
	}

	timeout := semantic.Timeout
	if timeout <= 0 {
		timeout = configstore.DefaultComplexitySemanticTimeout
	}

	input := &schemas.EmbeddingInput{}
	if len(texts) == 1 {
		text := texts[0]
		input.Text = &text
	} else {
		input.Texts = append([]string(nil), texts...)
	}
	embeddingReq := &schemas.BifrostEmbeddingRequest{
		Provider: semantic.Provider,
		Model:    semantic.EmbeddingModel,
		Input:    input,
	}

	embeddingCtx := schemas.NewBifrostContext(ctx, time.Now().Add(timeout))
	// Cancel the derived context once we're done. NewBifrostContext starts a
	// watchCancellation goroutine that holds a reference to ctx (the scoped
	// plugin context). Without this, that goroutine outlives the plugin call
	// and may dereference fields on a parent context that has already been
	// released back to its sync.Pool — see core/schemas.ReleasePluginScope.
	defer embeddingCtx.Cancel()
	// The embedding request targets the configured embedding provider/model,
	// not the caller's. Mark it as an internal sub-request: it skips the
	// plugin pipeline (so it cannot recurse back through governance) and
	// sheds the caller's key-routing and body-transport state so it behaves
	// like a fresh external /v1/embeddings call.
	bifrost.PrepareContextForInternalRequest(embeddingCtx)

	response, bifrostErr := executor(embeddingCtx, embeddingReq)
	if bifrostErr != nil {
		// The executor reports every failure as a *BifrostError, so a blown
		// budget is otherwise indistinguishable from a bad key or an unreachable
		// provider once the error is rendered into a string. Tag it here, at the
		// only layer that knows which deadline was set and why.
		if isEmbeddingTimeout(embeddingCtx, bifrostErr) {
			return nil, 0, fmt.Errorf("%w after %s", ErrEmbeddingTimeout, timeout)
		}
		return nil, 0, fmt.Errorf("failed to generate embedding: %v", bifrostErr)
	}

	if response == nil || len(response.Data) == 0 {
		return nil, 0, fmt.Errorf("no embeddings returned from provider")
	}
	inputTokens := 0
	if response.Usage != nil {
		inputTokens = response.Usage.TotalTokens
	}

	if len(response.Data) != len(texts) {
		if len(texts) > 1 {
			return nil, inputTokens, fmt.Errorf(
				"%w: provider returned %d vectors for %d inputs",
				complexity.ErrBatchEmbeddingsUnsupported,
				len(response.Data),
				len(texts),
			)
		}
		return nil, inputTokens, fmt.Errorf("provider returned %d vectors for one input", len(response.Data))
	}

	embeddings := make([][]float32, len(texts))
	seen := make([]bool, len(texts))
	for responseIndex, data := range response.Data {
		inputIndex := data.Index
		if len(texts) == 1 {
			// Preserve the historical single-input behavior: the sole response
			// vector is the answer even if a provider omits its index.
			inputIndex = 0
		} else if inputIndex < 0 || inputIndex >= len(texts) || seen[inputIndex] {
			return nil, inputTokens, fmt.Errorf(
				"%w: invalid or duplicate response index %d at position %d",
				complexity.ErrBatchEmbeddingsUnsupported,
				data.Index,
				responseIndex,
			)
		}

		embedding, err := decodeEmbedding(data.Embedding)
		if err != nil {
			return nil, inputTokens, fmt.Errorf("decode embedding %d: %w", inputIndex, err)
		}
		embeddings[inputIndex] = embedding
		seen[inputIndex] = true
	}
	return embeddings, inputTokens, nil
}

// decodeEmbedding normalizes the provider-supported embedding encodings into
// the float32 representation used by VectorStore.
func decodeEmbedding(embedding schemas.EmbeddingStruct) ([]float32, error) {
	switch {
	case embedding.EmbeddingStr != nil:
		var vals []float32
		if err := json.Unmarshal([]byte(*embedding.EmbeddingStr), &vals); err != nil {
			return nil, fmt.Errorf("failed to parse string embedding: %w", err)
		}
		return vals, nil
	case embedding.EmbeddingArray != nil:
		return float64ToFloat32Embedding(embedding.EmbeddingArray), nil
	case len(embedding.Embedding2DArray) > 0:
		return flattenToFloat32Embedding(embedding.Embedding2DArray), nil
	case embedding.EmbeddingInt8Array != nil:
		// Quantized int8/binary embedding format. Promote to float32 so the
		// similarity path treats it uniformly.
		return int8ToFloat32Embedding(embedding.EmbeddingInt8Array), nil
	case embedding.EmbeddingInt32Array != nil:
		return int32ToFloat32Embedding(embedding.EmbeddingInt32Array), nil
	}
	return nil, fmt.Errorf("embedding data is not in expected format")
}

// isEmbeddingTimeout reports whether a failed embedding call ran out of time
// rather than failing on its own merits. The derived context is authoritative
// for the budget this plugin set; the error type covers the case where the
// deadline fires inside the client, which maps an expired context to
// RequestTimedOut/504 before this frame observes the context as done. A parent
// cancellation (the caller hung up) is deliberately not a timeout: it leaves
// Err as context.Canceled and falls through to the generic failure path.
func isEmbeddingTimeout(ctx *schemas.BifrostContext, err *schemas.BifrostError) bool {
	if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return true
	}
	if err == nil {
		return false
	}
	if err.Type != nil && *err.Type == schemas.RequestTimedOut {
		return true
	}
	return err.StatusCode != nil && *err.StatusCode == 504
}

// float64ToFloat32Embedding converts a []float64 to a []float32. Vector
// payloads stay float32: cosine similarity at classification time is well
// within float32 range.
func float64ToFloat32Embedding(values []float64) []float32 {
	if len(values) == 0 {
		return nil
	}
	embedding := make([]float32, len(values))
	for i, value := range values {
		embedding[i] = float32(value)
	}
	return embedding
}

// int8ToFloat32Embedding promotes a quantized int8 embedding (used for
// binary/quantized formats by some providers) to float32 so it can be stored
// and compared uniformly against float32 entries.
func int8ToFloat32Embedding(values []int8) []float32 {
	if len(values) == 0 {
		return nil
	}
	embedding := make([]float32, len(values))
	for i, value := range values {
		embedding[i] = float32(value)
	}
	return embedding
}

// int32ToFloat32Embedding promotes a uint8/ubinary-style int32 embedding to
// float32 for the same reason as int8ToFloat32Embedding.
func int32ToFloat32Embedding(values []int32) []float32 {
	if len(values) == 0 {
		return nil
	}
	embedding := make([]float32, len(values))
	for i, value := range values {
		embedding[i] = float32(value)
	}
	return embedding
}

// flattenToFloat32Embedding concatenates a 2D embedding (one inner slice per
// input chunk) into a single flat []float32.
func flattenToFloat32Embedding(values [][]float64) []float32 {
	total := 0
	for _, arr := range values {
		total += len(arr)
	}
	if total == 0 {
		return nil
	}
	embedding := make([]float32, 0, total)
	for _, arr := range values {
		embedding = append(embedding, float64ToFloat32Embedding(arr)...)
	}
	return embedding
}
