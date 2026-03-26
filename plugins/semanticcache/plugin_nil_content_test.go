package semanticcache

import (
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
			// This should not panic
			text, hash, err := plugin.extractTextForEmbedding(tt.request)
			// We don't care about the error — the important thing is no panic
			t.Logf("text=%q, hash=%q, err=%v", text, hash, err)
		})
	}
}

func TestPrepareDirectCacheLookup_ResponsesStreamRequest(t *testing.T) {
	plugin := &Plugin{
		config: getDefaultTestConfig(),
		logger: bifrost.NewDefaultLogger(schemas.LogLevelDebug),
	}

	req := &schemas.BifrostRequest{
		RequestType:      schemas.ResponsesStreamRequest,
		ResponsesRequest: CreateStreamingResponsesRequest("Explain cache invalidation", 0.2, 200),
	}

	ctx := CreateContextWithCacheKey("responses-stream-direct")
	directID, err := plugin.prepareDirectCacheLookup(ctx, req, "responses-stream-direct")
	if err != nil {
		t.Fatalf("prepareDirectCacheLookup failed: %v", err)
	}
	if directID == "" {
		t.Fatal("expected deterministic direct cache id")
	}
	if got, _ := ctx.Value(requestHashKey).(string); got == "" {
		t.Fatal("expected request hash to be stored in context")
	}
	if got, _ := ctx.Value(requestParamsHashKey).(string); got == "" {
		t.Fatal("expected params hash to be stored in context")
	}
}

func TestPrepareDirectCacheLookup_UnsupportedRequestTypeFailsClosed(t *testing.T) {
	plugin := &Plugin{
		config: getDefaultTestConfig(),
		logger: bifrost.NewDefaultLogger(schemas.LogLevelDebug),
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

	ctx := CreateContextWithCacheKey("unsupported-direct")
	directID, err := plugin.prepareDirectCacheLookup(ctx, req, "unsupported-direct")
	if err == nil {
		t.Fatal("expected prepareDirectCacheLookup to reject unsupported request type")
	}
	if directID != "" {
		t.Fatalf("expected no direct cache id, got %q", directID)
	}
	if got, _ := ctx.Value(requestHashKey).(string); got != "" {
		t.Fatalf("expected request hash to remain unset, got %q", got)
	}
	if got, _ := ctx.Value(requestParamsHashKey).(string); got != "" {
		t.Fatalf("expected params hash to remain unset, got %q", got)
	}
	if got, _ := ctx.Value(requestStorageIDKey).(string); got != "" {
		t.Fatalf("expected storage id to remain unset, got %q", got)
	}
}

func TestPreLLMHookSkipsUnsupportedCountTokensRequest(t *testing.T) {
	plugin := &Plugin{
		config: getDefaultTestConfig(),
		logger: bifrost.NewDefaultLogger(schemas.LogLevelDebug),
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

	ctx := CreateContextWithCacheKey("count-tokens-test")

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
	if got, _ := ctx.Value(requestIDKey).(string); got != "" {
		t.Fatalf("expected requestIDKey to remain unset, got %q", got)
	}
	if got, _ := ctx.Value(requestHashKey).(string); got != "" {
		t.Fatalf("expected requestHashKey to remain unset, got %q", got)
	}
	if got, _ := ctx.Value(requestParamsHashKey).(string); got != "" {
		t.Fatalf("expected requestParamsHashKey to remain unset, got %q", got)
	}
	if got, _ := ctx.Value(requestStorageIDKey).(string); got != "" {
		t.Fatalf("expected requestStorageIDKey to remain unset, got %q", got)
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

	// This should not panic
	result := plugin.getNormalizedInputForCaching(request)
	t.Logf("result type: %T", result)
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
