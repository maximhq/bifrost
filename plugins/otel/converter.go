package otel

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/pricing"
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

// createResourceSpan creates a new resource span for a Bifrost request
func createResourceSpan(traceID, spanID string, timestamp time.Time, req *schemas.BifrostRequest) *ResourceSpan {
	// preparing parameters
	params := []*KeyValue{}
	spanName := "span"
	// Preparing parameters
	switch req.RequestType {
	case schemas.TextCompletionRequest:
		if req.TextCompletionRequest.Params.MaxTokens != nil {
			params = append(params, kvInt("gen_ai.request.max_tokens", int64(*req.TextCompletionRequest.Params.MaxTokens)))
		}		
		
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		if req.ChatRequest.Params.MaxCompletionTokens != nil {
			params = append(params, kvInt("gen_ai.request.max_tokens", int64(*req.ChatRequest.Params.MaxCompletionTokens)))
		}
		if req.ChatRequest.Params.Temperature != nil {
			params = append(params, kvDbl("gen_ai.request.temperature", *req.ChatRequest.Params.Temperature))
		}
		if req.ChatRequest.Params.TopP != nil {
			params = append(params, kvDbl("gen_ai.request.top_p", *req.ChatRequest.Params.TopP))
		}
		if req.ChatRequest.Params.Stop != nil {
			params = append(params, kvStr("gen_ai.request.stop_sequences", strings.Join(req.ChatRequest.Params.Stop, ",")))
		}
		if req.ChatRequest.Params.PresencePenalty != nil {
			params = append(params, kvDbl("gen_ai.request.presence_penalty", *req.ChatRequest.Params.PresencePenalty))
		}
		if req.ChatRequest.Params.FrequencyPenalty != nil {
			params = append(params, kvDbl("gen_ai.request.frequency_penalty", *req.ChatRequest.Params.FrequencyPenalty))
		}
		if req.ChatRequest.Params.ParallelToolCalls != nil {
			params = append(params, kvBool("gen_ai.request.parallel_tool_calls", *req.ChatRequest.Params.ParallelToolCalls))
		}
		if req.ChatRequest.Params.User != nil {
			params = append(params, kvStr("gen_ai.request.user", *req.ChatRequest.Params.User))
		}
		if req.ChatRequest.Params.ExtraParams != nil {
			for k, v := range req.ChatRequest.Params.ExtraParams {
				params = append(params, kvStr(k, fmt.Sprintf("%v", v)))
			}
		}
	case schemas.EmbeddingRequest:
		if req.EmbeddingRequest.Params.Dimensions != nil {
			params = append(params, kvDbl("gen_ai.request.dimensions", float64(*req.EmbeddingRequest.Params.Dimensions)))
		}
		if req.EmbeddingRequest.Params.EncodingFormat != nil {
			params = append(params, kvStr("gen_ai.request.encoding_format", *req.EmbeddingRequest.Params.EncodingFormat))
		}
		
	}

	// Preparing request based parameters
	if req != nil {
		params = append(params, kvStr("gen_ai.provider.name", string(req.Provider)))
		params = append(params, kvStr("gen_ai.request.model", req.Model))
		// Handling chat completion
		if req.Input.ChatCompletionInput != nil {
			spanName = "gen_ai.chat"
			messages := []*AnyValue{}
			for _, message := range req.Input.ChatCompletionInput {
				switch message.Role {
				case schemas.ModelChatMessageRoleUser:
					kvs := []*KeyValue{kvStr("role", "user")}
					if message.Content.ContentStr != nil {
						kvs = append(kvs, kvStr("content", *message.Content.ContentStr))
					}
					messages = append(messages, listValue(kvs...))
				case schemas.ModelChatMessageRoleAssistant:
					kvs := []*KeyValue{kvStr("role", "assistant")}
					if message.Content.ContentStr != nil {
						kvs = append(kvs, kvStr("content", *message.Content.ContentStr))
					}
					messages = append(messages, listValue(kvs...))
				case schemas.ModelChatMessageRoleSystem:
					if message.Content.ContentStr != nil {
						params = append(params, kvStr("gen_ai.system_instructions", *message.Content.ContentStr))
					}
				}
			}
			params = append(params, kvAny("gen_ai.input.messages", arrValue(messages...)))
		}
		// Handling text completion
		if req.Input.TextCompletionInput != nil {
			spanName = "gen_ai.text"
			params = append(params, kvStr("gen_ai.input.text", *req.Input.TextCompletionInput))
		}
		// Handling embedding
		if req.Input.EmbeddingInput != nil {
			spanName = "gen_ai.embedding"
			if req.Input.EmbeddingInput.Text != nil {
				params = append(params, kvStr("gen_ai.input.text", *req.Input.EmbeddingInput.Text))
			}
			if req.Input.EmbeddingInput.Texts != nil {
				params = append(params, kvStr("gen_ai.input.text", strings.Join(req.Input.EmbeddingInput.Texts, ",")))
			}
			if req.Input.EmbeddingInput.Embedding != nil {
				embedding := make([]string, len(req.Input.EmbeddingInput.Embedding))
				for i, v := range req.Input.EmbeddingInput.Embedding {
					embedding[i] = fmt.Sprintf("%d", v)
				}
				params = append(params, kvStr("gen_ai.input.embedding", strings.Join(embedding, ",")))
			}
			// We don't send across embeddings as they are too large to log and makes no sense to log them
		}
		// Handling speech
		if req.Input.SpeechInput != nil {
			spanName = "gen_ai.speech"
			params = append(params, kvStr("gen_ai.input.speech", req.Input.SpeechInput.Input))
			if req.Input.SpeechInput.VoiceConfig.Voice != nil {
				params = append(params, kvStr("gen_ai.input.speech.voice", *req.Input.SpeechInput.VoiceConfig.Voice))
			}
			params = append(params, kvStr("gen_ai.input.speech.instructions", req.Input.SpeechInput.Instructions))
			params = append(params, kvStr("gen_ai.input.speech.response_format", req.Input.SpeechInput.ResponseFormat))
			if len(req.Input.SpeechInput.VoiceConfig.MultiVoiceConfig) > 0 {
				multiVoiceConfigParams := []*KeyValue{}
				for _, voiceConfig := range req.Input.SpeechInput.VoiceConfig.MultiVoiceConfig {
					multiVoiceConfigParams = append(multiVoiceConfigParams, kvStr("gen_ai.input.speech.voice", voiceConfig.Voice))
				}
				params = append(params, kvAny("gen_ai.input.speech.multi_voice_config", arrValue(listValue(multiVoiceConfigParams...))))
			}
		}
		// Handling transcription
		if req.Input.TranscriptionInput != nil {
			spanName = "gen_ai.transcription"
			params = append(params, kvInt("gen_ai.transcription.fileSize", int64(len(req.Input.TranscriptionInput.File))))
			if req.Input.TranscriptionInput.Language != nil {
				params = append(params, kvStr("gen_ai.input.transcription.language", *req.Input.TranscriptionInput.Language))
			}
			if req.Input.TranscriptionInput.Prompt != nil {
				params = append(params, kvStr("gen_ai.input.transcription.prompt", *req.Input.TranscriptionInput.Prompt))
			}
			if req.Input.TranscriptionInput.ResponseFormat != nil {
				params = append(params, kvStr("gen_ai.input.transcription.response_format", *req.Input.TranscriptionInput.ResponseFormat))
			}
			if req.Input.TranscriptionInput.Format != nil {
				params = append(params, kvStr("gen_ai.input.transcription.format", *req.Input.TranscriptionInput.Format))
			}
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
func completeResourceSpan(span *ResourceSpan, timestamp time.Time, provider schemas.ModelProvider, model string, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError, pricingManager *pricing.PricingManager, requestType schemas.RequestType) *ResourceSpan {
	params := []*commonpb.KeyValue{}
	if resp != nil && resp.Usage != nil {
		params = append(params, kvStr("gen_ai.response.id", resp.ID))
		params = append(params, kvInt("gen_ai.usage.prompt_tokens", int64(resp.Usage.PromptTokens)))
		params = append(params, kvInt("gen_ai.usage.completion_tokens", int64(resp.Usage.CompletionTokens)))
		params = append(params, kvInt("gen_ai.usage.total_tokens", int64(resp.Usage.TotalTokens)))
		if resp.Usage.TokenDetails != nil {
			params = append(params, kvInt("gen_ai.usage.token_details.cached_tokens", int64(resp.Usage.TokenDetails.CachedTokens)))
			params = append(params, kvInt("gen_ai.usage.token_details.audio_tokens", int64(resp.Usage.TokenDetails.AudioTokens)))
		}
		if resp.Usage.CompletionTokensDetails != nil {
			params = append(params, kvInt("gen_ai.usage.completion_tokens_details.reasoning_tokens", int64(resp.Usage.CompletionTokensDetails.ReasoningTokens)))
			params = append(params, kvInt("gen_ai.usage.completion_tokens_details.audio_tokens", int64(resp.Usage.CompletionTokensDetails.AudioTokens)))
			params = append(params, kvInt("gen_ai.usage.completion_tokens_details.accepted_prediction_tokens", int64(resp.Usage.CompletionTokensDetails.AcceptedPredictionTokens)))
			params = append(params, kvInt("gen_ai.usage.completion_tokens_details.rejected_prediction_tokens", int64(resp.Usage.CompletionTokensDetails.RejectedPredictionTokens)))
		}
		// Computing cost
		if pricingManager != nil {
			cost := pricingManager.CalculateCostWithCacheDebug(resp, provider, model, requestType)
			params = append(params, kvStr("gen_ai.usage.cost", fmt.Sprintf("%f", cost)))
		}
	}
	if resp != nil && resp.Speech != nil && resp.Speech.Usage != nil {
		params = append(params, kvInt("gen_ai.usage.input_tokens", int64(resp.Speech.Usage.InputTokens)))
		params = append(params, kvInt("gen_ai.usage.output_tokens", int64(resp.Speech.Usage.OutputTokens)))
		params = append(params, kvInt("gen_ai.usage.total_tokens", int64(resp.Speech.Usage.TotalTokens)))
		if resp.Speech.Usage.InputTokensDetails != nil {
			params = append(params, kvInt("gen_ai.usage.input_tokens_details.text_tokens", int64(resp.Speech.Usage.InputTokensDetails.TextTokens)))
			params = append(params, kvInt("gen_ai.usage.input_tokens_details.audio_tokens", int64(resp.Speech.Usage.InputTokensDetails.AudioTokens)))
		}
	}
	if resp != nil && resp.Transcribe != nil && resp.Transcribe.Usage != nil {
		if resp.Transcribe.Usage.InputTokens != nil {
			params = append(params, kvInt("gen_ai.usage.input_tokens", int64(*resp.Transcribe.Usage.InputTokens)))
		}
		if resp.Transcribe.Usage.OutputTokens != nil {
			params = append(params, kvInt("gen_ai.usage.completion_tokens", int64(*resp.Transcribe.Usage.OutputTokens)))
		}
		if resp.Transcribe.Usage.TotalTokens != nil {
			params = append(params, kvInt("gen_ai.usage.total_tokens", int64(*resp.Transcribe.Usage.TotalTokens)))
		}
		if resp.Transcribe.Usage.InputTokenDetails != nil {
			params = append(params, kvInt("gen_ai.usage.input_token_details.text_tokens", int64(resp.Transcribe.Usage.InputTokenDetails.TextTokens)))
			params = append(params, kvInt("gen_ai.usage.input_token_details.audio_tokens", int64(resp.Transcribe.Usage.InputTokenDetails.AudioTokens)))
		}
	}
	if resp != nil {
		params = append(params, kvStr("gen_ai.chat.object", resp.Object))
		params = append(params, kvStr("gen_ai.text.model", resp.Model))
		params = append(params, kvStr("gen_ai.chat.created", fmt.Sprintf("%d", resp.Created)))
	}
	if resp != nil && resp.SystemFingerprint != nil {
		params = append(params, kvStr("gen_ai.chat.system_fingerprint", *resp.SystemFingerprint))
	}

	if resp != nil {
		switch resp.Object {
		case "chat.completion":
			outputMessages := []*AnyValue{}
			for _, choice := range resp.Choices {
				kvs := []*KeyValue{kvStr("role", string(choice.Message.Role))}
				if choice.Message.Content.ContentStr != nil {
					kvs = append(kvs, kvStr("content", *choice.Message.Content.ContentStr))
				}
				outputMessages = append(outputMessages, listValue(kvs...))
			}
			params = append(params, kvAny("gen_ai.chat.output_messages", arrValue(outputMessages...)))
		case "text.completion":
			outputMessages := []*AnyValue{}
			for _, choice := range resp.Choices {
				kvs := []*KeyValue{kvStr("role", string(choice.Message.Role))}
				if choice.Message.Content.ContentStr != nil {
					kvs = append(kvs, kvStr("content", *choice.Message.Content.ContentStr))
				}
				outputMessages = append(outputMessages, listValue(kvs...))
			}
			params = append(params, kvAny("gen_ai.text.output_messages", arrValue(outputMessages...)))
		case "audio.transcription":
			outputMessages := []*AnyValue{}
			kvs := []*KeyValue{kvStr("text", resp.Transcribe.Text)}
			outputMessages = append(outputMessages, listValue(kvs...))
			params = append(params, kvAny("gen_ai.transcribe.output_messages", arrValue(outputMessages...)))
		}

	}
	// This is a fallback for worst case scenario where latency is not available
	status := tracepb.Status_STATUS_CODE_OK
	if bifrostErr != nil {
		status = tracepb.Status_STATUS_CODE_ERROR
		if bifrostErr.Error.Type != nil {
			params = append(params, kvStr("gen_ai.error.type", *bifrostErr.Error.Type))
		}
		if bifrostErr.Error.Code != nil {
			params = append(params, kvStr("gen_ai.error.code", *bifrostErr.Error.Code))
		}
		params = append(params, kvStr("gen_ai.error", bifrostErr.Error.Message))
	}
	if resp != nil && resp.ExtraFields.BilledUsage != nil {
		if resp.ExtraFields.BilledUsage.PromptTokens != nil {
			params = append(params, kvInt("gen_ai.usage.cost.prompt_tokens", int64(*resp.ExtraFields.BilledUsage.PromptTokens)))
		}
		if resp.ExtraFields.BilledUsage.CompletionTokens != nil {
			params = append(params, kvInt("gen_ai.usage.cost.completion_tokens", int64(*resp.ExtraFields.BilledUsage.CompletionTokens)))
		}
		if resp.ExtraFields.BilledUsage.SearchUnits != nil {
			params = append(params, kvInt("gen_ai.usage.cost.search_units", int64(*resp.ExtraFields.BilledUsage.SearchUnits)))
		}
		if resp.ExtraFields.BilledUsage.Classifications != nil {
			params = append(params, kvInt("gen_ai.usage.cost.classifications", int64(*resp.ExtraFields.BilledUsage.Classifications)))
		}
	}
	span.ScopeSpans[0].Spans[0].Attributes = append(span.ScopeSpans[0].Spans[0].Attributes, params...)
	span.ScopeSpans[0].Spans[0].Status = &tracepb.Status{Code: status}
	span.ScopeSpans[0].Spans[0].EndTimeUnixNano = uint64(timestamp.UnixNano())
	return span
}
