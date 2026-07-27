package anthropic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
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

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
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

func TestToAnthropicChatRequest_OpenAICompatibleFileIDUsesFileSource(t *testing.T) {
	body := `{
		"model": "anthropic/claude-sonnet-4-5-20250929",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "Read the attached PDF."},
				{
					"type": "file",
					"file": {
						"file_id": "file_abc123",
						"filename": "tiny.pdf",
						"format": "application/pdf"
					}
				}
			]
		}]
	}`

	var openAIReq openai.OpenAIChatRequest
	if err := sonic.Unmarshal([]byte(body), &openAIReq); err != nil {
		t.Fatalf("unmarshal OpenAI-compatible request: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	bifrostReq := openAIReq.ToBifrostChatRequest(ctx)
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("convert to Anthropic request: %v", err)
	}

	if len(result.Messages) != 1 {
		t.Fatalf("expected one message, got %d", len(result.Messages))
	}
	blocks := result.Messages[0].Content.ContentBlocks
	if len(blocks) != 2 {
		t.Fatalf("expected two content blocks, got %d", len(blocks))
	}

	documentBlock := blocks[1]
	if documentBlock.Type != AnthropicContentBlockTypeDocument {
		t.Fatalf("expected document block, got %q", documentBlock.Type)
	}
	if documentBlock.Title == nil || *documentBlock.Title != "tiny.pdf" {
		t.Fatalf("expected document title tiny.pdf, got %v", documentBlock.Title)
	}
	if documentBlock.Source == nil || documentBlock.Source.SourceObj == nil {
		t.Fatalf("expected document source object, got %#v", documentBlock.Source)
	}
	source := documentBlock.Source.SourceObj
	if source.Type != "file" {
		t.Fatalf("expected source type file, got %q", source.Type)
	}
	if source.FileID == nil || *source.FileID != "file_abc123" {
		t.Fatalf("expected source file_id file_abc123, got %v", source.FileID)
	}
}

func TestToAnthropicChatRequest_CachingDeterminism(t *testing.T) {
	makeReq := func(props *schemas.OrderedMap) *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-sonnet-4-20250514",
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: new("test")},
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

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
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

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
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

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
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

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result := response.ToBifrostChatResponse(ctx)

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// With multiple text blocks, ToBifrostChatResponse preserves them as ContentBlocks
	// (only a single text block collapses to ContentStr — see chat.go:812-815).
	// Thinking flows through ReasoningDetails below, not ContentStr.
	choice := result.Choices[0]
	msg := choice.ChatNonStreamResponseChoice.Message
	if msg.Content.ContentStr != nil {
		t.Errorf("expected ContentStr to be nil with multiple text blocks, got %q", *msg.Content.ContentStr)
	}
	if len(msg.Content.ContentBlocks) != 2 {
		t.Fatalf("expected 2 content blocks (one per text block), got %d", len(msg.Content.ContentBlocks))
	}
	if msg.Content.ContentBlocks[0].Text == nil || *msg.Content.ContentBlocks[0].Text != textBlock1 {
		t.Errorf("block 0 text mismatch: got %v, want %q", msg.Content.ContentBlocks[0].Text, textBlock1)
	}
	if msg.Content.ContentBlocks[1].Text == nil || *msg.Content.ContentBlocks[1].Text != textBlock2 {
		t.Errorf("block 1 text mismatch: got %v, want %q", msg.Content.ContentBlocks[1].Text, textBlock2)
	}

	// Thinking is surfaced via ReasoningDetails with the signature preserved
	// (see chat.go:798-807).
	if msg.ChatAssistantMessage == nil {
		t.Fatal("expected ChatAssistantMessage to be non-nil")
	}
	rd := msg.ChatAssistantMessage.ReasoningDetails
	if len(rd) != 1 {
		t.Fatalf("expected 1 reasoning details entry (the thinking block), got %d", len(rd))
	}
	if rd[0].Type != schemas.BifrostReasoningDetailsTypeText {
		t.Errorf("expected reasoning detail type %s, got %s", schemas.BifrostReasoningDetailsTypeText, rd[0].Type)
	}
	if rd[0].Signature == nil || *rd[0].Signature != signature {
		t.Error("expected thinking signature to be preserved on reasoning detail")
	}
	if rd[0].Text == nil || *rd[0].Text != thinkingText {
		t.Errorf("expected reasoning text to match thinking text")
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

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
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

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
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

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
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

func TestToAnthropicChatRequest_Opus47_StripsTemperatureTopPTopK(t *testing.T) {
	temp := 0.7
	topP := 0.9

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-7-20260401",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
		},
		Params: &schemas.ChatParameters{
			Temperature: &temp,
			TopP:        &topP,
			ExtraParams: map[string]interface{}{"top_k": 40},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Temperature != nil {
		t.Errorf("expected Temperature to be nil for Opus 4.7, got %v", result.Temperature)
	}
	if result.TopP != nil {
		t.Errorf("expected TopP to be nil for Opus 4.7, got %v", result.TopP)
	}
	if result.TopK != nil {
		t.Errorf("expected TopK to be nil for Opus 4.7, got %v", result.TopK)
	}
}

func TestToAnthropicChatRequest_NonOpus47_PreservesTemperature(t *testing.T) {
	temp := 0.7

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-6-20250514",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hi")}},
		},
		Params: &schemas.ChatParameters{
			Temperature: &temp,
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Temperature == nil || *result.Temperature != temp {
		t.Errorf("expected Temperature %v, got %v", temp, result.Temperature)
	}
}

