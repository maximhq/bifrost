package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestToOpenAIChatRequest_ToolNormalization(t *testing.T) {
	// Create tool parameters with keys in non-alphabetical order:
	// "required" before "properties" before "type" — Normalized() should reorder to
	// type → description → properties → required, then alphabetical.
	unsortedParams := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("zebra", map[string]interface{}{"type": "string"}),
			schemas.KV("alpha", map[string]interface{}{"type": "number"}),
		),
		Required: []string{"zebra"},
	}

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{
				{
					Type: "function",
					Function: &schemas.ChatToolFunction{
						Name:       "test_func",
						Parameters: unsortedParams,
					},
				},
				{
					Type:     "function",
					Function: &schemas.ChatToolFunction{Name: "no_params_func"},
				},
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result := ToOpenAIChatRequest(ctx, bifrostReq)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Verify parameters are normalized: Properties keys should preserve original order
	// (user-defined property names are kept in client order for LLM generation quality)
	normalizedParams := result.ChatParameters.Tools[0].Function.Parameters
	if normalizedParams == nil {
		t.Fatal("expected normalized parameters to be non-nil")
	}
	keys := normalizedParams.Properties.Keys()
	if len(keys) != 2 || keys[0] != "zebra" || keys[1] != "alpha" {
		t.Errorf("expected Properties keys preserved as [zebra, alpha], got %v", keys)
	}

	// Verify tool without parameters is unaffected
	if result.ChatParameters.Tools[1].Function.Parameters != nil {
		t.Error("expected nil parameters for tool without parameters")
	}

	// Verify original bifrostReq.Params.Tools was NOT mutated
	origKeys := bifrostReq.Params.Tools[0].Function.Parameters.Properties.Keys()
	if len(origKeys) != 2 || origKeys[0] != "zebra" || origKeys[1] != "alpha" {
		t.Errorf("original parameters were mutated: expected [zebra, alpha], got %v", origKeys)
	}

	// Verify the Function pointer is a different object (deep copy)
	if result.ChatParameters.Tools[0].Function == bifrostReq.Params.Tools[0].Function {
		t.Error("expected Function pointer to be a copy, not the original")
	}
}

func TestToOpenAIChatRequest_PreservesN(t *testing.T) {
	n := 2
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4.1",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("hello"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			N: &n,
		},
	}

	out := ToOpenAIChatRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline), req)
	if out == nil {
		t.Fatal("expected request")
	}
	if out.N == nil || *out.N != 2 {
		t.Fatalf("expected n=2, got %#v", out.N)
	}
}

func TestToOpenAIChatRequest_PreservesPropertyOrder(t *testing.T) {
	params := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("reasoning", map[string]interface{}{"type": "string", "description": "Step by step"}),
			schemas.KV("answer", map[string]interface{}{"type": "string", "description": "Final answer"}),
			schemas.KV("confidence", map[string]interface{}{"type": "number", "description": "Score"}),
		),
		Required: []string{"reasoning", "answer"},
	}

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{
				Type:     "function",
				Function: &schemas.ChatToolFunction{Name: "test_func", Parameters: params},
			}},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result := ToOpenAIChatRequest(ctx, bifrostReq)

	// CoT: property order preserved
	normalizedParams := result.ChatParameters.Tools[0].Function.Parameters
	keys := normalizedParams.Properties.Keys()
	if len(keys) != 3 || keys[0] != "reasoning" || keys[1] != "answer" || keys[2] != "confidence" {
		t.Errorf("expected property order [reasoning, answer, confidence], got %v", keys)
	}
}

