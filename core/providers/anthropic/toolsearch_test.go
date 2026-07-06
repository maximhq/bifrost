package anthropic

import (
	"strings"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"

	"github.com/bytedance/sonic"
)

// toolSearchStreamEvents mirrors a Claude tool_search_tool_bm25 turn: the model
// invokes the server-run tool_search tool, Anthropic returns the discovered tool
// references in a tool_search_tool_result block, then the model answers using one
// of the discovered tools. Regression fixture for #4780: this result was
// previously dropped entirely on the /v1/responses streaming path.
var toolSearchStreamEvents = []string{
	`{"type":"message_start","message":{"model":"claude-opus-4-8","id":"msg_ts1","type":"message","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_ts1","name":"tool_search_tool_bm25","input":{}}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\": \"weather"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"}"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_ts1","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"get_weather"},{"type":"tool_reference","tool_name":"get_forecast"}]}}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"I found the get_weather tool."}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`,
	`{"type":"message_stop"}`,
}

// TestToolSearch_Stream verifies the streaming converter surfaces the
// tool_search_tool_result on the /v1/responses path instead of dropping it: an
// output_item.added (tool_search_call, in_progress) is emitted for the
// server_tool_use, and an output_item.done (tool_search_call, completed) carrying
// the discovered tool names is emitted once the result block closes.
func TestToolSearch_Stream(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	state := AcquireAnthropicResponsesStreamState()
	defer ReleaseAnthropicResponsesStreamState(state)

	var emitted []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for _, raw := range toolSearchStreamEvents {
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
			emitted = append(emitted, r)
		}
	}

	var sawAdded, sawDone bool
	for _, e := range emitted {
		switch e.Type {
		case schemas.ResponsesStreamResponseTypeOutputItemAdded:
			if e.Item != nil && e.Item.Type != nil && *e.Item.Type == schemas.ResponsesMessageTypeAnthropicToolSearchCall {
				sawAdded = true
				if e.Item.Status == nil || *e.Item.Status != "in_progress" {
					t.Errorf("added item status = %v, want in_progress", e.Item.Status)
				}
			}
		case schemas.ResponsesStreamResponseTypeOutputItemDone:
			if e.Item == nil || e.Item.Type == nil || *e.Item.Type != schemas.ResponsesMessageTypeAnthropicToolSearchCall {
				continue
			}
			sawDone = true
			if e.Item.Status == nil || *e.Item.Status != "completed" {
				t.Errorf("done item status = %v, want completed", e.Item.Status)
			}
			tm := e.Item.ResponsesToolMessage
			if tm == nil || tm.Output == nil || tm.Output.ResponsesToolCallOutputStr == nil {
				t.Fatal("output_item.done tool_search_call missing Output")
			}
			out := *tm.Output.ResponsesToolCallOutputStr
			if !strings.Contains(out, "get_weather") || !strings.Contains(out, "get_forecast") {
				t.Errorf("output = %q, want it to contain both discovered tool names", out)
			}
		}
	}

	if !sawAdded {
		t.Error("expected an output_item.added for the tool_search_call, got none — the call was dropped")
	}
	if !sawDone {
		t.Error("expected an output_item.done for the tool_search_call carrying discovered tool references, got none — the result was dropped (#4780)")
	}

	// response.completed's Output array is built solely from state.OutputItems
	// (see AnthropicStreamEventTypeMessageStop) — verify the tool_search_call
	// actually persisted there, not just in the individual stream events.
	var sawInCompleted bool
	for _, r := range emitted {
		if r.Type != schemas.ResponsesStreamResponseTypeCompleted || r.Response == nil {
			continue
		}
		for _, out := range r.Response.Output {
			if out.Type != nil && *out.Type == schemas.ResponsesMessageTypeAnthropicToolSearchCall {
				sawInCompleted = true
			}
		}
	}
	if !sawInCompleted {
		t.Error("expected the tool_search_call to be present in response.completed's Output array, got none — it would be missing from the final response")
	}
}

