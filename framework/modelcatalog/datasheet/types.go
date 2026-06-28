// Package pricing owns the pricing/model-parameters catalog (compartments A + B + E):
// canonical pricing rows fetched from the upstream datasheet, per-provider
// datasheet-derived model views, supported request types and parameters,
// and scoped pricing overrides. It also computes per-response cost.
//
// The package performs no list-models I/O — that's the live store's domain.
// The hourly sync ticker lives on the composer (ModelCatalog), not here; the
// composer calls SyncFromURL / LoadFromDB / Sync*ModelParams*. Reads are
// hot-path and lock-free where possible.
package datasheet

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// Tier boundaries for tiered token pricing. Matches the upstream datasheet
// keys (input_cost_per_token_above_<N>k_tokens).
const (
	TokenTierAbove272K = 272000
	TokenTierAbove200K = 200000
	TokenTierAbove128K = 128000
)

// retryBackoffMin is the initial wait before the first retry; subsequent
// retries scale exponentially up to maxBackoff.
const retryBackoffMin = time.Second

// Entry represents a single model's pricing information. Field names and
// JSON tags match the datasheet schema exactly. AdditionalAttributes carries
// editorial metadata stored on the pricing row — never populated from the
// URL datasheet, only from DB reads via the management API.
type Entry struct {
	BaseModel string `json:"base_model,omitempty"`
	Provider  string `json:"provider"`
	Mode      string `json:"mode"`

	ContextLength   *int                  `json:"context_length,omitempty"`
	MaxInputTokens  *int                  `json:"max_input_tokens,omitempty"`
	MaxOutputTokens *int                  `json:"max_output_tokens,omitempty"`
	Architecture    *schemas.Architecture `json:"architecture,omitempty"`

	// AdditionalAttributes carries editorial metadata stored on the pricing
	// row (e.g. description). Populated from the DB read path only; the
	// json:"-" tag prevents URL datasheet payloads from ever feeding into
	// this field via json.Unmarshal.
	AdditionalAttributes map[string]string `json:"-"`

	Options
}

// UnmarshalJSON handles the special case where search_context_cost_per_query
// may arrive as either a plain float64 or a tiered object
// {"search_context_size_low":…, "search_context_size_medium":…, "search_context_size_high":…}.
func (p *Entry) UnmarshalJSON(data []byte) error {
	type entryAlias Entry
	var raw struct {
		entryAlias
		SearchContextCostPerQuery *struct {
			Low    *float64 `json:"search_context_size_low"`
			Medium *float64 `json:"search_context_size_medium"`
			High   *float64 `json:"search_context_size_high"`
		} `json:"search_context_cost_per_query,omitempty"`
	}
	if err := sonic.Unmarshal(data, &raw); err != nil {
		return err
	}
	*p = Entry(raw.entryAlias)

	// search_context_cost_per_query arrives as a tiered object — all three values are
	// equal for non-Perplexity providers; we prefer medium, then low, then high.
	// Perplexity always returns a pre-computed total_cost so the per-query rate is
	// never consumed for that provider.
	if q := raw.SearchContextCostPerQuery; q != nil {
		switch {
		case q.Medium != nil:
			p.SearchContextCostPerQuery = q.Medium
		case q.Low != nil:
			p.SearchContextCostPerQuery = q.Low
		case q.High != nil:
			p.SearchContextCostPerQuery = q.High
		}
	}
	return nil
}

