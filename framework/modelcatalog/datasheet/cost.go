package datasheet

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// CalculateCost calculates the cost of a Bifrost response.
// It handles all request types, cache and guardrail billing, and tiered pricing.
// If scopes is nil, an empty LookupScopes is used; global and provider-scoped
// overrides may still apply since the provider is derived from the response.
func (s *Store) CalculateCost(result *schemas.BifrostResponse, scopes *LookupScopes) float64 {
	breakdown := s.CalculateCostBreakdown(result, scopes)
	if breakdown == nil {
		return 0
	}
	return breakdown.TotalCost
}

// CalculateCostBreakdown mirrors CalculateCost but returns the full per-category
// cost breakdown (input / output / cache) instead of only the total. Returns nil
// when there is no cost to record. CalculateCost is a thin wrapper over this that
// returns breakdown.TotalCost, so both paths compute cost identically.
func (s *Store) CalculateCostBreakdown(result *schemas.BifrostResponse, scopes *LookupScopes) *schemas.BifrostCost {
	if result == nil {
		return nil
	}

	var lookupScopes LookupScopes
	if scopes != nil {
		lookupScopes = *scopes
	}

	extraFields := result.GetExtraFields()

	// Handle semantic cache billing
	cacheDebug := extraFields.CacheDebug
	var requestCost *schemas.BifrostCost
	if cacheDebug != nil {
		requestCost = s.calculateCostWithCache(result, cacheDebug, lookupScopes)
	} else {
		requestCost = s.calculateBaseCost(result, lookupScopes)
	}

	// Handle guardrail judge-call billing
	if extraFields.GuardrailDebug == nil {
		return requestCost
	}
	guardrailCost := s.CalculateGuardrailCost(extraFields.GuardrailDebug, &lookupScopes)
	if guardrailCost == 0 {
		return requestCost
	}
	// Copy rather than mutate: requestCost may alias the provider-supplied
	// usage.Cost. The judge call is a separate internal cost with no input/output
	// token category, so it lands on the additional side (and the total).
	merged := &schemas.BifrostCost{}
	if requestCost != nil {
		*merged = *requestCost
		if requestCost.AdditionalCostDetails != nil {
			d := *requestCost.AdditionalCostDetails
			merged.AdditionalCostDetails = &d
		}
	}
	if merged.AdditionalCostDetails == nil {
		merged.AdditionalCostDetails = &schemas.AdditionalCostDetails{}
	}
	merged.AdditionalCost += guardrailCost
	merged.AdditionalCostDetails.GuardrailCost += guardrailCost
	merged.TotalCost += guardrailCost
	return merged
}

// CalculateCostForUsage computes the dollar cost from a bare usage object plus
// provider / model / request type, for cases where no full BifrostResponse
// exists. The primary use is billing partial usage carried on a failed or
// cancelled request via BifrostError.ExtraFields.BilledUsage: the
// provider consumed tokens, so we must charge for them even though there is no
// success response to read. It mirrors CalculateCost's compute path so success
// and failure billing use identical rates. Returns 0 when usage is nil. A thin
// wrapper over CalculateCostBreakdownForUsage returning only the total, so both
// paths compute cost identically.
func (s *Store) CalculateCostForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *LookupScopes) float64 {
	breakdown := s.CalculateCostBreakdownForUsage(usage, provider, model, requestType, scopes)
	if breakdown == nil {
		return 0
	}
	return breakdown.TotalCost
}

// CalculateCostBreakdownForUsage mirrors CalculateCostForUsage but returns the
// full per-category breakdown instead of only the total, so callers billing a
// bare usage object (failed/cancelled requests) can denormalize the input /
// output / additional split, not just the scalar total. Returns nil when there
// is no cost to record.
func (s *Store) CalculateCostBreakdownForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *LookupScopes) *schemas.BifrostCost {
	if usage == nil {
		return nil
	}

	var lookupScopes LookupScopes
	if scopes != nil {
		lookupScopes = *scopes
	}

	// If the provider already computed cost, trust it (matches calculateBaseCost).
	if usage.Cost != nil && usage.Cost.TotalCost > 0 {
		return usage.Cost
	}

	// Apply the served tier (fast mode / data residency) carried on the usage so
	// cancelled/failed fast or US-residency streams keep their multiplier.
	input := costInput{usage: usage}
	input.tier = tierFromResponse(nil, usage.Speed, usage.InferenceGeo)

	return s.computeCostFromInput(
		input,
		schemas.RoutingInfo{
			Provider:                provider,
			Model:                   model,
			ServerSideFallbackModel: usage.ServerSideFallbackModel,
		},
		normalizeStreamRequestType(requestType),
		lookupScopes,
	)
}

// CalculateGuardrailCost computes judge cost when no parent response is available.
//
// CalculateCost uses this for normal responses. Logging also calls it directly
// for input guardrail blocks, where the main provider call never produced a
// BifrostResponse.
func (s *Store) CalculateGuardrailCost(debug *schemas.BifrostGuardrailDebug, scopes *LookupScopes) float64 {
	if debug == nil || len(debug.JudgeCalls) == 0 {
		return 0
	}

	var total float64
	for _, call := range debug.JudgeCalls {
		total += s.computeGuardrailJudgeCost(call, scopes)
	}
	return total
}

// computeGuardrailJudgeCost computes one internal judge chat-completion cost.
func (s *Store) computeGuardrailJudgeCost(call schemas.BifrostGuardrailJudgeCall, scopes *LookupScopes) float64 {
	if call.JudgeProvider == "" || call.JudgeModel == "" {
		return 0
	}

	// Price the judge call using its own provider/model. Keep virtual-key
	// attribution for the caller, but do not reuse the main request's selected
	// provider key: the judge may use a different configured key.
	judgeScopes := LookupScopes{Provider: string(call.JudgeProvider)}
	if scopes != nil {
		judgeScopes.VirtualKeyID = scopes.VirtualKeyID
	}

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:            call.PromptTokens,
		PromptTokensDetails:     call.PromptTokensDetails,
		CompletionTokens:        call.CompletionTokens,
		CompletionTokensDetails: call.CompletionTokensDetails,
		TotalTokens:             call.TotalTokens,
	}
	requestType := call.JudgeRequestType
	if requestType == "" {
		requestType = schemas.ChatCompletionRequest
	}
	return s.CalculateCostForUsage(
		usage,
		call.JudgeProvider,
		call.JudgeModel,
		requestType,
		&judgeScopes,
	)
}

// BatchCostDetails captures the rate inputs used to price a batch result row.
// InputCostPerTokenBatches/OutputCostPerTokenBatches are the rates actually
// applied — the catalog's explicit batch rate when set, otherwise
// defaultBatchPricingRatio of the standard rate. There is no field marking
// which case occurred; callers that need to distinguish an authoritative
// catalog rate from an assumed default must compare against the standard rate
// themselves.
type BatchCostDetails struct {
	Cost                      float64
	Priced                    bool
	ProviderCostUsed          bool
	InputCostPerTokenBatches  *float64
	OutputCostPerTokenBatches *float64
}

// defaultBatchPricingRatio is the fraction of the standard synchronous rate
// used to price a batch request when the catalog has no batch-specific rate
// for the model.
const defaultBatchPricingRatio = 0.5

// resolveBatchRate returns the catalog's explicit batch rate when set, else
// defaultBatchPricingRatio times the standard rate when that's available, else
// nil — meaning there is truly nothing to price this with.
func resolveBatchRate(standard, batch *float64) *float64 {
	if batch != nil {
		return cloneFloat64Pointer(batch)
	}
	if standard != nil {
		defaulted := *standard * defaultBatchPricingRatio
		return &defaulted
	}
	return nil
}

// CalculateBatchCostDetailsForUsage computes batch result cost and returns the
// explicit batch rates used so aggregate logs can explain historical pricing.
// When the catalog has no batch-specific rate, it defaults to
// defaultBatchPricingRatio of the standard rate rather than refusing to price —
// only a model with no pricing at all (neither batch nor standard) is unpriced.
func (s *Store) CalculateBatchCostDetailsForUsage(usage *schemas.BifrostLLMUsage, provider schemas.ModelProvider, model string, requestType schemas.RequestType, scopes *LookupScopes) BatchCostDetails {
	if usage == nil {
		return BatchCostDetails{}
	}
	// Only honor a provider-supplied cost when it is actually populated. A
	// non-nil but zero cost (e.g. a partial cost object on the wire) must fall
	// through to the catalog rates rather than price the row at zero — matching
	// CalculateCostForUsage and calculateBaseCost.
	if usage.Cost != nil && usage.Cost.TotalCost > 0 {
		return BatchCostDetails{
			Cost:             usage.Cost.TotalCost,
			Priced:           true,
			ProviderCostUsed: true,
		}
	}

	var lookupScopes LookupScopes
	if scopes != nil {
		lookupScopes = *scopes
	}
	pricing := s.resolvePricing(schemas.RoutingInfo{Provider: provider, Model: model}, normalizeStreamRequestType(requestType), lookupScopes)
	if pricing == nil {
		return BatchCostDetails{}
	}

	switch normalizeStreamRequestType(requestType) {
	case schemas.BatchResultsRequest, schemas.ChatCompletionRequest, schemas.TextCompletionRequest, schemas.ResponsesRequest, schemas.EmbeddingRequest:
		inputRate := resolveBatchRate(pricing.InputCostPerToken, pricing.InputCostPerTokenBatches)
		outputRate := resolveBatchRate(pricing.OutputCostPerToken, pricing.OutputCostPerTokenBatches)
		if usage.PromptTokens > 0 && inputRate == nil {
			return BatchCostDetails{}
		}
		if usage.CompletionTokens > 0 && outputRate == nil {
			return BatchCostDetails{}
		}
		// Speed or InferenceGeo are carried on BifrostLLMUsage for exactly this —
		// the bare-usage batch path never sees a full response to read a served
		// tier off, mirroring CalculateCostForUsage.
		tier := tierFromResponse(nil, usage.Speed, usage.InferenceGeo)
		breakdown := computeBatchTextCost(pricing, usage, tier)
		cost := 0.0
		if breakdown != nil {
			cost = breakdown.TotalCost
		}
		// Flat per-request surcharge, mirroring computeCostFromInput: each row in
		// a batch is its own distinct request, so a per-request fee applies once
		// per row exactly as it would have applied once per synchronous call.
		if pricing.CostPerRequest != nil {
			cost += *pricing.CostPerRequest
		}
		return BatchCostDetails{
			Cost:                      cost,
			Priced:                    true,
			InputCostPerTokenBatches:  inputRate,
			OutputCostPerTokenBatches: outputRate,
		}
	default:
		return BatchCostDetails{}
	}
}

func cloneFloat64Pointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

// calculateCostWithCache handles cost calculation when semantic cache debug info is present.
func (s *Store) calculateCostWithCache(result *schemas.BifrostResponse, cacheDebug *schemas.BifrostCacheDebug, scopes LookupScopes) *schemas.BifrostCost {
	if cacheDebug.CacheHit {
		// Direct cache hit — no LLM call, no cost
		if cacheDebug.HitType != nil && *cacheDebug.HitType == "direct" {
			return nil
		}
		// Semantic cache hit — only the embedding lookup cost. It's an internal
		// sidecar cost (a separate embedding call), so it lands on the additional
		// side, alongside guardrail/MCP, not folded into the request's input.
		if cacheDebug.ProviderUsed != nil && cacheDebug.ModelUsed != nil && cacheDebug.InputTokens != nil {
			c := s.computeCacheEmbeddingCost(cacheDebug, scopes)
			if c == 0 {
				return nil
			}
			return &schemas.BifrostCost{
				AdditionalCost:        c,
				AdditionalCostDetails: &schemas.AdditionalCostDetails{SemanticCacheCost: c},
				TotalCost:             c,
			}
		}
		return nil
	}

	// Cache miss — full LLM cost + embedding lookup cost (a sidecar additional cost)
	base := s.calculateBaseCost(result, scopes)
	embeddingCost := s.computeCacheEmbeddingCost(cacheDebug, scopes)
	if embeddingCost == 0 {
		return base
	}
	// Copy rather than mutate: base may alias the provider-supplied usage.Cost.
	merged := &schemas.BifrostCost{}
	if base != nil {
		*merged = *base
		if base.AdditionalCostDetails != nil {
			d := *base.AdditionalCostDetails
			merged.AdditionalCostDetails = &d
		}
	}
	if merged.AdditionalCostDetails == nil {
		merged.AdditionalCostDetails = &schemas.AdditionalCostDetails{}
	}
	merged.AdditionalCost += embeddingCost
	merged.AdditionalCostDetails.SemanticCacheCost += embeddingCost
	merged.TotalCost += embeddingCost
	return merged
}

