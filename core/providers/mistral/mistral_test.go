package mistral_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/mistral"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestMistral(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("MISTRAL_API_KEY")) == "" {
		t.Skip("Skipping Mistral tests because MISTRAL_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Mistral,
		ChatModel: "ministral-8b-latest",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Mistral, Model: "ministral-3b-latest"},
		},
		VisionModel:         "pixtral-12b-latest",
		EmbeddingModel:      "codestral-embed",
		TranscriptionModel:  "voxtral-mini-latest", // Mistral's audio transcription model
		ExternalTTSProvider: schemas.OpenAI,
		ExternalTTSModel:    "gpt-4o-mini-tts",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			FileBase64:            false, // supports documents url
			FileURL:               false, // bifrost limitation: native mistral api converter needed
			CompleteEnd2End:       true,
			Embedding:             true,
			Transcription:         true,
			TranscriptionStream:   true,
			ListModels:            true,
			Reasoning:             false, // Not supported right now because we are not using native mistral converters
		},
	}

	t.Run("MistralTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// These SSE fixtures are synthesized from the official Mistral SDK's
// DeltaMessageContent/ThinkChunk contract, not captured incident responses.
// Mistral thinking contains nested text chunks, unlike Anthropic thinking:string.
// All requests terminate at an httptest server and use an explicit dummy key.
func TestMistralContentArray(t *testing.T) {
	cases := []struct {
		name          string
		deltas        []string
		wantText      string
		wantReasoning string
		wantError     bool
		rawDisabled   bool
		customAlias   bool
		chatOnly      bool
		choices       string
		wantToolCall  bool
	}{
		{
			name:          "ThinkingOnly",
			deltas:        []string{`{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"Compute "},{"type":"text","text":"17 times 23."}]}]}`},
			wantReasoning: "Compute 17 times 23.",
		},
		{
			name:     "TextArray",
			deltas:   []string{`{"role":"assistant","content":[{"type":"text","text":"The answer "},{"type":"text","text":"is 391."}]}`},
			wantText: "The answer is 391.",
		},
		{
			name:     "TextArrayAcrossFrames",
			deltas:   []string{`{"role":"assistant","content":[{"type":"text","text":"The answer "}]}`, `{"content":[{"type":"text","text":"is 391."}]}`},
			wantText: "The answer is 391.",
		},
		{
			name: "ThinkingThenText",
			deltas: []string{
				`{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"Compute "},{"type":"text","text":"17 times 23."}]}]}`,
				`{"content":[{"type":"text","text":"The answer "},{"type":"text","text":"is 391."}]}`,
			},
			wantText:      "The answer is 391.",
			wantReasoning: "Compute 17 times 23.",
		},
		{
			name:          "MixedThinkingBeforeText",
			deltas:        []string{`{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"Compute 17 times 23."}]},{"type":"text","text":"391"}]}`},
			wantText:      "391",
			wantReasoning: "Compute 17 times 23.",
		},
		{
			// zai-glm-5-3 shape: empty string deltas around closed thinking bursts, first answer text shares the last thinking frame.
			name: "ClosedThinkingWithEmptyDeltas",
			deltas: []string{
				`{"role":"assistant","content":""}`,
				`{"role":"assistant","index":0,"content":""}`,
				`{"index":0,"content":[{"type":"thinking","thinking":[{"type":"text","text":"The user"}],"closed":true}]}`,
				`{"index":0,"content":""}`,
				`{"index":0,"content":[{"type":"thinking","thinking":[{"type":"text","text":" wants hello."}],"closed":true},{"type":"text","text":"hello"}]}`,
				`{"index":0,"content":" there"}`,
			},
			wantText:      "hello there",
			wantReasoning: "The user wants hello.",
		},
		{
			name:      "MixedTextBeforeThinking",
			deltas:    []string{`{"role":"assistant","content":[{"type":"text","text":"must not leak"},{"type":"thinking","thinking":[{"type":"text","text":"Compute."}]}]}`},
			wantError: true,
		},
		{
			name:      "InvalidSecondChoice",
			choices:   `[{"index":0,"delta":{"content":[{"type":"text","text":"must not leak"}]},"finish_reason":null},{"index":1,"delta":{"content":[{"type":"future_chunk","payload":"not text"}]},"finish_reason":null}]`,
			wantError: true,
		},
		{
			name:     "StringControl",
			deltas:   []string{`{"role":"assistant","content":"The answer is 391."}`},
			wantText: "The answer is 391.",
		},
		{
			name:     "NullControl",
			deltas:   []string{`{"role":"assistant","content":null}`, `{"content":"The answer is 391."}`},
			wantText: "The answer is 391.",
		},
		{
			name:     "OmittedControl",
			deltas:   []string{`{"role":"assistant"}`, `{"content":"The answer is 391."}`},
			wantText: "The answer is 391.",
		},
		{
			name:     "EmptyArrayControl",
			deltas:   []string{`{"role":"assistant","content":[]}`, `{"content":"The answer is 391."}`},
			wantText: "The answer is 391.",
		},
		{
			name:        "TextArrayRawDisabled",
			deltas:      []string{`{"role":"assistant","content":[{"type":"text","text":"391"}]}`},
			wantText:    "391",
			rawDisabled: true,
		},
		{
			name:        "TextArrayCustomAlias",
			deltas:      []string{`{"role":"assistant","content":[{"type":"text","text":"391"}]}`},
			wantText:    "391",
			customAlias: true,
		},
		{
			name:          "ThinkingRawDisabled",
			deltas:        []string{`{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"Compute."}]}]}`},
			wantReasoning: "Compute.",
			rawDisabled:   true,
		},
		{
			name:      "MalformedThinkingBlock",
			deltas:    []string{`{"content":[{"type":"thinking","thinking":"Compute."}]}`},
			wantError: true,
		},
		{
			name:      "NontextThinkingChunk",
			deltas:    []string{`{"content":[{"type":"thinking","thinking":[{"type":"image_url","image_url":"https://example.invalid/image.png"}]}]}`},
			wantError: true,
		},
		{
			name:      "NontextArray",
			deltas:    []string{`{"content":[{"type":"image_url","image_url":"https://example.invalid/image.png"}]}`},
			wantError: true,
		},
		{
			name:      "UnknownArrayBlock",
			deltas:    []string{`{"content":[{"type":"future_chunk","payload":"not text"}]}`},
			wantError: true,
		},
		{
			name:      "MalformedTextBlock",
			deltas:    []string{`{"content":[{"type":"text"}]}`},
			wantError: true,
		},
		{
			name:      "NumericContent",
			deltas:    []string{`{"content":42}`},
			wantError: true,
		},
		{
			name:      "MalformedJSON",
			deltas:    []string{`{"content":`},
			wantError: true,
		},
		{
			name:         "MultipleChoicesAndToolCalls",
			choices:      `[{"index":0,"delta":{"role":"assistant","content":[{"type":"text","text":"first"}],"tool_calls":[{"index":0,"id":"call-local","type":"function","function":{"name":"local_tool","arguments":"{}"}}]},"finish_reason":null},{"index":1,"delta":{"content":[{"type":"text","text":"second"}]},"finish_reason":null}]`,
			wantText:     "firstsecond",
			wantToolCall: true,
			chatOnly:     true, // The existing chat-to-Responses fallback maps only choice zero.
		},
	}
	for _, api := range []string{"ChatCompletionStream", "ResponsesStream"} {
		t.Run(api, func(t *testing.T) {
			for _, tc := range cases {
				if tc.chatOnly && api != "ChatCompletionStream" {
					continue
				}
				t.Run(tc.name, func(t *testing.T) {
					frames := make([]string, 0, len(tc.deltas)+1)
					for _, delta := range tc.deltas {
						frames = append(frames, mistralContentArrayFrame(`[{"index":0,"delta":`+delta+`,"finish_reason":null}]`))
					}
					if tc.choices != "" {
						frames = append(frames, mistralContentArrayFrame(tc.choices))
					}
					frames = append(frames, mistralContentArrayFinishFrame)
					server := mistralContentArrayServer(t, frames)
					defer server.Close()
					config := &schemas.ProviderConfig{
						NetworkConfig:       schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true},
						SendBackRawResponse: !tc.rawDisabled,
					}
					providerName := schemas.Mistral
					if tc.customAlias {
						providerName = schemas.ModelProvider("offline-mistral-alias")
						config.CustomProviderConfig = &schemas.CustomProviderConfig{CustomProviderKey: string(providerName), BaseProviderType: schemas.Mistral}
					}
					provider := mistral.NewMistralProvider(config, mistralContentArrayLogger{t})
					if provider.GetProviderKey() != providerName {
						t.Fatalf("provider alias changed: got %q, want %q", provider.GetProviderKey(), providerName)
					}
					parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
					key := schemas.Key{Value: *schemas.NewSecretVar("offline-mistral-content-array-key")}
					var hookCalls, terminalHookCalls, errorHookCalls, finalizerCalls atomic.Int32
					finalized := make(chan struct{})
					finalizer := func(context.Context) {
						if finalizerCalls.Add(1) == 1 {
							close(finalized)
						}
					}
					postHook := func(ctx *schemas.BifrostContext, response *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
						hookCalls.Add(1)
						if err != nil {
							errorHookCalls.Add(1)
						}
						if final, _ := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator).(bool); final {
							terminalHookCalls.Add(1)
						}
						return response, err
					}
					chatRequest := &schemas.BifrostChatRequest{
						Provider: providerName,
						Model:    "magistral-small-latest",
						Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is 17 times 23?")}}},
					}
					var stream chan *schemas.BifrostStreamChunk
					var startupError *schemas.BifrostError
					if api == "ChatCompletionStream" {
						stream, startupError = provider.ChatCompletionStream(ctx, postHook, finalizer, key, chatRequest)
					} else {
						stream, startupError = provider.ResponsesStream(ctx, postHook, finalizer, key, chatRequest.ToResponsesRequest())
					}
					if startupError != nil || stream == nil {
						t.Fatalf("local stream did not start: %+v", startupError)
					}
					chunks := mistralContentArrayDrain(t, stream)
					if len(chunks) == 0 {
						t.Fatal("stream closed without chunks")
					}
					select {
					case <-finalized:
					case <-time.After(time.Second):
						t.Fatal("stream finalizer not called")
					}
					wantErrors := int32(0)
					if tc.wantError {
						wantErrors = 1
					}
					if int(hookCalls.Load()) != len(chunks) || errorHookCalls.Load() != wantErrors || terminalHookCalls.Load() != 1 || finalizerCalls.Load() != 1 {
						t.Errorf("post-hook lifecycle: calls=%d chunks=%d errors=%d terminal=%d", hookCalls.Load(), len(chunks), errorHookCalls.Load(), terminalHookCalls.Load())
					}
					var text, reasoning strings.Builder
					finishCount, usageCount, completedCount, errorCount, toolCount := 0, 0, 0, 0, 0
					var rawFrames []string
					choiceIndices := map[int]bool{}
					var anthropicEvents []*anthropic.AnthropicStreamEvent
					conversionContext := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
					for _, chunk := range chunks {
						if chunk.BifrostError != nil {
							errorCount++
							if !tc.wantError || chunk.BifrostError.Error == nil || chunk.BifrostError.Error.Message != schemas.ErrProviderResponseUnmarshal || chunk.BifrostError.Error.Error == nil {
								t.Errorf("unexpected late stream error: %+v", chunk.BifrostError)
							}
							rawFrames = append(rawFrames, mistralContentArrayRaw(t, chunk.BifrostError.ExtraFields.RawResponse))
							if api == "ResponsesStream" {
								wireError := anthropic.ToAnthropicResponsesStreamError(chunk.BifrostError)
								var convertedError anthropic.AnthropicMessageError
								payload := strings.TrimSpace(strings.TrimPrefix(wireError, "event: error\ndata: "))
								if !strings.HasPrefix(wireError, "event: error\ndata: ") || json.Unmarshal([]byte(payload), &convertedError) != nil || convertedError.Type != "error" || convertedError.Error.Type != "api_error" || convertedError.Error.Message != schemas.ErrProviderResponseUnmarshal {
									t.Errorf("Anthropic stream lost clear provider error: %s", wireError)
								}
							}
						}
						if chat := chunk.BifrostChatResponse; chat != nil {
							rawFrames = append(rawFrames, mistralContentArrayRaw(t, chat.ExtraFields.RawResponse))
							if chat.ID != "mistral-array-stream" || chat.Model != "magistral-small-latest" || chat.Created != 1 {
								t.Errorf("chat envelope changed: id=%q model=%q created=%d", chat.ID, chat.Model, chat.Created)
							}
							for _, choice := range chat.Choices {
								if choice.ChatStreamResponseChoice != nil && choice.Delta != nil {
									for _, call := range choice.Delta.ToolCalls {
										toolCount++
										if call.ID == nil || *call.ID != "call-local" || call.Function.Name == nil || *call.Function.Name != "local_tool" || call.Function.Arguments != "{}" {
											t.Errorf("tool-call metadata changed: %+v", call)
										}
									}
									if choice.Delta.Content != nil {
										text.WriteString(*choice.Delta.Content)
										choiceIndices[choice.Index] = true
									}
									if choice.Delta.Reasoning != nil {
										reasoning.WriteString(*choice.Delta.Reasoning)
									}
								}
								if choice.FinishReason != nil {
									finishCount++
									if *choice.FinishReason != "stop" {
										t.Errorf("finish reason = %q", *choice.FinishReason)
									}
								}
							}
							if chat.Usage != nil {
								usageCount++
								if chat.Usage.PromptTokens != 5 || chat.Usage.CompletionTokens != 8 || chat.Usage.TotalTokens != 13 {
									t.Errorf("chat terminal usage = %+v", chat.Usage)
								}
							}
						}
						if response := chunk.BifrostResponsesStreamResponse; response != nil {
							rawFrames = append(rawFrames, mistralContentArrayRaw(t, response.ExtraFields.RawResponse))
							anthropicEvents = append(anthropicEvents, anthropic.ToAnthropicResponsesStreamResponse(conversionContext, response)...)
							switch response.Type {
							case schemas.ResponsesStreamResponseTypeOutputTextDelta:
								if response.Delta != nil {
									text.WriteString(*response.Delta)
								}
							case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta:
								if response.Delta != nil {
									reasoning.WriteString(*response.Delta)
								}
							case schemas.ResponsesStreamResponseTypeCompleted:
								completedCount++
								if response.Response == nil || response.Response.Usage == nil {
									t.Error("completed event has no terminal usage")
								} else if u := response.Response.Usage; u.InputTokens != 5 || u.OutputTokens != 8 || u.TotalTokens != 13 {
									t.Errorf("responses terminal usage = %+v", u)
								}
							}
						}
					}
					if text.String() != tc.wantText {
						t.Errorf("answer content lost: got %q, want %q", text.String(), tc.wantText)
					}
					if reasoning.String() != tc.wantReasoning {
						t.Errorf("reasoning content lost: got %q, want %q", reasoning.String(), tc.wantReasoning)
					}
					if tc.wantError {
						if errorCount != 1 || finishCount != 0 || usageCount != 0 || completedCount != 0 || chunks[len(chunks)-1].BifrostError == nil {
							t.Errorf("unsupported content must end with one error and no success: errors=%d finish=%d usage=%d completed=%d", errorCount, finishCount, usageCount, completedCount)
						}
						for _, event := range anthropicEvents {
							if event.Type == anthropic.AnthropicStreamEventTypeMessageDelta || event.Type == anthropic.AnthropicStreamEventTypeMessageStop {
								t.Errorf("Anthropic error stream emitted successful termination: %s", event.Type)
							}
						}
					} else if api == "ChatCompletionStream" {
						if finishCount != 1 || usageCount != 1 {
							t.Errorf("chat terminal lifecycle: finish=%d usage=%d", finishCount, usageCount)
						}
					} else {
						if completedCount != 1 {
							t.Errorf("completed event count = %d, want 1", completedCount)
						}
						mistralContentArrayAnthropicLifecycle(t, anthropicEvents, tc.wantText, tc.wantReasoning)
					}
					if tc.wantToolCall && toolCount != 1 {
						t.Errorf("tool-call count = %d, want 1", toolCount)
					}
					if tc.choices != "" && !tc.wantError && (!choiceIndices[0] || !choiceIndices[1]) {
						t.Errorf("multiple-choice indices lost: %v", choiceIndices)
					}
					joinedRaw := strings.Join(rawFrames, "\n\n")
					if tc.rawDisabled {
						if strings.TrimSpace(joinedRaw) != "" {
							t.Errorf("raw capture disabled but emitted %q", joinedRaw)
						}
					} else {
						wantRaw := frames
						if tc.wantError {
							wantRaw = frames[:1]
						}
						for _, frame := range wantRaw {
							if count := strings.Count(joinedRaw, frame); count != 1 {
								t.Errorf("original raw frame appears %d times, want 1: %s", count, frame)
							}
						}
					}
					t.Logf("drained=%d hooks=%d terminal_hooks=%d answer=%q reasoning=%q finish=%d usage=%d completed=%d", len(chunks), hookCalls.Load(), terminalHookCalls.Load(), text.String(), reasoning.String(), finishCount, usageCount, completedCount)
				})
			}
		})
	}
}

