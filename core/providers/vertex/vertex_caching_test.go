package vertex_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestVertex_AnthropicModel_CachingDeterminism verifies that Vertex's delegation
// to anthropic.ToAnthropicChatRequest() produces deterministic JSON for prompt caching.
// Two schemas with the same properties but different structural key order within
// property definitions must produce byte-identical JSON after normalization.
func TestVertex_AnthropicModel_CachingDeterminism(t *testing.T) {
	makeReq := func(props *schemas.OrderedMap) *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: schemas.Vertex,
			Model:    "claude-sonnet-4-20250514",
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("test")},
			}},
			Params: &schemas.ChatParameters{
				Tools: []schemas.ChatTool{{
					Type: schemas.ChatToolTypeFunction,
					Function: &schemas.ChatToolFunction{
						Name: "test",
						Parameters: &schemas.ToolFunctionParameters{
							Type:       "object",
							Properties: props,
						},
					},
				}},
			},
		}
	}

	// Version A: type before description
	propsA := schemas.NewOrderedMapFromPairs(
		schemas.KV("chain_of_thought", schemas.NewOrderedMapFromPairs(
			schemas.KV("type", "string"),
			schemas.KV("description", "Reasoning"),
		)),
		schemas.KV("answer", schemas.NewOrderedMapFromPairs(
			schemas.KV("type", "string"),
			schemas.KV("description", "The answer"),
		)),
	)

	// Version B: description before type (different structural order)
	propsB := schemas.NewOrderedMapFromPairs(
		schemas.KV("chain_of_thought", schemas.NewOrderedMapFromPairs(
			schemas.KV("description", "Reasoning"),
			schemas.KV("type", "string"),
		)),
		schemas.KV("answer", schemas.NewOrderedMapFromPairs(
			schemas.KV("description", "The answer"),
			schemas.KV("type", "string"),
		)),
	)

	// Vertex delegates Anthropic models to anthropic.ToAnthropicChatRequest()
	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	resultA, err := anthropic.ToAnthropicChatRequest(ctx, makeReq(propsA))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resultB, err := anthropic.ToAnthropicChatRequest(ctx, makeReq(propsB))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	jsonA, err := schemas.Marshal(resultA.Tools[0].InputSchema)
	if err != nil {
		t.Fatalf("failed to marshal params A: %v", err)
	}
	jsonB, err := schemas.Marshal(resultB.Tools[0].InputSchema)
	if err != nil {
		t.Fatalf("failed to marshal params B: %v", err)
	}

	// Caching: byte-identical JSON
	if string(jsonA) != string(jsonB) {
		t.Errorf("caching broken via Vertex→Anthropic path: same schema produced different JSON\nA: %s\nB: %s", jsonA, jsonB)
	}

	// CoT: property order preserved
	keys := resultA.Tools[0].InputSchema.Properties.Keys()
	if len(keys) != 2 || keys[0] != "chain_of_thought" || keys[1] != "answer" {
		t.Errorf("expected property order [chain_of_thought, answer], got %v", keys)
	}
}

// TestVertex_AnthropicModel_PreservesPropertyOrder verifies that the
// Vertex→Anthropic delegation path preserves user-defined property ordering.
func TestVertex_AnthropicModel_PreservesPropertyOrder(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Vertex,
		Model:    "claude-sonnet-4-20250514",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("test")},
		}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        "AnswerResponseModel",
					Description: schemas.Ptr("Extract answer"),
					Parameters: &schemas.ToolFunctionParameters{
						Type: "object",
						Properties: schemas.NewOrderedMapFromPairs(
							schemas.KV("chain_of_thought", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
							schemas.KV("answer", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
							schemas.KV("citations", schemas.NewOrderedMapFromPairs(schemas.KV("type", "array"))),
							schemas.KV("is_unanswered", schemas.NewOrderedMapFromPairs(schemas.KV("type", "boolean"))),
						),
						Required: []string{"answer", "is_unanswered"},
					},
				},
			}},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result, err := anthropic.ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	keys := result.Tools[0].InputSchema.Properties.Keys()
	expected := []string{"chain_of_thought", "answer", "citations", "is_unanswered"}
	if len(keys) != len(expected) {
		t.Fatalf("expected %d properties, got %d: %v", len(expected), len(keys), keys)
	}
	for i, k := range expected {
		if keys[i] != k {
			t.Errorf("property %d: expected %q, got %q (full order: %v)", i, k, keys[i], keys)
		}
	}
}

// TestVertex_ToolInputKeyOrderPreservation verifies that tool call arguments
// preserve their original key ordering through the Vertex→Anthropic delegation path.
// TestVertex_ToolInputKeyOrderPreservation verifies that Vertex→Anthropic delegation
// preserves the original key ordering of tool call arguments for prompt caching.
// Tests multiple parallel tool calls with different key orderings per block.
func TestVertex_ToolInputKeyOrderPreservation(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Vertex,
		Model:    "claude-sonnet-4-20250514",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("test")},
			},
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{
						{
							Index: 0,
							Type:  schemas.Ptr("function"),
							ID:    schemas.Ptr("toolu_vrtx_001"),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      schemas.Ptr("bash"),
								Arguments: `{"description":"Find references quickly","timeout":30000,"command":"grep -r auth_injector ."}`,
							},
						},
						{
							Index: 1,
							Type:  schemas.Ptr("function"),
							ID:    schemas.Ptr("toolu_vrtx_002"),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      schemas.Ptr("bash"),
								Arguments: `{"command":"git diff main...HEAD --stat","description":"Show diff"}`,
							},
						},
						{
							Index: 2,
							Type:  schemas.Ptr("function"),
							ID:    schemas.Ptr("toolu_vrtx_003"),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      schemas.Ptr("bash"),
								Arguments: `{"command":"git log main..HEAD","description":"Show commits"}`,
							},
						},
					},
				},
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result, err := anthropic.ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Collect all tool_use content blocks
	var toolUseBlocks []struct{ jsonStr string }
	for _, msg := range result.Messages {
		for _, block := range msg.Content.ContentBlocks {
			if block.Type == "tool_use" {
				jsonBytes, _ := json.Marshal(block.Input)
				toolUseBlocks = append(toolUseBlocks, struct{ jsonStr string }{string(jsonBytes)})
			}
		}
	}

	if len(toolUseBlocks) != 3 {
		t.Fatalf("expected 3 tool_use blocks, got %d", len(toolUseBlocks))
	}

	// Block 0: keys should be description, timeout, command (NOT alphabetical)
	s0 := toolUseBlocks[0].jsonStr
	if !(strings.Index(s0, "description") < strings.Index(s0, "timeout") &&
		strings.Index(s0, "timeout") < strings.Index(s0, "command")) {
		t.Errorf("block 0: key order not preserved, expected description < timeout < command in: %s", s0)
	}

	// Block 1: keys should be command, description (NOT alphabetical)
	s1 := toolUseBlocks[1].jsonStr
	if !(strings.Index(s1, "command") < strings.Index(s1, "description")) {
		t.Errorf("block 1: key order not preserved, expected command < description in: %s", s1)
	}

	// Block 2: keys should be command, description
	s2 := toolUseBlocks[2].jsonStr
	if !(strings.Index(s2, "command") < strings.Index(s2, "description")) {
		t.Errorf("block 2: key order not preserved, expected command < description in: %s", s2)
	}
}
