package schemas

import (
	"fmt"
	"strings"
)

// Attributes renders the typed span payload back into the flat attribute map the
// connectors read today. It exists so connectors can migrate to the typed record
// one at a time; TestAttributesShimMatchesLegacy pins it to the output of
// framework/tracing's Populate* functions.
//
// Renders only what the Populate* functions in framework/tracing emit. The
// governance dimensions, retry count and fallback index live on LLMSpanData for
// connectors to read, but are written onto the span by core/spanenrichment.go
// and core/bifrost.go, so rendering them here would double-write them.
//
// Delete this once every connector reads LLMSpanData directly.
func (d *LLMSpanData) Attributes() map[string]any {
	if d == nil {
		return map[string]any{}
	}
	attrs := make(map[string]any, 48)
	d.appendIdentity(attrs)
	d.appendFamily(attrs)
	d.appendResponseEnvelope(attrs)
	d.appendUsage(attrs)
	d.appendError(attrs)
	for k, v := range d.Extra {
		attrs[k] = v
	}
	return attrs
}

// ResponseAttributes renders only the keys that become known once the response
// settles. The request keys are already on the span from the earlier pass, and
// re-rendering them would marshal the input messages a second time.
func (d *LLMSpanData) ResponseAttributes() map[string]any {
	if d == nil {
		return map[string]any{}
	}
	attrs := make(map[string]any, 24)
	d.appendResponseEnvelope(attrs)
	d.appendUsage(attrs)
	d.appendError(attrs)
	return attrs
}

func (d *LLMSpanData) appendIdentity(attrs map[string]any) {
	attrs[AttrProviderName] = OTelProviderName(d.Provider)
	attrs[AttrBifrostProviderName] = string(d.Provider)
	attrs[AttrRequestModel] = d.RequestModel
	attrs[AttrOperationName] = OTelOperationName(d.RequestType)
	if d.Alias != "" {
		attrs[AttrBifrostAlias] = d.Alias
	}
}

func (d *LLMSpanData) appendFamily(attrs map[string]any) {
	if d.MessageCount > 0 {
		attrs[AttrMessageCount] = d.MessageCount
	}
	if len(d.InputMessages) > 0 {
		if data, err := MarshalString(d.InputMessages); err == nil {
			attrs[AttrInputMessages] = data
		}
	}
	switch {
	case d.Responses != nil:
		d.appendResponsesRequest(attrs)
	case d.TextCompletion != nil:
		d.appendTextCompletionRequest(attrs)
	case d.Embedding != nil || d.EmbeddingInput != nil:
		d.appendEmbeddingRequest(attrs)
	case d.Speech != nil || d.SpeechInput != nil:
		d.appendSpeechRequest(attrs)
	case d.Transcription != nil:
		d.appendTranscriptionRequest(attrs)
	}

	if p := d.Chat; p != nil {
		setIfNotNil(attrs, AttrMaxTokens, p.MaxCompletionTokens)
		setIfNotNil(attrs, AttrTemperature, p.Temperature)
		setIfNotNil(attrs, AttrTopP, p.TopP)
		if p.Stop != nil {
			attrs[AttrStopSequences] = append([]string(nil), p.Stop...)
			attrs[AttrBifrostStopSequencesJoined] = strings.Join(p.Stop, ",")
		}
		setIfNotNil(attrs, AttrPresencePenalty, p.PresencePenalty)
		setIfNotNil(attrs, AttrFrequencyPenalty, p.FrequencyPenalty)
		setIfNotNil(attrs, AttrParallelToolCall, p.ParallelToolCalls)
		setIfNotNil(attrs, AttrRequestUser, p.User)
	}
}

