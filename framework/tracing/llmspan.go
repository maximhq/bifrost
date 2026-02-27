// Package tracing provides distributed tracing utilities for Bifrost.
package tracing

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// PopulateRequestAttributes extracts common request attributes from a BifrostRequest.
// This is the main entry point for populating request attributes on a span.
func PopulateRequestAttributes(req *schemas.BifrostRequest) map[string]any {
	attrs := make(map[string]any)
	if req == nil {
		return attrs
	}

	provider, model, _ := req.GetRequestFields()
	attrs[schemas.AttrProviderName] = string(provider)
	attrs[schemas.AttrRequestModel] = model

	switch req.RequestType {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		PopulateChatRequestAttributes(req.ChatRequest, attrs)
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		PopulateTextCompletionRequestAttributes(req.TextCompletionRequest, attrs)
	case schemas.EmbeddingRequest:
		PopulateEmbeddingRequestAttributes(req.EmbeddingRequest, attrs)
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		PopulateTranscriptionRequestAttributes(req.TranscriptionRequest, attrs)
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		PopulateSpeechRequestAttributes(req.SpeechRequest, attrs)
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		PopulateResponsesRequestAttributes(req.ResponsesRequest, attrs)
	case schemas.BatchCreateRequest:
		PopulateBatchCreateRequestAttributes(req.BatchCreateRequest, attrs)
	case schemas.BatchListRequest:
		PopulateBatchListRequestAttributes(req.BatchListRequest, attrs)
	case schemas.BatchRetrieveRequest:
		PopulateBatchRetrieveRequestAttributes(req.BatchRetrieveRequest, attrs)
	case schemas.BatchCancelRequest:
		PopulateBatchCancelRequestAttributes(req.BatchCancelRequest, attrs)
	case schemas.BatchResultsRequest:
		PopulateBatchResultsRequestAttributes(req.BatchResultsRequest, attrs)
	case schemas.FileUploadRequest:
		PopulateFileUploadRequestAttributes(req.FileUploadRequest, attrs)
	case schemas.FileListRequest:
		PopulateFileListRequestAttributes(req.FileListRequest, attrs)
	case schemas.FileRetrieveRequest:
		PopulateFileRetrieveRequestAttributes(req.FileRetrieveRequest, attrs)
	case schemas.FileDeleteRequest:
		PopulateFileDeleteRequestAttributes(req.FileDeleteRequest, attrs)
	case schemas.FileContentRequest:
		PopulateFileContentRequestAttributes(req.FileContentRequest, attrs)
	}

	return attrs
}

// PopulateResponseAttributes extracts common response attributes from a BifrostResponse.
// This is the main entry point for populating response attributes on a span.
func PopulateResponseAttributes(resp *schemas.BifrostResponse) map[string]any {
	attrs := make(map[string]any)
	if resp == nil {
		return attrs
	}

	switch {
	case resp.ChatResponse != nil:
		PopulateChatResponseAttributes(resp.ChatResponse, attrs)
	case resp.TextCompletionResponse != nil:
		PopulateTextCompletionResponseAttributes(resp.TextCompletionResponse, attrs)
	case resp.EmbeddingResponse != nil:
		PopulateEmbeddingResponseAttributes(resp.EmbeddingResponse, attrs)
	case resp.TranscriptionResponse != nil:
		PopulateTranscriptionResponseAttributes(resp.TranscriptionResponse, attrs)
	case resp.SpeechResponse != nil:
		PopulateSpeechResponseAttributes(resp.SpeechResponse, attrs)
	case resp.ResponsesResponse != nil:
		PopulateResponsesResponseAttributes(resp.ResponsesResponse, attrs)
	case resp.BatchCreateResponse != nil:
		PopulateBatchCreateResponseAttributes(resp.BatchCreateResponse, attrs)
	case resp.BatchListResponse != nil:
		PopulateBatchListResponseAttributes(resp.BatchListResponse, attrs)
	case resp.BatchRetrieveResponse != nil:
		PopulateBatchRetrieveResponseAttributes(resp.BatchRetrieveResponse, attrs)
	case resp.BatchCancelResponse != nil:
		PopulateBatchCancelResponseAttributes(resp.BatchCancelResponse, attrs)
	case resp.BatchResultsResponse != nil:
		PopulateBatchResultsResponseAttributes(resp.BatchResultsResponse, attrs)
	case resp.FileUploadResponse != nil:
		PopulateFileUploadResponseAttributes(resp.FileUploadResponse, attrs)
	case resp.FileListResponse != nil:
		PopulateFileListResponseAttributes(resp.FileListResponse, attrs)
	case resp.FileRetrieveResponse != nil:
		PopulateFileRetrieveResponseAttributes(resp.FileRetrieveResponse, attrs)
	case resp.FileDeleteResponse != nil:
		PopulateFileDeleteResponseAttributes(resp.FileDeleteResponse, attrs)
	case resp.FileContentResponse != nil:
		PopulateFileContentResponseAttributes(resp.FileContentResponse, attrs)
	}

	return attrs
}

// PopulateErrorAttributes extracts error attributes from a BifrostError.
func PopulateErrorAttributes(err *schemas.BifrostError) map[string]any {
	attrs := make(map[string]any)
	if err == nil || err.Error == nil {
		return attrs
	}

	attrs[schemas.AttrError] = err.Error.Message
	if err.Error.Type != nil {
		attrs[schemas.AttrErrorType] = *err.Error.Type
	}
	if err.Error.Code != nil {
		attrs[schemas.AttrErrorCode] = *err.Error.Code
	}

	return attrs
}

