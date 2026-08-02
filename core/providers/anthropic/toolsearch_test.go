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
		Model:                     schemas.Ptr("claude-sonnet-4-6"),
		CreatedAt:                 1234567890,
		HasEmittedCreated:         true,
		HasEmittedInProgress:      true,
	}
}

// toolSearchStreamChunks builds a realistic server-side tool_search Anthropic
// stream for the given tool-search variant: server_tool_use(toolName) ->
// tool_search_tool_result(tool_references to the discovered tool) ->
// tool_use(discovered tool). When withStop is set, a terminal message_stop is
// appended so the converter emits response.completed.
func toolSearchStreamChunks(toolName string, withStop bool) []*AnthropicStreamEvent {
	q := `{"query":"weather"}`
	args := `{"location":"Tokyo"}`
	chunks := []*AnthropicStreamEvent{
		// idx0: server_tool_use(<tool search variant>) + its query deltas
		{Type: AnthropicStreamEventTypeContentBlockStart, Index: schemas.Ptr(0), ContentBlock: &AnthropicContentBlock{
			Type: AnthropicContentBlockTypeServerToolUse,
			ID:   schemas.Ptr(tsServerToolUseID),
			Name: schemas.Ptr(toolName),
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
	if withStop {
		stopReason := AnthropicStopReasonToolUse
		chunks = append(chunks,
			&AnthropicStreamEvent{Type: AnthropicStreamEventTypeMessageDelta, Delta: &AnthropicStreamDelta{StopReason: &stopReason}},
			&AnthropicStreamEvent{Type: AnthropicStreamEventTypeMessageStop},
		)
	}
	return chunks
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
// from tool_search_tool_result survive into a tool_search_call item (carrying
// the tool name), instead of being dropped. Runs both tool_search variants.
// Fails on the unpatched provider, which emits no tool_search_call.
func TestToolSearch_ForwardsToolReferences(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		toolName string
	}{
		{"regex", string(AnthropicToolNameToolSearchRegex)},
		{"bm25", string(AnthropicToolNameToolSearchBM25)},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			all := driveToolSearch(t, toolSearchStreamChunks(tc.toolName, false))

			var done *schemas.ResponsesMessage
			for _, r := range all {
				if r.Type == schemas.ResponsesStreamResponseTypeOutputItemDone &&
					r.Item != nil && r.Item.Type != nil &&
					*r.Item.Type == schemas.ResponsesMessageTypeToolSearchCall {
					done = r.Item
				}
			}
			if done == nil {
				t.Fatal("no tool_search_call output_item.done emitted — tool_search_tool_result was dropped")
			}
			if done.ResponsesToolMessage == nil || done.ResponsesToolMessage.ResponsesToolSearchCall == nil {
				t.Fatal("tool_search_call item carries no ResponsesToolSearchCall payload")
			}
			refs := done.ResponsesToolMessage.ResponsesToolSearchCall.ToolReferences
			if len(refs) != 1 || refs[0] != tsDiscoveredTool {
				t.Fatalf("tool_references = %v, want [%q]", refs, tsDiscoveredTool)
			}
			// done item must carry the tool name, matching the added item (advisor parity)
			if done.ResponsesToolMessage.Name == nil || *done.ResponsesToolMessage.Name != tc.toolName {
				t.Fatalf("tool_search_call done Name = %v, want %q", done.ResponsesToolMessage.Name, tc.toolName)
			}
		})
	}
}

// TestToolSearch_ForwardsDiscoveredToolUse asserts the follow-up tool_use that
// calls the discovered tool is forwarded as a function_call (added + done).
func TestToolSearch_ForwardsDiscoveredToolUse(t *testing.T) {
	t.Parallel()
	all := driveToolSearch(t, toolSearchStreamChunks(string(AnthropicToolNameToolSearchRegex), false))

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
	all := driveToolSearch(t, toolSearchStreamChunks(string(AnthropicToolNameToolSearchRegex), false))

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

// TestToolSearch_CompletedResponseIncludesToolSearchCall asserts the terminal
// response.completed Output carries the tool_search_call (with its tool_references),
// guarding the OutputItems persistence that the streamed done events alone don't cover.
func TestToolSearch_CompletedResponseIncludesToolSearchCall(t *testing.T) {
	t.Parallel()
	all := driveToolSearch(t, toolSearchStreamChunks(string(AnthropicToolNameToolSearchRegex), true))

	var completed *schemas.BifrostResponsesResponse
	for _, r := range all {
		if r.Type == schemas.ResponsesStreamResponseTypeCompleted && r.Response != nil {
			completed = r.Response
		}
	}
	if completed == nil {
		t.Fatal("no response.completed emitted")
	}
	var foundRefs []string
	var foundTool bool
	for i := range completed.Output {
		item := completed.Output[i]
		if item.Type == nil {
			continue
		}
		switch *item.Type {
		case schemas.ResponsesMessageTypeToolSearchCall:
			if item.ResponsesToolMessage != nil && item.ResponsesToolMessage.ResponsesToolSearchCall != nil {
				foundRefs = item.ResponsesToolMessage.ResponsesToolSearchCall.ToolReferences
			}
		case schemas.ResponsesMessageTypeFunctionCall:
			if item.ResponsesToolMessage != nil && item.ResponsesToolMessage.Name != nil &&
				*item.ResponsesToolMessage.Name == tsDiscoveredTool {
				foundTool = true
			}
		}
	}
	if len(foundRefs) != 1 || foundRefs[0] != tsDiscoveredTool {
		t.Fatalf("response.completed tool_search_call tool_references = %v, want [%q]", foundRefs, tsDiscoveredTool)
	}
	if !foundTool {
		t.Fatal("response.completed Output missing the discovered tool function_call")
	}
}

// TestToolSearch_ReverseRebuildsAnthropicBlocks asserts the Bifrost→Anthropic
// request builder rebuilds a tool_search_call into the paired
// server_tool_use(tool_search) + tool_search_tool_result(tool_references) blocks,
// so a follow-up turn keeps the search context (parity with web_search/advisor).
// Without the reverse case the item hits `default: continue` and is dropped.
func TestToolSearch_ReverseRebuildsAnthropicBlocks(t *testing.T) {
	t.Parallel()
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	history := []schemas.ResponsesMessage{
		{
			ID:   schemas.Ptr(tsServerToolUseID),
			Type: schemas.Ptr(schemas.ResponsesMessageTypeToolSearchCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:                  schemas.Ptr(tsServerToolUseID),
				Name:                    schemas.Ptr(string(AnthropicToolNameToolSearchRegex)),
				ResponsesToolSearchCall: &schemas.ResponsesToolSearchCall{ToolReferences: []string{tsDiscoveredTool}},
			},
		},
		{
			ID:   schemas.Ptr(tsDiscoveredCallID),
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: schemas.Ptr(tsDiscoveredCallID), Name: schemas.Ptr(tsDiscoveredTool), Arguments: schemas.Ptr(`{"location":"Tokyo"}`),
			},
		},
	}

	msgs, _ := ConvertBifrostMessagesToAnthropicMessages(ctx, history, true, schemas.Anthropic, "claude-sonnet-4-6")

	var serverToolUse, resultBlock *AnthropicContentBlock
	for mi := range msgs {
		for bi := range msgs[mi].Content.ContentBlocks {
			b := &msgs[mi].Content.ContentBlocks[bi]
			switch b.Type {
			case AnthropicContentBlockTypeServerToolUse:
				if b.Name != nil && *b.Name == string(AnthropicToolNameToolSearchRegex) {
					serverToolUse = b
				}
			case AnthropicContentBlockTypeToolSearchToolResult:
				resultBlock = b
			}
		}
	}

	if serverToolUse == nil {
		t.Fatal("reverse path dropped tool_search_call — no server_tool_use(tool_search) block rebuilt")
	}
	if serverToolUse.ID == nil || *serverToolUse.ID != tsServerToolUseID {
		t.Fatalf("server_tool_use ID = %v, want %q", serverToolUse.ID, tsServerToolUseID)
	}
	if resultBlock == nil {
		t.Fatal("no tool_search_tool_result block rebuilt")
	}
	if resultBlock.ToolUseID == nil || *resultBlock.ToolUseID != tsServerToolUseID {
		t.Fatalf("tool_search_tool_result tool_use_id = %v, want %q", resultBlock.ToolUseID, tsServerToolUseID)
	}
	if len(resultBlock.ToolReferences) != 1 || resultBlock.ToolReferences[0].ToolName == nil ||
		*resultBlock.ToolReferences[0].ToolName != tsDiscoveredTool {
		t.Fatalf("rebuilt tool_references = %+v, want one ref to %q", resultBlock.ToolReferences, tsDiscoveredTool)
	}
}

// A JSON-decoded tool_search_call input item has an initialized ResponsesToolMessage
// (arguments surfaced) but no CallID/ID, so there is no valid tool-use id to build
// server_tool_use / tool_search_tool_result blocks — the reverse path must skip it,
// not emit a nil-id pair Anthropic would reject.
func TestToolSearch_ReverseSkipsWhenNoToolUseID(t *testing.T) {
	t.Parallel()
	msg := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeToolSearchCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			Name:                    schemas.Ptr(string(AnthropicToolNameToolSearchRegex)),
			ResponsesToolSearchCall: &schemas.ResponsesToolSearchCall{ToolReferences: []string{tsDiscoveredTool}},
		},
	}
	if blocks := convertBifrostToolSearchCallToAnthropicBlocks(&msg); blocks != nil {
		t.Fatalf("expected nil (no tool-use id), got %+v", blocks)
	}
}