func TestToOpenAIChatRequest_CachingDeterminism(t *testing.T) {
	// Same properties, different structural key orders within property definitions
	makeReq := func(props *schemas.OrderedMap) *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
			Params: &schemas.ChatParameters{
				Tools: []schemas.ChatTool{{
					Type: "function",
					Function: &schemas.ChatToolFunction{
						Name:       "test",
						Parameters: &schemas.ToolFunctionParameters{Type: "object", Properties: props},
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

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	resultA := ToOpenAIChatRequest(ctx, makeReq(propsA))
	resultB := ToOpenAIChatRequest(ctx, makeReq(propsB))

	jsonA, err := schemas.Marshal(resultA.ChatParameters.Tools[0].Function.Parameters)
	if err != nil {
		t.Fatalf("failed to marshal params A: %v", err)
	}
	jsonB, err := schemas.Marshal(resultB.ChatParameters.Tools[0].Function.Parameters)
	if err != nil {
		t.Fatalf("failed to marshal params B: %v", err)
	}

	if string(jsonA) != string(jsonB) {
		t.Errorf("caching broken: same schema produced different JSON\nA: %s\nB: %s", jsonA, jsonB)
	}
}

func TestApplyXAICompatibility(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		request  *OpenAIChatRequest
		validate func(t *testing.T, req *OpenAIChatRequest)
	}{
		{
			name:  "grok-3: preserves frequency_penalty and stop, clears presence_penalty and reasoning_effort",
			model: "grok-3",
			request: &OpenAIChatRequest{
				Model:    "grok-3",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("high"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// frequency_penalty should be preserved
				if req.FrequencyPenalty == nil || *req.FrequencyPenalty != 0.5 {
					t.Errorf("Expected FrequencyPenalty to be preserved at 0.5, got %v", req.FrequencyPenalty)
				}

				// stop should be preserved
				if len(req.Stop) != 1 || req.Stop[0] != "STOP" {
					t.Errorf("Expected Stop to be preserved as ['STOP'], got %v", req.Stop)
				}

				// presence_penalty should be cleared
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				// reasoning_effort should be cleared for non-mini grok-3
				if req.Reasoning == nil {
					t.Fatal("Expected Reasoning to remain non-nil")
				}
				if req.Reasoning.Effort != nil {
					t.Errorf("Expected Reasoning.Effort to be cleared (nil) for grok-3, got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-3-mini: clears all penalties and stop, preserves reasoning_effort",
			model: "grok-3-mini",
			request: &OpenAIChatRequest{
				Model:    "grok-3-mini",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("medium"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// presence_penalty should be cleared
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				// frequency_penalty should be cleared for grok-3-mini
				if req.FrequencyPenalty != nil {
					t.Errorf("Expected FrequencyPenalty to be cleared (nil) for grok-3-mini, got %v", *req.FrequencyPenalty)
				}

				// stop should be cleared for grok-3-mini
				if req.Stop != nil {
					t.Errorf("Expected Stop to be cleared (nil) for grok-3-mini, got %v", req.Stop)
				}

				// reasoning_effort should be preserved for grok-3-mini
				if req.Reasoning == nil || req.Reasoning.Effort == nil {
					t.Fatal("Expected Reasoning.Effort to be preserved for grok-3-mini")
				}
				if *req.Reasoning.Effort != "medium" {
					t.Errorf("Expected Reasoning.Effort to be 'medium', got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-4: clears all penalties, stop, and reasoning_effort",
			model: "grok-4",
			request: &OpenAIChatRequest{
				Model:    "grok-4",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("high"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// presence_penalty should be cleared
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				// frequency_penalty should be cleared for grok-4
				if req.FrequencyPenalty != nil {
					t.Errorf("Expected FrequencyPenalty to be cleared (nil) for grok-4, got %v", *req.FrequencyPenalty)
				}

				// stop should be cleared for grok-4
				if req.Stop != nil {
					t.Errorf("Expected Stop to be cleared (nil) for grok-4, got %v", req.Stop)
				}

				// reasoning_effort should be cleared for grok-4
				if req.Reasoning == nil {
					t.Fatal("Expected Reasoning to remain non-nil")
				}
				if req.Reasoning.Effort != nil {
					t.Errorf("Expected Reasoning.Effort to be cleared (nil) for grok-4, got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-4-fast-reasoning: clears all penalties, stop, and reasoning_effort",
			model: "grok-4-fast-reasoning",
			request: &OpenAIChatRequest{
				Model:    "grok-4-fast-reasoning",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP", "END"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("high"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// presence_penalty should be cleared
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				// frequency_penalty should be cleared
				if req.FrequencyPenalty != nil {
					t.Errorf("Expected FrequencyPenalty to be cleared (nil), got %v", *req.FrequencyPenalty)
				}

				// stop should be cleared
				if req.Stop != nil {
					t.Errorf("Expected Stop to be cleared (nil), got %v", req.Stop)
				}

				// reasoning_effort should be cleared
				if req.Reasoning == nil {
					t.Fatal("Expected Reasoning to remain non-nil")
				}
				if req.Reasoning.Effort != nil {
					t.Errorf("Expected Reasoning.Effort to be cleared (nil), got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-code-fast-1: clears all penalties, stop, and reasoning_effort",
			model: "grok-code-fast-1",
			request: &OpenAIChatRequest{
				Model:    "grok-code-fast-1",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.2),
					PresencePenalty:  schemas.Ptr(0.1),
					Stop:             []string{"END"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("low"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// presence_penalty should be cleared
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				// frequency_penalty should be cleared
				if req.FrequencyPenalty != nil {
					t.Errorf("Expected FrequencyPenalty to be cleared (nil), got %v", *req.FrequencyPenalty)
				}

				// stop should be cleared
				if req.Stop != nil {
					t.Errorf("Expected Stop to be cleared (nil), got %v", req.Stop)
				}

				// reasoning_effort should be cleared
				if req.Reasoning == nil {
					t.Fatal("Expected Reasoning to remain non-nil")
				}
				if req.Reasoning.Effort != nil {
					t.Errorf("Expected Reasoning.Effort to be cleared (nil), got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "non-reasoning grok model: no changes applied",
			model: "grok-2-latest",
			request: &OpenAIChatRequest{
				Model:    "grok-2-latest",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("high"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// All parameters should be preserved for non-reasoning models
				if req.FrequencyPenalty == nil || *req.FrequencyPenalty != 0.5 {
					t.Errorf("Expected FrequencyPenalty to be preserved at 0.5, got %v", req.FrequencyPenalty)
				}

				if req.PresencePenalty == nil || *req.PresencePenalty != 0.3 {
					t.Errorf("Expected PresencePenalty to be preserved at 0.3, got %v", req.PresencePenalty)
				}

				if len(req.Stop) != 1 || req.Stop[0] != "STOP" {
					t.Errorf("Expected Stop to be preserved as ['STOP'], got %v", req.Stop)
				}

				if req.Reasoning == nil || req.Reasoning.Effort == nil {
					t.Fatal("Expected Reasoning.Effort to be preserved")
				}
				if *req.Reasoning.Effort != "high" {
					t.Errorf("Expected Reasoning.Effort to be 'high', got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-3: handles nil reasoning gracefully",
			model: "grok-3",
			request: &OpenAIChatRequest{
				Model:    "grok-3",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP"},
					Reasoning:        nil,
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// Should handle nil reasoning without panicking
				if req.Reasoning != nil {
					t.Errorf("Expected Reasoning to remain nil, got %v", req.Reasoning)
				}

				// Other parameters should still be processed
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				if req.FrequencyPenalty == nil || *req.FrequencyPenalty != 0.5 {
					t.Errorf("Expected FrequencyPenalty to be preserved at 0.5, got %v", req.FrequencyPenalty)
				}
			},
		},
		{
			name:  "grok-3: preserves other parameters like temperature",
			model: "grok-3",
			request: &OpenAIChatRequest{
				Model:    "grok-3",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					Temperature:      schemas.Ptr(0.8),
					TopP:             schemas.Ptr(0.9),
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// Unrelated parameters should be preserved
				if req.Temperature == nil || *req.Temperature != 0.8 {
					t.Errorf("Expected Temperature to be preserved at 0.8, got %v", req.Temperature)
				}

				if req.TopP == nil || *req.TopP != 0.9 {
					t.Errorf("Expected TopP to be preserved at 0.9, got %v", req.TopP)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Apply the compatibility function
			tt.request.applyXAICompatibility(tt.model)

			// Validate the results
			tt.validate(t, tt.request)
		})
	}
}
