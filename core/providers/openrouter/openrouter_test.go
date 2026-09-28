package openrouter_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/openrouter"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

func TestOpenRouter(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")) == "" {
		t.Skip("Skipping OpenRouter tests because OPENROUTER_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:             schemas.OpenRouter,
		ChatModel:            "openai/gpt-4.1",
		VisionModel:          "openai/gpt-4o",
		TextModel:            "google/gemini-2.5-flash",
		EmbeddingModel:       "qwen/qwen3-embedding-4b",
		ReasoningModel:       "openai/gpt-oss-120b",
		PromptCachingModel:   "anthropic/claude-sonnet-4", // Claude is the only OpenRouter model with explicit caching; its Responses half was broken until #6290
		TranscriptionModel:   "openai/gpt-4o-mini-transcribe",
		SpeechSynthesisModel: "minimax/speech-2.8-turbo",
		DecisionModel:        "typesafe/jev-1.13",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             true,
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         false, // OpenRouter's responses API is in Beta
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageURL:                   false, // OpenRouter's responses API is in Beta
			ImageBase64:                false, // OpenRouter's responses API is in Beta
			MultipleImages:             false, // OpenRouter's responses API is in Beta
			FileBase64:                 false, // Responses API times out (300s+) with file input
			FileURL:                    false, // Responses API times out (300s+) with file input
			CompleteEnd2End:            false, // OpenRouter's responses API is in Beta
			Reasoning:                  true,
			PromptCaching:              true, // Gates the three tool-block scenarios; two of them issue Responses requests, which is what #6290 broke
			ListModels:                 true,
			StructuredOutputs:          true, // Structured outputs with nullable enum support
			Embedding:                  true,
			SpeechSynthesis:            true,  // Supported via OpenAI-compatible /v1/audio/speech
			SpeechSynthesisStream:      false, // Streaming not offered by upstream OpenRouter API
			Transcription:              true,  // Supported via OpenAI-compatible /v1/audio/transcriptions
			TranscriptionStream:        false, // Streaming not offered by upstream OpenRouter API
			Decision:                   true,  // Native via /api/alpha/decisions for TypeSafe System One models
		},
	}

	t.Run("OpenRouterTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

type messagesTestLogger struct{}

func (messagesTestLogger) Debug(string, ...any)                   {}
func (messagesTestLogger) Info(string, ...any)                    {}
func (messagesTestLogger) Warn(string, ...any)                    {}
func (messagesTestLogger) Error(string, ...any)                   {}
func (messagesTestLogger) Fatal(string, ...any)                   {}
func (messagesTestLogger) SetLevel(schemas.LogLevel)              {}
func (messagesTestLogger) SetOutputType(schemas.LoggerOutputType) {}
func (messagesTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// Exercise the same Anthropic-to-neutral conversion as /anthropic/v1/messages,
// then capture the real provider HTTP request without making a paid API call.
func TestOpenRouterAnthropicMessagesToolCache(t *testing.T) {
	for _, marker := range []string{"tool_use", "tool_result"} {
		for _, streaming := range []bool{false, true} {
			for _, raw := range []bool{false, true} {
				name := marker + "/unary/typed"
				if streaming {
					name = marker + "/stream/typed"
				}
				if raw {
					name = strings.TrimSuffix(name, "typed") + "raw"
				}
				t.Run(name, func(t *testing.T) {
					ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
					ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, raw)
					body := []byte(`{"model":"openrouter/anthropic/claude-sonnet-4","max_tokens":8,"messages":[{"role":"user","content":[{"type":"text","text":"seed"}]},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_stable","name":"weather","input":{"city":"Paris"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_stable","content":"Sunny"}]}],"tools":[{"name":"weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]}`)
					var incoming anthropic.AnthropicMessageRequest
					if err := json.Unmarshal(body, &incoming); err != nil {
						t.Fatal(err)
					}
					index := 1
					if marker == "tool_result" {
						index = 2
					}
					incoming.Messages[index].Content.ContentBlocks[0].CacheControl = &schemas.CacheControl{
						Type: schemas.CacheControlTypeEphemeral,
						TTL:  schemas.Ptr("1h"),
					}
					body, err := schemas.MarshalSorted(incoming)
					if err != nil {
						t.Fatal(err)
					}
					request := incoming.ToBifrostResponsesRequest(ctx)
					request.RawRequestBody = body
					before, err := schemas.MarshalSorted(request)
					if err != nil {
						t.Fatal(err)
					}
					captured := make(chan []byte, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						payload, readErr := io.ReadAll(r.Body)
						if readErr != nil {
							t.Error(readErr)
						}
						if r.URL.Path != "/v1/messages" {
							t.Errorf("tool cache request went to %s, want /v1/messages", r.URL.Path)
						}
						if r.Header.Get("Authorization") != "Bearer test-openrouter-key" {
							t.Error("missing selected OpenRouter bearer key")
						}
						captured <- payload
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"request captured"}}`)
					}))
					defer server.Close()
					provider := openrouter.NewOpenRouterProvider(&schemas.ProviderConfig{
						NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true},
					}, messagesTestLogger{})
					key := schemas.Key{Value: *schemas.NewSecretVar("test-openrouter-key")}
					if streaming {
						_, _ = provider.ResponsesStream(ctx, nil, nil, key, request)
					} else {
						_, _ = provider.Responses(ctx, key, request)
					}
					var payload []byte
					select {
					case payload = <-captured:
					case <-ctx.Done():
						t.Fatal("no upstream request was captured")
					}
					path := "messages.1.content.0.cache_control"
					if marker == "tool_result" {
						path = "messages.2.content.0.cache_control"
					}
					if gjson.GetBytes(payload, path+".type").String() != "ephemeral" || gjson.GetBytes(payload, path+".ttl").String() != "1h" {
						t.Errorf("%s cache marker/TTL lost on wire: %s", marker, payload)
					}
					if gjson.GetBytes(payload, "model").String() != "anthropic/claude-sonnet-4" {
						t.Errorf("wrong upstream model: %s", payload)
					}
					if gjson.GetBytes(payload, "stream").Bool() != streaming {
						t.Errorf("wrong stream flag: %s", payload)
					}
					after, err := schemas.MarshalSorted(request)
					if err != nil {
						t.Fatal(err)
					}
					if string(before) != string(after) || string(request.RawRequestBody) != string(body) {
						t.Error("provider mutated the request reused by retries/fallbacks")
					}
				})
			}
		}
	}
}

func TestOpenRouterAnthropicMessagesRouteIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, integration, model, override, wantPath string
	}{
		{"native Responses Claude", "", "anthropic/claude-sonnet-4", "", "/v1/responses"},
		{"OpenAI integration Claude", "openai", "anthropic/claude-sonnet-4", "", "/v1/responses"},
		{"Anthropic integration GPT", "anthropic", "openai/gpt-4.1", "", "/v1/responses"},
		{"Anthropic integration Claude", "anthropic", "anthropic/claude-sonnet-4", "", "/v1/messages"},
		{"Messages URL override", "anthropic", "anthropic/claude-sonnet-4", "/custom/messages", "/custom/messages"},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/unary", true: "/stream"}[streaming], func(t *testing.T) {
				ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				ctx.SetValue(schemas.BifrostContextKeyIntegrationType, tc.integration)
				if tc.override != "" {
					ctx.SetValue(schemas.BifrostContextKeyURLPath, tc.override)
				}
				paths := make(chan string, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					paths <- r.URL.Path
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"request captured"}}`)
				}))
				defer server.Close()
				provider := openrouter.NewOpenRouterProvider(&schemas.ProviderConfig{
					NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true},
				}, messagesTestLogger{})
				request := &schemas.BifrostResponsesRequest{
					Provider: schemas.OpenRouter,
					Model:    tc.model,
					Input: []schemas.ResponsesMessage{{
						Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
					}},
				}
				if streaming {
					_, _ = provider.ResponsesStream(ctx, nil, nil, schemas.Key{}, request)
				} else {
					_, _ = provider.Responses(ctx, schemas.Key{}, request)
				}
				select {
				case path := <-paths:
					if path != tc.wantPath {
						t.Errorf("upstream path = %s, want %s", path, tc.wantPath)
					}
				case <-ctx.Done():
					t.Fatal("no upstream request was captured")
				}
			})
		}
	}
}

