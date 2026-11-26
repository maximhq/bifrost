package dagon_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/dagon"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestDagon runs the shared live scenario suite against the Dagon API. It is skipped when
// DAGON_API_KEY is not set.
func TestDagon(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("DAGON_API_KEY")) == "" {
		t.Skip("Skipping Dagon tests because DAGON_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:       schemas.Dagon,
		ChatModel:      "abliterated-model",
		ReasoningModel: "arcium-blackthorn/qwen3.8-27b-bllm",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:        false, // Dagon does not serve /v1/completions
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			Embedding:             false, // Dagon does not serve /v1/embeddings
			ListModels:            true,
			Reasoning:             true,
		},
	}

	t.Run("DagonTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

type capturedRequest struct {
	method string
	path   string
	auth   string
	body   []byte
}

// newMockDagon starts a server that records each request and replies with the handler registered for "METHOD /path".
func newMockDagon(t *testing.T, handlers map[string]http.HandlerFunc) (*dagon.DagonProvider, func() []capturedRequest) {
	t.Helper()
	var mu sync.Mutex
	var captured []capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		captured = append(captured, capturedRequest{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body})
		mu.Unlock()
		if handler, ok := handlers[r.Method+" "+r.URL.Path]; ok {
			handler(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	provider, err := dagon.NewDagonProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL + "/",
			DefaultRequestTimeoutInSeconds: 10,
			AllowPrivateNetwork:            true,
		},
	}, bifrost.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewDagonProvider: %v", err)
	}
	return provider, func() []capturedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedRequest(nil), captured...)
	}
}

func jsonResponse(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

// sseResponse writes each event as-is and flushes, the way a live stream sends them.
func sseResponse(events ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, event := range events {
			_, _ = w.Write([]byte(event))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func testContext(t *testing.T) *schemas.BifrostContext {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func testKey() schemas.Key {
	return schemas.Key{Value: *schemas.NewSecretVar("test-key"), Models: []string{"*"}}
}

func anthropicKey() schemas.Key {
	key := testKey()
	key.UseAnthropicEndpoints = schemas.Ptr(true)
	return key
}

const namespacedModel = "arcium-blackthorn/qwen3.8-27b-bllm"

func chatRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Dagon,
		Model:    namespacedModel,
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
	}
}

func responsesRequest() *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.Dagon,
		Model:    namespacedModel,
		Input:    []schemas.ResponsesMessage{llmtests.CreateBasicResponsesMessage("hi")},
	}
}

