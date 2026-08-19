package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestToOpenAIResponsesRequest_DropsAdvisorTool verifies the Anthropic-only
// advisor_20260301 server tool is stripped before reaching OpenAI, so a request
// carrying it (e.g. via fallback/routing from an Anthropic-shaped client) does
// not get forwarded as an unknown tool type that OpenAI would reject. The
// function tool alongside it must survive.
func TestToOpenAIResponsesRequest_DropsAdvisorTool(t *testing.T) {
	model := "claude-opus-4-8"
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("hello"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{
					Type:                 schemas.ResponsesToolTypeAdvisor,
					Name:                 schemas.Ptr("advisor"),
					ResponsesToolAdvisor: &schemas.ResponsesToolAdvisor{Model: model},
				},
				{
					Type: schemas.ResponsesToolTypeFunction,
					Name: schemas.Ptr("test_func"),
					ResponsesToolFunction: &schemas.ResponsesToolFunction{
						Parameters: &schemas.ToolFunctionParameters{Type: "object"},
					},
				},
			},
		},
	}

	result := ToOpenAIResponsesRequest(nil, bifrostReq)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	for _, tool := range result.Tools {
		if tool.Type == schemas.ResponsesToolTypeAdvisor {
			t.Fatalf("advisor tool should have been stripped before OpenAI, got %+v", tool)
		}
	}
	if len(result.Tools) != 1 || result.Tools[0].Type != schemas.ResponsesToolTypeFunction {
		t.Fatalf("expected only the function tool to survive, got %+v", result.Tools)
	}
}