// PopulateContextAttributes extracts context-related attributes (virtual keys, retries, routing rules, etc.)
func PopulateContextAttributes(
	attrs map[string]any,
	virtualKeyID, virtualKeyName string,
	selectedKeyID, selectedKeyName string,
	routingRuleID, routingRuleName string,
	teamID, teamName string,
	customerID, customerName string,
	numberOfRetries, fallbackIndex int,
) {
	if virtualKeyID != "" {
		attrs[schemas.AttrVirtualKeyID] = virtualKeyID
		attrs[schemas.AttrVirtualKeyName] = virtualKeyName
	}
	if selectedKeyID != "" {
		attrs[schemas.AttrSelectedKeyID] = selectedKeyID
		attrs[schemas.AttrSelectedKeyName] = selectedKeyName
	}
	if routingRuleID != "" {
		attrs[schemas.AttrRoutingRuleID] = routingRuleID
		attrs[schemas.AttrRoutingRuleName] = routingRuleName
	}
	if teamID != "" {
		attrs[schemas.AttrTeamID] = teamID
		attrs[schemas.AttrTeamName] = teamName
	}
	if customerID != "" {
		attrs[schemas.AttrCustomerID] = customerID
		attrs[schemas.AttrCustomerName] = customerName
	}
	attrs[schemas.AttrNumberOfRetries] = numberOfRetries
	attrs[schemas.AttrFallbackIndex] = fallbackIndex
}

// ===============================================
// Chat Completion Request/Response
// ===============================================

// PopulateChatRequestAttributes extracts chat completion request attributes.
func PopulateChatRequestAttributes(req *schemas.BifrostChatRequest, attrs map[string]any) {
	if req == nil {
		return
	}

	if req.Params != nil {
		if req.Params.MaxCompletionTokens != nil {
			attrs[schemas.AttrMaxTokens] = *req.Params.MaxCompletionTokens
		}
		if req.Params.Temperature != nil {
			attrs[schemas.AttrTemperature] = *req.Params.Temperature
		}
		if req.Params.TopP != nil {
			attrs[schemas.AttrTopP] = *req.Params.TopP
		}
		if req.Params.Stop != nil {
			attrs[schemas.AttrStopSequences] = strings.Join(req.Params.Stop, ",")
		}
		if req.Params.PresencePenalty != nil {
			attrs[schemas.AttrPresencePenalty] = *req.Params.PresencePenalty
		}
		if req.Params.FrequencyPenalty != nil {
			attrs[schemas.AttrFrequencyPenalty] = *req.Params.FrequencyPenalty
		}
		if req.Params.ParallelToolCalls != nil {
			attrs[schemas.AttrParallelToolCall] = *req.Params.ParallelToolCalls
		}
		if req.Params.User != nil {
			attrs[schemas.AttrRequestUser] = *req.Params.User
		}
		// ExtraParams
		for k, v := range req.Params.ExtraParams {
			attrs[k] = fmt.Sprintf("%v", v)
		}
	}

	// Extract input messages
	if req.Input != nil {
		attrs[schemas.AttrMessageCount] = len(req.Input)
		messages := extractChatMessages(req.Input)
		if len(messages) > 0 {
			attrs[schemas.AttrInputMessages] = messages
		}
	}
}

// PopulateChatResponseAttributes extracts chat completion response attributes.
func PopulateChatResponseAttributes(resp *schemas.BifrostChatResponse, attrs map[string]any) {
	if resp == nil {
		return
	}

	attrs[schemas.AttrResponseID] = resp.ID
	attrs[schemas.AttrResponseModel] = resp.Model
	if resp.Object != "" {
		attrs[schemas.AttrObject] = resp.Object
	}
	if resp.SystemFingerprint != "" {
		attrs[schemas.AttrSystemFprint] = resp.SystemFingerprint
	}
	attrs[schemas.AttrCreated] = resp.Created
	if resp.ServiceTier != nil {
		attrs[schemas.AttrServiceTier] = *resp.ServiceTier
	}

	// Extract output messages
	outputMessages := extractChatResponseMessages(resp)
	if len(outputMessages) > 0 {
		attrs[schemas.AttrOutputMessages] = outputMessages
	}

	// Extract finish reason from first choice
	if len(resp.Choices) > 0 && resp.Choices[0].FinishReason != nil {
		attrs[schemas.AttrFinishReason] = *resp.Choices[0].FinishReason
	}

	// Usage
	if resp.Usage != nil {
		attrs[schemas.AttrPromptTokens] = resp.Usage.PromptTokens
		attrs[schemas.AttrCompletionTokens] = resp.Usage.CompletionTokens
		attrs[schemas.AttrTotalTokens] = resp.Usage.TotalTokens
	}
}

// ===============================================
// Text Completion Request/Response
// ===============================================

// PopulateTextCompletionRequestAttributes extracts text completion request attributes.
func PopulateTextCompletionRequestAttributes(req *schemas.BifrostTextCompletionRequest, attrs map[string]any) {
	if req == nil {
		return
	}

	if req.Params != nil {
		if req.Params.MaxTokens != nil {
			attrs[schemas.AttrMaxTokens] = *req.Params.MaxTokens
		}
		if req.Params.Temperature != nil {
			attrs[schemas.AttrTemperature] = *req.Params.Temperature
		}
		if req.Params.TopP != nil {
			attrs[schemas.AttrTopP] = *req.Params.TopP
		}
		if req.Params.Stop != nil {
			attrs[schemas.AttrStopSequences] = strings.Join(req.Params.Stop, ",")
		}
		if req.Params.PresencePenalty != nil {
			attrs[schemas.AttrPresencePenalty] = *req.Params.PresencePenalty
		}
		if req.Params.FrequencyPenalty != nil {
			attrs[schemas.AttrFrequencyPenalty] = *req.Params.FrequencyPenalty
		}
		if req.Params.BestOf != nil {
			attrs[schemas.AttrBestOf] = *req.Params.BestOf
		}
		if req.Params.Echo != nil {
			attrs[schemas.AttrEcho] = *req.Params.Echo
		}
		if req.Params.LogitBias != nil {
			attrs[schemas.AttrLogitBias] = fmt.Sprintf("%v", req.Params.LogitBias)
		}
		if req.Params.LogProbs != nil {
			attrs[schemas.AttrLogProbs] = *req.Params.LogProbs
		}
		if req.Params.N != nil {
			attrs[schemas.AttrN] = *req.Params.N
		}
		if req.Params.Seed != nil {
			attrs[schemas.AttrSeed] = *req.Params.Seed
		}
		if req.Params.Suffix != nil {
			attrs[schemas.AttrSuffix] = *req.Params.Suffix
		}
		if req.Params.User != nil {
			attrs[schemas.AttrRequestUser] = *req.Params.User
		}
		// ExtraParams
		for k, v := range req.Params.ExtraParams {
			attrs[k] = fmt.Sprintf("%v", v)
		}
	}

	// Extract input text
	if req.Input != nil {
		if req.Input.PromptStr != nil {
			attrs[schemas.AttrInputText] = *req.Input.PromptStr
		} else if req.Input.PromptArray != nil {
			attrs[schemas.AttrInputText] = strings.Join(req.Input.PromptArray, ",")
		}
	}
}