func TestMistralNonStreamingContentArray(t *testing.T) {
	cases := []struct {
		name, message, text, reasoning string
		wantError, wantToolCall        bool
	}{
		{
			name:      "ThinkingAndText",
			message:   `{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"Compute "},{"type":"text","text":"17 times 23."}]},{"type":"text","text":"3"},{"type":"text","text":"91"}]}`,
			text:      "391",
			reasoning: "Compute 17 times 23.",
		},
		{
			name:      "ThinkingOnly",
			message:   `{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"Compute."}]}]}`,
			reasoning: "Compute.",
		},
		{
			name:    "StringControl",
			message: `{"role":"assistant","content":"391"}`,
			text:    "391",
		},
		{
			name:    "TextArrayControl",
			message: `{"role":"assistant","content":[{"type":"text","text":"391"}]}`,
			text:    "391",
		},
		{
			name:         "ThinkingWithToolCall",
			message:      `{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"Use a tool."}]}],"tool_calls":[{"id":"call-local","type":"function","function":{"name":"local_tool","arguments":"{}"}}]}`,
			reasoning:    "Use a tool.",
			wantToolCall: true,
		},
		{
			name:      "MalformedThinking",
			message:   `{"role":"assistant","content":[{"type":"thinking","thinking":"not a chunk array"}]}`,
			wantError: true,
		},
	}
	for _, api := range []string{"ChatCompletion", "Responses", "Anthropic"} {
		for _, mode := range []struct {
			name       string
			raw, alias bool
		}{{name: "Raw", raw: true}, {name: "NoRaw"}, {name: "CustomAlias", raw: true, alias: true}} {
			for _, tc := range cases {
				t.Run(api+"/"+mode.name+"/"+tc.name, func(t *testing.T) {
					body := mistralUnaryResponse(tc.message)
					provider, providerName := mistralUnaryProvider(t, body, mode.raw, mode.alias)
					ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
					key := schemas.Key{Value: *schemas.NewSecretVar("offline-mistral-key")}
					request := &schemas.BifrostChatRequest{
						Provider: providerName,
						Model:    "mistral-small-latest",
						Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is 17 times 23?")}}},
					}
					var response any
					var bifrostErr *schemas.BifrostError
					if api == "ChatCompletion" {
						response, bifrostErr = provider.ChatCompletion(ctx, key, request)
					} else if api == "Responses" {
						response, bifrostErr = provider.Responses(ctx, key, request.ToResponsesRequest())
					} else {
						var native anthropic.AnthropicMessageRequest
						require.NoError(t, json.Unmarshal([]byte(`{"model":"`+string(providerName)+`/mistral-small-latest","max_tokens":1024,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"What is 17 times 23?"}]}`), &native))
						result, err := provider.Responses(ctx, key, native.ToBifrostResponsesRequest(ctx))
						bifrostErr = err
						if err == nil {
							response = anthropic.ToAnthropicResponsesResponse(ctx, result)
						}
					}
					if tc.wantError {
						require.NotNil(t, bifrostErr)
						require.Equal(t, schemas.ErrProviderResponseUnmarshal, bifrostErr.Error.Message)
						if mode.raw {
							require.JSONEq(t, body, mistralContentArrayRaw(t, bifrostErr.ExtraFields.RawResponse))
						}
						return
					}
					require.Nil(t, bifrostErr)
					encoded, err := json.Marshal(response)
					require.NoError(t, err)
					wire := gjson.ParseBytes(encoded)
					require.Equal(t, "mistral-unary", wire.Get("id").String())
					if mode.raw {
						require.JSONEq(t, body, wire.Get("extra_fields.raw_response").Raw)
					} else {
						require.False(t, wire.Get("extra_fields.raw_response").Exists())
					}
					var text, reasoning strings.Builder
					var toolCallID string
					if api == "ChatCompletion" {
						message := wire.Get("choices.0.message")
						reasoning.WriteString(message.Get("reasoning").String())
						content := message.Get("content")
						if content.IsArray() {
							for _, block := range content.Array() {
								require.Equal(t, "text", block.Get("type").String())
								text.WriteString(block.Get("text").String())
							}
						} else {
							text.WriteString(content.String())
						}
						toolCallID = message.Get("tool_calls.0.id").String()
						require.Equal(t, int64(10), wire.Get("usage.prompt_tokens").Int())
					} else if api == "Responses" {
						for _, item := range wire.Get("output").Array() {
							switch item.Get("type").String() {
							case "reasoning":
								for _, block := range item.Get("content").Array() {
									require.Equal(t, "reasoning_text", block.Get("type").String())
									reasoning.WriteString(block.Get("text").String())
								}
							case "message":
								for _, block := range item.Get("content").Array() {
									require.Equal(t, "output_text", block.Get("type").String())
									text.WriteString(block.Get("text").String())
								}
							case "function_call":
								toolCallID = item.Get("call_id").String()
							}
						}
						require.Equal(t, int64(10), wire.Get("usage.input_tokens").Int())
					} else {
						for _, block := range wire.Get("content").Array() {
							switch block.Get("type").String() {
							case "thinking":
								reasoning.WriteString(block.Get("thinking").String())
							case "text":
								text.WriteString(block.Get("text").String())
							case "tool_use":
								toolCallID = block.Get("id").String()
							}
						}
						require.Equal(t, int64(10), wire.Get("usage.input_tokens").Int())
					}
					require.Equal(t, tc.text, text.String())
					require.Equal(t, tc.reasoning, reasoning.String())
					if tc.wantToolCall {
						require.Equal(t, "call-local", toolCallID)
					}
				})
			}
		}
	}
}

