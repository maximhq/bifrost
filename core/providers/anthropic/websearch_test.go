package anthropic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestWebSearch_OutputItemAdded_StoresID verifies that a WebSearch function_call
// output_item.added event stores the item ID in the per-request stream state so that
// subsequent argument deltas can be skipped.
func TestWebSearch_OutputItemAdded_StoresID(t *testing.T) {
	t.Parallel()

	const itemID = "toolu_ws_storesid_test"

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	bifrostResp := &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
		OutputIndex: schemas.Ptr(0),
		Item: &schemas.ResponsesMessage{
			ID:   schemas.Ptr(itemID),
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    schemas.Ptr(itemID),
				Name:      schemas.Ptr("WebSearch"),
				Arguments: schemas.Ptr(""),
			},
		},
	}

	events := ToAnthropicResponsesStreamResponse(ctx, bifrostResp)

	// Should emit content_block_start
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}
	if events[0].Type != AnthropicStreamEventTypeContentBlockStart {
		t.Errorf("event[0].Type = %v, want content_block_start", events[0].Type)
	}
	if events[0].ContentBlock == nil || events[0].ContentBlock.Input == nil {
		t.Fatal("expected ContentBlock with Input")
	}
	if string(events[0].ContentBlock.Input) != "{}" {
		t.Errorf("ContentBlock.Input = %s, want {}", events[0].ContentBlock.Input)
	}

	// ID must now be tracked in per-request state
	state := getOrCreateAnthropicToResponsesStreamState(ctx)
	if !state.webSearchItemIDs[itemID] {
		t.Error("expected item ID to be stored in per-request stream state after output_item.added")
	}
}

// TestWebSearch_FunctionCallArgumentsDelta_Skipped verifies that argument deltas
// for a tracked WebSearch item are skipped (returning nil) regardless of the
// user agent — the fix for the original bug where non-Claude Code clients lost
// the query.
func TestWebSearch_FunctionCallArgumentsDelta_Skipped(t *testing.T) {
	t.Parallel()

	const itemID = "toolu_ws_skip_test"

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	// Pre-seed per-request state as if output_item.added already fired
	state := getOrCreateAnthropicToResponsesStreamState(ctx)
	state.webSearchItemIDs = map[string]bool{itemID: true}

	partial := `{"query": "world news"`
	bifrostResp := &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
		OutputIndex: new(0),
		ItemID:      new(itemID),
		Delta:       &partial,
	}

	events := ToAnthropicResponsesStreamResponse(ctx, bifrostResp)

	if len(events) != 0 {
		t.Errorf("expected deltas to be skipped (0 events), got %d", len(events))
	}
}

// TestWebSearch_OutputItemDone_GeneratesSyntheticDeltas verifies that when
// output_item.done fires for a tracked WebSearch item, synthetic input_json_delta
// events carrying the full query are emitted, followed by content_block_stop.
// This applies for ALL clients regardless of user agent.
func TestWebSearch_OutputItemDone_GeneratesSyntheticDeltas(t *testing.T) {
	t.Parallel()

	const itemID = "toolu_ws_synth_test"

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	// Pre-seed per-request state as if output_item.added already fired
	state := getOrCreateAnthropicToResponsesStreamState(ctx)
	state.webSearchItemIDs = map[string]bool{itemID: true}

	query := `{"query":"world news today"}`
	bifrostResp := &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemDone,
		OutputIndex: schemas.Ptr(1),
		Item: &schemas.ResponsesMessage{
			ID:   schemas.Ptr(itemID),
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    schemas.Ptr(itemID),
				Name:      schemas.Ptr("WebSearch"),
				Arguments: &query,
			},
		},
	}

	events := ToAnthropicResponsesStreamResponse(ctx, bifrostResp)

	// Must have at least one input_json_delta and a final content_block_stop
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (deltas + stop), got %d", len(events))
	}

	// All events except last must be input_json_delta
	for i, ev := range events[:len(events)-1] {
		if ev.Type != AnthropicStreamEventTypeContentBlockDelta {
			t.Errorf("event[%d].Type = %v, want content_block_delta", i, ev.Type)
			continue
		}
		if ev.Delta == nil || ev.Delta.Type != AnthropicStreamDeltaTypeInputJSON {
			t.Errorf("event[%d].Delta.Type = %v, want input_json", i, ev.Delta)
		}
	}

	// Last event must be content_block_stop
	last := events[len(events)-1]
	if last.Type != AnthropicStreamEventTypeContentBlockStop {
		t.Errorf("last event.Type = %v, want content_block_stop", last.Type)
	}

	// Reconstruct the accumulated JSON from the deltas
	var accumulated string
	for _, ev := range events[:len(events)-1] {
		if ev.Delta != nil && ev.Delta.PartialJSON != nil {
			accumulated += *ev.Delta.PartialJSON
		}
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(accumulated), &got); err != nil {
		t.Fatalf("accumulated JSON invalid: %v — got %q", err, accumulated)
	}
	if got["query"] != "world news today" {
		t.Errorf("query = %v, want %q", got["query"], "world news today")
	}

	// ID must have been cleaned up from per-request state
	if state.webSearchItemIDs[itemID] {
		t.Error("expected item ID to be removed from per-request stream state after output_item.done")
	}
}