// computeCacheEmbeddingCost calculates the embedding cost for a semantic cache lookup.
func (s *Store) computeCacheEmbeddingCost(cacheDebug *schemas.BifrostCacheDebug, scopes LookupScopes) float64 {
	if cacheDebug == nil || cacheDebug.ProviderUsed == nil || cacheDebug.ModelUsed == nil || cacheDebug.InputTokens == nil {
		return 0
	}
	if scopes.Provider == "" {
		scopes.Provider = *cacheDebug.ProviderUsed
	}
	// Cache-debug pricing has only a single model identifier (whatever the
	// cache recorded). Maps to RoutingInfo.Model — no alias resolution
	// context exists for the cache-replayed request.
	pricing := s.resolvePricing(schemas.RoutingInfo{
		Provider: schemas.ModelProvider(*cacheDebug.ProviderUsed),
		Model:    *cacheDebug.ModelUsed,
	}, schemas.EmbeddingRequest, scopes)
	if pricing == nil {
		return 0
	}
	cost := float64(*cacheDebug.InputTokens) * tieredInputRate(pricing, *cacheDebug.InputTokens, serviceTier{})
	// The lookup is a separate embedding call, so the embedding model's flat
	// per-request fee applies once, mirroring the synchronous/batch paths.
	if pricing.CostPerRequest != nil {
		cost += *pricing.CostPerRequest
	}
	return cost
}

// CalculateCacheEmbeddingCost computes the semantic-cache embedding lookup cost.
func (s *Store) CalculateCacheEmbeddingCost(cacheDebug *schemas.BifrostCacheDebug, scopes *LookupScopes) float64 {
	var lookupScopes LookupScopes
	if scopes != nil {
		lookupScopes = *scopes
	}
	return s.computeCacheEmbeddingCost(cacheDebug, lookupScopes)
}

// computeContainerCreationCost returns the cost for creating a container from an already-resolved pricing entry.
func computeContainerCreationCost(pricing *configstoreTables.TableModelPricing) *schemas.BifrostCost {
	if pricing == nil || pricing.CodeInterpreterCostPerSession == nil {
		return nil
	}
	// Container creation is a flat per-session cost, not a token cost, so it folds
	// onto the input side as a flat request cost.
	return totalOnlyCost(*pricing.CodeInterpreterCostPerSession)
}

// calculateBaseCost extracts usage from the response and routes to the appropriate compute function.
func (s *Store) calculateBaseCost(result *schemas.BifrostResponse, scopes LookupScopes) *schemas.BifrostCost {
	extraFields := result.GetExtraFields()
	if extraFields == nil {
		return nil
	}

	// Read routing info populated by core.bifrost at request time.
	//
	// Backward-compat fallback: when the caller (e.g. LoggerPlugin's
	// RecalculateCosts replaying logs written before RoutingInfo existed,
	// or third-party plugins still on the legacy ExtraFields shape) leaves
	// RoutingInfo empty, synthesise one from the deprecated triplet so
	// pricing keeps working. Triggered only when RoutingInfo is fully
	// unset — partial population is trusted as-is.
	routingInfo := extraFields.RoutingInfo
	if routingInfo.Provider == "" && routingInfo.Model == "" && routingInfo.ResolvedKeyAlias == nil {
		routingInfo.Provider = extraFields.Provider
		routingInfo.Model = extraFields.OriginalModelRequested
		if r := extraFields.ResolvedModelUsed; r != "" && r != extraFields.OriginalModelRequested {
			routingInfo.ResolvedKeyAlias = &schemas.ResolvedKeyAlias{ModelID: r}
		}
	}
	requestType := extraFields.RequestType

	// A retrieve is a status read, not a generation. Providers that report the job's cost on the
	// polled response (e.g. Runware, which echoes it on every getResponse once the task has
	// succeeded) would otherwise be billed again on every poll, inflating logs, traces and
	// governance budgets.
	if requestType == schemas.VideoRetrieveRequest {
		return nil
	}

	// Extract usage data from the response (passthrough and native paths unified)
	input := extractCostInput(result)

	// If provider already computed cost, use it
	if input.usage != nil && input.usage.Cost != nil && input.usage.Cost.TotalCost > 0 {
		return input.usage.Cost
	}
	// Image responses carry usage on imageUsage, never on input.usage.
	if input.imageUsage != nil && input.imageUsage.Cost != nil && input.imageUsage.Cost.TotalCost > 0 {
		return input.imageUsage.Cost
	}

	// If no usage data at all, nothing to price.
	//
	// Rerank is exempt: it bills per query rather than per unit of reported usage, so a call
	// that reports nothing still owes one query. Vertex returns no usage on rerank at all, and
	// treating that as free would silently under-report every one of its calls.
	if requestType != schemas.RerankRequest &&
		input.usage == nil && input.audioSeconds == nil && input.audioTokenDetails == nil && input.imageUsage == nil && input.videoSeconds == nil && input.audioTextInputChars == 0 && input.ocrProcessedPages == nil && input.containerIdentifierString == "" {
		return nil
	}

	if result.PassthroughResponse != nil {
		// Infer request type from usage fields + path; passthrough bypasses stream normalization.
		requestType = inferPassthroughRequestType(routingInfo.Provider, extraFields.PassthroughPath, result.PassthroughResponse.PassthroughUsage)
	} else {
		// Normalize stream request types to their base type for pricing lookup
		requestType = normalizeStreamRequestType(requestType)
	}

	// Azure Model Router bills a flat per-input-token surcharge on top of the
	// real cost of whatever underlying model Azure actually routed to. The
	// response's own model field carries that real model, distinct from the
	// "model-router" deployment name on RoutingInfo.Model.
	if result.PassthroughResponse == nil && routingInfo.Provider == schemas.Azure && schemas.IsAzureModelRouter(routingInfo.Model) &&
		(requestType == schemas.TextCompletionRequest || requestType == schemas.ChatCompletionRequest || requestType == schemas.ResponsesRequest) {
		return s.calculateAzureModelRouterCost(result, input, routingInfo, requestType, scopes)
	}

	return s.computeCostFromInput(input, routingInfo, requestType, scopes)
}

// calculateAzureModelRouterCost bills the Model Router deployment's own
// pricing row (the flat per-input-token surcharge) plus the real cost of the
// model it actually routed to, looked up fresh under the served model name so
// regular per-token/tiered pricing applies to it exactly as if it had been
// called directly.
func (s *Store) calculateAzureModelRouterCost(result *schemas.BifrostResponse, input costInput, routingInfo schemas.RoutingInfo, requestType schemas.RequestType, scopes LookupScopes) *schemas.BifrostCost {
	pricingRequestType := requestType
	if pricingRequestType == schemas.TextCompletionRequest {
		pricingRequestType = schemas.ChatCompletionRequest
	}

	cost := s.computeCostFromInput(input, routingInfo, pricingRequestType, scopes)

	if servedModel := azureModelRouterServedModel(result); servedModel != "" && servedModel != routingInfo.Model {
		underlyingRoutingInfo := schemas.RoutingInfo{
			Provider: routingInfo.Provider,
			Model:    servedModel,
		}
		cost = cost.Add(s.computeCostFromInput(input, underlyingRoutingInfo, pricingRequestType, scopes))
	}

	return cost
}

// azureModelRouterServedModel reads the model Azure Model Router actually
// routed to off the response body's own model field separate from the "model-router"
// deployment name carried on RoutingInfo.Model.
func azureModelRouterServedModel(result *schemas.BifrostResponse) string {
	switch {
	case result.ChatResponse != nil:
		return result.ChatResponse.Model
	case result.ResponsesResponse != nil:
		return result.ResponsesResponse.Model
	case result.ResponsesStreamResponse != nil && result.ResponsesStreamResponse.Response != nil:
		return result.ResponsesStreamResponse.Response.Model
	case result.TextCompletionResponse != nil:
		return result.TextCompletionResponse.Model
	default:
		return ""
	}
}

// computeCostFromInput resolves pricing for the given routing info + request
// type and routes the extracted usage to the appropriate per-modality compute
// function. Shared by calculateBaseCost (response-driven) and
// CalculateCostForUsage (bare-usage-driven, for failed/cancelled requests).
func (s *Store) computeCostFromInput(input costInput, routingInfo schemas.RoutingInfo, requestType schemas.RequestType, scopes LookupScopes) *schemas.BifrostCost {
	// When a pricing model override is set (e.g. container creates always look
	// up "container"), it replaces the lookup hierarchy entirely. Build a
	// synthetic RoutingInfo that reuses Provider but pins the model fields to
	// the container identifier — the lookup tries it as ModelName, the
	// override key is the container identifier so per-container overrides
	// stay addressable.
	if input.containerIdentifierString != "" {
		routingInfo = schemas.RoutingInfo{
			Provider: routingInfo.Provider,
			Model:    input.containerIdentifierString,
		}
	}

	pricing := s.resolvePricing(routingInfo, requestType, scopes)
	if pricing == nil {
		return nil
	}

	// Route to the appropriate compute function. Each returns a per-category
	// breakdown: token-based modalities populate input / output (and cache, for
	// text); non-token modalities (OCR per-page, container per-session) fold the
	// flat charge onto the input side as InputCostDetails.RequestCost.
	var cost *schemas.BifrostCost
	switch requestType {
	case schemas.ChatCompletionRequest, schemas.TextCompletionRequest, schemas.ResponsesRequest, schemas.RealtimeRequest, schemas.CompactionRequest:
		cost = computeTextCost(pricing, input.usage, input.tier)
	case schemas.BatchResultsRequest:
		cost = computeBatchTextCost(pricing, input.usage, input.tier)
	case schemas.EmbeddingRequest:
		cost = computeEmbeddingCost(pricing, input.usage, input.tier)
	case schemas.RerankRequest:
		cost = computeRerankCost(pricing, input.usage, input.tier)
	case schemas.SpeechRequest:
		cost = computeSpeechCost(pricing, input.usage, input.audioSeconds, input.audioTextInputChars, input.tier)
	case schemas.TranscriptionRequest:
		cost = computeTranscriptionCost(pricing, input.usage, input.audioSeconds, input.audioTokenDetails, input.tier)
	case schemas.ImageGenerationRequest, schemas.ImageEditRequest, schemas.ImageVariationRequest:
		cost = computeImageCost(pricing, input.imageUsage, input.imageSize, input.imageQuality, input.tier)
	case schemas.VideoGenerationRequest, schemas.VideoRemixRequest, schemas.VideoEditRequest:
		cost = computeVideoCost(pricing, input.usage, input.videoSeconds, input.tier)
	case schemas.OCRRequest:
		cost = computeOCRCost(pricing, input.ocrProcessedPages, input.ocrIsAnnotated)
	case schemas.ContainerCreateRequest:
		cost = computeContainerCreationCost(pricing)
	default:
		return nil
	}

	// Flat per-request surcharge, billed once on top of usage-based cost whenever
	// the resolved pricing row carries one. It maps to no token category, so it
	// folds into the input side (InputCostDetails.RequestCost) and the total.
	if pricing.CostPerRequest != nil {
		if cost == nil {
			cost = &schemas.BifrostCost{}
		}
		if cost.InputCostDetails == nil {
			cost.InputCostDetails = &schemas.InputCostDetails{}
		}
		cost.InputCost += *pricing.CostPerRequest
		cost.InputCostDetails.RequestCost += *pricing.CostPerRequest
		cost.TotalCost += *pricing.CostPerRequest
	}
	return cost
}

// ---------------------------------------------------------------------------
// Usage extraction
// ---------------------------------------------------------------------------

