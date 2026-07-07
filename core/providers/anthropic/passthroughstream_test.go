package anthropic

import (
	"fmt"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// This file reproduces and guards the Claude Code advisor/server-tool streaming
// bug: on the Anthropic passthrough path (gated by IsClaudeCodeRequest), raw
// upstream frames are forwarded verbatim (upstream content-block indices) while
// the converter re-synthesizes server-tool result blocks (its own re-numbered
// indices). Mixing the two schemes made a strict client (Claude Code) see a
// content_block_stop/_delta for an index that no content_block_start opened,
// failing the turn with "API Error: Content block not found".
//
// The fix lives in the transport (transports/bifrost-http/integrations/anthropic.go
// mustConvertInPassthrough): server-tool frames and every output_item.added are
// rendered by the converter instead of forwarded raw, keeping the converter's
// block-index allocation authoritative and in lockstep with the surrounding raw
// frames. The passthroughMustConvert helper below mirrors that rule; the transport
// package's TestMustConvertInPassthrough pins the real function against it.

// --- upstream Anthropic SSE fixture builders (indices match a real stream) ---

func ptMsgStart() string {
	return `{"type":"message_start","message":{"model":"claude-sonnet-4-6","id":"msg_019M","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":999,"output_tokens":35}}}`
}
func ptMsgEnd() []string {
	return []string{
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3777}}`,
		`{"type":"message_stop"}`,
	}
}
func ptThinking(i int) []string {
	return []string{
		fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":""}}`, i),
		fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":"hmm"}}`, i),
		fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":"abc"}}`, i),
		fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i),
	}
}

// ptAdvisor emits a server_tool_use(advisor) at index i and its advisor_tool_result
// at index i+1 — the shape captured from a real claude -p --advisor turn.
func ptAdvisor(i int, id string) []string {
	return []string{
		fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"server_tool_use","id":%q,"name":"advisor","input":{}}}`, i, id),
		fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, i),
		fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i),
		fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"advisor_tool_result","tool_use_id":%q,"content":{"type":"advisor_result","text":"graceful means X."}}}`, i+1, id),
		fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i+1),
	}
}
func ptWebSearch(i int, id string) []string {
	return []string{
		fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"server_tool_use","id":%q,"name":"web_search","input":{"query":"cats"}}}`, i, id),
		fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i),
		fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"web_search_tool_result","tool_use_id":%q,"content":[{"type":"web_search_result","url":"https://x.com","title":"X","encrypted_content":"e"}]}}`, i+1, id),
		fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i+1),
	}
}