// multiToolSearchStreamEvents reproduces a real event sequence observed against the
// live Anthropic API: the model issues three tool_search_tool_bm25 calls whose call
// blocks are ALL emitted before any of their result blocks arrive — the call/result
// blocks are not interleaved 1:1 per call. A single-slot state design (tracking only
// "the current" tool_use ID) drops every result except the last, leaving earlier
// calls stuck "in_progress" forever.
var multiToolSearchStreamEvents = []string{
	`{"type":"message_start","message":{"model":"claude-opus-4-8","id":"msg_multi1","type":"message","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_a","name":"tool_search_tool_bm25","input":{}}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu_b","name":"tool_search_tool_bm25","input":{}}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"server_tool_use","id":"srvtoolu_c","name":"tool_search_tool_bm25","input":{}}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"content_block_start","index":3,"content_block":{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_a","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"tool_a"}]}}}`,
	`{"type":"content_block_stop","index":3}`,
	`{"type":"content_block_start","index":4,"content_block":{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_b","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"tool_b"}]}}}`,
	`{"type":"content_block_stop","index":4}`,
	`{"type":"content_block_start","index":5,"content_block":{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_c","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"tool_c"}]}}}`,
	`{"type":"content_block_stop","index":5}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`,
	`{"type":"message_stop"}`,
}

// TestToolSearch_MultipleCallsInOneTurn is a regression test for a bug found while
// manually verifying #4780 against the live Anthropic API: a single-slot
// (non-map-keyed) state design silently drops every tool_search result except the
// last when a turn contains multiple calls whose blocks aren't interleaved 1:1.
func TestToolSearch_MultipleCallsInOneTurn(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	state := AcquireAnthropicResponsesStreamState()
	defer ReleaseAnthropicResponsesStreamState(state)

	var emitted []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for _, raw := range multiToolSearchStreamEvents {
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
			emitted = append(emitted, r)
		}
	}

	completedOutputs := map[string]string{} // call_id -> output
	for _, e := range emitted {
		if e.Type != schemas.ResponsesStreamResponseTypeOutputItemDone {
			continue
		}
		if e.Item == nil || e.Item.Type == nil || *e.Item.Type != schemas.ResponsesMessageTypeAnthropicToolSearchCall {
			continue
		}
		if e.Item.ResponsesToolMessage == nil || e.Item.ResponsesToolMessage.CallID == nil {
			continue
		}
		if e.Item.Status == nil || *e.Item.Status != "completed" {
			t.Errorf("call %s: status = %v, want completed", *e.Item.ResponsesToolMessage.CallID, e.Item.Status)
			continue
		}
		if e.Item.ResponsesToolMessage.Output == nil || e.Item.ResponsesToolMessage.Output.ResponsesToolCallOutputStr == nil {
			t.Errorf("call %s: missing Output", *e.Item.ResponsesToolMessage.CallID)
			continue
		}
		completedOutputs[*e.Item.ResponsesToolMessage.CallID] = *e.Item.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
	}

	for callID, wantTool := range map[string]string{
		"srvtoolu_a": "tool_a",
		"srvtoolu_b": "tool_b",
		"srvtoolu_c": "tool_c",
	} {
		out, ok := completedOutputs[callID]
		if !ok {
			t.Errorf("call %s: never completed — stuck in_progress forever (all-calls-before-results ordering not handled)", callID)
			continue
		}
		if !strings.Contains(out, wantTool) {
			t.Errorf("call %s: output = %q, want it to contain %q", callID, out, wantTool)
		}
	}

	// Every one of the three calls must also appear completed in response.completed's
	// Output array — not just in the individual output_item.done stream events.
	completedInFinal := map[string]bool{}
	for _, r := range emitted {
		if r.Type != schemas.ResponsesStreamResponseTypeCompleted || r.Response == nil {
			continue
		}
		for _, out := range r.Response.Output {
			if out.Type == nil || *out.Type != schemas.ResponsesMessageTypeAnthropicToolSearchCall {
				continue
			}
			if out.Status != nil && *out.Status == "completed" && out.ResponsesToolMessage != nil && out.ResponsesToolMessage.CallID != nil {
				completedInFinal[*out.ResponsesToolMessage.CallID] = true
			}
		}
	}
	for _, callID := range []string{"srvtoolu_a", "srvtoolu_b", "srvtoolu_c"} {
		if !completedInFinal[callID] {
			t.Errorf("call %s: not present as completed in response.completed's Output array", callID)
		}
	}
}
