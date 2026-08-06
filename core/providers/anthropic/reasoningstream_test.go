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
		blocks := convertBifrostReasoningToAnthropicThinking(schemas.NewBifrostContext(nil, schemas.NoDeadline), msg, schemas.DeepSeek, "deepseek-chat")
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
		blocks := convertBifrostReasoningToAnthropicThinking(schemas.NewBifrostContext(nil, schemas.NoDeadline), msg, schemas.OpenAI, "gpt-5")
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

	// A custom provider reports its own key, so the gate has to resolve it back to
	// the base provider type Bifrost recorded on the context -- otherwise OpenAI
	// models served through an openai-based custom provider silently lose the id.
	t.Run("openai-based custom provider embeds the id", func(t *testing.T) {
		msg := newMsg()
		ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyBaseProviderType, schemas.OpenAI)
		blocks := convertBifrostReasoningToAnthropicThinking(ctx, msg, schemas.ModelProvider("my-openai"), "my-gpt-deployment")
		if len(blocks) != 1 {
			t.Fatalf("expected 1 thinking block, got %d", len(blocks))
		}
		id, _, ok := providerUtils.ExtractReasoningItemID(*blocks[0].Signature)
		if !ok || id == nil || *id != *msg.ID {
			t.Errorf("custom-provider signature = %q, want an embedded id %q", *blocks[0].Signature, *msg.ID)
		}
	})
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