// PopulateTextCompletionResponseAttributes extracts text completion response attributes.
func PopulateTextCompletionResponseAttributes(resp *schemas.BifrostTextCompletionResponse, attrs map[string]any) {
	if resp == nil {
		return
	}

	attrs[schemas.AttrResponseID] = resp.ID
	attrs[schemas.AttrResponseModel] = resp.Model
	if resp.Object != "" {
		attrs[schemas.AttrObject] = resp.Object
	}
	if resp.SystemFingerprint != "" {
		attrs[schemas.AttrSystemFprint] = resp.SystemFingerprint
	}

	// Extract output text
	var outputs []string
	for _, choice := range resp.Choices {
		if choice.TextCompletionResponseChoice != nil && choice.TextCompletionResponseChoice.Text != nil {
			outputs = append(outputs, *choice.TextCompletionResponseChoice.Text)
		}
	}
	if len(outputs) > 0 {
		attrs[schemas.AttrOutputMessages] = outputs
	}

	// Usage
	if resp.Usage != nil {
		attrs[schemas.AttrPromptTokens] = resp.Usage.PromptTokens
		attrs[schemas.AttrCompletionTokens] = resp.Usage.CompletionTokens
		attrs[schemas.AttrTotalTokens] = resp.Usage.TotalTokens
	}
}

// ===============================================
// Embedding Request/Response
// ===============================================

// PopulateEmbeddingRequestAttributes extracts embedding request attributes.
func PopulateEmbeddingRequestAttributes(req *schemas.BifrostEmbeddingRequest, attrs map[string]any) {
	if req == nil {
		return
	}

	if req.Params != nil {
		if req.Params.Dimensions != nil {
			attrs[schemas.AttrDimensions] = *req.Params.Dimensions
		}
		if req.Params.EncodingFormat != nil {
			attrs[schemas.AttrEncodingFormat] = *req.Params.EncodingFormat
		}
		// ExtraParams
		for k, v := range req.Params.ExtraParams {
			attrs[k] = fmt.Sprintf("%v", v)
		}
	}

	// Extract input
	if req.Input != nil {
		if req.Input.Text != nil {
			attrs[schemas.AttrInputText] = *req.Input.Text
		} else if req.Input.Texts != nil {
			attrs[schemas.AttrInputText] = strings.Join(req.Input.Texts, ",")
		} else if req.Input.Embedding != nil {
			embedding := make([]string, len(req.Input.Embedding))
			for i, v := range req.Input.Embedding {
				// Use a float‑safe representation; adjust precision as needed.
				embedding[i] = fmt.Sprintf("%v", v)
			}
			attrs[schemas.AttrInputEmbedding] = strings.Join(embedding, ",")
		}
	}
}

// PopulateEmbeddingResponseAttributes extracts embedding response attributes.
func PopulateEmbeddingResponseAttributes(resp *schemas.BifrostEmbeddingResponse, attrs map[string]any) {
	if resp == nil {
		return
	}
	// Usage
	if resp.Usage != nil {
		attrs[schemas.AttrPromptTokens] = resp.Usage.PromptTokens
		attrs[schemas.AttrCompletionTokens] = resp.Usage.CompletionTokens
		attrs[schemas.AttrTotalTokens] = resp.Usage.TotalTokens
	}
}

// ===============================================
// Transcription Request/Response
// ===============================================

// PopulateTranscriptionRequestAttributes extracts transcription request attributes.
func PopulateTranscriptionRequestAttributes(req *schemas.BifrostTranscriptionRequest, attrs map[string]any) {
	if req == nil || req.Params == nil {
		return
	}

	if req.Params.Language != nil {
		attrs[schemas.AttrLanguage] = *req.Params.Language
	}
	if req.Params.Prompt != nil {
		attrs[schemas.AttrPrompt] = *req.Params.Prompt
	}
	if req.Params.ResponseFormat != nil {
		attrs[schemas.AttrResponseFormat] = *req.Params.ResponseFormat
	}
	if req.Params.Format != nil {
		attrs[schemas.AttrFormat] = *req.Params.Format
	}
}

// PopulateTranscriptionResponseAttributes extracts transcription response attributes.
func PopulateTranscriptionResponseAttributes(resp *schemas.BifrostTranscriptionResponse, attrs map[string]any) {
	if resp == nil {
		return
	}

	attrs[schemas.AttrOutputMessages] = resp.Text

	// Usage
	if resp.Usage != nil {
		if resp.Usage.InputTokens != nil {
			attrs[schemas.AttrInputTokens] = *resp.Usage.InputTokens
		}
		if resp.Usage.OutputTokens != nil {
			attrs[schemas.AttrOutputTokens] = *resp.Usage.OutputTokens
		}
		if resp.Usage.TotalTokens != nil {
			attrs[schemas.AttrTotalTokens] = *resp.Usage.TotalTokens
		}
		if resp.Usage.InputTokenDetails != nil {
			attrs[schemas.AttrInputTokenDetailsText] = resp.Usage.InputTokenDetails.TextTokens
			attrs[schemas.AttrInputTokenDetailsAudio] = resp.Usage.InputTokenDetails.AudioTokens
		}
	}
}

// ===============================================
// Speech Request/Response
// ===============================================

