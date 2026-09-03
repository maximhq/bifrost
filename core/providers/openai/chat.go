package openai

import (
	"strings"

	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostChatRequest converts an OpenAI chat request to Bifrost format
func (req *OpenAIChatRequest) ToBifrostChatRequest(ctx *schemas.BifrostContext) *schemas.BifrostChatRequest {
	provider, model := schemas.ParseModelString(req.Model, "")
	params := req.ChatParameters
	if params.MaxCompletionTokens == nil && req.MaxTokens != nil {
		params.MaxCompletionTokens = req.MaxTokens
	}

	return &schemas.BifrostChatRequest{
		Provider:  provider,
		Model:     model,
		Input:     ConvertOpenAIMessagesToBifrostMessages(req.Messages),
		Params:    &params,
		Fallbacks: schemas.ParseFallbacks(req.Fallbacks),
	}
}

// ToOpenAIChatRequest converts a Bifrost chat completion request to OpenAI format
func ToOpenAIChatRequest(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostChatRequest) *OpenAIChatRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	openaiReq := &OpenAIChatRequest{
		Model:    bifrostReq.Model,
		Messages: ConvertBifrostMessagesToOpenAIMessages(bifrostReq.Input),
		Provider: bifrostReq.Provider,
	}

	// Canonical model for capability gating only; wire model (openaiReq.Model) is untouched.
	capModel := schemas.ResolveCanonicalModel(ctx, bifrostReq.Model)
	caps := schemas.ResolveModelCaps(bifrostReq.Provider, capModel)

	if bifrostReq.Params != nil {
		openaiReq.ChatParameters = *bifrostReq.Params
		openaiReq.ServiceTier = serviceTierForModel(caps, openaiReq.ServiceTier)
		if openaiReq.ChatParameters.MaxCompletionTokens != nil && *openaiReq.ChatParameters.MaxCompletionTokens < MinMaxCompletionTokens {
			openaiReq.ChatParameters.MaxCompletionTokens = schemas.Ptr(MinMaxCompletionTokens)
		}
		// Drop user field if it exceeds OpenAI's 64 character limit
		openaiReq.ChatParameters.User = SanitizeUserField(openaiReq.ChatParameters.User)
		openaiReq.ExtraParams = bifrostReq.Params.ExtraParams

		// Normalize tool parameters for deterministic JSON serialization (improves prompt caching)
		if len(openaiReq.ChatParameters.Tools) > 0 {
			normalizedTools := make([]schemas.ChatTool, len(openaiReq.ChatParameters.Tools))
			for i, tool := range openaiReq.ChatParameters.Tools {
				normalizedTools[i] = tool
				if tool.Function != nil && tool.Function.Parameters != nil {
					funcCopy := *tool.Function
					funcCopy.Parameters = tool.Function.Parameters.Normalized()
					normalizedTools[i].Function = &funcCopy
					// The by-value copy carried over any precomputed serialized cache
					// (shared MCP tools cache it at the source). Drop it so MarshalJSON
					// re-emits from the normalized params instead of the stale bytes.
					normalizedTools[i].InvalidateSerialized()
				}
			}
			openaiReq.ChatParameters.Tools = normalizedTools
		}
	}

	switch bifrostReq.Provider {
	case schemas.OpenAI, schemas.Azure:
		openaiReq.normalizeReasoningEffort(caps)
		// URL-sourced documents are NOT inlined here. Chat Completions rejects file_url, so they
		// still have to be resolved before the request goes out - but that is a network fetch that
		// can fail, and this function has no way to report a failure. Callers invoke
		// ResolveChatFileURLs after conversion, where the error can propagate; see its doc comment.
		return openaiReq
	case schemas.Cerebras, schemas.Wafer:
		openaiReq.filterOpenAISpecificParameters(caps)
		openaiReq.stripReasoningDetails()
		return openaiReq
	case schemas.DeepSeek:
		openaiReq.filterOpenAISpecificParameters(caps)
		// DeepSeek is asymmetric: it rejects reasoning_content on ordinary assistant
		// turns, but *requires* it to be replayed on assistant tool_call turns and 400s
		// without it. Stripping both (as Cerebras/Wafer do) forced thinking off for every
		// tool-calling conversation — see issue #5887.
		openaiReq.stripReasoningDetailsExceptToolCalls()
		return openaiReq
	case schemas.XAI:
		openaiReq.filterOpenAISpecificParameters(caps)
		openaiReq.applyXAICompatibility(caps)
		return openaiReq
	case schemas.Gemini:
		openaiReq.filterOpenAISpecificParameters(caps)
		// Removing extra parameters that are not supported by Gemini
		openaiReq.ServiceTier = nil
		return openaiReq
	case schemas.Mistral:
		openaiReq.filterOpenAISpecificParameters(caps)
		openaiReq.applyMistralCompatibility()
		return openaiReq
	case schemas.OpencodeGo, schemas.OpencodeZen:
		openaiReq.filterOpenAISpecificParameters(caps)
		// OpenCode's chat-completions endpoints still use the legacy max_tokens
		// field and ignore max_completion_tokens.
		if openaiReq.MaxCompletionTokens != nil {
			openaiReq.MaxTokens = openaiReq.MaxCompletionTokens
			openaiReq.MaxCompletionTokens = nil
		}
		return openaiReq
	case schemas.Vertex:
		openaiReq.filterOpenAISpecificParameters(caps)

		// Apply Mistral-specific transformations for Vertex Mistral models
		if schemas.IsMistralModel(bifrostReq.Model) {
			openaiReq.applyMistralCompatibility()
		} else if openaiReq.Reasoning != nil && openaiReq.Reasoning.Effort != nil &&
			*openaiReq.Reasoning.Effort == "none" {
			// Vertex Model Garden MaaS models (gpt-oss, Qwen3, kimi-k2-thinking,
			// minimax-m2, ...) reject reasoning_effort "none" — only
			// minimal/low/medium/high are accepted. Drop it so the model uses its
			// default. (Mistral on Vertex does accept "none" and is handled above;
			// // proprietary OpenAI/Azure GPT-5.1+ keep "none" via their own cases.)
			openaiReq.Reasoning.Effort = nil
		}
		return openaiReq
	case schemas.Fireworks:
		// Fireworks uses prompt_cache_isolation_key for cache isolation on chat/completions.
		// Preserve it before the generic filter strips prompt_cache_key.
		if openaiReq.ChatParameters.PromptCacheKey != nil && openaiReq.PromptCacheIsolationKey == nil {
			openaiReq.PromptCacheIsolationKey = openaiReq.ChatParameters.PromptCacheKey
		}
		// Fireworks supports predicted outputs; save before the filter strips them.
		prediction := openaiReq.ChatParameters.Prediction
		openaiReq.filterOpenAISpecificParameters(caps)
		openaiReq.ChatParameters.Prediction = prediction
		return openaiReq
	default:
		// Check if provider is a custom provider
		if isCustomProvider, ok := ctx.Value(schemas.BifrostContextKeyIsCustomProvider).(bool); ok && isCustomProvider {
			return openaiReq
		}
		openaiReq.filterOpenAISpecificParameters(caps)
		return openaiReq
	}
}