// TestWebSearch_FullFlow_AnyUserAgent is the regression test for the original bug.
// It simulates the complete streaming sequence:
//
//	output_item.added → FunctionCallArgumentsDelta (×N) → output_item.done
//
// and verifies that the client-facing Anthropic stream contains proper
// input_json_delta events with the query, regardless of user agent.
func TestWebSearch_FullFlow_AnyUserAgent(t *testing.T) {
	t.Parallel()

	const itemID = "toolu_ws_fullflow_test"

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	var allEvents []*AnthropicStreamEvent

	// Step 1: output_item.added
	addedResp := &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
		OutputIndex: schemas.Ptr(0),
		Item: &schemas.ResponsesMessage{
			ID:   schemas.Ptr(itemID),
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    schemas.Ptr(itemID),
				Name:      schemas.Ptr("WebSearch"),
				Arguments: schemas.Ptr(""),
			},
		},
	}
	allEvents = append(allEvents, ToAnthropicResponsesStreamResponse(ctx, addedResp)...)

	// Step 2: FunctionCallArgumentsDelta events (should be skipped)
	for _, partial := range []string{`{"query": "`, `latest AI`, `news"}`} {
		p := partial
		deltaResp := &schemas.BifrostResponsesStreamResponse{
			Type:        schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			OutputIndex: schemas.Ptr(0),
			ItemID:      schemas.Ptr(itemID),
			Delta:       &p,
		}
		allEvents = append(allEvents, ToAnthropicResponsesStreamResponse(ctx, deltaResp)...)
	}

	// Step 3: output_item.done with full accumulated arguments
	fullArgs := `{"query":"latest AI news"}`
	doneResp := &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemDone,
		OutputIndex: schemas.Ptr(0),
		Item: &schemas.ResponsesMessage{
			ID:   schemas.Ptr(itemID),
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    schemas.Ptr(itemID),
				Name:      schemas.Ptr("WebSearch"),
				Arguments: &fullArgs,
			},
		},
	}
	allEvents = append(allEvents, ToAnthropicResponsesStreamResponse(ctx, doneResp)...)

	// Verify the sequence:
	// [0] content_block_start (input:{})
	// [1..N-1] input_json_delta events
	// [N] content_block_stop
	if len(allEvents) < 3 {
		t.Fatalf("expected at least 3 events, got %d: %v", len(allEvents), allEvents)
	}

	// First event: content_block_start with empty input
	if allEvents[0].Type != AnthropicStreamEventTypeContentBlockStart {
		t.Errorf("allEvents[0].Type = %v, want content_block_start", allEvents[0].Type)
	}

	// Last event: content_block_stop
	last := allEvents[len(allEvents)-1]
	if last.Type != AnthropicStreamEventTypeContentBlockStop {
		t.Errorf("last event.Type = %v, want content_block_stop", last.Type)
	}

	// Middle events: all input_json_delta
	for i, ev := range allEvents[1 : len(allEvents)-1] {
		if ev.Type != AnthropicStreamEventTypeContentBlockDelta {
			t.Errorf("allEvents[%d].Type = %v, want content_block_delta", i+1, ev.Type)
		}
		if ev.Delta == nil || ev.Delta.Type != AnthropicStreamDeltaTypeInputJSON {
			t.Errorf("allEvents[%d].Delta.Type = %v, want input_json", i+1, ev.Delta)
		}
	}

	// Reconstruct query from synthetic deltas
	var accumulated string
	for _, ev := range allEvents[1 : len(allEvents)-1] {
		if ev.Delta != nil && ev.Delta.PartialJSON != nil {
			accumulated += *ev.Delta.PartialJSON
		}
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(accumulated), &got); err != nil {
		t.Fatalf("reconstructed JSON is invalid: %v — got %q", err, accumulated)
	}
	if got["query"] != "latest AI news" {
		t.Errorf("reconstructed query = %v, want %q", got["query"], "latest AI news")
	}
}

