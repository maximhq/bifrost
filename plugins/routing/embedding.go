package routing

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
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// ErrEmbeddingRequestExecutorNotConfigured means the HTTP server has not
// finished wiring the governance plugin to Bifrost's embedding request path.
// Configuration clients can retry this transient startup state.
var ErrEmbeddingRequestExecutorNotConfigured = errors.New("embedding request executor is not configured")

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

// EmbeddingExecutorSetter is implemented by routing plugins that accept an
// embedding request executor. The HTTP server wires the executor after the
// bifrost client is constructed (the plugin itself is built while the client
// is still being assembled, so it cannot be passed at Init). Wrappers that
// embed *RoutingPlugin satisfy this via method promotion.
type EmbeddingExecutorSetter interface {
	SetEmbeddingRequestExecutor(EmbeddingRequestExecutor)
}

// WarmupEmbedUsageObserver receives the usage of every warmup/boot embedding
// call made by semantic complexity routing. Warmup embeds have no triggering
// request — there is no response to stamp — so this callback is how their cost
// reaches telemetry. The HTTP server wires it to the telemetry plugin's routing
// overhead counters. Budget attribution is separate: settleWarmupEmbedUsage
// bills the admin-owned provider/model-level budgets directly when
// count_toward_budgets is on.
type WarmupEmbedUsageObserver func(provider, model string, inputTokens int)

// WarmupEmbedUsageObserverSetter is implemented by routing plugins that
// accept a warmup embedding usage observer. Wired by the HTTP server like
// EmbeddingExecutorSetter; wrappers that embed *RoutingPlugin satisfy this
// via method promotion.
type WarmupEmbedUsageObserverSetter interface {
	SetWarmupEmbedUsageObserver(WarmupEmbedUsageObserver)
}

// ComplexityVectorStoreSetter is implemented by routing plugins that accept
// Bifrost's configured VectorStore for semantic complexity routing.
type ComplexityVectorStoreSetter interface {
	SetComplexityVectorStore(vectorstore.VectorStore)
}

// warmupEmbeddingTimeout bounds one warmup embedding call, whether that is a
// batch of exemplars or a single-input fallback. Warmup runs in the background
// with no request waiting on it, so it must NOT inherit semantic.Timeout — that
// is the hot-path budget (1500ms by default), which a 32-exemplar batch cannot
// possibly meet. It stays bounded so a hung provider cannot pin the warmup
// worker forever.
const warmupEmbeddingTimeout = 60 * time.Second

