// Inference operations served by both Databricks surfaces. Every method delegates to the
// shared OpenAI handlers; this file only supplies the resolved URL and Authorization header.
package databricks

import (
	"context"
	"maps"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// paramSupport records which optional wire fields the resolved model accepts. Bifrost's
// neutral parameter set is broader than any single Databricks endpoint takes, and both
// surfaces answer a field they do not know with a 400 rather than ignoring it, so every
// optional field is resolved against the datasheet row for the model before the request is
// converted.
//
// Every fallback is "keep the field": a workspace can serve a model the datasheet has never
// heard of, and a silent drop is worse than an upstream error the caller can read.
// reasoning_effort is the exception — see resolveParamSupport.
type paramSupport struct {
	reasoningEffort   bool // reasoning_effort
	samplingParams    bool // temperature, top_p, top_k
	toolChoice        bool // tool_choice
	parallelToolCalls bool // parallel_tool_calls
	responseSchema    bool // response_format (chat), text.format (responses)
	stop              bool // stop
	presencePenalty   bool // presence_penalty
	frequencyPenalty  bool // frequency_penalty
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

	effortFallback := caps.PublishesParameter(schemas.FieldReasoningEffort) ||
		(caps.SupportsReasoning(false) && !schemas.IsAnthropicModelFamily(ctx, canonicalModel))

	return paramSupport{
		reasoningEffort: !caps.FieldUnsupported(schemas.FieldReasoningEffort,
			!caps.SupportsReasoningEffort(effortFallback)),
		// supports_sampling_params is false on the adaptive-only Claude models
		// (Opus 4.7+, Sonnet 5+, Fable), which 400 on temperature/top_p/top_k.
		samplingParams:    caps.SupportsSamplingParams(!caps.FieldUnsupported(schemas.FieldTopP, false)),
		toolChoice:        caps.SupportsToolChoice(true),
		parallelToolCalls: caps.SupportsParallelFunctionCalling(true),
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

	if !dropAnthropicFields && !dropReasoningEffort && !dropSamplingParams && !dropToolChoice &&
		!dropParallelToolCalls && !dropResponseFormat && !dropStop && !dropPresencePenalty &&
		!dropFrequencyPenalty {
		return request
	}

	requestCopy := *request
	paramsCopy := *params
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
		nil,
		parseDatabricksError,
		nil,
		provider.logger,
	)
}

// ChatCompletionStream performs a streaming chat completion request against the resolved
// Databricks surface. Both surfaces emit OpenAI-shaped Server-Sent Events.
func (provider *DatabricksProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	request = stripUnsupportedChatFields(ctx, request)
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
		nil,
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

// Responses performs a responses request. Model Serving exposes the OpenAI Responses API
// natively at /serving-endpoints/responses. The Unity AI Gateway MLflow surface is chat-only,
// so there the request is emulated through chat completions.
func (provider *DatabricksProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if resolveAPIFormat(key, request.Model) == schemas.DatabricksAPIFormatAIGateway {
		chatResponse, bErr := provider.ChatCompletion(ctx, key, request.ToChatRequest())
		if bErr != nil {
			return nil, bErr
		}
		return chatResponse.ToBifrostResponsesResponse(), nil
	}

	request = stripUnsupportedResponsesFields(ctx, request)
	url, auth, bErr := provider.prepareRequest(ctx, key, request.Model, "/responses")
	if bErr != nil {
		return nil, bErr
	}
	return openai.HandleOpenAIResponsesRequest(
		ctx,
		provider.client,
		url,
		request,
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
}

// ResponsesStream performs a streaming responses request. See Responses for how the two
// surfaces differ.
func (provider *DatabricksProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context), key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if resolveAPIFormat(key, request.Model) == schemas.DatabricksAPIFormatAIGateway {
		ctx.SetValue(schemas.BifrostContextKeyIsResponsesToChatCompletionFallback, true)
		return provider.ChatCompletionStream(ctx, postHookRunner, postHookSpanFinalizer, key, request.ToChatRequest())
	}

	request = stripUnsupportedResponsesFields(ctx, request)
	url, auth, bErr := provider.prepareRequest(ctx, key, request.Model, "/responses")
	if bErr != nil {
		return nil, bErr
	}
	return openai.HandleOpenAIResponsesStreaming(
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
		parseDatabricksError,
		nil,
		nil,
		nil,
		provider.logger,
		postHookSpanFinalizer,
	)
}
