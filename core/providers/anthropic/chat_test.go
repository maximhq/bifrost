package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestToAnthropicChatRequest_PreservesPropertyOrder(t *testing.T) {
	params := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("chain_of_thought", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "string"),
				schemas.KV("description", "Reasoning steps"),
			)),
			schemas.KV("answer", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "string"),
				schemas.KV("description", "The answer"),
			)),
			schemas.KV("citations", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "array"),
			)),
			schemas.KV("is_unanswered", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "boolean"),
			)),
		),
		Required: []string{"answer", "is_unanswered"},
	}

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
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
					Parameters:  params,
				},
			}},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Tools) == 0 {
		t.Fatal("expected at least one tool")
	}

	inputSchema := result.Tools[0].InputSchema
	if inputSchema == nil {
		t.Fatal("expected InputSchema to be non-nil")
	}

	// CoT: property order preserved
	keys := inputSchema.Properties.Keys()
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

func TestToAnthropicChatRequest_CachingDeterminism(t *testing.T) {
	makeReq := func(props *schemas.OrderedMap) *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: schemas.Anthropic,
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
		schemas.KV("reasoning", schemas.NewOrderedMapFromPairs(
			schemas.KV("type", "string"),
			schemas.KV("description", "Step by step"),
		)),
		schemas.KV("answer", schemas.NewOrderedMapFromPairs(
			schemas.KV("type", "string"),
			schemas.KV("description", "Final answer"),
		)),
	)

	// Version B: description before type
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

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	resultA, err := ToAnthropicChatRequest(ctx, makeReq(propsA))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resultB, err := ToAnthropicChatRequest(ctx, makeReq(propsB))
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

	if string(jsonA) != string(jsonB) {
		t.Errorf("caching broken: same schema produced different JSON\nA: %s\nB: %s", jsonA, jsonB)
	}
}

func TestToAnthropicChatRequest_NestedProperties_Preserved(t *testing.T) {
	params := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("output", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "object"),
				schemas.KV("properties", schemas.NewOrderedMapFromPairs(
					schemas.KV("verdict", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
					schemas.KV("score", schemas.NewOrderedMapFromPairs(schemas.KV("type", "number"))),
					schemas.KV("explanation", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
				)),
			)),
			schemas.KV("reasoning", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
		),
	}

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4-20250514",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("test")},
		}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:       "nested_tool",
					Parameters: params,
				},
			}},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Tools) == 0 {
		t.Fatal("expected at least one tool")
	}
	inputSchema := result.Tools[0].InputSchema

	// CoT: top-level property order preserved
	keys := inputSchema.Properties.Keys()
	if len(keys) != 2 || keys[0] != "output" || keys[1] != "reasoning" {
		t.Errorf("expected top-level property order [output, reasoning], got %v", keys)
	}

	// CoT: nested property order preserved
	output, ok := inputSchema.Properties.Get("output")
	if !ok {
		t.Fatal("expected output property")
	}
	outputOM, ok := output.(*schemas.OrderedMap)
	if !ok {
		t.Fatalf("expected output to be *schemas.OrderedMap, got %T", output)
	}
	nestedProps, ok := outputOM.Get("properties")
	if !ok {
		t.Fatal("expected nested properties in output")
	}
	nestedPropsOM, ok := nestedProps.(*schemas.OrderedMap)
	if !ok {
		t.Fatalf("expected nested properties to be *schemas.OrderedMap, got %T", nestedProps)
	}
	nestedKeys := nestedPropsOM.Keys()
	if len(nestedKeys) != 3 || nestedKeys[0] != "verdict" || nestedKeys[1] != "score" || nestedKeys[2] != "explanation" {
		t.Errorf("expected nested property order [verdict, score, explanation], got %v", nestedKeys)
	}
}