// providerRejectsServiceTier reports whether the provider's endpoint implements
// service_tier at all. Bedrock Mantle does not: its OpenAI-compatible surface on
// bedrock-mantle.{region}.api.aws rejects the field outright ("'priority' is not
// supported for 'service_tier' on this model"). Provider bedrock reaches these
// converters only through the deprecated in-provider Mantle routing in
// bedrock/mantle.go — every other Bedrock path uses Converse — so it means the
// same endpoint and the same rejection.
func providerRejectsServiceTier(provider schemas.ModelProvider) bool {
	switch provider {
	case schemas.BedrockMantle, schemas.Bedrock:
		return true
	default:
		return false
	}
}

// serviceTierForModel filters a requested tier against the final target model's
// capabilities. Omitting an unsupported tier lets the provider use its default
// instead of returning an unsupported-tier error.
func serviceTierForModel(caps schemas.ModelCaps, tier *schemas.BifrostServiceTier) *schemas.BifrostServiceTier {
	if tier == nil {
		return nil
	}
	// Checked before the datasheet: ServiceTierSupported falls back to "keep the
	// tier" when the catalog has no row for the pair, and Mantle model ids
	// (openai.gpt-5.6-terra, ...) generally have none — so the fallback would
	// forward a field the endpoint 400s on.
	if providerRejectsServiceTier(caps.Provider()) {
		return nil
	}
	if !caps.ServiceTierSupported(*tier, true) {
		return nil
	}
	return tier
}