// SetEmbeddingRequestExecutor wires up the function used to call out to the
// embedding provider. Without it, semantic complexity classification publishes
// no tier. Safe for concurrent use with classification and plugin reloads.
func (p *RoutingPlugin) SetEmbeddingRequestExecutor(executor EmbeddingRequestExecutor) {
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
// classifier uses it only in "vector_store" mode; "embedded" mode retains its
// private Chromem store.
func (p *RoutingPlugin) SetComplexityVectorStore(store vectorstore.VectorStore) {
	if store == nil {
		p.complexityVectorStore.Store(nil)
	} else {
		p.complexityVectorStore.Store(&store)
	}
	if p.semanticClassifier != nil {
		p.semanticClassifier.SetConfiguredStore(store)
	}
}

// embedComplexityText adapts Governance's Bifrost-aware embedding path to the
// classifier's context-based dependency. It records the embed's usage on the
// triggering request's context so PostLLMHook can stamp RoutingDebug; budget
// attribution itself happens later in cost calculation, never here.
func (p *RoutingPlugin) embedComplexityText(ctx context.Context, semantic *complexity.SemanticConfig, text string) ([]float32, error) {
	// A *schemas.BifrostContext means a live request is blocked on this embed; a
	// plain context means warmup's single-input fallback (batch embeds
	// unsupported by the provider/model). The two get very different budgets:
	// the hot path must not wait, warmup can.
	_, isRequest := ctx.(*schemas.BifrostContext)
	timeout := warmupEmbeddingTimeout
	if isRequest {
		timeout = requestEmbeddingTimeout(semantic)
	}

	embeddingCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	defer embeddingCtx.Cancel()
	embedding, inputTokens, err := p.generateEmbedding(embeddingCtx, semantic, text, timeout)
	// Mirrors embedComplexityTexts: a response that arrived and was billed is
	// accounted for even when its shape was rejected, because the provider
	// completed the call either way. This is the single-input side of the same
	// rule — reached both by warmup's fallback after a batch is refused and by a
	// live request's classification embed.
	//
	// generateEmbeddings reports zero tokens for every failure before the
	// response arrives (no executor, bad config, timeout), so gating on a
	// positive count keeps those out and leaves the success path unchanged.
	if err == nil || inputTokens > 0 {
		if isRequest {
			recordRoutingEmbedUsage(ctx, semantic, inputTokens)
		} else {
			p.settleWarmupEmbedUsage(semantic, inputTokens)
		}
	}
	if err != nil {
		return nil, err
	}
	return embedding, nil
}

// requestEmbeddingTimeout is the configured hot-path budget for one
// classification embed, which a live request is waiting on.
func requestEmbeddingTimeout(semantic *complexity.SemanticConfig) time.Duration {
	if semantic != nil && semantic.Timeout > 0 {
		return semantic.Timeout
	}
	return configstore.DefaultComplexitySemanticTimeout
}

// recordRoutingEmbedUsage appends one classification embed's usage to the
// triggering request's context. Warmup embeds arrive on plain background
// contexts (never a *schemas.BifrostContext), so they are naturally excluded —
// boot/warmup embedding cost is never stamped or attributed to any request.
// Classification runs in PreRequestHook, once per top-level request before any
// provider fallback attempts, so this call happens at most once; a later llm
// fallback call (recordRoutingLLMUsage) appends alongside it rather than
// replacing it, so both calls' cost and budget attribution survive.
func recordRoutingEmbedUsage(ctx context.Context, semantic *complexity.SemanticConfig, inputTokens int) {
	bfCtx, ok := ctx.(*schemas.BifrostContext)
	if !ok || semantic == nil {
		return
	}
	if inputTokens < 0 {
		inputTokens = 0
	}
	provider := string(semantic.Provider)
	model := semantic.EmbeddingModel
	schemas.AppendRoutingCallOnContext(bfCtx, schemas.BifrostRoutingCall{
		ProviderUsed:       &provider,
		ModelUsed:          &model,
		InputTokens:        &inputTokens,
		CountTowardBudgets: semantic.CountTowardBudgets,
	})
}

// recordRoutingLLMUsage appends one llm classification completion to the same
// request-scoped handoff used by semantic classification. It runs at most
// once per request — the llm classifier is invoked only after a semantic
// non-answer, never retried within PreRequestHook — so this simply appends
// rather than accumulating repeated calls. OutputTokens remains non-nil even
// when zero because its presence tells cost calculation to use
// chat-completion pricing.
func recordRoutingLLMUsage(ctx context.Context, llm *complexity.LLMConfig, inputTokens, outputTokens int) {
	bfCtx, ok := ctx.(*schemas.BifrostContext)
	if !ok || llm == nil {
		return
	}
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}
	provider := string(llm.Provider)
	model := llm.Model
	schemas.AppendRoutingCallOnContext(bfCtx, schemas.BifrostRoutingCall{
		ProviderUsed:       &provider,
		ModelUsed:          &model,
		InputTokens:        &inputTokens,
		OutputTokens:       &outputTokens,
		CountTowardBudgets: llm.CountTowardBudgets,
	})
}

// stampRoutingDebug attaches routing-classification telemetry to the response
// when this request ran a semantic routing embed. Stamped on every such
// response for visibility, independent of count_toward_budgets — the flag rides
// in the struct because cost calculation (modelcatalog) cannot see governance
// config. For streams, only the final chunk is stamped, matching where cost is
// billed and mirroring the semantic cache's stamping.
func stampRoutingDebug(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, requestType schemas.RequestType, isFinalChunk bool) {
	if result == nil {
		return
	}
	if bifrost.IsStreamRequestType(requestType) && !isFinalChunk {
		return
	}
	usage, ok := schemas.InitialAttemptRoutingDebugFromContext(ctx)
	if !ok {
		return
	}
	extraFields := result.GetExtraFields()
	if extraFields == nil {
		return
	}
	extraFields.RoutingDebug = usage
}