func TestOpenRouterAnthropicMessagesResponseConversion(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "unary", true: "stream"}[streaming], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/messages" {
					t.Errorf("wrong path: %s", r.URL.Path)
				}
				w.Header().Set("x-request-id", "openrouter-test-request")
				if !streaming {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"msg_test","type":"message","role":"assistant","model":"anthropic/claude-sonnet-4","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2,"cache_read_input_tokens":4096,"cache_creation_input_tokens":32}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"anthropic/claude-sonnet-4","content":[],"usage":{"input_tokens":5,"output_tokens":0,"cache_read_input_tokens":4096,"cache_creation_input_tokens":32}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`)
			}))
			defer server.Close()
			provider := openrouter.NewOpenRouterProvider(&schemas.ProviderConfig{
				NetworkConfig:       schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true},
				SendBackRawRequest:  true,
				SendBackRawResponse: true,
			}, messagesTestLogger{})
			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
			request := &schemas.BifrostResponsesRequest{
				Provider: schemas.OpenRouter,
				Model:    "anthropic/claude-sonnet-4",
				Input: []schemas.ResponsesMessage{{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
				}},
			}
			var usage *schemas.ResponsesResponseUsage
			if !streaming {
				response, err := provider.Responses(ctx, schemas.Key{}, request)
				if err != nil {
					t.Fatalf("unary conversion failed: %+v", err)
				}
				if len(response.Output) != 1 || response.Output[0].Content == nil || len(response.Output[0].Content.ContentBlocks) != 1 || response.Output[0].Content.ContentBlocks[0].Text == nil || *response.Output[0].Content.ContentBlocks[0].Text != "OK" {
					t.Fatalf("wrong converted output: %+v", response.Output)
				}
				if response.ExtraFields.RawRequest == nil || response.ExtraFields.RawResponse == nil {
					t.Error("native raw request/response capture missing")
				}
				usage = response.Usage
			} else {
				var hookCalls atomic.Int32
				hook := func(_ *schemas.BifrostContext, response *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
					hookCalls.Add(1)
					return response, err
				}
				stream, err := provider.ResponsesStream(ctx, hook, nil, schemas.Key{}, request)
				if err != nil {
					t.Fatalf("stream request failed: %+v", err)
				}
				text := ""
				completed := false
				for chunk := range stream {
					if chunk.BifrostError != nil {
						t.Fatalf("stream conversion failed: %+v", chunk.BifrostError)
					}
					response := chunk.BifrostResponsesStreamResponse
					if response == nil {
						continue
					}
					if response.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta && response.Delta != nil {
						text += *response.Delta
					}
					if response.Type == schemas.ResponsesStreamResponseTypeCompleted && response.Response != nil {
						completed = true
						usage = response.Response.Usage
					}
				}
				if !completed || text != "OK" || hookCalls.Load() == 0 {
					t.Errorf("incomplete stream: completed=%v, text=%q, hookCalls=%d", completed, text, hookCalls.Load())
				}
			}
			if usage == nil || usage.InputTokensDetails == nil || usage.InputTokensDetails.CachedReadTokens != 4096 || usage.InputTokensDetails.CachedWriteTokens != 32 || usage.OutputTokens != 2 {
				t.Errorf("cache usage lost during conversion: %+v", usage)
			}
		})
	}
}