// ptWebSearchNoResults is a web_search whose result block carries no sources
// (empty results / error case). Upstream still emits the web_search_tool_result
// block, consuming an index — so the reverse converter must still advance its
// block counter even though it emits no result content.
func ptWebSearchNoResults(i int, id string) []string {
	return []string{
		fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"server_tool_use","id":%q,"name":"web_search","input":{"query":"cats"}}}`, i, id),
		fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i),
		fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"web_search_tool_result","tool_use_id":%q,"content":[]}}`, i+1, id),
		fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i+1),
	}
}
func ptWebFetch(i int, id string) []string {
	return []string{
		fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"server_tool_use","id":%q,"name":"web_fetch","input":{"url":"https://x.com"}}}`, i, id),
		fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, i),
		fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i),
		fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"web_fetch_tool_result","tool_use_id":%q,"content":{"type":"web_fetch_result","url":"https://x.com","content":{"type":"text","text":"hi"}}}}`, i+1, id),
		fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i+1),
	}
}
func ptCodeExec(i int, id string) []string {
	return []string{
		fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"server_tool_use","id":%q,"name":"bash_code_execution","input":{}}}`, i, id),
		fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}`, i),
		fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i),
		fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"bash_code_execution_tool_result","tool_use_id":%q,"content":{"type":"bash_code_execution_result","stdout":"out","stderr":"","return_code":0}}}`, i+1, id),
		fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i+1),
	}
}
func ptFuncTool(i int, id, name string) []string {
	return []string{
		fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":%q,"name":%q,"input":{}}}`, i, id, name),
		fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":"{\"x\":1}"}}`, i),
		fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i),
	}
}
func ptComputer(i int, id string) []string {
	return []string{
		fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":%q,"name":"computer","input":{}}}`, i, id),
		fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":"{\"action\":\"screenshot\"}"}}`, i),
		fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i),
	}
}
func ptText(i int) []string {
	return []string{
		fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, i),
		fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":"OK"}}`, i),
		fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i),
	}
}
func ptConcat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// passthroughMustConvert mirrors transports/bifrost-http/integrations/anthropic.go
// mustConvertInPassthrough. Kept in sync by that package's TestMustConvertInPassthrough.
func passthroughMustConvert(r *schemas.BifrostResponsesStreamResponse) bool {
	switch r.Type {
	case schemas.ResponsesStreamResponseTypeOutputItemAdded:
		return true
	case schemas.ResponsesStreamResponseTypeOutputItemDone:
		if r.Item == nil || r.Item.Type == nil {
			return false
		}
		switch *r.Item.Type {
		case schemas.ResponsesMessageTypeAdvisorCall,
			schemas.ResponsesMessageTypeWebSearchCall,
			schemas.ResponsesMessageTypeWebFetchCall,
			schemas.ResponsesMessageTypeCodeInterpreterCall:
			return true
		}
		return false
	case schemas.ResponsesStreamResponseTypeWebSearchCallInProgress,
		schemas.ResponsesStreamResponseTypeWebSearchCallSearching,
		schemas.ResponsesStreamResponseTypeWebSearchCallCompleted,
		schemas.ResponsesStreamResponseTypeWebSearchCallResultsAdded,
		schemas.ResponsesStreamResponseTypeWebSearchCallResultsCompleted,
		schemas.ResponsesStreamResponseTypeWebFetchCallInProgress,
		schemas.ResponsesStreamResponseTypeWebFetchCallFetching,
		schemas.ResponsesStreamResponseTypeWebFetchCallCompleted,
		schemas.ResponsesStreamResponseTypeCodeInterpreterCallInProgress,
		schemas.ResponsesStreamResponseTypeCodeInterpreterCallInterpreting,
		schemas.ResponsesStreamResponseTypeCodeInterpreterCallCompleted,
		schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDelta,
		schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDone:
		return true
	}
	return false
}

type ptFrame struct {
	typ string
	idx int
	via string
}

// runAnthropicPassthrough drives the real pipeline: each upstream Anthropic SSE
// frame is converted to bifrost stream responses (ToBifrostResponsesStream); the
// raw upstream frame is attached to exactly one response (response.created, else
// the last — mirroring core/providers/anthropic/anthropic.go's stream loop); then
// the transport passthrough decision picks raw vs converter for each response.
// When applyFix is false it models the pre-fix transport (raw whenever present,
// except ContentPartAdded) to prove the bug reproduces.
func runAnthropicPassthrough(t *testing.T, raws []string, applyFix bool) ([]ptFrame, *schemas.BifrostContext) {
	t.Helper()
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	// This harness always models the passthrough path (raw frames interleaved), so
	// mark the reverse converter accordingly — mirroring the transport, which calls
	// SetResponsesStreamPassthrough when shouldUsePassthrough is true. This drives
	// the server-tool result-block index bumps that keep converted frames in
	// lockstep with the raw ones; the all-normalized path (TestAnthropicConverterOnly_*)
	// leaves it unset so indices stay contiguous.
	SetResponsesStreamPassthrough(ctx)
	state := newAdvisorStreamState()
	var out []ptFrame
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
		rawIdx := len(responses) - 1
		for j, r := range responses {
			if r != nil && r.Type == schemas.ResponsesStreamResponseTypeCreated {
				rawIdx = j
				break
			}
		}
		for i, r := range responses {
			seq++
			useRaw := i == rawIdx && r.Type != schemas.ResponsesStreamResponseTypeContentPartAdded
			if applyFix && passthroughMustConvert(r) {
				useRaw = false
			}
			if useRaw {
				typ := gjson.Get(raw, "type").String()
				if typ == "" {
					continue
				}
				idx := -1
				if g := gjson.Get(raw, "index"); g.Exists() {
					idx = int(g.Int())
				}
				out = append(out, ptFrame{typ: typ, idx: idx, via: "raw"})
				continue
			}
			for _, e := range ToAnthropicResponsesStreamResponse(ctx, r) {
				idx := -1
				if e.Index != nil {
					idx = *e.Index
				}
				out = append(out, ptFrame{typ: string(e.Type), idx: idx, via: "conv"})
			}
		}
	}
	return out, ctx
}