func extractCostInput(result *schemas.BifrostResponse) costInput {
	var input costInput

	switch {
	case result.PassthroughResponse != nil && result.PassthroughResponse.PassthroughUsage != nil:
		return passthroughUsageToCostInput(result.PassthroughResponse.PassthroughUsage)

	case result.TextCompletionResponse != nil && result.TextCompletionResponse.Usage != nil:
		input.usage = result.TextCompletionResponse.Usage

	case result.ChatResponse != nil && result.ChatResponse.Usage != nil:
		input.usage = result.ChatResponse.Usage
		input.tier = tierFromResponse(result.ChatResponse.ServiceTier, result.ChatResponse.Speed, result.ChatResponse.InferenceGeo)

	case result.ResponsesResponse != nil && result.ResponsesResponse.Usage != nil:
		input.usage = responsesUsageToBifrostUsage(result.ResponsesResponse.Usage)
		input.tier = tierFromResponse(result.ResponsesResponse.ServiceTier, result.ResponsesResponse.Speed, result.ResponsesResponse.InferenceGeo)

	case result.CompactionResponse != nil && result.CompactionResponse.Usage != nil:
		input.usage = responsesUsageToBifrostUsage(result.CompactionResponse.Usage)

	case result.ResponsesStreamResponse != nil && result.ResponsesStreamResponse.Response != nil && result.ResponsesStreamResponse.Response.Usage != nil:
		input.usage = responsesUsageToBifrostUsage(result.ResponsesStreamResponse.Response.Usage)
		input.tier = tierFromResponse(result.ResponsesStreamResponse.Response.ServiceTier, result.ResponsesStreamResponse.Response.Speed, result.ResponsesStreamResponse.Response.InferenceGeo)

	case result.EmbeddingResponse != nil && result.EmbeddingResponse.Usage != nil:
		input.usage = result.EmbeddingResponse.Usage

	// Not gated on Usage like its neighbours: rerank bills per query, so a response that
	// carries no usage at all (Vertex reports none) still owes one query's cost.
	case result.RerankResponse != nil:
		input.usage = result.RerankResponse.Usage

	case result.SpeechResponse != nil && result.SpeechResponse.Usage != nil:
		input.usage = speechUsageToBifrostUsage(result.SpeechResponse.Usage)
		input.audioTextInputChars = result.SpeechResponse.Usage.InputChars

	case result.SpeechStreamResponse != nil && result.SpeechStreamResponse.Usage != nil:
		input.usage = speechUsageToBifrostUsage(result.SpeechStreamResponse.Usage)
		input.audioTextInputChars = result.SpeechStreamResponse.Usage.InputChars

	case result.TranscriptionResponse != nil && result.TranscriptionResponse.Usage != nil:
		input.usage, input.audioSeconds, input.audioTokenDetails = extractTranscriptionUsage(result.TranscriptionResponse.Usage)

	case result.TranscriptionStreamResponse != nil && result.TranscriptionStreamResponse.Usage != nil:
		input.usage, input.audioSeconds, input.audioTokenDetails = extractTranscriptionUsage(result.TranscriptionStreamResponse.Usage)

	case result.ImageGenerationResponse != nil:
		// Defensive copy: populateOutputImageCount writes into imageUsage,
		// and we must not mutate the caller's BifrostResponse during what is
		// otherwise a pure read path.
		if result.ImageGenerationResponse.Usage != nil {
			input.imageUsage = result.ImageGenerationResponse.Usage.DeepCopy()
		} else {
			// No usage data but response exists — default to empty so per-image pricing can apply
			input.imageUsage = &schemas.ImageUsage{}
		}
		populateOutputImageCount(input.imageUsage, len(result.ImageGenerationResponse.Data))
		if result.ImageGenerationResponse.ImageGenerationResponseParameters != nil {
			input.imageSize = result.ImageGenerationResponse.ImageGenerationResponseParameters.Size
			input.imageQuality = result.ImageGenerationResponse.ImageGenerationResponseParameters.Quality
		}

	case result.ImageGenerationStreamResponse != nil:
		// Defensive copy mirrors the non-stream path so CalculateCost never
		// aliases the caller's response — keeps the read-only invariant
		// uniform and prevents accidental mutation if image-count derivation
		// is later added on this branch.
		if result.ImageGenerationStreamResponse.Usage != nil {
			input.imageUsage = result.ImageGenerationStreamResponse.Usage.DeepCopy()
		} else {
			input.imageUsage = &schemas.ImageUsage{}
		}
		input.imageSize = result.ImageGenerationStreamResponse.Size
		input.imageQuality = result.ImageGenerationStreamResponse.Quality

	case result.VideoGenerationResponse != nil && result.VideoGenerationResponse.Usage != nil && result.VideoGenerationResponse.Usage.Cost != nil:
		// Provider-reported cost (e.g. Runware's per-task cost). Routed through input.usage.Cost so
		// the provider-cost short-circuit in computeCost uses it verbatim; covers task types (3D,
		// etc.) that have no datasheet rate.
		input.usage = &schemas.BifrostLLMUsage{Cost: result.VideoGenerationResponse.Usage.Cost}

	case result.VideoGenerationResponse != nil && result.VideoGenerationResponse.Seconds != nil:
		seconds, err := strconv.Atoi(*result.VideoGenerationResponse.Seconds)
		if err == nil {
			input.videoSeconds = &seconds
		}

	case result.OCRResponse != nil:
		pages := len(result.OCRResponse.Pages)
		if result.OCRResponse.UsageInfo != nil && result.OCRResponse.UsageInfo.PagesProcessed > 0 {
			pages = result.OCRResponse.UsageInfo.PagesProcessed
		}
		input.ocrProcessedPages = &pages
		isAnnotated := result.OCRResponse.DocumentAnnotation != nil && *result.OCRResponse.DocumentAnnotation != ""
		input.ocrIsAnnotated = &isAnnotated

	case result.ContainerCreateResponse != nil:
		if memLimit := result.ContainerCreateResponse.MemoryLimit; memLimit != "" {
			input.containerIdentifierString = "container-" + memLimit
		} else {
			input.containerIdentifierString = "container"
		}
	}

	return input
}

func responsesUsageToBifrostUsage(u *schemas.ResponsesResponseUsage) *schemas.BifrostLLMUsage {
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
		Cost:             u.Cost,
	}
	// Map token details for cache and search query pricing
	if u.InputTokensDetails != nil {
		usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
			TextTokens:              u.InputTokensDetails.TextTokens,
			AudioTokens:             u.InputTokensDetails.AudioTokens,
			ImageTokens:             u.InputTokensDetails.ImageTokens,
			CachedReadTokens:        u.InputTokensDetails.CachedReadTokens,
			CachedWriteTokens:       u.InputTokensDetails.CachedWriteTokens,
			CachedWriteTokenDetails: u.InputTokensDetails.CachedWriteTokenDetails,
		}
	}
	if u.OutputTokensDetails != nil {
		usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{
			ReasoningTokens: u.OutputTokensDetails.ReasoningTokens,
			AudioTokens:     u.OutputTokensDetails.AudioTokens,
		}
		if u.OutputTokensDetails.NumSearchQueries != nil {
			usage.CompletionTokensDetails.NumSearchQueries = u.OutputTokensDetails.NumSearchQueries
		}
	}
	return usage
}

func speechUsageToBifrostUsage(u *schemas.SpeechUsage) *schemas.BifrostLLMUsage {
	return &schemas.BifrostLLMUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
	}
}

func extractTranscriptionUsage(u *schemas.TranscriptionUsage) (*schemas.BifrostLLMUsage, *int, *schemas.TranscriptionUsageInputTokenDetails) {
	usage := &schemas.BifrostLLMUsage{}
	if u.InputTokens != nil {
		usage.PromptTokens = *u.InputTokens
	}
	if u.OutputTokens != nil {
		usage.CompletionTokens = *u.OutputTokens
	}
	if u.TotalTokens != nil {
		usage.TotalTokens = *u.TotalTokens
	} else {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}

	var audioTokenDetails *schemas.TranscriptionUsageInputTokenDetails
	if u.InputTokenDetails != nil {
		audioTokenDetails = &schemas.TranscriptionUsageInputTokenDetails{
			AudioTokens: u.InputTokenDetails.AudioTokens,
			TextTokens:  u.InputTokenDetails.TextTokens,
		}
	}

	var audioSeconds *int
	if u.Seconds != nil {
		audioSeconds = new(int(*u.Seconds))
	}

	return usage, audioSeconds, audioTokenDetails
}

// ---------------------------------------------------------------------------
// Per-request-type cost computation
// ---------------------------------------------------------------------------

// computeTextCost handles chat, text completion, and responses requests.
// It returns a per-category cost breakdown; TotalCost equals the sum of every
// component so callers that only need the total can read that field. Returns
// nil when usage is nil.
func computeTextCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, tier serviceTier) *schemas.BifrostCost {
	if usage == nil {
		return nil
	}

	promptTokens := usage.PromptTokens
	completionTokens := usage.CompletionTokens

	// Extract cached token counts
	cachedReadTokens := 0
	cachedWriteTokens := 0
	cachedWriteTokensAbove1hr := 0
	if usage.PromptTokensDetails != nil {
		cachedReadTokens = usage.PromptTokensDetails.CachedReadTokens
		cachedWriteTokens = usage.PromptTokensDetails.CachedWriteTokens
		if usage.PromptTokensDetails.CachedWriteTokenDetails != nil {
			cachedWriteTokensAbove1hr = usage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h
		}
	}

	// Long-context pricing tiers are selected by input context size. Once
	// selected, the tier's input/cache/output rates apply to their respective
	// billed token categories for the request.
	tierTokens := promptTokens
	inputRate := tieredInputRate(pricing, tierTokens, tier)
	outputRate := tieredOutputRate(pricing, tierTokens, tier)
	cacheReadInputRate := tieredCacheReadInputTokenRate(pricing, tierTokens, tier)
	cacheCreationInputRate := tieredCacheCreationInputTokenRate(pricing, tierTokens, tier)
	cacheCreationInputAbove1hrInputRate := tieredCacheCreationInputAbove1hrTokenRate(pricing, tierTokens, tier)

	// Clamp cached token counts to avoid negative billing on malformed provider payloads
	if cachedReadTokens > promptTokens {
		cachedReadTokens = promptTokens
	}
	if cachedWriteTokens > promptTokens-cachedReadTokens {
		cachedWriteTokens = promptTokens - cachedReadTokens
	}
	// Should not happen, but just in case
	if cachedWriteTokensAbove1hr > cachedWriteTokens {
		cachedWriteTokensAbove1hr = cachedWriteTokens
	}

	// Input cost components, tracked separately so the breakdown reports each
	// category; together they sum to the input token cost.
	nonCachedPrompt := promptTokens - cachedReadTokens - cachedWriteTokens
	textInputCost := float64(nonCachedPrompt) * inputRate

	cacheReadCost := 0.0
	if cachedReadTokens > 0 {
		cacheReadCost = float64(cachedReadTokens) * cacheReadInputRate
	}

	cacheWriteCost := 0.0
	if cachedWriteTokens > 0 {
		if cachedWriteTokensAbove1hr > 0 {
			cacheWriteCost += float64(cachedWriteTokensAbove1hr) * cacheCreationInputAbove1hrInputRate
		}
		cacheWriteCost += float64(cachedWriteTokens-cachedWriteTokensAbove1hr) * cacheCreationInputRate
	}

	textOutputCost := float64(completionTokens) * outputRate

	// Audio token cost: when token details include audio tokens, price them at
	// the dedicated audio rate and drop them from the text token cost above.
	// Realtime and audio-enabled chat models report audio tokens in details.
	// AudioCost carries the full audio-token charge; TextCost keeps only
	// non-audio tokens. The side totals are unchanged: what leaves TextCost at
	// the text rate re-enters AudioCost at the audio rate.
	inputAudioCost := 0.0
	outputAudioCost := 0.0
	inputAudioTokens := 0
	outputAudioTokens := 0
	if usage.PromptTokensDetails != nil {
		inputAudioTokens = usage.PromptTokensDetails.AudioTokens
	}
	if usage.CompletionTokensDetails != nil {
		outputAudioTokens = usage.CompletionTokensDetails.AudioTokens
	}
	if inputAudioTokens < 0 {
		inputAudioTokens = 0
	} else if inputAudioTokens > promptTokens {
		inputAudioTokens = promptTokens
	}
	if outputAudioTokens < 0 {
		outputAudioTokens = 0
	} else if outputAudioTokens > completionTokens {
		outputAudioTokens = completionTokens
	}
	if inputAudioTokens > 0 && pricing.InputCostPerAudioToken != nil {
		if inputAudioTokens > nonCachedPrompt {
			inputAudioTokens = nonCachedPrompt
		}
		inputAudioCost = float64(inputAudioTokens) * *pricing.InputCostPerAudioToken
		textInputCost -= float64(inputAudioTokens) * inputRate
	}
	if outputAudioTokens > 0 && pricing.OutputCostPerAudioToken != nil {
		outputAudioCost = float64(outputAudioTokens) * *pricing.OutputCostPerAudioToken
		textOutputCost -= float64(outputAudioTokens) * outputRate
	}

	// Search query cost (billed on the output side)
	searchCost := 0.0
	if pricing.SearchContextCostPerQuery != nil && usage.CompletionTokensDetails != nil && usage.CompletionTokensDetails.NumSearchQueries != nil {
		searchCost = float64(*usage.CompletionTokensDetails.NumSearchQueries) * *pricing.SearchContextCostPerQuery
	}

	// Data residency (Anthropic inference_geo:"us") scales all token/cache costs
	// by a flat multiplier; the per-search fee is not a token category, so it is
	// excluded.
	if tier.inferenceGeoUS && pricing.InferenceGeoUSMultiplier != nil {
		m := *pricing.InferenceGeoUSMultiplier
		textInputCost *= m
		cacheReadCost *= m
		cacheWriteCost *= m
		inputAudioCost *= m
		textOutputCost *= m
		outputAudioCost *= m
	}

	inputCost := textInputCost + cacheReadCost + cacheWriteCost + inputAudioCost
	outputCost := textOutputCost + outputAudioCost + searchCost

	cost := &schemas.BifrostCost{
		InputCost:  inputCost,
		OutputCost: outputCost,
		TotalCost:  inputCost + outputCost,
	}
	if textInputCost != 0 || inputAudioCost != 0 || cacheReadCost != 0 || cacheWriteCost != 0 {
		cost.InputCostDetails = &schemas.InputCostDetails{
			TextCost:        textInputCost,
			AudioCost:       inputAudioCost,
			CachedReadCost:  cacheReadCost,
			CachedWriteCost: cacheWriteCost,
		}
	}
	if textOutputCost != 0 || outputAudioCost != 0 || searchCost != 0 {
		cost.OutputCostDetails = &schemas.OutputCostDetails{
			TextCost:          textOutputCost,
			AudioCost:         outputAudioCost,
			SearchQueriesCost: searchCost,
		}
	}
	return cost
}

