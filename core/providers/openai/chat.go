package openai

import (
	"encoding/json"
	"slices"
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
	// OpenAI chat has no reasoning.summary; on this integration it is only sent via passthrough extra_params.
	if params.Reasoning != nil && params.Reasoning.Summary != nil {
		reasoning := *params.Reasoning
		reasoning.Summary = nil
		params.Reasoning = &reasoning
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
		// Fable 5.1+ rejects forced tool use outright. Drop the choice so the model
		// answers under the default "auto" rather than the provider returning a 400.
		if openaiReq.ChatParameters.ToolChoice.IsForced() &&
			!caps.SupportsForcedToolChoice(schemas.DefaultSupportsForcedToolChoice(capModel)) {
			openaiReq.ChatParameters.ToolChoice = nil
		}
		// The Anthropic integration emits the provider-generic forced tool choice "any".
		// OpenAI accepts only "none", "auto" and "required" as string tool choices and
		// rejects "any" with HTTP 400, so map it to "required" on a copy of the choice
		// without mutating the caller's parameters. Destinations that accept "any"
		// natively keep it.
		if tc := openaiReq.ChatParameters.ToolChoice; tc != nil && tc.ChatToolChoiceStr != nil &&
			*tc.ChatToolChoiceStr == string(schemas.ChatToolChoiceTypeAny) &&
			!caps.ToolChoiceAnySupported(toolChoiceAnySupported(bifrostReq.Provider, capModel)) {
			openaiReq.ChatParameters.ToolChoice = &schemas.ChatToolChoice{
				ChatToolChoiceStr: schemas.Ptr(string(schemas.ChatToolChoiceTypeRequired)),
			}
		}
		openaiReq.ExtraParams = bifrostReq.Params.ExtraParams

		// Models older than GPT-5 reject custom tools with "Invalid value: 'custom'",
		// so each one is sent as a function tool. Chat has no custom tool call shape,
		// so the model's call reaches the client as a function call.
		customToolsAsFunctions := customToolsUnsupported(ctx, bifrostReq.Provider, bifrostReq.Model)
		if customToolsAsFunctions {
			openaiReq.ChatParameters.ToolChoice = chatCustomToolChoiceAsFunction(openaiReq.ChatParameters.ToolChoice)
		}

		// Normalize tool parameters for deterministic JSON serialization (improves prompt caching)
		if len(openaiReq.ChatParameters.Tools) > 0 {
			normalizedTools := make([]schemas.ChatTool, len(openaiReq.ChatParameters.Tools))
			for i, tool := range openaiReq.ChatParameters.Tools {
				if customToolsAsFunctions && tool.Type == schemas.ChatToolTypeCustom {
					tool = chatCustomToolAsFunction(tool)
				}
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

	// gpt-5.6 and later take caching intent as prompt_cache_breakpoint on a text
	// part rather than as Anthropic-style cache_control, which the serializer
	// strips. Same translation as the Responses path (applyResponsesCacheBreakpoints),
	// resolved on the base provider so a custom provider built on OpenAI is not
	// sent down the strip-only default.
	// The request stays in the provider's default mode: see
	// responsesIsOpenAIFamilyBreakpointTarget for why Bifrost never sets explicit mode.
	if chatUsesPromptCacheBreakpoints(caps, schemas.ResolveBaseProvider(ctx, bifrostReq.Provider), capModel) {
		ceiling := promptCacheBreakpointCeiling(ctx, true, openaiReq.ChatParameters.PromptCacheOptions, openaiReq.ExtraParams)
		openaiReq.Messages = applyChatCacheBreakpoints(openaiReq.Messages, ceiling)
	}

	// OpenRouter forwards cache_control verbatim to Claude, which rejects more than four
	// markers outright, so clamp the same way the direct Anthropic path does.
	if schemas.ResolveBaseProvider(ctx, bifrostReq.Provider) == schemas.OpenRouter && schemas.IsAnthropicModel(capModel) {
		clampOpenRouterCacheControl(openaiReq, maxResponsesCacheBreakpoints)
	}

	switch bifrostReq.Provider {
	case schemas.OpenAI, schemas.Azure:
		openaiReq.normalizeReasoningEffort(caps)
		openaiReq.stripUnsupportedSamplingParams(caps)
		// URL-sourced documents are NOT inlined here. Chat Completions rejects file_url, so they
		// still have to be resolved before the request goes out - but that is a network fetch that
		// can fail, and this function has no way to report a failure. Callers invoke
		// ResolveChatFileURLs after conversion, where the error can propagate; see its doc comment.
		return openaiReq
	case schemas.DeepSeek:
		openaiReq.filterOpenAISpecificParameters(caps)
		// DeepSeek's chat-completions endpoint still uses the legacy max_tokens
		// field and ignores max_completion_tokens.
		if openaiReq.MaxCompletionTokens != nil {
			openaiReq.MaxTokens = openaiReq.MaxCompletionTokens
			openaiReq.MaxCompletionTokens = nil
		}
		// DeepSeek is asymmetric: it rejects reasoning_content on ordinary assistant
		// turns, but *requires* it to be replayed on assistant tool_call turns and 400s
		// without it. Stripping both forced thinking off for every tool-calling conversation
		// — see issue #5887.
		openaiReq.stripReasoningDetailsExceptToolCalls()
		openaiReq.downgradeJSONSchemaToJSONObject()
		return openaiReq
	case schemas.Groq, schemas.Cerebras:
		openaiReq.filterOpenAISpecificParameters(caps)
		openaiReq.renameAssistantReasoningToAlias()
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
	case schemas.Ollama:
		openaiReq.filterOpenAISpecificParameters(caps)
		// Ollama's chat-completions endpoints still use the legacy max_tokens
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
			// A custom provider reports its own name, so a DeepSeek-based one lands here
			// and still needs the json_schema downgrade DeepSeek's endpoint requires.
			if schemas.ResolveBaseProvider(ctx, bifrostReq.Provider) == schemas.DeepSeek {
				openaiReq.downgradeJSONSchemaToJSONObject()
			}
			return openaiReq
		}
		openaiReq.filterOpenAISpecificParameters(caps)
		return openaiReq
	}
}

// serviceTierForModel filters a requested tier against the final target model's
// capabilities. Omitting an unsupported tier lets the provider use its default
// instead of returning an unsupported-tier error.
func serviceTierForModel(caps schemas.ModelCaps, tier *schemas.BifrostServiceTier) *schemas.BifrostServiceTier {
	if tier == nil {
		return nil
	}
	fallback := true
	if caps.Provider() == schemas.Bedrock || caps.Provider() == schemas.BedrockMantle {
		// Bedrock defaults to Standard when the field is omitted. Non-standard
		// tiers are model-specific, so unknown catalog state must fail closed.
		if *tier == schemas.BifrostServiceTierDefault || *tier == schemas.BifrostServiceTierAuto {
			return nil
		}
		fallback = false
	}
	if !caps.ServiceTierSupported(*tier, fallback) {
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
			req.ChatParameters.Reasoning.Effort = schemas.Ptr(normalizeReasoningEffort(req.Provider, caps, effort))
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
		// A model that always reasons rejects "none"; "minimal" normalizes to its lowest level.
		if e := req.ChatParameters.Reasoning.Effort; e != nil && *e == schemas.ReasoningEffortNone &&
			!caps.CanDisableReasoning(defaultCanDisableReasoning(caps.Model())) {
			req.ChatParameters.Reasoning.Effort = schemas.Ptr(caps.NormalizeReasoningEffort(schemas.ReasoningEffortMinimal, defaultEffortControl(req.Provider, caps.Model())))
		}
	}
}

// stripUnsupportedSamplingParams drops sampling fields OpenAI rejects at the request's reasoning effort.
func (req *OpenAIChatRequest) stripUnsupportedSamplingParams(caps schemas.ModelCaps) {
	effort := ""
	if req.ChatParameters.Reasoning != nil && req.ChatParameters.Reasoning.Effort != nil {
		effort = *req.ChatParameters.Reasoning.Effort
	}
	model := caps.Model()
	if samplingParamUnsupported(caps, schemas.FieldTopP, model, effort) {
		req.ChatParameters.TopP = nil
	}
	if samplingParamUnsupported(caps, schemas.FieldTemperature, model, effort) {
		req.ChatParameters.Temperature = nil
	}
	if samplingParamUnsupported(caps, schemas.FieldTopLogprobs, model, effort) {
		req.ChatParameters.TopLogProbs = nil
	}
	if samplingParamUnsupported(caps, schemas.FieldLogprobs, model, effort) {
		req.ChatParameters.LogProbs = nil
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

	// Mistral rejects unknown assistant fields with 422 extra_forbidden, reasoning_content included
	for i := range req.Messages {
		if assistantMessage := req.Messages[i].OpenAIChatAssistantMessage; assistantMessage != nil {
			assistantMessage.Reasoning = nil
		}
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

// downgradeJSONSchemaToJSONObject rewrites a json_schema response_format to
// json_object and carries the schema to the model as a system instruction.
//
// DeepSeek's chat endpoint rejects json_schema with HTTP 400 ("This response_format
// type is unavailable now") and accepts only json_object, which in turn requires the
// word "json" somewhere in the prompt. This is a property of the DeepSeek API surface,
// not of a model, so it is not gated on SupportsResponseSchema: the datasheet marks
// the DeepSeek rows supports_response_schema=true because they do support JSON mode.
//
// Only requests that carry json_schema pay for this. The caller's response_format and
// messages are never written: the format is replaced by pointer and the instruction is
// inserted into a new slice (Messages is already a fresh conversion of the input).
func (req *OpenAIChatRequest) downgradeJSONSchemaToJSONObject() {
	if req.ResponseFormat == nil {
		return
	}
	rf, ok := schemas.ParseChatResponseFormat(req.ResponseFormat)
	if !ok || rf.Type != "json_schema" {
		return
	}

	var jsonObject interface{} = json.RawMessage(`{"type":"json_object"}`)
	req.ResponseFormat = &jsonObject

	instruction := jsonSchemaInstruction(rf)
	// The instruction goes after any leading system/developer messages so the
	// client's own system prompt keeps its place at the top of the prompt.
	insertAt := 0
	for insertAt < len(req.Messages) &&
		(req.Messages[insertAt].Role == schemas.ChatMessageRoleSystem || req.Messages[insertAt].Role == schemas.ChatMessageRoleDeveloper) {
		insertAt++
	}
	messages := make([]OpenAIMessage, 0, len(req.Messages)+1)
	messages = append(messages, req.Messages[:insertAt]...)
	messages = append(messages, OpenAIMessage{
		Role:    schemas.ChatMessageRoleSystem,
		Content: &schemas.ChatMessageContent{ContentStr: &instruction},
	})
	messages = append(messages, req.Messages[insertAt:]...)
	req.Messages = messages
}

// jsonSchemaInstruction renders a json_schema response_format as a prompt
// instruction for providers that only offer JSON mode. It always contains the
// lowercase word "json", which DeepSeek's JSON mode requires in the prompt.
func jsonSchemaInstruction(rf *schemas.ChatResponseFormat) string {
	const lead = "Respond with only a valid json object, with no surrounding text or code fences"
	schema := rf.RawSchema()
	if len(schema) == 0 {
		return lead + "."
	}
	name, _ := rf.Name()
	description, _ := rf.Description()
	var sb strings.Builder
	sb.Grow(len(lead) + len(name) + len(description) + len(schema) + 80)
	sb.WriteString(lead)
	sb.WriteString(", that conforms to the following JSON Schema")
	if name != "" {
		sb.WriteString(" (")
		sb.WriteString(name)
		sb.WriteString(")")
	}
	sb.WriteString(".")
	if description != "" {
		sb.WriteString(" ")
		sb.WriteString(description)
	}
	sb.WriteString("\n\nJSON Schema:\n")
	sb.Write(schema)
	return sb.String()
}

// renameAssistantReasoningToAlias moves replayed assistant reasoning from
// reasoning_content to the "reasoning" key, for providers that only accept the latter.
func (req *OpenAIChatRequest) renameAssistantReasoningToAlias() {
	for i := range req.Messages {
		assistantMessage := req.Messages[i].OpenAIChatAssistantMessage
		if assistantMessage == nil || assistantMessage.Reasoning == nil {
			continue
		}
		if assistantMessage.ReasoningAlias == nil {
			assistantMessage.ReasoningAlias = assistantMessage.Reasoning
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

// chatUsesPromptCacheBreakpoints is the Chat Completions half of the gate behind
// responsesIsOpenAIFamilyBreakpointTarget: the OpenAI family on gpt-5.6 and later. OpenRouter
// is deliberately absent, since it accepts cache_control verbatim on this path.
func chatUsesPromptCacheBreakpoints(caps schemas.ModelCaps, provider schemas.ModelProvider, model string) bool {
	switch provider {
	case schemas.OpenAI, schemas.Azure, schemas.BedrockMantle, schemas.Bedrock:
		return caps.SupportsPromptCacheBreakpoint(schemas.ModelSupportsPromptCacheBreakpoint(model))
	default:
		return false
	}
}

type openRouterCacheRef struct {
	tool, msg, block, toolCall int
}

// clampOpenRouterCacheControl drops the earliest surplus markers so at most n remain
// (same trade-off as clampAnthropicCacheBreakpoints).
func clampOpenRouterCacheControl(req *OpenAIChatRequest, n int) {
	if req == nil {
		return
	}
	var buf [8]openRouterCacheRef // stays on the stack for the usual marker counts
	refs := buf[:0]
	for i := range req.ChatParameters.Tools {
		if req.ChatParameters.Tools[i].CacheControl != nil {
			refs = append(refs, openRouterCacheRef{tool: i, msg: -1, block: -1, toolCall: -1})
		}
	}
	for i := range req.Messages {
		msg := &req.Messages[i]
		if msg.Content != nil {
			for j := range msg.Content.ContentBlocks {
				if msg.Content.ContentBlocks[j].CacheControl != nil {
					refs = append(refs, openRouterCacheRef{tool: -1, msg: i, block: j, toolCall: -1})
				}
			}
		}
		if msg.OpenAIChatAssistantMessage != nil {
			for j := range msg.OpenAIChatAssistantMessage.ToolCalls {
				if msg.OpenAIChatAssistantMessage.ToolCalls[j].CacheControl != nil {
					refs = append(refs, openRouterCacheRef{tool: -1, msg: i, block: -1, toolCall: j})
				}
			}
		}
	}
	excess := len(refs) - n
	if excess <= 0 {
		return
	}

	toolsCopied := false
	contentCopied := map[int]bool{}
	assistantCopied := map[int]bool{}
	for _, ref := range refs[:excess] {
		switch {
		case ref.tool >= 0:
			if !toolsCopied {
				req.ChatParameters.Tools = slices.Clone(req.ChatParameters.Tools)
				toolsCopied = true
			}
			req.ChatParameters.Tools[ref.tool].CacheControl = nil
			req.ChatParameters.Tools[ref.tool].InvalidateSerialized()
		case ref.block >= 0:
			msg := &req.Messages[ref.msg]
			if !contentCopied[ref.msg] {
				contentCopy := *msg.Content
				contentCopy.ContentBlocks = slices.Clone(msg.Content.ContentBlocks)
				msg.Content = &contentCopy
				contentCopied[ref.msg] = true
			}
			msg.Content.ContentBlocks[ref.block].CacheControl = nil
		case ref.toolCall >= 0:
			msg := &req.Messages[ref.msg]
			if !assistantCopied[ref.msg] {
				assistantCopy := *msg.OpenAIChatAssistantMessage
				assistantCopy.ToolCalls = slices.Clone(msg.OpenAIChatAssistantMessage.ToolCalls)
				msg.OpenAIChatAssistantMessage = &assistantCopy
				assistantCopied[ref.msg] = true
			}
			msg.OpenAIChatAssistantMessage.ToolCalls[ref.toolCall].CacheControl = nil
		}
	}
}

// applyChatCacheBreakpoints rewrites ephemeral cache_control markers on text parts
// into prompt_cache_breakpoint, for every role including tool messages, which is where
// an agent loop's per-turn marker lands after a tool call. Only text parts are
// marked: that is the one Chat Completions part type with a verified breakpoint
// field, and a rejected field costs more than the miss it would fix. The same
// ceiling (promptCacheBreakpointCeiling) and keep-the-latest rule as the Responses
// path apply.
//
// Messages, their Content pointers and the part arrays alias bifrostReq.Input, which
// plugins and the fallback chain reuse, so everything touched is copied first.
func applyChatCacheBreakpoints(messages []OpenAIMessage, ceiling int) []OpenAIMessage {
	type blockRef struct{ msg, block int }
	var refs []blockRef
	existing := 0
	for i := range messages {
		if messages[i].Content == nil {
			continue
		}
		for j, block := range messages[i].Content.ContentBlocks {
			if block.PromptCacheBreakpoint != nil {
				existing++
				continue
			}
			if block.CacheControl == nil || block.CacheControl.Type != schemas.CacheControlTypeEphemeral ||
				block.Type != schemas.ChatContentBlockTypeText || block.Text == nil {
				continue
			}
			refs = append(refs, blockRef{msg: i, block: j})
		}
	}
	if len(refs) == 0 {
		return messages
	}
	budget := ceiling - existing
	if budget <= 0 {
		return messages
	}
	if len(refs) > budget {
		refs = refs[len(refs)-budget:]
	}

	out := make([]OpenAIMessage, len(messages))
	copy(out, messages)
	copied := make(map[int]bool, len(refs))
	for _, ref := range refs {
		if !copied[ref.msg] {
			contentCopy := *out[ref.msg].Content
			contentCopy.ContentBlocks = append(
				make([]schemas.ChatContentBlock, 0, len(contentCopy.ContentBlocks)), contentCopy.ContentBlocks...)
			out[ref.msg].Content = &contentCopy
			copied[ref.msg] = true
		}
		out[ref.msg].Content.ContentBlocks[ref.block].PromptCacheBreakpoint = &schemas.PromptCacheBreakpoint{
			Mode: schemas.Ptr(PromptCacheBreakpointModeExplicit),
		}
	}
	return out
}