// blockFramingProblems models a strict SSE consumer (Claude Code): every
// content_block_delta / content_block_stop must reference an index opened by a
// prior content_block_start, no index may be opened twice, every opened block must
// be closed.
func blockFramingProblems(t *testing.T, frames []ptFrame, dump bool) []string {
	t.Helper()
	var problems []string
	open := map[int]bool{}
	for i, f := range frames {
		if dump {
			t.Logf("  [%02d] %-24s index=%-3d (%s)", i, f.typ, f.idx, f.via)
		}
		switch f.typ {
		case "content_block_start":
			if open[f.idx] {
				problems = append(problems, fmt.Sprintf("double content_block_start for index %d", f.idx))
			}
			open[f.idx] = true
		case "content_block_delta":
			if !open[f.idx] {
				problems = append(problems, fmt.Sprintf("content_block_delta for unopened index %d", f.idx))
			}
		case "content_block_stop":
			if !open[f.idx] {
				problems = append(problems, fmt.Sprintf("content_block_stop for unopened index %d", f.idx))
			}
			delete(open, f.idx)
		}
	}
	for idx := range open {
		problems = append(problems, fmt.Sprintf("index %d opened but never stopped", idx))
	}
	return problems
}

func TestAnthropicPassthrough_ServerToolIndexConsistency(t *testing.T) {
	scenarios := map[string][]string{
		"thinking+advisor+text":  ptConcat([]string{ptMsgStart()}, ptThinking(0), ptAdvisor(1, "srv_A"), ptText(3), ptMsgEnd()),
		"advisor_first":          ptConcat([]string{ptMsgStart()}, ptAdvisor(0, "srv_A"), ptText(2), ptMsgEnd()),
		"two_advisors":           ptConcat([]string{ptMsgStart()}, ptThinking(0), ptAdvisor(1, "srv_A"), ptAdvisor(3, "srv_B"), ptText(5), ptMsgEnd()),
		"websearch":              ptConcat([]string{ptMsgStart()}, ptThinking(0), ptWebSearch(1, "srv_W"), ptText(3), ptMsgEnd()),
		"websearch_no_results":   ptConcat([]string{ptMsgStart()}, ptThinking(0), ptWebSearchNoResults(1, "srv_W"), ptText(3), ptMsgEnd()),
		"two_websearch":          ptConcat([]string{ptMsgStart()}, ptWebSearch(0, "srv_W"), ptWebSearch(2, "srv_W2"), ptText(4), ptMsgEnd()),
		"web_fetch":              ptConcat([]string{ptMsgStart()}, ptThinking(0), ptWebFetch(1, "srv_F"), ptText(3), ptMsgEnd()),
		"advisor_then_web_fetch": ptConcat([]string{ptMsgStart()}, ptAdvisor(0, "srv_A"), ptWebFetch(2, "srv_F"), ptText(4), ptMsgEnd()),
		"two_web_fetch":          ptConcat([]string{ptMsgStart()}, ptWebFetch(0, "srv_F"), ptWebFetch(2, "srv_F2"), ptText(4), ptMsgEnd()),
		"code_execution":         ptConcat([]string{ptMsgStart()}, ptThinking(0), ptCodeExec(1, "srv_C"), ptText(3), ptMsgEnd()),
		"func_then_advisor":      ptConcat([]string{ptMsgStart()}, ptFuncTool(0, "toolu_1", "Read"), ptAdvisor(1, "srv_A"), ptText(3), ptMsgEnd()),
		"advisor_then_func":      ptConcat([]string{ptMsgStart()}, ptAdvisor(0, "srv_A"), ptFuncTool(2, "toolu_1", "Read"), ptText(3), ptMsgEnd()),
		"advisor_then_websearch": ptConcat([]string{ptMsgStart()}, ptAdvisor(0, "srv_A"), ptWebSearch(2, "srv_W"), ptText(4), ptMsgEnd()),
		"computer":               ptConcat([]string{ptMsgStart()}, ptThinking(0), ptComputer(1, "toolu_C"), ptText(2), ptMsgEnd()),
		"plain_text":             ptConcat([]string{ptMsgStart()}, ptText(0), ptMsgEnd()),
	}
	for name, raws := range scenarios {
		t.Run(name, func(t *testing.T) {
			frames, ctx := runAnthropicPassthrough(t, raws, true)
			if fixed := blockFramingProblems(t, frames, true); len(fixed) > 0 {
				t.Errorf("passthrough stream is inconsistent after fix: %v", fixed)
			}
			// The fix's mechanism: on the passthrough path every output_item.added is
			// routed through the converter so allocBlockIndex always runs — so
			// blockIndexFor must never miss (a miss = a stop/delta for a block whose
			// start was never registered).
			if misses := getOrCreateAnthropicToResponsesStreamState(ctx).blockIndexMisses; len(misses) > 0 {
				t.Errorf("reverse converter recorded block-index misses on the passthrough path: %v", misses)
			}
		})
	}
}

