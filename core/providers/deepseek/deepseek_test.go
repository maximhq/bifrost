package deepseek_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestDeepseek(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")) == "" {
		t.Skip("Skipping DeepSeek tests because DEEPSEEK_API_KEY is not set")
	}

	client, ctx, cancel, err := llmtests.SetupTest()
	if err != nil {
		t.Fatalf("Error initializing test setup: %v", err)
	}
	defer cancel()
	defer client.Shutdown()

	testConfig := llmtests.ComprehensiveTestConfig{
		Provider:  schemas.DeepSeek,
		ChatModel: "deepseek-v4-flash",
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.DeepSeek, Model: "deepseek-v4-flash"},
			{Provider: schemas.DeepSeek, Model: "deepseek-v4-pro"},
		},
		TextModel:      "deepseek-v4-pro",
		EmbeddingModel: "", // DeepSeek doesn't support embedding
		ReasoningModel: "deepseek-v4-pro",
		Scenarios: llmtests.TestScenarios{
			TextCompletion:             true,
			TextCompletionStream:       true,
			SimpleChat:                 true,
			CompletionStream:           true,
			MultiTurnConversation:      true,
			ToolCalls:                  true,
			ToolCallsStreaming:         true,
			MultipleToolCalls:          true,
			MultipleToolCallsStreaming: true,
			End2EndToolCalling:         true,
			AutomaticFunctionCall:      true,
			ImageURL:                   false,
			ImageBase64:                false,
			MultipleImages:             false,
			CompleteEnd2End:            true,
			Embedding:                  false,
			ListModels:                 true,
			Reasoning:                  true,
		},
	}

	t.Run("DeepSeekTests", func(t *testing.T) {
		llmtests.RunAllComprehensiveTests(t, client, ctx, testConfig)
	})
}

// DeepSeek keeps accepting retired model ids but serves the current model behind
// them, naming that model in the response. requestedModel/servedModel mirror that.
const (
	requestedModel = "deepseek-v4-flash"
	servedModel    = "deepseek-flash"
)

// newServedModelServer answers every request with body, served as contentType.
func newServedModelServer(t *testing.T, contentType, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func servedModelChatRequest() *schemas.BifrostChatRequest {
	msg := "hello"
	return &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    requestedModel,
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: &msg},
		}},
	}
}

func assertServedModel(t *testing.T, label string, got *string) {
	t.Helper()
	if got == nil || *got != servedModel {
		t.Fatalf("%s ServerSideFallbackModel = %v, want %q", label, got, servedModel)
	}
}

// collectStream drains a stream through a recording post-hook runner and returns
// the last result the runner saw, which is what pricing reads at stream end.
func collectStream(t *testing.T, start func(schemas.PostHookRunner) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError)) *schemas.BifrostResponse {
	t.Helper()
	var last *schemas.BifrostResponse
	runner := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		if result != nil {
			last = result
		}
		return result, err
	}
	stream, bifrostErr := start(runner)
	if bifrostErr != nil {
		t.Fatalf("stream setup: %v", bifrostErr.Error.Message)
	}
	for chunk := range stream {
		if chunk.BifrostError != nil {
			t.Fatalf("stream error: %v", chunk.BifrostError.Error.Message)
		}
	}
	if last == nil {
		t.Fatal("post-hook runner saw no results")
	}
	return last
}