func mistralUnaryResponse(message string) string {
	return `{"id":"mistral-unary","object":"chat.completion","created":1,"model":"mistral-small-latest","choices":[{"index":0,"message":` + message + `,"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`
}

func mistralUnaryProvider(t testing.TB, body string, raw, alias bool) (*mistral.MistralProvider, schemas.ModelProvider) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	config := &schemas.ProviderConfig{
		NetworkConfig:      schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true},
		SendBackRawRequest: raw, SendBackRawResponse: raw,
	}
	name := schemas.Mistral
	if alias {
		name = schemas.ModelProvider("offline-mistral-alias")
		config.CustomProviderConfig = &schemas.CustomProviderConfig{CustomProviderKey: string(name), BaseProviderType: schemas.Mistral}
	}
	return mistral.NewMistralProvider(config, mistralContentArrayLogger{t}), name
}

func BenchmarkMistralNonStreamingContent(b *testing.B) {
	for _, tc := range []struct{ name, message string }{
		{"String", `{"role":"assistant","content":"391"}`},
		{"Thinking", `{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"Compute 17 times 23."}]},{"type":"text","text":"391"}]}`},
	} {
		b.Run(tc.name, func(b *testing.B) {
			provider, name := mistralUnaryProvider(b, mistralUnaryResponse(tc.message), false, false)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			key := schemas.Key{Value: *schemas.NewSecretVar("offline-mistral-key")}
			request := &schemas.BifrostChatRequest{Provider: name, Model: "mistral-small-latest", Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is 17 times 23?")}}}}
			for range 10 {
				if response, err := provider.ChatCompletion(ctx, key, request); err != nil || response == nil {
					b.Fatalf("local chat warmup failed: %+v", err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				response, err := provider.ChatCompletion(ctx, key, request)
				if err != nil || response == nil {
					b.Fatalf("local chat request failed: %+v", err)
				}
			}
		})
	}
}

const mistralContentArrayFinishFrame = `{"id":"mistral-array-stream","object":"chat.completion.chunk","created":1,"model":"magistral-small-latest","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":8,"total_tokens":13}}`

func mistralContentArrayFrame(choices string) string {
	return `{"id":"mistral-array-stream","object":"chat.completion.chunk","created":1,"model":"magistral-small-latest","choices":` + choices + `}`
}

func mistralContentArrayRaw(t *testing.T, raw any) string {
	t.Helper()
	switch value := raw.(type) {
	case nil:
		return ""
	case string:
		return value
	case json.RawMessage:
		return string(value)
	case []byte:
		return string(value)
	default:
		t.Errorf("raw response was decoded rather than preserved: %T", raw)
		return ""
	}
}

func mistralContentArrayServer(t *testing.T, frames []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer offline-mistral-content-array-key" {
			t.Errorf("unexpected local request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["stream"] != true || request["model"] != "magistral-small-latest" {
			t.Errorf("expected actual streaming request: body=%+v error=%v", request, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, frame := range frames {
			_, err := fmt.Fprintf(w, "data: %s\n\n", frame)
			if err != nil {
				t.Errorf("write local SSE: %v", err)
				return
			}
			flusher.Flush()
		}
		_, err := fmt.Fprint(w, "data: [DONE]\n\n")
		if err != nil {
			t.Errorf("write local terminal SSE: %v", err)
		}
		flusher.Flush()
	}))
}

func mistralContentArrayDrain(t *testing.T, stream <-chan *schemas.BifrostStreamChunk) []*schemas.BifrostStreamChunk {
	t.Helper()
	var chunks []*schemas.BifrostStreamChunk
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case chunk, open := <-stream:
			if !open {
				return chunks
			}
			if chunk == nil {
				t.Error("nil stream chunk")
				continue
			}
			chunks = append(chunks, chunk)
		case <-timer.C:
			t.Fatal("local stream did not close; this is not a content-array assertion reproduction")
		}
	}
}

