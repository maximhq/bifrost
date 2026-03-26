package anthropic

import (
	"encoding/json"
	"strings"
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

// TestToAnthropicChatRequest_ToolInputKeyOrderPreservation verifies that tool_use input
// arguments preserve the client's original key ordering after conversion to Anthropic format.
// This is critical for prompt caching, which relies on exact byte-for-byte prefix matching.
// The test uses multiple parallel tool calls in a single assistant message — each with
// a different key ordering — matching real-world Claude Code usage patterns.
func TestToAnthropicChatRequest_ToolInputKeyOrderPreservation(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4-20250514",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("test")},
			},
			{
				// Multiple parallel tool calls with different key orderings per block
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{
						{
							Index: 0,
							Type:  schemas.Ptr("function"),
							ID:    schemas.Ptr("toolu_vrtx_013t7gabfKz98BKpdwrnS6LP"),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      schemas.Ptr("bash"),
								Arguments: `{"description":"Find references to auth_injector quickly","timeout":30000,"command":"grep -r \"auth_injector\" . --include=\"Makefile\" -l 2>/dev/null"}`,
							},
						},
						{
							Index: 1,
							Type:  schemas.Ptr("function"),
							ID:    schemas.Ptr("toolu_vrtx_01K2kr3wi7M4RriLgE7Kq3vJ"),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      schemas.Ptr("bash"),
								Arguments: `{"command":"git diff main...HEAD --stat","description":"Show diff of commits in branch"}`,
							},
						},
						{
							Index: 2,
							Type:  schemas.Ptr("function"),
							ID:    schemas.Ptr("toolu_vrtx_01D1mMkcvpfqGrEhkcxUQpGc"),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      schemas.Ptr("bash"),
								Arguments: `{"command":"git log main..HEAD --format=\"%H %s\" | head -20","description":"Show detailed commits in branch"}`,
							},
						},
					},
				},
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Collect all tool_use content blocks
	var toolUseBlocks []AnthropicContentBlock
	for _, msg := range result.Messages {
		for _, block := range msg.Content.ContentBlocks {
			if block.Type == AnthropicContentBlockTypeToolUse {
				toolUseBlocks = append(toolUseBlocks, block)
			}
		}
	}

	if len(toolUseBlocks) != 3 {
		t.Fatalf("expected 3 tool_use blocks, got %d", len(toolUseBlocks))
	}

	// Block 0: keys should be description, timeout, command (NOT alphabetical)
	json0, _ := json.Marshal(toolUseBlocks[0].Input)
	s0 := string(json0)
	descIdx0 := strings.Index(s0, `"description"`)
	timeIdx0 := strings.Index(s0, `"timeout"`)
	cmdIdx0 := strings.Index(s0, `"command"`)
	if descIdx0 < 0 || timeIdx0 < 0 || cmdIdx0 < 0 {
		t.Fatalf("block 0: missing expected key(s) in: %s", s0)
	}
	if !(descIdx0 < timeIdx0 && timeIdx0 < cmdIdx0) {
		t.Errorf("block 0: key order not preserved, expected description < timeout < command in: %s", s0)
	}

	// Block 1: keys should be command, description (NOT alphabetical)
	json1, _ := json.Marshal(toolUseBlocks[1].Input)
	s1 := string(json1)
	cmdIdx1 := strings.Index(s1, `"command"`)
	descIdx1 := strings.Index(s1, `"description"`)
	if cmdIdx1 < 0 || descIdx1 < 0 {
		t.Fatalf("block 1: missing expected key(s) in: %s", s1)
	}
	if !(cmdIdx1 < descIdx1) {
		t.Errorf("block 1: key order not preserved, expected command < description in: %s", s1)
	}

	// Block 2: keys should be command, description (same as block 1)
	json2, _ := json.Marshal(toolUseBlocks[2].Input)
	s2 := string(json2)
	cmdIdx2 := strings.Index(s2, `"command"`)
	descIdx2 := strings.Index(s2, `"description"`)
	if cmdIdx2 < 0 || descIdx2 < 0 {
		t.Fatalf("block 2: missing expected key(s) in: %s", s2)
	}
	if !(cmdIdx2 < descIdx2) {
		t.Errorf("block 2: key order not preserved, expected command < description in: %s", s2)
	}
}