// Options holds every individual cost field. Embedded into Entry and reused
// as the patch shape for Override.
type Options struct {
	// Costs - Text
	InputCostPerToken          *float64 `json:"input_cost_per_token,omitempty"`
	OutputCostPerToken         *float64 `json:"output_cost_per_token,omitempty"`
	InputCostPerTokenBatches   *float64 `json:"input_cost_per_token_batches,omitempty"`
	OutputCostPerTokenBatches  *float64 `json:"output_cost_per_token_batches,omitempty"`
	InputCostPerTokenPriority  *float64 `json:"input_cost_per_token_priority,omitempty"`
	OutputCostPerTokenPriority *float64 `json:"output_cost_per_token_priority,omitempty"`
	InputCostPerTokenFlex      *float64 `json:"input_cost_per_token_flex,omitempty"`
	OutputCostPerTokenFlex     *float64 `json:"output_cost_per_token_flex,omitempty"`
	// Fast mode (Anthropic research preview, speed:"fast" on Opus 4.6/4.7/4.8).
	// Flat rate across the full context window — no 128k/200k/272k tiering.
	InputCostPerTokenFast  *float64 `json:"input_cost_per_token_fast,omitempty"`
	OutputCostPerTokenFast *float64 `json:"output_cost_per_token_fast,omitempty"`
	InputCostPerCharacter  *float64 `json:"input_cost_per_character,omitempty"`
	// Costs - 128k Tier
	InputCostPerTokenAbove128kTokens          *float64 `json:"input_cost_per_token_above_128k_tokens,omitempty"`
	InputCostPerImageAbove128kTokens          *float64 `json:"input_cost_per_image_above_128k_tokens,omitempty"`
	InputCostPerVideoPerSecondAbove128kTokens *float64 `json:"input_cost_per_video_per_second_above_128k_tokens,omitempty"`
	InputCostPerAudioPerSecondAbove128kTokens *float64 `json:"input_cost_per_audio_per_second_above_128k_tokens,omitempty"`
	OutputCostPerTokenAbove128kTokens         *float64 `json:"output_cost_per_token_above_128k_tokens,omitempty"`
	// Costs - 200k Tier
	InputCostPerTokenAbove200kTokens          *float64 `json:"input_cost_per_token_above_200k_tokens,omitempty"`
	InputCostPerTokenAbove200kTokensPriority  *float64 `json:"input_cost_per_token_above_200k_tokens_priority,omitempty"`
	OutputCostPerTokenAbove200kTokens         *float64 `json:"output_cost_per_token_above_200k_tokens,omitempty"`
	OutputCostPerTokenAbove200kTokensPriority *float64 `json:"output_cost_per_token_above_200k_tokens_priority,omitempty"`
	// Costs - 272k Tier
	InputCostPerTokenAbove272kTokens          *float64 `json:"input_cost_per_token_above_272k_tokens,omitempty"`
	InputCostPerTokenAbove272kTokensPriority  *float64 `json:"input_cost_per_token_above_272k_tokens_priority,omitempty"`
	OutputCostPerTokenAbove272kTokens         *float64 `json:"output_cost_per_token_above_272k_tokens,omitempty"`
	OutputCostPerTokenAbove272kTokensPriority *float64 `json:"output_cost_per_token_above_272k_tokens_priority,omitempty"`

	// Costs - Cache
	CacheCreationInputTokenCost                        *float64 `json:"cache_creation_input_token_cost,omitempty"`
	CacheReadInputTokenCost                            *float64 `json:"cache_read_input_token_cost,omitempty"`
	CacheCreationInputTokenCostAbove200kTokens         *float64 `json:"cache_creation_input_token_cost_above_200k_tokens,omitempty"`
	CacheReadInputTokenCostAbove200kTokens             *float64 `json:"cache_read_input_token_cost_above_200k_tokens,omitempty"`
	CacheReadInputTokenCostAbove200kTokensPriority     *float64 `json:"cache_read_input_token_cost_above_200k_tokens_priority,omitempty"`
	CacheCreationInputTokenCostAbove1hr                *float64 `json:"cache_creation_input_token_cost_above_1hr,omitempty"`
	CacheCreationInputTokenCostAbove1hrAbove200kTokens *float64 `json:"cache_creation_input_token_cost_above_1hr_above_200k_tokens,omitempty"`
	CacheCreationInputAudioTokenCost                   *float64 `json:"cache_creation_input_audio_token_cost,omitempty"`
	CacheReadInputTokenCostPriority                    *float64 `json:"cache_read_input_token_cost_priority,omitempty"`
	CacheReadInputTokenCostFlex                        *float64 `json:"cache_read_input_token_cost_flex,omitempty"`
	CacheReadInputImageTokenCost                       *float64 `json:"cache_read_input_image_token_cost,omitempty"`
	CacheReadInputTokenCostAbove272kTokens             *float64 `json:"cache_read_input_token_cost_above_272k_tokens,omitempty"`
	CacheReadInputTokenCostAbove272kTokensPriority     *float64 `json:"cache_read_input_token_cost_above_272k_tokens_priority,omitempty"`

	// Costs - Image
	InputCostPerImage                             *float64 `json:"input_cost_per_image,omitempty"`
	InputCostPerPixel                             *float64 `json:"input_cost_per_pixel,omitempty"`
	OutputCostPerImage                            *float64 `json:"output_cost_per_image,omitempty"`
	OutputCostPerPixel                            *float64 `json:"output_cost_per_pixel,omitempty"`
	OutputCostPerImagePremiumImage                *float64 `json:"output_cost_per_image_premium_image,omitempty"`
	OutputCostPerImageAbove512x512Pixels          *float64 `json:"output_cost_per_image_above_512_and_512_pixels,omitempty"`
	OutputCostPerImageAbove512x512PixelsPremium   *float64 `json:"output_cost_per_image_above_512_and_512_pixels_and_premium_image,omitempty"`
	OutputCostPerImageAbove1024x1024Pixels        *float64 `json:"output_cost_per_image_above_1024_and_1024_pixels,omitempty"`
	OutputCostPerImageAbove1024x1024PixelsPremium *float64 `json:"output_cost_per_image_above_1024_and_1024_pixels_and_premium_image,omitempty"`
	OutputCostPerImageAbove2048x2048Pixels        *float64 `json:"output_cost_per_image_above_2048_and_2048_pixels,omitempty"`
	OutputCostPerImageAbove4096x4096Pixels        *float64 `json:"output_cost_per_image_above_4096_and_4096_pixels,omitempty"`
	OutputCostPerImageLowQuality                  *float64 `json:"output_cost_per_image_low_quality,omitempty"`
	OutputCostPerImageMediumQuality               *float64 `json:"output_cost_per_image_medium_quality,omitempty"`
	OutputCostPerImageHighQuality                 *float64 `json:"output_cost_per_image_high_quality,omitempty"`
	OutputCostPerImageAutoQuality                 *float64 `json:"output_cost_per_image_auto_quality,omitempty"`
	InputCostPerImageToken                        *float64 `json:"input_cost_per_image_token,omitempty"`
	OutputCostPerImageToken                       *float64 `json:"output_cost_per_image_token,omitempty"`

	// Costs - Audio/Video
	InputCostPerAudioToken      *float64 `json:"input_cost_per_audio_token,omitempty"`
	InputCostPerAudioPerSecond  *float64 `json:"input_cost_per_audio_per_second,omitempty"`
	InputCostPerSecond          *float64 `json:"input_cost_per_second,omitempty"`
	InputCostPerVideoPerSecond  *float64 `json:"input_cost_per_video_per_second,omitempty"`
	OutputCostPerAudioToken     *float64 `json:"output_cost_per_audio_token,omitempty"`
	OutputCostPerVideoPerSecond *float64 `json:"output_cost_per_video_per_second,omitempty"`
	OutputCostPerSecond         *float64 `json:"output_cost_per_second,omitempty"`

	// Costs - Other.
	//
	// SearchContextCostPerQuery is stored as a single float64, but the upstream datasheet
	// represents it as a tiered object. See Entry.UnmarshalJSON.
	SearchContextCostPerQuery     *float64 `json:"search_context_cost_per_query,omitempty"`
	CodeInterpreterCostPerSession *float64 `json:"code_interpreter_cost_per_session,omitempty"`

	// Costs - OCR
	OCRCostPerPage        *float64 `json:"ocr_cost_per_page,omitempty"`
	AnnotationCostPerPage *float64 `json:"annotation_cost_per_page,omitempty"`
}

