package anthropic

import (
	"context"
	"encoding/json"
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