func (d *LLMSpanData) appendResponsesRequest(attrs map[string]any) {
	p := d.Responses
	setIfNotNil(attrs, AttrParallelToolCall, p.ParallelToolCalls)
	setIfNotNil(attrs, AttrPromptCacheKey, p.PromptCacheKey)
	if r := p.Reasoning; r != nil {
		setIfNotNil(attrs, AttrReasoningEffort, r.Effort)
		setIfNotNil(attrs, AttrReasoningSummary, r.Summary)
		setIfNotNil(attrs, AttrReasoningGenSummary, r.GenerateSummary)
	}
	setIfNotNil(attrs, AttrSafetyIdentifier, p.SafetyIdentifier)
	if p.ServiceTier != nil {
		attrs[AttrServiceTier] = string(*p.ServiceTier)
	}
	setIfNotNil(attrs, AttrStore, p.Store)
	setIfNotNil(attrs, AttrTemperature, p.Temperature)
	if t := p.Text; t != nil {
		setIfNotNil(attrs, AttrTextVerbosity, t.Verbosity)
		if t.Format != nil {
			attrs[AttrTextFormatType] = t.Format.Type
		}
	}
	setIfNotNil(attrs, AttrTopLogProbs, p.TopLogProbs)
	setIfNotNil(attrs, AttrTopP, p.TopP)
	if tc := p.ToolChoice; tc != nil {
		if tc.ResponsesToolChoiceStr != nil && *tc.ResponsesToolChoiceStr != "" {
			attrs[AttrToolChoiceType] = *tc.ResponsesToolChoiceStr
		}
		if tc.ResponsesToolChoiceStruct != nil && tc.ResponsesToolChoiceStruct.Name != nil {
			attrs[AttrToolChoiceName] = *tc.ResponsesToolChoiceStruct.Name
		}
	}
	if p.Tools != nil {
		if data, err := MarshalString(responsesToolSummaries(p.Tools)); err == nil {
			attrs[AttrTools] = data
		}
	}
	setIfNotNil(attrs, AttrTruncation, p.Truncation)
}

func (d *LLMSpanData) appendTextCompletionRequest(attrs map[string]any) {
	p := d.TextCompletion
	setIfNotNil(attrs, AttrMaxTokens, p.MaxTokens)
	setIfNotNil(attrs, AttrTemperature, p.Temperature)
	setIfNotNil(attrs, AttrTopP, p.TopP)
	if p.Stop != nil {
		attrs[AttrStopSequences] = append([]string(nil), p.Stop...)
		attrs[AttrBifrostStopSequencesJoined] = strings.Join(p.Stop, ",")
	}
	setIfNotNil(attrs, AttrPresencePenalty, p.PresencePenalty)
	setIfNotNil(attrs, AttrFrequencyPenalty, p.FrequencyPenalty)
	setIfNotNil(attrs, AttrBestOf, p.BestOf)
	setIfNotNil(attrs, AttrEcho, p.Echo)
	if p.LogitBias != nil {
		if data, err := MarshalString(p.LogitBias); err == nil {
			attrs[AttrLogitBias] = data
		}
	}
	setIfNotNil(attrs, AttrLogProbs, p.LogProbs)
	setIfNotNil(attrs, AttrChoiceCount, p.N)
	setIfNotNil(attrs, AttrSeed, p.Seed)
	setIfNotNil(attrs, AttrSuffix, p.Suffix)
	setIfNotNil(attrs, AttrRequestUser, p.User)

	if in := d.TextPrompt; in != nil {
		if in.PromptStr != nil {
			attrs[AttrInputText] = *in.PromptStr
		} else if in.PromptArray != nil {
			attrs[AttrInputText] = strings.Join(in.PromptArray, ",")
		}
	}
}

func (d *LLMSpanData) appendEmbeddingRequest(attrs map[string]any) {
	if p := d.Embedding; p != nil {
		setIfNotNil(attrs, AttrEmbeddingsDimensionCount, p.Dimensions)
		if p.EncodingFormat != nil {
			attrs[AttrEncodingFormats] = []string{*p.EncodingFormat}
		}
	}
	in := d.EmbeddingInput
	if in == nil {
		return
	}
	switch {
	case in.Text != nil:
		attrs[AttrInputText] = *in.Text
	case in.Texts != nil:
		attrs[AttrInputText] = strings.Join(in.Texts, ",")
	case in.Embedding != nil:
		parts := make([]string, len(in.Embedding))
		for i, v := range in.Embedding {
			parts[i] = fmt.Sprintf("%v", v)
		}
		attrs[AttrInputEmbedding] = strings.Join(parts, ",")
	}
}