// PopulateSpeechRequestAttributes extracts speech request attributes.
func PopulateSpeechRequestAttributes(req *schemas.BifrostSpeechRequest, attrs map[string]any) {
	if req == nil {
		return
	}

	if req.Params != nil {
		if req.Params.VoiceConfig != nil {
			if req.Params.VoiceConfig.Voice != nil {
				attrs[schemas.AttrVoice] = *req.Params.VoiceConfig.Voice
			}
			if len(req.Params.VoiceConfig.MultiVoiceConfig) > 0 {
				voices := make([]string, len(req.Params.VoiceConfig.MultiVoiceConfig))
				for i, vc := range req.Params.VoiceConfig.MultiVoiceConfig {
					voices[i] = vc.Voice
				}
				attrs[schemas.AttrMultiVoiceConfig] = strings.Join(voices, ",")
			}
		}
		if req.Params.Instructions != "" {
			attrs[schemas.AttrInstructions] = req.Params.Instructions
		}
		if req.Params.ResponseFormat != "" {
			attrs[schemas.AttrResponseFormat] = req.Params.ResponseFormat
		}
		if req.Params.Speed != nil {
			attrs[schemas.AttrSpeed] = *req.Params.Speed
		}
	}

	if req.Input != nil && req.Input.Input != "" {
		attrs[schemas.AttrInputSpeech] = req.Input.Input
	}
}

// PopulateSpeechResponseAttributes extracts speech response attributes.
func PopulateSpeechResponseAttributes(resp *schemas.BifrostSpeechResponse, attrs map[string]any) {
	if resp == nil {
		return
	}

	// Usage
	if resp.Usage != nil {
		attrs[schemas.AttrInputTokens] = resp.Usage.InputTokens
		attrs[schemas.AttrOutputTokens] = resp.Usage.OutputTokens
		attrs[schemas.AttrTotalTokens] = resp.Usage.TotalTokens
	}
}

// ===============================================
// Responses API Request/Response
// ===============================================

// PopulateResponsesRequestAttributes extracts responses API request attributes.
func PopulateResponsesRequestAttributes(req *schemas.BifrostResponsesRequest, attrs map[string]any) {
	if req == nil || req.Params == nil {
		return
	}

	if req.Params.ParallelToolCalls != nil {
		attrs[schemas.AttrParallelToolCall] = *req.Params.ParallelToolCalls
	}
	if req.Params.PromptCacheKey != nil {
		attrs[schemas.AttrPromptCacheKey] = *req.Params.PromptCacheKey
	}
	if req.Params.Reasoning != nil {
		if req.Params.Reasoning.Effort != nil {
			attrs[schemas.AttrReasoningEffort] = *req.Params.Reasoning.Effort
		}
		if req.Params.Reasoning.Summary != nil {
			attrs[schemas.AttrReasoningSummary] = *req.Params.Reasoning.Summary
		}
		if req.Params.Reasoning.GenerateSummary != nil {
			attrs[schemas.AttrReasoningGenSummary] = *req.Params.Reasoning.GenerateSummary
		}
	}
	if req.Params.SafetyIdentifier != nil {
		attrs[schemas.AttrSafetyIdentifier] = *req.Params.SafetyIdentifier
	}
	if req.Params.ServiceTier != nil {
		attrs[schemas.AttrServiceTier] = *req.Params.ServiceTier
	}
	if req.Params.Store != nil {
		attrs[schemas.AttrStore] = *req.Params.Store
	}
	if req.Params.Temperature != nil {
		attrs[schemas.AttrTemperature] = *req.Params.Temperature
	}
	if req.Params.Text != nil {
		if req.Params.Text.Verbosity != nil {
			attrs[schemas.AttrTextVerbosity] = *req.Params.Text.Verbosity
		}
		if req.Params.Text.Format != nil {
			attrs[schemas.AttrTextFormatType] = req.Params.Text.Format.Type
		}
	}
	if req.Params.TopLogProbs != nil {
		attrs[schemas.AttrTopLogProbs] = *req.Params.TopLogProbs
	}
	if req.Params.TopP != nil {
		attrs[schemas.AttrTopP] = *req.Params.TopP
	}
	if req.Params.ToolChoice != nil {
		if req.Params.ToolChoice.ResponsesToolChoiceStr != nil && *req.Params.ToolChoice.ResponsesToolChoiceStr != "" {
			attrs[schemas.AttrToolChoiceType] = *req.Params.ToolChoice.ResponsesToolChoiceStr
		}
		if req.Params.ToolChoice.ResponsesToolChoiceStruct != nil && req.Params.ToolChoice.ResponsesToolChoiceStruct.Name != nil {
			attrs[schemas.AttrToolChoiceName] = *req.Params.ToolChoice.ResponsesToolChoiceStruct.Name
		}
	}
	if req.Params.Tools != nil {
		tools := make([]string, len(req.Params.Tools))
		for i, tool := range req.Params.Tools {
			tools[i] = string(tool.Type)
		}
		attrs[schemas.AttrTools] = strings.Join(tools, ",")
	}
	if req.Params.Truncation != nil {
		attrs[schemas.AttrTruncation] = *req.Params.Truncation
	}
	// ExtraParams
	for k, v := range req.Params.ExtraParams {
		attrs[k] = fmt.Sprintf("%v", v)
	}
}