func TestToBifrostChatResponse_MultipleTextBlocksWithThinking(t *testing.T) {
	thinkingText := "Let me reason step by step about this problem."
	textBlock1 := "The answer is 42."
	textBlock2 := "Here is why that is the case."
	signature := "sig_abc123"

	response := &AnthropicMessageResponse{
		ID:    "msg_test123",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-opus-4-6-20250514",
		Content: []AnthropicContentBlock{
			{
				Type:      AnthropicContentBlockTypeThinking,
				Thinking:  &thinkingText,
				Signature: &signature,
			},
			{
				Type: AnthropicContentBlockTypeText,
				Text: &textBlock1,
			},
			{
				Type: AnthropicContentBlockTypeText,
				Text: &textBlock2,
			},
		},
		StopReason: "end_turn",
		Usage: &AnthropicUsage{
			InputTokens:  100,
			OutputTokens: 50,
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result := response.ToBifrostChatResponse(ctx)

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Content should be a combined string, not blocks
	choice := result.Choices[0]
	msg := choice.ChatNonStreamResponseChoice.Message
	if msg.Content.ContentBlocks != nil {
		t.Error("expected ContentBlocks to be nil (combined into string)")
	}
	if msg.Content.ContentStr == nil {
		t.Fatal("expected ContentStr to be non-nil")
	}

	// Combined string: thinking first, then text blocks
	expected := thinkingText + "\n\n" + textBlock1 + "\n\n" + textBlock2
	if *msg.Content.ContentStr != expected {
		t.Errorf("expected combined content:\n%s\ngot:\n%s", expected, *msg.Content.ContentStr)
	}

	// Reasoning field should still have thinking text
	if msg.ChatAssistantMessage == nil {
		t.Fatal("expected ChatAssistantMessage to be non-nil")
	}
	if msg.ChatAssistantMessage.Reasoning == nil {
		t.Fatal("expected Reasoning to be non-nil")
	}

	// ReasoningDetails should have: signature-only thinking entry + content blocks boundary
	rd := msg.ChatAssistantMessage.ReasoningDetails
	if len(rd) < 2 {
		t.Fatalf("expected at least 2 reasoning details entries, got %d", len(rd))
	}

	// First entry: thinking with signature, no text (text was cleared)
	if rd[0].Type != schemas.BifrostReasoningDetailsTypeText {
		t.Errorf("expected first reasoning detail type %s, got %s", schemas.BifrostReasoningDetailsTypeText, rd[0].Type)
	}
	if rd[0].Signature == nil || *rd[0].Signature != signature {
		t.Error("expected signature to be preserved")
	}
	if rd[0].Text != nil {
		t.Error("expected thinking text to be nil (cleared to avoid duplication)")
	}

	// Last entry: content blocks boundary
	lastRD := rd[len(rd)-1]
	if lastRD.Type != schemas.BifrostReasoningDetailsTypeContentBlocks {
		t.Errorf("expected last reasoning detail type %s, got %s", schemas.BifrostReasoningDetailsTypeContentBlocks, lastRD.Type)
	}
	if lastRD.Text == nil {
		t.Fatal("expected content blocks metadata to be non-nil")
	}
}

func TestToBifrostChatResponse_SingleTextBlockNoThinking(t *testing.T) {
	// Verify existing behavior: single text block without thinking collapses to string
	text := "Simple response"
	response := &AnthropicMessageResponse{
		ID:    "msg_simple",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-sonnet-4-6-20250514",
		Content: []AnthropicContentBlock{
			{Type: AnthropicContentBlockTypeText, Text: &text},
		},
		StopReason: "end_turn",
		Usage:      &AnthropicUsage{InputTokens: 10, OutputTokens: 5},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result := response.ToBifrostChatResponse(ctx)

	msg := result.Choices[0].ChatNonStreamResponseChoice.Message
	if msg.Content.ContentStr == nil || *msg.Content.ContentStr != text {
		t.Error("expected ContentStr to be the text")
	}
	if msg.Content.ContentBlocks != nil {
		t.Error("expected ContentBlocks to be nil")
	}
	// No reasoning details for plain text
	if msg.ChatAssistantMessage != nil && len(msg.ChatAssistantMessage.ReasoningDetails) > 0 {
		t.Error("expected no reasoning details for single text block without thinking")
	}
}

func TestToAnthropicChatRequest_ReconstructsFromBoundaries(t *testing.T) {
	thinkingText := "Step by step reasoning"
	textBlock1 := "First answer"
	textBlock2 := "Second answer"
	signature := "sig_xyz789"

	// Simulate combined content string
	combined := thinkingText + "\n\n" + textBlock1 + "\n\n" + textBlock2

	// Build block metadata

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-6-20250514",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Question")},
			},
			{
				Role:    schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentStr: &combined},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ReasoningDetails: []schemas.ChatReasoningDetails{
						{
							Index:     0,
							Type:      schemas.BifrostReasoningDetailsTypeText,
							Signature: &signature,
							// Text is nil — was cleared during response conversion
						},
					},
				},
			},
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Follow up")},
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The assistant message (index 1, which maps to anthropic messages[1] since user is [0])
	// Find the assistant message
	var assistantMsg *AnthropicMessage
	for i := range result.Messages {
		if result.Messages[i].Role == "assistant" {
			assistantMsg = &result.Messages[i]
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("expected assistant message in result")
	}

	blocks := assistantMsg.Content.ContentBlocks
	if len(blocks) != 3 {
		t.Fatalf("expected 3 content blocks, got %d", len(blocks))
	}

	// Block 0: thinking with signature and text reconstructed from combined string
	if blocks[0].Type != AnthropicContentBlockTypeThinking {
		t.Errorf("block 0: expected thinking, got %s", blocks[0].Type)
	}
	if blocks[0].Thinking == nil || *blocks[0].Thinking != thinkingText {
		t.Errorf("block 0: expected thinking text %q, got %v", thinkingText, blocks[0].Thinking)
	}
	if blocks[0].Signature == nil || *blocks[0].Signature != signature {
		t.Errorf("block 0: expected signature %q, got %v", signature, blocks[0].Signature)
	}

	// Block 1: text block 1
	if blocks[1].Type != AnthropicContentBlockTypeText {
		t.Errorf("block 1: expected text, got %s", blocks[1].Type)
	}
	if blocks[1].Text == nil || *blocks[1].Text != textBlock1 {
		t.Errorf("block 1: expected text %q, got %v", textBlock1, blocks[1].Text)
	}

	// Block 2: text block 2
	if blocks[2].Type != AnthropicContentBlockTypeText {
		t.Errorf("block 2: expected text, got %s", blocks[2].Type)
	}
	if blocks[2].Text == nil || *blocks[2].Text != textBlock2 {
		t.Errorf("block 2: expected text %q, got %v", textBlock2, blocks[2].Text)
	}
}