func (d *LLMSpanData) appendSpeechRequest(attrs map[string]any) {
	if p := d.Speech; p != nil {
		if vc := p.VoiceConfig; vc != nil {
			setIfNotNil(attrs, AttrVoice, vc.Voice)
			if len(vc.MultiVoiceConfig) > 0 {
				voices := make([]string, len(vc.MultiVoiceConfig))
				for i, v := range vc.MultiVoiceConfig {
					voices[i] = v.Voice
				}
				attrs[AttrMultiVoiceConfig] = strings.Join(voices, ",")
			}
		}
		setIfNotEmpty(attrs, AttrInstructions, p.Instructions)
		setIfNotEmpty(attrs, AttrResponseFormat, p.ResponseFormat)
		setIfNotNil(attrs, AttrSpeed, p.Speed)
	}
	if in := d.SpeechInput; in != nil {
		setIfNotEmpty(attrs, AttrInputSpeech, in.Input)
	}
}

func (d *LLMSpanData) appendTranscriptionRequest(attrs map[string]any) {
	p := d.Transcription
	setIfNotNil(attrs, AttrLanguage, p.Language)
	setIfNotNil(attrs, AttrPrompt, p.Prompt)
	setIfNotNil(attrs, AttrResponseFormat, p.ResponseFormat)
	setIfNotNil(attrs, AttrFormat, p.Format)
}

// responsesToolSummaries projects tool definitions to the name/description pairs
// the span has always exported.
func responsesToolSummaries(tools []ResponsesTool) []ToolSummary {
	out := make([]ToolSummary, 0, len(tools))
	for _, tool := range tools {
		if tool.Name == nil {
			out = append(out, ToolSummary{Name: string(tool.Type)})
			continue
		}
		info := ToolSummary{Name: *tool.Name}
		if tool.Description != nil {
			info.Description = *tool.Description
		}
		out = append(out, info)
	}
	return out
}

// ToolSummary is the name/description pair exported for a tool definition.
type ToolSummary struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (d *LLMSpanData) appendResponseEnvelope(attrs map[string]any) {
	if !d.hasResponse() {
		return
	}
	switch d.RequestType {
	case ResponsesRequest, ResponsesStreamRequest:
		setIfNotEmpty(attrs, AttrResponseID, d.ResponseID)
		setIfNotEmpty(attrs, AttrResponseModel, d.ResponseModel)
		setIfNotEmpty(attrs, AttrServiceTier, d.ServiceTier)
		d.appendOutputMessagesJSON(attrs, AttrOutputMessages)
		d.appendResponsesEcho(attrs)

	case TextCompletionRequest, TextCompletionStreamRequest:
		attrs[AttrResponseID] = d.ResponseID
		attrs[AttrResponseModel] = d.ResponseModel
		setIfNotEmpty(attrs, AttrObject, d.Object)
		setIfNotEmpty(attrs, AttrSystemFprint, d.SystemFingerprint)
		if outputs := d.outputContents(); len(outputs) > 0 {
			attrs[AttrOutputMessages] = outputs
		}
		if len(d.FinishReasons) > 0 {
			attrs[AttrFinishReasons] = d.FinishReasons
		}

	case TranscriptionRequest, TranscriptionStreamRequest:
		if len(d.OutputMessages) > 0 {
			attrs[AttrOutputMessages] = d.OutputMessages[0].Content
		}

	case EmbeddingRequest, SpeechRequest, SpeechStreamRequest:
		// Usage only; these families emit no response envelope.

	default:
		attrs[AttrResponseID] = d.ResponseID
		attrs[AttrResponseModel] = d.ResponseModel
		setIfNotEmpty(attrs, AttrObject, d.Object)
		setIfNotEmpty(attrs, AttrSystemFprint, d.SystemFingerprint)
		attrs[AttrCreated] = int(d.Created)
		setIfNotEmpty(attrs, AttrServiceTier, d.ServiceTier)
		d.appendOutputMessagesJSON(attrs, AttrOutputMessages)
		if len(d.FinishReasons) > 0 {
			attrs[AttrFinishReasons] = d.FinishReasons
		}
	}
}

