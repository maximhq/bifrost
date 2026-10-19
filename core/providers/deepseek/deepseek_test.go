package deepseek_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
		_, _ = fmt.Fprint(w, body)
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

const servedModelTextStream = "data: {\"id\":\"t1\",\"object\":\"text_completion\",\"model\":\"" + servedModel + "\",\"choices\":[{\"index\":0,\"text\":\"hi\"}]}\n\n" +
	"data: {\"id\":\"t1\",\"object\":\"text_completion\",\"model\":\"" + servedModel + "\",\"choices\":[{\"index\":0,\"text\":\"\",\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"id\":\"t1\",\"object\":\"text_completion\",\"model\":\"" + servedModel + "\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n" +
	"data: [DONE]\n\n"

// The final text chunk is built by Bifrost with the requested id, which must not
// overwrite the served model seen on earlier chunks.
func TestTextCompletionStream_PricesServedModelOnFinalChunk(t *testing.T) {
	t.Parallel()

	server := newServedModelServer(t, "text/event-stream", servedModelTextStream)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	prompt := "hello"
	request := &schemas.BifrostTextCompletionRequest{
		Provider: schemas.DeepSeek,
		Model:    requestedModel,
		Input:    &schemas.TextCompletionInput{PromptStr: &prompt},
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	last := collectStream(t, func(runner schemas.PostHookRunner) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return provider.TextCompletionStream(ctx, runner, func(context.Context) {}, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, request)
	})
	if last.TextCompletionResponse == nil || last.TextCompletionResponse.Usage == nil {
		t.Fatalf("final chunk carries no text usage: %#v", last)
	}
	assertServedModel(t, "RoutingInfo", last.TextCompletionResponse.ExtraFields.RoutingInfo.ServerSideFallbackModel)
	assertServedModel(t, "Usage", last.TextCompletionResponse.Usage.ServerSideFallbackModel)
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

// The datasheet only applies once the response confirms it echoed the requested id;
// a response without a model gives no basis to stamp one.
func TestChatCompletion_ResponseWithoutModelIgnoresDatasheet(t *testing.T) {
	useDatasheetServedModel(t, servedModel)

	server := newServedModelServer(t, "application/json", `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, servedModelChatRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	if got := resp.ExtraFields.RoutingInfo.ServerSideFallbackModel; got != nil {
		t.Fatalf("ServerSideFallbackModel = %q, want nil when the response names no model", *got)
	}
}

// newCapturingServer answers like newServedModelServer and records the last
// request body it received.
func newCapturingServer(t *testing.T, contentType, body string, captured *[]byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*captured = raw
		w.Header().Set("Content-Type", contentType)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

const personSchema = `{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"integer"}},"required":["name","age"]}`

// assertJSONSchemaDowngraded checks a DeepSeek chat body: response_format is
// json_object and the schema rides in a system message that mentions json.
func assertJSONSchemaDowngraded(t *testing.T, body []byte) {
	t.Helper()
	var wire struct {
		ResponseFormat map[string]json.RawMessage `json:"response_format"`
		Messages       []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode wire body: %v\n%s", err, body)
	}
	if got := string(wire.ResponseFormat["type"]); got != `"json_object"` {
		t.Fatalf("response_format.type = %s, want json_object; body: %s", got, body)
	}
	if _, ok := wire.ResponseFormat["json_schema"]; ok {
		t.Fatalf("json_schema reached DeepSeek: %s", body)
	}
	for _, message := range wire.Messages {
		var text string
		if message.Role != "system" || json.Unmarshal(message.Content, &text) != nil {
			continue
		}
		if strings.Contains(text, "json") && strings.Contains(text, personSchema) {
			return
		}
	}
	t.Fatalf("no system message carries the schema and the word json: %s", body)
}

func TestChatCompletion_DowngradesJSONSchemaResponseFormat(t *testing.T) {
	t.Parallel()

	var captured []byte
	server := newCapturingServer(t, "application/json", newOpenAIChatResponse(), &captured)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	request := servedModelChatRequest()
	var format interface{} = map[string]interface{}{
		"type": "json_schema",
		"json_schema": map[string]interface{}{
			"name":   "person",
			"schema": json.RawMessage(personSchema),
		},
	}
	request.Params = &schemas.ChatParameters{ResponseFormat: &format}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, bifrostErr := provider.ChatCompletion(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, request); bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	assertJSONSchemaDowngraded(t, captured)
}

func TestResponses_DowngradesJSONSchemaTextFormat(t *testing.T) {
	t.Parallel()

	var captured []byte
	server := newCapturingServer(t, "application/json", newOpenAIChatResponse(), &captured)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	var text schemas.ResponsesTextConfig
	if err := schemas.Unmarshal([]byte(`{"format":{"type":"json_schema","name":"person","schema":`+personSchema+`}}`), &text); err != nil {
		t.Fatalf("decode text config: %v", err)
	}
	msg := "Ada is 36."
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Responses(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, &schemas.BifrostResponsesRequest{
		Provider: schemas.DeepSeek,
		Model:    requestedModel,
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: &msg},
		}},
		Params: &schemas.ResponsesParameters{Text: &text},
	})
	if bifrostErr != nil {
		t.Fatalf("Responses: %v", bifrostErr.Error.Message)
	}
	assertJSONSchemaDowngraded(t, captured)
}

// toolsRequestCapture is what a test server saw on a Search or Fetch call.
type toolsRequestCapture struct {
	path      string
	body      []byte
	auth      string
	userAgent string
}

// newToolsServer answers the two tools endpoints and records what it received.
func newToolsServer(t *testing.T, status int, body string, captured *toolsRequestCapture) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if captured != nil {
			raw, _ := io.ReadAll(r.Body)
			captured.path = r.URL.Path
			captured.body = raw
			captured.auth = r.Header.Get("Authorization")
			captured.userAgent = r.Header.Get("User-Agent")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDeepSeekImplementsToolsProvider(t *testing.T) {
	t.Parallel()
	provider, err := newTestDeepSeekProvider("http://localhost:1")
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	var _ schemas.ToolsProvider = provider
}

func TestSearch_SendsDocumentedContractAndReportsPerRequestCost(t *testing.T) {
	t.Parallel()

	captured := &toolsRequestCapture{}
	server := newToolsServer(t, http.StatusOK, `{"results":[{"title":"Bifrost","url":"https://example.com/a","snippet":"short","summary":"longer","site_name":"Example","authority":3,"rerank_score":1.5,"date":"2026-10-18T00:00:00Z"}]}`, captured)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	count := 5
	needContent := false
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Search(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, &schemas.BifrostSearchRequest{
		Provider:    schemas.DeepSeek,
		Query:       "bifrost gateway",
		Count:       &count,
		NeedContent: &needContent,
	})
	if bifrostErr != nil {
		t.Fatalf("Search: %v", bifrostErr.Error.Message)
	}

	if captured.path != "/v1/tools/search" {
		t.Fatalf("path = %q, want /v1/tools/search", captured.path)
	}
	if captured.auth != "Bearer test-api-key" {
		t.Fatalf("Authorization = %q, want a bearer token", captured.auth)
	}
	var sent map[string]any
	if err := json.Unmarshal(captured.body, &sent); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if sent["query"] != "bifrost gateway" {
		t.Fatalf("query = %v, want the caller's query", sent["query"])
	}
	if sent["count"] != float64(5) {
		t.Fatalf("count = %v, want 5", sent["count"])
	}
	if sent["need_content"] != false {
		t.Fatalf("need_content = %v, want false", sent["need_content"])
	}
	// The upstream contract carries no model; a routing model must never leak onto the wire.
	if _, present := sent["model"]; present {
		t.Fatalf("model must not be sent to the tools API, got %v", sent["model"])
	}

	if len(resp.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(resp.Results))
	}
	result := resp.Results[0]
	if result.Title != "Bifrost" || result.URL != "https://example.com/a" {
		t.Fatalf("result = %+v, want the upstream title and url", result)
	}
	if result.Authority == nil || *result.Authority != 3 {
		t.Fatalf("authority = %v, want 3", result.Authority)
	}
	if result.Date == nil || *result.Date != "2026-10-18T00:00:00Z" {
		t.Fatalf("date = %v, want the upstream RFC 3339 string", result.Date)
	}
	if resp.Usage == nil || resp.Usage.Cost == nil {
		t.Fatal("Usage.Cost must be reported so the gateway can price a model-less call")
	}
	if got := resp.Usage.Cost.TotalCost; got != 0.00075 {
		t.Fatalf("TotalCost = %v, want 0.00075", got)
	}
	if resp.ExtraFields.RequestType != schemas.SearchRequest {
		t.Fatalf("RequestType = %q, want search", resp.ExtraFields.RequestType)
	}
}

func TestSearch_EmptyResultsIsASuccessfulAnswer(t *testing.T) {
	t.Parallel()

	server := newToolsServer(t, http.StatusOK, `{"results":[]}`, nil)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Search(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, &schemas.BifrostSearchRequest{
		Provider: schemas.DeepSeek,
		Query:    "nothing matches this",
	})
	if bifrostErr != nil {
		t.Fatalf("a no-match search is not an error: %v", bifrostErr.Error.Message)
	}
	if resp.Results == nil {
		t.Fatal("results must marshal as [] rather than null")
	}
	if len(resp.Results) != 0 {
		t.Fatalf("results = %d, want 0", len(resp.Results))
	}
	// DeepSeek does not charge for an empty result set, so the gateway must not report a
	// cost for one. Without Usage the cost engine has nothing to price.
	if resp.Usage != nil {
		t.Fatalf("Usage = %+v, want nil for a no-match search", resp.Usage)
	}
}

// 402 means "top up", not "retry" and not "bad key". Flattening it would hide the fix.
func TestSearch_PreservesUpstreamStatusCodes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status int
	}{
		{name: "insufficient balance", status: http.StatusPaymentRequired},
		{name: "bad key", status: http.StatusUnauthorized},
		{name: "qps exceeded", status: http.StatusTooManyRequests},
		{name: "upstream failure", status: http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := newToolsServer(t, tc.status, `{"error":{"message":"upstream said no","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`, nil)
			provider, err := newTestDeepSeekProvider(server.URL)
			if err != nil {
				t.Fatalf("NewDeepSeekProvider: %v", err)
			}

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			_, bifrostErr := provider.Search(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, &schemas.BifrostSearchRequest{
				Provider: schemas.DeepSeek,
				Query:    "q",
			})
			if bifrostErr == nil {
				t.Fatal("expected an error")
			}
			if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != tc.status {
				t.Fatalf("StatusCode = %v, want %d", bifrostErr.StatusCode, tc.status)
			}
		})
	}
}