// TestAnthropicPassthrough_ReproducesBugWithoutFix asserts the pre-fix transport
// behavior (forward raw whenever present) produces exactly the doc's failure: a
// content_block_stop for an index the client never opened. This guards the fix
// from silently regressing to a test that passes vacuously.
func TestAnthropicPassthrough_ReproducesBugWithoutFix(t *testing.T) {
	stream := ptConcat([]string{ptMsgStart()}, ptThinking(0), ptAdvisor(1, "srv_A"), ptText(3), ptMsgEnd())

	buggyFrames, _ := runAnthropicPassthrough(t, stream, false)
	buggy := blockFramingProblems(t, buggyFrames, false)
	if len(buggy) == 0 {
		t.Fatal("expected the pre-fix passthrough to produce an inconsistent stream, but it was clean")
	}
	foundStopUnopened := false
	for _, p := range buggy {
		if p == "content_block_stop for unopened index 2" {
			foundStopUnopened = true
		}
	}
	if !foundStopUnopened {
		t.Errorf("expected the documented failure (stop for unopened index 2); got %v", buggy)
	}

	// The same stream must be clean once the fix routes server-tool frames through
	// the converter.
	fixedFrames, _ := runAnthropicPassthrough(t, stream, true)
	if fixed := blockFramingProblems(t, fixedFrames, false); len(fixed) > 0 {
		t.Errorf("fix did not resolve the inconsistency: %v", fixed)
	}
}

// TestAnthropicConverterOnlyStream_WellFormed covers the all-frames-through-the-
// converter path — i.e. non-Claude-Code clients (plain curl / OpenAI-format), which
// never take the verbatim-passthrough route. It asserts two things over a rich
// server-tool stream: (1) the emitted Anthropic SSE satisfies the content_block
// open/close invariant (every stop/delta references an opened start; nothing left
// open), and (2) blockIndexFor never misses (blockIndexMisses stays empty), i.e. the
// reverse converter's allocator stays in lockstep with itself when it drives every
// frame.
func TestAnthropicConverterOnlyStream_WellFormed(t *testing.T) {
	stream := ptConcat([]string{ptMsgStart()}, ptThinking(0), ptAdvisor(1, "srv_A"), ptWebSearch(3, "srv_W"), ptWebFetch(5, "srv_F"), ptText(7), ptMsgEnd())
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	state := newAdvisorStreamState()
	var frames []ptFrame
	seq := 0
	for _, raw := range stream {
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
			for _, e := range ToAnthropicResponsesStreamResponse(ctx, r) {
				idx := -1
				if e.Index != nil {
					idx = *e.Index
				}
				frames = append(frames, ptFrame{typ: string(e.Type), idx: idx, via: "conv"})
			}
		}
	}
	if problems := blockFramingProblems(t, frames, false); len(problems) > 0 {
		t.Errorf("converter-only stream is not well-formed: %v", problems)
	}
	rs := getOrCreateAnthropicToResponsesStreamState(ctx)
	if len(rs.blockIndexMisses) > 0 {
		t.Errorf("reverse converter recorded block-index misses (stop/delta for unregistered blocks): %v", rs.blockIndexMisses)
	}
}