// embedComplexityTexts adapts the same internal embedding path for bounded
// warmup batches. It preserves response order by EmbeddingData.Index. Batch
// embeds are warmup-only, so usage always goes to the warmup observer and is
// never attributed to a request.
func (p *RoutingPlugin) embedComplexityTexts(ctx context.Context, semantic *complexity.SemanticConfig, texts []string) ([][]float32, error) {
	embeddingCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	defer embeddingCtx.Cancel()
	embeddings, inputTokens, err := p.generateEmbeddings(embeddingCtx, semantic, texts, warmupEmbeddingTimeout)
	// A rejected response shape still cost input tokens: the provider completed
	// the call and billed for it, we just refused to trust its vector count or
	// indices. ErrBatchEmbeddingsUnsupported is the case that matters, because
	// warmup recovers from it by re-embedding one text at a time and then
	// succeeds — dropping the batch's usage here would leave those tokens
	// unobserved and unbilled with nothing downstream to notice. The retries
	// settle their own separate calls, so this cannot double-count.
	//
	// generateEmbeddings reports zero tokens for every failure before the
	// response arrives (no executor, bad config, timeout), so gating on a
	// positive count keeps those out of the observer's call counter while
	// leaving the success path settling exactly as before.
	if err == nil || inputTokens > 0 {
		p.settleWarmupEmbedUsage(semantic, inputTokens)
	}
	if err != nil {
		return nil, err
	}
	return embeddings, nil
}

// embeddingExecutor returns the currently wired executor, or nil.
func (p *RoutingPlugin) embeddingExecutor() EmbeddingRequestExecutor {
	if ptr := p.embeddingRequestExecutor.Load(); ptr != nil {
		return *ptr
	}
	return nil
}

// SetWarmupEmbedUsageObserver wires (or clears, with nil) the callback that
// receives warmup embedding usage. Safe for concurrent use with warmup and
// plugin reloads.
func (p *RoutingPlugin) SetWarmupEmbedUsageObserver(observer WarmupEmbedUsageObserver) {
	if observer == nil {
		p.warmupEmbedUsageObserver.Store(nil)
		return
	}
	p.warmupEmbedUsageObserver.Store(&observer)
}

// settleWarmupEmbedUsage settles one warmup embedding call: it reports the
// usage to the wired observer (telemetry, always) and, when
// count_toward_budgets is on, attributes the cost to the admin-owned
// provider-level and global model-level budgets — the same ledger the tracker
// uses for usage with no virtual key. Warmup has no triggering request, so no
// VK/team/customer budget is ever touched: there is no tenant to bill, only
// the platform-level budgets on the embedding provider/model. Called only from
// paths with no triggering request.
func (p *RoutingPlugin) settleWarmupEmbedUsage(semantic *complexity.SemanticConfig, inputTokens int) {
	if semantic == nil {
		return
	}
	if ptr := p.warmupEmbedUsageObserver.Load(); ptr != nil {
		(*ptr)(string(semantic.Provider), semantic.EmbeddingModel, inputTokens)
	}
	if !semantic.CountTowardBudgets {
		return
	}
	// Billing itself stays with governance: it owns the model catalog and the
	// provider/model budget ledger, and routing must not grow its own copy.
	p.governance.AttributeRoutingEmbeddingCost(semantic.Provider, semantic.EmbeddingModel, inputTokens)
}

// CanClassifySemantically reports whether semantic classification is currently
// viable. The executor alone is not a sufficient gate — the server wires it
// unconditionally; the semantic config decides whether classification is
// actually configured.
func (p *RoutingPlugin) CanClassifySemantically(semantic *complexity.SemanticConfig) bool {
	return p.embeddingExecutor() != nil &&
		semantic != nil &&
		semantic.Provider != "" &&
		semantic.EmbeddingModel != ""
}