// computeBatchTextCost handles token usage returned by batch result retrieval.
// When the catalog has an explicit batch rate, that rate is used. When it does
// not, the rate defaults to defaultBatchPricingRatio of the synchronous rate.
// A model with neither rate is left unpriced.
func computeBatchTextCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, tier serviceTier) *schemas.BifrostCost {
	if usage == nil {
		return nil
	}
	// Falls back to defaultBatchPricingRatio of the standard rate when the
	// catalog has no batch-specific rate; nil only when neither rate exists.
	resolvedInputRate := resolveBatchRate(pricing.InputCostPerToken, pricing.InputCostPerTokenBatches)
	resolvedOutputRate := resolveBatchRate(pricing.OutputCostPerToken, pricing.OutputCostPerTokenBatches)
	if usage.PromptTokens > 0 && resolvedInputRate == nil {
		return nil
	}
	if usage.CompletionTokens > 0 && resolvedOutputRate == nil {
		return nil
	}

	inputCost := 0.0
	if usage.PromptTokens > 0 {
		promptTokens := usage.PromptTokens
		cachedReadTokens := 0
		cachedWriteTokens := 0
		cachedWriteTokensAbove1hr := 0
		if usage.PromptTokensDetails != nil {
			cachedReadTokens = usage.PromptTokensDetails.CachedReadTokens
			cachedWriteTokens = usage.PromptTokensDetails.CachedWriteTokens
			if usage.PromptTokensDetails.CachedWriteTokenDetails != nil {
				cachedWriteTokensAbove1hr = usage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h
			}
		}
		cachedReadTokens = min(max(cachedReadTokens, 0), promptTokens)
		cachedWriteTokens = min(max(cachedWriteTokens, 0), promptTokens-cachedReadTokens)
		cachedWriteTokensAbove1hr = min(max(cachedWriteTokensAbove1hr, 0), cachedWriteTokens)

		batchInputRate := *resolvedInputRate
		inputRate := batchInputRate
		cacheReadRate := batchInputRate
		cacheWriteRate := batchInputRate
		cacheWriteAbove1hrRate := batchInputRate
		// Catalog long-context tiers and cache rates describe synchronous pricing.
		// Batch discounts stack with them, so scale each category by the same
		// batch/input ratio instead of charging every token at the flat batch rate.
		if pricing.InputCostPerToken != nil && *pricing.InputCostPerToken > 0 {
			batchRatio := batchInputRate / *pricing.InputCostPerToken

			switch {
			case promptTokens > TokenTierAbove272K && pricing.InputCostPerTokenAbove272kTokens != nil:
				inputRate = *pricing.InputCostPerTokenAbove272kTokens * batchRatio
			case promptTokens > TokenTierAbove200K && pricing.InputCostPerTokenAbove200kTokens != nil:
				inputRate = *pricing.InputCostPerTokenAbove200kTokens * batchRatio
			case promptTokens > TokenTierAbove128K && pricing.InputCostPerTokenAbove128kTokens != nil:
				inputRate = *pricing.InputCostPerTokenAbove128kTokens * batchRatio
			}

			if pricing.CacheReadInputTokenCost != nil {
				cacheReadRate = *pricing.CacheReadInputTokenCost * batchRatio
			}
			if promptTokens > TokenTierAbove272K && pricing.CacheReadInputTokenCostAbove272kTokens != nil {
				cacheReadRate = *pricing.CacheReadInputTokenCostAbove272kTokens * batchRatio
			} else if promptTokens > TokenTierAbove200K && pricing.CacheReadInputTokenCostAbove200kTokens != nil {
				cacheReadRate = *pricing.CacheReadInputTokenCostAbove200kTokens * batchRatio
			}

			if pricing.CacheCreationInputTokenCost != nil {
				cacheWriteRate = *pricing.CacheCreationInputTokenCost * batchRatio
			}
			if promptTokens > TokenTierAbove272K && pricing.CacheCreationInputTokenCostAbove272kTokens != nil {
				cacheWriteRate = *pricing.CacheCreationInputTokenCostAbove272kTokens * batchRatio
			} else if promptTokens > TokenTierAbove200K && pricing.CacheCreationInputTokenCostAbove200kTokens != nil {
				cacheWriteRate = *pricing.CacheCreationInputTokenCostAbove200kTokens * batchRatio
			}

			if promptTokens > TokenTierAbove200K && pricing.CacheCreationInputTokenCostAbove1hrAbove200kTokens != nil {
				cacheWriteAbove1hrRate = *pricing.CacheCreationInputTokenCostAbove1hrAbove200kTokens * batchRatio
			} else if pricing.CacheCreationInputTokenCostAbove1hr != nil {
				cacheWriteAbove1hrRate = *pricing.CacheCreationInputTokenCostAbove1hr * batchRatio
			} else {
				cacheWriteAbove1hrRate = cacheWriteRate
			}
		}

		nonCachedPrompt := promptTokens - cachedReadTokens - cachedWriteTokens
		inputCost = float64(nonCachedPrompt)*inputRate +
			float64(cachedReadTokens)*cacheReadRate +
			float64(cachedWriteTokens-cachedWriteTokensAbove1hr)*cacheWriteRate +
			float64(cachedWriteTokensAbove1hr)*cacheWriteAbove1hrRate
	}

	outputCost := 0.0
	if usage.CompletionTokens > 0 {
		outputRate := *resolvedOutputRate
		// Tier is selected by prompt/context size, mirroring computeTextCost's
		// tieredOutputRate — output pricing tiers key off input context length,
		// not completion length.
		if pricing.OutputCostPerToken != nil && *pricing.OutputCostPerToken > 0 {
			outputBatchRatio := outputRate / *pricing.OutputCostPerToken
			promptTokens := usage.PromptTokens
			switch {
			case promptTokens > TokenTierAbove272K && pricing.OutputCostPerTokenAbove272kTokens != nil:
				outputRate = *pricing.OutputCostPerTokenAbove272kTokens * outputBatchRatio
			case promptTokens > TokenTierAbove200K && pricing.OutputCostPerTokenAbove200kTokens != nil:
				outputRate = *pricing.OutputCostPerTokenAbove200kTokens * outputBatchRatio
			case promptTokens > TokenTierAbove128K && pricing.OutputCostPerTokenAbove128kTokens != nil:
				outputRate = *pricing.OutputCostPerTokenAbove128kTokens * outputBatchRatio
			}
		}
		outputCost = float64(usage.CompletionTokens) * outputRate
	}

	// Data residency (Anthropic inference_geo:"us") scales all token or cache costs
	// by a flat multiplier, mirroring computeTextCost — batch and data residency
	// are independent axes, so a batch request can still carry it. Scale each side
	// so the input/output split is preserved.
	if tier.inferenceGeoUS && pricing.InferenceGeoUSMultiplier != nil {
		inputCost *= *pricing.InferenceGeoUSMultiplier
		outputCost *= *pricing.InferenceGeoUSMultiplier
	}
	return newInputOutputCost(inputCost, outputCost)
}

// computeEmbeddingCost handles embedding requests (input-only).
func computeEmbeddingCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, tier serviceTier) *schemas.BifrostCost {
	if usage == nil {
		return nil
	}
	c := float64(usage.PromptTokens) * tieredInputRate(pricing, usage.PromptTokens, tier)
	if c == 0 {
		return nil
	}
	return &schemas.BifrostCost{
		InputCost:        c,
		InputCostDetails: &schemas.InputCostDetails{TextCost: c},
		TotalCost:        c,
	}
}

// computeRerankCost handles rerank requests.
//
// Rerank is priced two different ways depending on the model. Cohere, Bedrock and Azure bill per
// query - one query covers up to 100 document chunks, so a larger request bills as several - while
// hosted rerankers such as Voyage and Jina bill per token. Both terms are summed because a given
// pricing row only ever carries one of them.
//
// Per-query pricing needs no usage at all, so a nil usage still bills one query rather than
// returning zero: Vertex reports no usage on rerank, and dropping the charge there would silently
// under-report every one of its calls.
func computeRerankCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, tier serviceTier) *schemas.BifrostCost {
	queryCost := 0.0
	if pricing.InputCostPerQuery != nil {
		// Providers that report their own billed unit count win: Cohere's search_units already
		// accounts for long documents being chunked past the 100-per-query boundary.
		queries := 1
		if usage != nil && usage.SearchUnits != nil && *usage.SearchUnits > 0 {
			queries = *usage.SearchUnits
		}
		queryCost = float64(queries) * *pricing.InputCostPerQuery
	}

	// Token terms stay zero when the response reports no usage; the per-query charge above
	// still applies.
	inputCost, outputCost, searchCost := 0.0, 0.0, 0.0
	if usage != nil {
		tierTokens := usage.PromptTokens
		inputCost = float64(usage.PromptTokens) * tieredInputRate(pricing, tierTokens, tier)
		outputCost = float64(usage.CompletionTokens) * tieredOutputRate(pricing, tierTokens, tier)

		if pricing.SearchContextCostPerQuery != nil && usage.CompletionTokensDetails != nil && usage.CompletionTokensDetails.NumSearchQueries != nil {
			searchCost = float64(*usage.CompletionTokensDetails.NumSearchQueries) * *pricing.SearchContextCostPerQuery
		}
	}

	// Search queries are billed on the output side, matching computeTextCost. The flat
	// per-query rerank charge maps to no token category, so it folds into the input side
	// as RequestCost, matching CostPerRequest.
	inputTokensCost := inputCost + queryCost
	outputTokensCost := outputCost + searchCost

	var inputDetails *schemas.InputCostDetails
	if inputTokensCost != 0 {
		inputDetails = &schemas.InputCostDetails{TextCost: inputCost, RequestCost: queryCost}
	}
	var outputDetails *schemas.OutputCostDetails
	if outputTokensCost != 0 {
		outputDetails = &schemas.OutputCostDetails{TextCost: outputCost, SearchQueriesCost: searchCost}
	}
	return newInputOutputCostWithDetails(inputTokensCost, outputTokensCost, inputDetails, outputDetails)
}

// newInputOutputCost builds a BifrostCost from separate input and output costs,
// or returns nil when both are zero (nothing to record).
func newInputOutputCost(inputCost, outputCost float64) *schemas.BifrostCost {
	return newInputOutputCostWithDetails(inputCost, outputCost, nil, nil)
}

// newInputOutputCostWithDetails is newInputOutputCost with the nested per-category
// breakdowns attached to each side. Details are passed through as-is (callers guard
// them the same way computeTextCost does), so a nil side stays absent.
func newInputOutputCostWithDetails(inputCost, outputCost float64, inputDetails *schemas.InputCostDetails, outputDetails *schemas.OutputCostDetails) *schemas.BifrostCost {
	total := inputCost + outputCost
	if total == 0 {
		return nil
	}
	return &schemas.BifrostCost{
		InputCost:         inputCost,
		InputCostDetails:  inputDetails,
		OutputCost:        outputCost,
		OutputCostDetails: outputDetails,
		TotalCost:         total,
	}
}

