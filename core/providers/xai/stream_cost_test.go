package xai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

type streamCostTestLogger struct{}

func (streamCostTestLogger) Debug(string, ...any)                   {}
func (streamCostTestLogger) Info(string, ...any)                    {}
func (streamCostTestLogger) Warn(string, ...any)                    {}
func (streamCostTestLogger) Error(string, ...any)                   {}
func (streamCostTestLogger) Fatal(string, ...any)                   {}
func (streamCostTestLogger) SetLevel(schemas.LogLevel)              {}
func (streamCostTestLogger) SetOutputType(schemas.LoggerOutputType) {}
func (streamCostTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func passthroughStreamCostPostHook(_ *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, bifrostErr
}

func TestChatCompletionNormalizesUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"id":"chatcmpl-xai",
			"object":"chat.completion",
			"created":1,
			"model":"grok-4.5",
			"choices":[{"index":0,"message":{"role":"assistant","content":"visible answer"},"finish_reason":"stop"}],
			"usage":{
				"prompt_tokens":220,
				"completion_tokens":5,
				"total_tokens":276,
				"completion_tokens_details":{"reasoning_tokens":51}
			}
		}`)
	}))
	defer server.Close()

	provider := &XAIProvider{
		client: &fasthttp.Client{ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second},
		networkConfig: schemas.NetworkConfig{
			BaseURL: schemas.NewSecretVar(server.URL),
		},
		logger: streamCostTestLogger{},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostChatRequest{
		Provider: schemas.XAI,
		Model:    "grok-4.5",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}

	response, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{
		Value: schemas.SecretVar{Val: "test-key"},
	}, request)
	if bifrostErr != nil {
		t.Fatalf("chat completion failed: %v", bifrostErr)
	}
	if response == nil || response.Usage == nil {
		t.Fatal("chat completion did not return usage")
	}
	if got, want := response.Usage.PromptTokens, 220; got != want {
		t.Fatalf("prompt tokens = %d, want %d", got, want)
	}
	if got, want := response.Usage.CompletionTokens, 56; got != want {
		t.Fatalf("completion tokens = %d, want visible + reasoning = %d", got, want)
	}
	if got, want := response.Usage.TotalTokens, 276; got != want {
		t.Fatalf("total tokens = %d, want %d", got, want)
	}
	if response.Usage.CompletionTokensDetails == nil {
		t.Fatal("chat completion did not return completion token details")
	}
	if got, want := response.Usage.CompletionTokensDetails.ReasoningTokens, 51; got != want {
		t.Fatalf("reasoning tokens = %d, want %d", got, want)
	}
	if got, want := response.Usage.TotalTokens, response.Usage.PromptTokens+response.Usage.CompletionTokens; got != want {
		t.Fatalf("total tokens = %d, want prompt + normalized completion = %d", got, want)
	}
}

func TestChatCompletionStreamNormalizesUsageAndPreservesProviderReportedCost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w,
			"data: {\"id\":\"chatcmpl-xai\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"grok-4.5\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"+
				"data: {\"id\":\"chatcmpl-xai\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"grok-4.5\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
				"data: {\"id\":\"chatcmpl-xai\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"grok-4.5\",\"choices\":[],\"usage\":{\"prompt_tokens\":220,\"completion_tokens\":5,\"total_tokens\":276,\"prompt_tokens_details\":{\"cached_tokens\":128},\"completion_tokens_details\":{\"reasoning_tokens\":51},\"cost_in_usd_ticks\":5584000}}\n\n"+
				"data: [DONE]\n\n")
	}))
	defer server.Close()

	client := &fasthttp.Client{ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	provider := &XAIProvider{
		client:          client,
		streamingClient: client,
		networkConfig: schemas.NetworkConfig{
			BaseURL: schemas.NewSecretVar(server.URL),
		},
		logger: streamCostTestLogger{},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostChatRequest{
		Provider: schemas.XAI,
		Model:    "grok-4.5",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}

	stream, bifrostErr := provider.ChatCompletionStream(ctx, passthroughStreamCostPostHook, nil, schemas.Key{
		Value: schemas.SecretVar{Val: "test-key"},
	}, request)
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	var final *schemas.BifrostChatResponse
	for chunk := range stream {
		if chunk.BifrostError != nil {
			t.Fatalf("stream returned error: %+v", chunk.BifrostError)
		}
		if chunk.BifrostChatResponse != nil && chunk.BifrostChatResponse.Usage != nil {
			final = chunk.BifrostChatResponse
		}
	}
	if final == nil {
		t.Fatal("stream did not emit terminal usage")
	}
	if final.Usage.Cost == nil {
		t.Fatal("terminal usage lost xAI provider-reported cost")
	}
	if got, want := final.Usage.Cost.TotalCost, 0.0005584; got != want {
		t.Fatalf("provider-reported cost = %.10f, want %.10f", got, want)
	}
	if got, want := final.Usage.CompletionTokensDetails.ReasoningTokens, 51; got != want {
		t.Fatalf("reasoning tokens = %d, want %d", got, want)
	}
	if got, want := final.Usage.CompletionTokens, 56; got != want {
		t.Fatalf("completion tokens = %d, want %d", got, want)
	}
	if got, want := final.Usage.TotalTokens, final.Usage.PromptTokens+final.Usage.CompletionTokens; got != want {
		t.Fatalf("total tokens = %d, want prompt + completion = %d", got, want)
	}
}

func TestNormalizeXAIChatUsageIsIdempotent(t *testing.T) {
	response := &schemas.BifrostChatResponse{Usage: &schemas.BifrostLLMUsage{
		PromptTokens:     220,
		CompletionTokens: 5,
		TotalTokens:      276,
		CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
			ReasoningTokens: 51,
		},
	}}

	normalizeXAIChatUsage(response)
	normalizeXAIChatUsage(response)

	if got, want := response.Usage.CompletionTokens, 56; got != want {
		t.Fatalf("completion tokens after repeated normalization = %d, want %d", got, want)
	}
}

func TestResponsesStreamPreservesProviderReportedCost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w,
			"data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp-xai\",\"object\":\"response\",\"created_at\":1,\"model\":\"grok-4.5\",\"output\":[],\"usage\":{\"input_tokens\":220,\"output_tokens\":56,\"total_tokens\":276,\"input_tokens_details\":{\"cached_tokens\":128},\"output_tokens_details\":{\"reasoning_tokens\":51},\"cost_in_usd_ticks\":5584000}}}\n\n")
	}))
	defer server.Close()

	client := &fasthttp.Client{ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	provider := &XAIProvider{
		client:          client,
		streamingClient: client,
		networkConfig: schemas.NetworkConfig{
			BaseURL: schemas.NewSecretVar(server.URL),
		},
		logger: streamCostTestLogger{},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.XAI,
		Model:    "grok-4.5",
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}

	stream, bifrostErr := provider.ResponsesStream(ctx, passthroughStreamCostPostHook, nil, schemas.Key{
		Value: schemas.SecretVar{Val: "test-key"},
	}, request)
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	var completed *schemas.BifrostResponsesStreamResponse
	for chunk := range stream {
		if chunk.BifrostError != nil {
			t.Fatalf("stream returned error: %+v", chunk.BifrostError)
		}
		if chunk.BifrostResponsesStreamResponse != nil &&
			chunk.BifrostResponsesStreamResponse.Type == schemas.ResponsesStreamResponseTypeCompleted {
			completed = chunk.BifrostResponsesStreamResponse
		}
	}
	if completed == nil || completed.Response == nil || completed.Response.Usage == nil {
		t.Fatal("stream did not emit completed response usage")
	}
	if completed.Response.Usage.Cost == nil {
		t.Fatal("completed response usage lost xAI provider-reported cost")
	}
	if got, want := completed.Response.Usage.Cost.TotalCost, 0.0005584; got != want {
		t.Fatalf("provider-reported cost = %.10f, want %.10f", got, want)
	}
	if got, want := completed.Response.Usage.OutputTokensDetails.ReasoningTokens, 51; got != want {
		t.Fatalf("reasoning tokens = %d, want %d", got, want)
	}
}