// generateEmbedding embeds text with the configured semantic provider/model and
// returns the vector plus the input token count (fed to routing-cost
// attribution). The call is bounded by the configured semantic timeout: the
// router hot path must never wait on a slow embedding provider.
func (p *RoutingPlugin) generateEmbedding(ctx *schemas.BifrostContext, semantic *complexity.SemanticConfig, text string, timeout time.Duration) ([]float32, int, error) {
	// Both failure paths carry the token count through rather than zeroing it:
	// generateEmbeddings only reports a positive count once the provider has
	// responded and billed, and callers decide what to do with usage from a
	// rejected response. Zeroing here hid it from them entirely.
	embeddings, inputTokens, err := p.generateEmbeddings(ctx, semantic, []string{text}, timeout)
	if err != nil {
		return nil, inputTokens, err
	}
	if len(embeddings) != 1 {
		return nil, inputTokens, fmt.Errorf("expected one embedding, got %d", len(embeddings))
	}
	return embeddings[0], inputTokens, nil
}

// generateEmbeddings sends one embedding request for an ordered set of texts.
// A multi-input response must contain exactly one uniquely indexed vector per
// input; otherwise warmup can safely retry through the single-input adapter.
func (p *RoutingPlugin) generateEmbeddings(ctx *schemas.BifrostContext, semantic *complexity.SemanticConfig, texts []string, timeout time.Duration) ([][]float32, int, error) {
	executor := p.embeddingExecutor()
	if executor == nil {
		return nil, 0, ErrEmbeddingRequestExecutorNotConfigured
	}
	if semantic == nil || semantic.Provider == "" || semantic.EmbeddingModel == "" {
		return nil, 0, fmt.Errorf("semantic classification is not configured")
	}
	if len(texts) == 0 {
		return nil, 0, fmt.Errorf("embedding input is empty")
	}

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

	// A nil response means nothing arrived to read usage from, so it keeps the
	// zero-token contract every pre-response failure above shares.
	if response == nil {
		return nil, 0, fmt.Errorf("no embeddings returned from provider")
	}
	inputTokens := 0
	// Provider-reported usage is untrusted input: a negative count would flow
	// into the RoutingDebug stamp and from there into cost calculation and
	// warmup budget attribution, where it would subtract from billed usage.
	// Drop it to zero — the embed still happened, we just cannot price it.
	if response.Usage != nil && response.Usage.TotalTokens > 0 {
		inputTokens = response.Usage.TotalTokens
	}
	// Read usage before rejecting the shape, for the same reason the vector-count
	// mismatch below returns it: the provider completed the call and billed for
	// it, and we are only refusing to trust what came back. Returning zero here
	// left an empty-Data response as the one post-response failure whose tokens
	// went unobserved and unbilled.
	if len(response.Data) == 0 {
		return nil, inputTokens, fmt.Errorf("no embeddings returned from provider")
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
	var vector []float32
	switch {
	case embedding.EmbeddingStr != nil:
		if err := json.Unmarshal([]byte(*embedding.EmbeddingStr), &vector); err != nil {
			return nil, fmt.Errorf("failed to parse string embedding: %w", err)
		}
	case embedding.EmbeddingArray != nil:
		vector = float64ToFloat32Embedding(embedding.EmbeddingArray)
	case len(embedding.Embedding2DArray) > 0:
		vector = flattenToFloat32Embedding(embedding.Embedding2DArray)
	case embedding.EmbeddingInt8Array != nil:
		// Quantized int8/binary embedding format. Promote to float32 so the
		// similarity path treats it uniformly.
		vector = int8ToFloat32Embedding(embedding.EmbeddingInt8Array)
	case embedding.EmbeddingInt32Array != nil:
		vector = int32ToFloat32Embedding(embedding.EmbeddingInt32Array)
	default:
		return nil, fmt.Errorf("embedding data is not in expected format")
	}
	// A present-but-empty vector ("[]", an empty array, a 2D array of empty
	// rows) is not a usable embedding: it would be stored or compared as a
	// zero-dimension vector and every similarity against it is meaningless.
	// Reject it here so generateEmbeddings fails the whole call.
	if len(vector) == 0 {
		return nil, fmt.Errorf("embedding data is empty")
	}
	return vector, nil
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