func mistralContentArrayAnthropicLifecycle(t *testing.T, events []*anthropic.AnthropicStreamEvent, wantText, wantReasoning string) {
	t.Helper()
	open := map[int]anthropic.AnthropicContentBlockType{}
	starts, stops := map[int]int{}, map[int]int{}
	var blockTypes []anthropic.AnthropicContentBlockType
	var text, reasoning strings.Builder
	messageStarts, messageDeltas, messageStops := 0, 0, 0
	for _, event := range events {
		switch event.Type {
		case anthropic.AnthropicStreamEventTypeMessageStart:
			messageStarts++
		case anthropic.AnthropicStreamEventTypeMessageDelta:
			messageDeltas++
			if event.Usage == nil || event.Usage.InputTokens != 5 || event.Usage.OutputTokens != 8 || event.Delta == nil || event.Delta.StopReason == nil || *event.Delta.StopReason != anthropic.AnthropicStopReasonEndTurn {
				t.Errorf("Anthropic terminal usage/stop reason changed: usage=%+v delta=%+v", event.Usage, event.Delta)
			}
		case anthropic.AnthropicStreamEventTypeMessageStop:
			messageStops++
		case anthropic.AnthropicStreamEventTypeContentBlockStart:
			if event.Index == nil || event.ContentBlock == nil {
				t.Error("Anthropic block start lacks index/type")
				continue
			}
			index := *event.Index
			starts[index]++
			if starts[index] != 1 {
				t.Errorf("Anthropic block %d started more than once", index)
			}
			open[index] = event.ContentBlock.Type
			blockTypes = append(blockTypes, event.ContentBlock.Type)
		case anthropic.AnthropicStreamEventTypeContentBlockDelta:
			if event.Index == nil || event.Delta == nil {
				t.Error("Anthropic delta lacks index/payload")
				continue
			}
			blockType, exists := open[*event.Index]
			if !exists {
				t.Errorf("Anthropic delta targets unopened or closed block %d", *event.Index)
			}
			switch event.Delta.Type {
			case anthropic.AnthropicStreamDeltaTypeText:
				if blockType != anthropic.AnthropicContentBlockTypeText {
					t.Errorf("text delta targets %q", blockType)
				}
				if event.Delta.Text != nil {
					text.WriteString(*event.Delta.Text)
				}
			case anthropic.AnthropicStreamDeltaTypeThinking:
				if blockType != anthropic.AnthropicContentBlockTypeThinking {
					t.Errorf("thinking delta targets %q", blockType)
				}
				if event.Delta.Thinking != nil {
					reasoning.WriteString(*event.Delta.Thinking)
				}
			default:
				t.Errorf("unexpected Anthropic delta type %q", event.Delta.Type)
			}
		case anthropic.AnthropicStreamEventTypeContentBlockStop:
			if event.Index == nil {
				t.Error("Anthropic block stop lacks index")
				continue
			}
			index := *event.Index
			stops[index]++
			if _, exists := open[index]; !exists || stops[index] != 1 {
				t.Errorf("Anthropic block %d stopped without one open start", index)
			}
			delete(open, index)
		}
	}
	if len(open) != 0 || len(starts) != len(stops) {
		t.Errorf("Anthropic unclosed blocks: open=%v starts=%v stops=%v", open, starts, stops)
	}
	if messageStarts != 1 || messageDeltas != 1 || messageStops != 1 {
		t.Errorf("Anthropic message lifecycle: starts=%d deltas=%d stops=%d", messageStarts, messageDeltas, messageStops)
	}
	if len(events) == 0 || events[len(events)-1].Type != anthropic.AnthropicStreamEventTypeMessageStop {
		t.Error("Anthropic message_stop is not terminal")
	}
	var wantTypes []anthropic.AnthropicContentBlockType
	if wantReasoning != "" {
		wantTypes = append(wantTypes, anthropic.AnthropicContentBlockTypeThinking)
	}
	if wantText != "" {
		wantTypes = append(wantTypes, anthropic.AnthropicContentBlockTypeText)
	}
	if fmt.Sprint(blockTypes) != fmt.Sprint(wantTypes) {
		t.Errorf("Anthropic block order: got %v, want %v", blockTypes, wantTypes)
	}
	if text.String() != wantText {
		t.Errorf("Anthropic answer content lost: got %q, want %q", text.String(), wantText)
	}
	if reasoning.String() != wantReasoning {
		t.Errorf("Anthropic reasoning content lost: got %q, want %q", reasoning.String(), wantReasoning)
	}
	t.Logf("Anthropic converted=%d block_types=%v answer=%q reasoning=%q", len(events), blockTypes, text.String(), reasoning.String())
}