func TestToAnthropicChatRequest_BoundaryMismatchFallback(t *testing.T) {
	// If content was modified by the client, boundaries won't match — fall back to single text block
	signature := "sig_fallback"
	modifiedContent := "The user edited this content"

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-6-20250514",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hi")},
			},
			{
				Role:    schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentStr: &modifiedContent},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ReasoningDetails: []schemas.ChatReasoningDetails{
						{Index: 0, Type: schemas.BifrostReasoningDetailsTypeText, Text: &modifiedContent, Signature: &signature},
					},
				},
			},
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Continue")},
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var assistantMsg *AnthropicMessage
	for i := range result.Messages {
		if result.Messages[i].Role == "assistant" {
			assistantMsg = &result.Messages[i]
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("expected assistant message")
	}

	// Should have thinking block (from reasoning_details with signature) + single text fallback
	blocks := assistantMsg.Content.ContentBlocks
	// First block: thinking (from reasoning_details, text is nil since it was cleared)
	// Plus: fallback single text block with the full modified content
	foundText := false
	for _, block := range blocks {
		if block.Type == AnthropicContentBlockTypeText {
			if block.Text != nil && *block.Text == modifiedContent {
				foundText = true
			}
		}
	}
	if !foundText {
		t.Error("expected fallback to single text block with full content")
	}
}

func TestToAnthropicChatRequest_NormalFlowUnchanged(t *testing.T) {
	// Verify that the normal multi-turn flow (reasoning_details with text + signature,
	// no bifrost.content_blocks) produces the same output as before.
	thinkingText := "I need to think about this carefully"
	signature := "sig_normal"
	responseText := "Here is my answer"

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-6-20250514",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("What is 2+2?")},
			},
			{
				Role:    schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentStr: &responseText},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ReasoningDetails: []schemas.ChatReasoningDetails{
						{
							Index:     0,
							Type:      schemas.BifrostReasoningDetailsTypeText,
							Text:      &thinkingText,
							Signature: &signature,
						},
					},
				},
			},
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Are you sure?")},
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var assistantMsg *AnthropicMessage
	for i := range result.Messages {
		if result.Messages[i].Role == "assistant" {
			assistantMsg = &result.Messages[i]
			break
		}
	}
	if assistantMsg == nil {
		t.Fatal("expected assistant message")
	}

	blocks := assistantMsg.Content.ContentBlocks
	if len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks (thinking + text), got %d", len(blocks))
	}

	// Block 0: thinking with original text and signature
	if blocks[0].Type != AnthropicContentBlockTypeThinking {
		t.Errorf("block 0: expected thinking, got %s", blocks[0].Type)
	}
	if blocks[0].Thinking == nil || *blocks[0].Thinking != thinkingText {
		t.Errorf("block 0: expected thinking text %q, got %v", thinkingText, blocks[0].Thinking)
	}
	if blocks[0].Signature == nil || *blocks[0].Signature != signature {
		t.Errorf("block 0: expected signature %q, got %v", signature, blocks[0].Signature)
	}

	// Block 1: text with response
	if blocks[1].Type != AnthropicContentBlockTypeText {
		t.Errorf("block 1: expected text, got %s", blocks[1].Type)
	}
	if blocks[1].Text == nil || *blocks[1].Text != responseText {
		t.Errorf("block 1: expected text %q, got %v", responseText, blocks[1].Text)
	}
}