// PopulateResponsesResponseAttributes extracts responses API response attributes.
func PopulateResponsesResponseAttributes(resp *schemas.BifrostResponsesResponse, attrs map[string]any) {
	if resp == nil {
		return
	}

	if resp.ID != nil && *resp.ID != "" {
		attrs[schemas.AttrResponseID] = *resp.ID
	}
	if resp.Model != "" {
		attrs[schemas.AttrResponseModel] = resp.Model
	}
	if resp.ServiceTier != nil {
		attrs[schemas.AttrServiceTier] = *resp.ServiceTier
	}

	// Extract output messages (includes reasoning)
	outputMessages := extractResponsesOutputMessages(resp)
	if len(outputMessages) > 0 {
		attrs[schemas.AttrOutputMessages] = outputMessages
	}

	// Additional response fields
	if resp.Include != nil {
		attrs[schemas.AttrRespInclude] = strings.Join(resp.Include, ",")
	}
	if resp.MaxOutputTokens != nil {
		attrs[schemas.AttrRespMaxOutputTokens] = *resp.MaxOutputTokens
	}
	if resp.MaxToolCalls != nil {
		attrs[schemas.AttrRespMaxToolCalls] = *resp.MaxToolCalls
	}
	if resp.Metadata != nil {
		attrs[schemas.AttrRespMetadata] = fmt.Sprintf("%v", resp.Metadata)
	}
	if resp.PreviousResponseID != nil {
		attrs[schemas.AttrRespPreviousRespID] = *resp.PreviousResponseID
	}
	if resp.PromptCacheKey != nil {
		attrs[schemas.AttrRespPromptCacheKey] = *resp.PromptCacheKey
	}
	if resp.Reasoning != nil {
		if resp.Reasoning.Summary != nil {
			attrs[schemas.AttrRespReasoningText] = *resp.Reasoning.Summary
		}
		if resp.Reasoning.Effort != nil {
			attrs[schemas.AttrRespReasoningEffort] = *resp.Reasoning.Effort
		}
		if resp.Reasoning.GenerateSummary != nil {
			attrs[schemas.AttrRespReasoningGenSum] = *resp.Reasoning.GenerateSummary
		}
	}
	if resp.SafetyIdentifier != nil {
		attrs[schemas.AttrRespSafetyIdentifier] = *resp.SafetyIdentifier
	}
	if resp.Store != nil {
		attrs[schemas.AttrRespStore] = *resp.Store
	}
	if resp.Temperature != nil {
		attrs[schemas.AttrRespTemperature] = *resp.Temperature
	}
	if resp.Text != nil {
		if resp.Text.Verbosity != nil {
			attrs[schemas.AttrRespTextVerbosity] = *resp.Text.Verbosity
		}
		if resp.Text.Format != nil {
			attrs[schemas.AttrRespTextFormatType] = resp.Text.Format.Type
		}
	}
	if resp.TopLogProbs != nil {
		attrs[schemas.AttrRespTopLogProbs] = *resp.TopLogProbs
	}
	if resp.TopP != nil {
		attrs[schemas.AttrRespTopP] = *resp.TopP
	}
	if resp.ToolChoice != nil {
		if resp.ToolChoice.ResponsesToolChoiceStr != nil {
			attrs[schemas.AttrRespToolChoiceType] = *resp.ToolChoice.ResponsesToolChoiceStr
		}
		if resp.ToolChoice.ResponsesToolChoiceStruct != nil && resp.ToolChoice.ResponsesToolChoiceStruct.Name != nil {
			attrs[schemas.AttrRespToolChoiceName] = *resp.ToolChoice.ResponsesToolChoiceStruct.Name
		}
	}
	if resp.Truncation != nil {
		attrs[schemas.AttrRespTruncation] = *resp.Truncation
	}
	if resp.Tools != nil {
		tools := make([]string, len(resp.Tools))
		for i, tool := range resp.Tools {
			tools[i] = string(tool.Type)
		}
		attrs[schemas.AttrRespTools] = strings.Join(tools, ",")
	}

	// Usage
	if resp.Usage != nil {
		attrs[schemas.AttrInputTokens] = resp.Usage.InputTokens
		attrs[schemas.AttrOutputTokens] = resp.Usage.OutputTokens
		attrs[schemas.AttrTotalTokens] = resp.Usage.TotalTokens
	}
}

// ===============================================
// Batch Operations Request/Response
// ===============================================

// PopulateBatchCreateRequestAttributes extracts batch create request attributes.
func PopulateBatchCreateRequestAttributes(req *schemas.BifrostBatchCreateRequest, attrs map[string]any) {
	if req == nil {
		return
	}

	if req.InputFileID != "" {
		attrs[schemas.AttrBatchInputFileID] = req.InputFileID
	}
	if req.Endpoint != "" {
		attrs[schemas.AttrBatchEndpoint] = string(req.Endpoint)
	}
	if req.CompletionWindow != "" {
		attrs[schemas.AttrBatchCompletionWin] = req.CompletionWindow
	}
	if len(req.Requests) > 0 {
		attrs[schemas.AttrBatchRequestsCount] = len(req.Requests)
	}
	if len(req.Metadata) > 0 {
		attrs[schemas.AttrBatchMetadata] = fmt.Sprintf("%v", req.Metadata)
	}
	// ExtraParams
	for k, v := range req.ExtraParams {
		attrs[k] = fmt.Sprintf("%v", v)
	}
}

// PopulateBatchListRequestAttributes extracts batch list request attributes.
func PopulateBatchListRequestAttributes(req *schemas.BifrostBatchListRequest, attrs map[string]any) {
	if req == nil {
		return
	}

	if req.Limit > 0 {
		attrs[schemas.AttrBatchLimit] = req.Limit
	}
	if req.After != nil {
		attrs[schemas.AttrBatchAfter] = *req.After
	}
	if req.BeforeID != nil {
		attrs[schemas.AttrBatchBeforeID] = *req.BeforeID
	}
	if req.AfterID != nil {
		attrs[schemas.AttrBatchAfterID] = *req.AfterID
	}
	if req.PageToken != nil {
		attrs[schemas.AttrBatchPageToken] = *req.PageToken
	}
	if req.PageSize > 0 {
		attrs[schemas.AttrBatchPageSize] = req.PageSize
	}
	// ExtraParams
	for k, v := range req.ExtraParams {
		attrs[k] = fmt.Sprintf("%v", v)
	}
}

// PopulateBatchRetrieveRequestAttributes extracts batch retrieve request attributes.
func PopulateBatchRetrieveRequestAttributes(req *schemas.BifrostBatchRetrieveRequest, attrs map[string]any) {
	if req == nil {
		return
	}

	if req.BatchID != "" {
		attrs[schemas.AttrBatchID] = req.BatchID
	}
	// ExtraParams
	for k, v := range req.ExtraParams {
		attrs[k] = fmt.Sprintf("%v", v)
	}
}

// PopulateBatchCancelRequestAttributes extracts batch cancel request attributes.
func PopulateBatchCancelRequestAttributes(req *schemas.BifrostBatchCancelRequest, attrs map[string]any) {
	if req == nil {
		return
	}

	if req.BatchID != "" {
		attrs[schemas.AttrBatchID] = req.BatchID
	}
	// ExtraParams
	for k, v := range req.ExtraParams {
		attrs[k] = fmt.Sprintf("%v", v)
	}
}

// PopulateBatchResultsRequestAttributes extracts batch results request attributes.
func PopulateBatchResultsRequestAttributes(req *schemas.BifrostBatchResultsRequest, attrs map[string]any) {
	if req == nil {
		return
	}

	if req.BatchID != "" {
		attrs[schemas.AttrBatchID] = req.BatchID
	}
	// ExtraParams
	for k, v := range req.ExtraParams {
		attrs[k] = fmt.Sprintf("%v", v)
	}
}

