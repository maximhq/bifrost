package otel

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// kvStr creates a key-value pair with a string value
func kvStr(k, v string) *KeyValue {
	return &KeyValue{Key: k, Value: &AnyValue{Value: &StringValue{StringValue: v}}}
}

// kvInt creates a key-value pair with an integer value
func kvInt(k string, v int64) *KeyValue {
	return &KeyValue{Key: k, Value: &AnyValue{Value: &IntValue{IntValue: v}}}
}

// kvDbl creates a key-value pair with a double value
func kvDbl(k string, v float64) *KeyValue {
	return &KeyValue{Key: k, Value: &AnyValue{Value: &DoubleValue{DoubleValue: v}}}
}

// kvBool creates a key-value pair with a boolean value
func kvBool(k string, v bool) *KeyValue {
	return &KeyValue{Key: k, Value: &AnyValue{Value: &BoolValue{BoolValue: v}}}
}

// kvAny creates a key-value pair with an any value
func kvAny(k string, v *AnyValue) *KeyValue {
	return &KeyValue{Key: k, Value: v}
}

// arrValue converts a list of any values to an OpenTelemetry array value
func arrValue(vals ...*AnyValue) *AnyValue {
	return &AnyValue{Value: &ArrayValue{ArrayValue: &ArrayValueValue{Values: vals}}}
}

// listValue converts a list of key-value pairs to an OpenTelemetry list value
func listValue(kvs ...*KeyValue) *AnyValue {
	return &AnyValue{Value: &ListValue{KvlistValue: &KeyValueList{Values: kvs}}}
}

// hexToBytes converts a hex string to bytes, padding/truncating as needed
func hexToBytes(hexStr string, length int) []byte {
	// Remove any non-hex characters
	cleaned := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			return r
		}
		return -1
	}, hexStr)
	// Ensure even length
	if len(cleaned)%2 != 0 {
		cleaned = "0" + cleaned
	}
	// Truncate or pad to desired length
	if len(cleaned) > length*2 {
		cleaned = cleaned[:length*2]
	} else if len(cleaned) < length*2 {
		cleaned = strings.Repeat("0", length*2-len(cleaned)) + cleaned
	}
	bytes, _ := hex.DecodeString(cleaned)
	return bytes
}

// getSpeechRequestParams handles the speech request
func getSpeechRequestParams(req *schemas.BifrostSpeechRequest) []*KeyValue {
	params := []*KeyValue{}
	if req.Params != nil {
		if req.Params.VoiceConfig != nil {
			if req.Params.VoiceConfig.Voice != nil {
				params = append(params, kvStr("gen_ai.request.voice", *req.Params.VoiceConfig.Voice))
			}
			if len(req.Params.VoiceConfig.MultiVoiceConfig) > 0 {
				multiVoiceConfigParams := []*KeyValue{}
				for _, voiceConfig := range req.Params.VoiceConfig.MultiVoiceConfig {
					multiVoiceConfigParams = append(multiVoiceConfigParams, kvStr("gen_ai.request.voice", voiceConfig.Voice))
				}
				params = append(params, kvAny("gen_ai.request.multi_voice_config", arrValue(listValue(multiVoiceConfigParams...))))
			}
		}
		params = append(params, kvStr("gen_ai.request.instructions", req.Params.Instructions))
		params = append(params, kvStr("gen_ai.request.response_format", req.Params.ResponseFormat))
		if req.Params.Speed != nil {
			params = append(params, kvDbl("gen_ai.request.speed", *req.Params.Speed))
		}
	}
	if req.Input != nil {
		params = append(params, kvStr("gen_ai.input.speech", req.Input.Input))
	}
	return params
}

// getEmbeddingRequestParams handles the embedding request
func getEmbeddingRequestParams(req *schemas.BifrostEmbeddingRequest) []*KeyValue {
	params := []*KeyValue{}
	if req.Params != nil {
		if req.Params.Dimensions != nil {
			params = append(params, kvInt("gen_ai.request.dimensions", int64(*req.Params.Dimensions)))
		}
		if req.Params.ExtraParams != nil {
			for k, v := range req.Params.ExtraParams {
				params = append(params, kvStr(k, fmt.Sprintf("%v", v)))
			}
		}
		if req.Params.EncodingFormat != nil {
			params = append(params, kvStr("gen_ai.request.encoding_format", *req.Params.EncodingFormat))
		}
	}
	if req.Input.Text != nil {
		params = append(params, kvStr("gen_ai.input.text", *req.Input.Text))
	}
	if req.Input.Texts != nil {
		params = append(params, kvStr("gen_ai.input.text", strings.Join(req.Input.Texts, ",")))
	}
	if req.Input.Embedding != nil {
		embedding := make([]string, len(req.Input.Embedding))
		for i, v := range req.Input.Embedding {
			embedding[i] = fmt.Sprintf("%d", v)
		}
		params = append(params, kvStr("gen_ai.input.embedding", strings.Join(embedding, ",")))
	}
	return params
}

// getTextCompletionRequestParams handles the text completion request
func getTextCompletionRequestParams(req *schemas.BifrostTextCompletionRequest) []*KeyValue {
	params := []*KeyValue{}
	if req.Params != nil {
		if req.Params.MaxTokens != nil {
			params = append(params, kvInt("gen_ai.request.max_tokens", int64(*req.Params.MaxTokens)))
		}
		if req.Params.Temperature != nil {
			params = append(params, kvDbl("gen_ai.request.temperature", *req.Params.Temperature))
		}
		if req.Params.TopP != nil {
			params = append(params, kvDbl("gen_ai.request.top_p", *req.Params.TopP))
		}
		if req.Params.Stop != nil {
			params = append(params, kvStr("gen_ai.request.stop_sequences", strings.Join(req.Params.Stop, ",")))
		}
		if req.Params.PresencePenalty != nil {
			params = append(params, kvDbl("gen_ai.request.presence_penalty", *req.Params.PresencePenalty))
		}
		if req.Params.FrequencyPenalty != nil {
			params = append(params, kvDbl("gen_ai.request.frequency_penalty", *req.Params.FrequencyPenalty))
		}
		if req.Params.BestOf != nil {
			params = append(params, kvInt("gen_ai.request.best_of", int64(*req.Params.BestOf)))
		}
		if req.Params.Echo != nil {
			params = append(params, kvBool("gen_ai.request.echo", *req.Params.Echo))
		}
		if req.Params.LogitBias != nil {
			params = append(params, kvStr("gen_ai.request.logit_bias", fmt.Sprintf("%v", req.Params.LogitBias)))
		}
		if req.Params.LogProbs != nil {
			params = append(params, kvInt("gen_ai.request.logprobs", int64(*req.Params.LogProbs)))
		}
		if req.Params.N != nil {
			params = append(params, kvInt("gen_ai.request.n", int64(*req.Params.N)))
		}
		if req.Params.Seed != nil {
			params = append(params, kvInt("gen_ai.request.seed", int64(*req.Params.Seed)))
		}
		if req.Params.Suffix != nil {
			params = append(params, kvStr("gen_ai.request.suffix", *req.Params.Suffix))
		}
		if req.Params.User != nil {
			params = append(params, kvStr("gen_ai.request.user", *req.Params.User))
		}
		if req.Params.ExtraParams != nil {
			for k, v := range req.Params.ExtraParams {
				params = append(params, kvStr(k, fmt.Sprintf("%v", v)))
			}
		}
	}
	if req.Input.PromptStr != nil {
		params = append(params, kvStr("gen_ai.input.text", *req.Input.PromptStr))
	}
	if req.Input.PromptArray != nil {
		params = append(params, kvStr("gen_ai.input.text", strings.Join(req.Input.PromptArray, ",")))
	}
	return params
}

