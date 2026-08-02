package semanticcache

import (
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestExtractTextForEmbedding_NilContent verifies that extractTextForEmbedding
// does not panic when chat messages have nil Content (e.g., assistant tool-call messages).
func TestExtractTextForEmbedding_NilContent(t *testing.T) {
	plugin := &Plugin{
		config: &Config{},
	}

	tests := []struct {
		name    string
		request *schemas.BifrostRequest
	}{
		{
			name: "ChatRequest with nil Content in assistant tool-call message",
			request: &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Provider: schemas.OpenAI,
					Model:    "gpt-4o-mini",
					Input: []schemas.ChatMessage{
						{
							Role: schemas.ChatMessageRoleUser,
							Content: &schemas.ChatMessageContent{
								ContentStr: bifrost.Ptr("Call the get_weather function"),
							},
						},
						{
							Role:    schemas.ChatMessageRoleAssistant,
							Content: nil, // tool-call message with no content
							ChatAssistantMessage: &schemas.ChatAssistantMessage{
								ToolCalls: []schemas.ChatAssistantMessageToolCall{
									{
										ID:   bifrost.Ptr("call_123"),
										Type: bifrost.Ptr("function"),
										Function: schemas.ChatAssistantMessageToolCallFunction{
											Name:      bifrost.Ptr("get_weather"),
											Arguments: `{"location": "San Francisco"}`,
										},
									},
								},
							},
						},
					},
					Params: &schemas.ChatParameters{
						Temperature:         bifrost.Ptr(0.7),
						MaxCompletionTokens: bifrost.Ptr(100),
					},
				},
			},
		},
		{
			name: "ChatRequest where all messages have nil Content",
			request: &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Provider: schemas.OpenAI,
					Model:    "gpt-4o-mini",
					Input: []schemas.ChatMessage{
						{
							Role:    schemas.ChatMessageRoleAssistant,
							Content: nil,
						},
					},
					Params: &schemas.ChatParameters{
						Temperature:         bifrost.Ptr(0.7),
						MaxCompletionTokens: bifrost.Ptr(100),
					},
				},
			},
		},
		{
			name: "ResponsesRequest with nil Content",
			request: &schemas.BifrostRequest{
				RequestType:      schemas.ResponsesRequest,
				ResponsesRequest: createResponsesRequestWithNilContent(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Primary contract: must not panic on nil-content messages.
			// Secondary: returned text must not contain stringification
			// artifacts, and the all-nil case must surface as an error.
			text, err := plugin.extractTextForEmbedding(nil, tt.request)
			if strings.Contains(text, "<nil>") || strings.Contains(text, "%!") {
				t.Fatalf("extractTextForEmbedding produced a stringification artifact: %q", text)
			}
			if tt.name == "ChatRequest where all messages have nil Content" {
				if err == nil {
					t.Fatalf("expected error when no message has text content, got text=%q", text)
				}
				if text != "" {
					t.Fatalf("expected empty text when all content is nil, got %q", text)
				}
			}
		})
	}
}

// TestPreLLMHookSeedsDirectCacheIDForResponsesStream verifies the streaming
// Responses path runs through PreLLMHook → performDirectSearch and stamps a
// deterministic DirectCacheID on the per-request cacheState.
func TestPreLLMHookSeedsDirectCacheIDForResponsesStream(t *testing.T) {
	plugin := &Plugin{
		config: getDefaultTestConfig(),
		logger: bifrost.NewDefaultLogger(schemas.LogLevelDebug),
		store:  newDirectFastPathStore(),
	}

	req := &schemas.BifrostRequest{
		RequestType:      schemas.ResponsesStreamRequest,
		ResponsesRequest: CreateStreamingResponsesRequest("Explain cache invalidation", 0.2, 200),
	}

	ctx := CreateContextWithCacheKeyAndType(t, "responses-stream-direct", CacheTypeDirect)
	if _, _, err := plugin.PreLLMHook(ctx, req); err != nil {
		t.Fatalf("PreLLMHook failed: %v", err)
	}

	requestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	state := plugin.getCacheState(requestID)
	if state == nil {
		t.Fatal("expected cache state to be created")
	}
	if state.DirectCacheID == "" {
		t.Fatal("expected DirectCacheID to be populated by direct search")
	}
	if state.ParamsHash == "" {
		t.Fatal("expected ParamsHash to be populated")
	}
}

