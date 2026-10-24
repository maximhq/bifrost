package anthropic

import (
	"context"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// reasoningStreamChunk builds a single Chat streaming chunk.
func reasoningStreamChunk(id string, delta *schemas.ChatStreamResponseChoiceDelta, finishReason *string) *schemas.BifrostChatResponse {
	return &schemas.BifrostChatResponse{
		ID:    id,
		Model: "deepseek-reasoner",
		Choices: []schemas.BifrostResponseChoice{
			{
				Index:                    0,
				FinishReason:             finishReason,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: delta},
			},
		},
	}
}

// TestReasoningStream_NoOrphanThinkingBlock drives the full Chat -> Responses ->
// Anthropic SSE pipeline for a reasoning model (reasoning_content first, then
// content) and asserts the emitted Anthropic stream is well-formed: every
// content_block_delta references a block a prior content_block_start opened, and
// every opened block is stopped. Before the fix, the thinking delta landed on an
// index no content_block_start opened, which Claude Code rejects with
// "Content block not found", aborts the stream, and reports a client disconnect.
func TestReasoningStream_NoOrphanThinkingBlock(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	state := schemas.AcquireChatToResponsesStreamState()
	defer schemas.ReleaseChatToResponsesStreamState(state)

	p := func(s string) *string { return &s }
	chunks := []*schemas.BifrostChatResponse{
		reasoningStreamChunk("c1", &schemas.ChatStreamResponseChoiceDelta{Role: p("assistant"), Reasoning: p("")}, nil),
		reasoningStreamChunk("c1", &schemas.ChatStreamResponseChoiceDelta{Reasoning: p("The")}, nil),
		reasoningStreamChunk("c1", &schemas.ChatStreamResponseChoiceDelta{Reasoning: p(" user asked")}, nil),
		reasoningStreamChunk("c1", &schemas.ChatStreamResponseChoiceDelta{Content: p("Run ")}, nil),
		reasoningStreamChunk("c1", &schemas.ChatStreamResponseChoiceDelta{Content: p("make test")}, nil),
		reasoningStreamChunk("c1", &schemas.ChatStreamResponseChoiceDelta{}, p("stop")),
	}

	openBlocks := map[int]bool{} // set of currently-open block indices
	var sawThinkingStart, sawThinkingDelta bool

	for _, c := range chunks {
		for _, r := range c.ToBifrostResponsesStreamResponse(state) {
			for _, e := range ToAnthropicResponsesStreamResponse(ctx, r) {
				switch e.Type {
				case AnthropicStreamEventTypeContentBlockStart:
					if e.Index == nil {
						t.Fatal("content_block_start with nil index")
					}
					if _, dup := openBlocks[*e.Index]; dup {
						t.Fatalf("content_block_start opened index %d twice", *e.Index)
					}
					openBlocks[*e.Index] = true
					if e.ContentBlock != nil && e.ContentBlock.Type == AnthropicContentBlockTypeThinking {
						sawThinkingStart = true
					}
				case AnthropicStreamEventTypeContentBlockDelta:
					if e.Index == nil {
						t.Fatal("content_block_delta with nil index")
					}
					if _, open := openBlocks[*e.Index]; !open {
						t.Fatalf("content_block_delta for index %d that no content_block_start opened (orphan block)", *e.Index)
					}
					if e.Delta != nil && e.Delta.Type == AnthropicStreamDeltaTypeThinking {
						sawThinkingDelta = true
					}
				case AnthropicStreamEventTypeContentBlockStop:
					if e.Index == nil {
						t.Fatal("content_block_stop with nil index")
					}
					if _, open := openBlocks[*e.Index]; !open {
						t.Fatalf("content_block_stop for unopened index %d", *e.Index)
					}
					delete(openBlocks, *e.Index)
				}
			}
		}
	}

	if !sawThinkingStart {
		t.Fatal("no content_block_start type=thinking emitted for reasoning content")
	}
	if !sawThinkingDelta {
		t.Fatal("no thinking_delta emitted")
	}
	if len(openBlocks) != 0 {
		t.Fatalf("blocks left open (never stopped): %v", openBlocks)
	}
	if misses := getOrCreateAnthropicToResponsesStreamState(ctx).blockIndexMisses; len(misses) != 0 {
		t.Fatalf("block index misses (delta/stop for an unregistered block): %v", misses)
	}
}

