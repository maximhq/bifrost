package azure_test

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestAzure_OpenAIModel_CachingDeterminism verifies that Azure's delegation to
// openai.ToOpenAIChatRequest() produces deterministic JSON for prompt caching.
// Two schemas with the same properties but different structural key order within
// property definitions must produce byte-identical JSON after normalization.
func TestAzure_OpenAIModel_CachingDeterminism(t *testing.T) {
	makeReq := func(props *schemas.OrderedMap) *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: schemas.Azure,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
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
		schemas.KV("reasoning", schemas.NewOrderedMapFromPairs(
			schemas.KV("type", "string"),
			schemas.KV("description", "Step by step"),
		)),
		schemas.KV("answer", schemas.NewOrderedMapFromPairs(
			schemas.KV("type", "string"),
			schemas.KV("description", "Final answer"),
		)),
	)

	// Version B: description before type (different structural order)
	propsB := schemas.NewOrderedMapFromPairs(
		schemas.KV("reasoning", schemas.NewOrderedMapFromPairs(
			schemas.KV("description", "Step by step"),
			schemas.KV("type", "string"),
		)),
		schemas.KV("answer", schemas.NewOrderedMapFromPairs(
			schemas.KV("description", "Final answer"),
			schemas.KV("type", "string"),
		)),
	)

	// Azure delegates OpenAI models to openai.ToOpenAIChatRequest()
	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	resultA := openai.ToOpenAIChatRequest(ctx, makeReq(propsA))
	resultB := openai.ToOpenAIChatRequest(ctx, makeReq(propsB))

	jsonA, err := schemas.Marshal(resultA.ChatParameters.Tools[0].Function.Parameters)
	if err != nil {
		t.Fatalf("failed to marshal params A: %v", err)
	}
	jsonB, err := schemas.Marshal(resultB.ChatParameters.Tools[0].Function.Parameters)
	if err != nil {
		t.Fatalf("failed to marshal params B: %v", err)
	}

	// Caching: byte-identical JSON
	if string(jsonA) != string(jsonB) {
		t.Errorf("caching broken via Azure→OpenAI path: same schema produced different JSON\nA: %s\nB: %s", jsonA, jsonB)
	}

	// CoT: property order preserved
	keys := resultA.ChatParameters.Tools[0].Function.Parameters.Properties.Keys()
	if len(keys) != 2 || keys[0] != "reasoning" || keys[1] != "answer" {
		t.Errorf("expected property order [reasoning, answer], got %v", keys)
	}
}

// TestAzure_OpenAIModel_PreservesPropertyOrder verifies that the Azure→OpenAI
// delegation path preserves user-defined property ordering.
func TestAzure_OpenAIModel_PreservesPropertyOrder(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Azure,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name: "AnswerResponseModel",
					Parameters: &schemas.ToolFunctionParameters{
						Type: "object",
						Properties: schemas.NewOrderedMapFromPairs(
							schemas.KV("chain_of_thought", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
							schemas.KV("answer", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
							schemas.KV("citations", schemas.NewOrderedMapFromPairs(schemas.KV("type", "array"))),
						),
					},
				},
			}},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result := openai.ToOpenAIChatRequest(ctx, bifrostReq)

	keys := result.ChatParameters.Tools[0].Function.Parameters.Properties.Keys()
	if len(keys) != 3 || keys[0] != "chain_of_thought" || keys[1] != "answer" || keys[2] != "citations" {
		t.Errorf("expected property order [chain_of_thought, answer, citations], got %v", keys)
	}
}

// TestAzure_ToolInputKeyOrderPreservation verifies that tool call arguments
// preserve their original key ordering through the Azure→OpenAI delegation path.
// TestAzure_ToolInputKeyOrderPreservation verifies that Azure→OpenAI delegation
// preserves the original key ordering of tool call arguments for prompt caching.
// Tests multiple parallel tool calls with different key orderings per block.
func TestAzure_ToolInputKeyOrderPreservation(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Azure,
		Model:    "gpt-4o",
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
							ID:    schemas.Ptr("toolu_001"),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      schemas.Ptr("bash"),
								Arguments: `{"description":"Find references quickly","timeout":30000,"command":"grep -r auth_injector ."}`,
							},
						},
						{
							Index: 1,
							Type:  schemas.Ptr("function"),
							ID:    schemas.Ptr("toolu_002"),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      schemas.Ptr("bash"),
								Arguments: `{"command":"git diff main...HEAD --stat","description":"Show diff"}`,
							},
						},
					},
				},
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result := openai.ToOpenAIChatRequest(ctx, bifrostReq)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Collect tool call arguments from assistant message
	var argsList []string
	for _, msg := range result.Messages {
		if msg.OpenAIChatAssistantMessage != nil {
			for _, tc := range msg.OpenAIChatAssistantMessage.ToolCalls {
				argsList = append(argsList, tc.Function.Arguments)
			}
		}
	}

	if len(argsList) != 2 {
		t.Fatalf("expected 2 tool call arguments, got %d", len(argsList))
	}

	// OpenAI path passes Arguments through as strings — verify key order is preserved
	// Block 0: keys should be description, timeout, command
	s0 := argsList[0]
	if !(strings.Index(s0, "description") < strings.Index(s0, "timeout") &&
		strings.Index(s0, "timeout") < strings.Index(s0, "command")) {
		t.Errorf("block 0: key order not preserved, expected description < timeout < command in: %s", s0)
	}

	// Block 1: keys should be command, description
	s1 := argsList[1]
	if !(strings.Index(s1, "command") < strings.Index(s1, "description")) {
		t.Errorf("block 1: key order not preserved, expected command < description in: %s", s1)
	}
}
