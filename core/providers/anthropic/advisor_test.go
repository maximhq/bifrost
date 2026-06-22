package anthropic

import (
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

const advisorToolJSON = `{"type":"advisor_20260301","name":"advisor","model":"claude-opus-4-8","max_uses":3,"max_tokens":2048,"caching":{"type":"ephemeral","ttl":"5m"}}`

// rawAdvisorResponse mirrors the user-reported Anthropic response: an assistant
// turn containing server_tool_use(advisor) + advisor_tool_result + text.
const rawAdvisorResponse = `{
  "model": "claude-sonnet-4-6",
  "id": "msg_01V5B47VyNHS4XfYEvUEgbBs",
  "type": "message",
  "role": "assistant",
  "content": [
    { "type": "server_tool_use", "id": "srvtoolu_01WJ", "name": "advisor", "input": {} },
    {
      "type": "advisor_tool_result",
      "tool_use_id": "srvtoolu_01WJ",
      "content": { "type": "advisor_result", "text": "Use a channel-based coordination pattern." }
    },
    { "type": "text", "text": "Here is the implementation." }
  ],
  "stop_reason": "end_turn",
  "usage": { "input_tokens": 2733, "output_tokens": 3909 }
}`

func newAdvisorStreamState() *AnthropicResponsesStreamState {
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
		TextBuffers:               make(map[int]*strings.Builder),
		CompactionContentIndices:  make(map[int]*schemas.CacheControl),
		CurrentOutputIndex:        0,
		CreatedAt:                 1234567890,
		HasEmittedCreated:         true,
		HasEmittedInProgress:      true,
	}
}

// TestAnthropicTool_AdvisorMarshalRoundTrip verifies the advisor tool survives
// Unmarshal/Marshal through AnthropicTool — in particular that the unique
// `model` field is preserved despite `max_uses` being shared (embedded) with
// the web_search/web_fetch variant structs.
func TestAnthropicTool_AdvisorMarshalRoundTrip(t *testing.T) {
	var tool AnthropicTool
	if err := sonic.Unmarshal([]byte(advisorToolJSON), &tool); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tool.Type == nil || *tool.Type != AnthropicToolTypeAdvisor20260301 {
		t.Fatalf("type not parsed: %+v", tool.Type)
	}
	if tool.AnthropicToolAdvisor == nil {
		t.Fatal("AnthropicToolAdvisor is nil after unmarshal")
	}
	if tool.AnthropicToolAdvisor.Model != "claude-opus-4-8" {
		t.Errorf("model = %q, want claude-opus-4-8", tool.AnthropicToolAdvisor.Model)
	}
	if tool.AnthropicToolAdvisor.MaxTokens == nil || *tool.AnthropicToolAdvisor.MaxTokens != 2048 {
		t.Errorf("max_tokens not parsed: %+v", tool.AnthropicToolAdvisor.MaxTokens)
	}
	if tool.AnthropicToolAdvisor.Caching == nil || tool.AnthropicToolAdvisor.Caching.TTL != "5m" {
		t.Errorf("caching not parsed: %+v", tool.AnthropicToolAdvisor.Caching)
	}

	out, err := sonic.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(out)
	for _, want := range []string{`"type":"advisor_20260301"`, `"model":"claude-opus-4-8"`, `"max_tokens":2048`, `"ttl":"5m"`} {
		if !strings.Contains(s, want) {
			t.Errorf("marshaled JSON missing %q: %s", want, s)
		}
	}
}

