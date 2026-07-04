package anthropic

import (
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"

	"github.com/bytedance/sonic"
)

// webSearchStreamEvents mirrors a Claude web_search turn: server_tool_use ->
// web_search_tool_result -> text answer. Regression fixture for the same bug
// class as #4780 (tool_search): the web_search_call item was built and emitted
// correctly on the wire, but never persisted into state.OutputItems, so it was
// silently missing from response.completed's Output array.
var webSearchStreamEvents = []string{
	`{"type":"message_start","message":{"model":"claude-opus-4-8","id":"msg_ws1","type":"message","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_ws1","name":"web_search","input":{"query":"anthropic founding year"}}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_ws1","content":[{"type":"web_search_result","url":"https://example.com/anthropic","title":"Anthropic"}]}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Anthropic was founded in 2021."}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`,
	`{"type":"message_stop"}`,
}

// TestWebSearch_PersistedInOutputItems verifies that web_search_call is present
// in response.completed's Output array, not just in the individual streaming
// events — mirrors TestToolSearch_Stream's response.completed assertion.
func TestWebSearch_PersistedInOutputItems(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	state := AcquireAnthropicResponsesStreamState()
	defer ReleaseAnthropicResponsesStreamState(state)

	var emitted []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for _, raw := range webSearchStreamEvents {
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

	var sawDone bool
	for _, e := range emitted {
		if e.Type != schemas.ResponsesStreamResponseTypeOutputItemDone {
			continue
		}
		if e.Item == nil || e.Item.Type == nil || *e.Item.Type != schemas.ResponsesMessageTypeWebSearchCall {
			continue
		}
		sawDone = true
		if e.Item.Status == nil || *e.Item.Status != "completed" {
			t.Errorf("done item status = %v, want completed", e.Item.Status)
		}
	}
	if !sawDone {
		t.Fatal("expected an output_item.done for the web_search_call, got none")
	}

	var sawInCompleted bool
	for _, r := range emitted {
		if r.Type != schemas.ResponsesStreamResponseTypeCompleted || r.Response == nil {
			continue
		}
		for _, out := range r.Response.Output {
			if out.Type != nil && *out.Type == schemas.ResponsesMessageTypeWebSearchCall {
				sawInCompleted = true
				if out.Status == nil || *out.Status != "completed" {
					t.Errorf("web_search_call in response.completed has status = %v, want completed", out.Status)
				}
				// The persisted item must carry the finalized action (query + sources),
				// not just a bare type/status — a partial-persistence regression could
				// otherwise leave a "completed" item with an empty action.
				tm := out.ResponsesToolMessage
				if tm == nil || tm.Action == nil || tm.Action.ResponsesWebSearchToolCallAction == nil {
					t.Fatal("web_search_call in response.completed is missing its Action")
				}
				action := tm.Action.ResponsesWebSearchToolCallAction
				if action.Query == nil || *action.Query != "anthropic founding year" {
					t.Errorf("persisted action.Query = %v, want %q", action.Query, "anthropic founding year")
				}
				if len(action.Sources) != 1 || action.Sources[0].URL != "https://example.com/anthropic" {
					t.Errorf("persisted action.Sources = %v, want one source with the result URL", action.Sources)
				}
			}
		}
	}
	if !sawInCompleted {
		t.Error("expected the web_search_call to be present in response.completed's Output array, got none — it would be missing from the final response")
	}
}

// multiWebSearchStreamEvents reproduces the same all-calls-before-results ordering
// hazard fixed for tool_search (#4780): three web_search calls are all streamed
// before any of their results arrive.
var multiWebSearchStreamEvents = []string{
	`{"type":"message_start","message":{"model":"claude-opus-4-8","id":"msg_multiws1","type":"message","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_wsa","name":"web_search","input":{"query":"query a"}}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu_wsb","name":"web_search","input":{"query":"query b"}}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"server_tool_use","id":"srvtoolu_wsc","name":"web_search","input":{"query":"query c"}}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"content_block_start","index":3,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_wsa","content":[{"type":"web_search_result","url":"https://example.com/a","title":"A"}]}}`,
	`{"type":"content_block_stop","index":3}`,
	`{"type":"content_block_start","index":4,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_wsb","content":[{"type":"web_search_result","url":"https://example.com/b","title":"B"}]}}`,
	`{"type":"content_block_stop","index":4}`,
	`{"type":"content_block_start","index":5,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_wsc","content":[{"type":"web_search_result","url":"https://example.com/c","title":"C"}]}}`,
	`{"type":"content_block_stop","index":5}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`,
	`{"type":"message_stop"}`,
}

// TestWebSearch_MultipleCallsInOneTurn is a regression test for the same
// single-slot overwrite bug class fixed for tool_search: a turn with multiple
// web_search calls, all streamed before any result arrives, must not silently
// drop all but the last call.
func TestWebSearch_MultipleCallsInOneTurn(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	state := AcquireAnthropicResponsesStreamState()
	defer ReleaseAnthropicResponsesStreamState(state)

	var emitted []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for _, raw := range multiWebSearchStreamEvents {
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

	completedQueries := map[string]string{} // call_id -> query
	for _, e := range emitted {
		if e.Type != schemas.ResponsesStreamResponseTypeOutputItemDone {
			continue
		}
		if e.Item == nil || e.Item.Type == nil || *e.Item.Type != schemas.ResponsesMessageTypeWebSearchCall {
			continue
		}
		if e.Item.ResponsesToolMessage == nil || e.Item.ResponsesToolMessage.CallID == nil {
			continue
		}
		if e.Item.Status == nil || *e.Item.Status != "completed" {
			t.Errorf("call %s: status = %v, want completed", *e.Item.ResponsesToolMessage.CallID, e.Item.Status)
			continue
		}
		action := e.Item.ResponsesToolMessage.Action
		if action == nil || action.ResponsesWebSearchToolCallAction == nil || action.ResponsesWebSearchToolCallAction.Query == nil {
			t.Errorf("call %s: missing query in completed action", *e.Item.ResponsesToolMessage.CallID)
			continue
		}
		completedQueries[*e.Item.ResponsesToolMessage.CallID] = *action.ResponsesWebSearchToolCallAction.Query
	}

	for callID, wantQuery := range map[string]string{
		"srvtoolu_wsa": "query a",
		"srvtoolu_wsb": "query b",
		"srvtoolu_wsc": "query c",
	} {
		got, ok := completedQueries[callID]
		if !ok {
			t.Errorf("call %s: never completed — stuck in_progress forever (all-calls-before-results ordering not handled)", callID)
			continue
		}
		if got != wantQuery {
			t.Errorf("call %s: query = %q, want %q", callID, got, wantQuery)
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
			if out.Type == nil || *out.Type != schemas.ResponsesMessageTypeWebSearchCall {
				continue
			}
			if out.Status != nil && *out.Status == "completed" && out.ResponsesToolMessage != nil && out.ResponsesToolMessage.CallID != nil {
				completedInFinal[*out.ResponsesToolMessage.CallID] = true
			}
		}
	}
	for _, callID := range []string{"srvtoolu_wsa", "srvtoolu_wsb", "srvtoolu_wsc"} {
		if !completedInFinal[callID] {
			t.Errorf("call %s: not present as completed in response.completed's Output array", callID)
		}
	}
}

// streamedQueryWebSearchStreamEvents reproduces the rare case where the
// server_tool_use block arrives with empty input and the query streams in via
// input_json_delta events instead — exercises the WebSearchCallIndices
// fallback-capture path on content_block_stop, not just the common
// pre-populated-input case covered by webSearchStreamEvents above.
var streamedQueryWebSearchStreamEvents = []string{
	`{"type":"message_start","message":{"model":"claude-opus-4-8","id":"msg_ws2","type":"message","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_ws2","name":"web_search","input":{}}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\": \"streamed"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":" query\"}"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_ws2","content":[{"type":"web_search_result","url":"https://example.com/streamed","title":"Streamed"}]}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`,
	`{"type":"message_stop"}`,
}

// TestWebSearch_QueryCapturedFromStreamedInputJSON verifies the fallback path:
// when a web_search server_tool_use block arrives with empty input and the
// query only appears via input_json_delta events, WebSearchCallIndices lets
// content_block_stop resolve the right pendingWebSearch entry and capture the
// query — this is the branch TestWebSearch_MultipleCallsInOneTurn (which uses
// pre-populated input) does not exercise.
func TestWebSearch_QueryCapturedFromStreamedInputJSON(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	state := AcquireAnthropicResponsesStreamState()
	defer ReleaseAnthropicResponsesStreamState(state)

	var emitted []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for _, raw := range streamedQueryWebSearchStreamEvents {
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

	var gotQuery *string
	for _, e := range emitted {
		if e.Type != schemas.ResponsesStreamResponseTypeOutputItemDone {
			continue
		}
		if e.Item == nil || e.Item.Type == nil || *e.Item.Type != schemas.ResponsesMessageTypeWebSearchCall {
			continue
		}
		if e.Item.ResponsesToolMessage != nil && e.Item.ResponsesToolMessage.Action != nil &&
			e.Item.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction != nil {
			gotQuery = e.Item.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction.Query
		}
	}
	if gotQuery == nil || *gotQuery != "streamed query" {
		t.Errorf("query = %v, want %q — the streamed-input_json fallback capture did not run", gotQuery, "streamed query")
	}
}