func TestToAnthropicChatRequest_Opus47_ReasoningMaxTokens_AdaptiveOnly(t *testing.T) {
	maxTok := 2048

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-7-20260401",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("think")}},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: new(8192),
			Reasoning:           &schemas.ChatReasoning{MaxTokens: &maxTok},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Thinking == nil {
		t.Fatal("expected Thinking to be set")
	}
	if result.Thinking.Type != "adaptive" {
		t.Errorf("expected thinking type 'adaptive' for Opus 4.7, got %q", result.Thinking.Type)
	}
	if result.Thinking.BudgetTokens != nil {
		t.Errorf("expected BudgetTokens to be nil for Opus 4.7, got %v", result.Thinking.BudgetTokens)
	}
	if result.Thinking.Display == nil || *result.Thinking.Display != "summarized" {
		t.Errorf("expected Display to default to 'summarized' for Opus 4.7, got %v", result.Thinking.Display)
	}
}

func TestToAnthropicChatRequest_NonOpus47_ReasoningMaxTokens_EnabledWithBudget(t *testing.T) {
	maxTok := 2048

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-6-20250514",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("think")}},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: new(8192),
			Reasoning:           &schemas.ChatReasoning{MaxTokens: &maxTok},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Thinking == nil {
		t.Fatal("expected Thinking to be set")
	}
	if result.Thinking.Type != "enabled" {
		t.Errorf("expected thinking type 'enabled' for Opus 4.6, got %q", result.Thinking.Type)
	}
	if result.Thinking.BudgetTokens == nil || *result.Thinking.BudgetTokens != maxTok {
		t.Errorf("expected BudgetTokens %d, got %v", maxTok, result.Thinking.BudgetTokens)
	}
}

func TestToAnthropicChatRequest_Opus47_ReasoningEffort_AdaptiveWithEffort(t *testing.T) {
	effort := "high"

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-7-20260401",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("think")}},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: new(8192),
			Reasoning:           &schemas.ChatReasoning{Effort: &effort},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Thinking == nil {
		t.Fatal("expected Thinking to be set")
	}
	if result.Thinking.Type != "adaptive" {
		t.Errorf("expected thinking type 'adaptive' for Opus 4.7 effort-based, got %q", result.Thinking.Type)
	}
	if result.OutputConfig == nil || result.OutputConfig.Effort == nil {
		t.Error("expected OutputConfig.Effort to be set for Opus 4.7 effort-based reasoning")
	}
}

func TestToAnthropicChatRequest_Opus47_DefaultsDisplayToSummarized(t *testing.T) {
	effort := "high"

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-7-20260401",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("think")}},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: new(8192),
			Reasoning:           &schemas.ChatReasoning{Effort: &effort},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Thinking == nil {
		t.Fatal("expected Thinking to be set")
	}
	if result.Thinking.Display == nil || *result.Thinking.Display != "summarized" {
		t.Errorf("expected Display to default to 'summarized' for Opus 4.7, got %v", result.Thinking.Display)
	}
}

func TestToAnthropicChatRequest_Opus47_RespectsExplicitDisplayOmitted(t *testing.T) {
	effort := "high"
	display := "omitted"

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-7-20260401",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("think")}},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: new(8192),
			Reasoning:           &schemas.ChatReasoning{Effort: &effort, Display: &display},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Thinking == nil {
		t.Fatal("expected Thinking to be set")
	}
	if result.Thinking.Display == nil || *result.Thinking.Display != "omitted" {
		t.Errorf("expected Display to be 'omitted' when explicitly set, got %v", result.Thinking.Display)
	}
}

func TestToAnthropicChatRequest_NonOpus47_NoDefaultDisplay(t *testing.T) {
	maxTok := 2048

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-6-20250514",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("think")}},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: new(8192),
			Reasoning:           &schemas.ChatReasoning{MaxTokens: &maxTok},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Thinking == nil {
		t.Fatal("expected Thinking to be set")
	}
	if result.Thinking.Display != nil {
		t.Errorf("expected Display to be nil for non-Opus 4.7, got %q", *result.Thinking.Display)
	}
}

// ---------------------------------------------------------------------------
// Structured output (response_format: json_schema) round-trip tests
// ---------------------------------------------------------------------------