func passThroughPostHook(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

const (
	chatBody      = `{"id":"c1","object":"chat.completion","created":1,"model":"arcium-blackthorn/qwen3.8-27b-bllm","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
	responsesBody = `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"arcium-blackthorn/qwen3.8-27b-bllm","output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`
	messagesBody  = `{"id":"msg_1","type":"message","role":"assistant","model":"arcium-blackthorn/qwen3.8-27b-bllm","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`
	modelsBody    = `{"object":"list","data":[{"id":"abliterated-model","object":"model","created":0,"owned_by":"dagon"},{"id":"arcium-blackthorn/qwen3.8-27b-bllm","object":"model","created":0,"owned_by":"dagon"}]}`
)

// TestDagonDefaultBaseURL checks that the constructor fills in Dagon's base URL when the
// config leaves it empty.
func TestDagonDefaultBaseURL(t *testing.T) {
	config := &schemas.ProviderConfig{}
	if _, err := dagon.NewDagonProvider(config, bifrost.NewNoOpLogger()); err != nil {
		t.Fatalf("NewDagonProvider: %v", err)
	}
	if config.NetworkConfig.BaseURL != "https://inference.dagon.so" {
		t.Fatalf("BaseURL = %q, want https://inference.dagon.so", config.NetworkConfig.BaseURL)
	}
}

// TestDagonRequestPaths pins the upstream path and auth of every supported operation, on both
// the OpenAI-compatible surface and the opt-in Anthropic-compatible one, and checks that a
// namespaced model id travels in the body unchanged.
func TestDagonRequestPaths(t *testing.T) {
	tests := []struct {
		name     string
		wantCall string
		run      func(t *testing.T, provider *dagon.DagonProvider)
	}{
		{
			name:     "ChatCompletion",
			wantCall: "POST /v1/chat/completions",
			run: func(t *testing.T, provider *dagon.DagonProvider) {
				if _, err := provider.ChatCompletion(testContext(t), testKey(), chatRequest()); err != nil {
					t.Fatalf("ChatCompletion: %v", llmtests.GetErrorMessage(err))
				}
			},
		},
		{
			name:     "Responses",
			wantCall: "POST /v1/responses",
			run: func(t *testing.T, provider *dagon.DagonProvider) {
				resp, err := provider.Responses(testContext(t), testKey(), responsesRequest())
				if err != nil {
					t.Fatalf("Responses: %v", llmtests.GetErrorMessage(err))
				}
				if resp == nil || len(resp.Output) == 0 {
					t.Fatalf("expected responses output, got %#v", resp)
				}
			},
		},
		{
			name:     "ChatCompletionAnthropicEndpoint",
			wantCall: "POST /v1/messages",
			run: func(t *testing.T, provider *dagon.DagonProvider) {
				resp, err := provider.ChatCompletion(testContext(t), anthropicKey(), chatRequest())
				if err != nil {
					t.Fatalf("ChatCompletion: %v", llmtests.GetErrorMessage(err))
				}
				if resp == nil || len(resp.Choices) == 0 {
					t.Fatalf("expected chat choices, got %#v", resp)
				}
			},
		},
		{
			name:     "ResponsesAnthropicEndpoint",
			wantCall: "POST /v1/messages",
			run: func(t *testing.T, provider *dagon.DagonProvider) {
				resp, err := provider.Responses(testContext(t), anthropicKey(), responsesRequest())
				if err != nil {
					t.Fatalf("Responses: %v", llmtests.GetErrorMessage(err))
				}
				if resp == nil || len(resp.Output) == 0 {
					t.Fatalf("expected responses output, got %#v", resp)
				}
			},
		},
		{
			name:     "CountTokens",
			wantCall: "POST /v1/messages/count_tokens",
			run: func(t *testing.T, provider *dagon.DagonProvider) {
				resp, err := provider.CountTokens(testContext(t), testKey(), responsesRequest())
				if err != nil {
					t.Fatalf("CountTokens: %v", llmtests.GetErrorMessage(err))
				}
				if resp == nil || resp.InputTokens != 7 {
					t.Fatalf("expected 7 input tokens, got %#v", resp)
				}
			},
		},
		{
			name:     "ListModels",
			wantCall: "GET /v1/models",
			run: func(t *testing.T, provider *dagon.DagonProvider) {
				if _, err := provider.ListModels(testContext(t), []schemas.Key{testKey()}, &schemas.BifrostListModelsRequest{Provider: schemas.Dagon}); err != nil {
					t.Fatalf("ListModels: %v", llmtests.GetErrorMessage(err))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, captured := newMockDagon(t, map[string]http.HandlerFunc{
				"POST /v1/chat/completions":      jsonResponse(chatBody),
				"POST /v1/responses":             jsonResponse(responsesBody),
				"POST /v1/messages":              jsonResponse(messagesBody),
				"POST /v1/messages/count_tokens": jsonResponse(`{"input_tokens":7}`),
				"GET /v1/models":                 jsonResponse(modelsBody),
			})

			tt.run(t, provider)

			reqs := captured()
			if len(reqs) != 1 {
				t.Fatalf("expected 1 upstream request, got %d", len(reqs))
			}
			if got := reqs[0].method + " " + reqs[0].path; got != tt.wantCall {
				t.Errorf("upstream call = %q, want %q", got, tt.wantCall)
			}
			if reqs[0].auth != "Bearer test-key" {
				t.Errorf("Authorization = %q, want Bearer test-key", reqs[0].auth)
			}
			if reqs[0].method == http.MethodPost {
				var body struct {
					Model string `json:"model"`
				}
				if err := json.Unmarshal(reqs[0].body, &body); err != nil {
					t.Fatalf("decode body: %v", err)
				}
				if body.Model != namespacedModel {
					t.Errorf("model = %q, want %q", body.Model, namespacedModel)
				}
			}
		})
	}
}

// TestDagonChatStream checks that an OpenAI-style SSE stream from Dagon is relayed chunk by chunk.
func TestDagonChatStream(t *testing.T) {
	provider, captured := newMockDagon(t, map[string]http.HandlerFunc{
		"POST /v1/chat/completions": sseResponse(
			`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"abliterated-model","choices":[{"index":0,"delta":{"role":"assistant","content":"o"}}]}`+"\n\n",
			`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"abliterated-model","choices":[{"index":0,"delta":{"content":"k"},"finish_reason":"stop"}]}`+"\n\n",
			`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"abliterated-model","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`+"\n\n",
			"data: [DONE]\n\n",
		),
	})

	stream, bifrostErr := provider.ChatCompletionStream(testContext(t), passThroughPostHook, nil, testKey(), chatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream: %v", llmtests.GetErrorMessage(bifrostErr))
	}

	var content strings.Builder
	for chunk := range stream {
		if chunk.BifrostError != nil {
			t.Fatalf("unexpected stream error: %v", llmtests.GetErrorMessage(chunk.BifrostError))
		}
		if chunk.BifrostChatResponse == nil {
			continue
		}
		for _, choice := range chunk.BifrostChatResponse.Choices {
			if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil && choice.ChatStreamResponseChoice.Delta.Content != nil {
				content.WriteString(*choice.ChatStreamResponseChoice.Delta.Content)
			}
		}
	}
	if content.String() != "ok" {
		t.Fatalf("content = %q, want ok", content.String())
	}
	if reqs := captured(); len(reqs) != 1 || !strings.Contains(string(reqs[0].body), `"stream":true`) {
		t.Fatalf("expected one streaming upstream request, got %d", len(reqs))
	}
}

// TestDagonListModelsPrefixesIDs checks that both plain and namespaced Dagon model ids are
// listed under the dagon/ prefix.
func TestDagonListModelsPrefixesIDs(t *testing.T) {
	provider, _ := newMockDagon(t, map[string]http.HandlerFunc{"GET /v1/models": jsonResponse(modelsBody)})

	resp, err := provider.ListModels(testContext(t), []schemas.Key{testKey()}, &schemas.BifrostListModelsRequest{Provider: schemas.Dagon})
	if err != nil {
		t.Fatalf("ListModels: %v", llmtests.GetErrorMessage(err))
	}
	ids := make([]string, 0, len(resp.Data))
	for _, model := range resp.Data {
		ids = append(ids, model.ID)
	}
	for _, want := range []string{"dagon/abliterated-model", "dagon/arcium-blackthorn/qwen3.8-27b-bllm"} {
		if !slices.Contains(ids, want) {
			t.Errorf("missing %s in %v", want, ids)
		}
	}
}

// TestDagonAnthropicEndpointDropsServerTools checks that an Anthropic-hosted server tool is
// stripped before reaching Dagon's /v1/messages while the caller's function tools are kept.
func TestDagonAnthropicEndpointDropsServerTools(t *testing.T) {
	provider, captured := newMockDagon(t, map[string]http.HandlerFunc{"POST /v1/messages": jsonResponse(messagesBody)})

	request := responsesRequest()
	request.Params = &schemas.ResponsesParameters{
		Tools: []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeWebSearch},
			{
				Type:                  schemas.ResponsesToolTypeFunction,
				Name:                  schemas.Ptr("lookup"),
				ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: &schemas.ToolFunctionParameters{Type: "object"}},
			},
		},
	}
	if _, err := provider.Responses(testContext(t), anthropicKey(), request); err != nil {
		t.Fatalf("Responses: %v", llmtests.GetErrorMessage(err))
	}

	reqs := captured()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 upstream request, got %d", len(reqs))
	}
	var body struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(reqs[0].body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Tools) != 1 || body.Tools[0]["name"] != "lookup" {
		t.Fatalf("outbound tools = %v, want only the lookup function tool", body.Tools)
	}
}

