// Inference operations served by both Databricks surfaces. Every method delegates to the
// shared OpenAI handlers; this file only supplies the resolved URL and Authorization header.
package databricks

import (
	"context"
	"maps"
	"strings"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// paramSupport records which optional wire fields the resolved model accepts. Bifrost's
// neutral parameter set is broader than any single Databricks endpoint takes, and both
// surfaces answer a field they do not know with a 400 rather than ignoring it, so every
// optional field is resolved against the datasheet row for the model before the request is
// converted.
//
// Every fallback is "keep the field": a workspace can serve a model the datasheet has never
// heard of, and a silent drop is worse than an upstream error the caller can read.
// reasoning_effort and parallel_tool_calls are the exceptions — see resolveParamSupport.
type paramSupport struct {
	reasoningEffort   bool // reasoning_effort
	samplingParams    bool // temperature, top_p, top_k
	toolChoice        bool // tool_choice
	parallelToolCalls bool // parallel_tool_calls
	responseSchema    bool // response_format (chat), text.format (responses)
	stop              bool // stop
	presencePenalty   bool // presence_penalty
	frequencyPenalty  bool // frequency_penalty

	// anthropicThinking marks a Claude-backed endpoint, which takes reasoning as a "thinking"
	// object instead of reasoning_effort (see reasoning.go). adaptiveOnlyThinking narrows that
	// to the models that have dropped budget_tokens.
	anthropicThinking    bool
	adaptiveOnlyThinking bool
}

// resolveParamSupport reads the datasheet record for the model behind a Databricks endpoint
// and answers, per field, whether Bifrost may send it.
//
// reasoning_effort resolves through: an explicit unsupported_fields entry →
// supports_reasoning_effort (or a published effort ladder) → a reasoning_effort entry in the
// row's model_parameters → the model reasons and is not Anthropic-family. The last clause is
// what makes Claude-on-Databricks correct: those endpoints reason through Claude's thinking
// budget and reject an effort label, which is the 400 Claude Code requests hit. A model with
// no row drops the field, since Databricks endpoint names are workspace-defined and there is
// nothing else to go on.
func resolveParamSupport(ctx *schemas.BifrostContext, model string) paramSupport {
	canonicalModel := schemas.ResolveCanonicalModel(ctx, model)
	caps := schemas.ResolveModelCaps(schemas.Databricks, canonicalModel)

	isAnthropic := schemas.IsAnthropicModelFamily(ctx, canonicalModel)
	effortFallback := caps.PublishesParameter(schemas.FieldReasoningEffort) ||
		(caps.SupportsReasoning(false) && !isAnthropic)

	return paramSupport{
		anthropicThinking:    isAnthropic,
		adaptiveOnlyThinking: caps.AdaptiveOnlyThinking(anthropic.DefaultAdaptiveOnlyThinking(canonicalModel)),
		reasoningEffort: !caps.FieldUnsupported(schemas.FieldReasoningEffort,
			!caps.SupportsReasoningEffort(effortFallback)),
		// supports_sampling_params is false on the adaptive-only Claude models
		// (Opus 4.7+, Sonnet 5+, Fable), which 400 on temperature/top_p/top_k.
		samplingParams: caps.SupportsSamplingParams(!caps.FieldUnsupported(schemas.FieldTopP, false)),
		toolChoice:     caps.SupportsToolChoice(true),
		// Both surfaces reject parallel_tool_calls outright ("Extra inputs are not
		// permitted" on Model Serving, "unknown field" on the AI Gateway), so a model
		// with no datasheet row must not send it. A row that says otherwise still wins.
		parallelToolCalls: caps.SupportsParallelFunctionCalling(false),
		responseSchema:    caps.SupportsResponseSchema(true),
		stop:              !caps.FieldUnsupported(schemas.FieldStop, false),
		presencePenalty:   !caps.FieldUnsupported(schemas.FieldPresencePenalty, false),
		frequencyPenalty:  !caps.FieldUnsupported(schemas.FieldFrequencyPenalty, false),
	}
}

// stripUnsupportedChatFields removes request fields the resolved Databricks model rejects.
// Work on a copy because the original request may be reused by fallbacks and post-hooks.
//
// The Anthropic-native knobs are dropped unconditionally rather than per model. They are
// carried on the neutral chat parameters and serialize straight onto the wire, but both
// Databricks surfaces speak OpenAI shapes that reject them whichever model backs the
// endpoint — a fact about the surface, not a model capability. context_management is the one
// that surfaced first, as a 400 on Claude Code requests.
func stripUnsupportedChatFields(ctx *schemas.BifrostContext, request *schemas.BifrostChatRequest) *schemas.BifrostChatRequest {
	if request == nil || request.Params == nil {
		return request
	}
	params := request.Params
	support := resolveParamSupport(ctx, request.Model)

	_, hasExtraContextManagement := params.ExtraParams["context_management"]
	dropAnthropicFields := len(params.ContextManagement) > 0 || hasExtraContextManagement ||
		params.CacheControl != nil || params.Speed != nil || params.InferenceGeo != nil ||
		params.TaskBudget != nil || params.Container != nil || len(params.MCPServers) > 0

	_, hasExtraReasoningEffort := params.ExtraParams["reasoning_effort"]
	hasReasoningEffort := hasExtraReasoningEffort || (params.Reasoning != nil &&
		(params.Reasoning.Effort != nil || params.Reasoning.MaxTokens != nil))

	dropReasoningEffort := hasReasoningEffort && !support.reasoningEffort
	dropSamplingParams := !support.samplingParams &&
		(params.Temperature != nil || params.TopP != nil || params.TopK != nil)
	dropToolChoice := !support.toolChoice && params.ToolChoice != nil
	dropParallelToolCalls := !support.parallelToolCalls && params.ParallelToolCalls != nil
	dropResponseFormat := !support.responseSchema && params.ResponseFormat != nil
	dropStop := !support.stop && len(params.Stop) > 0
	dropPresencePenalty := !support.presencePenalty && params.PresencePenalty != nil
	dropFrequencyPenalty := !support.frequencyPenalty && params.FrequencyPenalty != nil

	// A Claude-backed endpoint does not lose the reasoning request when reasoning_effort is
	// dropped: it is re-expressed as the "thinking" object the endpoint does accept.
	var thinking map[string]any
	var thinkingMaxTokens *int
	if dropReasoningEffort {
		thinking, thinkingMaxTokens = thinkingParam(support, params)
	}

	if !dropAnthropicFields && !dropReasoningEffort && !dropSamplingParams && !dropToolChoice &&
		!dropParallelToolCalls && !dropResponseFormat && !dropStop && !dropPresencePenalty &&
		!dropFrequencyPenalty {
		return request
	}

	requestCopy := *request
	paramsCopy := *params
	if thinking != nil {
		paramsCopy.ExtraParams = maps.Clone(paramsCopy.ExtraParams)
		if paramsCopy.ExtraParams == nil {
			paramsCopy.ExtraParams = map[string]any{}
		}
		paramsCopy.ExtraParams["thinking"] = thinking
		if thinkingMaxTokens != nil {
			paramsCopy.MaxCompletionTokens = thinkingMaxTokens
		}
	}
	if dropAnthropicFields {
		paramsCopy.ContextManagement = nil
		paramsCopy.CacheControl = nil
		paramsCopy.Speed = nil
		paramsCopy.InferenceGeo = nil
		paramsCopy.TaskBudget = nil
		paramsCopy.Container = nil
		paramsCopy.MCPServers = nil
	}
	if dropReasoningEffort && paramsCopy.Reasoning != nil {
		reasoningCopy := *paramsCopy.Reasoning
		reasoningCopy.Effort = nil
		// The shared OpenAI converter derives reasoning_effort from MaxTokens
		// when Effort is absent, so clear both inputs to guarantee omission.
		reasoningCopy.MaxTokens = nil
		paramsCopy.Reasoning = &reasoningCopy
	}
	if dropSamplingParams {
		paramsCopy.Temperature = nil
		paramsCopy.TopP = nil
		paramsCopy.TopK = nil
	}
	if dropToolChoice {
		paramsCopy.ToolChoice = nil
	}
	if dropParallelToolCalls {
		paramsCopy.ParallelToolCalls = nil
	}
	if dropResponseFormat {
		paramsCopy.ResponseFormat = nil
	}
	if dropStop {
		paramsCopy.Stop = nil
	}
	if dropPresencePenalty {
		paramsCopy.PresencePenalty = nil
	}
	if dropFrequencyPenalty {
		paramsCopy.FrequencyPenalty = nil
	}
	// Only the extra-param spellings of fields dropped above are removed. Everything else in
	// ExtraParams is the documented passthrough for endpoint-specific knobs Bifrost does not
	// model, so it is forwarded untouched.
	if paramsCopy.ExtraParams != nil && (dropAnthropicFields || dropReasoningEffort) {
		paramsCopy.ExtraParams = maps.Clone(paramsCopy.ExtraParams)
		if dropAnthropicFields {
			delete(paramsCopy.ExtraParams, "context_management")
		}
		if dropReasoningEffort {
			delete(paramsCopy.ExtraParams, "reasoning_effort")
		}
	}
	requestCopy.Params = &paramsCopy
	return &requestCopy
}

// stripUnsupportedResponsesFields is stripUnsupportedChatFields for the Model Serving
// Responses surface, which carries the same fields through a different params struct — minus
// the chat-only knobs (stop, the penalties, top_k) and the Anthropic betas, which the
// neutral Responses parameters do not model at all. The AI Gateway surface has no native
// Responses endpoint and is emulated through ChatCompletion, so it is sanitized by the chat
// path instead.
func stripUnsupportedResponsesFields(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest) *schemas.BifrostResponsesRequest {
	if request == nil || request.Params == nil {
		return request
	}
	params := request.Params
	support := resolveParamSupport(ctx, request.Model)

	_, hasExtraContextManagement := params.ExtraParams["context_management"]
	dropContextManagement := len(params.ContextManagement) > 0 || hasExtraContextManagement

	_, hasExtraReasoningEffort := params.ExtraParams["reasoning_effort"]
	hasReasoningEffort := hasExtraReasoningEffort || (params.Reasoning != nil &&
		(params.Reasoning.Effort != nil || params.Reasoning.MaxTokens != nil))

	dropReasoningEffort := hasReasoningEffort && !support.reasoningEffort
	dropSamplingParams := !support.samplingParams && (params.Temperature != nil || params.TopP != nil)
	dropToolChoice := !support.toolChoice && params.ToolChoice != nil
	dropParallelToolCalls := !support.parallelToolCalls && params.ParallelToolCalls != nil
	dropTextFormat := !support.responseSchema && params.Text != nil && params.Text.Format != nil

	if !dropContextManagement && !dropReasoningEffort && !dropSamplingParams && !dropToolChoice &&
		!dropParallelToolCalls && !dropTextFormat {
		return request
	}

	requestCopy := *request
	paramsCopy := *params
	paramsCopy.ContextManagement = nil
	if dropReasoningEffort && paramsCopy.Reasoning != nil {
		reasoningCopy := *paramsCopy.Reasoning
		reasoningCopy.Effort = nil
		reasoningCopy.MaxTokens = nil
		paramsCopy.Reasoning = &reasoningCopy
	}
	if dropSamplingParams {
		paramsCopy.Temperature = nil
		paramsCopy.TopP = nil
	}
	if dropToolChoice {
		paramsCopy.ToolChoice = nil
	}
	if dropParallelToolCalls {
		paramsCopy.ParallelToolCalls = nil
	}
	if dropTextFormat {
		// text also carries verbosity, so only the schema is cleared.
		textCopy := *paramsCopy.Text
		textCopy.Format = nil
		paramsCopy.Text = &textCopy
	}
	if paramsCopy.ExtraParams != nil && (dropContextManagement || dropReasoningEffort) {
		paramsCopy.ExtraParams = maps.Clone(paramsCopy.ExtraParams)
		if dropContextManagement {
			delete(paramsCopy.ExtraParams, "context_management")
		}
		if dropReasoningEffort {
			delete(paramsCopy.ExtraParams, "reasoning_effort")
		}
	}
	requestCopy.Params = &paramsCopy
	return &requestCopy
}

// ChatCompletion performs a chat completion request against the resolved Databricks surface.
func (provider *DatabricksProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	request = stripUnsupportedChatFields(ctx, request)
	request, bErr := inlineChatImageURLs(ctx, request)
	if bErr != nil {
		return nil, bErr
	}
	url, auth, bErr := provider.prepareRequest(ctx, key, request.Model, "/chat/completions")
	if bErr != nil {
		return nil, bErr
	}
	return openai.HandleOpenAIChatCompletionRequest(
		ctx,
		provider.client,
		url,
		request,
		auth,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		chatResponseHandler,
		parseDatabricksError,
		nil,
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming chat completion request against the resolved
// Databricks surface. Both surfaces emit OpenAI-shaped Server-Sent Events.
func (provider *DatabricksProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	request = stripUnsupportedChatFields(ctx, request)
	request, bErr := inlineChatImageURLs(ctx, request)
	if bErr != nil {
		return nil, bErr
	}
	url, auth, bErr := provider.prepareRequest(ctx, key, request.Model, "/chat/completions")
	if bErr != nil {
		return nil, bErr
	}
	return openai.HandleOpenAIChatCompletionStreaming(
		ctx,
		provider.streamingClient,
		url,
		request,
		auth,
		provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		chatResponseHandler,
		parseDatabricksError,
		chatStreamOptionsFixup(request.Model),
		nil,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)
}

// Embedding performs an embedding request against the resolved Databricks surface.
func (provider *DatabricksProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	url, auth, bErr := provider.prepareRequest(ctx, key, request.Model, "/embeddings")
	if bErr != nil {
		return nil, bErr
	}
	return openai.HandleOpenAIEmbeddingRequest(
		ctx,
		provider.client,
		url,
		request,
		auth,
		provider.networkConfig.ExtraHeaders,
		provider.GetProviderKey(),
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		nil,
		parseDatabricksError,
		provider.logger,
	)
}

// Responses performs a responses request. Model Serving documents the OpenAI Responses API
// at /serving-endpoints/responses, but pay-per-token foundation model endpoints answer it
// with "Responses API passthrough is not supported" (HTTP 400). The Unity AI Gateway MLflow
// surface has no Responses route at all. In both cases the request is emulated through chat
// completions; see emulateResponses for the rule.
func (provider *DatabricksProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	emulate := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		chatResponse, bErr := provider.ChatCompletion(ctx, key, request.ToChatRequest())
		if bErr != nil {
			return nil, bErr
		}
		return chatResponse.ToBifrostResponsesResponse(), nil
	}
	if provider.emulateResponses(key, request.Model) {
		return emulate()
	}

	wireRequest := stripUnsupportedResponsesFields(ctx, request)
	wireRequest, bErr := inlineResponsesImageURLs(ctx, wireRequest)
	if bErr != nil {
		return nil, bErr
	}
	url, auth, bErr := provider.prepareRequest(ctx, key, wireRequest.Model, "/responses")
	if bErr != nil {
		return nil, bErr
	}
	response, bErr := openai.HandleOpenAIResponsesRequest(
		ctx,
		provider.client,
		url,
		wireRequest,
		auth,
		provider.networkConfig.ExtraHeaders,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		nil,
		parseDatabricksError,
		nil,
		provider.logger,
	)
	if bErr != nil && isResponsesPassthroughUnsupported(bErr) {
		provider.markResponsesUnsupported(key, request.Model)
		return emulate()
	}
	return response, bErr
}

// ResponsesStream performs a streaming responses request. See Responses for how the two
// surfaces differ. The native call fails before any chunk is produced when the endpoint
// rejects the surface, so retrying through chat is safe: nothing has reached the caller.
func (provider *DatabricksProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	emulate := func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
		return provider.ChatCompletionStream(ctx, postHookRunner, postHookSpanFinalizer, key, request.ToChatRequest())
	}
	if provider.emulateResponses(key, request.Model) {
		return emulate()
	}

	wireRequest := stripUnsupportedResponsesFields(ctx, request)
	wireRequest, bErr := inlineResponsesImageURLs(ctx, wireRequest)
	if bErr != nil {
		return nil, bErr
	}
	url, auth, bErr := provider.prepareRequest(ctx, key, wireRequest.Model, "/responses")
	if bErr != nil {
		return nil, bErr
	}
	stream, bErr := openai.HandleOpenAIResponsesStreaming(
		ctx,
		provider.streamingClient,
		url,
		wireRequest,
		auth,
		provider.networkConfig.ExtraHeaders,
		provider.networkConfig.StreamIdleTimeoutInSeconds,
		providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest),
		providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		provider.GetProviderKey(),
		postHookRunner,
		nil,
		parseDatabricksError,
		nil,
		nil,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)
	if bErr != nil && isResponsesPassthroughUnsupported(bErr) {
		provider.markResponsesUnsupported(key, request.Model)
		return emulate()
	}
	return stream, bErr
}

