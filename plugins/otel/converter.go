package otel

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
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

// requestToResourceSpan converts a Bifrost request to an OpenTelemetry resource span
func requestToResourceSpan(traceID, spanID string, timestamp time.Time, req *schemas.BifrostRequest) *ResourceSpan {
	// preparing parameters
	params := []*KeyValue{}
	spanName := "span"
	if req.Params != nil {
		params = append(params, kvStr("gen_ai.provider.name", string(req.Provider)))
		params = append(params, kvStr("gen_ai.request.model", req.Model))
		if req.Params.Dimensions != nil {
			params = append(params, kvDbl("gen_ai.request.dimensions", float64(*req.Params.Dimensions)))
		}
		if req.Params.MaxTokens != nil {
			params = append(params, kvInt("gen_ai.request.max_tokens", int64(*req.Params.MaxTokens)))
		}
		if req.Params.Temperature != nil {
			params = append(params, kvDbl("gen_ai.request.temperature", *req.Params.Temperature))
		}
		if req.Params.TopP != nil {
			params = append(params, kvDbl("gen_ai.request.top_p", *req.Params.TopP))
		}
		if req.Params.TopK != nil {
			params = append(params, kvInt("gen_ai.request.top_k", int64(*req.Params.TopK)))
		}
		if req.Params.StopSequences != nil {
			params = append(params, kvStr("gen_ai.request.stop_sequences", strings.Join(*req.Params.StopSequences, ",")))
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
		if req.Params.EncodingFormat != nil {
			params = append(params, kvStr("gen_ai.request.encoding_format", *req.Params.EncodingFormat))
		}
		if req.Params.User != nil {
			params = append(params, kvStr("gen_ai.request.user", *req.Params.User))
		}
		if req.Params.ExtraParams != nil {
			for k, v := range req.Params.ExtraParams {
				params = append(params, kvStr(k, fmt.Sprintf("%v", v)))
			}
		}
		// Handling chat completion
		if req.Input.ChatCompletionInput != nil {
			spanName = "genai.request"
			messages := []*KeyValue{}
			for _, message := range *req.Input.ChatCompletionInput {
				switch message.Role {
				case schemas.ModelChatMessageRoleUser:
					messages = append(messages, kvStr("role", "user"))
					messages = append(messages, kvStr("content", *message.Content.ContentStr))
				case schemas.ModelChatMessageRoleAssistant:
					messages = append(messages, kvStr("role", "assistant"))
					messages = append(messages, kvStr("content", *message.Content.ContentStr))
				case schemas.ModelChatMessageRoleSystem:
					params = append(params, kvStr("genai.system_instructions", *message.Content.ContentStr))
				}
			}
			params = append(params, kvAny("genai.messages", arrValue(listValue(messages...))))
		}
		// Handling text completion
		if req.Input.TextCompletionInput != nil {
			spanName = "genai.text"
			params = append(params, kvStr("genai.text", *req.Input.TextCompletionInput))
		}
		// Handling embedding
		if req.Input.EmbeddingInput != nil {
			spanName = "genai.embedding"
			if req.Input.EmbeddingInput.Text != nil {
				params = append(params, kvStr("genai.text", *req.Input.EmbeddingInput.Text))
			}
			if req.Input.EmbeddingInput.Texts != nil {
				params = append(params, kvStr("genai.text", strings.Join(req.Input.EmbeddingInput.Texts, ",")))
			}
			if req.Input.EmbeddingInput.Embedding != nil {
				embedding := make([]string, len(req.Input.EmbeddingInput.Embedding))
				for i, v := range req.Input.EmbeddingInput.Embedding {
					embedding[i] = fmt.Sprintf("%d", v)
				}
				params = append(params, kvStr("genai.embedding", strings.Join(embedding, ",")))
			}
			if req.Input.EmbeddingInput.Embeddings != nil {
				embeddings := make([]string, len(req.Input.EmbeddingInput.Embeddings))
				for i, v := range req.Input.EmbeddingInput.Embeddings {
					embeddings[i] = fmt.Sprintf("%d", v)
				}
				params = append(params, kvStr("genai.embedding", strings.Join(embeddings, ",")))
			}

		}
		// Handling speech
		if req.Input.SpeechInput != nil {
			spanName = "genai.speech"
			params = append(params, kvStr("genai.speech.input", req.Input.SpeechInput.Input))
			params = append(params, kvStr("genai.speech.voice", *req.Input.SpeechInput.VoiceConfig.Voice))
			params = append(params, kvStr("genai.speech.instructions", req.Input.SpeechInput.Instructions))
			params = append(params, kvStr("genai.speech.response_format", req.Input.SpeechInput.ResponseFormat))
		}
		// Handling transcription
		if req.Input.TranscriptionInput != nil {
			spanName = "genai.transcription"
			// Truncate file data to 100KB to avoid large data in the logs
			fileData := string(req.Input.TranscriptionInput.File)
			if len(fileData) > 100*1024 {
				fileData = fileData[:100*1024]
			}
			params = append(params, kvStr("genai.transcription", fileData))
		}
	}
	// Preparing final resource span
	return &ResourceSpan{
		Resource: &resourcepb.Resource{
			Attributes: []*commonpb.KeyValue{
				kvStr("service.name", "bifrost"),
				kvStr("service.version", "1.0.0"),
			},
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
						EndTimeUnixNano:   uint64(timestamp.Add(time.Second).UnixNano()),
						Name:              spanName,
						Attributes:        params,
					},
				},
			},
		},
	}
}