// TestAnthropicConverterOnly_IndicesContiguous guards the all-normalized path
// (OpenAI-via-Anthropic, curl — never Claude Code): with no interleaved raw frames,
// SetResponsesStreamPassthrough is NOT called, so the converter must emit contiguous
// content_block indices 0,1,2,… A gap (an index consumed for a hidden server-tool
// result block but no block emitted) is what a strict Anthropic SDK client sees as a
// missing block; native Anthropic never produces one. Zero-result web_search is the
// case that still collapses a result block — anti-vacuous: before the passthrough
// gating it emitted [0,1,3].
func TestAnthropicConverterOnly_IndicesContiguous(t *testing.T) {
	scenarios := map[string][]string{
		"web_fetch":              ptConcat([]string{ptMsgStart()}, ptThinking(0), ptWebFetch(1, "srv_F"), ptText(3), ptMsgEnd()),
		"websearch_no_results":   ptConcat([]string{ptMsgStart()}, ptThinking(0), ptWebSearchNoResults(1, "srv_W"), ptText(3), ptMsgEnd()),
		"websearch_with_results": ptConcat([]string{ptMsgStart()}, ptThinking(0), ptWebSearch(1, "srv_W"), ptText(3), ptMsgEnd()),
		"two_web_fetch":          ptConcat([]string{ptMsgStart()}, ptWebFetch(0, "srv_F"), ptWebFetch(2, "srv_F2"), ptText(4), ptMsgEnd()),
		"web_fetch_then_search":  ptConcat([]string{ptMsgStart()}, ptWebFetch(0, "srv_F"), ptWebSearchNoResults(2, "srv_W"), ptText(4), ptMsgEnd()),
	}
	for name, raws := range scenarios {
		t.Run(name, func(t *testing.T) {
			// No SetResponsesStreamPassthrough: this is the all-normalized path.
			ctx := schemas.NewBifrostContext(nil, time.Time{})
			state := newAdvisorStreamState()
			seq := 0
			var starts []int
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
					for _, e := range ToAnthropicResponsesStreamResponse(ctx, r) {
						if e.Type == AnthropicStreamEventTypeContentBlockStart && e.Index != nil {
							starts = append(starts, *e.Index)
						}
					}
				}
			}
			for i, idx := range starts {
				if idx != i {
					t.Errorf("non-contiguous content_block_start indices %v (position %d is index %d, not %d); native Anthropic never skips an index", starts, i, idx, i)
					break
				}
			}
		})
	}
}

func TestAnthropicWebFetchResultRoundTrip(t *testing.T) {
	toolID := "srvtoolu_fetch"
	result := &AnthropicContentBlock{
		Type:      AnthropicContentBlockTypeWebFetchToolResult,
		ToolUseID: &toolID,
		Content: &AnthropicContent{ContentObj: &AnthropicContentBlock{
			Type:        "web_fetch_result",
			URL:         schemas.Ptr("https://example.com/article"),
			RetrievedAt: schemas.Ptr("2026-07-06T09:00:28Z"),
			Content: &AnthropicContent{ContentObj: &AnthropicContentBlock{
				Type:  AnthropicContentBlockTypeDocument,
				Title: schemas.Ptr("Example Article"),
				Source: &AnthropicBlockSource{SourceObj: &AnthropicSource{
					Type:      "text",
					MediaType: schemas.Ptr("text/plain"),
					Data:      schemas.Ptr("Full text content"),
				}},
				Citations: &AnthropicCitations{Config: &schemas.Citations{Enabled: schemas.Ptr(true)}},
			}},
		}},
	}

	carry := convertAnthropicWebFetchResultToBifrost(result)
	if carry == nil || carry.Document == nil || carry.Document.Source == nil {
		t.Fatalf("expected typed web_fetch result payload, got %#v", carry)
	}
	if got := *carry.Document.Source.Data; got != "Full text content" {
		t.Fatalf("unexpected carried document data: %q", got)
	}

	msg := &schemas.ResponsesMessage{
		ID:     &toolID,
		Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebFetchCall),
		Status: schemas.Ptr("completed"),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: &toolID,
			Action: &schemas.ResponsesToolMessageActionStruct{
				ResponsesWebFetchToolCallAction: &schemas.ResponsesWebFetchToolCallAction{
					Type: "fetch",
					URL:  "https://example.com/article",
				},
			},
			ResponsesWebFetchCall: carry,
		},
	}

	blocks := convertBifrostWebFetchCallToAnthropicBlocks(msg)
	if len(blocks) != 2 {
		t.Fatalf("expected server_tool_use + web_fetch_tool_result, got %d blocks: %#v", len(blocks), blocks)
	}
	if blocks[1].Type != AnthropicContentBlockTypeWebFetchToolResult {
		t.Fatalf("expected web_fetch_tool_result, got %q", blocks[1].Type)
	}
	inner := getAnthropicContentObject(blocks[1].Content)
	if inner == nil || inner.URL == nil || *inner.URL != "https://example.com/article" {
		t.Fatalf("expected rebuilt fetch result URL, got %#v", inner)
	}
	doc := getAnthropicContentObject(inner.Content)
	if doc == nil || doc.Source == nil || doc.Source.SourceObj == nil || doc.Source.SourceObj.Data == nil {
		t.Fatalf("expected rebuilt document source, got %#v", doc)
	}
	if got := *doc.Source.SourceObj.Data; got != "Full text content" {
		t.Fatalf("unexpected rebuilt document data: %q", got)
	}
}