// TestDagonCountTokensHonoursBetaHeaderOverrides checks that CountTokens applies the
// operator's BetaHeaderOverrides like the chat and Responses paths: a beta Dagon drops by
// default reaches the upstream once an override enables it.
func TestDagonCountTokensHonoursBetaHeaderOverrides(t *testing.T) {
	const beta = "interleaved-thinking-2025-05-14"
	gotBeta := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta <- r.Header.Get(anthropic.AnthropicBetaHeader)
		jsonResponse(`{"input_tokens":7}`)(w, r)
	}))
	t.Cleanup(server.Close)

	provider, err := dagon.NewDagonProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL + "/",
			DefaultRequestTimeoutInSeconds: 10,
			AllowPrivateNetwork:            true,
			BetaHeaderOverrides:            map[string]bool{anthropic.AnthropicInterleavedThinkingBetaHeaderPrefix: true},
		},
	}, bifrost.NewNoOpLogger())
	if err != nil {
		t.Fatalf("NewDagonProvider: %v", err)
	}

	ctx := testContext(t)
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{anthropic.AnthropicBetaHeader: {beta}})
	if _, bifrostErr := provider.CountTokens(ctx, testKey(), responsesRequest()); bifrostErr != nil {
		t.Fatalf("CountTokens: %v", llmtests.GetErrorMessage(bifrostErr))
	}
	if got := <-gotBeta; !strings.Contains(got, beta) {
		t.Fatalf("%s = %q, want it to contain %q", anthropic.AnthropicBetaHeader, got, beta)
	}
}