// getChatRequestParams handles the chat completion request
func getChatRequestParams(req *schemas.BifrostChatRequest) []*KeyValue {
	params := []*KeyValue{}
	if req.Params != nil {
		if req.Params.MaxCompletionTokens != nil {
			params = append(params, kvInt("gen_ai.request.max_tokens", int64(*req.Params.MaxCompletionTokens)))
		}
		if req.Params.Temperature != nil {
			params = append(params, kvDbl("gen_ai.request.temperature", *req.Params.Temperature))
		}
		if req.Params.TopP != nil {
			params = append(params, kvDbl("gen_ai.request.top_p", *req.Params.TopP))
		}
		if req.Params.Stop != nil {
			params = append(params, kvStr("gen_ai.request.stop_sequences", strings.Join(req.Params.Stop, ",")))
		}
		if req.Params.PresencePenalty != nil {
			params = append(params, kvDbl("gen_ai.request.presence_penalty", *req.Params.PresencePenalty))
		}
		if req.Params.FrequencyPenalty != nil {
			params = append(params, kvDbl("gen_ai.request.frequency_penalty", *req.Params.FrequencyPenalty))
		}
		if req.Params.ParallelToolCalls != nil {
			params = append(params, kvBool("gen_ai.request.parallel_tool_calls", *req.Params.ParallelToolCalls))
		}
		if req.Params.User != nil {
			params = append(params, kvStr("gen_ai.request.user", *req.Params.User))
		}
		if req.Params.ExtraParams != nil {
			for k, v := range req.Params.ExtraParams {
				params = append(params, kvStr(k, fmt.Sprintf("%v", v)))
			}
		}
	}
	// Handling chat completion
	if req.Input != nil {
		messages := []*AnyValue{}
		for _, message := range req.Input {
			if message.Content == nil {
				continue
			}
			switch message.Role {
			case schemas.ChatMessageRoleUser:
				kvs := []*KeyValue{kvStr("role", "user")}
				if message.Content.ContentStr != nil {
					kvs = append(kvs, kvStr("content", *message.Content.ContentStr))
				}
				messages = append(messages, listValue(kvs...))
			case schemas.ChatMessageRoleAssistant:
				kvs := []*KeyValue{kvStr("role", "assistant")}
				if message.Content.ContentStr != nil {
					kvs = append(kvs, kvStr("content", *message.Content.ContentStr))
				}
				messages = append(messages, listValue(kvs...))
			case schemas.ChatMessageRoleSystem:
				kvs := []*KeyValue{kvStr("role", "system")}
				if message.Content.ContentStr != nil {
					kvs = append(kvs, kvStr("content", *message.Content.ContentStr))
				}
				messages = append(messages, listValue(kvs...))
			case schemas.ChatMessageRoleTool:
				kvs := []*KeyValue{kvStr("role", "tool")}
				if message.Content.ContentStr != nil {
					kvs = append(kvs, kvStr("content", *message.Content.ContentStr))
				}
				messages = append(messages, listValue(kvs...))
			case schemas.ChatMessageRoleDeveloper:
				kvs := []*KeyValue{kvStr("role", "developer")}
				if message.Content.ContentStr != nil {
					kvs = append(kvs, kvStr("content", *message.Content.ContentStr))
				}
				messages = append(messages, listValue(kvs...))
			}
		}
		params = append(params, kvAny("gen_ai.input.messages", arrValue(messages...)))
	}
	return params
}

// getTranscriptionRequestParams handles the transcription request
func getTranscriptionRequestParams(req *schemas.BifrostTranscriptionRequest) []*KeyValue {
	params := []*KeyValue{}
	if req.Params != nil {
		if req.Params.Language != nil {
			params = append(params, kvStr("gen_ai.request.language", *req.Params.Language))
		}
		if req.Params.Prompt != nil {
			params = append(params, kvStr("gen_ai.request.prompt", *req.Params.Prompt))
		}
		if req.Params.ResponseFormat != nil {
			params = append(params, kvStr("gen_ai.request.response_format", *req.Params.ResponseFormat))
		}
		if req.Params.Format != nil {
			params = append(params, kvStr("gen_ai.request.format", *req.Params.Format))
		}
	}
	return params
}