// TestConvertBifrostReasoning_SignaturePresent asserts converted (non-Anthropic)
// reasoning yields a thinking block with a non-nil signature. The Agent SDK's
// non-streaming parser requires the field; omitting it fails with
// "Missing required field in assistant message: 'signature'". It also pins the
// sourceProvider gate on this path: an id is only set here, so a nil/empty id
// would make the DeepSeek subtest pass regardless of whether the gate works.
func TestConvertBifrostReasoning_SignaturePresent(t *testing.T) {
	reasoning := "The user asked how to run core tests."
	newMsg := func() *schemas.ResponsesMessage {
		return &schemas.ResponsesMessage{
			ID:   schemas.Ptr("rs_test123"),
			Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{Type: schemas.ResponsesOutputMessageContentTypeReasoning, Text: &reasoning}, // no Signature
				},
			},
		}
	}

	t.Run("DeepSeek does not embed the id", func(t *testing.T) {
		msg := newMsg()
		blocks := convertBifrostReasoningToAnthropicThinking(schemas.NewBifrostContext(nil, schemas.NoDeadline), msg, schemas.DeepSeek, "deepseek-chat", false)
		if len(blocks) != 1 {
			t.Fatalf("expected 1 thinking block, got %d", len(blocks))
		}
		if blocks[0].Type != AnthropicContentBlockTypeThinking {
			t.Fatalf("expected thinking block, got %q", blocks[0].Type)
		}
		if blocks[0].Signature == nil {
			t.Fatal("thinking block signature is nil; Agent SDK parse would fail on the missing field")
		}
		if *blocks[0].Signature != "" {
			t.Errorf("DeepSeek-sourced signature = %q, want empty (non-OpenAI sources must not embed the id)", *blocks[0].Signature)
		}
		if _, _, ok := providerUtils.ExtractReasoningItemID(*blocks[0].Signature); ok {
			t.Errorf("DeepSeek-sourced signature %q unexpectedly carries an embedded reasoning item id", *blocks[0].Signature)
		}
	})

	t.Run("OpenAI embeds the id", func(t *testing.T) {
		msg := newMsg()
		blocks := convertBifrostReasoningToAnthropicThinking(schemas.NewBifrostContext(nil, schemas.NoDeadline), msg, schemas.OpenAI, "gpt-5", false)
		if len(blocks) != 1 {
			t.Fatalf("expected 1 thinking block, got %d", len(blocks))
		}
		if blocks[0].Signature == nil {
			t.Fatal("thinking block signature is nil; Agent SDK parse would fail on the missing field")
		}
		id, _, ok := providerUtils.ExtractReasoningItemID(*blocks[0].Signature)
		if !ok || id == nil || *id != *msg.ID {
			t.Errorf("OpenAI-sourced signature = %q, want an embedded id %q", *blocks[0].Signature, *msg.ID)
		}
	})

	t.Run("OpenAI chat bridge does not embed its minted id", func(t *testing.T) {
		msg := newMsg()
		blocks := convertBifrostReasoningToAnthropicThinking(schemas.NewBifrostContext(nil, schemas.NoDeadline), msg, schemas.OpenAI, "gpt-4o", true)
		if len(blocks) != 1 {
			t.Fatalf("expected 1 thinking block, got %d", len(blocks))
		}
		if blocks[0].Signature == nil {
			t.Fatal("thinking block signature is nil; Agent SDK parse would fail on the missing field")
		}
		if *blocks[0].Signature != "" {
			t.Errorf("chat-bridged signature = %q, want empty (Chat Completions issued no reasoning item id)", *blocks[0].Signature)
		}
	})

	// A chat upstream that returns reasoning_details pairs its encrypted token with a
	// real id, which the replay needs just as on the native Responses route.
	t.Run("OpenAI chat bridge embeds an id paired with an encrypted token", func(t *testing.T) {
		msg := newMsg()
		msg.ResponsesReasoning = &schemas.ResponsesReasoning{EncryptedContent: schemas.Ptr("enc-token")}
		blocks := convertBifrostReasoningToAnthropicThinking(schemas.NewBifrostContext(nil, schemas.NoDeadline), msg, schemas.OpenAI, "gpt-5", true)
		if len(blocks) != 1 || blocks[0].Signature == nil {
			t.Fatalf("expected 1 signed thinking block, got %+v", blocks)
		}
		id, _, ok := providerUtils.ExtractReasoningItemID(*blocks[0].Signature)
		if !ok || id == nil || *id != *msg.ID {
			t.Errorf("chat-bridged signature = %q, want an embedded id %q", *blocks[0].Signature, *msg.ID)
		}
	})

	// A custom provider reports its own key, so the gate has to resolve it back to
	// the base provider type Bifrost recorded on the context -- otherwise OpenAI
	// models served through an openai-based custom provider silently lose the id.
	t.Run("openai-based custom provider embeds the id", func(t *testing.T) {
		msg := newMsg()
		ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyBaseProviderType, schemas.OpenAI)
		blocks := convertBifrostReasoningToAnthropicThinking(ctx, msg, schemas.ModelProvider("my-openai"), "my-gpt-deployment", false)
		if len(blocks) != 1 {
			t.Fatalf("expected 1 thinking block, got %d", len(blocks))
		}
		id, _, ok := providerUtils.ExtractReasoningItemID(*blocks[0].Signature)
		if !ok || id == nil || *id != *msg.ID {
			t.Errorf("custom-provider signature = %q, want an embedded id %q", *blocks[0].Signature, *msg.ID)
		}
	})
}

