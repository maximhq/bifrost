package bifrost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

type astraUpstreamRequest struct {
	Method  string
	Path    string
	Body    []byte
	ReadErr error
}

// TestGPT6AstraMaxReasoningEffortReachesOpenAIUpstream exercises the public
// Responses API through provider selection, OpenAI conversion, JSON encoding,
// and the real HTTP client. The fake upstream makes the silently rewritten
// request observable without requiring a real OpenAI API key.
func TestGPT6AstraMaxReasoningEffortReachesOpenAIUpstream(t *testing.T) {
	requests := make(chan astraUpstreamRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		requests <- astraUpstreamRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Body:    body,
			ReadErr: err,
		}
		writeJSON(w, http.StatusOK, `{"id":"resp_astra_1","object":"response","created_at":1,"status":"completed","model":"gpt-6-astra","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(upstream.Close)

	// This is the capability information currently available to the Go runtime:
	// the datasheet says Astra supports reasoning, but does not publish its
	// reasoning_effort_levels ladder.
	schemas.SetCapabilityResolver(func(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		if provider == schemas.OpenAI && model == "gpt-6-astra" {
			return &schemas.ModelCapabilities{SupportsReasoning: schemas.Ptr(true)}
		}
		return nil
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, upstream.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{{
		ID:     "astra-e2e-key",
		Value:  *schemas.NewSecretVar("sk-local-astra-e2e"),
		Models: schemas.WhiteList{"gpt-6-astra"},
		Weight: 100,
	}})
	client := newStreamTestClient(t, account)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(5*time.Second))
	response, bifrostErr := client.ResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-6-astra",
		Input: []schemas.ResponsesMessage{{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{
				ContentStr: schemas.Ptr("Say hello"),
			},
		}},
		Params: &schemas.ResponsesParameters{
			Reasoning: &schemas.ResponsesParametersReasoning{
				Effort: schemas.Ptr(schemas.ReasoningEffortMax),
			},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ResponsesRequest failed before reaching the upstream: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("ResponsesRequest returned a nil response")
	}

	observed := <-requests
	if observed.ReadErr != nil {
		t.Fatalf("read upstream request body: %v", observed.ReadErr)
	}
	if observed.Method != http.MethodPost || observed.Path != "/v1/responses" {
		t.Fatalf("upstream request = %s %s, want POST /v1/responses", observed.Method, observed.Path)
	}

	var wire struct {
		Model     string `json:"model"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(observed.Body, &wire); err != nil {
		t.Fatalf("decode upstream request body %q: %v", observed.Body, err)
	}
	if wire.Model != "gpt-6-astra" {
		t.Fatalf("upstream model = %q, want gpt-6-astra", wire.Model)
	}
	if wire.Reasoning.Effort != schemas.ReasoningEffortMax {
		t.Fatalf("upstream reasoning.effort = %q, want %q; Bifrost silently changed the requested effort on the wire",
			wire.Reasoning.Effort, schemas.ReasoningEffortMax)
	}
}

// astraResponsesConversionPlugin stands in for the compat plugin, which lives in
// its own module and cannot be imported from core. It makes the same decision
// the compat plugin makes for a chat + tools request on a model that cannot
// reason alongside function tools on chat completions (#7275): mark the request
// for the Responses API and leave the rest to core.
type astraResponsesConversionPlugin struct{}

func (astraResponsesConversionPlugin) GetName() string { return "astra-responses-conversion" }
func (astraResponsesConversionPlugin) Cleanup() error  { return nil }
func (astraResponsesConversionPlugin) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (astraResponsesConversionPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if req.ChatRequest != nil && req.ChatRequest.Params != nil && len(req.ChatRequest.Params.Tools) > 0 {
		ctx.SetValue(schemas.BifrostContextKeyChangeRequestType, schemas.ResponsesRequest)
	}
	return req, nil, nil
}
func (astraResponsesConversionPlugin) PostLLMHook(_ *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return result, bifrostErr, nil
}

func newAstraChatToolsRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-6-astra",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is the weather in Paris?")},
		}},
		Params: &schemas.ChatParameters{
			Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr(schemas.ReasoningEffortMedium)},
			Tools: []schemas.ChatTool{{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        "get_weather",
					Description: schemas.Ptr("Returns weather"),
				},
			}},
		},
	}
}