// LookupScopes carries the runtime identifiers used to resolve scoped pricing
// overrides during cost calculation.
type LookupScopes struct {
	VirtualKeyID  string
	SelectedKeyID string
	Provider      string
}

// LookupScopesFromContext builds a LookupScopes from a BifrostContext. Reads
// the governance virtual key ID (not the raw VK token) and the selected key
// ID. provider should be the provider name string (e.g. "openai"); pass "" if
// unavailable. Returns nil only when ctx is nil. An empty scopes value is
// still returned when all fields are empty so global-scope overrides remain
// evaluable.
//
// NOT SAFE in a goroutine — reads from ctx which is cancelled when the
// request ends. Call synchronously in PostHooks and pass the result by value
// to anything that may outlive the request.
func LookupScopesFromContext(ctx *schemas.BifrostContext, provider string) *LookupScopes {
	if ctx == nil {
		return nil
	}
	virtualKeyID, _ := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID).(string)
	selectedKeyID, _ := ctx.Value(schemas.BifrostContextKeySelectedKeyID).(string)
	return &LookupScopes{
		VirtualKeyID:  virtualKeyID,
		SelectedKeyID: selectedKeyID,
		Provider:      provider,
	}
}

// ScopeKind identifies which governance scope an override applies to.
type ScopeKind string

const (
	ScopeKindGlobal                ScopeKind = "global"
	ScopeKindProvider              ScopeKind = "provider"
	ScopeKindProviderKey           ScopeKind = "provider_key"
	ScopeKindVirtualKey            ScopeKind = "virtual_key"
	ScopeKindVirtualKeyProvider    ScopeKind = "virtual_key_provider"
	ScopeKindVirtualKeyProviderKey ScopeKind = "virtual_key_provider_key"
)

// MatchType controls how an override pattern is matched against model names.
type MatchType string

const (
	MatchTypeExact    MatchType = "exact"
	MatchTypeWildcard MatchType = "wildcard"
)

// Override describes a scoped pricing override shared across config storage,
// model catalog compilation, and governance APIs.
type Override struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	ScopeKind     ScopeKind             `json:"scope_kind"`
	VirtualKeyID  *string               `json:"virtual_key_id,omitempty"`
	ProviderID    *string               `json:"provider_id,omitempty"`
	ProviderKeyID *string               `json:"provider_key_id,omitempty"`
	MatchType     MatchType             `json:"match_type"`
	Pattern       string                `json:"pattern"`
	RequestTypes  []schemas.RequestType `json:"request_types,omitempty"`
	Options       Options               `json:"options"`
}

// serviceTier captures the OpenAI service_tier value from a response.
// Add new tier flags here as OpenAI introduces them.
type serviceTier struct {
	isPriority bool // true when service_tier == "priority"
	isFlex     bool // true when service_tier == "flex"
	isFast     bool // true when usage.speed == "fast" (Anthropic fast mode)
}

