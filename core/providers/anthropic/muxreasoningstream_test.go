package anthropic

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestMuxReasoningStream_ReproducesCrashWithoutFix drives Bifrost's
// Chat-Completions-fallback streaming path (schemas.BifrostChatResponse.
// ToBifrostResponsesStreamResponse — used by every provider whose ResponsesStream
// falls back to ChatCompletionStream, e.g. Ollama, Groq, Cerebras, DeepSeek,
// Mistral, Nebius, Parasail, SGL, VLLM) with reasoning-then-text deltas, exactly
// as a local Ollama reasoning model (qwen3) streams them over its OpenAI-compatible
// endpoint. It then feeds the resulting Bifrost Responses-stream events through
// ToAnthropicResponsesStreamResponse — the same conversion the Anthropic-compat
// transport (/anthropic/v1/messages) applies before writing SSE to the client.
//
// Before the fix: the reasoning delta never got an output_item.added of its own
// (mux.go emitted only response.reasoning_summary_text.delta, with neither Item
// nor ItemID set), so reverseStreamItemKey fell back to "oi:<index>" — a key
// distinct from the text item's Item.ID key registered at its own
// output_item.added. blockIndexFor("oi:0") therefore always missed and silently
// allocated a fresh Anthropic content-block index with no content_block_start
// ever emitted for it. A strict client — including the official anthropic-python
// SDK's streaming accumulator (accumulate_event in
// anthropic/lib/streaming/_messages.py), which appends to its content list only
// on content_block_start and indexes it unchecked on content_block_delta — sees a
// content_block_delta for a block it never opened and raises IndexError.
func TestMuxReasoningStream_ReproducesCrashWithoutFix(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	state := schemas.AcquireChatToResponsesStreamState()
	defer schemas.ReleaseChatToResponsesStreamState(state)

	role := string(schemas.ChatMessageRoleAssistant)
	reasoning1 := "Okay, "
	reasoning2 := "so the user is asking 2+2."
	content1 := "2 + 2"
	content2 := " = 4."
	stop := string(schemas.BifrostFinishReasonStop)

	makeChunk := func(role *string, reasoning *string, content *string, finishReason *string) *schemas.BifrostChatResponse {
		return &schemas.BifrostChatResponse{
			ID:    "chatcmpl-test",
			Model: "qwen3:0.6b",
			Choices: []schemas.BifrostResponseChoice{
				{
					FinishReason: finishReason,
					ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
						Delta: &schemas.ChatStreamResponseChoiceDelta{
							Role:      role,
							Reasoning: reasoning,
							Content:   content,
						},
					},
				},
			},
		}
	}

	chunks := []*schemas.BifrostChatResponse{
		makeChunk(&role, nil, nil, nil),
		makeChunk(nil, &reasoning1, nil, nil),
		makeChunk(nil, &reasoning2, nil, nil),
		makeChunk(nil, nil, &content1, nil),
		makeChunk(nil, nil, &content2, nil),
		makeChunk(nil, nil, nil, &stop),
	}

	var frames []ptFrame
	var sawReasoningItemAdded bool
	var sawTextItemAdded bool
	for _, c := range chunks {
		for _, r := range c.ToBifrostResponsesStreamResponse(state) {
			if r == nil {
				continue
			}
			if r.Type == schemas.ResponsesStreamResponseTypeOutputItemAdded && r.Item != nil && r.Item.Type != nil {
				switch *r.Item.Type {
				case schemas.ResponsesMessageTypeReasoning:
					sawReasoningItemAdded = true
				case schemas.ResponsesMessageTypeMessage:
					sawTextItemAdded = true
				}
			}
			for _, e := range ToAnthropicResponsesStreamResponse(ctx, r) {
				idx := -1
				if e.Index != nil {
					idx = *e.Index
				}
				frames = append(frames, ptFrame{typ: string(e.Type), idx: idx, via: "conv"})
			}
		}
	}

	if !sawReasoningItemAdded {
		t.Fatal("expected a response.output_item.added event with Item.Type == reasoning before any reasoning delta")
	}
	if !sawTextItemAdded {
		t.Fatal("expected a response.output_item.added event with Item.Type == message for the text content")
	}

	if problems := blockFramingProblems(t, frames, false); len(problems) > 0 {
		t.Errorf("mux reasoning stream is not well-formed (would crash a strict Anthropic SSE client, e.g. the official anthropic-python SDK's accumulate_event): %v", problems)
	}

	rs := getOrCreateAnthropicToResponsesStreamState(ctx)
	if len(rs.blockIndexMisses) > 0 {
		t.Errorf("reverse converter recorded block-index misses (content_block_delta/_stop for a block whose content_block_start was never emitted): %v", rs.blockIndexMisses)
	}
}
