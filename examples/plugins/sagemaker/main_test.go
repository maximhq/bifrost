package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sagemakerruntime"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	testModel        = "example-model"
	testEndpointName = "openai-compatible-endpoint"
)

type mockRuntimeClient struct {
	input  *sagemakerruntime.InvokeEndpointInput
	output *sagemakerruntime.InvokeEndpointOutput
	err    error
	invoke func(context.Context, *sagemakerruntime.InvokeEndpointInput) (*sagemakerruntime.InvokeEndpointOutput, error)
}

func (client *mockRuntimeClient) InvokeEndpoint(ctx context.Context, input *sagemakerruntime.InvokeEndpointInput, _ ...func(*sagemakerruntime.Options)) (*sagemakerruntime.InvokeEndpointOutput, error) {
	client.input = input
	if client.invoke != nil {
		return client.invoke(ctx, input)
	}
	return client.output, client.err
}

func TestParseConfig(t *testing.T) {
	t.Run("normalizes defaults and optional routing fields", func(t *testing.T) {
		config, err := parseConfig(map[string]any{
			"region": " us-east-1 ",
			"endpoints": map[string]any{
				" * ": map[string]any{
					"endpoint_name":            " shared-endpoint ",
					"target_model":             " model.tar.gz ",
					"target_variant":           " blue ",
					"inference_component_name": " inference-component ",
					"custom_attributes":        " tenant=example ",
				},
			},
		})
		if err != nil {
			t.Fatalf("parseConfig() error = %v", err)
		}

		if config.Provider != defaultProvider {
			t.Fatalf("provider = %q, want %q", config.Provider, defaultProvider)
		}
		if config.Region != "us-east-1" {
			t.Fatalf("region = %q, want us-east-1", config.Region)
		}
		if config.TimeoutSeconds != int(defaultTimeout/time.Second) {
			t.Fatalf("timeout = %d, want %d", config.TimeoutSeconds, int(defaultTimeout/time.Second))
		}
		endpoint := config.Endpoints[wildcardModel]
		if endpoint.EndpointName != "shared-endpoint" ||
			endpoint.TargetModel != "model.tar.gz" ||
			endpoint.TargetVariant != "blue" ||
			endpoint.InferenceComponentName != "inference-component" ||
			endpoint.CustomAttributes != "tenant=example" {
			t.Fatalf("unexpected endpoint config: %+v", endpoint)
		}
	})

	tests := []struct {
		name   string
		config any
	}{
		{name: "nil config", config: nil},
		{name: "non object", config: "us-east-1"},
		{name: "unknown field", config: map[string]any{
			"unknown":   true,
			"endpoints": validEndpointConfig(),
		}},
		{name: "missing endpoints", config: map[string]any{"region": "us-east-1"}},
		{name: "blank model", config: map[string]any{
			"endpoints": map[string]any{" ": map[string]any{"endpoint_name": testEndpointName}},
		}},
		{name: "duplicate normalized model", config: map[string]any{
			"endpoints": map[string]any{
				testModel:       map[string]any{"endpoint_name": testEndpointName},
				" " + testModel: map[string]any{"endpoint_name": "other-endpoint"},
			},
		}},
		{name: "missing endpoint name", config: map[string]any{
			"endpoints": map[string]any{testModel: map[string]any{"target_variant": "blue"}},
		}},
		{name: "custom attributes too long", config: map[string]any{
			"endpoints": map[string]any{testModel: map[string]any{
				"endpoint_name":     testEndpointName,
				"custom_attributes": strings.Repeat("a", 1025),
			}},
		}},
		{name: "custom attributes non ASCII", config: map[string]any{
			"endpoints": map[string]any{testModel: map[string]any{
				"endpoint_name":     testEndpointName,
				"custom_attributes": "tenant=vibhup\u0080",
			}},
		}},
		{name: "negative timeout", config: map[string]any{
			"timeout_seconds": -1,
			"endpoints":       validEndpointConfig(),
		}},
		{name: "excessive timeout", config: map[string]any{
			"timeout_seconds": int(maxTimeout/time.Second) + 1,
			"endpoints":       validEndpointConfig(),
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseConfig(test.config); err == nil {
				t.Fatal("parseConfig() error = nil, want configuration error")
			}
		})
	}
}

func TestResolveEndpointPrefersExactModel(t *testing.T) {
	endpoints := map[string]endpointConfig{
		wildcardModel: {EndpointName: "shared"},
		testModel:     {EndpointName: "dedicated"},
	}

	if endpoint, ok := resolveEndpoint(endpoints, testModel); !ok || endpoint.EndpointName != "dedicated" {
		t.Fatalf("exact endpoint = %+v, %v; want dedicated", endpoint, ok)
	}
	if endpoint, ok := resolveEndpoint(endpoints, "another-model"); !ok || endpoint.EndpointName != "shared" {
		t.Fatalf("wildcard endpoint = %+v, %v; want shared", endpoint, ok)
	}
}