// chatBridgeThinkingSignatures drives OpenAI Chat Completions reasoning chunks
// through the chat-to-responses stream converter into Anthropic SSE, stamping each
// spread event the way the OpenAI provider's Responses-to-Chat fallback does when
// bridged, and returns every signature the thinking block carried.
func chatBridgeThinkingSignatures(t *testing.T, bridged bool) []string {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	state := schemas.AcquireChatToResponsesStreamState()
	defer schemas.ReleaseChatToResponsesStreamState(state)

	p := func(s string) *string { return &s }
	chunks := []*schemas.BifrostChatResponse{
		reasoningStreamChunk("chatcmpl-abc", &schemas.ChatStreamResponseChoiceDelta{Role: p("assistant"), Content: p("")}, nil),
		reasoningStreamChunk("chatcmpl-abc", &schemas.ChatStreamResponseChoiceDelta{Reasoning: p("Let me")}, nil),
		reasoningStreamChunk("chatcmpl-abc", &schemas.ChatStreamResponseChoiceDelta{Reasoning: p(" think")}, nil),
		reasoningStreamChunk("chatcmpl-abc", &schemas.ChatStreamResponseChoiceDelta{Content: p("Hi")}, nil),
		reasoningStreamChunk("chatcmpl-abc", &schemas.ChatStreamResponseChoiceDelta{}, p("stop")),
	}

	var signatures []string
	for _, c := range chunks {
		c.ExtraFields.RoutingInfo.Provider = schemas.OpenAI
		c.ExtraFields.RoutingInfo.Model = "gpt-4o"
		for _, r := range c.ToBifrostResponsesStreamResponse(state) {
			if bridged {
				r.ExtraFields.ConvertedRequestType = schemas.ChatCompletionRequest
			}
			for _, e := range ToAnthropicResponsesStreamResponse(ctx, r) {
				switch {
				case e.Type == AnthropicStreamEventTypeContentBlockStart && e.ContentBlock != nil &&
					e.ContentBlock.Type == AnthropicContentBlockTypeThinking:
					if e.ContentBlock.Signature == nil {
						t.Fatal("thinking content_block_start has no signature field")
					}
					signatures = append(signatures, *e.ContentBlock.Signature)
				case e.Type == AnthropicStreamEventTypeContentBlockDelta && e.Delta != nil &&
					e.Delta.Type == AnthropicStreamDeltaTypeSignature && e.Delta.Signature != nil:
					signatures = append(signatures, *e.Delta.Signature)
				}
			}
		}
	}
	if len(signatures) == 0 {
		t.Fatal("no thinking content_block_start emitted for reasoning content")
	}
	return signatures
}