// PopulateBatchCreateResponseAttributes extracts batch create response attributes.
func PopulateBatchCreateResponseAttributes(resp *schemas.BifrostBatchCreateResponse, attrs map[string]any) {
	if resp == nil {
		return
	}

	attrs[schemas.AttrBatchID] = resp.ID
	attrs[schemas.AttrBatchStatus] = string(resp.Status)
	if resp.Object != "" {
		attrs[schemas.AttrBatchObject] = resp.Object
	}
	if resp.Endpoint != "" {
		attrs[schemas.AttrBatchEndpoint] = resp.Endpoint
	}
	if resp.InputFileID != "" {
		attrs[schemas.AttrBatchInputFileID] = resp.InputFileID
	}
	if resp.CompletionWindow != "" {
		attrs[schemas.AttrBatchCompletionWin] = resp.CompletionWindow
	}
	if resp.CreatedAt != 0 {
		attrs[schemas.AttrBatchCreatedAt] = resp.CreatedAt
	}
	if resp.ExpiresAt != nil {
		attrs[schemas.AttrBatchExpiresAt] = *resp.ExpiresAt
	}
	if resp.OutputFileID != nil {
		attrs[schemas.AttrBatchOutputFileID] = *resp.OutputFileID
	}
	if resp.ErrorFileID != nil {
		attrs[schemas.AttrBatchErrorFileID] = *resp.ErrorFileID
	}
	attrs[schemas.AttrBatchCountTotal] = resp.RequestCounts.Total
	attrs[schemas.AttrBatchCountCompleted] = resp.RequestCounts.Completed
	attrs[schemas.AttrBatchCountFailed] = resp.RequestCounts.Failed
}

// PopulateBatchListResponseAttributes extracts batch list response attributes.
func PopulateBatchListResponseAttributes(resp *schemas.BifrostBatchListResponse, attrs map[string]any) {
	if resp == nil {
		return
	}

	if resp.Object != "" {
		attrs[schemas.AttrBatchObject] = resp.Object
	}
	attrs[schemas.AttrBatchDataCount] = len(resp.Data)
	attrs[schemas.AttrBatchHasMore] = resp.HasMore
	if resp.FirstID != nil {
		attrs[schemas.AttrBatchFirstID] = *resp.FirstID
	}
	if resp.LastID != nil {
		attrs[schemas.AttrBatchLastID] = *resp.LastID
	}
}

// PopulateBatchRetrieveResponseAttributes extracts batch retrieve response attributes.
func PopulateBatchRetrieveResponseAttributes(resp *schemas.BifrostBatchRetrieveResponse, attrs map[string]any) {
	if resp == nil {
		return
	}

	attrs[schemas.AttrBatchID] = resp.ID
	attrs[schemas.AttrBatchStatus] = string(resp.Status)
	if resp.Object != "" {
		attrs[schemas.AttrBatchObject] = resp.Object
	}
	if resp.Endpoint != "" {
		attrs[schemas.AttrBatchEndpoint] = resp.Endpoint
	}
	if resp.InputFileID != "" {
		attrs[schemas.AttrBatchInputFileID] = resp.InputFileID
	}
	if resp.CompletionWindow != "" {
		attrs[schemas.AttrBatchCompletionWin] = resp.CompletionWindow
	}
	if resp.CreatedAt != 0 {
		attrs[schemas.AttrBatchCreatedAt] = resp.CreatedAt
	}
	if resp.ExpiresAt != nil {
		attrs[schemas.AttrBatchExpiresAt] = *resp.ExpiresAt
	}
	if resp.InProgressAt != nil {
		attrs[schemas.AttrBatchInProgressAt] = *resp.InProgressAt
	}
	if resp.FinalizingAt != nil {
		attrs[schemas.AttrBatchFinalizingAt] = *resp.FinalizingAt
	}
	if resp.CompletedAt != nil {
		attrs[schemas.AttrBatchCompletedAt] = *resp.CompletedAt
	}
	if resp.FailedAt != nil {
		attrs[schemas.AttrBatchFailedAt] = *resp.FailedAt
	}
	if resp.ExpiredAt != nil {
		attrs[schemas.AttrBatchExpiredAt] = *resp.ExpiredAt
	}
	if resp.CancellingAt != nil {
		attrs[schemas.AttrBatchCancellingAt] = *resp.CancellingAt
	}
	if resp.CancelledAt != nil {
		attrs[schemas.AttrBatchCancelledAt] = *resp.CancelledAt
	}
	if resp.OutputFileID != nil {
		attrs[schemas.AttrBatchOutputFileID] = *resp.OutputFileID
	}
	if resp.ErrorFileID != nil {
		attrs[schemas.AttrBatchErrorFileID] = *resp.ErrorFileID
	}
	attrs[schemas.AttrBatchCountTotal] = resp.RequestCounts.Total
	attrs[schemas.AttrBatchCountCompleted] = resp.RequestCounts.Completed
	attrs[schemas.AttrBatchCountFailed] = resp.RequestCounts.Failed
}

// PopulateBatchCancelResponseAttributes extracts batch cancel response attributes.
func PopulateBatchCancelResponseAttributes(resp *schemas.BifrostBatchCancelResponse, attrs map[string]any) {
	if resp == nil {
		return
	}

	attrs[schemas.AttrBatchID] = resp.ID
	attrs[schemas.AttrBatchStatus] = string(resp.Status)
	if resp.Object != "" {
		attrs[schemas.AttrBatchObject] = resp.Object
	}
	if resp.CancellingAt != nil {
		attrs[schemas.AttrBatchCancellingAt] = *resp.CancellingAt
	}
	if resp.CancelledAt != nil {
		attrs[schemas.AttrBatchCancelledAt] = *resp.CancelledAt
	}
	attrs[schemas.AttrBatchCountTotal] = resp.RequestCounts.Total
	attrs[schemas.AttrBatchCountCompleted] = resp.RequestCounts.Completed
	attrs[schemas.AttrBatchCountFailed] = resp.RequestCounts.Failed
}