func TestFetch_ReturnsMarkdownAndReportsPerRequestCost(t *testing.T) {
	t.Parallel()

	captured := &toolsRequestCapture{}
	server := newToolsServer(t, http.StatusOK, `{"title":"Example","content":"# Heading\n\nBody text."}`, captured)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Fetch(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, &schemas.BifrostFetchRequest{
		Provider: schemas.DeepSeek,
		URL:      "https://example.com/page",
	})
	if bifrostErr != nil {
		t.Fatalf("Fetch: %v", bifrostErr.Error.Message)
	}

	if captured.path != "/v1/tools/fetch" {
		t.Fatalf("path = %q, want /v1/tools/fetch", captured.path)
	}
	var sent map[string]any
	if err := json.Unmarshal(captured.body, &sent); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if sent["url"] != "https://example.com/page" {
		t.Fatalf("url = %v, want the caller's url", sent["url"])
	}
	if _, present := sent["model"]; present {
		t.Fatalf("model must not be sent to the tools API, got %v", sent["model"])
	}

	if resp.Title != "Example" {
		t.Fatalf("title = %q, want Example", resp.Title)
	}
	if resp.Content != "# Heading\n\nBody text." {
		t.Fatalf("content = %q, want the upstream Markdown unchanged", resp.Content)
	}
	if resp.Usage == nil || resp.Usage.Cost == nil || resp.Usage.Cost.TotalCost != 0.00075 {
		t.Fatalf("Usage.Cost = %+v, want a flat 0.00075", resp.Usage)
	}
	if resp.ExtraFields.RequestType != schemas.FetchRequest {
		t.Fatalf("RequestType = %q, want fetch", resp.ExtraFields.RequestType)
	}
}