// TestPreLLMHookFailsClosedForUnsupportedRequestType verifies the plugin
// short-circuits early for unsupported request types and never populates
// state fields that downstream caching logic would read.
func TestPreLLMHookFailsClosedForUnsupportedRequestType(t *testing.T) {
	plugin := &Plugin{
		config: getDefaultTestConfig(),
		logger: bifrost.NewDefaultLogger(schemas.LogLevelDebug),
		store:  newDirectFastPathStore(),
	}

	req := &schemas.BifrostRequest{
		RequestType: schemas.PassthroughRequest,
		PassthroughRequest: &schemas.BifrostPassthroughRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Method:   "GET",
			Path:     "/v1/models",
		},
	}

	ctx := CreateContextWithCacheKey(t, "unsupported-direct")
	if _, shortCircuit, err := plugin.PreLLMHook(ctx, req); err != nil || shortCircuit != nil {
		t.Fatalf("PreLLMHook unexpected: shortCircuit=%v err=%v", shortCircuit, err)
	}

	requestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	state := plugin.getCacheState(requestID)
	// Unsupported types create the state slot (reset happens up front) but
	// never populate the caching fields.
	if state != nil {
		if state.DirectCacheID != "" {
			t.Fatalf("expected DirectCacheID unset, got %q", state.DirectCacheID)
		}
		if state.ParamsHash != "" {
			t.Fatalf("expected ParamsHash unset, got %q", state.ParamsHash)
		}
		if state.Embeddings != nil {
			t.Fatalf("expected Embeddings unset, got %v", state.Embeddings)
		}
	}
}

// TestPreLLMHookSkipsUnsupportedCountTokensRequest verifies CountTokensRequest
// (which is not in the supported set) flows through PreLLMHook without
// short-circuiting and without populating cache fields.
func TestPreLLMHookSkipsUnsupportedCountTokensRequest(t *testing.T) {
	plugin := &Plugin{
		config: getDefaultTestConfig(),
		logger: bifrost.NewDefaultLogger(schemas.LogLevelDebug),
		store:  newDirectFastPathStore(),
	}

	req := &schemas.BifrostRequest{
		RequestType: schemas.CountTokensRequest,
		CountTokensRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-sonnet-4-5",
			Input: []schemas.ResponsesMessage{
				{
					Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{
						ContentStr: bifrost.Ptr("How many tokens is this message?"),
					},
				},
			},
		},
	}

	ctx := CreateContextWithCacheKey(t, "count-tokens-test")
	modifiedReq, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	if err != nil {
		t.Fatalf("PreLLMHook failed: %v", err)
	}
	if modifiedReq != req {
		t.Fatal("expected original request to be returned unchanged")
	}
	if shortCircuit != nil {
		t.Fatal("expected no short-circuit for unsupported count tokens request")
	}

	requestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if state := plugin.getCacheState(requestID); state != nil {
		if state.DirectCacheID != "" || state.ParamsHash != "" || state.Embeddings != nil {
			t.Fatalf("expected unsupported request to leave state empty, got %+v", state)
		}
	}
}

// TestGetNormalizedInputForCaching_NilContent verifies that getNormalizedInputForCaching
// does not panic when chat messages have nil Content.
func TestGetNormalizedInputForCaching_NilContent(t *testing.T) {
	plugin := &Plugin{
		config: &Config{},
	}

	request := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Call the get_weather function"),
					},
				},
				{
					Role:    schemas.ChatMessageRoleAssistant,
					Content: nil,
					ChatAssistantMessage: &schemas.ChatAssistantMessage{
						ToolCalls: []schemas.ChatAssistantMessageToolCall{
							{
								ID:   bifrost.Ptr("call_123"),
								Type: bifrost.Ptr("function"),
								Function: schemas.ChatAssistantMessageToolCallFunction{
									Name:      bifrost.Ptr("get_weather"),
									Arguments: `{"location": "San Francisco"}`,
								},
							},
						},
					},
				},
			},
			Params: &schemas.ChatParameters{
				Temperature:         bifrost.Ptr(0.7),
				MaxCompletionTokens: bifrost.Ptr(100),
			},
		},
	}

	// Must not panic, and must return a non-nil filtered messages slice
	// of the right element type (we built a ChatCompletionRequest).
	result := plugin.getNormalizedInputForCaching(request)
	if result == nil {
		t.Fatal("getNormalizedInputForCaching returned nil for a valid Chat request")
	}
	msgs, ok := result.([]schemas.ChatMessage)
	if !ok {
		t.Fatalf("expected []schemas.ChatMessage, got %T", result)
	}
	if len(msgs) != len(request.ChatRequest.Input) {
		t.Fatalf("normalized message count %d differs from input %d (filtering changed unexpectedly)", len(msgs), len(request.ChatRequest.Input))
	}
}

// createResponsesRequestWithNilContent builds a BifrostResponsesRequest with a nil Content message for testing.
func createResponsesRequestWithNilContent() *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ResponsesMessage{
			{
				Role: bifrost.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: bifrost.Ptr("Hello"),
				},
			},
			{
				Role:    bifrost.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: nil,
			},
		},
		Params: &schemas.ResponsesParameters{
			Temperature:     bifrost.Ptr(0.7),
			MaxOutputTokens: bifrost.Ptr(100),
		},
	}
}
