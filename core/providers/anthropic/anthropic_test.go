package anthropic_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/openai"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestAnthropic(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) == "" {
		t.Skip("Skipping Anthropic tests because ANTHROPIC_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.Anthropic,
		ChatModel: "claude-sonnet-4-5",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Anthropic, Model: "claude-3-7-sonnet-20250219"},
			{Provider: schemas.Anthropic, Model: "claude-sonnet-4-6"},
		},
		VisionModel:        "claude-sonnet-4-5", // Same model supports vision
		ReasoningModel:     "claude-opus-4-5",
		PromptCachingModel: "claude-sonnet-4-6",
		PassthroughModel:   "claude-sonnet-4-5",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:               false, // Not supported
			SimpleChat:                   true,
			CompletionStream:             true,
			MultiTurnConversation:        true,
			ToolCalls:                    true,
			ToolCallsStreaming:           true,
			MultipleToolCalls:            true,
			MultipleToolCallsStreaming:   true,
			End2EndToolCalling:           true,
			AutomaticFunctionCall:        true,
			WebSearchTool:                true,
			ImageURL:                     true,
			ImageBase64:                  true,
			MultipleImages:               true,
			FileBase64:                   true,
			FileURL:                      true,
			CompleteEnd2End:              true,
			Embedding:                    false,
			Reasoning:                    true,
			PromptCaching:                true,
			ListModels:                   true,
			BatchCreate:                  true,
			BatchList:                    true,
			BatchRetrieve:                true,
			BatchCancel:                  true,
			BatchResults:                 true,
			FileUpload:                   true,
			FileList:                     true,
			FileRetrieve:                 true,
			FileDelete:                   true,
			FileContent:                  false,
			FileBatchInput:               false, // Anthropic batch API only supports inline requests, not file-based input
			CountTokens:                  true,
			StructuredOutputs:            true, // Structured outputs with nullable enum support
			PassthroughAPI:               true,
			Compaction:                   true,
			ToolSearch:                   true,
			InterleavedThinking:          true,
			FastMode:                     false, // Enable when test API key has Opus 4.6 access
			EagerInputStreaming:          true,  // fine-grained-tool-streaming-2025-05-14 (GA on Anthropic)
			ServerToolsViaOpenAIEndpoint: true,  // web_search / web_fetch / code_execution via /v1/chat/completions
		},
	}

	t.Run("AnthropicTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// A retrieve must send Anthropic's own auth headers, return the dated model an alias
// resolves to, carry created_at as a Unix timestamp, surface an upstream 404, and refuse
// a path-shaping id before anything is dispatched.
func TestAnthropicModelRetrieve(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotPath, gotAPIKey, gotVersion, gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath, gotAPIKey = r.URL.Path, r.Header.Get("x-api-key")
		gotVersion, gotAuthorization = r.Header.Get("anthropic-version"), r.Header.Get("Authorization")
		mu.Unlock()
		if r.URL.Path == "/v1/models/nope" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"not_found_error","message":"model: nope"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"type":"model","id":"claude-sonnet-4-5-20250929","display_name":"Claude Sonnet 4.5","created_at":"2025-09-29T00:00:00Z","max_input_tokens":200000,"max_tokens":64000,"capabilities":{"batch":{"supported":true}}}`))
	}))
	defer server.Close()

	provider := anthropic.NewAnthropicProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("sk-ant-test")}

	response, bifrostErr := provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "claude-sonnet-4-5"})
	require.Nil(t, bifrostErr)

	mu.Lock()
	path, apiKey, version, authorization := gotPath, gotAPIKey, gotVersion, gotAuthorization
	mu.Unlock()
	require.Equal(t, "/v1/models/claude-sonnet-4-5", path)
	require.Equal(t, "sk-ant-test", apiKey)
	require.Equal(t, "2023-06-01", version)
	require.Empty(t, authorization, "Anthropic authenticates with x-api-key, not a bearer token")

	require.Equal(t, "anthropic/claude-sonnet-4-5-20250929", response.ID)
	require.Equal(t, schemas.Ptr("Claude Sonnet 4.5"), response.Name)
	require.Equal(t, schemas.Ptr(time.Date(2025, 9, 29, 0, 0, 0, 0, time.UTC).Unix()), response.Created)
	require.Equal(t, schemas.Ptr(200000), response.MaxInputTokens)
	require.Equal(t, schemas.Ptr(64000), response.MaxOutputTokens)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "nope"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusNotFound), bifrostErr.StatusCode)

	_, bifrostErr = provider.ModelRetrieve(ctx, key, &schemas.BifrostModelRetrieveRequest{Model: "../models"})
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusBadRequest), bifrostErr.StatusCode)
}

// A local upstream pins actual dispatch, beta inference, and response/error
// handling without treating a successful request-body build as an HTTP test.
func TestAnthropicResponsesToolChangesHTTP(t *testing.T) {
	const nativeBody = `{
	  "model":"claude-sonnet-5-5","max_tokens":128,
	  "tools":[{"name":"echo","input_schema":{"type":"object","properties":{}}},{"name":"old","input_schema":{"type":"object","properties":{}}}],
	  "messages":[
	    {"role":"user","content":[{"type":"text","text":"hi"}]},
	    {"role":"system","content":[{"type":"tool_removal","tool":{"type":"tool_reference","name":"old"}}]},
	    {"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"get_weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}}}]},
	    {"role":"assistant","content":[{"type":"tool_use","id":"toolu_weather","name":"get_weather","input":{"city":"Paris"}}]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_weather","content":"sunny"}]}
	  ]
	}`
	const reply = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"text","text":"sunny"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	const sse = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"sunny\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, neutral := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			for _, status := range []int{http.StatusOK, http.StatusBadRequest} {
				t.Run(fmt.Sprintf("neutral=%v/stream=%v/status=%d", neutral, streaming, status), func(t *testing.T) {
					type captured struct {
						body   []byte
						header http.Header
						path   string
					}
					requests := make(chan captured, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Errorf("read request: %v", err)
						}
						requests <- captured{body: body, header: r.Header.Clone(), path: r.URL.Path}
						if status != http.StatusOK {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(status)
							_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"tool replay rejected by fixture"}}`))
						} else if streaming {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = w.Write([]byte(sse))
						} else {
							w.Header().Set("Content-Type", "application/json")
							_, _ = w.Write([]byte(reply))
						}
					}))
					defer server.Close()
					provider := anthropic.NewAnthropicProvider(&schemas.ProviderConfig{
						NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 5},
					}, bifrost.NewNoOpLogger())
					ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					var native anthropic.AnthropicMessageRequest
					require.NoError(t, schemas.Unmarshal([]byte(nativeBody), &native))
					req := native.ToBifrostResponsesRequest(ctx)
					// The gateway selects the provider before entering its typed builder.
					req.Provider = schemas.Anthropic
					if neutral {
						wire, err := schemas.MarshalSorted(&openai.OpenAIResponsesRequest{
							Model:               "anthropic/" + req.Model,
							Input:               openai.OpenAIResponsesRequestInput{OpenAIResponsesRequestInputArray: req.Input},
							ResponsesParameters: *req.Params,
						})
						require.NoError(t, err)
						var ingress openai.OpenAIResponsesRequest
						require.NoError(t, schemas.Unmarshal(wire, &ingress))
						req = ingress.ToBifrostResponsesRequest(ctx)
					}
					key := schemas.Key{Value: *schemas.NewSecretVar("sk-ant-fixture")}
					var bifrostErr *schemas.BifrostError
					if streaming {
						postHook := func(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
							return resp, err
						}
						var stream chan *schemas.BifrostStreamChunk
						stream, bifrostErr = provider.ResponsesStream(ctx, postHook, nil, key, req)
						if status == http.StatusOK {
							require.Nil(t, bifrostErr)
							var text strings.Builder
							completed := false
							for {
								select {
								case chunk, ok := <-stream:
									if !ok {
										require.Equal(t, "sunny", text.String())
										require.True(t, completed, "missing response.completed event")
										goto drained
									}
									require.NotNil(t, chunk)
									require.Nil(t, chunk.BifrostError)
									if event := chunk.BifrostResponsesStreamResponse; event != nil {
										if event.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta && event.Delta != nil {
											text.WriteString(*event.Delta)
										}
										completed = completed || event.Type == schemas.ResponsesStreamResponseTypeCompleted
									}
								case <-ctx.Done():
									t.Fatal("stream did not close before timeout")
								}
							}
						}
					} else {
						var response *schemas.BifrostResponsesResponse
						response, bifrostErr = provider.Responses(ctx, key, req)
						if status == http.StatusOK {
							require.Nil(t, bifrostErr)
							require.NotNil(t, response)
							require.Len(t, response.Output, 1)
							require.NotNil(t, response.Output[0].Content)
							require.Equal(t, "sunny", *response.Output[0].Content.ContentBlocks[0].Text)
						} else {
							require.Nil(t, response)
						}
					}
				drained:
					if status != http.StatusOK {
						require.NotNil(t, bifrostErr)
						require.NotNil(t, bifrostErr.StatusCode)
						require.Equal(t, status, *bifrostErr.StatusCode)
						require.Contains(t, bifrostErr.Error.Message, "tool replay rejected by fixture")
					}
					var got captured
					select {
					case got = <-requests:
					case <-ctx.Done():
						t.Fatal("provider did not dispatch a request")
					}
					require.Equal(t, "/v1/messages", got.path)
					require.Equal(t, "sk-ant-fixture", got.header.Get("x-api-key"))
					require.Equal(t, "2023-06-01", got.header.Get("anthropic-version"))
					betas := strings.Split(got.header.Get("anthropic-beta"), ",")
					require.Contains(t, betas, anthropic.AnthropicMidConversationToolChangesBetaHeader)
					require.Contains(t, betas, anthropic.AnthropicInlineToolsBetaHeader)
					var out anthropic.AnthropicMessageRequest
					require.NoError(t, schemas.Unmarshal(got.body, &out))
					require.Nil(t, out.System)
					require.Equal(t, streaming, out.Stream != nil && *out.Stream)
					wantMessages, err := schemas.MarshalSorted(native.Messages)
					require.NoError(t, err)
					gotMessages, err := schemas.MarshalSorted(out.Messages)
					require.NoError(t, err)
					require.JSONEq(t, string(wantMessages), string(gotMessages), "tool changes or replay pairing changed on dispatch")
					wantTools, err := schemas.MarshalSorted(native.Tools)
					require.NoError(t, err)
					gotTools, err := schemas.MarshalSorted(out.Tools)
					require.NoError(t, err)
					require.JSONEq(t, string(wantTools), string(gotTools), "inline changes must not mutate tools[]")
				})
			}
		}
	}
}