// responseToResourceSpan converts a Bifrost response to an OpenTelemetry resource span
func responseToResourceSpan(traceID, parentSpanID, spanID string, timestamp time.Time, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) *ResourceSpan {
	spanName := "genai.response"
	params := []*commonpb.KeyValue{}
	params = append(params, kvStr("genai.response.id", resp.ID))
	params = append(params, kvInt("genai.usage.input_tokens", int64(resp.Usage.PromptTokens)))
	params = append(params, kvInt("genai.usage.completion_tokens", int64(resp.Usage.CompletionTokens)))
	params = append(params, kvInt("genai.usage.total_tokens", int64(resp.Usage.TotalTokens)))
	params = append(params, kvStr("genai.chat.object", resp.Object))
	params = append(params, kvStr("genai.text.model", resp.Model))
	if resp.SystemFingerprint != nil {
		params = append(params, kvStr("genai.chat.system_fingerprint", *resp.SystemFingerprint))
	}
	params = append(params, kvStr("genai.chat.created", fmt.Sprintf("%d", resp.Created)))
	switch resp.Object {
	case "chat.completion":
		spanName = "genai.chat.response"
		outputMessages := []*KeyValue{}
		for _, choice := range resp.Choices {
			outputMessages = append(outputMessages, kvStr("role", string(choice.Message.Role)))
			if choice.Message.Content.ContentStr != nil {
				outputMessages = append(outputMessages, kvStr("content", *choice.Message.Content.ContentStr))
			}
		}
		params = append(params, kvAny("genai.chat.output_messages", arrValue(listValue(outputMessages...))))
	case "text.completion":
		spanName = "genai.text"
		outputMessages := []*KeyValue{}
		for _, choice := range resp.Choices {
			outputMessages = append(outputMessages, kvStr("role", string(choice.Message.Role)))
			if choice.Message.Content.ContentStr != nil {
				outputMessages = append(outputMessages, kvStr("content", *choice.Message.Content.ContentStr))
			}
		}
		params = append(params, kvAny("genai.text.output_messages", arrValue(listValue(outputMessages...))))
	}
	startTime := timestamp.Add(-(time.Duration(*resp.ExtraFields.Latency) * time.Millisecond))
	status := tracepb.Status_STATUS_CODE_OK
	if bifrostErr != nil {
		status = tracepb.Status_STATUS_CODE_ERROR
		if bifrostErr.Error.Type != nil {
			params = append(params, kvStr("genai.error.type", *bifrostErr.Error.Type))
		}
		if bifrostErr.Error.Code != nil {
			params = append(params, kvStr("genai.error.code", *bifrostErr.Error.Code))
		}
		params = append(params, kvStr("genai.error", bifrostErr.Error.Message))
	}
	if resp.ExtraFields.BilledUsage != nil {
		params = append(params, kvInt("genai.usage.cost.prompt_tokens", int64(*resp.ExtraFields.BilledUsage.PromptTokens)))
		params = append(params, kvInt("genai.usage.cost.completion_tokens", int64(*resp.ExtraFields.BilledUsage.CompletionTokens)))
		params = append(params, kvInt("genai.usage.cost.search_units", int64(*resp.ExtraFields.BilledUsage.SearchUnits)))
		params = append(params, kvInt("genai.usage.cost.classifications", int64(*resp.ExtraFields.BilledUsage.Classifications)))
	}
	return &ResourceSpan{
		Resource: &resourcepb.Resource{
			Attributes: []*commonpb.KeyValue{
				kvStr("service.name", "bifrost"),
				kvStr("service.version", "1.0.0"),
			},
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
						ParentSpanId:      hexToBytes(parentSpanID, 8),
						Kind:              tracepb.Span_SPAN_KIND_SERVER,
						StartTimeUnixNano: uint64(startTime.UnixNano()),
						EndTimeUnixNano:   uint64(timestamp.Add(time.Second).UnixNano()),
						Name:              spanName,
						Attributes:        params,
						Status:            &tracepb.Status{Code: status},
					},
				},
			},
		},
	}
}