// costInput holds the extracted usage data from a BifrostResponse,
// normalized for the pricing engine.
type costInput struct {
	usage               *schemas.BifrostLLMUsage
	audioTextInputChars int
	audioSeconds        *int
	audioTokenDetails   *schemas.TranscriptionUsageInputTokenDetails
	imageUsage          *schemas.ImageUsage
	imageSize           string // e.g. "1024x1024", used for per-pixel pricing
	imageQuality        string // "low", "medium", "high", "auto" (gpt-image-1.5); empty = use base rate
	videoSeconds        *int
	ocrProcessedPages   *int
	ocrIsAnnotated      *bool
	// containerIdentifierString, when non-empty, replaces the actual requested/resolved
	// model names during pricing lookup. Used for request types whose cost is not
	// tied to a specific model. Currently only used for container creates.
	containerIdentifierString string
	tier                      serviceTier
}

// customPricingEntry is one flattened override ready for lookup.
type customPricingEntry struct {
	id            string
	scopeKind     ScopeKind
	virtualKeyID  string
	providerID    string
	providerKeyID string
	pattern       string // exact model name, or wildcard prefix (trailing * stripped)
	wildcard      bool
	requestModes  map[string]struct{} // always non-nil for valid overrides
	options       Options
}

// customPricingData is the in-memory lookup structure for pricing overrides.
// Exact matches are indexed by model name; wildcards are a flat slice.
type customPricingData struct {
	exact    map[string][]customPricingEntry
	wildcard []customPricingEntry
}

// modelParametersParseResult is the parsed result type used by
// buildSupportedOutputsIndex (consumed by params.go's applyModelParameters).
type modelParametersParseResult struct {
	Mode               *string  `json:"mode,omitempty"`
	SupportedEndpoints []string `json:"supported_endpoints,omitempty"`
	ModelParameters    []struct {
		ID string `json:"id"`
	} `json:"model_parameters,omitempty"`
	SupportsAssistantPrefill        *bool `json:"supports_assistant_prefill,omitempty"`
	SupportsFunctionCalling         *bool `json:"supports_function_calling,omitempty"`
	SupportsParallelFunctionCalling *bool `json:"supports_parallel_function_calling,omitempty"`
	SupportsToolChoice              *bool `json:"supports_tool_choice,omitempty"`
	SupportsReasoning               *bool `json:"supports_reasoning,omitempty"`
	SupportsResponseSchema          *bool `json:"supports_response_schema,omitempty"`
	SupportsServiceTier             *bool `json:"supports_service_tier,omitempty"`
	SupportsPromptCaching           *bool `json:"supports_prompt_caching,omitempty"`
	SupportsWebSearch               *bool `json:"supports_web_search,omitempty"`
	VertexMultiRegionOnly           *bool `json:"vertex_multi_region_only,omitempty"`
}

// --- private helpers (shared across pricing/*.go files) ---

// makeKey is the composite map key used by pricingData: model|provider|mode.
func makeKey(model, provider, mode string) string {
	return model + "|" + provider + "|" + mode
}

// normalizeProvider folds upstream-datasheet provider name variants
// (vertex_ai, google-vertex, etc.) onto bifrost's canonical provider names.
func normalizeProvider(p string) string {
	switch {
	case strings.Contains(p, "vertex_ai") || p == "google-vertex":
		return string(schemas.Vertex)
	case strings.Contains(p, "bedrock"):
		return string(schemas.Bedrock)
	case strings.Contains(p, "cohere"):
		return string(schemas.Cohere)
	case strings.Contains(p, "runwayml"):
		return string(schemas.Runway)
	case strings.Contains(p, "fireworks_ai"):
		return string(schemas.Fireworks)
	default:
		return p
	}
}

// normalizeRequestType collapses streaming and non-streaming variants of a
// request type to a single pricing mode string.
func normalizeRequestType(reqType schemas.RequestType) string {
	switch reqType {
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		return "completion"
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		return "chat"
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest, schemas.WebSocketResponsesRequest, schemas.RealtimeRequest, schemas.CompactionRequest:
		return "responses"
	case schemas.EmbeddingRequest:
		return "embedding"
	case schemas.RerankRequest:
		return "rerank"
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		return "audio_speech"
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		return "audio_transcription"
	case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest, schemas.ImageVariationRequest:
		return "image_generation"
	case schemas.ImageEditRequest, schemas.ImageEditStreamRequest:
		return "image_edit"
	case schemas.VideoGenerationRequest, schemas.VideoRemixRequest:
		return "video_generation"
	case schemas.OCRRequest:
		return "ocr"
	case schemas.ContainerCreateRequest:
		return "container_create"
	}
	return "unknown"
}