// makeSOResponseFormat returns a response_format interface value in the
// OpenAI wire format expected by convertChatResponseFormatToTool.
func makeSOResponseFormat(schemaName string) interface{} {
	return map[string]interface{}{
		"type": "json_schema",
		"json_schema": map[string]interface{}{
			"name": schemaName,
			"schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"color":  map[string]interface{}{"type": "string"},
					"animal": map[string]interface{}{"type": "string"},
				},
				"required": []interface{}{"color", "animal"},
			},
		},
	}
}

// toolConversionProviders are the providers whose Anthropic-Messages-compatible
// endpoints reject native `output_config.format` and must instead receive
// structured output as a synthetic bf_so_* tool call. Any provider added here
// in the future must also be added to the branch under test in chat.go/responses.go.
var toolConversionProviders = []schemas.ModelProvider{schemas.Vertex, schemas.BedrockMantle, schemas.Azure}

// TestToAnthropicChatRequest_StructuredOutput_ToolConversion_NoThinking verifies that when
// response_format=json_schema is sent to a provider whose native Anthropic endpoint rejects
// output_config.format, Bifrost adds a synthetic bf_so_* tool AND forces tool_choice to it.
func TestToAnthropicChatRequest_StructuredOutput_ToolConversion_NoThinking(t *testing.T) {
	for _, provider := range toolConversionProviders {
		t.Run(string(provider), func(t *testing.T) {
			rf := makeSOResponseFormat("my_schema")
			bifrostReq := &schemas.BifrostChatRequest{
				Provider: provider,
				Model:    "claude-opus-4-6",
				Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
				},
				Params: &schemas.ChatParameters{
					ResponseFormat: &rf,
				},
			}

			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()
			result, err := ToAnthropicChatRequest(ctx, bifrostReq)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.OutputConfig != nil {
				t.Errorf("expected OutputConfig to stay unset for %s (native field unsupported), got %+v", provider, result.OutputConfig)
			}

			// A synthetic tool with the bf_so_ prefix must be present.
			var soTool *AnthropicTool
			for i := range result.Tools {
				if len(result.Tools[i].Name) > 6 && result.Tools[i].Name[:6] == "bf_so_" {
					soTool = &result.Tools[i]
					break
				}
			}
			if soTool == nil {
				t.Fatalf("expected a synthetic bf_so_* tool to be added for %s structured output", provider)
			}

			// ToolChoice must be set and must point at the SO tool.
			if result.ToolChoice == nil {
				t.Fatal("expected ToolChoice to be set when thinking is disabled")
			}
			if result.ToolChoice.Type != "tool" {
				t.Errorf("expected ToolChoice.Type=tool, got %q", result.ToolChoice.Type)
			}
			if result.ToolChoice.Name != soTool.Name {
				t.Errorf("expected ToolChoice.Name=%q, got %q", soTool.Name, result.ToolChoice.Name)
			}
		})
	}
}

// TestToAnthropicChatRequest_StructuredOutput_ToolConversion_ThinkingEffort verifies that when
// response_format=json_schema + reasoning_effort='medium' is sent to a tool-conversion provider,
// Bifrost still adds the synthetic tool but does NOT set tool_choice (to avoid Anthropic's
// "Thinking may not be enabled when tool_choice forces tool use" 400 error).
func TestToAnthropicChatRequest_StructuredOutput_ToolConversion_ThinkingEffort(t *testing.T) {
	for _, provider := range toolConversionProviders {
		t.Run(string(provider), func(t *testing.T) {
			rf := makeSOResponseFormat("my_schema")
			effort := "medium"
			bifrostReq := &schemas.BifrostChatRequest{
				Provider: provider,
				Model:    "claude-opus-4-6",
				Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: new(16000),
					ResponseFormat:      &rf,
					Reasoning:           &schemas.ChatReasoning{Effort: &effort},
				},
			}

			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()
			result, err := ToAnthropicChatRequest(ctx, bifrostReq)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Synthetic tool must still be present so the model knows the schema.
			found := false
			for _, tool := range result.Tools {
				if len(tool.Name) > 6 && tool.Name[:6] == "bf_so_" {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected synthetic bf_so_* tool to be present for %s even with thinking enabled", provider)
			}

			// ToolChoice must NOT be set — forcing it would trigger a 400 from Anthropic.
			if result.ToolChoice != nil {
				t.Errorf("expected ToolChoice to be nil when thinking is enabled (effort=%q) for %s, got %+v", effort, provider, result.ToolChoice)
			}
		})
	}
}