func TestPreLLMHookInvokesSageMakerForChatCompletion(t *testing.T) {
	client := &mockRuntimeClient{
		output: &sagemakerruntime.InvokeEndpointOutput{
			Body: []byte(`{
				"id":"chatcmpl-1",
				"object":"chat.completion",
				"created":1,
				"model":"example-model",
				"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}
			}`),
		},
	}
	setTestState(client)
	t.Cleanup(func() { activeState.Store(nil) })

	request := chatRequest(testModel)
	_, shortCircuit, err := PreLLMHook(testContext(), request)
	if err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}
	if shortCircuit == nil || shortCircuit.Response == nil || shortCircuit.Response.ChatResponse == nil {
		t.Fatal("PreLLMHook() did not return a chat response")
	}
	if got := shortCircuit.Response.ChatResponse.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr; got == nil || *got != "hello" {
		t.Fatalf("chat content = %v, want hello", got)
	}

	if client.input == nil {
		t.Fatal("InvokeEndpoint input was not captured")
	}
	if client.input.EndpointName == nil || *client.input.EndpointName != testEndpointName {
		t.Fatalf("endpoint name = %v, want %q", client.input.EndpointName, testEndpointName)
	}
	if client.input.ContentType == nil || *client.input.ContentType != "application/json" ||
		client.input.Accept == nil || *client.input.Accept != "application/json" {
		t.Fatalf("unexpected content negotiation: content-type=%v accept=%v", client.input.ContentType, client.input.Accept)
	}
	if client.input.TargetModel == nil || *client.input.TargetModel != "model.tar.gz" ||
		client.input.TargetVariant == nil || *client.input.TargetVariant != "blue" ||
		client.input.InferenceComponentName == nil || *client.input.InferenceComponentName != "inference-component" ||
		client.input.CustomAttributes == nil || *client.input.CustomAttributes != "tenant=example" {
		t.Fatalf("optional SageMaker routing fields were not forwarded: %+v", client.input)
	}

	var body map[string]any
	if err := json.Unmarshal(client.input.Body, &body); err != nil {
		t.Fatalf("unmarshal InvokeEndpoint body: %v", err)
	}
	if body["model"] != testModel {
		t.Fatalf("wire model = %v, want %q", body["model"], testModel)
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("wire messages = %#v, want one message", body["messages"])
	}
}

func TestBuildOpenAIRequestBodySupportsGeneralOperations(t *testing.T) {
	tests := []struct {
		name    string
		request *schemas.BifrostRequest
		fields  []string
	}{
		{name: "text completion", request: textCompletionRequest(testModel), fields: []string{"model", "prompt"}},
		{name: "chat completion", request: chatRequest(testModel), fields: []string{"model", "messages"}},
		{name: "responses", request: responsesRequest(testModel), fields: []string{"model", "input"}},
		{name: "embedding", request: embeddingRequest(testModel), fields: []string{"model", "input"}},
		{name: "rerank", request: rerankRequest(testModel), fields: []string{"model", "query", "documents"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, bifrostErr := buildOpenAIRequestBody(testContext(), test.request)
			if bifrostErr != nil {
				t.Fatalf("buildOpenAIRequestBody() error = %v", bifrostErr)
			}
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			for _, field := range test.fields {
				if _, ok := decoded[field]; !ok {
					t.Fatalf("wire body missing %q: %s", field, body)
				}
			}
		})
	}
}