// TestServerSearchTools_VersionRecognition guards the request converter against a
// newer version-dated web_search / web_fetch tool type being silently downgraded to
// a client function tool. convertAnthropicToolToBifrost matches these by prefix
// (mirroring the unmarshaler and the chat path), so any current or future dated
// version must map to the neutral server-tool type. Anti-vacuous: the future-dated
// entries (…20260318) fall through to ResponsesToolTypeFunction before the fix.
func TestServerSearchTools_VersionRecognition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		toolType string
		want     schemas.ResponsesToolType
	}{
		// web_search: known versions + a future-dated one.
		{"web_search_20250305", schemas.ResponsesToolTypeWebSearch},
		{"web_search_20260209", schemas.ResponsesToolTypeWebSearch},
		{"web_search_20260318", schemas.ResponsesToolTypeWebSearch},
		// web_fetch: known versions + the reported 20260318.
		{"web_fetch_20250910", schemas.ResponsesToolTypeWebFetch},
		{"web_fetch_20260209", schemas.ResponsesToolTypeWebFetch},
		{"web_fetch_20260309", schemas.ResponsesToolTypeWebFetch},
		{"web_fetch_20260318", schemas.ResponsesToolTypeWebFetch},
	}
	for _, c := range cases {
		t.Run(c.toolType, func(t *testing.T) {
			in := &AnthropicTool{Type: schemas.Ptr(AnthropicToolType(c.toolType)), Name: "web_fetch"}
			neutral := convertAnthropicToolToBifrost(in)
			if neutral == nil {
				t.Fatalf("%s: convertAnthropicToolToBifrost returned nil", c.toolType)
			}
			if neutral.Type != c.want {
				t.Errorf("%s: neutral tool type = %q, want %q (must not fall through to a client function tool)", c.toolType, neutral.Type, c.want)
			}
		})
	}
}

// webSearchTurnBody is an /anthropic/v1/messages request whose last assistant turn is
// the given content, followed by followUp (a user message, or "" for none).
func webSearchTurnBody(model, assistantContent, followUp string) string {
	if followUp != "" {
		followUp = "," + followUp
	}
	return `{"model":"` + model + `","max_tokens":1024,
"tools":[{"type":"web_search_20250305","name":"web_search"},
{"name":"kubectl","input_schema":{"type":"object","properties":{"args":{"type":"string"}}}}],
"messages":[{"role":"user","content":"why are pods crashing?"},
{"role":"assistant","content":` + assistantContent + `}` + followUp + `]}`
}

// webSearchRequestRoundTrip sends body through the /anthropic/v1/messages request path
// (Anthropic wire -> neutral -> Anthropic wire) and returns the neutral input and the
// rebuilt assistant turn.
func webSearchRequestRoundTrip(t *testing.T, body string) ([]schemas.ResponsesMessage, []AnthropicContentBlock) {
	t.Helper()
	var req AnthropicMessageRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	neutral := req.ToBifrostResponsesRequest(ctx)
	out, err := ToAnthropicResponsesRequest(ctx, neutral)
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}
	return neutral.Input, webSearchAssistantBlocks(out.Messages)
}