// TestDagonUnsupportedOperations checks that operations Dagon does not serve return
// unsupported_operation without calling the upstream API.
func TestDagonUnsupportedOperations(t *testing.T) {
	provider, captured := newMockDagon(t, nil)
	prompt := "hi"
	textRequest := &schemas.BifrostTextCompletionRequest{Provider: schemas.Dagon, Model: "abliterated-model", Input: &schemas.TextCompletionInput{PromptStr: &prompt}}

	assertUnsupported := func(name string, err *schemas.BifrostError) {
		t.Helper()
		if err == nil || err.Error == nil || err.Error.Code == nil || *err.Error.Code != "unsupported_operation" {
			t.Errorf("%s error = %+v, want unsupported_operation", name, err)
		}
	}
	_, err := provider.TextCompletion(testContext(t), testKey(), textRequest)
	assertUnsupported("TextCompletion", err)
	_, err = provider.TextCompletionStream(testContext(t), passThroughPostHook, nil, testKey(), textRequest)
	assertUnsupported("TextCompletionStream", err)
	_, err = provider.Embedding(testContext(t), testKey(), &schemas.BifrostEmbeddingRequest{Provider: schemas.Dagon, Model: "abliterated-model"})
	assertUnsupported("Embedding", err)
	_, err = provider.ModelRetrieve(testContext(t), testKey(), &schemas.BifrostModelRetrieveRequest{Model: "abliterated-model"})
	assertUnsupported("ModelRetrieve", err)

	if reqs := captured(); len(reqs) != 0 {
		t.Errorf("unsupported operations must not call upstream, got %d requests", len(reqs))
	}
}

