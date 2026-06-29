package anthropic

import (
	"context"
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// These tests cover the Anthropic server-side tool_search streaming path
// (server_tool_use(tool_search) -> tool_search_tool_result(tool_references) ->
// tool_use(discovered tool)). Without the handler this provider drops the
// tool_references and emits orphan function_call argument deltas; with it the
// discovered tool references are forwarded and the follow-up tool_use is intact.

const (
	tsServerToolUseID  = "srvtoolu_ts_1"
	tsDiscoveredTool   = "OpenMeteoMCP-weather_forecast"
	tsDiscoveredCallID = "toolu_weather_1"
)

// newToolSearchTestState builds a stream state primed past message_start so a
// test can feed content_block_* chunks directly.
func newToolSearchTestState() *AnthropicResponsesStreamState {
	return &AnthropicResponsesStreamState{
		ContentIndexToOutputIndex: make(map[int]int),
		ContentIndexToBlockType:   make(map[int]AnthropicContentBlockType),
		ToolArgumentBuffers:       make(map[int]string),
		MCPCallOutputIndices:      make(map[int]bool),
		ItemIDs:                   make(map[int]string),
		OutputItems:               make(map[int]*schemas.ResponsesMessage),
		ReasoningSignatures:       make(map[int]string),
		TextContentIndices:        make(map[int]bool),
		ReasoningContentIndices:   make(map[int]bool),
		CompactionContentIndices:  make(map[int]*schemas.CacheControl),
		TextBuffers:               make(map[int]*strings.Builder),
		CurrentOutputIndex:        0,
		MessageID:                 schemas.Ptr("msg_ts_test"),
		CreatedAt:                 1234567890,
		HasEmittedCreated:         true,
		HasEmittedInProgress:      true,
	}
}

// toolSearchStreamChunks builds a realistic server-side tool_search Anthropic
// stream: server_tool_use(tool_search_tool_regex) -> tool_search_tool_result
// (with tool_references to the discovered tool) -> tool_use(discovered tool).
func toolSearchStreamChunks() []*AnthropicStreamEvent {
	q := `{"query":"weather"}`
	args := `{"location":"Tokyo"}`
	return []*AnthropicStreamEvent{
		// idx0: server_tool_use(tool_search_tool_regex) + its query deltas
		{Type: AnthropicStreamEventTypeContentBlockStart, Index: schemas.Ptr(0), ContentBlock: &AnthropicContentBlock{
			Type: AnthropicContentBlockTypeServerToolUse,
			ID:   schemas.Ptr(tsServerToolUseID),
			Name: schemas.Ptr(string(AnthropicToolNameToolSearchRegex)),
		}},
		{Type: AnthropicStreamEventTypeContentBlockDelta, Index: schemas.Ptr(0), Delta: &AnthropicStreamDelta{
			Type: AnthropicStreamDeltaTypeInputJSON, PartialJSON: &q,
		}},
		{Type: AnthropicStreamEventTypeContentBlockStop, Index: schemas.Ptr(0)},

		// idx1: tool_search_tool_result carrying tool_references to the discovered tool
		{Type: AnthropicStreamEventTypeContentBlockStart, Index: schemas.Ptr(1), ContentBlock: &AnthropicContentBlock{
			Type:      AnthropicContentBlockTypeToolSearchToolResult,
			ToolUseID: schemas.Ptr(tsServerToolUseID),
			ToolReferences: []AnthropicContentBlock{
				{Type: AnthropicContentBlockTypeToolReference, ToolName: schemas.Ptr(tsDiscoveredTool)},
			},
		}},
		{Type: AnthropicStreamEventTypeContentBlockStop, Index: schemas.Ptr(1)},

		// idx2: tool_use that calls the discovered tool (the client must forward this)
		{Type: AnthropicStreamEventTypeContentBlockStart, Index: schemas.Ptr(2), ContentBlock: &AnthropicContentBlock{
			Type: AnthropicContentBlockTypeToolUse,
			ID:   schemas.Ptr(tsDiscoveredCallID),
			Name: schemas.Ptr(tsDiscoveredTool),
		}},
		{Type: AnthropicStreamEventTypeContentBlockDelta, Index: schemas.Ptr(2), Delta: &AnthropicStreamDelta{
			Type: AnthropicStreamDeltaTypeInputJSON, PartialJSON: &args,
		}},
		{Type: AnthropicStreamEventTypeContentBlockStop, Index: schemas.Ptr(2)},
	}
}

func driveToolSearch(t *testing.T, chunks []*AnthropicStreamEvent) []*schemas.BifrostResponsesStreamResponse {
	t.Helper()
	state := newToolSearchTestState()
	var all []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for i, c := range chunks {
		resps, berr, _ := c.ToBifrostResponsesStream(context.Background(), seq, state)
		if berr != nil {
			t.Fatalf("chunk %d returned error: %v", i, berr)
		}
		all = append(all, resps...)
		seq += len(resps)
	}
	return all
}

// TestToolSearch_ForwardsToolReferences asserts the discovered tool references
// from tool_search_tool_result survive into a tool_search_call item (instead of
// being dropped). Fails on the unpatched provider, which emits no tool_search_call.
func TestToolSearch_ForwardsToolReferences(t *testing.T) {
	t.Parallel()
	all := driveToolSearch(t, toolSearchStreamChunks())

	var toolSearchDone *schemas.ResponsesMessage
	for _, r := range all {
		if r.Type == schemas.ResponsesStreamResponseTypeOutputItemDone &&
			r.Item != nil && r.Item.Type != nil &&
			*r.Item.Type == schemas.ResponsesMessageTypeToolSearchCall {
			toolSearchDone = r.Item
		}
	}
	if toolSearchDone == nil {
		t.Fatal("no tool_search_call output_item.done emitted — tool_search_tool_result was dropped")
	}
	if toolSearchDone.ResponsesToolMessage == nil || toolSearchDone.ResponsesToolMessage.ResponsesToolSearchCall == nil {
		t.Fatal("tool_search_call item carries no ResponsesToolSearchCall payload")
	}
	refs := toolSearchDone.ResponsesToolMessage.ResponsesToolSearchCall.ToolReferences
	if len(refs) != 1 || refs[0] != tsDiscoveredTool {
		t.Fatalf("tool_references = %v, want [%q]", refs, tsDiscoveredTool)
	}
}

// TestToolSearch_ForwardsDiscoveredToolUse asserts the follow-up tool_use that
// calls the discovered tool is forwarded as a function_call (added + done).
func TestToolSearch_ForwardsDiscoveredToolUse(t *testing.T) {
	t.Parallel()
	all := driveToolSearch(t, toolSearchStreamChunks())

	var sawAdded, sawDone bool
	for _, r := range all {
		if r.Item == nil || r.Item.Type == nil || *r.Item.Type != schemas.ResponsesMessageTypeFunctionCall {
			continue
		}
		if r.Item.ResponsesToolMessage == nil || r.Item.ResponsesToolMessage.Name == nil ||
			*r.Item.ResponsesToolMessage.Name != tsDiscoveredTool {
			continue
		}
		switch r.Type {
		case schemas.ResponsesStreamResponseTypeOutputItemAdded:
			sawAdded = true
		case schemas.ResponsesStreamResponseTypeOutputItemDone:
			sawDone = true
		}
	}
	if !sawAdded || !sawDone {
		t.Fatalf("discovered tool_use not forwarded as function_call (added=%v done=%v)", sawAdded, sawDone)
	}
}

// TestToolSearch_NoOrphanFunctionCallArgs asserts every function_call argument
// delta/done is preceded by an output_item.added for the same item. The unpatched
// provider emits orphan tool-search query argument deltas (args with no parent
// item), which desync the client stream parser — this guards against that.
func TestToolSearch_NoOrphanFunctionCallArgs(t *testing.T) {
	t.Parallel()
	all := driveToolSearch(t, toolSearchStreamChunks())

	added := map[string]bool{}
	for _, r := range all {
		switch r.Type {
		case schemas.ResponsesStreamResponseTypeOutputItemAdded:
			if r.Item != nil && r.Item.ID != nil {
				added[*r.Item.ID] = true
			}
		case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone:
			if r.ItemID == nil {
				t.Fatalf("function_call args event %q has no ItemID (orphan)", r.Type)
			}
			if !added[*r.ItemID] {
				t.Fatalf("orphan function_call args for item %q — no preceding output_item.added", *r.ItemID)
			}
		}
	}
}