// hasResponse reports whether a response side was ever populated, so a
// request-only span does not emit empty envelope keys.
func (d *LLMSpanData) hasResponse() bool {
	return d.ResponseID != "" || d.ResponseModel != "" || len(d.OutputMessages) > 0 ||
		d.Created != 0 || d.ResponsesEcho != nil
}

func (d *LLMSpanData) appendOutputMessagesJSON(attrs map[string]any, key string) {
	if len(d.OutputMessages) == 0 {
		return
	}
	if data, err := MarshalString(d.OutputMessages); err == nil {
		attrs[key] = data
	}
}

// outputContents renders output as the plain string slice the text-completion
// family has always used.
func (d *LLMSpanData) outputContents() []string {
	if len(d.OutputMessages) == 0 {
		return nil
	}
	out := make([]string, 0, len(d.OutputMessages))
	for _, m := range d.OutputMessages {
		out = append(out, m.Content)
	}
	return out
}

// appendResponsesEcho emits the params the Responses API echoes on its response,
// under the gen_ai.response.* namespace.
func (d *LLMSpanData) appendResponsesEcho(attrs map[string]any) {
	p := d.ResponsesEcho
	if p == nil {
		return
	}
	if p.Include != nil {
		attrs[AttrRespInclude] = strings.Join(p.Include, ",")
	}
	setIfNotNil(attrs, AttrRespMaxOutputTokens, p.MaxOutputTokens)
	setIfNotNil(attrs, AttrRespMaxToolCalls, p.MaxToolCalls)
	if p.Metadata != nil {
		if data, err := MarshalString(p.Metadata); err == nil {
			attrs[AttrRespMetadata] = data
		}
	}
	setIfNotNil(attrs, AttrRespPreviousRespID, p.PreviousResponseID)
	setIfNotNil(attrs, AttrRespPromptCacheKey, p.PromptCacheKey)
	if r := p.Reasoning; r != nil {
		setIfNotNil(attrs, AttrRespReasoningText, r.Summary)
		setIfNotNil(attrs, AttrRespReasoningEffort, r.Effort)
		setIfNotNil(attrs, AttrRespReasoningGenSum, r.GenerateSummary)
	}
	setIfNotNil(attrs, AttrRespSafetyIdentifier, p.SafetyIdentifier)
	setIfNotNil(attrs, AttrRespStore, p.Store)
	setIfNotNil(attrs, AttrRespTemperature, p.Temperature)
	if t := p.Text; t != nil {
		setIfNotNil(attrs, AttrRespTextVerbosity, t.Verbosity)
		if t.Format != nil {
			attrs[AttrRespTextFormatType] = t.Format.Type
		}
	}
	setIfNotNil(attrs, AttrRespTopLogProbs, p.TopLogProbs)
	setIfNotNil(attrs, AttrRespTopP, p.TopP)
	if tc := p.ToolChoice; tc != nil {
		setIfNotNil(attrs, AttrRespToolChoiceType, tc.ResponsesToolChoiceStr)
		if tc.ResponsesToolChoiceStruct != nil {
			setIfNotNil(attrs, AttrRespToolChoiceName, tc.ResponsesToolChoiceStruct.Name)
		}
	}
	setIfNotNil(attrs, AttrRespTruncation, p.Truncation)
	if p.Tools != nil {
		if data, err := MarshalString(responsesToolSummaries(p.Tools)); err == nil {
			attrs[AttrRespTools] = data
		}
	}
}