// TestAdvisor_ResponsesRoundTrip verifies the advisor tool round-trips through
// the neutral ResponsesTool schema (Anthropic -> Bifrost -> Anthropic) with its
// type, name, and model intact.
func TestAdvisor_ResponsesRoundTrip(t *testing.T) {
	var tool AnthropicTool
	if err := sonic.Unmarshal([]byte(advisorToolJSON), &tool); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	bifrostTool := convertAnthropicToolToBifrost(&tool)
	if bifrostTool == nil {
		t.Fatal("convertAnthropicToolToBifrost returned nil")
	}
	if bifrostTool.Type != schemas.ResponsesToolTypeAdvisor {
		t.Fatalf("neutral type = %q, want advisor", bifrostTool.Type)
	}
	if bifrostTool.ResponsesToolAdvisor == nil || bifrostTool.ResponsesToolAdvisor.Model != "claude-opus-4-8" {
		t.Fatalf("neutral advisor model not carried: %+v", bifrostTool.ResponsesToolAdvisor)
	}
	if bifrostTool.ResponsesToolAdvisor.MaxUses == nil || *bifrostTool.ResponsesToolAdvisor.MaxUses != 3 {
		t.Fatalf("neutral advisor max_uses not carried: %+v", bifrostTool.ResponsesToolAdvisor.MaxUses)
	}
	if bifrostTool.ResponsesToolAdvisor.MaxTokens == nil || *bifrostTool.ResponsesToolAdvisor.MaxTokens != 2048 {
		t.Fatalf("neutral advisor max_tokens not carried: %+v", bifrostTool.ResponsesToolAdvisor.MaxTokens)
	}
	if bifrostTool.ResponsesToolAdvisor.Caching == nil ||
		bifrostTool.ResponsesToolAdvisor.Caching.Type != "ephemeral" ||
		bifrostTool.ResponsesToolAdvisor.Caching.TTL != "5m" {
		t.Fatalf("neutral advisor caching not carried: %+v", bifrostTool.ResponsesToolAdvisor.Caching)
	}

	back := convertBifrostToolToAnthropic("claude-sonnet-4-6", bifrostTool, schemas.Anthropic, false)
	if back == nil || back.Type == nil || *back.Type != AnthropicToolTypeAdvisor20260301 {
		t.Fatalf("rebuilt type wrong: %+v", back)
	}
	if back.Name != string(AnthropicToolNameAdvisor) {
		t.Errorf("rebuilt name = %q, want advisor", back.Name)
	}
	if back.AnthropicToolAdvisor == nil || back.AnthropicToolAdvisor.Model != "claude-opus-4-8" {
		t.Fatalf("rebuilt advisor model lost: %+v", back.AnthropicToolAdvisor)
	}
	if back.AnthropicToolAdvisor.MaxUses == nil || *back.AnthropicToolAdvisor.MaxUses != 3 {
		t.Fatalf("rebuilt advisor max_uses lost: %+v", back.AnthropicToolAdvisor.MaxUses)
	}
	if back.AnthropicToolAdvisor.MaxTokens == nil || *back.AnthropicToolAdvisor.MaxTokens != 2048 {
		t.Fatalf("rebuilt advisor max_tokens lost: %+v", back.AnthropicToolAdvisor.MaxTokens)
	}
	if back.AnthropicToolAdvisor.Caching == nil ||
		back.AnthropicToolAdvisor.Caching.Type != "ephemeral" ||
		back.AnthropicToolAdvisor.Caching.TTL != "5m" {
		t.Fatalf("rebuilt advisor caching lost: %+v", back.AnthropicToolAdvisor.Caching)
	}
}

// TestAdvisor_ProviderGating verifies advisor is supported only on Anthropic.
func TestAdvisor_ProviderGating(t *testing.T) {
	cases := []struct {
		provider schemas.ModelProvider
		want     bool
	}{
		{schemas.Anthropic, true},
		{schemas.Vertex, false},
		{schemas.Bedrock, false},
		{schemas.Azure, false},
	}
	for _, c := range cases {
		got := isAnthropicServerToolSupported(string(AnthropicToolTypeAdvisor20260301), ProviderFeatures[c.provider])
		if got != c.want {
			t.Errorf("isAnthropicServerToolSupported advisor on %s = %v, want %v", c.provider, got, c.want)
		}

		err := ValidateToolsForProvider([]schemas.ResponsesTool{{
			Type: schemas.ResponsesToolTypeAdvisor,
		}}, c.provider)
		if c.want && err != nil {
			t.Errorf("ValidateToolsForProvider advisor on %s returned error: %v", c.provider, err)
		}
		if !c.want && err == nil {
			t.Errorf("ValidateToolsForProvider advisor on %s should have errored", c.provider)
		}
	}
}