func TestFetch_PreservesSecurityPolicyRejection(t *testing.T) {
	t.Parallel()

	server := newToolsServer(t, http.StatusForbidden, `{"error":{"message":"rejected by security policy","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`, nil)
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Fetch(ctx, schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}, &schemas.BifrostFetchRequest{
		Provider: schemas.DeepSeek,
		URL:      "https://example.com/blocked",
	})
	if bifrostErr == nil {
		t.Fatal("expected an error")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusForbidden {
		t.Fatalf("StatusCode = %v, want 403", bifrostErr.StatusCode)
	}
}

// Search and Fetch share one HTTP path, and their status codes are actionable rather than
// interchangeable: 402 means top up the balance, 403 means the page was refused by policy,
// 404 means there is no usable content, and 429 means the account QPS budget is spent.
// Flattening any of them into a generic provider error would hide the fix, so every code
// the vendor documents is pinned here.
func TestTools_PreservesUpstreamErrors(t *testing.T) {
	t.Parallel()

	const upstreamMessage = "upstream says no"
	tests := []struct {
		name   string
		status int
		search bool
	}{
		{name: "search 400 invalid field", status: http.StatusBadRequest, search: true},
		{name: "search 401 bad key", status: http.StatusUnauthorized, search: true},
		{name: "search 402 insufficient balance", status: http.StatusPaymentRequired, search: true},
		{name: "search 404 no results", status: http.StatusNotFound, search: true},
		{name: "search 408 body read timeout", status: http.StatusRequestTimeout, search: true},
		{name: "search 413 body too large", status: http.StatusRequestEntityTooLarge, search: true},
		{name: "search 429 QPS exceeded", status: http.StatusTooManyRequests, search: true},
		{name: "search 500 internal", status: http.StatusInternalServerError, search: true},
		{name: "search 504 downstream timeout", status: http.StatusGatewayTimeout, search: true},
		{name: "fetch 403 security policy", status: http.StatusForbidden},
		{name: "fetch 429 QPS exceeded", status: http.StatusTooManyRequests},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := fmt.Sprintf(`{"error":{"message":%q,"type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`, upstreamMessage)
			server := newToolsServer(t, tt.status, body, nil)
			provider, err := newTestDeepSeekProvider(server.URL)
			if err != nil {
				t.Fatalf("NewDeepSeekProvider: %v", err)
			}

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			key := schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}
			var bifrostErr *schemas.BifrostError
			if tt.search {
				_, bifrostErr = provider.Search(ctx, key, &schemas.BifrostSearchRequest{Provider: schemas.DeepSeek, Query: "q"})
			} else {
				_, bifrostErr = provider.Fetch(ctx, key, &schemas.BifrostFetchRequest{Provider: schemas.DeepSeek, URL: "https://example.com/"})
			}

			if bifrostErr == nil {
				t.Fatalf("status %d: expected an error", tt.status)
			}
			if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != tt.status {
				t.Fatalf("StatusCode = %v, want %d", bifrostErr.StatusCode, tt.status)
			}
			if bifrostErr.Error == nil || !strings.Contains(bifrostErr.Error.Message, upstreamMessage) {
				t.Fatalf("message = %+v, want it to carry the upstream text", bifrostErr.Error)
			}
		})
	}
}