// webSearchAssistantBlocks returns the assistant blocks of a rebuilt request in order.
// The converter may split one turn over consecutive assistant messages, which Anthropic
// reads as a single turn.
func webSearchAssistantBlocks(messages []AnthropicMessage) []AnthropicContentBlock {
	var blocks []AnthropicContentBlock
	for _, m := range messages {
		if m.Role == AnthropicMessageRoleAssistant {
			blocks = append(blocks, m.Content.ContentBlocks...)
		}
	}
	return blocks
}

func webSearchBlockTypes(blocks []AnthropicContentBlock) []string {
	types := make([]string, 0, len(blocks))
	for _, b := range blocks {
		types = append(types, string(b.Type))
	}
	return types
}

func webSearchCallStatus(t *testing.T, items []schemas.ResponsesMessage) string {
	t.Helper()
	for _, item := range items {
		if item.Type != nil && *item.Type == schemas.ResponsesMessageTypeWebSearchCall {
			if item.Status == nil {
				return ""
			}
			return *item.Status
		}
	}
	t.Fatalf("no web_search_call among %d neutral items", len(items))
	return ""
}

// A web_search server_tool_use with no web_search_tool_result is a search Anthropic has
// not run yet: it defers a search called in the same turn as a client tool, and resumes
// it when the turn is sent back with the client tool_result blocks. The rebuilt request
// must keep that call bare. A synthesized result turns it into a finished search, the
// turn no longer ends on the client tool_use, and Anthropic rejects the request with
// "tool_use ids were found without tool_result blocks immediately after".
func TestWebSearch_PendingCallKeepsNoResultOnRequestRoundTrip(t *testing.T) {
	const (
		thinking   = `{"type":"thinking","thinking":"check the pods, then search","signature":"sig"}`
		clientUse  = `{"type":"tool_use","id":"toolu_01Qn","name":"kubectl","input":{"args":"get pods"}}`
		pendingUse = `{"type":"server_tool_use","id":"srvtoolu_01Js","name":"web_search","input":{"query":"pod crashloop"}}`
		clientDone = `{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01Qn","content":"CrashLoopBackOff"}]}`
	)
	cases := []struct {
		name      string
		assistant string
		followUp  string
		want      []string
	}{
		{"client tool before the search", "[" + thinking + "," + clientUse + "," + pendingUse + "]", clientDone,
			[]string{"thinking", "tool_use", "server_tool_use"}},
		{"search before the client tool", "[" + pendingUse + "," + clientUse + "]", clientDone,
			[]string{"server_tool_use", "tool_use"}},
		{"paused turn with no client tool", `[{"type":"text","text":"Searching."},` + pendingUse + "]", "",
			[]string{"text", "server_tool_use"}},
	}
	// Vertex and Azure send Claude through the same converters as the Anthropic host.
	for _, model := range []string{"claude-sonnet-4-6", "anthropic/claude-sonnet-4-6", "vertex/claude-sonnet-4-6", "azure/claude-sonnet-4-6"} {
		for _, tc := range cases {
			t.Run(model+"/"+tc.name, func(t *testing.T) {
				neutral, blocks := webSearchRequestRoundTrip(t, webSearchTurnBody(model, tc.assistant, tc.followUp))
				if got := webSearchCallStatus(t, neutral); got != "in_progress" {
					t.Errorf("neutral web_search_call status = %q, want in_progress", got)
				}
				got := webSearchBlockTypes(blocks)
				if strings.Join(got, ",") != strings.Join(tc.want, ",") {
					t.Fatalf("assistant turn = %v, want %v", got, tc.want)
				}
				for _, b := range blocks {
					if b.Type != AnthropicContentBlockTypeServerToolUse {
						continue
					}
					if b.ID == nil || *b.ID != "srvtoolu_01Js" {
						t.Errorf("server_tool_use id = %v, want srvtoolu_01Js", b.ID)
					}
					if string(b.Input) != `{"query":"pod crashloop"}` {
						t.Errorf("server_tool_use input = %s", b.Input)
					}
				}
			})
		}
	}
}