// TestAdvisor_RawToolGating verifies advisor tools in a raw passthrough body are
// rejected for every non-Anthropic Claude provider (Vertex, Bedrock, Azure) and
// allowed for Anthropic. Covers the raw path (unsupportedRawToolTypes), the
// counterpart to TestAdvisor_ProviderGating's normalized-path coverage.
func TestAdvisor_RawToolGating(t *testing.T) {
	rawBody := []byte(`{"model":"claude-opus-4-8","tools":[{"type":"advisor_20260301","model":"claude-opus-4-8"}]}`)
	for _, p := range []schemas.ModelProvider{schemas.Vertex, schemas.Bedrock, schemas.Azure} {
		if _, err := RemapRawToolVersionsForProvider(rawBody, p, "claude-opus-4-8"); err == nil {
			t.Errorf("RemapRawToolVersionsForProvider advisor on %s should have errored", p)
		}
	}
	if _, err := RemapRawToolVersionsForProvider(rawBody, schemas.Anthropic, "claude-opus-4-8"); err != nil {
		t.Errorf("RemapRawToolVersionsForProvider advisor on Anthropic returned error: %v", err)
	}
}

// TestAdvisor_BetaHeaderFiltering verifies the advisor beta header survives for
// Anthropic and is dropped for providers that don't support advisor.
func TestAdvisor_BetaHeaderFiltering(t *testing.T) {
	in := []string{AnthropicAdvisorBetaHeader}

	if got := FilterBetaHeadersForProvider(in, schemas.Anthropic); len(got) != 1 || got[0] != AnthropicAdvisorBetaHeader {
		t.Errorf("Anthropic should keep advisor header, got %v", got)
	}
	for _, p := range []schemas.ModelProvider{schemas.Vertex, schemas.Bedrock, schemas.Azure} {
		if got := FilterBetaHeadersForProvider(in, p); len(got) != 0 {
			t.Errorf("%s should drop advisor header, got %v", p, got)
		}
	}
}

// TestAdvisorResponse_RoundTripPreservesBlocks is the regression test for the
// user-reported bug: Anthropic -> Bifrost -> Anthropic dropped the advisor
// server_tool_use and advisor_tool_result blocks, leaving only the text block.
func TestAdvisorResponse_RoundTripPreservesBlocks(t *testing.T) {
	var resp AnthropicMessageResponse
	if err := sonic.Unmarshal([]byte(rawAdvisorResponse), &resp); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})

	bifrostResp := resp.ToBifrostResponsesResponse(ctx)
	if bifrostResp == nil {
		t.Fatal("ToBifrostResponsesResponse returned nil")
	}

	var foundAdvisor bool
	for _, out := range bifrostResp.Output {
		if out.Type != nil && *out.Type == schemas.ResponsesMessageTypeAdvisorCall {
			foundAdvisor = true
			if out.ResponsesToolMessage == nil || out.ResponsesToolMessage.ResponsesAdvisorCall == nil {
				t.Fatal("advisor_call present but ResponsesAdvisorCall payload missing")
			}
			adv := out.ResponsesToolMessage.ResponsesAdvisorCall
			if adv.ResultType != "advisor_result" {
				t.Errorf("advisor result_type = %q, want advisor_result", adv.ResultType)
			}
			if adv.Text == nil || *adv.Text != "Use a channel-based coordination pattern." {
				t.Errorf("advisor text not carried: %+v", adv.Text)
			}
		}
	}
	if !foundAdvisor {
		t.Fatal("neutral output has no advisor_call message — advisor blocks dropped on parse")
	}

	back := ToAnthropicResponsesResponse(ctx, bifrostResp)
	if back == nil {
		t.Fatal("ToAnthropicResponsesResponse returned nil")
	}
	out, err := sonic.Marshal(back)
	if err != nil {
		t.Fatalf("marshal back: %v", err)
	}

	blocks := gjson.GetBytes(out, "content")
	var types []string
	var advisorText, serverToolName, advisorToolUseID string
	blocks.ForEach(func(_, b gjson.Result) bool {
		bt := b.Get("type").String()
		types = append(types, bt)
		switch bt {
		case "server_tool_use":
			serverToolName = b.Get("name").String()
		case "advisor_tool_result":
			advisorToolUseID = b.Get("tool_use_id").String()
			// Anthropic types advisor_tool_result.content as a single object, not an array.
			if content := b.Get("content"); !content.IsObject() {
				t.Errorf("advisor_tool_result.content = %q, want a single object", content.Raw)
			}
			advisorText = b.Get("content.text").String()
		}
		return true
	})

	if len(types) != 3 || types[0] != "server_tool_use" || types[1] != "advisor_tool_result" || types[2] != "text" {
		t.Fatalf("converted content blocks = %v, want [server_tool_use advisor_tool_result text]\n%s", types, out)
	}
	if serverToolName != "advisor" {
		t.Errorf("server_tool_use name = %q, want advisor", serverToolName)
	}
	if advisorToolUseID != "srvtoolu_01WJ" {
		t.Errorf("advisor_tool_result tool_use_id = %q, want srvtoolu_01WJ", advisorToolUseID)
	}
	if advisorText != "Use a channel-based coordination pattern." {
		t.Errorf("advisor text not preserved through round-trip: %q", advisorText)
	}
}