// TestToAnthropicChatRequest_StructuredOutput_ToolConversion_ThinkingMaxTokens is the same as
// the effort variant but uses explicit budget_tokens reasoning instead.
func TestToAnthropicChatRequest_StructuredOutput_ToolConversion_ThinkingMaxTokens(t *testing.T) {
	for _, provider := range toolConversionProviders {
		t.Run(string(provider), func(t *testing.T) {
			rf := makeSOResponseFormat("my_schema")
			maxTok := 4000
			bifrostReq := &schemas.BifrostChatRequest{
				Provider: provider,
				Model:    "claude-opus-4-6",
				Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: new(16000),
					ResponseFormat:      &rf,
					Reasoning:           &schemas.ChatReasoning{MaxTokens: &maxTok},
				},
			}

			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()
			result, err := ToAnthropicChatRequest(ctx, bifrostReq)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.ToolChoice != nil {
				t.Errorf("expected ToolChoice to be nil when thinking (MaxTokens) is enabled for %s, got %+v", provider, result.ToolChoice)
			}
		})
	}
}

// TestToAnthropicChatRequest_StructuredOutput_NativeOutputConfig_Anthropic verifies the
// negative case: a provider whose native Anthropic endpoint DOES support output_config.format
// (i.e. Anthropic itself) gets the native field set and no synthetic tool is added. This is
// the control that the regression (Azure missing from toolConversionProviders) would have
// broken in reverse — Azure incorrectly took this branch instead of the tool-conversion one.
func TestToAnthropicChatRequest_StructuredOutput_NativeOutputConfig_Anthropic(t *testing.T) {
	rf := makeSOResponseFormat("my_schema")
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-6",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
		},
		Params: &schemas.ChatParameters{
			ResponseFormat: &rf,
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.OutputConfig == nil || result.OutputConfig.Format == nil {
		t.Fatal("expected OutputConfig.Format to be set natively for Anthropic")
	}

	for _, tool := range result.Tools {
		if len(tool.Name) > 6 && tool.Name[:6] == "bf_so_" {
			t.Errorf("did not expect a synthetic bf_so_* tool for Anthropic, got %q", tool.Name)
		}
	}
}

// TestToBifrostChatResponse_StructuredOutput_FinishReasonStop verifies that when
// the model responds with only the synthetic SO tool (no real tool calls), the
// finish_reason is mapped to "stop", not "tool_calls".
func TestToBifrostChatResponse_StructuredOutput_FinishReasonStop(t *testing.T) {
	soToolName := "bf_so_my_schema"
	jsonInput, err := json.Marshal(map[string]interface{}{"color": "blue", "animal": "fox"})
	if err != nil {
		t.Fatalf("failed to marshal structured output input: %v", err)
	}

	response := &AnthropicMessageResponse{
		ID:    "msg_so_test",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-opus-4-6",
		Content: []AnthropicContentBlock{
			{
				Type:  AnthropicContentBlockTypeToolUse,
				ID:    schemas.Ptr("toolu_001"),
				Name:  schemas.Ptr(soToolName),
				Input: json.RawMessage(jsonInput),
			},
		},
		StopReason: AnthropicStopReasonToolUse,
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, soToolName)

	result := response.ToBifrostChatResponse(ctx)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	choice := result.Choices[0]

	// Content must be the JSON from the SO tool, not nil.
	msg := choice.ChatNonStreamResponseChoice.Message
	if msg.Content.ContentStr == nil {
		t.Fatal("expected ContentStr to be set from the structured output tool input")
	}

	// No real tool calls should be surfaced.
	if msg.ChatAssistantMessage != nil && len(msg.ChatAssistantMessage.ToolCalls) > 0 {
		t.Errorf("expected no tool calls in output, got %d", len(msg.ChatAssistantMessage.ToolCalls))
	}

	// Finish reason must be "stop", not "tool_calls".
	if choice.FinishReason == nil {
		t.Fatal("expected FinishReason to be set")
	}
	if *choice.FinishReason != string(schemas.BifrostFinishReasonStop) {
		t.Errorf("expected FinishReason=%q, got %q", schemas.BifrostFinishReasonStop, *choice.FinishReason)
	}
}

