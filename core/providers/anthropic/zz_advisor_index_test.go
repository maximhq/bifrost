package anthropic

// Diagnostic (not part of the shipped suite intent): verifies the Anthropic SSE
// that the reverse converter emits for an advisor turn has *well-formed
// content_block indices* — every content_block_stop / content_block_delta must
// reference an index opened by a prior content_block_start, and no index may be
// opened twice or left unclosed. This is the invariant a strict client
// (Claude Code / `claude -p`) enforces; when Bifrost violates it the client
// fails the turn with "API Error: Content block not found".
//
// Two inputs:
//   - advisorStreamEvents (already in advisor_test.go): a bare advisor turn.
//   - a claude-cli-shaped turn with a leading `thinking` block before the
//     advisor — the shape that reproduced the break on the m4 deploy.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// runAdvisorStream drives raw Anthropic SSE strings through the full normalized
// round-trip (Anthropic SSE -> Bifrost Responses stream -> Anthropic SSE) and
// returns the emitted Anthropic events.
func runAdvisorStream(t *testing.T, raws []string) []*AnthropicStreamEvent {
	t.Helper()
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	state := newAdvisorStreamState()
	var emitted []*AnthropicStreamEvent
	seq := 0
	for _, raw := range raws {
		var chunk AnthropicStreamEvent
		if err := sonic.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		responses, bErr, _ := chunk.ToBifrostResponsesStream(ctx, seq, state)
		if bErr != nil {
			t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
		}
		for _, r := range responses {
			seq++
			emitted = append(emitted, ToAnthropicResponsesStreamResponse(ctx, r)...)
		}
	}
	return emitted
}

// assertBlockIndicesWellFormed checks the content_block open/close invariant and
// returns the human-readable event sequence (always, for diagnostics).
func assertBlockIndicesWellFormed(t *testing.T, emitted []*AnthropicStreamEvent) string {
	t.Helper()
	open := map[int]bool{}    // currently-open block indices
	started := map[int]bool{} // indices ever opened (detect reuse)
	var seqLines []string
	idxStr := func(e *AnthropicStreamEvent) string {
		if e.Index == nil {
			return "nil"
		}
		return fmt.Sprintf("%d", *e.Index)
	}

	for _, e := range emitted {
		switch e.Type {
		case AnthropicStreamEventTypeContentBlockStart:
			kind := ""
			if e.ContentBlock != nil {
				kind = string(e.ContentBlock.Type)
				if e.ContentBlock.Name != nil {
					kind += "/" + *e.ContentBlock.Name
				}
			}
			seqLines = append(seqLines, fmt.Sprintf("START idx=%s %s", idxStr(e), kind))
			if e.Index == nil {
				t.Errorf("content_block_start with nil index (%s)", kind)
				continue
			}
			if open[*e.Index] {
				t.Errorf("BUG: content_block_start idx=%d while that index is already OPEN (%s)", *e.Index, kind)
			}
			if started[*e.Index] {
				t.Errorf("BUG: content_block_start idx=%d REUSES an index that was already opened earlier (%s)", *e.Index, kind)
			}
			open[*e.Index] = true
			started[*e.Index] = true

		case AnthropicStreamEventTypeContentBlockDelta:
			dk := ""
			if e.Delta != nil {
				dk = string(e.Delta.Type)
			}
			seqLines = append(seqLines, fmt.Sprintf("delta idx=%s %s", idxStr(e), dk))
			if e.Index == nil {
				t.Errorf("content_block_delta with nil index (%s)", dk)
				continue
			}
			if !open[*e.Index] {
				t.Errorf("BUG: content_block_delta idx=%d for a block that was never opened / already closed (%s)", *e.Index, dk)
			}

		case AnthropicStreamEventTypeContentBlockStop:
			seqLines = append(seqLines, fmt.Sprintf("STOP  idx=%s", idxStr(e)))
			if e.Index == nil {
				t.Errorf("content_block_stop with nil index")
				continue
			}
			if !open[*e.Index] {
				t.Errorf("BUG: content_block_stop idx=%d for a block that was never opened (or double-closed) — this is the exact 'Content block not found' failure", *e.Index)
			}
			open[*e.Index] = false
		}
	}

	for idx, isOpen := range open {
		if isOpen {
			t.Errorf("BUG: content_block idx=%d left OPEN (no content_block_stop)", idx)
		}
	}
	return strings.Join(seqLines, "\n")
}

func TestAdvisorStream_BlockIndicesWellFormed_Simple(t *testing.T) {
	emitted := runAdvisorStream(t, advisorStreamEvents)
	seqStr := assertBlockIndicesWellFormed(t, emitted)
	t.Logf("emitted content_block sequence (simple advisor turn):\n%s", seqStr)
}

// advisorStreamEventsWithThinking is the claude-cli-shaped turn: a leading
// `thinking` block (with thinking_delta + signature_delta), then the advisor
// server_tool_use + advisor_tool_result, then the assistant text. This is the
// shape captured from `claude -p --advisor` that failed on the m4 deploy while
// a bare single-message curl succeeded.
var advisorStreamEventsWithThinking = []string{
	`{"type":"message_start","message":{"model":"claude-sonnet-4-6","id":"msg_01TH","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":2500,"output_tokens":4}}}`,
	// thinking block @ upstream index 0
	`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me consult the advisor before answering."}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"c2ln"}}`,
	`{"type":"content_block_stop","index":0}`,
	// advisor server_tool_use @ upstream index 1
	`{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu_01TH","name":"advisor","input":{}}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`,
	`{"type":"content_block_stop","index":1}`,
	// advisor_tool_result @ upstream index 2
	`{"type":"content_block_start","index":2,"content_block":{"type":"advisor_tool_result","tool_use_id":"srvtoolu_01TH","content":{"type":"advisor_result","text":"Prefer a channel-based shutdown."}}}`,
	`{"type":"content_block_stop","index":2}`,
	// assistant text @ upstream index 3
	`{"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"OK"}}`,
	`{"type":"content_block_stop","index":3}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`,
	`{"type":"message_stop"}`,
}

func TestAdvisorStream_BlockIndicesWellFormed_WithThinking(t *testing.T) {
	emitted := runAdvisorStream(t, advisorStreamEventsWithThinking)
	seqStr := assertBlockIndicesWellFormed(t, emitted)
	t.Logf("emitted content_block sequence (thinking + advisor turn):\n%s", seqStr)
}