type mistralContentArrayLogger struct{ t testing.TB }

func (l mistralContentArrayLogger) Debug(string, ...any) {}
func (l mistralContentArrayLogger) Info(string, ...any)  {}
func (l mistralContentArrayLogger) Warn(message string, args ...any) {
	l.t.Logf("provider warning: "+message, args...)
}
func (l mistralContentArrayLogger) Error(message string, args ...any) {
	l.t.Logf("provider error: "+message, args...)
}
func (l mistralContentArrayLogger) Fatal(message string, args ...any) {
	l.t.Errorf("provider fatal: "+message, args...)
}
func (l mistralContentArrayLogger) SetLevel(schemas.LogLevel)              {}
func (l mistralContentArrayLogger) SetOutputType(schemas.LoggerOutputType) {}
func (l mistralContentArrayLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// A retrieve must land on /v1/models/{model} with the key's bearer token, map Mistral's
// model card onto the Bifrost shape, surface an upstream 404, and refuse a path-shaping id.
func TestMistralModelRetrieve(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath, gotMethod, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath, gotMethod, gotAuth = r.URL.Path, r.Method, r.Header.Get("Authorization")
		mu.Unlock()
		if r.URL.Path == "/v1/models/nope" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"object":"error","message":"Invalid model: nope","type":"invalid_model","code":"1500"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"mistral-large-2411","object":"model","created":1731974400,"owned_by":"mistralai","name":"Mistral Large","description":"Top-tier reasoning model","max_context_length":131072,"aliases":["mistral-large-latest"],"capabilities":{"completion_chat":true,"function_calling":true},"type":"base"}`))
	}))
	defer server.Close()

	provider := mistral.NewMistralProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("mistral-test")}

	// Retrieving an alias returns the concrete model it points at.
	response, bifrostErr := provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "mistral-large-latest"})
	require.Nil(t, bifrostErr)

	mu.Lock()
	path, method, auth := gotPath, gotMethod, gotAuth
	mu.Unlock()
	require.Equal(t, "/v1/models/mistral-large-latest", path)
	require.Equal(t, http.MethodGet, method)
	require.Equal(t, "Bearer mistral-test", auth)

	require.Equal(t, "mistral/mistral-large-2411", response.ID)
	require.Equal(t, schemas.Ptr("Mistral Large"), response.Name)
	require.Equal(t, schemas.Ptr(131072), response.ContextLength)
	require.Equal(t, schemas.Ptr(int64(1731974400)), response.Created)
	require.Equal(t, schemas.Ptr("mistralai"), response.OwnedBy)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "nope"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusNotFound), bifrostErr.StatusCode)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "../models"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusBadRequest), bifrostErr.StatusCode)
}