// getResponsesRequestParams handles the responses request
func getResponsesRequestParams(req *schemas.BifrostResponsesRequest) []*KeyValue {
	params := []*KeyValue{}
	if req.Params != nil {
		if req.Params.ParallelToolCalls != nil {
			params = append(params, kvBool("gen_ai.request.parallel_tool_calls", *req.Params.ParallelToolCalls))
		}
		if req.Params.PromptCacheKey != nil {
			params = append(params, kvStr("gen_ai.request.prompt_cache_key", *req.Params.PromptCacheKey))
		}
		if req.Params.Reasoning != nil {
			if req.Params.Reasoning.Effort != nil {
				params = append(params, kvStr("gen_ai.request.reasoning_effort", *req.Params.Reasoning.Effort))
			}
			if req.Params.Reasoning.Summary != nil {
				params = append(params, kvStr("gen_ai.request.reasoning_summary", *req.Params.Reasoning.Summary))
			}
			if req.Params.Reasoning.GenerateSummary != nil {
				params = append(params, kvStr("gen_ai.request.reasoning_generate_summary", *req.Params.Reasoning.GenerateSummary))
			}
		}
		if req.Params.SafetyIdentifier != nil {
			params = append(params, kvStr("gen_ai.request.safety_identifier", *req.Params.SafetyIdentifier))
		}
		if req.Params.ServiceTier != nil {
			params = append(params, kvStr("gen_ai.request.service_tier", *req.Params.ServiceTier))
		}
		if req.Params.Store != nil {
			params = append(params, kvBool("gen_ai.request.store", *req.Params.Store))
		}
		if req.Params.Temperature != nil {
			params = append(params, kvDbl("gen_ai.request.temperature", *req.Params.Temperature))
		}
		if req.Params.Text != nil {
			if req.Params.Text.Verbosity != nil {
				params = append(params, kvStr("gen_ai.request.text", *req.Params.Text.Verbosity))
			}
			if req.Params.Text.Format != nil {
				params = append(params, kvStr("gen_ai.request.text_format_type", req.Params.Text.Format.Type))
			}

		}
		if req.Params.TopLogProbs != nil {
			params = append(params, kvInt("gen_ai.request.top_logprobs", int64(*req.Params.TopLogProbs)))
		}
		if req.Params.TopP != nil {
			params = append(params, kvDbl("gen_ai.request.top_p", *req.Params.TopP))
		}
		if req.Params.ToolChoice != nil {
			if req.Params.ToolChoice.ResponsesToolChoiceStr != nil && *req.Params.ToolChoice.ResponsesToolChoiceStr != "" {
				params = append(params, kvStr("gen_ai.request.tool_choice_type", *req.Params.ToolChoice.ResponsesToolChoiceStr))
			}
			if req.Params.ToolChoice.ResponsesToolChoiceStruct != nil && req.Params.ToolChoice.ResponsesToolChoiceStruct.Name != nil {
				params = append(params, kvStr("gen_ai.request.tool_choice_name", *req.Params.ToolChoice.ResponsesToolChoiceStruct.Name))
			}

		}
		if req.Params.Tools != nil {
			tools := make([]string, len(req.Params.Tools))
			for i, tool := range req.Params.Tools {
				tools[i] = string(tool.Type)
			}
			params = append(params, kvStr("gen_ai.request.tools", strings.Join(tools, ",")))
		}
		if req.Params.Truncation != nil {
			params = append(params, kvStr("gen_ai.request.truncation", *req.Params.Truncation))
		}
		if req.Params.ExtraParams != nil {
			for k, v := range req.Params.ExtraParams {
				params = append(params, kvStr(k, fmt.Sprintf("%v", v)))
			}
		}
	}
	return params
}

// getFileUploadRequestParams handles the file upload request
func getFileUploadRequestParams(req *schemas.BifrostFileUploadRequest) []*KeyValue {
	params := []*KeyValue{}
	if req.Filename != "" {
		params = append(params, kvStr("gen_ai.file.filename", req.Filename))
	}
	if req.Purpose != "" {
		params = append(params, kvStr("gen_ai.file.purpose", string(req.Purpose)))
	}
	if len(req.File) > 0 {
		params = append(params, kvInt("gen_ai.file.bytes", int64(len(req.File))))
	}
	if req.ExtraParams != nil {
		for k, v := range req.ExtraParams {
			params = append(params, kvStr(k, fmt.Sprintf("%v", v)))
		}
	}
	return params
}

// getFileListRequestParams handles the file list request
func getFileListRequestParams(req *schemas.BifrostFileListRequest) []*KeyValue {
	params := []*KeyValue{}
	if req.Purpose != "" {
		params = append(params, kvStr("gen_ai.file.purpose", string(req.Purpose)))
	}
	if req.Limit > 0 {
		params = append(params, kvInt("gen_ai.file.limit", int64(req.Limit)))
	}
	if req.After != nil {
		params = append(params, kvStr("gen_ai.file.after", *req.After))
	}
	if req.Order != nil {
		params = append(params, kvStr("gen_ai.file.order", *req.Order))
	}
	if req.ExtraParams != nil {
		for k, v := range req.ExtraParams {
			params = append(params, kvStr(k, fmt.Sprintf("%v", v)))
		}
	}
	return params
}

// getFileRetrieveRequestParams handles the file retrieve request
func getFileRetrieveRequestParams(req *schemas.BifrostFileRetrieveRequest) []*KeyValue {
	params := []*KeyValue{}
	if req.FileID != "" {
		params = append(params, kvStr("gen_ai.file.file_id", req.FileID))
	}
	if req.ExtraParams != nil {
		for k, v := range req.ExtraParams {
			params = append(params, kvStr(k, fmt.Sprintf("%v", v)))
		}
	}
	return params
}

// getFileDeleteRequestParams handles the file delete request
func getFileDeleteRequestParams(req *schemas.BifrostFileDeleteRequest) []*KeyValue {
	params := []*KeyValue{}
	if req.FileID != "" {
		params = append(params, kvStr("gen_ai.file.file_id", req.FileID))
	}
	if req.ExtraParams != nil {
		for k, v := range req.ExtraParams {
			params = append(params, kvStr(k, fmt.Sprintf("%v", v)))
		}
	}
	return params
}

// getFileContentRequestParams handles the file content request
func getFileContentRequestParams(req *schemas.BifrostFileContentRequest) []*KeyValue {
	params := []*KeyValue{}
	if req.FileID != "" {
		params = append(params, kvStr("gen_ai.file.file_id", req.FileID))
	}
	if req.ExtraParams != nil {
		for k, v := range req.ExtraParams {
			params = append(params, kvStr(k, fmt.Sprintf("%v", v)))
		}
	}
	return params
}

// getBatchCreateRequestParams handles the batch create request
func getBatchCreateRequestParams(req *schemas.BifrostBatchCreateRequest) []*KeyValue {
	params := []*KeyValue{}
	if req.InputFileID != "" {
		params = append(params, kvStr("gen_ai.batch.input_file_id", req.InputFileID))
	}
	if req.Endpoint != "" {
		params = append(params, kvStr("gen_ai.batch.endpoint", string(req.Endpoint)))
	}
	if req.CompletionWindow != "" {
		params = append(params, kvStr("gen_ai.batch.completion_window", req.CompletionWindow))
	}
	if len(req.Requests) > 0 {
		params = append(params, kvInt("gen_ai.batch.requests_count", int64(len(req.Requests))))
	}
	if len(req.Metadata) > 0 {
		params = append(params, kvStr("gen_ai.batch.metadata", fmt.Sprintf("%v", req.Metadata)))
	}
	if req.ExtraParams != nil {
		for k, v := range req.ExtraParams {
			params = append(params, kvStr(k, fmt.Sprintf("%v", v)))
		}
	}
	return params
}

// getBatchListRequestParams handles the batch list request
func getBatchListRequestParams(req *schemas.BifrostBatchListRequest) []*KeyValue {
	params := []*KeyValue{}
	if req.Limit > 0 {
		params = append(params, kvInt("gen_ai.batch.limit", int64(req.Limit)))
	}
	if req.After != nil {
		params = append(params, kvStr("gen_ai.batch.after", *req.After))
	}
	if req.BeforeID != nil {
		params = append(params, kvStr("gen_ai.batch.before_id", *req.BeforeID))
	}
	if req.AfterID != nil {
		params = append(params, kvStr("gen_ai.batch.after_id", *req.AfterID))
	}
	if req.PageToken != nil {
		params = append(params, kvStr("gen_ai.batch.page_token", *req.PageToken))
	}
	if req.PageSize > 0 {
		params = append(params, kvInt("gen_ai.batch.page_size", int64(req.PageSize)))
	}
	if req.ExtraParams != nil {
		for k, v := range req.ExtraParams {
			params = append(params, kvStr(k, fmt.Sprintf("%v", v)))
		}
	}
	return params
}