// TestToBifrostChatResponse_StructuredOutput_MixedWithRealTools verifies that when
// both the SO tool and a real tool call appear in the response, finish_reason remains
// "tool_calls" so the caller knows to handle the real tool.
func TestToBifrostChatResponse_StructuredOutput_MixedWithRealTools(t *testing.T) {
	soToolName := "bf_so_my_schema"
	soInput, err := json.Marshal(map[string]interface{}{"color": "blue", "animal": "fox"})
	if err != nil {
		t.Fatalf("failed to marshal SO input: %v", err)
	}
	realInput, err := json.Marshal(map[string]interface{}{"location": "NYC"})
	if err != nil {
		t.Fatalf("failed to marshal real tool input: %v", err)
	}

	response := &AnthropicMessageResponse{
		ID:    "msg_so_mixed",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-opus-4-6",
		Content: []AnthropicContentBlock{
			{
				Type:  AnthropicContentBlockTypeToolUse,
				ID:    schemas.Ptr("toolu_001"),
				Name:  schemas.Ptr(soToolName),
				Input: json.RawMessage(soInput),
			},
			{
				Type:  AnthropicContentBlockTypeToolUse,
				ID:    schemas.Ptr("toolu_real_001"),
				Name:  schemas.Ptr("get_weather"),
				Input: json.RawMessage(realInput),
			},
		},
		StopReason: AnthropicStopReasonToolUse,
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, soToolName)

	result := response.ToBifrostChatResponse(ctx)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	choice := result.Choices[0]

	// The real tool call must be surfaced.
	msg := choice.ChatNonStreamResponseChoice.Message
	if msg.ChatAssistantMessage == nil || len(msg.ChatAssistantMessage.ToolCalls) == 0 {
		t.Fatal("expected real tool calls to be present")
	}

	// Finish reason must remain "tool_calls".
	if choice.FinishReason == nil {
		t.Fatal("expected FinishReason to be set")
	}
	if *choice.FinishReason != string(schemas.BifrostFinishReasonToolCalls) {
		t.Errorf("expected FinishReason=%q, got %q", schemas.BifrostFinishReasonToolCalls, *choice.FinishReason)
	}
}

// TestToAnthropicChatRequest_MidConversationSystem_Opus48 verifies that a
// role:"system" message in a valid placement (followed by an assistant turn)
// is emitted as role:"system" in the Anthropic messages array when the
// provider is Anthropic and the model is Opus 4.8+.
// Valid placement per Anthropic: system must immediately follow a user turn
// and must precede an assistant turn or end the array.
func TestToAnthropicChatRequest_MidConversationSystem_Opus48(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-8",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("You are a helpful assistant.")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("From now on, respond only in French.")}},
			{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hi there!")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("How are you?")}},
		},
		Params: &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(1024)},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Top-level system field must hold only the initial system message.
	if result.System == nil {
		t.Fatal("expected top-level System to be set")
	}
	if result.System.ContentStr == nil || *result.System.ContentStr != "You are a helpful assistant." {
		t.Errorf("unexpected top-level System content: %v", result.System)
	}

	// Messages array: user, system(mid), assistant, user — 4 entries.
	if len(result.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(result.Messages))
	}
	if result.Messages[0].Role != AnthropicMessageRoleUser {
		t.Errorf("msg[0] role = %q, want user", result.Messages[0].Role)
	}
	if result.Messages[1].Role != AnthropicMessageRoleSystem {
		t.Errorf("msg[1] role = %q, want system (mid-conversation)", result.Messages[1].Role)
	}
	if result.Messages[2].Role != AnthropicMessageRoleAssistant {
		t.Errorf("msg[2] role = %q, want assistant", result.Messages[2].Role)
	}
	if result.Messages[3].Role != AnthropicMessageRoleUser {
		t.Errorf("msg[3] role = %q, want user", result.Messages[3].Role)
	}
}

// TestToAnthropicChatRequest_MidConversationSystem_InvalidPlacement verifies
// that a mid-conv system message with invalid placement (followed by user, not
// assistant) falls back to top-level system accumulation rather than emitting
// a role:"system" that would 400 on the Anthropic API.
func TestToAnthropicChatRequest_MidConversationSystem_InvalidPlacement(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-8",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Initial.")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
			{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hi!")}},
			// system followed by user — invalid placement, Anthropic returns 400
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Bad placement.")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Continue")}},
		},
		Params: &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(1024)},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Invalid placement: falls back to top-level system accumulation.
	if result.System == nil {
		t.Fatal("expected top-level System to contain both initial and fallback content")
	}
	blocks := result.System.ContentBlocks
	if len(blocks) != 2 {
		t.Fatalf("expected 2 system blocks (initial + fallback), got %d", len(blocks))
	}
	if blocks[0].Text == nil || *blocks[0].Text != "Initial." {
		t.Errorf("block[0] = %v, want \"Initial.\"", blocks[0].Text)
	}
	if blocks[1].Text == nil || *blocks[1].Text != "Bad placement." {
		t.Errorf("block[1] = %v, want \"Bad placement.\"", blocks[1].Text)
	}
	// No role:"system" in messages array.
	for i, msg := range result.Messages {
		if msg.Role == AnthropicMessageRoleSystem {
			t.Errorf("msg[%d] has role:system — invalid placement should have been caught", i)
		}
	}
}