// normalizeStreamRequestType maps a stream variant to its non-stream base type.
// Idempotent — passing a non-stream type returns it unchanged.
func normalizeStreamRequestType(rt schemas.RequestType) schemas.RequestType {
	switch rt {
	case schemas.TextCompletionStreamRequest:
		return schemas.TextCompletionRequest
	case schemas.ChatCompletionStreamRequest:
		return schemas.ChatCompletionRequest
	case schemas.ResponsesStreamRequest, schemas.WebSocketResponsesRequest:
		return schemas.ResponsesRequest
	case schemas.RealtimeRequest:
		return schemas.RealtimeRequest
	case schemas.SpeechStreamRequest:
		return schemas.SpeechRequest
	case schemas.TranscriptionStreamRequest:
		return schemas.TranscriptionRequest
	case schemas.ImageGenerationStreamRequest:
		return schemas.ImageGenerationRequest
	case schemas.ImageEditStreamRequest:
		return schemas.ImageEditRequest
	default:
		return rt
	}
}

// extractModelName strips a leading "provider/" prefix from a model key.
func extractModelName(modelKey string) string {
	if idx := strings.Index(modelKey, "/"); idx >= 0 {
		return modelKey[idx+1:]
	}
	return modelKey
}

// normalizeEndpointToOutputType converts a supported_endpoints URL path to a
// normalized output type. Empty string for unrecognized endpoints.
func normalizeEndpointToOutputType(endpoint string) string {
	switch {
	case strings.Contains(endpoint, "/chat/completions"):
		return "chat_completion"
	case strings.Contains(endpoint, "/responses"):
		return "responses"
	case strings.Contains(endpoint, "/completions"):
		return "text_completion"
	default:
		return ""
	}
}

// normalizeModeToOutputType converts mode to a normalized output type.
func normalizeModeToOutputType(mode string) string {
	switch mode {
	case "chat":
		return "chat_completion"
	case "completion":
		return "text_completion"
	case "responses":
		return "responses"
	default:
		return ""
	}
}

// extractSupportedParams builds a list of supported OpenAI-compatible parameter
// names from model_parameters[].id values and supports_* boolean flags.
func extractSupportedParams(parsed *modelParametersParseResult) []string {
	var supported []string
	addParam := func(name string) {
		if !slices.Contains(supported, name) {
			supported = append(supported, name)
		}
	}

	for _, mp := range parsed.ModelParameters {
		switch mp.ID {
		case "reasoning_effort", "reasoning_summary":
			addParam("reasoning")
		case "web_search":
			addParam("web_search_options") // chat-path param
			addParam("web_search")         // responses-path server tool
		case "stop_sequences":
			addParam("stop")
		case "promptTools", "image_detail", "stream":
			// skip — not top-level request parameters
		default:
			addParam(mp.ID)
		}
	}

	if parsed.SupportsAssistantPrefill != nil && *parsed.SupportsAssistantPrefill {
		// Not an actual request parameter; if present, trailing assistant messages
		// for anthropic and bedrock's anthropic models will not be trimmed.
		addParam("assistant_prefill")
	}
	if parsed.SupportsFunctionCalling != nil && *parsed.SupportsFunctionCalling {
		addParam("tools")
	}
	if parsed.SupportsParallelFunctionCalling != nil && *parsed.SupportsParallelFunctionCalling {
		addParam("parallel_tool_calls")
	}
	if parsed.SupportsToolChoice != nil && *parsed.SupportsToolChoice {
		addParam("tool_choice")
	}
	if parsed.SupportsReasoning != nil && *parsed.SupportsReasoning {
		addParam("reasoning")
	}
	if parsed.SupportsResponseSchema != nil && *parsed.SupportsResponseSchema {
		addParam("response_format")
		addParam("text")
	}
	if parsed.SupportsServiceTier != nil && *parsed.SupportsServiceTier {
		addParam("service_tier")
	}
	if parsed.SupportsPromptCaching != nil && *parsed.SupportsPromptCaching {
		addParam("cachePoint")
		addParam("cache_control")
		addParam("prompt_cache_key")
		addParam("prompt_cache_retention")
	}
	if parsed.SupportsWebSearch != nil && *parsed.SupportsWebSearch {
		addParam("web_search")
		addParam("web_search_options")
	}

	return supported
}

// withRetries runs op until it succeeds or maxRetries retries are exhausted
// (1 initial attempt + maxRetries retries). After each failure it waits with
// exponential backoff starting at 1 second (retryBackoffMin), capped at
// maxBackoff when > 0. If maxBackoff is zero, the delay grows unbounded.
func withRetries[T any](ctx context.Context, maxRetries int, maxBackoff time.Duration, op func() (T, error)) (T, error) {
	var zero T
	if maxRetries < 0 {
		maxRetries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		default:
		}

		if attempt > 0 {
			backoff := retryBackoffMin * time.Duration(1<<uint(attempt-1))
			if maxBackoff > 0 && backoff > maxBackoff {
				backoff = maxBackoff
			}
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-time.After(backoff):
			}
		}
		v, err := op()
		if err == nil {
			return v, nil
		}
		lastErr = err
	}
	return zero, lastErr
}