// A search that did return keeps its web_search_tool_result, including one that found
// nothing: an empty result is a finished search, not a pending one.
func TestWebSearch_FinishedCallKeepsResultOnRequestRoundTrip(t *testing.T) {
	const use = `{"type":"server_tool_use","id":"srvtoolu_01Js","name":"web_search","input":{"query":"pod crashloop"}}`
	cases := []struct {
		name       string
		result     string
		wantStatus string
		wantURLs   int
	}{
		{"with sources", `{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01Js","content":[
{"type":"web_search_result","url":"https://example.com/a","title":"A","encrypted_content":"enc"}]}`, "completed", 1},
		{"no sources", `{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01Js","content":[]}`, "completed", 0},
		{"error", `{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01Js","content":
{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}}`, "completed", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assistant := "[" + use + "," + tc.result + `,{"type":"text","text":"Found it."}]`
			neutral, blocks := webSearchRequestRoundTrip(t, webSearchTurnBody("claude-sonnet-4-6", assistant, `{"role":"user","content":"thanks"}`))
			if got := webSearchCallStatus(t, neutral); got != tc.wantStatus {
				t.Errorf("neutral web_search_call status = %q, want %q", got, tc.wantStatus)
			}
			got := webSearchBlockTypes(blocks)
			if want := "server_tool_use,web_search_tool_result,text"; strings.Join(got, ",") != want {
				t.Fatalf("assistant turn = %v, want %s", got, want)
			}
			result := blocks[1]
			if result.ToolUseID == nil || *result.ToolUseID != "srvtoolu_01Js" {
				t.Errorf("result tool_use_id = %v, want srvtoolu_01Js", result.ToolUseID)
			}
			if result.Content == nil || len(result.Content.ContentBlocks) != tc.wantURLs {
				t.Errorf("result content = %+v, want %d sources", result.Content, tc.wantURLs)
			}
		})
	}
}

// /v1/responses and the Go SDK enter at the neutral types. An in_progress
// web_search_call is the pending search and stays bare; one with any other status, or
// none, is history from a provider that does not replay a result block (OpenAI, Gemini)
// and still gets the web_search_tool_result Anthropic requires beside a finished search.
func TestWebSearch_NeutralCallStatusDecidesResultBlock(t *testing.T) {
	user := schemas.ResponsesInputMessageRoleUser
	cases := []struct {
		name   string
		status *string
		want   string
	}{
		{"in_progress", schemas.Ptr("in_progress"), "tool_use,server_tool_use"},
		{"completed", schemas.Ptr("completed"), "tool_use,server_tool_use,web_search_tool_result"},
		{"failed", schemas.Ptr("failed"), "tool_use,server_tool_use,web_search_tool_result"},
		{"no status", nil, "tool_use,server_tool_use,web_search_tool_result"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &schemas.BifrostResponsesRequest{
				Provider: schemas.Anthropic,
				Model:    "claude-sonnet-4-6",
				Input: []schemas.ResponsesMessage{
					{Role: &user, Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("why are pods crashing?")}},
					{
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						Status: schemas.Ptr("completed"),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: schemas.Ptr("toolu_01Qn"), Name: schemas.Ptr("kubectl"), Arguments: schemas.Ptr(`{"args":"get pods"}`),
						},
					},
					{
						ID:     schemas.Ptr("srvtoolu_01Js"),
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
						Status: tc.status,
						ResponsesToolMessage: &schemas.ResponsesToolMessage{Action: &schemas.ResponsesToolMessageActionStruct{
							ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{Type: "search", Query: schemas.Ptr("pod crashloop")},
						}},
					},
					{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: schemas.Ptr("toolu_01Qn"),
							Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("CrashLoopBackOff")},
						},
					},
				},
			}
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()
			out, err := ToAnthropicResponsesRequest(ctx, req)
			if err != nil {
				t.Fatalf("ToAnthropicResponsesRequest: %v", err)
			}
			if got := strings.Join(webSearchBlockTypes(webSearchAssistantBlocks(out.Messages)), ","); got != tc.want {
				t.Fatalf("assistant turn = %s, want %s", got, tc.want)
			}
		})
	}
}