// astraChatToolsWire is the shape core puts on the wire after the compat
// decision: Responses tools carry name at the top level, and reasoning is the
// nested Responses object rather than chat's reasoning_effort.
type astraChatToolsWire struct {
	Model string `json:"model"`
	Tools []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"tools"`
	Reasoning *struct {
		Effort string `json:"effort"`
	} `json:"reasoning"`
	ReasoningEffort *string `json:"reasoning_effort"`
}

func assertAstraChatToolsWire(t *testing.T, observed astraUpstreamRequest) {
	t.Helper()
	if observed.ReadErr != nil {
		t.Fatalf("read upstream request body: %v", observed.ReadErr)
	}
	if observed.Method != http.MethodPost || observed.Path != "/v1/responses" {
		t.Fatalf("upstream request = %s %s, want POST /v1/responses", observed.Method, observed.Path)
	}
	var wire astraChatToolsWire
	if err := json.Unmarshal(observed.Body, &wire); err != nil {
		t.Fatalf("decode upstream request body %q: %v", observed.Body, err)
	}
	if wire.Model != "gpt-6-astra" {
		t.Fatalf("upstream model = %q, want gpt-6-astra", wire.Model)
	}
	if len(wire.Tools) != 1 || wire.Tools[0].Type != "function" || wire.Tools[0].Name != "get_weather" {
		t.Fatalf("upstream tools = %+v, want one Responses-shaped function tool named get_weather", wire.Tools)
	}
	if wire.Reasoning == nil || wire.Reasoning.Effort != schemas.ReasoningEffortMedium {
		t.Fatalf("upstream reasoning = %+v, want effort %q preserved on the Responses wire", wire.Reasoning, schemas.ReasoningEffortMedium)
	}
	if wire.ReasoningEffort != nil {
		t.Fatalf("upstream carries chat reasoning_effort=%q, want only the Responses reasoning object", *wire.ReasoningEffort)
	}
}

func newAstraChatToolsClient(t *testing.T, upstreamURL string) *Bifrost {
	t.Helper()
	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, upstreamURL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{{
		ID:     "astra-chat-tools-key",
		Value:  *schemas.NewSecretVar("sk-local-astra-chat-tools"),
		Models: schemas.WhiteList{"gpt-6-astra"},
		Weight: 100,
	}})
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:    account,
		Logger:     NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{astraResponsesConversionPlugin{}},
	})
	if err != nil {
		t.Fatalf("failed to initialize bifrost: %v", err)
	}
	t.Cleanup(client.Shutdown)
	return client
}

// TestGPT6AstraChatWithToolsServedThroughResponses pins the wire a chat + tools
// request takes once a plugin marks it for the Responses API: the upstream sees
// POST /v1/responses with Responses-shaped tools and the caller's reasoning
// effort intact, and the caller still receives a chat completion.
func TestGPT6AstraChatWithToolsServedThroughResponses(t *testing.T) {
	requests := make(chan astraUpstreamRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		requests <- astraUpstreamRequest{Method: r.Method, Path: r.URL.Path, Body: body, ReadErr: err}
		writeJSON(w, http.StatusOK, `{"id":"resp_astra_tools","object":"response","created_at":1,"status":"completed","model":"gpt-6-astra","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}","status":"completed"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(upstream.Close)

	client := newAstraChatToolsClient(t, upstream.URL)
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(5*time.Second))
	response, bifrostErr := client.ChatCompletionRequest(ctx, newAstraChatToolsRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionRequest failed: %v", bifrostErr)
	}
	if response == nil || len(response.Choices) == 0 || response.Choices[0].ChatNonStreamResponseChoice == nil {
		t.Fatalf("response = %+v, want a chat completion with one choice", response)
	}
	toolCalls := response.Choices[0].Message.ToolCalls
	if len(toolCalls) != 1 || toolCalls[0].Function.Name == nil || *toolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("tool_calls = %+v, want the upstream function_call mapped back to one chat tool call", toolCalls)
	}

	assertAstraChatToolsWire(t, <-requests)
}

// TestGPT6AstraChatWithToolsStreamServedThroughResponses is the streaming
// twin: the Responses SSE events come back to the caller as chat completion
// chunks.
func TestGPT6AstraChatWithToolsStreamServedThroughResponses(t *testing.T) {
	requests := make(chan astraUpstreamRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		requests <- astraUpstreamRequest{Method: r.Method, Path: r.URL.Path, Body: body, ReadErr: err}
		sseHandler(
			`{"type":"response.created","sequence_number":0,"response":{"id":"resp_astra_stream","object":"response","created_at":1,"status":"in_progress","model":"gpt-6-astra","output":[]}}`,
			`{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Sunny"}`,
			`{"type":"response.completed","sequence_number":2,"response":{"id":"resp_astra_stream","object":"response","created_at":1,"status":"completed","model":"gpt-6-astra","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		)(w, r)
	}))
	t.Cleanup(upstream.Close)

	client := newAstraChatToolsClient(t, upstream.URL)
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(5*time.Second))
	stream, bifrostErr := client.ChatCompletionStreamRequest(ctx, newAstraChatToolsRequest())
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStreamRequest failed: %v", bifrostErr)
	}

	var content string
	var chatChunks int
	for chunk := range stream {
		if chunk.BifrostError != nil {
			t.Fatalf("stream returned error chunk: %v", chunk.BifrostError)
		}
		if chunk.BifrostResponsesStreamResponse != nil {
			t.Fatalf("stream leaked a Responses event %q to a chat completions caller", chunk.BifrostResponsesStreamResponse.Type)
		}
		if chunk.BifrostChatResponse == nil {
			continue
		}
		chatChunks++
		for _, choice := range chunk.BifrostChatResponse.Choices {
			if choice.ChatStreamResponseChoice != nil && choice.Delta != nil && choice.Delta.Content != nil {
				content += *choice.Delta.Content
			}
		}
	}
	if chatChunks == 0 {
		t.Fatal("stream produced no chat completion chunks")
	}
	if content != "Sunny" {
		t.Fatalf("streamed content = %q, want %q assembled from the Responses output_text delta", content, "Sunny")
	}

	assertAstraChatToolsWire(t, <-requests)
}