// convertEntryToTablePricing converts a parsed Entry from the upstream
// datasheet into the row shape persisted in the config store.
func convertEntryToTablePricing(modelKey string, entry Entry) configstoreTables.TableModelPricing {
	provider := normalizeProvider(entry.Provider)
	modelName := extractModelName(modelKey)
	return configstoreTables.TableModelPricing{
		Model:           modelName,
		BaseModel:       entry.BaseModel,
		Provider:        provider,
		Mode:            entry.Mode,
		ContextLength:   entry.ContextLength,
		MaxInputTokens:  entry.MaxInputTokens,
		MaxOutputTokens: entry.MaxOutputTokens,
		Architecture:    entry.Architecture,

		InputCostPerToken:                         entry.InputCostPerToken,
		OutputCostPerToken:                        entry.OutputCostPerToken,
		InputCostPerTokenBatches:                  entry.InputCostPerTokenBatches,
		OutputCostPerTokenBatches:                 entry.OutputCostPerTokenBatches,
		InputCostPerTokenPriority:                 entry.InputCostPerTokenPriority,
		OutputCostPerTokenPriority:                entry.OutputCostPerTokenPriority,
		InputCostPerTokenFlex:                     entry.InputCostPerTokenFlex,
		OutputCostPerTokenFlex:                    entry.OutputCostPerTokenFlex,
		InputCostPerTokenFast:                     entry.InputCostPerTokenFast,
		OutputCostPerTokenFast:                    entry.OutputCostPerTokenFast,
		InputCostPerTokenAbove200kTokens:          entry.InputCostPerTokenAbove200kTokens,
		InputCostPerTokenAbove200kTokensPriority:  entry.InputCostPerTokenAbove200kTokensPriority,
		OutputCostPerTokenAbove200kTokens:         entry.OutputCostPerTokenAbove200kTokens,
		OutputCostPerTokenAbove200kTokensPriority: entry.OutputCostPerTokenAbove200kTokensPriority,
		InputCostPerTokenAbove272kTokens:          entry.InputCostPerTokenAbove272kTokens,
		InputCostPerTokenAbove272kTokensPriority:  entry.InputCostPerTokenAbove272kTokensPriority,
		OutputCostPerTokenAbove272kTokens:         entry.OutputCostPerTokenAbove272kTokens,
		OutputCostPerTokenAbove272kTokensPriority: entry.OutputCostPerTokenAbove272kTokensPriority,
		InputCostPerCharacter:                     entry.InputCostPerCharacter,
		InputCostPerTokenAbove128kTokens:          entry.InputCostPerTokenAbove128kTokens,
		InputCostPerImageAbove128kTokens:          entry.InputCostPerImageAbove128kTokens,
		InputCostPerVideoPerSecondAbove128kTokens: entry.InputCostPerVideoPerSecondAbove128kTokens,
		InputCostPerAudioPerSecondAbove128kTokens: entry.InputCostPerAudioPerSecondAbove128kTokens,
		OutputCostPerTokenAbove128kTokens:         entry.OutputCostPerTokenAbove128kTokens,

		CacheCreationInputTokenCost:                        entry.CacheCreationInputTokenCost,
		CacheReadInputTokenCost:                            entry.CacheReadInputTokenCost,
		CacheCreationInputTokenCostAbove200kTokens:         entry.CacheCreationInputTokenCostAbove200kTokens,
		CacheReadInputTokenCostAbove200kTokens:             entry.CacheReadInputTokenCostAbove200kTokens,
		CacheReadInputTokenCostAbove200kTokensPriority:     entry.CacheReadInputTokenCostAbove200kTokensPriority,
		CacheCreationInputTokenCostAbove1hr:                entry.CacheCreationInputTokenCostAbove1hr,
		CacheCreationInputTokenCostAbove1hrAbove200kTokens: entry.CacheCreationInputTokenCostAbove1hrAbove200kTokens,
		CacheCreationInputAudioTokenCost:                   entry.CacheCreationInputAudioTokenCost,
		CacheReadInputTokenCostPriority:                    entry.CacheReadInputTokenCostPriority,
		CacheReadInputTokenCostFlex:                        entry.CacheReadInputTokenCostFlex,
		CacheReadInputImageTokenCost:                       entry.CacheReadInputImageTokenCost,
		CacheReadInputTokenCostAbove272kTokens:             entry.CacheReadInputTokenCostAbove272kTokens,
		CacheReadInputTokenCostAbove272kTokensPriority:     entry.CacheReadInputTokenCostAbove272kTokensPriority,

		InputCostPerImage:                             entry.InputCostPerImage,
		InputCostPerPixel:                             entry.InputCostPerPixel,
		OutputCostPerImage:                            entry.OutputCostPerImage,
		OutputCostPerPixel:                            entry.OutputCostPerPixel,
		OutputCostPerImagePremiumImage:                entry.OutputCostPerImagePremiumImage,
		OutputCostPerImageAbove512x512Pixels:          entry.OutputCostPerImageAbove512x512Pixels,
		OutputCostPerImageAbove512x512PixelsPremium:   entry.OutputCostPerImageAbove512x512PixelsPremium,
		OutputCostPerImageAbove1024x1024Pixels:        entry.OutputCostPerImageAbove1024x1024Pixels,
		OutputCostPerImageAbove1024x1024PixelsPremium: entry.OutputCostPerImageAbove1024x1024PixelsPremium,
		OutputCostPerImageAbove2048x2048Pixels:        entry.OutputCostPerImageAbove2048x2048Pixels,
		OutputCostPerImageAbove4096x4096Pixels:        entry.OutputCostPerImageAbove4096x4096Pixels,
		OutputCostPerImageLowQuality:                  entry.OutputCostPerImageLowQuality,
		OutputCostPerImageMediumQuality:               entry.OutputCostPerImageMediumQuality,
		OutputCostPerImageHighQuality:                 entry.OutputCostPerImageHighQuality,
		OutputCostPerImageAutoQuality:                 entry.OutputCostPerImageAutoQuality,
		InputCostPerImageToken:                        entry.InputCostPerImageToken,
		OutputCostPerImageToken:                       entry.OutputCostPerImageToken,

		InputCostPerAudioToken:      entry.InputCostPerAudioToken,
		InputCostPerAudioPerSecond:  entry.InputCostPerAudioPerSecond,
		InputCostPerSecond:          entry.InputCostPerSecond,
		InputCostPerVideoPerSecond:  entry.InputCostPerVideoPerSecond,
		OutputCostPerAudioToken:     entry.OutputCostPerAudioToken,
		OutputCostPerVideoPerSecond: entry.OutputCostPerVideoPerSecond,
		OutputCostPerSecond:         entry.OutputCostPerSecond,

		SearchContextCostPerQuery:     entry.SearchContextCostPerQuery,
		CodeInterpreterCostPerSession: entry.CodeInterpreterCostPerSession,

		OCRCostPerPage:        entry.OCRCostPerPage,
		AnnotationCostPerPage: entry.AnnotationCostPerPage,
	}
}