func TestParseOpenAIResponseSupportsGeneralOperations(t *testing.T) {
	tests := []struct {
		name     string
		request  *schemas.BifrostRequest
		body     string
		validate func(*testing.T, *schemas.BifrostResponse)
	}{
		{
			name:    "text completion",
			request: textCompletionRequest(testModel),
			body:    `{"id":"cmpl-1","object":"text_completion","model":"example-model","choices":[{"index":0,"text":"hello","finish_reason":"stop"}]}`,
			validate: func(t *testing.T, response *schemas.BifrostResponse) {
				if response.TextCompletionResponse == nil || len(response.TextCompletionResponse.Choices) != 1 {
					t.Fatalf("unexpected text completion response: %+v", response)
				}
			},
		},
		{
			name:    "chat completion",
			request: chatRequest(testModel),
			body:    `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"example-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			validate: func(t *testing.T, response *schemas.BifrostResponse) {
				if response.ChatResponse == nil || len(response.ChatResponse.Choices) != 1 {
					t.Fatalf("unexpected chat response: %+v", response)
				}
			},
		},
		{
			name:    "responses",
			request: responsesRequest(testModel),
			body:    `{"id":"resp-1","object":"response","created_at":1,"model":"example-model","output":[],"tools":[]}`,
			validate: func(t *testing.T, response *schemas.BifrostResponse) {
				if response.ResponsesResponse == nil || response.ResponsesResponse.Object != "response" {
					t.Fatalf("unexpected responses response: %+v", response)
				}
			},
		},
		{
			name:    "embedding",
			request: embeddingRequest(testModel),
			body:    `{"object":"list","model":"example-model","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}]}`,
			validate: func(t *testing.T, response *schemas.BifrostResponse) {
				if response.EmbeddingResponse == nil || len(response.EmbeddingResponse.Data) != 1 {
					t.Fatalf("unexpected embedding response: %+v", response)
				}
			},
		},
		{
			name:    "rerank",
			request: rerankRequest(testModel),
			body:    `{"id":"rerank-1","results":[{"index":0,"relevance_score":0.9}]}`,
			validate: func(t *testing.T, response *schemas.BifrostResponse) {
				if response.RerankResponse == nil || len(response.RerankResponse.Results) != 1 {
					t.Fatalf("unexpected rerank response: %+v", response)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := parseOpenAIResponse(test.request, []byte(test.body))
			if err != nil {
				t.Fatalf("parseOpenAIResponse() error = %v", err)
			}
			test.validate(t, response)
		})
	}
}

func TestPreLLMHookPassesThroughOtherProviders(t *testing.T) {
	client := &mockRuntimeClient{}
	setTestState(client)
	t.Cleanup(func() { activeState.Store(nil) })

	request := embeddingRequest(testModel)
	request.EmbeddingRequest.Provider = schemas.OpenAI
	returned, shortCircuit, err := PreLLMHook(testContext(), request)
	if err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}
	if returned != request || shortCircuit != nil {
		t.Fatalf("other provider request was not passed through: request=%p returned=%p shortCircuit=%+v", request, returned, shortCircuit)
	}
	if client.input != nil {
		t.Fatal("InvokeEndpoint was called for another provider")
	}
}

func TestPreLLMHookRejectsStreamingAndUnmappedModels(t *testing.T) {
	setTestState(&mockRuntimeClient{})
	t.Cleanup(func() { activeState.Store(nil) })

	streamRequest := chatRequest(testModel)
	streamRequest.RequestType = schemas.ChatCompletionStreamRequest
	assertShortCircuitError(t, streamRequest, 400, "unsupported_request", false)

	request := embeddingRequest("unmapped-model")
	assertShortCircuitError(t, request, 400, "unsupported_model", false)
}

func TestPreLLMHookMapsInvokeErrorsAndAllowsFallbacks(t *testing.T) {
	t.Run("service error", func(t *testing.T) {
		setTestState(&mockRuntimeClient{err: errors.New("service unavailable")})
		t.Cleanup(func() { activeState.Store(nil) })
		assertShortCircuitError(t, embeddingRequest(testModel), 502, "sagemaker_invoke_failed", true)
	})

	t.Run("timeout", func(t *testing.T) {
		client := &mockRuntimeClient{
			invoke: func(ctx context.Context, _ *sagemakerruntime.InvokeEndpointInput) (*sagemakerruntime.InvokeEndpointOutput, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}
		activeState.Store(&pluginState{
			provider: defaultProvider,
			timeout:  time.Millisecond,
			endpoints: map[string]endpointConfig{
				testModel: {EndpointName: testEndpointName},
			},
			client: client,
		})
		t.Cleanup(func() { activeState.Store(nil) })
		assertShortCircuitError(t, embeddingRequest(testModel), 504, "sagemaker_timeout", true)
	})
}

func TestParseOpenAIResponseRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name    string
		request *schemas.BifrostRequest
		body    string
	}{
		{name: "invalid JSON", request: embeddingRequest(testModel), body: "{"},
		{name: "in-band error", request: embeddingRequest(testModel), body: `{"error":{"message":"model failed"}}`},
		{name: "chat without choices", request: chatRequest(testModel), body: `{"object":"chat.completion"}`},
		{name: "embedding wrong item count", request: embeddingRequest(testModel), body: `{"object":"list","data":[]}`},
		{name: "embedding empty vector", request: embeddingRequest(testModel), body: `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[]}]}`},
		{name: "rerank invalid index", request: rerankRequest(testModel), body: `{"results":[{"index":2,"relevance_score":0.9}]}`},
		{name: "rerank duplicate index", request: rerankRequestWithDocuments(testModel, 2), body: `{"results":[{"index":0,"relevance_score":0.9},{"index":0,"relevance_score":0.8}]}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseOpenAIResponse(test.request, []byte(test.body)); err == nil {
				t.Fatal("parseOpenAIResponse() error = nil, want response validation error")
			}
		})
	}
}

func TestCleanupClearsState(t *testing.T) {
	setTestState(&mockRuntimeClient{})
	if activeState.Load() == nil {
		t.Fatal("test state was not initialized")
	}
	if err := Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if activeState.Load() != nil {
		t.Fatal("Cleanup() did not clear plugin state")
	}
}

func TestFailureDoesNotExposeUpstreamErrors(t *testing.T) {
	client := &mockRuntimeClient{err: errors.New("sensitive internal endpoint detail")}
	setTestState(client)
	t.Cleanup(func() { activeState.Store(nil) })

	_, shortCircuit, err := PreLLMHook(testContext(), embeddingRequest(testModel))
	if err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}
	if shortCircuit == nil || shortCircuit.Error == nil || shortCircuit.Error.Error == nil {
		t.Fatal("missing short-circuit error")
	}
	if strings.Contains(shortCircuit.Error.Error.Message, "sensitive") {
		t.Fatalf("client error exposed upstream details: %q", shortCircuit.Error.Error.Message)
	}
}

func validEndpointConfig() map[string]any {
	return map[string]any{
		testModel: map[string]any{"endpoint_name": testEndpointName},
	}
}

func setTestState(client runtimeClient) {
	activeState.Store(&pluginState{
		provider: defaultProvider,
		timeout:  time.Second,
		endpoints: map[string]endpointConfig{
			testModel: {
				EndpointName:           testEndpointName,
				TargetModel:            "model.tar.gz",
				TargetVariant:          "blue",
				InferenceComponentName: "inference-component",
				CustomAttributes:       "tenant=example",
			},
		},
		client: client,
	})
}

func testContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func textCompletionRequest(model string) *schemas.BifrostRequest {
	prompt := "hello"
	return &schemas.BifrostRequest{
		RequestType: schemas.TextCompletionRequest,
		TextCompletionRequest: &schemas.BifrostTextCompletionRequest{
			Provider: schemas.ModelProvider(defaultProvider),
			Model:    model,
			Input:    &schemas.TextCompletionInput{PromptStr: &prompt},
		},
	}
}

func chatRequest(model string) *schemas.BifrostRequest {
	content := "hello"
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.ModelProvider(defaultProvider),
			Model:    model,
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: &content},
				},
			},
		},
	}
}

func responsesRequest(model string) *schemas.BifrostRequest {
	content := "hello"
	role := schemas.ResponsesInputMessageRoleUser
	return &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.ModelProvider(defaultProvider),
			Model:    model,
			Input: []schemas.ResponsesMessage{
				{
					Role:    &role,
					Content: &schemas.ResponsesMessageContent{ContentStr: &content},
				},
			},
		},
	}
}

func embeddingRequest(model string) *schemas.BifrostRequest {
	input := "hello"
	return &schemas.BifrostRequest{
		RequestType: schemas.EmbeddingRequest,
		EmbeddingRequest: &schemas.BifrostEmbeddingRequest{
			Provider: schemas.ModelProvider(defaultProvider),
			Model:    model,
			Input:    &schemas.EmbeddingInput{Text: &input},
		},
	}
}

func rerankRequest(model string) *schemas.BifrostRequest {
	return rerankRequestWithDocuments(model, 1)
}

func rerankRequestWithDocuments(model string, documentCount int) *schemas.BifrostRequest {
	documents := make([]schemas.RerankDocument, documentCount)
	for index := range documents {
		documents[index] = schemas.RerankDocument{Text: fmt.Sprintf("document %d", index)}
	}
	return &schemas.BifrostRequest{
		RequestType: schemas.RerankRequest,
		RerankRequest: &schemas.BifrostRerankRequest{
			Provider:  schemas.ModelProvider(defaultProvider),
			Model:     model,
			Query:     "hello",
			Documents: documents,
		},
	}
}

func assertShortCircuitError(t *testing.T, request *schemas.BifrostRequest, wantStatus int, wantCode string, wantFallbacks bool) {
	t.Helper()

	_, shortCircuit, err := PreLLMHook(testContext(), request)
	if err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}
	if shortCircuit == nil || shortCircuit.Error == nil {
		t.Fatal("PreLLMHook() did not return an error short circuit")
	}
	bifrostError := shortCircuit.Error
	if bifrostError.StatusCode == nil || *bifrostError.StatusCode != wantStatus {
		t.Fatalf("status = %v, want %d", bifrostError.StatusCode, wantStatus)
	}
	if bifrostError.Error == nil || bifrostError.Error.Code == nil || *bifrostError.Error.Code != wantCode {
		t.Fatalf("code = %+v, want %q", bifrostError.Error, wantCode)
	}
	if bifrostError.AllowFallbacks == nil || *bifrostError.AllowFallbacks != wantFallbacks {
		t.Fatalf("allow_fallbacks = %v, want %v", bifrostError.AllowFallbacks, wantFallbacks)
	}
}