func TestChatCompletion_PricesServedModelWhenDeepSeekUpgrades(t *testing.T) {
	t.Parallel()

	server := newServedModelServer(t, "application/json", `{"id":"chatcmpl_1","object":"chat.completion","model":"`+servedModel+`","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, servedModelChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	if resp.Model != servedModel {
		t.Fatalf("response model = %q, want %q", resp.Model, servedModel)
	}
	assertServedModel(t, "RoutingInfo", resp.ExtraFields.RoutingInfo.ServerSideFallbackModel)
	assertServedModel(t, "Usage", resp.Usage.ServerSideFallbackModel)
}

func TestChatCompletion_NoServedModelWhenModelUnchanged(t *testing.T) {
	t.Parallel()

	server := newServedModelServer(t, "application/json", newOpenAIChatResponse())
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	request := servedModelChatRequest() // newOpenAIChatResponse echoes requestedModel back
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, request)
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	if got := resp.ExtraFields.RoutingInfo.ServerSideFallbackModel; got != nil {
		t.Fatalf("ServerSideFallbackModel = %q, want nil when DeepSeek served the requested model", *got)
	}
}

func TestChatCompletion_AnthropicEndpointPricesServedModel(t *testing.T) {
	t.Parallel()

	server := newServedModelServer(t, "application/json", `{"id":"msg_1","type":"message","role":"assistant","model":"`+servedModel+`","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}, UseAnthropicEndpoints: new(true)}
	resp, bifrostErr := provider.ChatCompletion(ctx, key, servedModelChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	assertServedModel(t, "RoutingInfo", resp.ExtraFields.RoutingInfo.ServerSideFallbackModel)
}

func TestResponses_PricesServedModelWhenDeepSeekUpgrades(t *testing.T) {
	t.Parallel()

	server := newServedModelServer(t, "application/json", `{"id":"chatcmpl_1","object":"chat.completion","model":"`+servedModel+`","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	msg := "hello"
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Responses(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, &schemas.BifrostResponsesRequest{
		Provider: schemas.DeepSeek,
		Model:    requestedModel,
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: &msg},
		}},
	})
	if bifrostErr != nil {
		t.Fatalf("Responses: %v", bifrostErr.Error.Message)
	}
	assertServedModel(t, "RoutingInfo", resp.ExtraFields.RoutingInfo.ServerSideFallbackModel)
}

const servedModelChatStream = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"" + servedModel + "\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"" + servedModel + "\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"" + servedModel + "\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n" +
	"data: [DONE]\n\n"

func TestChatCompletionStream_PricesServedModelOnFinalChunk(t *testing.T) {
	t.Parallel()

	server := newServedModelServer(t, "text/event-stream", servedModelChatStream)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	last := collectStream(t, func(runner schemas.PostHookRunner) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return provider.ChatCompletionStream(ctx, runner, func(context.Context) {}, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, servedModelChatRequest())
	})
	if last.ChatResponse == nil || last.ChatResponse.Usage == nil {
		t.Fatalf("final chunk carries no chat usage: %#v", last)
	}
	assertServedModel(t, "RoutingInfo", last.ChatResponse.ExtraFields.RoutingInfo.ServerSideFallbackModel)
	assertServedModel(t, "Usage", last.ChatResponse.Usage.ServerSideFallbackModel)
}

func TestResponsesStream_PricesServedModelOnFinalEvent(t *testing.T) {
	t.Parallel()

	server := newServedModelServer(t, "text/event-stream", servedModelChatStream)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	msg := "hello"
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	last := collectStream(t, func(runner schemas.PostHookRunner) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return provider.ResponsesStream(ctx, runner, func(context.Context) {}, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, &schemas.BifrostResponsesRequest{
			Provider: schemas.DeepSeek,
			Model:    requestedModel,
			Input: []schemas.ResponsesMessage{{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: &msg},
			}},
		})
	})
	if last.ResponsesStreamResponse == nil {
		t.Fatalf("final result is not a responses stream event: %#v", last)
	}
	assertServedModel(t, "RoutingInfo", last.ResponsesStreamResponse.ExtraFields.RoutingInfo.ServerSideFallbackModel)
}

// useDatasheetServedModel installs a capability resolver whose DeepSeek
// requestedModel row names served as its server_side_model. The resolver is
// process-global, so callers must not run in parallel.
func useDatasheetServedModel(t *testing.T, served string) {
	t.Helper()
	schemas.SetCapabilityResolver(func(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		if provider != schemas.DeepSeek || model != requestedModel {
			return nil
		}
		return &schemas.ModelCapabilities{ServerSideModel: &served}
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
}

// DeepSeek's Anthropic endpoint echoes the requested id back, so the response
// cannot reveal the served model and the datasheet has to.
const echoedAnthropicResponse = `{"id":"msg_1","type":"message","role":"assistant","model":"` + requestedModel + `","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

const echoedAnthropicStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"" + requestedModel + "\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestChatCompletion_AnthropicEndpointUsesDatasheetServerSideModel(t *testing.T) {
	useDatasheetServedModel(t, servedModel)

	server := newServedModelServer(t, "application/json", echoedAnthropicResponse)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}, UseAnthropicEndpoints: new(true)}
	resp, bifrostErr := provider.ChatCompletion(ctx, key, servedModelChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	assertServedModel(t, "RoutingInfo", resp.ExtraFields.RoutingInfo.ServerSideFallbackModel)
	assertServedModel(t, "Usage", resp.Usage.ServerSideFallbackModel)
}

func TestChatCompletionStream_AnthropicEndpointUsesDatasheetServerSideModel(t *testing.T) {
	useDatasheetServedModel(t, servedModel)

	server := newServedModelServer(t, "text/event-stream", echoedAnthropicStream)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}, UseAnthropicEndpoints: new(true)}
	last := collectStream(t, func(runner schemas.PostHookRunner) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return provider.ChatCompletionStream(ctx, runner, func(context.Context) {}, key, servedModelChatRequest())
	})
	if last.ChatResponse == nil {
		t.Fatalf("final result is not a chat chunk: %#v", last)
	}
	assertServedModel(t, "RoutingInfo", last.ChatResponse.ExtraFields.RoutingInfo.ServerSideFallbackModel)
}

// The response naming its served model is the stronger signal and wins over the datasheet.
func TestChatCompletion_ResponseServedModelOutranksDatasheet(t *testing.T) {
	useDatasheetServedModel(t, "deepseek-stale-datasheet-target")

	server := newServedModelServer(t, "application/json", `{"id":"chatcmpl_1","object":"chat.completion","model":"`+servedModel+`","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, servedModelChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	assertServedModel(t, "RoutingInfo", resp.ExtraFields.RoutingInfo.ServerSideFallbackModel)
}

func TestChatCompletion_AnthropicEndpointWithoutDatasheetServerSideModelStaysUnset(t *testing.T) {
	server := newServedModelServer(t, "application/json", echoedAnthropicResponse)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}, UseAnthropicEndpoints: new(true)}
	resp, bifrostErr := provider.ChatCompletion(ctx, key, servedModelChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	if got := resp.ExtraFields.RoutingInfo.ServerSideFallbackModel; got != nil {
		t.Fatalf("ServerSideFallbackModel = %q, want nil without a datasheet server_side_model", *got)
	}
}