// PopulateBatchResultsResponseAttributes extracts batch results response attributes.
func PopulateBatchResultsResponseAttributes(resp *schemas.BifrostBatchResultsResponse, attrs map[string]any) {
	if resp == nil {
		return
	}

	attrs[schemas.AttrBatchID] = resp.BatchID
	attrs[schemas.AttrBatchResultsCount] = len(resp.Results)
	attrs[schemas.AttrBatchHasMore] = resp.HasMore
	if resp.NextCursor != nil {
		attrs[schemas.AttrBatchNextCursor] = *resp.NextCursor
	}
}

// ===============================================
// File Operations Request/Response
// ===============================================

// PopulateFileUploadRequestAttributes extracts file upload request attributes.
func PopulateFileUploadRequestAttributes(req *schemas.BifrostFileUploadRequest, attrs map[string]any) {
	if req == nil {
		return
	}

	if req.Filename != "" {
		attrs[schemas.AttrFileFilename] = req.Filename
	}
	if req.Purpose != "" {
		attrs[schemas.AttrFilePurpose] = string(req.Purpose)
	}
	if len(req.File) > 0 {
		attrs[schemas.AttrFileBytes] = len(req.File)
	}
	// ExtraParams
	for k, v := range req.ExtraParams {
		attrs[k] = fmt.Sprintf("%v", v)
	}
}

// PopulateFileListRequestAttributes extracts file list request attributes.
func PopulateFileListRequestAttributes(req *schemas.BifrostFileListRequest, attrs map[string]any) {
	if req == nil {
		return
	}

	if req.Purpose != "" {
		attrs[schemas.AttrFilePurpose] = string(req.Purpose)
	}
	if req.Limit > 0 {
		attrs[schemas.AttrFileLimit] = req.Limit
	}
	if req.After != nil {
		attrs[schemas.AttrFileAfter] = *req.After
	}
	if req.Order != nil {
		attrs[schemas.AttrFileOrder] = *req.Order
	}
	// ExtraParams
	for k, v := range req.ExtraParams {
		attrs[k] = fmt.Sprintf("%v", v)
	}
}

// PopulateFileRetrieveRequestAttributes extracts file retrieve request attributes.
func PopulateFileRetrieveRequestAttributes(req *schemas.BifrostFileRetrieveRequest, attrs map[string]any) {
	if req == nil {
		return
	}

	if req.FileID != "" {
		attrs[schemas.AttrFileID] = req.FileID
	}
	// ExtraParams
	for k, v := range req.ExtraParams {
		attrs[k] = fmt.Sprintf("%v", v)
	}
}

// PopulateFileDeleteRequestAttributes extracts file delete request attributes.
func PopulateFileDeleteRequestAttributes(req *schemas.BifrostFileDeleteRequest, attrs map[string]any) {
	if req == nil {
		return
	}

	if req.FileID != "" {
		attrs[schemas.AttrFileID] = req.FileID
	}
	// ExtraParams
	for k, v := range req.ExtraParams {
		attrs[k] = fmt.Sprintf("%v", v)
	}
}

// PopulateFileContentRequestAttributes extracts file content request attributes.
func PopulateFileContentRequestAttributes(req *schemas.BifrostFileContentRequest, attrs map[string]any) {
	if req == nil {
		return
	}

	if req.FileID != "" {
		attrs[schemas.AttrFileID] = req.FileID
	}
	// ExtraParams
	for k, v := range req.ExtraParams {
		attrs[k] = fmt.Sprintf("%v", v)
	}
}

// PopulateFileUploadResponseAttributes extracts file upload response attributes.
func PopulateFileUploadResponseAttributes(resp *schemas.BifrostFileUploadResponse, attrs map[string]any) {
	if resp == nil {
		return
	}

	attrs[schemas.AttrFileID] = resp.ID
	if resp.Object != "" {
		attrs[schemas.AttrFileObject] = resp.Object
	}
	attrs[schemas.AttrFileBytes] = resp.Bytes
	attrs[schemas.AttrFileCreatedAt] = resp.CreatedAt
	attrs[schemas.AttrFileFilename] = resp.Filename
	attrs[schemas.AttrFilePurpose] = string(resp.Purpose)
	if resp.Status != "" {
		attrs[schemas.AttrFileStatus] = string(resp.Status)
	}
	if resp.StorageBackend != "" {
		attrs[schemas.AttrFileStorageBackend] = string(resp.StorageBackend)
	}
}

// PopulateFileListResponseAttributes extracts file list response attributes.
func PopulateFileListResponseAttributes(resp *schemas.BifrostFileListResponse, attrs map[string]any) {
	if resp == nil {
		return
	}

	if resp.Object != "" {
		attrs[schemas.AttrFileObject] = resp.Object
	}
	attrs[schemas.AttrFileDataCount] = len(resp.Data)
	attrs[schemas.AttrFileHasMore] = resp.HasMore
}

// PopulateFileRetrieveResponseAttributes extracts file retrieve response attributes.
func PopulateFileRetrieveResponseAttributes(resp *schemas.BifrostFileRetrieveResponse, attrs map[string]any) {
	if resp == nil {
		return
	}

	attrs[schemas.AttrFileID] = resp.ID
	if resp.Object != "" {
		attrs[schemas.AttrFileObject] = resp.Object
	}
	attrs[schemas.AttrFileBytes] = resp.Bytes
	attrs[schemas.AttrFileCreatedAt] = resp.CreatedAt
	attrs[schemas.AttrFileFilename] = resp.Filename
	attrs[schemas.AttrFilePurpose] = string(resp.Purpose)
	if resp.Status != "" {
		attrs[schemas.AttrFileStatus] = string(resp.Status)
	}
	if resp.StorageBackend != "" {
		attrs[schemas.AttrFileStorageBackend] = string(resp.StorageBackend)
	}
}

// PopulateFileDeleteResponseAttributes extracts file delete response attributes.
func PopulateFileDeleteResponseAttributes(resp *schemas.BifrostFileDeleteResponse, attrs map[string]any) {
	if resp == nil {
		return
	}

	attrs[schemas.AttrFileID] = resp.ID
	if resp.Object != "" {
		attrs[schemas.AttrFileObject] = resp.Object
	}
	attrs[schemas.AttrFileDeleted] = resp.Deleted
}