// computeSpeechCost handles speech (TTS) requests.
// Input is text (PromptTokens), output is audio (CompletionTokens).
//
// Per-character pricing (InputCostPerCharacter) is used as first-class support for TTS/audio
// models — providers such as OpenAI TTS, ElevenLabs, and AWS Polly bill per character of
// input text rather than per token. PromptTokens from usage is treated as the character count
// since TTS providers report their billable unit in that field.
// Output falls back to per-second duration when no audio token rate is configured.
func computeSpeechCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, audioTextInputChars int, tier serviceTier) *schemas.BifrostCost {
	tierTokens := inputTierTokens(usage)

	// Input: per-character rate takes precedence for TTS/audio models
	inputCost := 0.0
	if audioTextInputChars > 0 {
		if pricing.InputCostPerCharacter != nil {
			inputCost = float64(audioTextInputChars) * *pricing.InputCostPerCharacter
		} else {
			inputCost = float64(audioTextInputChars) * tieredInputRate(pricing, tierTokens, tier)
		}
	} else if usage != nil && usage.PromptTokens > 0 {
		inputCost = float64(usage.PromptTokens) * tieredInputRate(pricing, tierTokens, tier)
	}

	// Output: audio tokens first, then per-second fallback
	outputCost := computeAudioOutputCost(pricing, usage, audioSeconds, tierTokens, tier)

	// Input is text (chars/tokens), output is audio.
	var inputDetails *schemas.InputCostDetails
	if inputCost != 0 {
		inputDetails = &schemas.InputCostDetails{TextCost: inputCost}
	}
	var outputDetails *schemas.OutputCostDetails
	if outputCost != 0 {
		outputDetails = &schemas.OutputCostDetails{AudioCost: outputCost}
	}
	return newInputOutputCostWithDetails(inputCost, outputCost, inputDetails, outputDetails)
}

// computeTranscriptionCost handles transcription (STT) requests.
// Input is audio, output is text (CompletionTokens).
// Input and output are calculated independently — tokens first, then per-second fallback.
func computeTranscriptionCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, audioTokenDetails *schemas.TranscriptionUsageInputTokenDetails, tier serviceTier) *schemas.BifrostCost {
	tierTokens := inputTierTokens(usage)

	// Input: audio tokens/details first, then per-second fallback
	inputCost := computeAudioInputCost(pricing, usage, audioSeconds, audioTokenDetails, tierTokens, tier)

	// Output: text tokens
	outputCost := 0.0
	if usage != nil && usage.CompletionTokens > 0 {
		outputCost = float64(usage.CompletionTokens) * tieredOutputRate(pricing, tierTokens, tier)
	}

	// Input is audio, output is text. When audio-token details carry a text-token
	// portion (see computeAudioInputCost), split it out so AudioCost + TextCost
	// reconcile with the authoritative input total.
	var inputDetails *schemas.InputCostDetails
	if inputCost != 0 {
		textPortion := 0.0
		if audioTokenDetails != nil && audioTokenDetails.TextTokens > 0 {
			textPortion = float64(audioTokenDetails.TextTokens) * tieredInputRate(pricing, tierTokens, tier)
		}
		inputDetails = &schemas.InputCostDetails{AudioCost: inputCost - textPortion, TextCost: textPortion}
	}
	var outputDetails *schemas.OutputCostDetails
	if outputCost != 0 {
		outputDetails = &schemas.OutputCostDetails{TextCost: outputCost}
	}
	return newInputOutputCostWithDetails(inputCost, outputCost, inputDetails, outputDetails)
}

// computeAudioInputCost calculates input cost for audio: audio token details first,
// then generic input tokens, then per-second duration fallback.
func computeAudioInputCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, audioTokenDetails *schemas.TranscriptionUsageInputTokenDetails, totalTokens int, tier serviceTier) float64 {
	// Audio token detail pricing (audio + text token breakdown)
	if audioTokenDetails != nil && (audioTokenDetails.AudioTokens > 0 || audioTokenDetails.TextTokens > 0) {
		return float64(audioTokenDetails.AudioTokens)*tieredAudioTokenInputRate(pricing, totalTokens, tier) +
			float64(audioTokenDetails.TextTokens)*tieredInputRate(pricing, totalTokens, tier)
	}

	// Generic input tokens
	if usage != nil && usage.PromptTokens > 0 {
		return float64(usage.PromptTokens) * tieredInputRate(pricing, totalTokens, tier)
	}

	// Per-second duration fallback
	if audioSeconds != nil && *audioSeconds > 0 {
		if rate := tieredAudioInputPerSecondRate(pricing, totalTokens); rate > 0 {
			return float64(*audioSeconds) * rate
		}
	}

	return 0
}

// computeAudioOutputCost calculates output cost for audio: audio tokens first,
// then generic output tokens, then per-second duration fallback.
func computeAudioOutputCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, audioSeconds *int, totalTokens int, tier serviceTier) float64 {
	// Audio-specific output tokens
	if usage != nil && usage.CompletionTokens > 0 {
		return float64(usage.CompletionTokens) * tieredAudioTokenOutputRate(pricing, totalTokens, tier)
	}

	// Per-second duration fallback
	if audioSeconds != nil && *audioSeconds > 0 {
		if pricing.OutputCostPerSecond != nil {
			return float64(*audioSeconds) * *pricing.OutputCostPerSecond
		}
	}

	return 0
}

// computeImageCost handles image generation requests.
// Input and output are calculated independently — each tries token-based pricing first,
// then per-pixel pricing, falling back to per-image count pricing.
// imageQuality must be one of "low", "medium", "high", "standard", "auto" to use
// quality-specific rates; other values use base rates.
func computeImageCost(pricing *configstoreTables.TableModelPricing, imageUsage *schemas.ImageUsage, imageSize string, imageQuality string, tier serviceTier) *schemas.BifrostCost {
	if imageUsage == nil {
		return nil
	}

	tierTokens := imageInputTierTokens(imageUsage)
	width, height := parseImageDimensions(imageSize)
	pixels := width * height
	inputCost := computeImageInputCost(pricing, imageUsage, tierTokens, pixels, tier)
	outputCost := computeImageOutputCost(pricing, imageUsage, tierTokens, width, height, pixels, imageQuality, tier)

	return newInputOutputCost(inputCost, outputCost)
}

// computeImageInputCost calculates input cost: tokens first, then per-pixel, then per-image count fallback.
func computeImageInputCost(pricing *configstoreTables.TableModelPricing, imageUsage *schemas.ImageUsage, totalTokens int, pixels int, tier serviceTier) float64 {
	// Try token-based pricing first
	var inputTextTokens, inputImageTokens int
	if imageUsage.InputTokensDetails != nil {
		inputImageTokens = imageUsage.InputTokensDetails.ImageTokens
		inputTextTokens = imageUsage.InputTokensDetails.TextTokens
	} else {
		inputTextTokens = imageUsage.InputTokens
	}

	if inputTextTokens > 0 || inputImageTokens > 0 {
		return float64(inputTextTokens)*tieredInputRate(pricing, totalTokens, tier) +
			float64(inputImageTokens)*tieredImageInputRate(pricing, totalTokens, tier)
	}

	// Per-pixel pricing fallback
	if pricing.InputCostPerPixel != nil && pixels > 0 && imageUsage.NumInputImages > 0 {
		return float64(pixels*imageUsage.NumInputImages) * *pricing.InputCostPerPixel
	}

	// Fall back to per-image count pricing
	if pricing.InputCostPerImage != nil && imageUsage.NumInputImages > 0 {
		return float64(imageUsage.NumInputImages) * *pricing.InputCostPerImage
	}

	return 0
}

// computeImageOutputCost calculates output cost: tokens first, then per-pixel, then per-image count fallback.
// imageQuality: "low", "medium", "high", "standard", "auto" use quality-specific rates when available; other values use base/size-tier rates.
func computeImageOutputCost(pricing *configstoreTables.TableModelPricing, imageUsage *schemas.ImageUsage, totalTokens int, width, height, pixels int, imageQuality string, tier serviceTier) float64 {
	// Try token-based pricing first
	var outputTextTokens, outputImageTokens int
	if imageUsage.OutputTokensDetails != nil {
		outputImageTokens = imageUsage.OutputTokensDetails.ImageTokens
		outputTextTokens = imageUsage.OutputTokensDetails.TextTokens
	} else {
		outputImageTokens = imageUsage.OutputTokens
	}

	if outputTextTokens > 0 || outputImageTokens > 0 {
		return float64(outputTextTokens)*tieredOutputRate(pricing, totalTokens, tier) +
			float64(outputImageTokens)*tieredImageOutputRate(pricing, totalTokens, tier)
	}

	// Per-pixel pricing fallback
	if pricing.OutputCostPerPixel != nil && pixels > 0 {
		numOutputImages := 1
		if imageUsage.OutputTokensDetails != nil && imageUsage.OutputTokensDetails.NImages > 0 {
			numOutputImages = imageUsage.OutputTokensDetails.NImages
		}
		return float64(pixels*numOutputImages) * *pricing.OutputCostPerPixel
	}

	// Fall back to per-image count pricing with size-tier selection
	// TODO: handle premium image flag when it becomes available in imageUsage
	numOutputImages := 1
	if imageUsage.OutputTokensDetails != nil && imageUsage.OutputTokensDetails.NImages > 0 {
		numOutputImages = imageUsage.OutputTokensDetails.NImages
	}
	q := imageQuality
	if q == "" {
		q = "auto"
	}
	// Most specific rate wins: joint size+quality, then quality-only, then
	// size-only, then the flat per-image rate.
	perImageRate := imageSizeRatesForQuality(pricing, q).rateForSize(width, height, pixels)
	if perImageRate == nil {
		perImageRate = imageQualityRate(pricing, q)
	}
	if perImageRate == nil {
		perImageRate = baseImageSizeRates(pricing).rateForSize(width, height, pixels)
	}
	if perImageRate == nil {
		perImageRate = pricing.OutputCostPerImage
	}
	if perImageRate != nil {
		return float64(numOutputImages) * *perImageRate
	}

	return 0
}

// imageSizeRates holds the per-image output rates for each size threshold:
// either the base set or the set belonging to one image quality.
type imageSizeRates struct {
	above512x512   *float64
	above1024x1024 *float64
	above1024x1536 *float64
	above1536x1024 *float64
	above2048x2048 *float64
	above4096x4096 *float64
	above4MP       *float64
	above8MP       *float64
	above16MP      *float64
	above32MP      *float64
	above64MP      *float64
}

// rateForSize returns the rate for the largest size threshold the generated
// image meets, or nil when no matching threshold is configured.
//
// Two independent tier families exist because providers publish resolution-based
// pricing in different units: some tier by exact width×height threshold
// (output_cost_per_image_above_<N>x<N>_pixels), others (e.g. Replicate's upscaler
// models) tier by total output megapixels (output_cost_per_image_above_<N>_megapixels).
// A given model is expected to populate only one family; both are checked here,
// interleaved strictly by their actual pixel threshold largest-first — NOT by field
// family — since 4096x4096 (16,777,216px) falls between the 16MP and 32MP megapixel
// thresholds, and 2048x2048 (4,194,304px) falls just above the 4MP threshold.
//
// 1536x1024 and 1024x1536 have identical pixel counts, so they are matched on
// width and height rather than on the total, and are checked ahead of the
// square 1024x1024 threshold that both of them exceed.
func (r imageSizeRates) rateForSize(width, height, pixels int) *float64 {
	const (
		pixels512x512      = 512 * 512
		pixels1024x1024    = 1024 * 1024
		pixels2048x2048    = 2048 * 2048
		pixels4Megapixels  = 4_000_000
		pixels4096x4096    = 4096 * 4096
		pixels8Megapixels  = 8_000_000
		pixels16Megapixels = 16_000_000
		pixels32Megapixels = 32_000_000
		pixels64Megapixels = 64_000_000
	)
	switch {
	case pixels >= pixels64Megapixels && r.above64MP != nil:
		return r.above64MP
	case pixels >= pixels32Megapixels && r.above32MP != nil:
		return r.above32MP
	case pixels >= pixels4096x4096 && r.above4096x4096 != nil:
		return r.above4096x4096
	case pixels >= pixels16Megapixels && r.above16MP != nil:
		return r.above16MP
	case pixels >= pixels8Megapixels && r.above8MP != nil:
		return r.above8MP
	case pixels >= pixels2048x2048 && r.above2048x2048 != nil:
		return r.above2048x2048
	case pixels >= pixels4Megapixels && r.above4MP != nil:
		return r.above4MP
	case width >= 1536 && height >= 1024 && r.above1536x1024 != nil:
		return r.above1536x1024
	case width >= 1024 && height >= 1536 && r.above1024x1536 != nil:
		return r.above1024x1536
	case pixels >= pixels1024x1024 && r.above1024x1024 != nil:
		return r.above1024x1024
	case pixels >= pixels512x512 && r.above512x512 != nil:
		return r.above512x512
	}
	return nil
}