// Filter OpenAI Specific Parameters
func (req *OpenAIChatRequest) filterOpenAISpecificParameters(caps schemas.ModelCaps) {
	// Handle reasoning parameter: OpenAI uses effort-based reasoning
	// Priority: effort (native) > max_tokens (estimated)
	req.normalizeReasoningEffort(caps)

	// OpenAI-native passthrough params. Compat providers reject them, so the
	// fallback strips; a datasheet row can opt an individual model back in.
	if caps.FieldUnsupported(schemas.FieldPrediction, true) {
		req.ChatParameters.Prediction = nil
	}
	if caps.FieldUnsupported(schemas.FieldPromptCacheKey, true) {
		req.ChatParameters.PromptCacheKey = nil
	}
	if caps.FieldUnsupported(schemas.FieldPromptCacheRetention, true) {
		req.ChatParameters.PromptCacheRetention = nil
	}
	if caps.FieldUnsupported(schemas.FieldPromptCacheOptions, true) {
		req.ChatParameters.PromptCacheOptions = nil
	}
	if caps.FieldUnsupported(schemas.FieldVerbosity, true) {
		req.ChatParameters.Verbosity = nil
	}
	if caps.FieldUnsupported(schemas.FieldStore, true) {
		req.ChatParameters.Store = nil
	}
	if caps.FieldUnsupported(schemas.FieldWebSearchOptions, true) {
		req.ChatParameters.WebSearchOptions = nil
	}
}

func (req *OpenAIChatRequest) normalizeReasoningEffort(caps schemas.ModelCaps) {
	if req.ChatParameters.Reasoning != nil {
		reasoningCopy := *req.ChatParameters.Reasoning
		req.ChatParameters.Reasoning = &reasoningCopy
		if req.ChatParameters.Reasoning.Effort != nil {
			// Native field is provided, use it (and clear max_tokens)
			effort := *req.ChatParameters.Reasoning.Effort
			req.ChatParameters.Reasoning.Effort = schemas.Ptr(caps.NormalizeReasoningEffort(effort, defaultEffortControl(caps.Model())))
			// Clear max_tokens since OpenAI doesn't use it
			req.ChatParameters.Reasoning.MaxTokens = nil
		} else if req.ChatParameters.Reasoning.MaxTokens != nil {
			// Estimate effort from max_tokens
			maxTokens := *req.ChatParameters.Reasoning.MaxTokens
			maxCompletionTokens := utils.GetMaxOutputTokensOrDefault(req.Provider, req.Model, DefaultCompletionMaxTokens)
			if req.ChatParameters.MaxCompletionTokens != nil {
				maxCompletionTokens = *req.ChatParameters.MaxCompletionTokens
			}
			effort := utils.GetReasoningEffortFromBudgetTokens(maxTokens, MinReasoningMaxTokens, maxCompletionTokens)
			req.ChatParameters.Reasoning.Effort = schemas.Ptr(effort)
			// Clear max_tokens since OpenAI doesn't use it
			req.ChatParameters.Reasoning.MaxTokens = nil
		}
	}
}