// TestToAnthropicChatRequest_MidConversationSystem_FallbackAppends verifies that
// for a non-supporting model/provider the mid-conversation system content is
// appended to (not overwrites) the top-level system field.
func TestToAnthropicChatRequest_MidConversationSystem_FallbackAppends(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "global.anthropic.claude-opus-4-8",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Initial system.")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
			{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hi!")}},
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Mid-conv instruction.")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Continue")}},
		},
		Params: &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(1024)},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Top-level system field must exist and contain BOTH the initial and mid-conv content.
	if result.System == nil {
		t.Fatal("expected top-level System to be set")
	}
	if len(result.System.ContentBlocks) != 2 {
		t.Fatalf("expected 2 system content blocks (initial + mid-conv appended), got %d", len(result.System.ContentBlocks))
	}
	if result.System.ContentBlocks[0].Text == nil || *result.System.ContentBlocks[0].Text != "Initial system." {
		t.Errorf("block[0] = %v, want 'Initial system.'", result.System.ContentBlocks[0].Text)
	}
	if result.System.ContentBlocks[1].Text == nil || *result.System.ContentBlocks[1].Text != "Mid-conv instruction." {
		t.Errorf("block[1] = %v, want 'Mid-conv instruction.'", result.System.ContentBlocks[1].Text)
	}

	// No role:"system" entry should appear in the messages array.
	for i, msg := range result.Messages {
		if msg.Role == AnthropicMessageRoleSystem {
			t.Errorf("msg[%d] has role system — should not appear for Bedrock", i)
		}
	}
}

// TestToAnthropicChatRequest_MidConversationSystem_NotOnOpus47 verifies that
// mid-conversation system messages are NOT emitted for Opus 4.7 (feature is
// Opus 4.8+ only).
func TestToAnthropicChatRequest_MidConversationSystem_NotOnOpus47(t *testing.T) {
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-7",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
			{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hi!")}},
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("New instruction.")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Continue")}},
		},
		Params: &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(1024)},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Mid-conv instruction must be preserved in the top-level System field.
	if result.System == nil {
		t.Fatal("expected top-level System to contain fallback mid-conversation instruction")
	}
	foundFallback := false
	if result.System.ContentStr != nil && *result.System.ContentStr == "New instruction." {
		foundFallback = true
	}
	for _, block := range result.System.ContentBlocks {
		if block.Text != nil && *block.Text == "New instruction." {
			foundFallback = true
			break
		}
	}
	if !foundFallback {
		t.Fatal("expected mid-conversation system content to be preserved in top-level System")
	}

	for i, msg := range result.Messages {
		if msg.Role == AnthropicMessageRoleSystem {
			t.Errorf("msg[%d] has role system — should not appear for Opus 4.7", i)
		}
	}
}

// TestToBifrostChatCompletionStream_NoArgToolFlushesEmptyObject covers the
// "no-arg tool" case (e.g. tools whose Go input type is `struct{}` and whose
// JSON schema is `{}`). Anthropic emits content_block_start followed
// immediately by content_block_stop with no input_json_delta in between. The
// converter must flush a synthetic `arguments: "{}"` on stop so OpenAI clients
// can unmarshal the accumulated arguments as valid JSON.
func TestToBifrostChatCompletionStream_NoArgToolFlushesEmptyObject(t *testing.T) {
	state := NewAnthropicStreamState()

	idx := 0
	toolID := "toolu_no_args"
	toolName := "response_start"

	start := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockStart,
		Index: &idx,
		ContentBlock: &AnthropicContentBlock{
			Type: AnthropicContentBlockTypeToolUse,
			ID:   &toolID,
			Name: &toolName,
		},
	}
	startResp, bErr, isLast := start.ToBifrostChatCompletionStream(nil, "", state)
	if bErr != nil {
		t.Fatalf("unexpected error on start: %v", bErr)
	}
	if isLast {
		t.Fatal("expected non-terminal chunk on start")
	}
	if startResp == nil || len(startResp.Choices) != 1 {
		t.Fatalf("expected one choice on start, got %#v", startResp)
	}
	startDelta := startResp.Choices[0].ChatStreamResponseChoice.Delta
	if startDelta == nil || len(startDelta.ToolCalls) != 1 {
		t.Fatalf("expected one tool call on start, got %#v", startDelta)
	}
	startTC := startDelta.ToolCalls[0]
	if startTC.Type == nil || *startTC.Type != string(schemas.ChatToolTypeFunction) {
		t.Fatalf("expected type=function on start, got %#v", startTC.Type)
	}
	if startTC.ID == nil || *startTC.ID != toolID {
		t.Fatalf("expected id %q on start, got %#v", toolID, startTC.ID)
	}
	if startTC.Function.Name == nil || *startTC.Function.Name != toolName {
		t.Fatalf("expected name %q on start, got %#v", toolName, startTC.Function.Name)
	}
	if startTC.Function.Arguments != "" {
		t.Fatalf("expected empty initial arguments, got %q", startTC.Function.Arguments)
	}

	stop := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockStop,
		Index: &idx,
	}
	stopResp, bErr, isLast := stop.ToBifrostChatCompletionStream(nil, "", state)
	if bErr != nil {
		t.Fatalf("unexpected error on stop: %v", bErr)
	}
	if isLast {
		t.Fatal("expected non-terminal chunk on stop")
	}
	if stopResp == nil || len(stopResp.Choices) != 1 {
		t.Fatalf("expected one choice on stop flush, got %#v", stopResp)
	}
	stopDelta := stopResp.Choices[0].ChatStreamResponseChoice.Delta
	if stopDelta == nil || len(stopDelta.ToolCalls) != 1 {
		t.Fatalf("expected one tool call on stop flush, got %#v", stopDelta)
	}
	stopTC := stopDelta.ToolCalls[0]
	if stopTC.Type != nil {
		t.Errorf("expected type to be nil on stop-flush continuation, got %q", *stopTC.Type)
	}
	if stopTC.Function.Arguments != "{}" {
		t.Fatalf("expected flushed arguments \"{}\", got %q", stopTC.Function.Arguments)
	}

	accumulated := startTC.Function.Arguments + stopTC.Function.Arguments
	var parsed map[string]any
	if err := json.Unmarshal([]byte(accumulated), &parsed); err != nil {
		t.Fatalf("accumulated arguments %q did not parse as JSON: %v", accumulated, err)
	}
	if len(parsed) != 0 {
		t.Errorf("expected empty JSON object, got %#v", parsed)
	}

	// A defensive duplicate stop event must not re-emit a flush chunk.
	dupResp, _, _ := stop.ToBifrostChatCompletionStream(nil, "", state)
	if dupResp != nil {
		t.Errorf("duplicate stop should not re-flush, got %#v", dupResp)
	}
}