// Exercise each shared streaming parser through Dagon's actual dispatch, not just
// its request builder. A parser swap must not silently return an empty stream.
func TestDagonStreamingSurfaces(t *testing.T) {
	messagesEvents := []string{
		"event: message_start\ndata: " + `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"abliterated-model","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}` + "\n\n",
		"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n",
		"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n",
		"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\n",
		"event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\n",
		"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n",
	}
	for _, name := range []string{"responses", "anthropic-chat", "anthropic-responses"} {
		t.Run(name, func(t *testing.T) {
			path := "/v1/messages"
			events := messagesEvents
			key := anthropicKey()
			if name == "responses" {
				path = "/v1/responses"
				key = testKey()
				events = []string{
					"event: response.output_text.delta\ndata: " + `{"type":"response.output_text.delta","sequence_number":0,"output_index":0,"content_index":0,"item_id":"msg_1","delta":"ok"}` + "\n\n",
					"event: response.completed\ndata: " + `{"type":"response.completed","sequence_number":1,"response":` + responsesBody + "}\n\n",
				}
			}
			provider, captured := newMockDagon(t, map[string]http.HandlerFunc{"POST " + path: sseResponse(events...)})
			ctx := testContext(t)
			var stream chan *schemas.BifrostStreamChunk
			var err *schemas.BifrostError
			if name == "anthropic-chat" {
				stream, err = provider.ChatCompletionStream(ctx, passThroughPostHook, nil, key, chatRequest())
			} else {
				stream, err = provider.ResponsesStream(ctx, passThroughPostHook, nil, key, responsesRequest())
			}
			if err != nil {
				t.Fatalf("stream: %v", llmtests.GetErrorMessage(err))
			}
			var text strings.Builder
			for chunk := range stream {
				if chunk.BifrostError != nil {
					t.Fatalf("stream chunk: %v", llmtests.GetErrorMessage(chunk.BifrostError))
				}
				if r := chunk.BifrostResponsesStreamResponse; r != nil && r.Type == "response.output_text.delta" && r.Delta != nil {
					text.WriteString(*r.Delta)
				}
				if r := chunk.BifrostChatResponse; r != nil {
					for _, choice := range r.Choices {
						if choice.ChatStreamResponseChoice != nil && choice.Delta != nil && choice.Delta.Content != nil {
							text.WriteString(*choice.Delta.Content)
						}
					}
				}
			}
			if text.String() != "ok" {
				t.Fatalf("streamed text = %q, want ok", text.String())
			}
			reqs := captured()
			if len(reqs) != 1 || reqs[0].path != path || reqs[0].auth != "Bearer test-key" {
				t.Fatalf("unexpected upstream requests: %+v", reqs)
			}
			var body struct {
				Model  string `json:"model"`
				Stream bool   `json:"stream"`
			}
			if err := json.Unmarshal(reqs[0].body, &body); err != nil {
				t.Fatal(err)
			}
			if !body.Stream || body.Model != namespacedModel {
				t.Fatalf("unexpected streaming body: %s", reqs[0].body)
			}
		})
	}
}

func TestDagonProviderErrors(t *testing.T) {
	for _, useAnthropic := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			for _, status := range []int{400, 401, 402, 429, 503} {
				name := "openai"
				if useAnthropic {
					name = "anthropic"
				}
				if streaming {
					name += "-stream"
				}
				t.Run(name+"/"+http.StatusText(status), func(t *testing.T) {
					path := "/v1/chat/completions"
					key := testKey()
					if useAnthropic {
						path = "/v1/messages"
						key = anthropicKey()
					}
					provider, _ := newMockDagon(t, map[string]http.HandlerFunc{
						"POST " + path: func(w http.ResponseWriter, _ *http.Request) {
							w.Header().Set("Content-Type", "application/json")
							w.Header().Set("Retry-After", "2")
							w.WriteHeader(status)
							_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","code":"invalid_provider","message":"Unknown or disabled provider"}}`)
						},
					})
					var err *schemas.BifrostError
					if streaming {
						_, err = provider.ChatCompletionStream(testContext(t), passThroughPostHook, nil, key, chatRequest())
					} else {
						_, err = provider.ChatCompletion(testContext(t), key, chatRequest())
					}
					if err == nil || err.StatusCode == nil || *err.StatusCode != status || err.Error == nil {
						t.Fatalf("error = %+v, want HTTP %d", err, status)
					}
					if err.Error.Message != "Unknown or disabled provider" || err.Error.Type == nil || *err.Error.Type != "invalid_request_error" {
						t.Fatalf("error details = %+v", err.Error)
					}
					if !useAnthropic && (err.Error.Code == nil || *err.Error.Code != "invalid_provider") {
						t.Fatalf("OpenAI error code lost: %+v", err.Error)
					}
					if err.ExtraFields.RetryAfter != 2000 {
						t.Fatalf("retry_after_ms = %d, want 2000", err.ExtraFields.RetryAfter)
					}
				})
			}
		}
	}
}