// baseImageSizeRates collects the size-threshold rates that apply regardless of
// the requested image quality.
func baseImageSizeRates(pricing *configstoreTables.TableModelPricing) imageSizeRates {
	return imageSizeRates{
		above512x512:   pricing.OutputCostPerImageAbove512x512Pixels,
		above1024x1024: pricing.OutputCostPerImageAbove1024x1024Pixels,
		above1024x1536: pricing.OutputCostPerImageAbove1024x1536Pixels,
		above1536x1024: pricing.OutputCostPerImageAbove1536x1024Pixels,
		above2048x2048: pricing.OutputCostPerImageAbove2048x2048Pixels,
		above4096x4096: pricing.OutputCostPerImageAbove4096x4096Pixels,
		above4MP:       pricing.OutputCostPerImageAbove4Megapixels,
		above8MP:       pricing.OutputCostPerImageAbove8Megapixels,
		above16MP:      pricing.OutputCostPerImageAbove16Megapixels,
		above32MP:      pricing.OutputCostPerImageAbove32Megapixels,
		above64MP:      pricing.OutputCostPerImageAbove64Megapixels,
	}
}

// imageSizeRatesForQuality collects the size-threshold rates that apply only to
// the given image quality. Returns the zero set for a quality that has no
// size-specific rates.
func imageSizeRatesForQuality(pricing *configstoreTables.TableModelPricing, quality string) imageSizeRates {
	switch quality {
	case "low":
		return imageSizeRates{
			above1024x1024: pricing.OutputCostPerImageAbove1024x1024PixelsLowQuality,
			above1024x1536: pricing.OutputCostPerImageAbove1024x1536PixelsLowQuality,
			above1536x1024: pricing.OutputCostPerImageAbove1536x1024PixelsLowQuality,
		}
	case "medium":
		return imageSizeRates{
			above1024x1024: pricing.OutputCostPerImageAbove1024x1024PixelsMediumQuality,
			above1024x1536: pricing.OutputCostPerImageAbove1024x1536PixelsMediumQuality,
			above1536x1024: pricing.OutputCostPerImageAbove1536x1024PixelsMediumQuality,
		}
	case "high":
		return imageSizeRates{
			above1024x1024: pricing.OutputCostPerImageAbove1024x1024PixelsHighQuality,
			above1024x1536: pricing.OutputCostPerImageAbove1024x1536PixelsHighQuality,
			above1536x1024: pricing.OutputCostPerImageAbove1536x1024PixelsHighQuality,
		}
	case "standard":
		return imageSizeRates{
			above1024x1024: pricing.OutputCostPerImageAbove1024x1024PixelsStandardQuality,
			above1024x1536: pricing.OutputCostPerImageAbove1024x1536PixelsStandardQuality,
			above1536x1024: pricing.OutputCostPerImageAbove1536x1024PixelsStandardQuality,
		}
	}
	return imageSizeRates{}
}

// imageQualityRate returns the quality-only per-image rate, independent of the
// generated image's size. "standard" has no quality-only rate upstream.
func imageQualityRate(pricing *configstoreTables.TableModelPricing, quality string) *float64 {
	switch quality {
	case "low":
		return pricing.OutputCostPerImageLowQuality
	case "medium":
		return pricing.OutputCostPerImageMediumQuality
	case "high":
		return pricing.OutputCostPerImageHighQuality
	case "auto":
		return pricing.OutputCostPerImageAutoQuality
	}
	return nil
}

// computeVideoCost handles video generation requests.
// Input and output are calculated independently — tokens first, then per-second fallback.
func computeVideoCost(pricing *configstoreTables.TableModelPricing, usage *schemas.BifrostLLMUsage, videoSeconds *int, tier serviceTier) *schemas.BifrostCost {
	tierTokens := inputTierTokens(usage)

	// Input: text prompt tokens first, then per-second fallback
	inputCost := 0.0
	if usage != nil && usage.PromptTokens > 0 {
		inputCost = float64(usage.PromptTokens) * tieredInputRate(pricing, tierTokens, tier)
	} else if videoSeconds != nil && *videoSeconds > 0 {
		if rate := tieredVideoInputPerSecondRate(pricing, tierTokens); rate > 0 {
			inputCost = float64(*videoSeconds) * rate
		}
	}

	// Output: completion tokens first, then per-second fallback
	outputCost := 0.0
	if usage != nil && usage.CompletionTokens > 0 {
		outputCost = float64(usage.CompletionTokens) * tieredOutputRate(pricing, tierTokens, tier)
	} else if videoSeconds != nil && *videoSeconds > 0 {
		if pricing.OutputCostPerVideoPerSecond != nil {
			outputCost = float64(*videoSeconds) * *pricing.OutputCostPerVideoPerSecond
		} else if pricing.OutputCostPerSecond != nil {
			outputCost = float64(*videoSeconds) * *pricing.OutputCostPerSecond
		}
	}

	return newInputOutputCost(inputCost, outputCost)
}

// computeOCRCost handles OCR requests, billing per page processed.
// ocr_cost_per_page covers base processing; annotation_cost_per_page is added when set.
func computeOCRCost(pricing *configstoreTables.TableModelPricing, ocrProcessedPages *int, ocrIsAnnotated *bool) *schemas.BifrostCost {
	if ocrProcessedPages == nil {
		return nil
	}
	pages := float64(*ocrProcessedPages)
	cost := 0.0
	if pricing.OCRCostPerPage != nil {
		cost += pages * *pricing.OCRCostPerPage
	}
	if ocrIsAnnotated != nil && *ocrIsAnnotated && pricing.AnnotationCostPerPage != nil {
		cost += pages * *pricing.AnnotationCostPerPage
	}
	// OCR is billed per page, not per input/output token, so the flat charge
	// folds onto the input side as a request cost.
	return totalOnlyCost(cost)
}

// totalOnlyCost wraps a non-token-based cost (per-page, per-session) into a
// BifrostCost, folding it onto the input side as a flat request cost, or nil
// when there is nothing to record.
func totalOnlyCost(c float64) *schemas.BifrostCost {
	if c == 0 {
		return nil
	}
	return &schemas.BifrostCost{
		InputCost:        c,
		InputCostDetails: &schemas.InputCostDetails{RequestCost: c},
		TotalCost:        c,
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// tierFromResponse builds a serviceTier from a response's billing-relevant
// fields: the OpenAI service_tier (priority/flex/ultrafast) and the Anthropic speed
// (fast mode). speed == "fast" means fast mode was actually served — the
// provider echoes the served speed, so stripped/fell-back requests report
// "standard" and bill at standard rates.
func tierFromResponse(s *schemas.BifrostServiceTier, speed *string, inferenceGeo *string) serviceTier {
	var tier serviceTier
	if s != nil {
		switch *s {
		case schemas.BifrostServiceTierPriority:
			tier.isPriority = true
		case schemas.BifrostServiceTierFlex:
			tier.isFlex = true
		case schemas.BifrostServiceTierUltrafast:
			tier.isUltrafast = true
		}
	}
	tier.isFast = speed != nil && *speed == "fast"
	tier.inferenceGeoUS = inferenceGeo != nil && strings.EqualFold(*inferenceGeo, "us")
	return tier
}

// tieredInputRate returns the effective per-token input rate based on total token count.
// Flex applies a flat rate. Priority-specific tier rates are preferred where available.
func tieredInputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	// Fast mode (Anthropic) is a flat rate across the full context window — it
	// takes precedence over the token-count tiers below.
	if tier.isFast && pricing.InputCostPerTokenFast != nil {
		return *pricing.InputCostPerTokenFast
	}
	if tier.isUltrafast && pricing.InputCostPerTokenUltrafast != nil {
		return *pricing.InputCostPerTokenUltrafast
	}
	if tier.isFlex {
		if totalTokens > TokenTierAbove272K && pricing.InputCostPerTokenFlexAbove272kTokens != nil {
			return *pricing.InputCostPerTokenFlexAbove272kTokens
		}
		if pricing.InputCostPerTokenFlex != nil {
			return *pricing.InputCostPerTokenFlex
		}
	}
	if totalTokens > TokenTierAbove272K {
		if tier.isPriority && pricing.InputCostPerTokenAbove272kTokensPriority != nil {
			return *pricing.InputCostPerTokenAbove272kTokensPriority
		}
		if pricing.InputCostPerTokenAbove272kTokens != nil {
			return *pricing.InputCostPerTokenAbove272kTokens
		}
	}
	if totalTokens > TokenTierAbove200K {
		if tier.isPriority && pricing.InputCostPerTokenAbove200kTokensPriority != nil {
			return *pricing.InputCostPerTokenAbove200kTokensPriority
		}
		if pricing.InputCostPerTokenAbove200kTokens != nil {
			return *pricing.InputCostPerTokenAbove200kTokens
		}
	}
	if totalTokens > TokenTierAbove128K && pricing.InputCostPerTokenAbove128kTokens != nil {
		return *pricing.InputCostPerTokenAbove128kTokens
	}
	if tier.isPriority && pricing.InputCostPerTokenPriority != nil {
		return *pricing.InputCostPerTokenPriority
	}
	if pricing.InputCostPerToken != nil {
		return *pricing.InputCostPerToken
	}
	return 0
}

// tieredOutputRate returns the effective per-token output rate based on total token count.
// Flex applies a flat rate. Priority-specific tier rates are preferred where available.
func tieredOutputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	// Fast mode (Anthropic) is a flat rate across the full context window — it
	// takes precedence over the token-count tiers below.
	if tier.isFast && pricing.OutputCostPerTokenFast != nil {
		return *pricing.OutputCostPerTokenFast
	}
	if tier.isUltrafast && pricing.OutputCostPerTokenUltrafast != nil {
		return *pricing.OutputCostPerTokenUltrafast
	}
	if tier.isFlex {
		if totalTokens > TokenTierAbove272K && pricing.OutputCostPerTokenFlexAbove272kTokens != nil {
			return *pricing.OutputCostPerTokenFlexAbove272kTokens
		}
		if pricing.OutputCostPerTokenFlex != nil {
			return *pricing.OutputCostPerTokenFlex
		}
	}
	if totalTokens > TokenTierAbove272K {
		if tier.isPriority && pricing.OutputCostPerTokenAbove272kTokensPriority != nil {
			return *pricing.OutputCostPerTokenAbove272kTokensPriority
		}
		if pricing.OutputCostPerTokenAbove272kTokens != nil {
			return *pricing.OutputCostPerTokenAbove272kTokens
		}
	}
	if totalTokens > TokenTierAbove200K {
		if tier.isPriority && pricing.OutputCostPerTokenAbove200kTokensPriority != nil {
			return *pricing.OutputCostPerTokenAbove200kTokensPriority
		}
		if pricing.OutputCostPerTokenAbove200kTokens != nil {
			return *pricing.OutputCostPerTokenAbove200kTokens
		}
	}
	if totalTokens > TokenTierAbove128K && pricing.OutputCostPerTokenAbove128kTokens != nil {
		return *pricing.OutputCostPerTokenAbove128kTokens
	}

	if tier.isPriority && pricing.OutputCostPerTokenPriority != nil {
		return *pricing.OutputCostPerTokenPriority
	}

	if pricing.OutputCostPerToken != nil {
		return *pricing.OutputCostPerToken
	}

	return 0
}

// tieredImageInputRate returns the effective rate for image tokens on the input side.
// Falls back to the general tieredInputRate when no image-specific rate is configured.
func tieredImageInputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if totalTokens > TokenTierAbove128K && pricing.InputCostPerImageAbove128kTokens != nil {
		return *pricing.InputCostPerImageAbove128kTokens
	}
	if pricing.InputCostPerImageToken != nil {
		return *pricing.InputCostPerImageToken
	}
	return tieredInputRate(pricing, totalTokens, tier)
}

// tieredImageOutputRate returns the effective rate for image tokens on the output side.
// Falls back to the general tieredOutputRate when no image-specific rate is configured.
func tieredImageOutputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if pricing.OutputCostPerImageToken != nil {
		return *pricing.OutputCostPerImageToken
	}
	return tieredOutputRate(pricing, totalTokens, tier)
}

// tieredAudioInputPerSecondRate returns the effective per-second rate for audio input.
func tieredAudioInputPerSecondRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if totalTokens > TokenTierAbove128K && pricing.InputCostPerAudioPerSecondAbove128kTokens != nil {
		return *pricing.InputCostPerAudioPerSecondAbove128kTokens
	}
	if pricing.InputCostPerAudioPerSecond != nil {
		return *pricing.InputCostPerAudioPerSecond
	}
	if pricing.InputCostPerSecond != nil {
		return *pricing.InputCostPerSecond
	}
	return 0
}

// tieredVideoInputPerSecondRate returns the effective per-second rate for video input.
func tieredVideoInputPerSecondRate(pricing *configstoreTables.TableModelPricing, totalTokens int) float64 {
	if totalTokens > TokenTierAbove128K && pricing.InputCostPerVideoPerSecondAbove128kTokens != nil {
		return *pricing.InputCostPerVideoPerSecondAbove128kTokens
	}
	if pricing.InputCostPerVideoPerSecond != nil {
		return *pricing.InputCostPerVideoPerSecond
	}
	return 0
}