// TestToBifrostChatCompletionStream_EmptyPartialJSONSuppressedBeforeArgs
// guards two things at once: (1) the spurious empty partial_json marker that
// Anthropic emits right after content_block_start is suppressed (returns
// nil response); (2) once real non-empty input_json_delta chunks arrive, the
// subsequent content_block_stop must NOT emit a synthetic "{}" flush —
// otherwise concatenation would yield malformed JSON like `{"x":1}{}`.
func TestToBifrostChatCompletionStream_EmptyPartialJSONSuppressedBeforeArgs(t *testing.T) {
	state := NewAnthropicStreamState()

	idx := 0
	toolID := "toolu_real_args"
	toolName := "get_thing"
	empty := ""
	frag1 := `{"x":`
	frag2 := `1}`

	start := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockStart,
		Index: &idx,
		ContentBlock: &AnthropicContentBlock{
			Type: AnthropicContentBlockTypeToolUse,
			ID:   &toolID,
			Name: &toolName,
		},
	}
	startResp, _, _ := start.ToBifrostChatCompletionStream(nil, "", state)
	if startResp == nil {
		t.Fatal("expected a response on start")
	}
	accumulated := startResp.Choices[0].ChatStreamResponseChoice.Delta.ToolCalls[0].Function.Arguments

	emptyDelta := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockDelta,
		Index: &idx,
		Delta: &AnthropicStreamDelta{
			Type:        AnthropicStreamDeltaTypeInputJSON,
			PartialJSON: &empty,
		},
	}
	resp, bErr, isLast := emptyDelta.ToBifrostChatCompletionStream(nil, "", state)
	if bErr != nil {
		t.Fatalf("unexpected error on empty delta: %v", bErr)
	}
	if isLast {
		t.Fatal("expected non-terminal chunk on empty delta")
	}
	if resp != nil {
		t.Fatalf("expected empty partial_json to be suppressed, got %#v", resp)
	}

	for _, frag := range []*string{&frag1, &frag2} {
		ev := &AnthropicStreamEvent{
			Type:  AnthropicStreamEventTypeContentBlockDelta,
			Index: &idx,
			Delta: &AnthropicStreamDelta{
				Type:        AnthropicStreamDeltaTypeInputJSON,
				PartialJSON: frag,
			},
		}
		r, _, _ := ev.ToBifrostChatCompletionStream(nil, "", state)
		if r == nil {
			t.Fatalf("expected response for fragment %q", *frag)
		}
		tc := r.Choices[0].ChatStreamResponseChoice.Delta.ToolCalls[0]
		if tc.Type != nil {
			t.Errorf("expected type to be nil on continuation chunk for fragment %q, got %q", *frag, *tc.Type)
		}
		accumulated += tc.Function.Arguments
	}

	stop := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockStop,
		Index: &idx,
	}
	stopResp, _, _ := stop.ToBifrostChatCompletionStream(nil, "", state)
	if stopResp != nil {
		t.Fatalf("expected no flush on stop when args were streamed, got %#v", stopResp)
	}

	var parsed struct {
		X int `json:"x"`
	}
	if err := json.Unmarshal([]byte(accumulated), &parsed); err != nil {
		t.Fatalf("accumulated arguments %q did not parse as JSON: %v", accumulated, err)
	}
	if parsed.X != 1 {
		t.Errorf("expected {\"x\":1}, got %#v", parsed)
	}
}