// PopulateFileContentResponseAttributes extracts file content response attributes.
func PopulateFileContentResponseAttributes(resp *schemas.BifrostFileContentResponse, attrs map[string]any) {
	if resp == nil {
		return
	}

	attrs[schemas.AttrFileID] = resp.FileID
	if resp.ContentType != "" {
		attrs[schemas.AttrFileContentType] = resp.ContentType
	}
	if len(resp.Content) > 0 {
		attrs[schemas.AttrFileContentBytes] = len(resp.Content)
	}
}

// ===============================================
// Helper functions for extracting messages
// ===============================================

// MessageSummary represents a summarized chat message for tracing
type MessageSummary struct {
	Role             string                   `json:"role"`
	Content          string                   `json:"content"`
	ToolCalls        []ToolCallSummary        `json:"tool_calls,omitempty"`
	Reasoning        string                   `json:"reasoning,omitempty"`
	ReasoningDetails []ReasoningDetailSummary `json:"reasoning_details,omitempty"`
	Audio            *AudioSummary            `json:"audio,omitempty"`
	Refusal          string                   `json:"refusal,omitempty"`
}

// ToolCallSummary represents a summarized tool call for tracing
type ToolCallSummary struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
	Args string `json:"args,omitempty"`
}

// ReasoningDetailSummary represents a summarized reasoning detail for tracing
type ReasoningDetailSummary struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// AudioSummary represents summarized audio data for tracing
type AudioSummary struct {
	ID         string `json:"id,omitempty"`
	Transcript string `json:"transcript,omitempty"`
}

// extractChatMessages extracts chat messages into a slice of MessageSummary
func extractChatMessages(messages []schemas.ChatMessage) []MessageSummary {
	result := make([]MessageSummary, 0, len(messages))
	for _, msg := range messages {
		summary := extractMessageSummary(&msg)
		result = append(result, summary)
	}
	return result
}

// extractChatResponseMessages extracts output messages from chat response
func extractChatResponseMessages(resp *schemas.BifrostChatResponse) []MessageSummary {
	if resp == nil {
		return nil
	}

	result := make([]MessageSummary, 0, len(resp.Choices))
	for _, choice := range resp.Choices {
		if choice.ChatNonStreamResponseChoice == nil || choice.ChatNonStreamResponseChoice.Message == nil {
			continue
		}
		msg := choice.ChatNonStreamResponseChoice.Message
		summary := extractMessageSummary(msg)
		result = append(result, summary)
	}
	return result
}

// extractMessageSummary extracts a full MessageSummary from a ChatMessage
func extractMessageSummary(msg *schemas.ChatMessage) MessageSummary {
	if msg == nil {
		return MessageSummary{}
	}

	summary := MessageSummary{
		Role:    string(schemas.ChatMessageRoleAssistant),
		Content: extractMessageContent(msg.Content),
	}

	if msg.Role != "" {
		summary.Role = string(msg.Role)
	}

	// Extract assistant-specific fields
	if msg.ChatAssistantMessage != nil {
		am := msg.ChatAssistantMessage

		// Extract refusal
		if am.Refusal != nil && *am.Refusal != "" {
			summary.Refusal = *am.Refusal
		}

		// Extract reasoning
		if am.Reasoning != nil && *am.Reasoning != "" {
			summary.Reasoning = *am.Reasoning
		}

		// Extract reasoning details
		if len(am.ReasoningDetails) > 0 {
			summary.ReasoningDetails = make([]ReasoningDetailSummary, 0, len(am.ReasoningDetails))
			for _, rd := range am.ReasoningDetails {
				detail := ReasoningDetailSummary{
					Type: string(rd.Type),
				}
				if rd.Text != nil {
					detail.Text = *rd.Text
				}
				summary.ReasoningDetails = append(summary.ReasoningDetails, detail)
			}
		}

		// Extract audio
		if am.Audio != nil {
			summary.Audio = &AudioSummary{
				ID:         am.Audio.ID,
				Transcript: am.Audio.Transcript,
			}
		}

		// Extract tool calls
		if len(am.ToolCalls) > 0 {
			summary.ToolCalls = make([]ToolCallSummary, 0, len(am.ToolCalls))
			for _, tc := range am.ToolCalls {
				toolCall := ToolCallSummary{
					Type: "function",
				}
				if tc.ID != nil {
					toolCall.ID = *tc.ID
				}
				if tc.Type != nil {
					toolCall.Type = *tc.Type
				}
				if tc.Function.Name != nil {
					toolCall.Name = *tc.Function.Name
				}
				toolCall.Args = tc.Function.Arguments
				summary.ToolCalls = append(summary.ToolCalls, toolCall)
			}
		}
	}

	return summary
}

// ResponsesMessageSummary extends MessageSummary with reasoning
type ResponsesMessageSummary struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Reasoning string `json:"reasoning,omitempty"`
}

// extractResponsesOutputMessages extracts output messages from responses API
func extractResponsesOutputMessages(resp *schemas.BifrostResponsesResponse) []ResponsesMessageSummary {
	if resp == nil {
		return nil
	}

	result := make([]ResponsesMessageSummary, 0, len(resp.Output))
	for _, msg := range resp.Output {
		if msg.Role == nil {
			continue
		}
		content := ""
		if msg.Content != nil {
			if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
				content = *msg.Content.ContentStr
			} else if msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					if block.Text != nil {
						content += *block.Text
					}
				}
			}
		}
		// Extract reasoning text
		reasoning := ""
		if msg.ResponsesReasoning != nil && msg.ResponsesReasoning.Summary != nil {
			for _, block := range msg.ResponsesReasoning.Summary {
				if block.Text != "" {
					reasoning += block.Text
				}
			}
		}
		result = append(result, ResponsesMessageSummary{
			Role:      string(*msg.Role),
			Content:   content,
			Reasoning: reasoning,
		})
	}
	return result
}

// extractMessageContent extracts text content from ChatMessageContent
func extractMessageContent(content *schemas.ChatMessageContent) string {
	if content == nil {
		return ""
	}

	if content.ContentStr != nil {
		return *content.ContentStr
	}

	if content.ContentBlocks != nil {
		var builder strings.Builder
		for _, block := range content.ContentBlocks {
			if block.Text != nil {
				builder.WriteString(*block.Text)
			}
		}
		return builder.String()
	}

	return ""
}
