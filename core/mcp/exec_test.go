package mcp

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestSetResolvedToolDefinition(t *testing.T) {
	name := "weather"
	description := "Get weather"
	request := &schemas.BifrostMCPRequest{
		ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{
			Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name},
		},
	}
	state := &schemas.MCPClientState{
		ToolMap: map[string]schemas.ChatTool{
			name: {
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        name,
					Description: &description,
				},
			},
		},
	}

	setResolvedToolDefinition(request, state)

	if request.ToolDefinition == nil || request.ToolDefinition.Function == nil {
		t.Fatal("resolved tool definition was not populated")
	}
	*request.ToolDefinition.Function.Description = "mutated"
	if got := *state.ToolMap[name].Function.Description; got != description {
		t.Fatalf("resolved tool definition shares description pointer: got %q, want %q", got, description)
	}

	resetMCPRequest(request)
	if request.ToolDefinition != nil {
		t.Fatal("resetMCPRequest did not clear tool definition")
	}
}