// TestToBifrostChatCompletionStream_ContinuationOmitsTypeField is the
// regression guard for issue #3443: only the initial tool_call setup chunk
// should carry `function.type`; continuation chunks must not re-declare it,
// because strict OpenAI Chat Completions stream parsers treat a repeated
// `type` field on a continuation as a fresh tool-call declaration.
func TestToBifrostChatCompletionStream_ContinuationOmitsTypeField(t *testing.T) {
	state := NewAnthropicStreamState()

	idx := 0
	toolID := "toolu_type_check"
	toolName := "noop"
	frag := `{"a":1}`

	start := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockStart,
		Index: &idx,
		ContentBlock: &AnthropicContentBlock{
			Type: AnthropicContentBlockTypeToolUse,
			ID:   &toolID,
			Name: &toolName,
		},
	}
	startResp, _, _ := start.ToBifrostChatCompletionStream(nil, "", state)
	if startResp == nil {
		t.Fatal("expected response on start")
	}
	startTC := startResp.Choices[0].ChatStreamResponseChoice.Delta.ToolCalls[0]
	if startTC.Type == nil || *startTC.Type != string(schemas.ChatToolTypeFunction) {
		t.Fatalf("expected start chunk to carry type=function, got %#v", startTC.Type)
	}

	delta := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockDelta,
		Index: &idx,
		Delta: &AnthropicStreamDelta{
			Type:        AnthropicStreamDeltaTypeInputJSON,
			PartialJSON: &frag,
		},
	}
	deltaResp, _, _ := delta.ToBifrostChatCompletionStream(nil, "", state)
	if deltaResp == nil {
		t.Fatal("expected response on continuation delta")
	}
	contTC := deltaResp.Choices[0].ChatStreamResponseChoice.Delta.ToolCalls[0]
	if contTC.Type != nil {
		t.Errorf("expected continuation chunk to omit type, got %q", *contTC.Type)
	}
}

// TestToBifrostChatCompletionStream_MixedToolBlocks interleaves a no-arg tool
// and a real-args tool across two content_block indices. Ensures the
// per-content-block sawArgsDelta tracking flushes "{}" for the no-arg block
// and not for the real-args block.
func TestToBifrostChatCompletionStream_MixedToolBlocks(t *testing.T) {
	state := NewAnthropicStreamState()

	noArgIdx := 0
	argIdx := 1
	noArgID := "toolu_no_args"
	argID := "toolu_args"
	noArgName := "response_start"
	argName := "get_thing"
	frag := `{"y":2}`

	events := []*AnthropicStreamEvent{
		{
			Type:  AnthropicStreamEventTypeContentBlockStart,
			Index: &noArgIdx,
			ContentBlock: &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeToolUse,
				ID:   &noArgID,
				Name: &noArgName,
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockStart,
			Index: &argIdx,
			ContentBlock: &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeToolUse,
				ID:   &argID,
				Name: &argName,
			},
		},
		{
			Type:  AnthropicStreamEventTypeContentBlockDelta,
			Index: &argIdx,
			Delta: &AnthropicStreamDelta{
				Type:        AnthropicStreamDeltaTypeInputJSON,
				PartialJSON: &frag,
			},
		},
	}

	args := map[int]string{noArgIdx: "", argIdx: ""}
	for _, ev := range events {
		r, _, _ := ev.ToBifrostChatCompletionStream(nil, "", state)
		if r == nil {
			continue
		}
		tcs := r.Choices[0].ChatStreamResponseChoice.Delta.ToolCalls
		if len(tcs) != 1 {
			continue
		}
		// Each tool call carries its own assistant-message index; map back via
		// the state we already populated by content-block index.
		for cbIdx, callIdx := range state.contentBlockToToolCallIdx {
			if uint16(callIdx) == tcs[0].Index {
				args[cbIdx] += tcs[0].Function.Arguments
				break
			}
		}
	}

	// Stop the no-arg block first; expect a "{}" flush.
	stopNoArg := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockStop,
		Index: &noArgIdx,
	}
	r, _, _ := stopNoArg.ToBifrostChatCompletionStream(nil, "", state)
	if r == nil {
		t.Fatal("expected flush on stop for no-arg block")
	}
	args[noArgIdx] += r.Choices[0].ChatStreamResponseChoice.Delta.ToolCalls[0].Function.Arguments

	// Stop the args block; expect no flush.
	stopArg := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockStop,
		Index: &argIdx,
	}
	r, _, _ = stopArg.ToBifrostChatCompletionStream(nil, "", state)
	if r != nil {
		t.Fatalf("expected no flush on stop for args block, got %#v", r)
	}

	if args[noArgIdx] != "{}" {
		t.Errorf("no-arg accumulated arguments = %q, want \"{}\"", args[noArgIdx])
	}
	if args[argIdx] != `{"y":2}` {
		t.Errorf("args accumulated arguments = %q, want %q", args[argIdx], `{"y":2}`)
	}
}