// convertTablePricingToEntry converts a TableModelPricing row from the DB back
// into the Entry shape callers consume.
func convertTablePricingToEntry(pricing *configstoreTables.TableModelPricing) *Entry {
	options := Options{
		InputCostPerToken:                         pricing.InputCostPerToken,
		OutputCostPerToken:                        pricing.OutputCostPerToken,
		InputCostPerTokenBatches:                  pricing.InputCostPerTokenBatches,
		OutputCostPerTokenBatches:                 pricing.OutputCostPerTokenBatches,
		InputCostPerTokenPriority:                 pricing.InputCostPerTokenPriority,
		OutputCostPerTokenPriority:                pricing.OutputCostPerTokenPriority,
		InputCostPerTokenFlex:                     pricing.InputCostPerTokenFlex,
		OutputCostPerTokenFlex:                    pricing.OutputCostPerTokenFlex,
		InputCostPerTokenFast:                     pricing.InputCostPerTokenFast,
		OutputCostPerTokenFast:                    pricing.OutputCostPerTokenFast,
		InputCostPerTokenAbove200kTokens:          pricing.InputCostPerTokenAbove200kTokens,
		InputCostPerTokenAbove200kTokensPriority:  pricing.InputCostPerTokenAbove200kTokensPriority,
		OutputCostPerTokenAbove200kTokens:         pricing.OutputCostPerTokenAbove200kTokens,
		OutputCostPerTokenAbove200kTokensPriority: pricing.OutputCostPerTokenAbove200kTokensPriority,
		InputCostPerTokenAbove272kTokens:          pricing.InputCostPerTokenAbove272kTokens,
		InputCostPerTokenAbove272kTokensPriority:  pricing.InputCostPerTokenAbove272kTokensPriority,
		OutputCostPerTokenAbove272kTokens:         pricing.OutputCostPerTokenAbove272kTokens,
		OutputCostPerTokenAbove272kTokensPriority: pricing.OutputCostPerTokenAbove272kTokensPriority,
		InputCostPerCharacter:                     pricing.InputCostPerCharacter,
		InputCostPerTokenAbove128kTokens:          pricing.InputCostPerTokenAbove128kTokens,
		InputCostPerImageAbove128kTokens:          pricing.InputCostPerImageAbove128kTokens,
		InputCostPerVideoPerSecondAbove128kTokens: pricing.InputCostPerVideoPerSecondAbove128kTokens,
		InputCostPerAudioPerSecondAbove128kTokens: pricing.InputCostPerAudioPerSecondAbove128kTokens,
		OutputCostPerTokenAbove128kTokens:         pricing.OutputCostPerTokenAbove128kTokens,

		CacheCreationInputTokenCost:                        pricing.CacheCreationInputTokenCost,
		CacheReadInputTokenCost:                            pricing.CacheReadInputTokenCost,
		CacheCreationInputTokenCostAbove200kTokens:         pricing.CacheCreationInputTokenCostAbove200kTokens,
		CacheReadInputTokenCostAbove200kTokens:             pricing.CacheReadInputTokenCostAbove200kTokens,
		CacheReadInputTokenCostAbove200kTokensPriority:     pricing.CacheReadInputTokenCostAbove200kTokensPriority,
		CacheCreationInputTokenCostAbove1hr:                pricing.CacheCreationInputTokenCostAbove1hr,
		CacheCreationInputTokenCostAbove1hrAbove200kTokens: pricing.CacheCreationInputTokenCostAbove1hrAbove200kTokens,
		CacheCreationInputAudioTokenCost:                   pricing.CacheCreationInputAudioTokenCost,
		CacheReadInputTokenCostPriority:                    pricing.CacheReadInputTokenCostPriority,
		CacheReadInputTokenCostFlex:                        pricing.CacheReadInputTokenCostFlex,
		CacheReadInputImageTokenCost:                       pricing.CacheReadInputImageTokenCost,
		CacheReadInputTokenCostAbove272kTokens:             pricing.CacheReadInputTokenCostAbove272kTokens,
		CacheReadInputTokenCostAbove272kTokensPriority:     pricing.CacheReadInputTokenCostAbove272kTokensPriority,

		InputCostPerImage:                             pricing.InputCostPerImage,
		InputCostPerPixel:                             pricing.InputCostPerPixel,
		OutputCostPerImage:                            pricing.OutputCostPerImage,
		OutputCostPerPixel:                            pricing.OutputCostPerPixel,
		OutputCostPerImagePremiumImage:                pricing.OutputCostPerImagePremiumImage,
		OutputCostPerImageAbove512x512Pixels:          pricing.OutputCostPerImageAbove512x512Pixels,
		OutputCostPerImageAbove512x512PixelsPremium:   pricing.OutputCostPerImageAbove512x512PixelsPremium,
		OutputCostPerImageAbove1024x1024Pixels:        pricing.OutputCostPerImageAbove1024x1024Pixels,
		OutputCostPerImageAbove1024x1024PixelsPremium: pricing.OutputCostPerImageAbove1024x1024PixelsPremium,
		OutputCostPerImageAbove2048x2048Pixels:        pricing.OutputCostPerImageAbove2048x2048Pixels,
		OutputCostPerImageAbove4096x4096Pixels:        pricing.OutputCostPerImageAbove4096x4096Pixels,
		OutputCostPerImageLowQuality:                  pricing.OutputCostPerImageLowQuality,
		OutputCostPerImageMediumQuality:               pricing.OutputCostPerImageMediumQuality,
		OutputCostPerImageHighQuality:                 pricing.OutputCostPerImageHighQuality,
		OutputCostPerImageAutoQuality:                 pricing.OutputCostPerImageAutoQuality,
		InputCostPerImageToken:                        pricing.InputCostPerImageToken,
		OutputCostPerImageToken:                       pricing.OutputCostPerImageToken,

		InputCostPerAudioToken:      pricing.InputCostPerAudioToken,
		InputCostPerAudioPerSecond:  pricing.InputCostPerAudioPerSecond,
		InputCostPerSecond:          pricing.InputCostPerSecond,
		InputCostPerVideoPerSecond:  pricing.InputCostPerVideoPerSecond,
		OutputCostPerAudioToken:     pricing.OutputCostPerAudioToken,
		OutputCostPerVideoPerSecond: pricing.OutputCostPerVideoPerSecond,
		OutputCostPerSecond:         pricing.OutputCostPerSecond,

		SearchContextCostPerQuery:     pricing.SearchContextCostPerQuery,
		CodeInterpreterCostPerSession: pricing.CodeInterpreterCostPerSession,

		OCRCostPerPage:        pricing.OCRCostPerPage,
		AnnotationCostPerPage: pricing.AnnotationCostPerPage,
	}
	return &Entry{
		BaseModel:            pricing.BaseModel,
		Provider:             pricing.Provider,
		Mode:                 pricing.Mode,
		ContextLength:        pricing.ContextLength,
		MaxInputTokens:       pricing.MaxInputTokens,
		MaxOutputTokens:      pricing.MaxOutputTokens,
		Architecture:         pricing.Architecture,
		AdditionalAttributes: pricing.AdditionalAttributes,
		Options:              options,
	}
}

// convertTableOverride converts a TablePricingOverride to an Override.
func convertTableOverride(override *configstoreTables.TablePricingOverride) (Override, error) {
	var options Options
	if err := sonic.Unmarshal([]byte(override.PricingPatchJSON), &options); err != nil {
		return Override{}, err
	}
	return Override{
		ID:            override.ID,
		Name:          override.Name,
		ScopeKind:     ScopeKind(override.ScopeKind),
		VirtualKeyID:  override.VirtualKeyID,
		ProviderID:    override.ProviderID,
		ProviderKeyID: override.ProviderKeyID,
		MatchType:     MatchType(override.MatchType),
		Pattern:       override.Pattern,
		RequestTypes:  override.RequestTypes,
		Options:       options,
	}, nil
}