// TestReasoningStream_ChatBridgeOmitsSyntheticSignature pins #8549: reasoning
// bridged from an OpenAI Chat Completions stream has no upstream reasoning item, so
// the thinking block must open with signature "" (Anthropic's own content_block_start
// shape) instead of the converter-minted "brid_msg_chatcmpl-abc_reasoning:".
func TestReasoningStream_ChatBridgeOmitsSyntheticSignature(t *testing.T) {
	for _, sig := range chatBridgeThinkingSignatures(t, true) {
		if sig != "" {
			t.Errorf("chat-bridged thinking signature = %q, want empty", sig)
		}
	}

	// The same stream without the bridge marker is still embedded, so the
	// assertion above is driven by the marker and not by a dead gate.
	sigs := chatBridgeThinkingSignatures(t, false)
	if _, _, ok := providerUtils.ExtractReasoningItemID(sigs[0]); !ok {
		t.Errorf("unbridged OpenAI thinking signature = %q, want an embedded reasoning item id", sigs[0])
	}
}

// TestToAnthropicResponsesResponse_ChatBridgeOmitsSyntheticSignature is the
// non-streaming half of #8549: a Chat Completions response converted to Responses
// mints an rs_ id for its reasoning, which must not reach the client as a signature.
func TestToAnthropicResponsesResponse_ChatBridgeOmitsSyntheticSignature(t *testing.T) {
	reasoning := "Let me think step"
	content := "Hi"
	chat := &schemas.BifrostChatResponse{
		ID:     "chatcmpl-abc",
		Object: "chat.completion",
		Model:  "gpt-4o",
		Choices: []schemas.BifrostResponseChoice{{
			Index:        0,
			FinishReason: schemas.Ptr("stop"),
			ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
				Message: &schemas.ChatMessage{
					Role:                 schemas.ChatMessageRoleAssistant,
					Content:              &schemas.ChatMessageContent{ContentStr: &content},
					ChatAssistantMessage: &schemas.ChatAssistantMessage{Reasoning: &reasoning},
				},
			},
		}},
	}
	resp := chat.ToBifrostResponsesResponse()
	// Stamped as the OpenAI provider's Responses-to-Chat fallback does.
	resp.ExtraFields.ConvertedRequestType = schemas.ChatCompletionRequest
	resp.ExtraFields.RoutingInfo.Provider = schemas.OpenAI
	resp.ExtraFields.RoutingInfo.Model = "gpt-4o"

	out := ToAnthropicResponsesResponse(schemas.NewBifrostContext(nil, schemas.NoDeadline), resp)
	var sawThinking bool
	for _, b := range out.Content {
		if b.Type != AnthropicContentBlockTypeThinking {
			continue
		}
		sawThinking = true
		if b.Signature == nil {
			t.Fatal("thinking block signature is nil; Agent SDK parse would fail on the missing field")
		}
		if *b.Signature != "" {
			t.Errorf("chat-bridged thinking signature = %q, want empty", *b.Signature)
		}
	}
	if !sawThinking {
		t.Fatalf("no thinking block in converted response: %+v", out.Content)
	}
}

// TestReasoningStream_ResumedReasoningNotEmittedPastStop guards the
// reasoning -> text -> reasoning ordering: once text closes the thinking block,
// a resumed reasoning chunk must not emit a thinking delta past its
// content_block_stop. The late reasoning is dropped; the stream stays well-formed.
func TestReasoningStream_ResumedReasoningNotEmittedPastStop(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	state := schemas.AcquireChatToResponsesStreamState()
	defer schemas.ReleaseChatToResponsesStreamState(state)

	p := func(s string) *string { return &s }
	chunks := []*schemas.BifrostChatResponse{
		reasoningStreamChunk("c1", &schemas.ChatStreamResponseChoiceDelta{Role: p("assistant"), Reasoning: p("think")}, nil),
		reasoningStreamChunk("c1", &schemas.ChatStreamResponseChoiceDelta{Content: p("answer")}, nil),
		reasoningStreamChunk("c1", &schemas.ChatStreamResponseChoiceDelta{Reasoning: p(" more think")}, nil), // resumes after text
		reasoningStreamChunk("c1", &schemas.ChatStreamResponseChoiceDelta{}, p("stop")),
	}

	openBlocks := map[int]bool{}
	for _, c := range chunks {
		for _, r := range c.ToBifrostResponsesStreamResponse(state) {
			for _, e := range ToAnthropicResponsesStreamResponse(ctx, r) {
				switch e.Type {
				case AnthropicStreamEventTypeContentBlockStart:
					openBlocks[*e.Index] = true
				case AnthropicStreamEventTypeContentBlockDelta:
					if !openBlocks[*e.Index] {
						t.Fatalf("content_block_delta for index %d with no open block (delta past stop / orphan)", *e.Index)
					}
				case AnthropicStreamEventTypeContentBlockStop:
					delete(openBlocks, *e.Index)
				}
			}
		}
	}
	if len(openBlocks) != 0 {
		t.Fatalf("blocks left open: %v", openBlocks)
	}
	if misses := getOrCreateAnthropicToResponsesStreamState(ctx).blockIndexMisses; len(misses) != 0 {
		t.Fatalf("block index misses: %v", misses)
	}
}