// getBatchRetrieveRequestParams handles the batch retrieve request
func getBatchRetrieveRequestParams(req *schemas.BifrostBatchRetrieveRequest) []*KeyValue {
	params := []*KeyValue{}
	if req.BatchID != "" {
		params = append(params, kvStr("gen_ai.batch.batch_id", req.BatchID))
	}
	if req.ExtraParams != nil {
		for k, v := range req.ExtraParams {
			params = append(params, kvStr(k, fmt.Sprintf("%v", v)))
		}
	}
	return params
}

// getBatchCancelRequestParams handles the batch cancel request
func getBatchCancelRequestParams(req *schemas.BifrostBatchCancelRequest) []*KeyValue {
	params := []*KeyValue{}
	if req.BatchID != "" {
		params = append(params, kvStr("gen_ai.batch.batch_id", req.BatchID))
	}
	if req.ExtraParams != nil {
		for k, v := range req.ExtraParams {
			params = append(params, kvStr(k, fmt.Sprintf("%v", v)))
		}
	}
	return params
}

// getBatchResultsRequestParams handles the batch results request
func getBatchResultsRequestParams(req *schemas.BifrostBatchResultsRequest) []*KeyValue {
	params := []*KeyValue{}
	if req.BatchID != "" {
		params = append(params, kvStr("gen_ai.batch.batch_id", req.BatchID))
	}
	if req.ExtraParams != nil {
		for k, v := range req.ExtraParams {
			params = append(params, kvStr(k, fmt.Sprintf("%v", v)))
		}
	}
	return params
}

// createResourceSpan creates a new resource span for a Bifrost request
func (p *OtelPlugin) createResourceSpan(traceID, spanID string, timestamp time.Time, req *schemas.BifrostRequest) *ResourceSpan {
	provider, model, _ := req.GetRequestFields()

	// preparing parameters
	params := []*KeyValue{}
	spanName := "span"
	params = append(params, kvStr("gen_ai.provider.name", string(provider)))
	params = append(params, kvStr("gen_ai.request.model", model))
	// Preparing parameters
	switch req.RequestType {
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		spanName = "gen_ai.text"
		params = append(params, getTextCompletionRequestParams(req.TextCompletionRequest)...)
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		spanName = "gen_ai.chat"
		params = append(params, getChatRequestParams(req.ChatRequest)...)
	case schemas.EmbeddingRequest:
		spanName = "gen_ai.embedding"
		params = append(params, getEmbeddingRequestParams(req.EmbeddingRequest)...)
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		spanName = "gen_ai.transcription"
		params = append(params, getTranscriptionRequestParams(req.TranscriptionRequest)...)
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		spanName = "gen_ai.speech"
		params = append(params, getSpeechRequestParams(req.SpeechRequest)...)
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		spanName = "gen_ai.responses"
		params = append(params, getResponsesRequestParams(req.ResponsesRequest)...)
	case schemas.BatchCreateRequest:
		spanName = "gen_ai.batch.create"
		params = append(params, getBatchCreateRequestParams(req.BatchCreateRequest)...)
	case schemas.BatchListRequest:
		spanName = "gen_ai.batch.list"
		params = append(params, getBatchListRequestParams(req.BatchListRequest)...)
	case schemas.BatchRetrieveRequest:
		spanName = "gen_ai.batch.retrieve"
		params = append(params, getBatchRetrieveRequestParams(req.BatchRetrieveRequest)...)
	case schemas.BatchCancelRequest:
		spanName = "gen_ai.batch.cancel"
		params = append(params, getBatchCancelRequestParams(req.BatchCancelRequest)...)
	case schemas.BatchResultsRequest:
		spanName = "gen_ai.batch.results"
		params = append(params, getBatchResultsRequestParams(req.BatchResultsRequest)...)
	case schemas.FileUploadRequest:
		spanName = "gen_ai.file.upload"
		params = append(params, getFileUploadRequestParams(req.FileUploadRequest)...)
	case schemas.FileListRequest:
		spanName = "gen_ai.file.list"
		params = append(params, getFileListRequestParams(req.FileListRequest)...)
	case schemas.FileRetrieveRequest:
		spanName = "gen_ai.file.retrieve"
		params = append(params, getFileRetrieveRequestParams(req.FileRetrieveRequest)...)
	case schemas.FileDeleteRequest:
		spanName = "gen_ai.file.delete"
		params = append(params, getFileDeleteRequestParams(req.FileDeleteRequest)...)
	case schemas.FileContentRequest:
		spanName = "gen_ai.file.content"
		params = append(params, getFileContentRequestParams(req.FileContentRequest)...)
	}
	attributes := append(p.attributesFromEnvironment, kvStr("service.name", p.serviceName), kvStr("service.version", p.bifrostVersion))
	// Preparing final resource span
	return &ResourceSpan{
		Resource: &resourcepb.Resource{
			Attributes: attributes,
		},
		ScopeSpans: []*ScopeSpan{
			{
				Scope: &commonpb.InstrumentationScope{
					Name: "bifrost-otel-plugin",
				},
				Spans: []*Span{
					{
						TraceId:           hexToBytes(traceID, 16),
						SpanId:            hexToBytes(spanID, 8),
						Kind:              tracepb.Span_SPAN_KIND_SERVER,
						StartTimeUnixNano: uint64(timestamp.UnixNano()),
						EndTimeUnixNano:   uint64(timestamp.UnixNano()),
						Name:              spanName,
						Attributes:        params,
					},
				},
			},
		},
	}
}