// appendUsage renders usage under the key namespace the request family uses:
// prompt_token_details.* for chat, input_token_details.* for Responses. That
// split is the whole reason connectors hand-roll per-family token extraction.
func (d *LLMSpanData) appendUsage(attrs map[string]any) {
	u := d.Usage
	if u == nil {
		return
	}
	attrs[AttrTotalTokens] = u.TotalTokens
	attrs[AttrInputTokens] = u.PromptTokens
	attrs[AttrOutputTokens] = u.CompletionTokens

	responsesNS := usesInputTokenDetailsNamespace(d.RequestType)

	if pd := u.PromptTokensDetails; pd != nil {
		textKey, audioKey, imageKey := AttrPromptTokenDetailsText, AttrPromptTokenDetailsAudio, AttrPromptTokenDetailsImage
		write5m, write1h := AttrPromptTokenDetailsCachedWrite5m, AttrPromptTokenDetailsCachedWrite1h
		if responsesNS {
			textKey, audioKey, imageKey = AttrInputTokenDetailsText, AttrInputTokenDetailsAudio, AttrInputTokenDetailsImage
			write5m, write1h = AttrInputTokenDetailsCachedWrite5m, AttrInputTokenDetailsCachedWrite1h
		}
		setIfPositive(attrs, textKey, pd.TextTokens)
		setIfPositive(attrs, audioKey, pd.AudioTokens)
		setIfPositive(attrs, imageKey, pd.ImageTokens)
		setIfPositive(attrs, AttrUsageCacheReadInputTokens, pd.CachedReadTokens)
		setIfPositive(attrs, AttrUsageCacheCreationInputTokens, pd.CachedWriteTokens)
		if wd := pd.CachedWriteTokenDetails; wd != nil {
			setIfPositive(attrs, write5m, wd.CachedWriteTokens5m)
			setIfPositive(attrs, write1h, wd.CachedWriteTokens1h)
		}
	}

	if cd := u.CompletionTokensDetails; cd != nil {
		textKey, audioKey, imageKey := AttrCompletionTokenDetailsText, AttrCompletionTokenDetailsAudio, AttrCompletionTokenDetailsImage
		acceptKey, rejectKey := AttrCompletionTokenDetailsAccept, AttrCompletionTokenDetailsReject
		citeKey, searchKey := AttrCompletionTokenDetailsCite, AttrCompletionTokenDetailsSearch
		if responsesNS {
			textKey, audioKey, imageKey = AttrOutputTokenDetailsText, AttrOutputTokenDetailsAudio, AttrOutputTokenDetailsImage
			acceptKey, rejectKey = AttrOutputTokenDetailsAccept, AttrOutputTokenDetailsReject
			citeKey, searchKey = AttrOutputTokenDetailsCite, AttrOutputTokenDetailsSearch
		}
		setIfPositive(attrs, textKey, cd.TextTokens)
		setIfPositive(attrs, audioKey, cd.AudioTokens)
		setIfPositivePtr(attrs, imageKey, cd.ImageTokens)
		setIfPositive(attrs, AttrUsageReasoningOutputTokens, cd.ReasoningTokens)
		setIfPositive(attrs, acceptKey, cd.AcceptedPredictionTokens)
		setIfPositive(attrs, rejectKey, cd.RejectedPredictionTokens)
		setIfPositivePtr(attrs, citeKey, cd.CitationTokens)
		setIfPositivePtr(attrs, searchKey, cd.NumSearchQueries)
	}

	// Usage.Cost is the provider-reported cost; LLMSpanData.Cost is Bifrost's
	// computed one and supersedes it. Legacy emitted the provider value here and
	// the computed value later, last write winning — preserved.
	appendCostAttributes(attrs, u.Cost)
	appendCostAttributes(attrs, d.Cost)
}

func (d *LLMSpanData) appendError(attrs map[string]any) {
	e := d.Error
	if e == nil {
		return
	}
	if e.Detail != nil {
		attrs[AttrError] = e.Detail.Message
		setIfNotNil(attrs, AttrErrorTypeSpec, e.Detail.Type)
		setIfNotNil(attrs, AttrErrorCode, e.Detail.Code)
	}
	setIfNotNil(attrs, AttrHTTPResponseStatusCode, e.StatusCode)
	if e.Type != "" {
		attrs[AttrBifrostErrorType] = e.Type
	}
}

