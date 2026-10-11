package anthropic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	msgs, _ := ConvertBifrostMessagesToAnthropicMessages(ctx, history, true,
		schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-4-6"))

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

// toolSearchWireResultBlock is the tool_search_tool_result block exactly as
// Anthropic documents it on the wire — tool_references nested inside a
// tool_search_tool_search_result "content" object, NOT flat on the block:
//
//	{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_01ABC123",
//	 "content":{"type":"tool_search_tool_search_result",
//	            "tool_references":[{"type":"tool_reference","tool_name":"get_weather"}]}}
//
// https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool
const toolSearchWireResultBlock = `{
	"type": "tool_search_tool_result",
	"tool_use_id": "` + tsServerToolUseID + `",
	"content": {
		"type": "tool_search_tool_search_result",
		"tool_references": [{"type": "tool_reference", "tool_name": "` + tsDiscoveredTool + `"}]
	}
}`

// TestToolSearch_WireShapeCarriesToolReferences decodes the documented wire shape
// and asserts the discovered tool names are recoverable from the decoded block.
// AnthropicContentBlock.ToolReferences is declared flat (`json:"tool_references"`),
// so real traffic leaves it nil and the references land one level down in Content —
// DiscoveredToolReferences is the reader that spans both shapes.
func TestToolSearch_WireShapeCarriesToolReferences(t *testing.T) {
	t.Parallel()

	var block AnthropicContentBlock
	if err := sonic.Unmarshal([]byte(toolSearchWireResultBlock), &block); err != nil {
		t.Fatalf("documented wire block must decode: %v", err)
	}
	if block.Type != AnthropicContentBlockTypeToolSearchToolResult {
		t.Fatalf("block type = %q, want tool_search_tool_result", block.Type)
	}
	if len(block.ToolReferences) != 0 {
		t.Errorf("the flat field is not the wire shape; expected it to stay empty, got %d refs", len(block.ToolReferences))
	}

	names := make([]string, 0, 1)
	for _, ref := range block.DiscoveredToolReferences() {
		if ref.ToolName != nil {
			names = append(names, *ref.ToolName)
		}
	}
	if len(names) != 1 || names[0] != tsDiscoveredTool {
		t.Fatalf("DiscoveredToolReferences() = %v, want [%q] — nested tool_references were not reachable",
			names, tsDiscoveredTool)
	}
}

// TestToolSearch_FlatToolReferencesStillRead pins the other half of the accessor's
// contract: Bifrost's own rebuild (convertBifrostToolSearchCallToAnthropicBlocks)
// sets the flat field, so a block in that shape must keep working.
func TestToolSearch_FlatToolReferencesStillRead(t *testing.T) {
	t.Parallel()

	block := AnthropicContentBlock{
		Type:      AnthropicContentBlockTypeToolSearchToolResult,
		ToolUseID: schemas.Ptr(tsServerToolUseID),
		ToolReferences: []AnthropicContentBlock{
			{Type: AnthropicContentBlockTypeToolReference, ToolName: schemas.Ptr(tsDiscoveredTool)},
		},
	}

	refs := block.DiscoveredToolReferences()
	if len(refs) != 1 || refs[0].ToolName == nil || *refs[0].ToolName != tsDiscoveredTool {
		t.Fatalf("DiscoveredToolReferences() = %+v, want one ref to %q", refs, tsDiscoveredTool)
	}
}

// TestToolSearch_NonStreamingForwardsToolReferences is the non-streaming twin of
// TestToolSearch_ForwardsToolReferences. The streaming state machine emits a
// tool_search_call item; the non-streaming converter's server_tool_use dispatch
// handles web_search / web_fetch / advisor / code_execution only, so a
// tool_search server_tool_use and its tool_search_tool_result are both dropped
// and the caller sees no tool_search_call at all.
func TestToolSearch_NonStreamingForwardsToolReferences(t *testing.T) {
	t.Parallel()

	var resultBlock AnthropicContentBlock
	if err := sonic.Unmarshal([]byte(toolSearchWireResultBlock), &resultBlock); err != nil {
		t.Fatalf("documented wire block must decode: %v", err)
	}

	resp := &AnthropicMessageResponse{
		ID:         "msg_ts_nonstream",
		Type:       "message",
		Role:       "assistant",
		Model:      "claude-sonnet-4-6",
		StopReason: AnthropicStopReasonToolUse,
		Content: []AnthropicContentBlock{
			{
				Type: AnthropicContentBlockTypeServerToolUse,
				ID:   schemas.Ptr(tsServerToolUseID),
				Name: schemas.Ptr(string(AnthropicToolNameToolSearchRegex)),
			},
			resultBlock,
			{
				Type: AnthropicContentBlockTypeToolUse,
				ID:   schemas.Ptr(tsDiscoveredCallID),
				Name: schemas.Ptr(tsDiscoveredTool),
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostResp := resp.ToBifrostResponsesResponse(ctx)
	if bifrostResp == nil {
		t.Fatal("converter returned nil")
	}

	var search *schemas.ResponsesMessage
	var sawDiscoveredCall bool
	for i := range bifrostResp.Output {
		item := &bifrostResp.Output[i]
		if item.Type == nil {
			continue
		}
		switch *item.Type {
		case schemas.ResponsesMessageTypeToolSearchCall:
			search = item
		case schemas.ResponsesMessageTypeFunctionCall:
			if item.ResponsesToolMessage != nil && item.ResponsesToolMessage.Name != nil &&
				*item.ResponsesToolMessage.Name == tsDiscoveredTool {
				sawDiscoveredCall = true
			}
		}
	}

	if search == nil {
		t.Fatal("no tool_search_call item in non-streaming output — the server_tool_use and tool_search_tool_result blocks were dropped")
	}
	if search.ResponsesToolMessage == nil || search.ResponsesToolMessage.ResponsesToolSearchCall == nil {
		t.Fatal("tool_search_call item carries no ResponsesToolSearchCall payload")
	}
	refs := search.ResponsesToolMessage.ResponsesToolSearchCall.ToolReferences
	if len(refs) != 1 || refs[0] != tsDiscoveredTool {
		t.Fatalf("non-streaming tool_references = %v, want [%q]", refs, tsDiscoveredTool)
	}
	if !sawDiscoveredCall {
		t.Errorf("the follow-up tool_use calling the discovered tool must still be forwarded as a function_call")
	}
}

// TestToolSearch_GroupedReplayKeepsToolSearchCall covers the replay direction used
// for Bedrock (ConvertAnthropicMessagesToBifrostMessages is called with
// keepToolsGrouped = provider == schemas.Bedrock). A client echoing the assistant
// turn back — which Anthropic requires, unchanged — must not have the tool-search
// server_tool_use downgraded into a function_call: that would make the caller
// return a tool_result for a srvtoolu_ id, which the API rejects.
func TestToolSearch_GroupedReplayKeepsToolSearchCall(t *testing.T) {
	t.Parallel()

	var resultBlock AnthropicContentBlock
	if err := sonic.Unmarshal([]byte(toolSearchWireResultBlock), &resultBlock); err != nil {
		t.Fatalf("documented wire block must decode: %v", err)
	}

	assistant := AnthropicMessage{
		Role: AnthropicMessageRoleAssistant,
		Content: AnthropicContent{
			ContentBlocks: []AnthropicContentBlock{
				{
					Type: AnthropicContentBlockTypeServerToolUse,
					ID:   schemas.Ptr(tsServerToolUseID),
					Name: schemas.Ptr(string(AnthropicToolNameToolSearchRegex)),
				},
				resultBlock,
				{
					Type: AnthropicContentBlockTypeToolUse,
					ID:   schemas.Ptr(tsDiscoveredCallID),
					Name: schemas.Ptr(tsDiscoveredTool),
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	msgs := ConvertAnthropicMessagesToBifrostMessages(ctx, []AnthropicMessage{assistant}, nil, false, true)

	var search *schemas.ResponsesMessage
	var sawDiscoveredCall bool
	for i := range msgs {
		m := &msgs[i]
		if m.Type == nil {
			continue
		}
		switch *m.Type {
		case schemas.ResponsesMessageTypeToolSearchCall:
			search = m
		case schemas.ResponsesMessageTypeFunctionCall:
			if m.ResponsesToolMessage != nil && m.ResponsesToolMessage.Name != nil {
				switch *m.ResponsesToolMessage.Name {
				case tsDiscoveredTool:
					sawDiscoveredCall = true
				case string(AnthropicToolNameToolSearchRegex), string(AnthropicToolNameToolSearchBM25):
					t.Errorf("tool-search server_tool_use was replayed as a client function_call")
				}
			}
		}
	}

	if search == nil {
		t.Fatal("no tool_search_call survived the grouped replay conversion")
	}
	if search.ResponsesToolMessage == nil || search.ResponsesToolMessage.ResponsesToolSearchCall == nil {
		t.Fatal("replayed tool_search_call carries no ResponsesToolSearchCall payload")
	}
	refs := search.ResponsesToolMessage.ResponsesToolSearchCall.ToolReferences
	if len(refs) != 1 || refs[0] != tsDiscoveredTool {
		t.Fatalf("replayed tool_references = %v, want [%q]", refs, tsDiscoveredTool)
	}
	if !sawDiscoveredCall {
		t.Errorf("the tool_use calling the discovered tool must still replay as a function_call")
	}
}

// tsSearchQuery is the server_tool_use.input payload Anthropic sends for a regex
// tool search — the pattern the model actually searched with.
const tsSearchQuery = `{"query":"weather"}`

// TestToolSearch_PreservesSearchQuery pins the search query across all four hops it
// has to survive. Anthropic requires the client to echo the assistant's
// server_tool_use back unchanged on the next turn, and a block whose input has been
// replaced with {} is not unchanged - the query the model searched with is gone.
// (https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool)
func TestToolSearch_PreservesSearchQuery(t *testing.T) {
	t.Parallel()

	t.Run("streaming buffers the query onto Arguments", func(t *testing.T) {
		t.Parallel()
		all := driveToolSearch(t, toolSearchStreamChunks(string(AnthropicToolNameToolSearchRegex), false))

		var done *schemas.ResponsesMessage
		for _, r := range all {
			if r.Type == schemas.ResponsesStreamResponseTypeOutputItemDone &&
				r.Item != nil && r.Item.Type != nil &&
				*r.Item.Type == schemas.ResponsesMessageTypeToolSearchCall {
				done = r.Item
			}
		}
		require.NotNil(t, done, "no tool_search_call done emitted")
		require.NotNil(t, done.ResponsesToolMessage)
		require.NotNil(t, done.ResponsesToolMessage.Arguments,
			"the buffered input_json deltas were discarded, so the search query is lost")
		assert.JSONEq(t, tsSearchQuery, *done.ResponsesToolMessage.Arguments)
	})

	t.Run("non-streaming carries block.Input onto Arguments", func(t *testing.T) {
		t.Parallel()
		var resultBlock AnthropicContentBlock
		require.NoError(t, sonic.Unmarshal([]byte(toolSearchWireResultBlock), &resultBlock))

		resp := &AnthropicMessageResponse{
			ID: "msg_q", Type: "message", Role: "assistant", Model: "claude-sonnet-4-6",
			StopReason: AnthropicStopReasonToolUse,
			Content: []AnthropicContentBlock{
				{
					Type:  AnthropicContentBlockTypeServerToolUse,
					ID:    schemas.Ptr(tsServerToolUseID),
					Name:  schemas.Ptr(string(AnthropicToolNameToolSearchRegex)),
					Input: json.RawMessage(tsSearchQuery),
				},
				resultBlock,
			},
		}
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		out := resp.ToBifrostResponsesResponse(ctx)
		require.NotNil(t, out)

		var search *schemas.ResponsesMessage
		for i := range out.Output {
			if out.Output[i].Type != nil && *out.Output[i].Type == schemas.ResponsesMessageTypeToolSearchCall {
				search = &out.Output[i]
			}
		}
		require.NotNil(t, search, "no tool_search_call in non-streaming output")
		require.NotNil(t, search.ResponsesToolMessage.Arguments,
			"server_tool_use.input was dropped at the non-streaming creation site")
		assert.JSONEq(t, tsSearchQuery, *search.ResponsesToolMessage.Arguments)
	})

	t.Run("grouped replay carries block.Input onto Arguments", func(t *testing.T) {
		t.Parallel()
		var resultBlock AnthropicContentBlock
		require.NoError(t, sonic.Unmarshal([]byte(toolSearchWireResultBlock), &resultBlock))

		assistant := AnthropicMessage{
			Role: AnthropicMessageRoleAssistant,
			Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{
				{
					Type:  AnthropicContentBlockTypeServerToolUse,
					ID:    schemas.Ptr(tsServerToolUseID),
					Name:  schemas.Ptr(string(AnthropicToolNameToolSearchRegex)),
					Input: json.RawMessage(tsSearchQuery),
				},
				resultBlock,
			}},
		}
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		msgs := ConvertAnthropicMessagesToBifrostMessages(ctx, []AnthropicMessage{assistant}, nil, false, true)

		var search *schemas.ResponsesMessage
		for i := range msgs {
			if msgs[i].Type != nil && *msgs[i].Type == schemas.ResponsesMessageTypeToolSearchCall {
				search = &msgs[i]
			}
		}
		require.NotNil(t, search, "no tool_search_call survived grouped replay")
		require.NotNil(t, search.ResponsesToolMessage.Arguments,
			"server_tool_use.input was dropped at the grouped creation site")
		assert.JSONEq(t, tsSearchQuery, *search.ResponsesToolMessage.Arguments)
	})

	t.Run("reverse rebuild emits the query, not an empty object", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()

		history := []schemas.ResponsesMessage{{
			ID:   schemas.Ptr(tsServerToolUseID),
			Type: schemas.Ptr(schemas.ResponsesMessageTypeToolSearchCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:                  schemas.Ptr(tsServerToolUseID),
				Name:                    schemas.Ptr(string(AnthropicToolNameToolSearchRegex)),
				Arguments:               schemas.Ptr(tsSearchQuery),
				ResponsesToolSearchCall: &schemas.ResponsesToolSearchCall{ToolReferences: []string{tsDiscoveredTool}},
			},
		}}

		msgs, _ := ConvertBifrostMessagesToAnthropicMessages(ctx, history, true,
			schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-4-6"))

		var serverToolUse *AnthropicContentBlock
		for mi := range msgs {
			for bi := range msgs[mi].Content.ContentBlocks {
				if b := &msgs[mi].Content.ContentBlocks[bi]; b.Type == AnthropicContentBlockTypeServerToolUse {
					serverToolUse = b
				}
			}
		}
		require.NotNil(t, serverToolUse, "no server_tool_use rebuilt")
		assert.JSONEq(t, tsSearchQuery, string(serverToolUse.Input),
			"the rebuilt block must carry the original query, not {}")
	})

	t.Run("absent Arguments still rebuilds an empty object", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()

		history := []schemas.ResponsesMessage{{
			ID:   schemas.Ptr(tsServerToolUseID),
			Type: schemas.Ptr(schemas.ResponsesMessageTypeToolSearchCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:                  schemas.Ptr(tsServerToolUseID),
				Name:                    schemas.Ptr(string(AnthropicToolNameToolSearchRegex)),
				ResponsesToolSearchCall: &schemas.ResponsesToolSearchCall{ToolReferences: []string{tsDiscoveredTool}},
			},
		}}

		msgs, _ := ConvertBifrostMessagesToAnthropicMessages(ctx, history, true,
			schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-4-6"))

		var serverToolUse *AnthropicContentBlock
		for mi := range msgs {
			for bi := range msgs[mi].Content.ContentBlocks {
				if b := &msgs[mi].Content.ContentBlocks[bi]; b.Type == AnthropicContentBlockTypeServerToolUse {
					serverToolUse = b
				}
			}
		}
		require.NotNil(t, serverToolUse)
		assert.JSONEq(t, `{}`, string(serverToolUse.Input))
	})
}
