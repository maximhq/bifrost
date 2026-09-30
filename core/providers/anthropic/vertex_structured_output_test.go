package anthropic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Claude on Vertex serves structured outputs natively (output_config.format).
// The schema must reach Vertex as output_config.format whether or not extended
// thinking is on: the synthetic bf_so_* tool cannot be forced under thinking,
// so it would leave a reasoning request's schema unenforced.

type vertexSOThinkingCase struct {
	name      string
	reasoning *schemas.ChatReasoning
}

func vertexSOThinkingCases() []vertexSOThinkingCase {
	return []vertexSOThinkingCase{
		{name: "thinking_off"},
		{name: "thinking_effort", reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr("high")}},
		{name: "thinking_budget", reasoning: &schemas.ChatReasoning{MaxTokens: schemas.Ptr(4096)}},
	}
}

func assertVertexNativeStructuredOutput(t *testing.T, outputConfig *AnthropicOutputConfig, tools []AnthropicTool, toolChoice *AnthropicToolChoice) {
	t.Helper()
	if outputConfig == nil || len(outputConfig.Format) == 0 {
		t.Fatalf("expected output_config.format for vertex, got %+v", outputConfig)
	}
	var format struct {
		Type   string         `json:"type"`
		Schema map[string]any `json:"schema"`
	}
	if err := json.Unmarshal(outputConfig.Format, &format); err != nil {
		t.Fatalf("output_config.format is not JSON: %v", err)
	}
	if format.Type != "json_schema" {
		t.Errorf("expected output_config.format.type=json_schema, got %q", format.Type)
	}
	if format.Schema["type"] != "object" {
		t.Errorf("expected the caller's schema in output_config.format.schema, got %v", format.Schema)
	}
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name, "bf_so_") {
			t.Errorf("expected no synthetic %q tool for vertex", tool.Name)
		}
	}
	if toolChoice != nil {
		t.Errorf("expected no tool_choice for a native structured output, got %+v", toolChoice)
	}
}

func TestToAnthropicChatRequest_Vertex_NativeStructuredOutput(t *testing.T) {
	for _, tc := range vertexSOThinkingCases() {
		t.Run(tc.name, func(t *testing.T) {
			rf := makeSOResponseFormat("my_schema")
			bifrostReq := &schemas.BifrostChatRequest{
				Provider: schemas.Vertex,
				Model:    "claude-sonnet-5",
				Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: schemas.Ptr(16000),
					ResponseFormat:      &rf,
					Reasoning:           tc.reasoning,
				},
			}

			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()
			result, err := ToAnthropicChatRequest(ctx, bifrostReq)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertVertexNativeStructuredOutput(t, result.OutputConfig, result.Tools, result.ToolChoice)
			if tc.reasoning != nil && result.Thinking == nil {
				t.Errorf("expected thinking to stay on alongside output_config.format")
			}
		})
	}
}

func TestToAnthropicResponsesRequest_Vertex_NativeStructuredOutput(t *testing.T) {
	for _, tc := range vertexSOThinkingCases() {
		t.Run(tc.name, func(t *testing.T) {
			params := &schemas.ResponsesParameters{
				MaxOutputTokens: schemas.Ptr(16000),
				Text:            makeResponsesTextFormat("my_schema"),
			}
			if tc.reasoning != nil {
				params.Reasoning = &schemas.ResponsesParametersReasoning{
					Effort:    tc.reasoning.Effort,
					MaxTokens: tc.reasoning.MaxTokens,
				}
			}
			req := &schemas.BifrostResponsesRequest{
				Provider: schemas.Vertex,
				Model:    "claude-sonnet-5",
				Input: []schemas.ResponsesMessage{
					{
						Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Hello")},
					},
				},
				Params: params,
			}

			ctx := schemas.NewBifrostContext(nil, time.Time{})
			result, err := ToAnthropicResponsesRequest(ctx, req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertVertexNativeStructuredOutput(t, result.OutputConfig, result.Tools, result.ToolChoice)
			if tc.reasoning != nil && result.Thinking == nil {
				t.Errorf("expected thinking to stay on alongside output_config.format")
			}
		})
	}
}

func TestProviderRequiresSyntheticStructuredOutput_VertexIsNative(t *testing.T) {
	if ProviderRequiresSyntheticStructuredOutput(schemas.Vertex) {
		t.Error("vertex serves output_config.format natively and must not use the synthetic tool")
	}
	if !ProviderFeatures[schemas.Vertex].StructuredOutputs {
		t.Error("vertex must keep strict tools and the structured-outputs beta header")
	}
	for _, provider := range toolConversionProviders {
		if !ProviderRequiresSyntheticStructuredOutput(provider) {
			t.Errorf("%s still requires the synthetic structured-output tool", provider)
		}
	}
}