func TestAnthropicWebFetchTextResultRoundTrip(t *testing.T) {
	toolID := "srvtoolu_fetch_text"
	result := &AnthropicContentBlock{
		Type:      AnthropicContentBlockTypeWebFetchToolResult,
		ToolUseID: &toolID,
		Content: &AnthropicContent{ContentObj: &AnthropicContentBlock{
			Type: "web_fetch_result",
			URL:  schemas.Ptr("https://example.com/plain"),
			Content: &AnthropicContent{ContentObj: &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeText,
				Text: schemas.Ptr("plain fetched text"),
			}},
		}},
	}

	carry := convertAnthropicWebFetchResultToBifrost(result)
	if carry == nil || carry.Document == nil || carry.Document.Text == nil {
		t.Fatalf("expected typed text web_fetch result payload, got %#v", carry)
	}
	if got := *carry.Document.Text; got != "plain fetched text" {
		t.Fatalf("unexpected carried text: %q", got)
	}

	msg := &schemas.ResponsesMessage{
		ID:     &toolID,
		Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebFetchCall),
		Status: schemas.Ptr("completed"),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:                &toolID,
			ResponsesWebFetchCall: carry,
		},
	}

	blocks := convertBifrostWebFetchCallToAnthropicBlocks(msg)
	if len(blocks) != 2 {
		t.Fatalf("expected server_tool_use + web_fetch_tool_result, got %d blocks: %#v", len(blocks), blocks)
	}
	inner := getAnthropicContentObject(blocks[1].Content)
	if inner == nil || inner.Content == nil {
		t.Fatalf("expected rebuilt fetch result content, got %#v", inner)
	}
	textBlock := getAnthropicContentObject(inner.Content)
	if textBlock == nil || textBlock.Text == nil || *textBlock.Text != "plain fetched text" {
		t.Fatalf("expected rebuilt text content, got %#v", textBlock)
	}
}

func TestAnthropicWebFetchPassthroughNoResultConsumesHiddenIndex(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	SetResponsesStreamPassthrough(ctx)

	toolID := "srvtoolu_fetch_missing_result"
	textID := "msg_after_fetch"
	addFetch := &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
		OutputIndex: schemas.Ptr(0),
		Item: &schemas.ResponsesMessage{
			ID:     &toolID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebFetchCall),
			Status: schemas.Ptr("in_progress"),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: &toolID,
				Action: &schemas.ResponsesToolMessageActionStruct{
					ResponsesWebFetchToolCallAction: &schemas.ResponsesWebFetchToolCallAction{
						Type: "fetch",
						URL:  "https://example.com",
					},
				},
			},
		},
	}
	doneFetch := &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemDone,
		OutputIndex: schemas.Ptr(0),
		Item: &schemas.ResponsesMessage{
			ID:     &toolID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebFetchCall),
			Status: schemas.Ptr("completed"),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: &toolID,
			},
		},
	}
	addText := &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
		OutputIndex: schemas.Ptr(1),
		Item: &schemas.ResponsesMessage{
			ID:      &textID,
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("after")},
		},
	}

	_ = ToAnthropicResponsesStreamResponse(ctx, addFetch)
	_ = ToAnthropicResponsesStreamResponse(ctx, doneFetch)
	events := ToAnthropicResponsesStreamResponse(ctx, addText)
	if len(events) == 0 || events[0].Index == nil {
		t.Fatalf("expected text start event with index, got %#v", events)
	}
	if got := *events[0].Index; got != 2 {
		t.Fatalf("expected hidden web_fetch result index to be consumed before next block; got index %d", got)
	}
}