// TestTextStream_ContentAfterToolStartNotEmittedPastStop guards the
// text -> tool_call -> text ordering on the Chat -> Responses -> Anthropic SSE path.
// Once a tool call closes the text block, a later delta.content (SGLang's
// qwen3_coder parser emits "\n" next to the closing tool-call arguments) must not
// emit a text_delta on the stopped text index while the tool_use block is open.
// Claude Code rejects that with "The response stream was malformed".
func TestTextStream_ContentAfterToolStartNotEmittedPastStop(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	state := schemas.AcquireChatToResponsesStreamState()
	defer schemas.ReleaseChatToResponsesStreamState(state)

	p := func(s string) *string { return &s }
	toolCall := func(id *string, name *string, args string) []schemas.ChatAssistantMessageToolCall {
		return []schemas.ChatAssistantMessageToolCall{{
			Index:    0,
			ID:       id,
			Function: schemas.ChatAssistantMessageToolCallFunction{Name: name, Arguments: args},
		}}
	}
	chunks := []*schemas.BifrostChatResponse{
		reasoningStreamChunk("c1", &schemas.ChatStreamResponseChoiceDelta{Role: p("assistant"), Content: p("Running it.")}, nil),
		reasoningStreamChunk("c1", &schemas.ChatStreamResponseChoiceDelta{ToolCalls: toolCall(p("call_1"), p("bash"), `{"command":`)}, nil),
		reasoningStreamChunk("c1", &schemas.ChatStreamResponseChoiceDelta{Content: p("\n"), ToolCalls: toolCall(nil, nil, `"echo hi"}`)}, nil),
		reasoningStreamChunk("c1", &schemas.ChatStreamResponseChoiceDelta{}, p("tool_calls")),
	}

	openBlocks := map[int]bool{}
	var sawToolArgs bool
	for _, c := range chunks {
		for _, r := range c.ToBifrostResponsesStreamResponse(state) {
			for _, e := range ToAnthropicResponsesStreamResponse(ctx, r) {
				switch e.Type {
				case AnthropicStreamEventTypeContentBlockStart:
					openBlocks[*e.Index] = true
				case AnthropicStreamEventTypeContentBlockDelta:
					if !openBlocks[*e.Index] {
						t.Fatalf("content_block_delta for index %d with no open block (delta past stop): %+v", *e.Index, e.Delta)
					}
					if e.Delta != nil && e.Delta.Type == AnthropicStreamDeltaTypeInputJSON && e.Delta.PartialJSON != nil && *e.Delta.PartialJSON == `"echo hi"}` {
						sawToolArgs = true
					}
				case AnthropicStreamEventTypeContentBlockStop:
					delete(openBlocks, *e.Index)
				}
			}
		}
	}
	if !sawToolArgs {
		t.Fatal("tool arguments carried alongside the trailing content were not emitted")
	}
	if len(openBlocks) != 0 {
		t.Fatalf("blocks left open: %v", openBlocks)
	}
	if misses := getOrCreateAnthropicToResponsesStreamState(ctx).blockIndexMisses; len(misses) != 0 {
		t.Fatalf("block index misses: %v", misses)
	}
}