// responsesPassthroughUnsupportedMarker is the fragment of the 400 body a Model Serving
// endpoint returns when it does not expose the Responses surface for the model.
const responsesPassthroughUnsupportedMarker = "responses api passthrough is not supported"

// isResponsesPassthroughUnsupported reports whether an upstream error is the endpoint
// declining the Responses surface, as opposed to rejecting this particular request.
func isResponsesPassthroughUnsupported(bErr *schemas.BifrostError) bool {
	if bErr == nil || bErr.Error == nil {
		return false
	}
	if bErr.StatusCode != nil && *bErr.StatusCode != fasthttp.StatusBadRequest {
		return false
	}
	return strings.Contains(strings.ToLower(bErr.Error.Message), responsesPassthroughUnsupportedMarker)
}

// emulateResponses decides whether a Responses request is served through chat completions
// without trying the native route first: always on the AI Gateway, and on Model Serving once
// the endpoint has declined the surface for this workspace and model.
func (provider *DatabricksProvider) emulateResponses(key schemas.Key, model string) bool {
	if resolveAPIFormat(key, model) == schemas.DatabricksAPIFormatAIGateway {
		return true
	}
	_, unsupported := provider.responsesUnsupported.Load(provider.responsesCacheKey(key, model))
	return unsupported
}

// markResponsesUnsupported remembers that the native Responses route declined a model so
// later requests skip the failing round trip. The decision is per workspace host and model
// because the same endpoint name can be served differently on different workspaces.
func (provider *DatabricksProvider) markResponsesUnsupported(key schemas.Key, model string) {
	cacheKey := provider.responsesCacheKey(key, model)
	if _, loaded := provider.responsesUnsupported.LoadOrStore(cacheKey, struct{}{}); !loaded {
		provider.logger.Info("[databricks] native Responses API declined for %s; emulating through chat completions from now on", cacheKey)
	}
}

func (provider *DatabricksProvider) responsesCacheKey(key schemas.Key, model string) string {
	host, bErr := provider.resolveWorkspaceHost(key)
	if bErr != nil {
		host = ""
	}
	return host + "|" + model
}