// advisorStreamEvents is the user-reported raw Anthropic stream (trimmed): the
// advisor server_tool_use + advisor_tool_result arrive before the text block.
var advisorStreamEvents = []string{
	`{"type":"message_start","message":{"model":"claude-sonnet-4-6","id":"msg_019M","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":999,"output_tokens":35}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_01Y14","name":"advisor","input":{}}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"advisor_tool_result","tool_use_id":"srvtoolu_01Y14","content":{"type":"advisor_result","text":"Decide what graceful means."}}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"# Worker Pool"}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3777}}`,
	`{"type":"message_stop"}`,
}

// TestAdvisorStream_RoundTrip checks whether the advisor server_tool_use and
// advisor_tool_result blocks survive the streaming converter round-trip
// (Anthropic SSE -> Bifrost stream -> Anthropic SSE) on the normalized path.
func TestAdvisorStream_RoundTrip(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	state := newAdvisorStreamState()

	var emitted []*AnthropicStreamEvent
	seq := 0
	for _, raw := range advisorStreamEvents {
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

	var sawAdvisorServerToolUse, sawAdvisorResult, sawText bool
	var advisorText string
	for _, e := range emitted {
		if e.Type != AnthropicStreamEventTypeContentBlockStart || e.ContentBlock == nil {
			continue
		}
		switch e.ContentBlock.Type {
		case AnthropicContentBlockTypeServerToolUse:
			if e.ContentBlock.Name != nil && *e.ContentBlock.Name == string(AnthropicToolNameAdvisor) {
				sawAdvisorServerToolUse = true
			}
		case AnthropicContentBlockTypeAdvisorToolResult:
			sawAdvisorResult = true
			if e.ContentBlock.Content != nil && e.ContentBlock.Content.ContentObj != nil {
				if txt := e.ContentBlock.Content.ContentObj.Text; txt != nil {
					advisorText = *txt
				}
			}
		case AnthropicContentBlockTypeText:
			sawText = true
		}
	}

	if !sawAdvisorServerToolUse {
		t.Error("STREAM GAP: advisor server_tool_use block dropped")
	}
	if !sawAdvisorResult {
		t.Error("STREAM GAP: advisor_tool_result block dropped")
	}
	if !strings.Contains(advisorText, "graceful") {
		t.Errorf("STREAM GAP: advisor result text lost (got %q)", advisorText)
	}
	if !sawText {
		t.Error("text block dropped")
	}
}

// countPassthroughMessageStarts mirrors the transport passthrough converter
// (transports/bifrost-http/integrations/anthropic.go): for each bifrost stream
// response it forwards the raw upstream frame verbatim when present (except
// ContentPartAdded), otherwise falls back to the normalized converter. Returns
// how many message_start frames are emitted and the raw bytes of the forwarded one.
func countPassthroughMessageStarts(ctx *schemas.BifrostContext, responses []*schemas.BifrostResponsesStreamResponse) (int, string) {
	count := 0
	forwarded := ""
	for _, r := range responses {
		if r.ExtraFields.RawResponse != nil && r.Type != schemas.ResponsesStreamResponseTypeContentPartAdded {
			raw, _ := r.ExtraFields.RawResponse.(string)
			if gjson.Get(raw, "type").String() == "message_start" {
				count++
				forwarded = raw
			}
			continue
		}
		for _, ev := range ToAnthropicResponsesStreamResponse(ctx, r) {
			if ev.Type == AnthropicStreamEventTypeMessageStart {
				count++
			}
		}
	}
	return count, forwarded
}

// TestResponsesStream_MessageStart_NoDuplicateOnPassthrough guards the fix for the
// duplicate message_start emitted on the Anthropic passthrough responses_stream
// path. A single upstream message_start expands inbound into [response.created,
// response.in_progress]. The raw upstream frame must ride on response.created
// (which maps back to message_start) so it is forwarded once with all upstream
// fields intact; response.in_progress must carry no raw and convert to nil.
// Attaching the raw to in_progress instead (the old "last chunk" rule) produced
// two message_start frames: a lossy synthesized one from created plus the
// raw-forwarded one from in_progress.
func TestResponsesStream_MessageStart_NoDuplicateOnPassthrough(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})

	const rawMessageStart = `{"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-opus-4-8","content":[],"stop_reason":null,"usage":{"input_tokens":3450,"output_tokens":4,"service_tier":"standard","inference_geo":"not_available"}}}`

	var chunk AnthropicStreamEvent
	if err := sonic.Unmarshal([]byte(rawMessageStart), &chunk); err != nil {
		t.Fatalf("unmarshal message_start: %v", err)
	}

	state := acquireAnthropicResponsesStreamState()
	defer releaseAnthropicResponsesStreamState(state)

	responses, bErr, _ := chunk.ToBifrostResponsesStream(ctx, 0, state)
	if bErr != nil {
		t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
	}

	// Inbound must expand the single message_start into [created, in_progress].
	if len(responses) != 2 {
		t.Fatalf("expected 2 bifrost responses (created, in_progress), got %d", len(responses))
	}
	createdIdx := -1
	for i, r := range responses {
		if r.Type == schemas.ResponsesStreamResponseTypeCreated {
			createdIdx = i
		}
	}
	if createdIdx == -1 {
		t.Fatal("expected a response.created in the message_start expansion")
	}
	// The fix attaches the raw to response.created; the old rule attached it to the
	// last response (in_progress). Confirm they differ so the cases below exercise it.
	if createdIdx == len(responses)-1 {
		t.Fatal("response.created is last; raw attachment would not exercise the fix")
	}

	t.Run("raw on created emits one full message_start", func(t *testing.T) {
		for _, r := range responses {
			r.ExtraFields.RawResponse = nil
		}
		responses[createdIdx].ExtraFields.RawResponse = rawMessageStart

		count, forwarded := countPassthroughMessageStarts(ctx, responses)
		if count != 1 {
			t.Fatalf("expected exactly 1 message_start, got %d", count)
		}
		// The surviving frame must be the verbatim upstream bytes, preserving
		// service_tier/inference_geo that the synthesized frame drops.
		if forwarded != rawMessageStart {
			t.Errorf("forwarded message_start is not the verbatim upstream frame: %s", forwarded)
		}
	})

	t.Run("raw on in_progress (old behavior) duplicates message_start", func(t *testing.T) {
		for _, r := range responses {
			r.ExtraFields.RawResponse = nil
		}
		responses[len(responses)-1].ExtraFields.RawResponse = rawMessageStart

		count, _ := countPassthroughMessageStarts(ctx, responses)
		if count != 2 {
			t.Fatalf("expected the old last-chunk rule to duplicate message_start (2), got %d", count)
		}
	})
}