// completeResourceSpan completes a resource span for a Bifrost response
func completeResourceSpan(
	span *ResourceSpan,
	timestamp time.Time,
	resp *schemas.BifrostResponse,
	bifrostErr *schemas.BifrostError,
	pricingManager *modelcatalog.ModelCatalog,
	virtualKeyID string,
	virtualKeyName string,
	selectedKeyID string,
	selectedKeyName string,
	numberOfRetries int,
	fallbackIndex int,
	teamID string,
	teamName string,
	customerID string,
	customerName string,
) *ResourceSpan {
	params := []*KeyValue{}

	if resp != nil {
		switch { // Accumulator wont return stream type responses
		case resp.TextCompletionResponse != nil:
			params = append(params, kvStr("gen_ai.text.id", resp.TextCompletionResponse.ID))
			params = append(params, kvStr("gen_ai.text.model", resp.TextCompletionResponse.Model))
			params = append(params, kvStr("gen_ai.text.object", resp.TextCompletionResponse.Object))
			params = append(params, kvStr("gen_ai.text.system_fingerprint", resp.TextCompletionResponse.SystemFingerprint))
			outputMessages := []*AnyValue{}
			for _, choice := range resp.TextCompletionResponse.Choices {
				if choice.TextCompletionResponseChoice == nil {
					continue
				}
				kvs := []*KeyValue{kvStr("role", string(schemas.ChatMessageRoleAssistant))}
				if choice.TextCompletionResponseChoice != nil && choice.TextCompletionResponseChoice.Text != nil {
					kvs = append(kvs, kvStr("content", *choice.TextCompletionResponseChoice.Text))
				}
				outputMessages = append(outputMessages, listValue(kvs...))
			}
			params = append(params, kvAny("gen_ai.text.output_messages", arrValue(outputMessages...)))
			if resp.TextCompletionResponse.Usage != nil {
				params = append(params, kvInt("gen_ai.usage.prompt_tokens", int64(resp.TextCompletionResponse.Usage.PromptTokens)))
				params = append(params, kvInt("gen_ai.usage.completion_tokens", int64(resp.TextCompletionResponse.Usage.CompletionTokens)))
				params = append(params, kvInt("gen_ai.usage.total_tokens", int64(resp.TextCompletionResponse.Usage.TotalTokens)))
			}
			// Computing cost
			if pricingManager != nil {
				cost := pricingManager.CalculateCostWithCacheDebug(resp)
				params = append(params, kvDbl("gen_ai.usage.cost", cost))
			}
		case resp.ChatResponse != nil:
			params = append(params, kvStr("gen_ai.chat.id", resp.ChatResponse.ID))
			params = append(params, kvStr("gen_ai.chat.model", resp.ChatResponse.Model))
			params = append(params, kvStr("gen_ai.chat.object", resp.ChatResponse.Object))
			params = append(params, kvStr("gen_ai.chat.system_fingerprint", resp.ChatResponse.SystemFingerprint))
			params = append(params, kvStr("gen_ai.chat.created", fmt.Sprintf("%d", resp.ChatResponse.Created)))
			if resp.ChatResponse.ServiceTier != nil {
				params = append(params, kvStr("gen_ai.chat.service_tier", *resp.ChatResponse.ServiceTier))
			}
			outputMessages := []*AnyValue{}
			for _, choice := range resp.ChatResponse.Choices {
				var role string
				if choice.ChatNonStreamResponseChoice != nil && choice.ChatNonStreamResponseChoice.Message != nil && choice.ChatNonStreamResponseChoice.Message.Role != "" {
					role = string(choice.ChatNonStreamResponseChoice.Message.Role)
				} else {
					role = string(schemas.ChatMessageRoleAssistant)
				}
				kvs := []*KeyValue{kvStr("role", role)}

				if choice.ChatNonStreamResponseChoice != nil &&
					choice.ChatNonStreamResponseChoice.Message != nil &&
					choice.ChatNonStreamResponseChoice.Message.Content != nil {
					if choice.ChatNonStreamResponseChoice.Message.Content.ContentStr != nil {
						kvs = append(kvs, kvStr("content", *choice.ChatNonStreamResponseChoice.Message.Content.ContentStr))
					} else if choice.ChatNonStreamResponseChoice.Message.Content.ContentBlocks != nil {
						blockText := ""
						for _, block := range choice.ChatNonStreamResponseChoice.Message.Content.ContentBlocks {
							if block.Text != nil {
								blockText += *block.Text
							}
						}
						kvs = append(kvs, kvStr("content", blockText))
					}
				}
				outputMessages = append(outputMessages, listValue(kvs...))
			}
			params = append(params, kvAny("gen_ai.chat.output_messages", arrValue(outputMessages...)))
			if resp.ChatResponse.Usage != nil {
				params = append(params, kvInt("gen_ai.usage.prompt_tokens", int64(resp.ChatResponse.Usage.PromptTokens)))
				params = append(params, kvInt("gen_ai.usage.completion_tokens", int64(resp.ChatResponse.Usage.CompletionTokens)))
				params = append(params, kvInt("gen_ai.usage.total_tokens", int64(resp.ChatResponse.Usage.TotalTokens)))
			}
			// Computing cost
			if pricingManager != nil {
				cost := pricingManager.CalculateCostWithCacheDebug(resp)
				params = append(params, kvDbl("gen_ai.usage.cost", cost))
			}
		case resp.ResponsesResponse != nil:
			outputMessages := []*AnyValue{}
			for _, message := range resp.ResponsesResponse.Output {
				if message.Role == nil {
					continue
				}
				kvs := []*KeyValue{kvStr("role", string(*message.Role))}
				if message.Content != nil {
					if message.Content.ContentStr != nil && *message.Content.ContentStr != "" {
						kvs = append(kvs, kvStr("content", *message.Content.ContentStr))
					} else if message.Content.ContentBlocks != nil {
						blockText := ""
						for _, block := range message.Content.ContentBlocks {
							if block.Text != nil {
								blockText += *block.Text
							}
						}
						kvs = append(kvs, kvStr("content", blockText))
					}
				}
				if message.ResponsesReasoning != nil && message.ResponsesReasoning.Summary != nil {
					reasoningText := ""
					for _, block := range message.ResponsesReasoning.Summary {
						if block.Text != "" {
							reasoningText += block.Text
						}
					}
					kvs = append(kvs, kvStr("reasoning", reasoningText))
				}
				outputMessages = append(outputMessages, listValue(kvs...))

			}
			params = append(params, kvAny("gen_ai.responses.output_messages", arrValue(outputMessages...)))

			responsesResponse := resp.ResponsesResponse
			if responsesResponse.Include != nil {
				params = append(params, kvStr("gen_ai.responses.include", strings.Join(responsesResponse.Include, ",")))
			}
			if responsesResponse.MaxOutputTokens != nil {
				params = append(params, kvInt("gen_ai.responses.max_output_tokens", int64(*responsesResponse.MaxOutputTokens)))
			}
			if responsesResponse.MaxToolCalls != nil {
				params = append(params, kvInt("gen_ai.responses.max_tool_calls", int64(*responsesResponse.MaxToolCalls)))
			}
			if responsesResponse.Metadata != nil {
				params = append(params, kvStr("gen_ai.responses.metadata", fmt.Sprintf("%v", responsesResponse.Metadata)))
			}
			if responsesResponse.PreviousResponseID != nil {
				params = append(params, kvStr("gen_ai.responses.previous_response_id", *responsesResponse.PreviousResponseID))
			}
			if responsesResponse.PromptCacheKey != nil {
				params = append(params, kvStr("gen_ai.responses.prompt_cache_key", *responsesResponse.PromptCacheKey))
			}
			if responsesResponse.Reasoning != nil {
				if responsesResponse.Reasoning.Summary != nil {
					params = append(params, kvStr("gen_ai.responses.reasoning", *responsesResponse.Reasoning.Summary))
				}
				if responsesResponse.Reasoning.Effort != nil {
					params = append(params, kvStr("gen_ai.responses.reasoning_effort", *responsesResponse.Reasoning.Effort))
				}
				if responsesResponse.Reasoning.GenerateSummary != nil {
					params = append(params, kvStr("gen_ai.responses.reasoning_generate_summary", *responsesResponse.Reasoning.GenerateSummary))
				}
			}
			if responsesResponse.SafetyIdentifier != nil {
				params = append(params, kvStr("gen_ai.responses.safety_identifier", *responsesResponse.SafetyIdentifier))
			}
			if responsesResponse.ServiceTier != nil {
				params = append(params, kvStr("gen_ai.responses.service_tier", *responsesResponse.ServiceTier))
			}
			if responsesResponse.Store != nil {
				params = append(params, kvBool("gen_ai.responses.store", *responsesResponse.Store))
			}
			if responsesResponse.Temperature != nil {
				params = append(params, kvDbl("gen_ai.responses.temperature", *responsesResponse.Temperature))
			}
			if responsesResponse.Text != nil {
				if responsesResponse.Text.Verbosity != nil {
					params = append(params, kvStr("gen_ai.responses.text", *responsesResponse.Text.Verbosity))
				}
				if responsesResponse.Text.Format != nil {
					params = append(params, kvStr("gen_ai.responses.text_format_type", responsesResponse.Text.Format.Type))
				}
			}
			if responsesResponse.TopLogProbs != nil {
				params = append(params, kvInt("gen_ai.responses.top_logprobs", int64(*responsesResponse.TopLogProbs)))
			}
			if responsesResponse.TopP != nil {
				params = append(params, kvDbl("gen_ai.responses.top_p", *responsesResponse.TopP))
			}
			if responsesResponse.ToolChoice != nil {
				if responsesResponse.ToolChoice.ResponsesToolChoiceStruct != nil && responsesResponse.ToolChoice.ResponsesToolChoiceStr != nil {
					params = append(params, kvStr("gen_ai.responses.tool_choice_type", *responsesResponse.ToolChoice.ResponsesToolChoiceStr))
				}
				if responsesResponse.ToolChoice.ResponsesToolChoiceStruct != nil && responsesResponse.ToolChoice.ResponsesToolChoiceStruct.Name != nil {
					params = append(params, kvStr("gen_ai.responses.tool_choice_name", *responsesResponse.ToolChoice.ResponsesToolChoiceStruct.Name))
				}
			}
			if responsesResponse.Truncation != nil {
				params = append(params, kvStr("gen_ai.responses.truncation", *responsesResponse.Truncation))
			}
			if responsesResponse.Tools != nil {
				tools := make([]string, len(responsesResponse.Tools))
				for i, tool := range responsesResponse.Tools {
					tools[i] = string(tool.Type)
				}
				params = append(params, kvStr("gen_ai.responses.tools", strings.Join(tools, ",")))
			}
		case resp.EmbeddingResponse != nil:
			if resp.EmbeddingResponse.Usage != nil {
				params = append(params, kvInt("gen_ai.usage.prompt_tokens", int64(resp.EmbeddingResponse.Usage.PromptTokens)))
				params = append(params, kvInt("gen_ai.usage.completion_tokens", int64(resp.EmbeddingResponse.Usage.CompletionTokens)))
				params = append(params, kvInt("gen_ai.usage.total_tokens", int64(resp.EmbeddingResponse.Usage.TotalTokens)))
			}
		case resp.SpeechResponse != nil:
			if resp.SpeechResponse.Usage != nil {
				params = append(params, kvInt("gen_ai.usage.input_tokens", int64(resp.SpeechResponse.Usage.InputTokens)))
				params = append(params, kvInt("gen_ai.usage.output_tokens", int64(resp.SpeechResponse.Usage.OutputTokens)))
				params = append(params, kvInt("gen_ai.usage.total_tokens", int64(resp.SpeechResponse.Usage.TotalTokens)))
			}
		case resp.TranscriptionResponse != nil:
			outputMessages := []*AnyValue{}
			kvs := []*KeyValue{kvStr("text", resp.TranscriptionResponse.Text)}
			outputMessages = append(outputMessages, listValue(kvs...))
			params = append(params, kvAny("gen_ai.transcribe.output_messages", arrValue(outputMessages...)))
			if resp.TranscriptionResponse.Usage != nil {
				if resp.TranscriptionResponse.Usage.InputTokens != nil {
					params = append(params, kvInt("gen_ai.usage.input_tokens", int64(*resp.TranscriptionResponse.Usage.InputTokens)))
				}
				if resp.TranscriptionResponse.Usage.OutputTokens != nil {
					params = append(params, kvInt("gen_ai.usage.completion_tokens", int64(*resp.TranscriptionResponse.Usage.OutputTokens)))
				}
				if resp.TranscriptionResponse.Usage.TotalTokens != nil {
					params = append(params, kvInt("gen_ai.usage.total_tokens", int64(*resp.TranscriptionResponse.Usage.TotalTokens)))
				}
				if resp.TranscriptionResponse.Usage.InputTokenDetails != nil {
					params = append(params, kvInt("gen_ai.usage.input_token_details.text_tokens", int64(resp.TranscriptionResponse.Usage.InputTokenDetails.TextTokens)))
					params = append(params, kvInt("gen_ai.usage.input_token_details.audio_tokens", int64(resp.TranscriptionResponse.Usage.InputTokenDetails.AudioTokens)))
				}
			}
		case resp.BatchCreateResponse != nil:
			params = append(params, kvStr("gen_ai.batch.id", resp.BatchCreateResponse.ID))
			params = append(params, kvStr("gen_ai.batch.status", string(resp.BatchCreateResponse.Status)))
			if resp.BatchCreateResponse.Object != "" {
				params = append(params, kvStr("gen_ai.batch.object", resp.BatchCreateResponse.Object))
			}
			if resp.BatchCreateResponse.Endpoint != "" {
				params = append(params, kvStr("gen_ai.batch.endpoint", resp.BatchCreateResponse.Endpoint))
			}
			if resp.BatchCreateResponse.InputFileID != "" {
				params = append(params, kvStr("gen_ai.batch.input_file_id", resp.BatchCreateResponse.InputFileID))
			}
			if resp.BatchCreateResponse.CompletionWindow != "" {
				params = append(params, kvStr("gen_ai.batch.completion_window", resp.BatchCreateResponse.CompletionWindow))
			}
			if resp.BatchCreateResponse.CreatedAt != 0 {
				params = append(params, kvInt("gen_ai.batch.created_at", resp.BatchCreateResponse.CreatedAt))
			}
			if resp.BatchCreateResponse.ExpiresAt != nil {
				params = append(params, kvInt("gen_ai.batch.expires_at", *resp.BatchCreateResponse.ExpiresAt))
			}
			if resp.BatchCreateResponse.OutputFileID != nil {
				params = append(params, kvStr("gen_ai.batch.output_file_id", *resp.BatchCreateResponse.OutputFileID))
			}
			if resp.BatchCreateResponse.ErrorFileID != nil {
				params = append(params, kvStr("gen_ai.batch.error_file_id", *resp.BatchCreateResponse.ErrorFileID))
			}
			params = append(params, kvInt("gen_ai.batch.request_counts.total", int64(resp.BatchCreateResponse.RequestCounts.Total)))
			params = append(params, kvInt("gen_ai.batch.request_counts.completed", int64(resp.BatchCreateResponse.RequestCounts.Completed)))
			params = append(params, kvInt("gen_ai.batch.request_counts.failed", int64(resp.BatchCreateResponse.RequestCounts.Failed)))
		case resp.BatchListResponse != nil:
			if resp.BatchListResponse.Object != "" {
				params = append(params, kvStr("gen_ai.batch.object", resp.BatchListResponse.Object))
			}
			params = append(params, kvInt("gen_ai.batch.data_count", int64(len(resp.BatchListResponse.Data))))
			params = append(params, kvBool("gen_ai.batch.has_more", resp.BatchListResponse.HasMore))
			if resp.BatchListResponse.FirstID != nil {
				params = append(params, kvStr("gen_ai.batch.first_id", *resp.BatchListResponse.FirstID))
			}
			if resp.BatchListResponse.LastID != nil {
				params = append(params, kvStr("gen_ai.batch.last_id", *resp.BatchListResponse.LastID))
			}
		case resp.BatchRetrieveResponse != nil:
			params = append(params, kvStr("gen_ai.batch.id", resp.BatchRetrieveResponse.ID))
			params = append(params, kvStr("gen_ai.batch.status", string(resp.BatchRetrieveResponse.Status)))
			if resp.BatchRetrieveResponse.Object != "" {
				params = append(params, kvStr("gen_ai.batch.object", resp.BatchRetrieveResponse.Object))
			}
			if resp.BatchRetrieveResponse.Endpoint != "" {
				params = append(params, kvStr("gen_ai.batch.endpoint", resp.BatchRetrieveResponse.Endpoint))
			}
			if resp.BatchRetrieveResponse.InputFileID != "" {
				params = append(params, kvStr("gen_ai.batch.input_file_id", resp.BatchRetrieveResponse.InputFileID))
			}
			if resp.BatchRetrieveResponse.CompletionWindow != "" {
				params = append(params, kvStr("gen_ai.batch.completion_window", resp.BatchRetrieveResponse.CompletionWindow))
			}
			if resp.BatchRetrieveResponse.CreatedAt != 0 {
				params = append(params, kvInt("gen_ai.batch.created_at", resp.BatchRetrieveResponse.CreatedAt))
			}
			if resp.BatchRetrieveResponse.ExpiresAt != nil {
				params = append(params, kvInt("gen_ai.batch.expires_at", *resp.BatchRetrieveResponse.ExpiresAt))
			}
			if resp.BatchRetrieveResponse.InProgressAt != nil {
				params = append(params, kvInt("gen_ai.batch.in_progress_at", *resp.BatchRetrieveResponse.InProgressAt))
			}
			if resp.BatchRetrieveResponse.FinalizingAt != nil {
				params = append(params, kvInt("gen_ai.batch.finalizing_at", *resp.BatchRetrieveResponse.FinalizingAt))
			}
			if resp.BatchRetrieveResponse.CompletedAt != nil {
				params = append(params, kvInt("gen_ai.batch.completed_at", *resp.BatchRetrieveResponse.CompletedAt))
			}
			if resp.BatchRetrieveResponse.FailedAt != nil {
				params = append(params, kvInt("gen_ai.batch.failed_at", *resp.BatchRetrieveResponse.FailedAt))
			}
			if resp.BatchRetrieveResponse.ExpiredAt != nil {
				params = append(params, kvInt("gen_ai.batch.expired_at", *resp.BatchRetrieveResponse.ExpiredAt))
			}
			if resp.BatchRetrieveResponse.CancellingAt != nil {
				params = append(params, kvInt("gen_ai.batch.cancelling_at", *resp.BatchRetrieveResponse.CancellingAt))
			}
			if resp.BatchRetrieveResponse.CancelledAt != nil {
				params = append(params, kvInt("gen_ai.batch.cancelled_at", *resp.BatchRetrieveResponse.CancelledAt))
			}
			if resp.BatchRetrieveResponse.OutputFileID != nil {
				params = append(params, kvStr("gen_ai.batch.output_file_id", *resp.BatchRetrieveResponse.OutputFileID))
			}
			if resp.BatchRetrieveResponse.ErrorFileID != nil {
				params = append(params, kvStr("gen_ai.batch.error_file_id", *resp.BatchRetrieveResponse.ErrorFileID))
			}
			params = append(params, kvInt("gen_ai.batch.request_counts.total", int64(resp.BatchRetrieveResponse.RequestCounts.Total)))
			params = append(params, kvInt("gen_ai.batch.request_counts.completed", int64(resp.BatchRetrieveResponse.RequestCounts.Completed)))
			params = append(params, kvInt("gen_ai.batch.request_counts.failed", int64(resp.BatchRetrieveResponse.RequestCounts.Failed)))
		case resp.BatchCancelResponse != nil:
			params = append(params, kvStr("gen_ai.batch.id", resp.BatchCancelResponse.ID))
			params = append(params, kvStr("gen_ai.batch.status", string(resp.BatchCancelResponse.Status)))
			if resp.BatchCancelResponse.Object != "" {
				params = append(params, kvStr("gen_ai.batch.object", resp.BatchCancelResponse.Object))
			}
			if resp.BatchCancelResponse.CancellingAt != nil {
				params = append(params, kvInt("gen_ai.batch.cancelling_at", *resp.BatchCancelResponse.CancellingAt))
			}
			if resp.BatchCancelResponse.CancelledAt != nil {
				params = append(params, kvInt("gen_ai.batch.cancelled_at", *resp.BatchCancelResponse.CancelledAt))
			}
			params = append(params, kvInt("gen_ai.batch.request_counts.total", int64(resp.BatchCancelResponse.RequestCounts.Total)))
			params = append(params, kvInt("gen_ai.batch.request_counts.completed", int64(resp.BatchCancelResponse.RequestCounts.Completed)))
			params = append(params, kvInt("gen_ai.batch.request_counts.failed", int64(resp.BatchCancelResponse.RequestCounts.Failed)))
		case resp.BatchResultsResponse != nil:
			params = append(params, kvStr("gen_ai.batch.batch_id", resp.BatchResultsResponse.BatchID))
			params = append(params, kvInt("gen_ai.batch.results_count", int64(len(resp.BatchResultsResponse.Results))))
			params = append(params, kvBool("gen_ai.batch.has_more", resp.BatchResultsResponse.HasMore))
			if resp.BatchResultsResponse.NextCursor != nil {
				params = append(params, kvStr("gen_ai.batch.next_cursor", *resp.BatchResultsResponse.NextCursor))
			}
		case resp.FileUploadResponse != nil:
			params = append(params, kvStr("gen_ai.file.id", resp.FileUploadResponse.ID))
			if resp.FileUploadResponse.Object != "" {
				params = append(params, kvStr("gen_ai.file.object", resp.FileUploadResponse.Object))
			}
			params = append(params, kvInt("gen_ai.file.bytes", resp.FileUploadResponse.Bytes))
			params = append(params, kvInt("gen_ai.file.created_at", resp.FileUploadResponse.CreatedAt))
			params = append(params, kvStr("gen_ai.file.filename", resp.FileUploadResponse.Filename))
			params = append(params, kvStr("gen_ai.file.purpose", string(resp.FileUploadResponse.Purpose)))
			if resp.FileUploadResponse.Status != "" {
				params = append(params, kvStr("gen_ai.file.status", string(resp.FileUploadResponse.Status)))
			}
			if resp.FileUploadResponse.StorageBackend != "" {
				params = append(params, kvStr("gen_ai.file.storage_backend", string(resp.FileUploadResponse.StorageBackend)))
			}
		case resp.FileListResponse != nil:
			if resp.FileListResponse.Object != "" {
				params = append(params, kvStr("gen_ai.file.object", resp.FileListResponse.Object))
			}
			params = append(params, kvInt("gen_ai.file.data_count", int64(len(resp.FileListResponse.Data))))
			params = append(params, kvBool("gen_ai.file.has_more", resp.FileListResponse.HasMore))
		case resp.FileRetrieveResponse != nil:
			params = append(params, kvStr("gen_ai.file.id", resp.FileRetrieveResponse.ID))
			if resp.FileRetrieveResponse.Object != "" {
				params = append(params, kvStr("gen_ai.file.object", resp.FileRetrieveResponse.Object))
			}
			params = append(params, kvInt("gen_ai.file.bytes", resp.FileRetrieveResponse.Bytes))
			params = append(params, kvInt("gen_ai.file.created_at", resp.FileRetrieveResponse.CreatedAt))
			params = append(params, kvStr("gen_ai.file.filename", resp.FileRetrieveResponse.Filename))
			params = append(params, kvStr("gen_ai.file.purpose", string(resp.FileRetrieveResponse.Purpose)))
			if resp.FileRetrieveResponse.Status != "" {
				params = append(params, kvStr("gen_ai.file.status", string(resp.FileRetrieveResponse.Status)))
			}
			if resp.FileRetrieveResponse.StorageBackend != "" {
				params = append(params, kvStr("gen_ai.file.storage_backend", string(resp.FileRetrieveResponse.StorageBackend)))
			}
		case resp.FileDeleteResponse != nil:
			params = append(params, kvStr("gen_ai.file.id", resp.FileDeleteResponse.ID))
			if resp.FileDeleteResponse.Object != "" {
				params = append(params, kvStr("gen_ai.file.object", resp.FileDeleteResponse.Object))
			}
			params = append(params, kvBool("gen_ai.file.deleted", resp.FileDeleteResponse.Deleted))
		case resp.FileContentResponse != nil:
			params = append(params, kvStr("gen_ai.file.file_id", resp.FileContentResponse.FileID))
			if resp.FileContentResponse.ContentType != "" {
				params = append(params, kvStr("gen_ai.file.content_type", resp.FileContentResponse.ContentType))
			}
			if len(resp.FileContentResponse.Content) > 0 {
				params = append(params, kvInt("gen_ai.file.content_bytes", int64(len(resp.FileContentResponse.Content))))
			}
		}
	}

	// This is a fallback for worst case scenario where latency is not available
	status := tracepb.Status_STATUS_CODE_OK
	if bifrostErr != nil {
		status = tracepb.Status_STATUS_CODE_ERROR
		if bifrostErr.Error != nil {
			if bifrostErr.Error.Type != nil {
				params = append(params, kvStr("gen_ai.error.type", *bifrostErr.Error.Type))
			}
			if bifrostErr.Error.Code != nil {
				params = append(params, kvStr("gen_ai.error.code", *bifrostErr.Error.Code))
			}
		}
		params = append(params, kvStr("gen_ai.error", bifrostErr.Error.Message))
	}
	// Adding request metadata to the span for backward compatibility
	if virtualKeyID != "" {
		params = append(params, kvStr("gen_ai.virtual_key_id", virtualKeyID))
		params = append(params, kvStr("gen_ai.virtual_key_name", virtualKeyName))
	}
	if selectedKeyID != "" {
		params = append(params, kvStr("gen_ai.selected_key_id", selectedKeyID))
		params = append(params, kvStr("gen_ai.selected_key_name", selectedKeyName))
	}
	if teamID != "" {
		params = append(params, kvStr("gen_ai.team_id", teamID))
		params = append(params, kvStr("gen_ai.team_name", teamName))
	}
	if customerID != "" {
		params = append(params, kvStr("gen_ai.customer_id", customerID))
		params = append(params, kvStr("gen_ai.customer_name", customerName))
	}
	params = append(params, kvInt("gen_ai.number_of_retries", int64(numberOfRetries)))
	params = append(params, kvInt("gen_ai.fallback_index", int64(fallbackIndex)))
	span.ScopeSpans[0].Spans[0].Attributes = append(span.ScopeSpans[0].Spans[0].Attributes, params...)
	span.ScopeSpans[0].Spans[0].Status = &tracepb.Status{Code: status}
	span.ScopeSpans[0].Spans[0].EndTimeUnixNano = uint64(timestamp.UnixNano())
	// Attaching virtual keys as resource attributes as well
	span.Resource.Attributes = append(span.Resource.Attributes, kvStr("virtual_key_id", virtualKeyID))
	span.Resource.Attributes = append(span.Resource.Attributes, kvStr("virtual_key_name", virtualKeyName))
	span.Resource.Attributes = append(span.Resource.Attributes, kvStr("selected_key_id", selectedKeyID))
	span.Resource.Attributes = append(span.Resource.Attributes, kvStr("selected_key_name", selectedKeyName))
	span.Resource.Attributes = append(span.Resource.Attributes, kvStr("team_id", teamID))
	span.Resource.Attributes = append(span.Resource.Attributes, kvStr("team_name", teamName))
	span.Resource.Attributes = append(span.Resource.Attributes, kvStr("customer_id", customerID))
	span.Resource.Attributes = append(span.Resource.Attributes, kvStr("customer_name", customerName))
	span.Resource.Attributes = append(span.Resource.Attributes, kvInt("number_of_retries", int64(numberOfRetries)))
	span.Resource.Attributes = append(span.Resource.Attributes, kvInt("fallback_index", int64(fallbackIndex)))
	return span
}