// tieredAudioTokenInputRate returns the effective per-token rate for audio input tokens.
// Falls back to the general tieredInputRate when no audio-specific rate is configured.
func tieredAudioTokenInputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if pricing.InputCostPerAudioToken != nil {
		return *pricing.InputCostPerAudioToken
	}
	return tieredInputRate(pricing, totalTokens, tier)
}

// tieredAudioTokenOutputRate returns the effective per-token rate for audio output tokens.
// Falls back to the general tieredOutputRate when no audio-specific rate is configured.
func tieredAudioTokenOutputRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	if pricing.OutputCostPerAudioToken != nil {
		return *pricing.OutputCostPerAudioToken
	}
	return tieredOutputRate(pricing, totalTokens, tier)
}

func tieredCacheReadInputTokenRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	// Fast mode (Anthropic) is a flat rate across the full context window.
	if tier.isFast && pricing.CacheReadInputTokenCostFast != nil {
		return *pricing.CacheReadInputTokenCostFast
	}
	if tier.isUltrafast && pricing.CacheReadInputTokenCostUltrafast != nil {
		return *pricing.CacheReadInputTokenCostUltrafast
	}
	if tier.isFlex {
		if totalTokens > TokenTierAbove272K && pricing.CacheReadInputTokenCostFlexAbove272kTokens != nil {
			return *pricing.CacheReadInputTokenCostFlexAbove272kTokens
		}
		if pricing.CacheReadInputTokenCostFlex != nil {
			return *pricing.CacheReadInputTokenCostFlex
		}
	}
	if totalTokens > TokenTierAbove272K {
		if tier.isPriority && pricing.CacheReadInputTokenCostAbove272kTokensPriority != nil {
			return *pricing.CacheReadInputTokenCostAbove272kTokensPriority
		}
		if pricing.CacheReadInputTokenCostAbove272kTokens != nil {
			return *pricing.CacheReadInputTokenCostAbove272kTokens
		}
	}
	if totalTokens > TokenTierAbove200K {
		if tier.isPriority && pricing.CacheReadInputTokenCostAbove200kTokensPriority != nil {
			return *pricing.CacheReadInputTokenCostAbove200kTokensPriority
		}
		if pricing.CacheReadInputTokenCostAbove200kTokens != nil {
			return *pricing.CacheReadInputTokenCostAbove200kTokens
		}
	}
	if tier.isPriority && pricing.CacheReadInputTokenCostPriority != nil {
		return *pricing.CacheReadInputTokenCostPriority
	}
	if pricing.CacheReadInputTokenCost != nil {
		return *pricing.CacheReadInputTokenCost
	}
	return tieredInputRate(pricing, totalTokens, tier)
}

// OpenAI introduced cache-write (cache-creation) pricing with gpt-5.6, tiered by
// service tier (flex/priority/ultrafast) and by the 272k context window; Anthropic uses the
// flat fast rate. Precedence mirrors tieredCacheReadInputTokenRate.
func tieredCacheCreationInputTokenRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	// Fast mode (Anthropic) is a flat rate across the full context window.
	if tier.isFast && pricing.CacheCreationInputTokenCostFast != nil {
		return *pricing.CacheCreationInputTokenCostFast
	}
	if tier.isUltrafast && pricing.CacheCreationInputTokenCostUltrafast != nil {
		return *pricing.CacheCreationInputTokenCostUltrafast
	}
	if tier.isFlex {
		if totalTokens > TokenTierAbove272K && pricing.CacheCreationInputTokenCostFlexAbove272kTokens != nil {
			return *pricing.CacheCreationInputTokenCostFlexAbove272kTokens
		}
		if pricing.CacheCreationInputTokenCostFlex != nil {
			return *pricing.CacheCreationInputTokenCostFlex
		}
	}
	// Priority has no long context: OpenAI does not offer priority >272k, and billing
	// uses the served tier (response.service_tier), so an actual-priority request is
	// always ≤272k. Its cache-write rate is flat, so it takes precedence over the
	// standard context tiers below (which would otherwise capture the 200k–272k band).
	if tier.isPriority && pricing.CacheCreationInputTokenCostPriority != nil {
		return *pricing.CacheCreationInputTokenCostPriority
	}
	if totalTokens > TokenTierAbove272K && pricing.CacheCreationInputTokenCostAbove272kTokens != nil {
		return *pricing.CacheCreationInputTokenCostAbove272kTokens
	}
	if totalTokens > TokenTierAbove200K && pricing.CacheCreationInputTokenCostAbove200kTokens != nil {
		return *pricing.CacheCreationInputTokenCostAbove200kTokens
	}
	if pricing.CacheCreationInputTokenCost != nil {
		return *pricing.CacheCreationInputTokenCost
	}
	return tieredInputRate(pricing, totalTokens, tier)
}

func tieredCacheCreationInputAbove1hrTokenRate(pricing *configstoreTables.TableModelPricing, totalTokens int, tier serviceTier) float64 {
	// Fast mode (Anthropic) is a flat rate across the full context window.
	if tier.isFast && pricing.CacheCreationInputTokenCostAbove1hrFast != nil {
		return *pricing.CacheCreationInputTokenCostAbove1hrFast
	}
	if totalTokens > TokenTierAbove200K && pricing.CacheCreationInputTokenCostAbove1hrAbove200kTokens != nil {
		return *pricing.CacheCreationInputTokenCostAbove1hrAbove200kTokens
	}
	if pricing.CacheCreationInputTokenCostAbove1hr != nil {
		return *pricing.CacheCreationInputTokenCostAbove1hr
	}
	return tieredCacheCreationInputTokenRate(pricing, totalTokens, tier)
}

func inputTierTokens(usage *schemas.BifrostLLMUsage) int {
	if usage == nil {
		return 0
	}
	return usage.PromptTokens
}

func imageInputTierTokens(usage *schemas.ImageUsage) int {
	if usage == nil {
		return 0
	}
	if usage.InputTokensDetails != nil {
		return usage.InputTokensDetails.TextTokens + usage.InputTokensDetails.ImageTokens
	}
	if usage.InputTokens > 0 {
		return usage.InputTokens
	}

	// Some older/provider-specific image adapters only report TotalTokens.
	// Derive input from total-output when output is known, but do not treat a
	// bare total as input: total includes generated output tokens.
	outputTokens := imageOutputTokens(usage)
	if usage.TotalTokens > outputTokens {
		return usage.TotalTokens - outputTokens
	}
	return 0
}

func imageOutputTokens(usage *schemas.ImageUsage) int {
	if usage == nil {
		return 0
	}
	if usage.OutputTokensDetails != nil {
		return usage.OutputTokensDetails.TextTokens + usage.OutputTokensDetails.ImageTokens
	}
	return usage.OutputTokens
}

