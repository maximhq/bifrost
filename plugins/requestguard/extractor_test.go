package requestguard

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestExtractChatRequestFeatures(t *testing.T) {
	parallel := true
	var format interface{} = json.RawMessage(`{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"}}}`)
	chat := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-test",
		Input: []schemas.ChatMessage{{
			Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: "https://example.test/a.png"}},
				{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: "https://example.test/b.png"}},
			}},
		}},
		Params: &schemas.ChatParameters{
			Tools:               []schemas.ChatTool{{}, {}},
			ParallelToolCalls:   &parallel,
			ToolChoice:          &schemas.ChatToolChoice{ChatToolChoiceStr: stringPtr("required")},
			ResponseFormat:      &format,
			MaxCompletionTokens: intPtr(512),
		},
	}

	for _, requestType := range []schemas.RequestType{schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest} {
		t.Run(string(requestType), func(t *testing.T) {
			features := ExtractRequestFeatures(&schemas.BifrostRequest{RequestType: requestType, ChatRequest: chat})
			if !features.Supported || features.Provider != schemas.OpenAI || features.Model != "gpt-test" {
				t.Fatalf("identity/support extraction = %+v", features)
			}
			if features.IsStreaming != (requestType == schemas.ChatCompletionStreamRequest) {
				t.Errorf("IsStreaming = %v for %s", features.IsStreaming, requestType)
			}
			if !features.RequiresTools || features.ToolCount != 2 || !features.RequiresParallelTools {
				t.Errorf("tool features = %+v", features)
			}
			if !features.RequiresToolChoice || !features.RequiresForcedToolChoice {
				t.Errorf("tool-choice features = %+v", features)
			}
			if !features.RequiresStructuredOutput || !features.RequiresStructuredOutputWithTools {
				t.Errorf("structured-output features = %+v", features)
			}
			if !features.RequiresVision || features.ImageCount != 2 {
				t.Errorf("vision features = %+v", features)
			}
			if features.RequestedMaxOutputTokens != 512 {
				t.Errorf("RequestedMaxOutputTokens = %d, want 512", features.RequestedMaxOutputTokens)
			}
		})
	}
}

func TestExtractResponsesRequestFeatures(t *testing.T) {
	parallel := true
	responses := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-test",
		Input: []schemas.ResponsesMessage{{
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{Type: schemas.ResponsesInputMessageContentBlockTypeImage, ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{ImageURL: stringPtr("https://example.test/image.png")}},
			}},
		}},
		Params: &schemas.ResponsesParameters{
			Tools:             []schemas.ResponsesTool{{}},
			ParallelToolCalls: &parallel,
			ToolChoice:        &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: stringPtr("required")},
			Text:              &schemas.ResponsesTextConfig{Format: &schemas.ResponsesTextConfigFormat{Type: "json_schema"}},
			MaxOutputTokens:   intPtr(700),
		},
	}

	for _, requestType := range []schemas.RequestType{schemas.ResponsesRequest, schemas.ResponsesStreamRequest} {
		t.Run(string(requestType), func(t *testing.T) {
			features := ExtractRequestFeatures(&schemas.BifrostRequest{RequestType: requestType, ResponsesRequest: responses})
			if !features.Supported || features.Provider != schemas.Anthropic || features.Model != "claude-test" {
				t.Fatalf("identity/support extraction = %+v", features)
			}
			if features.IsStreaming != (requestType == schemas.ResponsesStreamRequest) {
				t.Errorf("IsStreaming = %v for %s", features.IsStreaming, requestType)
			}
			if !features.RequiresTools || features.ToolCount != 1 || !features.RequiresParallelTools {
				t.Errorf("tool features = %+v", features)
			}
			if !features.RequiresToolChoice || !features.RequiresForcedToolChoice {
				t.Errorf("tool-choice features = %+v", features)
			}
			if !features.RequiresStructuredOutput || !features.RequiresStructuredOutputWithTools {
				t.Errorf("structured-output features = %+v", features)
			}
			if !features.RequiresVision || features.ImageCount != 1 {
				t.Errorf("vision features = %+v", features)
			}
			if features.RequestedMaxOutputTokens != 700 {
				t.Errorf("RequestedMaxOutputTokens = %d, want 700", features.RequestedMaxOutputTokens)
			}
		})
	}
}

func TestExtractVisionWhenChatParamsAreNil(t *testing.T) {
	features := ExtractRequestFeatures(&schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{{Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{
				Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: "https://example.test/image.png"},
			}}}}},
		},
	})
	if !features.RequiresVision || features.ImageCount != 1 {
		t.Fatalf("features without Params = %+v, want vision detected", features)
	}
}

func TestExtractVisionWhenResponsesParamsAreNil(t *testing.T) {
	features := ExtractRequestFeatures(&schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Input: []schemas.ResponsesMessage{{Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type:                                   schemas.ResponsesInputMessageContentBlockTypeImage,
				ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{ImageURL: stringPtr("https://example.test/image.png")},
			}}}}},
		},
	})
	if !features.RequiresVision || features.ImageCount != 1 {
		t.Fatalf("Responses features without Params = %+v, want vision detected", features)
	}
}

func TestExtractRequestFeaturesUnsupportedAndNilInputs(t *testing.T) {
	tests := []struct {
		name string
		req  *schemas.BifrostRequest
	}{
		{name: "nil request"},
		{name: "unsupported request type", req: &schemas.BifrostRequest{RequestType: schemas.EmbeddingRequest}},
		{name: "chat with nil body", req: &schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest}},
		{name: "responses with nil params", req: &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "model"}}},
		{name: "infer unary type", req: &schemas.BifrostRequest{ChatRequest: &schemas.BifrostChatRequest{Model: "model"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			features := ExtractRequestFeatures(tt.req)
			if tt.req == nil {
				if features.Supported || features.RequestType != "" {
					t.Fatalf("nil request features = %+v, want zero value", features)
				}
				return
			}
			if tt.req.RequestType == schemas.EmbeddingRequest && features.Supported {
				t.Fatalf("embedding request marked supported: %+v", features)
			}
			if tt.req.RequestType == schemas.ChatCompletionRequest && !features.Supported {
				t.Fatalf("chat request marked unsupported: %+v", features)
			}
			if features.ImageCount != 0 || features.ToolCount != 0 || features.RequiresStructuredOutput {
				t.Errorf("unexpected features from nil/unsupported sub-struct: %+v", features)
			}
		})
	}
}

func TestExtractChatStructuredJSONObject(t *testing.T) {
	var format interface{} = map[string]interface{}{"type": "json_object"}
	features := ExtractRequestFeatures(&schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Params: &schemas.ChatParameters{ResponseFormat: &format}},
	})
	if !features.RequiresStructuredOutput {
		t.Fatal("json_object response_format was not recognized as structured output")
	}
}

func BenchmarkExtractRequestFeatures(b *testing.B) {
	request := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-test",
			Params: &schemas.ChatParameters{
				Tools:               []schemas.ChatTool{{}, {}},
				ParallelToolCalls:   boolPtr(true),
				ToolChoice:          &schemas.ChatToolChoice{ChatToolChoiceStr: stringPtr("required")},
				MaxCompletionTokens: intPtr(512),
			},
		},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ExtractRequestFeatures(request)
	}
}