// A non-streaming response that defers its search reaches the client the way Anthropic
// sent it: the neutral output item is in_progress (the status the streaming path already
// gives a search with no result yet), the /anthropic/v1/messages response carries the bare
// server_tool_use the client has to send back, and the /v1/responses output replays bare.
func TestWebSearch_PendingCallKeepsNoResultOnResponse(t *testing.T) {
	var resp AnthropicMessageResponse
	if err := json.Unmarshal([]byte(`{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-4-6",
"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":10},"content":[
{"type":"tool_use","id":"toolu_01Qn","name":"kubectl","input":{"args":"get pods"}},
{"type":"server_tool_use","id":"srvtoolu_01Js","name":"web_search","input":{"query":"pod crashloop"}}]}`), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	neutral := resp.ToBifrostResponsesResponse(ctx)
	if got := webSearchCallStatus(t, neutral.Output); got != "in_progress" {
		t.Errorf("neutral web_search_call status = %q, want in_progress", got)
	}
	back := ToAnthropicResponsesResponse(ctx, neutral)
	if got, want := strings.Join(webSearchBlockTypes(back.Content), ","), "tool_use,server_tool_use"; got != want {
		t.Fatalf("response content = %s, want %s", got, want)
	}

	// A /v1/responses client sends the output items back as it received them. The
	// status has to survive that JSON round trip for the search to stay pending.
	wire, err := json.Marshal(neutral.Output)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	var input []schemas.ResponsesMessage
	if err := json.Unmarshal(wire, &input); err != nil {
		t.Fatalf("unmarshal output as input: %v", err)
	}
	var toolOutput schemas.ResponsesMessage
	if err := json.Unmarshal([]byte(`{"type":"function_call_output","call_id":"toolu_01Qn","output":"CrashLoopBackOff"}`), &toolOutput); err != nil {
		t.Fatalf("unmarshal function_call_output: %v", err)
	}
	replay, err := ToAnthropicResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic, Model: "claude-sonnet-4-6", Input: append(input, toolOutput),
	})
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}
	if got, want := strings.Join(webSearchBlockTypes(webSearchAssistantBlocks(replay.Messages)), ","), "tool_use,server_tool_use"; got != want {
		t.Fatalf("replayed assistant turn = %s, want %s", got, want)
	}
}

// BenchmarkWebSearchRequestRoundTrip measures the request path for a turn that holds a
// finished search (the common case) and for one that holds a pending search.
func BenchmarkWebSearchRequestRoundTrip(b *testing.B) {
	const use = `{"type":"server_tool_use","id":"srvtoolu_01Js","name":"web_search","input":{"query":"pod crashloop"}}`
	bodies := map[string]string{
		"finished": webSearchTurnBody("claude-sonnet-4-6", "["+use+`,{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01Js","content":[
{"type":"web_search_result","url":"https://example.com/a","title":"A","encrypted_content":"enc"}]},{"type":"text","text":"Found it."}]`,
			`{"role":"user","content":"thanks"}`),
		"pending": webSearchTurnBody("claude-sonnet-4-6",
			`[{"type":"tool_use","id":"toolu_01Qn","name":"kubectl","input":{"args":"get pods"}},`+use+"]",
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01Qn","content":"CrashLoopBackOff"}]}`),
	}
	for _, name := range []string{"finished", "pending"} {
		b.Run(name, func(b *testing.B) {
			var req AnthropicMessageRequest
			if err := json.Unmarshal([]byte(bodies[name]), &req); err != nil {
				b.Fatal(err)
			}
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := ToAnthropicResponsesRequest(ctx, req.ToBifrostResponsesRequest(ctx)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