// usesInputTokenDetailsNamespace reports which of the two token-detail key
// namespaces a family emits. Responses and transcription use
// input_token_details.*; chat, text completion and embedding use
// prompt_token_details.*. The split is per family, not per API shape, which is
// why connectors reading one namespace silently miss the other.
func usesInputTokenDetailsNamespace(rt RequestType) bool {
	switch rt {
	case ResponsesRequest, ResponsesStreamRequest,
		TranscriptionRequest, TranscriptionStreamRequest:
		return true
	}
	return false
}

func setIfNotEmpty(attrs map[string]any, key, value string) {
	if value != "" {
		attrs[key] = value
	}
}

func setIfNotEmptySlice(attrs map[string]any, key string, value []string) {
	if len(value) > 0 {
		attrs[key] = value
	}
}

func setIfPositive(attrs map[string]any, key string, value int) {
	if value > 0 {
		attrs[key] = value
	}
}

func setIfPositivePtr(attrs map[string]any, key string, value *int) {
	if value != nil && *value > 0 {
		attrs[key] = *value
	}
}

func setIfNotNil[T any](attrs map[string]any, key string, value *T) {
	if value != nil {
		attrs[key] = *value
	}
}

// CostAttributes renders a cost breakdown as span attributes, for callers that
// price outside the record (the tracer's error path).
func CostAttributes(cost *BifrostCost) map[string]any {
	attrs := make(map[string]any, 8)
	appendCostAttributes(attrs, cost)
	return attrs
}

// appendCostAttributes renders a cost breakdown. The pricing engine produces the
// per-category split on the same call as the total, so emitting it costs nothing
// and lets connectors slice input vs output vs sidecar spend.
//
// Zero-valued categories are omitted: a request with no audio should carry no
// audio cost key rather than a zero, matching how token details are emitted.
func appendCostAttributes(attrs map[string]any, cost *BifrostCost) {
	if cost == nil {
		return
	}
	attrs[AttrUsageCost] = cost.TotalCost
	setIfNonZero(attrs, AttrBifrostCostInput, cost.InputCost)
	setIfNonZero(attrs, AttrBifrostCostOutput, cost.OutputCost)
	setIfNonZero(attrs, AttrBifrostCostAdditional, cost.AdditionalCost)

	if d := cost.InputCostDetails; d != nil {
		setIfNonZero(attrs, AttrBifrostCostInputText, d.TextCost)
		setIfNonZero(attrs, AttrBifrostCostInputAudio, d.AudioCost)
		setIfNonZero(attrs, AttrBifrostCostInputImage, d.ImageCost)
		setIfNonZero(attrs, AttrBifrostCostInputCachedRead, d.CachedReadCost)
		setIfNonZero(attrs, AttrBifrostCostInputCachedWrite, d.CachedWriteCost)
		setIfNonZero(attrs, AttrBifrostCostInputRequest, d.RequestCost)
	}
	if d := cost.OutputCostDetails; d != nil {
		setIfNonZero(attrs, AttrBifrostCostOutputText, d.TextCost)
		setIfNonZero(attrs, AttrBifrostCostOutputAudio, d.AudioCost)
		setIfNonZero(attrs, AttrBifrostCostOutputImage, d.ImageCost)
		setIfNonZero(attrs, AttrBifrostCostOutputReasoning, d.ReasoningCost)
		setIfNonZero(attrs, AttrBifrostCostOutputCitation, d.CitationCost)
		setIfNonZero(attrs, AttrBifrostCostOutputSearch, d.SearchQueriesCost)
	}
	if d := cost.AdditionalCostDetails; d != nil {
		setIfNonZero(attrs, AttrBifrostCostGuardrail, d.GuardrailCost)
		setIfNonZero(attrs, AttrBifrostCostMCP, d.MCPCost)
		setIfNonZero(attrs, AttrBifrostCostSemanticCache, d.SemanticCacheCost)
		setIfNonZero(attrs, AttrBifrostCostRouting, d.RoutingCost)
	}
}

func setIfNonZero(attrs map[string]any, key string, value float64) {
	if value != 0 {
		attrs[key] = value
	}
}