// applyMistralCompatibility applies Mistral-specific transformations to the request
func (req *OpenAIChatRequest) applyMistralCompatibility() {
	// Mistral uses max_tokens instead of max_completion_tokens
	if req.MaxCompletionTokens != nil {
		req.MaxTokens = req.MaxCompletionTokens
		req.MaxCompletionTokens = nil
	}

	// Mistral does not support ToolChoiceStruct, only simple tool choice strings are supported
	if req.ToolChoice != nil && req.ToolChoice.ChatToolChoiceStruct != nil {
		req.ToolChoice.ChatToolChoiceStr = schemas.Ptr("any")
		req.ToolChoice.ChatToolChoiceStruct = nil
	}

	// Mistral only support reasoning effort "none" and "high"
	if req.Reasoning != nil && req.Reasoning.Effort != nil {
		if *req.Reasoning.Effort != "none" && *req.Reasoning.Effort != "high" {
			req.Reasoning.Effort = schemas.Ptr("high")
		}
	}
}

// stripReasoningDetails for providers that throw error for reasoning_details in assistant messages
// e.g. Cerebras, DeepSeek
func (req *OpenAIChatRequest) stripReasoningDetails() {
	for i := range req.Messages {
		assistantMessage := req.Messages[i].OpenAIChatAssistantMessage
		if assistantMessage == nil {
			continue
		}
		assistantMessage.Reasoning = nil
	}
}

// stripReasoningDetailsExceptToolCalls strips reasoning_content from assistant messages that
// carry no tool calls, and preserves it on assistant tool_call turns. This is DeepSeek's
// contract: reasoning_content "must be passed back to the API in all subsequent user
// interaction turns" for tool calls, while an ordinary assistant turn's reasoning_content
// "does not need to participate in the context concatenation".
//
// Mutating in place is safe here — ConvertBifrostMessagesToOpenAIMessages allocates a fresh
// OpenAIChatAssistantMessage per message, so the caller's input is never touched.
func (req *OpenAIChatRequest) stripReasoningDetailsExceptToolCalls() {
	for i := range req.Messages {
		assistantMessage := req.Messages[i].OpenAIChatAssistantMessage
		if assistantMessage == nil || len(assistantMessage.ToolCalls) > 0 {
			continue
		}
		assistantMessage.Reasoning = nil
	}
}

// applyXAICompatibility applies xAI-specific transformations to the request.
// Each gate prefers the datasheet's unsupported_fields entry, falling back to
// the per-model name detection below.
func (req *OpenAIChatRequest) applyXAICompatibility(caps schemas.ModelCaps) {
	model := caps.Model()
	isGrokReasoning := schemas.IsGrokReasoningModel(model)

	if caps.FieldUnsupported(schemas.FieldPresencePenalty, isGrokReasoning) {
		req.ChatParameters.PresencePenalty = nil
	}

	// Only non-mini grok-3 models support frequency_penalty and stop
	// grok-3-mini only supports reasoning_effort in reasoning mode
	penaltyUnsupported := isGrokReasoning &&
		(!strings.Contains(model, "grok-3") || strings.Contains(model, "grok-3-mini"))
	if caps.FieldUnsupported(schemas.FieldFrequencyPenalty, penaltyUnsupported) {
		req.ChatParameters.FrequencyPenalty = nil
	}
	if caps.FieldUnsupported(schemas.FieldStop, penaltyUnsupported) {
		req.ChatParameters.Stop = nil
	}

	// Strip reasoning_effort only for the models known to reject it; current-generation
	// models (grok-4.5, grok-4.6, grok-4.20-*) accept it. See SupportsGrokReasoningEffort.
	effortUnsupported := isGrokReasoning && !schemas.SupportsGrokReasoningEffort(model)
	if req.ChatParameters.Reasoning != nil &&
		caps.FieldUnsupported(schemas.FieldReasoningEffort, effortUnsupported) {
		req.ChatParameters.Reasoning.Effort = nil
	}
}