// parseImageDimensions parses a size string like "1024x1024" into its width and
// height. Returns 0, 0 if the size string is empty or malformed.
func parseImageDimensions(size string) (int, int) {
	if size == "" {
		return 0, 0
	}
	parts := strings.SplitN(size, "x", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	w, err := strconv.Atoi(parts[0])
	if err != nil || w <= 0 {
		return 0, 0
	}
	h, err := strconv.Atoi(parts[1])
	if err != nil || h <= 0 {
		return 0, 0
	}
	return w, h
}

// parseImagePixels parses a size string like "1024x1024" into total pixel count.
// Returns 0 if the size string is empty or malformed.
func parseImagePixels(size string) int {
	w, h := parseImageDimensions(size)
	return w * h
}

// populateOutputImageCount sets the output image count on ImageUsage from len(Data)
// when OutputTokensDetails.NImages is not already populated.
func populateOutputImageCount(imageUsage *schemas.ImageUsage, dataLen int) {
	if imageUsage == nil || dataLen == 0 {
		return
	}
	if imageUsage.OutputTokensDetails == nil {
		imageUsage.OutputTokensDetails = &schemas.ImageTokenDetails{}
	}
	if imageUsage.OutputTokensDetails.NImages == 0 {
		imageUsage.OutputTokensDetails.NImages = dataLen
	}
}

// ---------------------------------------------------------------------------
// Pricing resolution
// ---------------------------------------------------------------------------

// resolvePricing resolves the pricing entry for a request directly from the
// RoutingInfo populated on the response/error by core.bifrost at request time.
//
// Lookup precedence — ServerSideFallbackModel → AliasModelName → AliasModelID →
// ModelName. Each non-empty candidate is tried against the base catalog in
// order; the first hit wins.
//
//   - ServerSideFallbackModel is the model that produced the response when the
//     provider swapped models inside one call (Anthropic server-side fallback).
//     Ranked first: the tokens being priced are its own. Nil on ordinary responses.
//   - AliasModelName (RoutingInfo.ResolvedKeyAlias.ModelName) is the canonical
//     model name the admin tagged on the matched alias. Catches the
//     opaque-deployment-ID case where the wire model wouldn't hit the catalog
//     on its own.
//   - AliasModelID (RoutingInfo.ResolvedKeyAlias.ModelID) is the wire model
//     when an alias matched. nil/empty otherwise.
//   - ModelName (RoutingInfo.Model) is the model string the caller sent — the
//     alias key when an alias matched, or the raw user input when none did.
//
// Overrides are applied keyed by the wire model (ServerSideFallbackModel when the
// provider handed off mid-call, else AliasModelID when an alias matched, otherwise
// ModelName) so per-deployment override pricing stays addressable in either flow.
// A fallback-served turn keys on the serving model so base rates and overrides
// agree: an override negotiated for the model that actually ran is the one that
// applies.
func (s *Store) resolvePricing(routingInfo schemas.RoutingInfo, requestType schemas.RequestType, scopes LookupScopes) *configstoreTables.TableModelPricing {
	provider := string(routingInfo.Provider)
	catalogProvider := normalizeProvider(provider)
	var aliasModelID, aliasModelName string
	if rka := routingInfo.ResolvedKeyAlias; rka != nil {
		aliasModelID = rka.ModelID
		if rka.ModelName != nil {
			aliasModelName = *rka.ModelName
		}
	}
	var serverSideFallbackModel string
	if routingInfo.ServerSideFallbackModel != nil {
		serverSideFallbackModel = *routingInfo.ServerSideFallbackModel
	}
	overrideKey := serverSideFallbackModel
	if overrideKey == "" {
		overrideKey = aliasModelID
	}
	if overrideKey == "" {
		overrideKey = routingInfo.Model
	}
	s.logger.Debug("looking up pricing for wire model %s and provider %s of request type %s", overrideKey, provider, normalizeRequestType(requestType))

	if scopes.Provider == "" {
		scopes.Provider = provider
	}

	for _, candidate := range []string{serverSideFallbackModel, aliasModelName, aliasModelID, routingInfo.Model} {
		if candidate == "" {
			continue
		}
		base, exists := s.getBasePricing(candidate, catalogProvider, requestType, routingInfo.Region)
		if exists && base != nil {
			result, _ := s.applyPricingOverrides(overrideKey, requestType, *base, scopes)
			return &result
		}
		s.logger.Debug("pricing not found for %s, trying next candidate", candidate)
	}

	// No base catalog entry found; still try overrides in case the user defined
	// override-only pricing for a model not in the built-in catalog.
	s.logger.Debug("pricing not found for any candidate (provider %s), trying override-only pricing keyed by %s", provider, overrideKey)
	result, applied := s.applyPricingOverrides(overrideKey, requestType, configstoreTables.TableModelPricing{}, scopes)
	if applied {
		return &result
	}
	s.logger.Debug("no pricing found for wire model %s and provider %s, skipping cost calculation", overrideKey, provider)
	return nil
}

// getBasePricing looks up catalog pricing for the given model, provider, and request type.
// It applies a provider-specific fallback chain when an exact match is not found:
//
//   - Gemini: retries under the "vertex" provider, then falls back to the counterpart chat/responses mode.
//   - Vertex: strips the "provider/model" prefix and retries, then falls back to the counterpart chat/responses mode.
//   - Bedrock: prepends the vendor namespace ("anthropic.", "openai.", "google.", "xai.") inferred from the model family, then falls back to the counterpart chat/responses mode.
//   - Bedrock Mantle: folded onto the "bedrock" provider up front (datasheet rows for all Bedrock variants are stored there), so it shares every Bedrock fallback.
//   - All providers: chat and responses requests retry in each other's mode, since a model served over both APIs often has a datasheet row under only one of them.
//   - All providers: for ImageEdit/ImageVariation requests, retries the lookup in image-generation mode.
//
// The method acquires a read lock for the duration of the lookup.
//
// Input:  model       — exact model name to look up.
//
//	provider    — provider identifier (e.g. "openai", "anthropic").
//	requestType — the request type used to derive the pricing mode.
//
// Output: TableModelPricing — the matched pricing row (zero value when not found).
//
//	bool              — true when a pricing entry was found, false otherwise.
//	region      — routed regional-processing region ("" for default/US). When
//	              non-empty, each lookup prefers the region-prefixed model row
//	              (e.g. "eu.gpt-5.4") and falls back to the bare model when the
//	              datasheet has no regional entry, so models without a regional
//	              row keep their default pricing. This mirrors the datasheet's
//	              existing region-prefixed naming (e.g. "eu.anthropic.claude-*",
//	              Bedrock "us./eu./apac." inference profiles).
func (s *Store) getBasePricing(model, provider string, requestType schemas.RequestType, region string) (*configstoreTables.TableModelPricing, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	mode := normalizeRequestType(requestType)
	fallbackMode, hasFallbackMode := chatResponsesFallbackMode(requestType)

	// lookup resolves a (model, provider, mode) key against the routed region.
	// When a region is set it first tries the region-prefixed model name
	// ("<region>.<model>") — the datasheet convention for regional-processing
	// rows — then falls back to the bare model. Guarded so an already-prefixed
	// model (a caller that addressed "eu.<model>" directly) isn't double-prefixed.
	// All fallback-chain lookups below go through it so region selection is uniform.
	lookup := func(model, provider, mode string) (configstoreTables.TableModelPricing, bool) {
		if region != "" && !strings.HasPrefix(model, region+".") {
			if p, ok := s.pricingData[makeKey(region+"."+model, provider, mode)]; ok {
				return p, true
			}
		}
		p, ok := s.pricingData[makeKey(model, provider, mode)]
		return p, ok
	}

	// Datasheet rows for all Bedrock variants are stored under the "bedrock"
	// provider (normalizeProvider folds bedrock_* onto "bedrock"), so
	// bedrock_mantle lookups run entirely against "bedrock".
	if provider == string(schemas.BedrockMantle) {
		provider = string(schemas.Bedrock)
	}

	pricing, ok := lookup(model, provider, mode)
	if ok {
		return &pricing, true
	}

	// Lookup in vertex if gemini not found
	if provider == string(schemas.Gemini) {
		s.logger.Debug("primary lookup failed, trying vertex provider for the same model")
		pricing, ok = lookup(model, "vertex", mode)
		if ok {
			return &pricing, true
		}

		// Lookup in the counterpart chat/responses mode if this model's row is filed under the other one
		if hasFallbackMode {
			s.logger.Debug("secondary lookup failed, trying vertex provider for the same model in %s mode", fallbackMode)
			pricing, ok = lookup(model, "vertex", fallbackMode)
			if ok {
				return &pricing, true
			}
		}
	}

	if provider == string(schemas.Vertex) {
		// Vertex models can be of the form "provider/model", so try to lookup the model without the provider prefix and keep the original provider
		if strings.Contains(model, "/") {
			modelWithoutProvider := strings.SplitN(model, "/", 2)[1]
			s.logger.Debug("primary lookup failed, trying vertex provider for the same model with provider/model format %s", modelWithoutProvider)
			pricing, ok = lookup(modelWithoutProvider, "vertex", mode)
			if ok {
				return &pricing, true
			}

			// Lookup in the counterpart chat/responses mode if this model's row is filed under the other one
			if hasFallbackMode {
				s.logger.Debug("secondary lookup failed, trying vertex provider for the same model in %s mode", fallbackMode)
				pricing, ok = lookup(modelWithoutProvider, "vertex", fallbackMode)
				if ok {
					return &pricing, true
				}
			}
		}
	}

	if provider == string(schemas.Bedrock) {
		// Bedrock model IDs carry a vendor namespace ("anthropic.claude-*",
		// "openai.gpt-oss-*", "google.gemma-*", "xai.grok-*"). When the caller
		// sends the bare model name, retry with the namespace inferred from
		// the model family.
		var vendorPrefix string
		switch {
		case !strings.Contains(model, "anthropic.") && schemas.IsAnthropicModel(model):
			vendorPrefix = "anthropic."
		case !strings.Contains(model, "openai.") && schemas.IsOpenAIModel(model):
			vendorPrefix = "openai."
		case !strings.Contains(model, "google.") && (schemas.IsGemmaModel(model) || schemas.IsGeminiModel(model)):
			vendorPrefix = "google."
		case !strings.Contains(model, "xai.") && schemas.IsGrokModel(model):
			vendorPrefix = "xai."
		}
		if vendorPrefix != "" {
			s.logger.Debug("primary lookup failed, trying with %s prefix for the same model", vendorPrefix)
			pricing, ok = lookup(vendorPrefix+model, provider, mode)
			if ok {
				return &pricing, true
			}

			// Lookup in the counterpart chat/responses mode if this model's row is filed under the other one
			if hasFallbackMode {
				s.logger.Debug("secondary lookup failed, trying the same prefixed model in %s mode", fallbackMode)
				pricing, ok = lookup(vendorPrefix+model, provider, fallbackMode)
				if ok {
					return &pricing, true
				}
			}
		}
	}

	// Lookup in the counterpart chat/responses mode if this model's row is filed under the other one
	if hasFallbackMode {
		s.logger.Debug("primary lookup failed, trying the same model in %s mode", fallbackMode)
		pricing, ok = lookup(model, provider, fallbackMode)
		if ok {
			return &pricing, true
		}
	}

	// Lookup in image generation if image edit not found
	if requestType == schemas.ImageEditRequest ||
		requestType == schemas.ImageEditStreamRequest ||
		requestType == schemas.ImageVariationRequest {
		s.logger.Debug("primary lookup failed, trying image generation provider for the same model")
		pricing, ok = lookup(model, provider, normalizeRequestType(schemas.ImageGenerationRequest))
		if ok {
			return &pricing, true
		}
	}

	// Lookup fallback chain for container_create:
	// 1. Try chat mode for the same model (e.g. "container-1g" in chat mode)
	// 2. Try the base "container" model in chat mode (default rate when no memory-specific entry exists)
	if requestType == schemas.ContainerCreateRequest {
		s.logger.Debug("primary lookup failed, trying chat mode for container create pricing")
		pricing, ok = lookup(model, provider, normalizeRequestType(schemas.ChatCompletionRequest))
		if ok {
			return &pricing, true
		}
		if model != "container" {
			s.logger.Debug("memory-specific container pricing not found, falling back to base container entry")
			pricing, ok = lookup("container", provider, normalizeRequestType(schemas.ChatCompletionRequest))
			if ok {
				return &pricing, true
			}
		}
	}

	return nil, false
}

// UpsertModelPricingAttributes writes the additional_attributes column for
// every pricing row that matches (model, provider), then reloads the pricing
// cache so the new values are immediately visible to list-models. Returns
// the number of rows updated (0 = no such pricing row, which callers must
// surface as a validation error). An empty/nil attrs map clears the column.
func (s *Store) UpsertModelPricingAttributes(ctx context.Context, model string, provider schemas.ModelProvider, attrs map[string]string) (int64, error) {
	if s.configStore == nil {
		return 0, fmt.Errorf("model catalog requires a config store")
	}
	rows, err := s.configStore.UpsertModelPricingAttributes(ctx, model, string(provider), attrs)
	if err != nil {
		return 0, err
	}
	if rows == 0 {
		return 0, nil
	}
	if err := s.LoadFromDB(ctx); err != nil {
		return rows, fmt.Errorf("failed to reload pricing cache after attribute write: %w", err)
	}
	return rows, nil
}

// ---------------------------------------------------------------------------
// Passthrough pricing helpers
// ---------------------------------------------------------------------------

// detectPassthroughRequestType maps a provider + stripped path to a RequestType.
func detectPassthroughRequestType(provider schemas.ModelProvider, path string) schemas.RequestType {
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}
	path = strings.TrimRight(path, "/")
	switch provider {
	case schemas.OpenAI, schemas.Azure:
		switch {
		case strings.HasSuffix(path, "/chat/completions"):
			return schemas.ChatCompletionRequest
		case strings.HasSuffix(path, "/completions"):
			return schemas.TextCompletionRequest
		case strings.HasSuffix(path, "/embeddings"):
			return schemas.EmbeddingRequest
		case strings.HasSuffix(path, "/responses/compact"):
			return schemas.CompactionRequest
		case strings.HasSuffix(path, "/responses"):
			return schemas.ResponsesRequest
		case strings.HasSuffix(path, "/images/generations"):
			return schemas.ImageGenerationRequest
		case strings.HasSuffix(path, "/images/edits"):
			return schemas.ImageEditRequest
		case strings.HasSuffix(path, "/images/variations"):
			return schemas.ImageVariationRequest
		case strings.HasSuffix(path, "/audio/speech"):
			return schemas.SpeechRequest
		case strings.HasSuffix(path, "/audio/transcriptions"),
			strings.HasSuffix(path, "/audio/translations"):
			return schemas.TranscriptionRequest
		case strings.HasSuffix(path, "/containers"):
			return schemas.ContainerCreateRequest
		case strings.Contains(path, "/video"):
			return schemas.VideoGenerationRequest
		default:
			return schemas.ChatCompletionRequest
		}
	case schemas.Gemini, schemas.Vertex:
		// Interactions API paths carry no colon action suffix.
		if strings.Contains(path, "/interactions") {
			return schemas.ResponsesRequest
		}
		colonIdx := strings.LastIndexByte(path, ':')
		if colonIdx < 0 {
			return schemas.ChatCompletionRequest
		}
		switch path[colonIdx+1:] {
		case "generateContent", "streamGenerateContent":
			return schemas.ResponsesRequest
		case "embedContent", "batchEmbedContents":
			return schemas.EmbeddingRequest
		case "generateImages":
			return schemas.ImageGenerationRequest
		case "predict":
			return schemas.EmbeddingRequest
		case "predictLongRunning":
			return schemas.VideoGenerationRequest
		default:
			return schemas.ChatCompletionRequest
		}
	case schemas.Anthropic:
		switch {
		case strings.HasSuffix(path, "/messages"):
			return schemas.ResponsesRequest
		case strings.HasSuffix(path, "/complete"):
			return schemas.TextCompletionRequest
		default:
			return schemas.ResponsesRequest
		}
	default:
		return schemas.ChatCompletionRequest
	}
}

// inferPassthroughRequestType determines the request type from usage fields (primary)
// and falls back to path detection for text/embedding/responses where LLMUsage is ambiguous.
func inferPassthroughRequestType(provider schemas.ModelProvider, path string, su *schemas.BifrostPassthroughUsage) schemas.RequestType {
	if su != nil {
		if su.ContainerIdentifier != "" {
			return schemas.ContainerCreateRequest
		}
		if su.ImageUsage != nil {
			return schemas.ImageGenerationRequest
		}
		if su.AudioInputChars > 0 {
			return schemas.SpeechRequest
		}
		if su.AudioTokenDetails != nil || su.AudioSeconds != nil {
			return schemas.TranscriptionRequest
		}
		if su.VideoSeconds != nil {
			return schemas.VideoGenerationRequest
		}
	}
	return detectPassthroughRequestType(provider, path)
}

// passthroughUsageToCostInput converts BifrostPassthroughUsage into costInput.
func passthroughUsageToCostInput(su *schemas.BifrostPassthroughUsage) costInput {
	var input costInput
	if su.LLMUsage != nil {
		input.usage = su.LLMUsage
	}
	input.tier = tierFromResponse(su.ServiceTier, su.Speed, su.InferenceGeo)
	if su.ImageUsage != nil {
		input.imageUsage = su.ImageUsage
		input.imageSize = su.ImageSize
		input.imageQuality = su.ImageQuality
	}
	if su.AudioInputChars > 0 {
		input.audioTextInputChars = su.AudioInputChars
	}
	if su.AudioSeconds != nil {
		input.audioSeconds = su.AudioSeconds
	}
	if su.AudioTokenDetails != nil {
		input.audioTokenDetails = su.AudioTokenDetails
	}
	if su.VideoSeconds != nil {
		input.videoSeconds = su.VideoSeconds
	}
	if su.ContainerIdentifier != "" {
		input.containerIdentifierString = su.ContainerIdentifier
	}
	// Provider-reported exact cost wins over datasheet estimation; attach it to usage.Cost so the
	// provider-cost short-circuit in computeCost returns it verbatim. input.usage may alias the
	// caller's su.LLMUsage, so copy before assigning Cost to preserve the pure-read invariant (see
	// the DeepCopy in the ImageGenerationResponse branch above).
	if su.Cost != nil {
		if input.usage == nil {
			input.usage = &schemas.BifrostLLMUsage{Cost: su.Cost}
		} else {
			usageCopy := *input.usage
			usageCopy.Cost = su.Cost
			input.usage = &usageCopy
		}
	}
	return input
}
