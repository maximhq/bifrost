package anthropic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestChatTool_ServerToolRoundTrip verifies that every Anthropic server-tool
// variant survives Marshal/Unmarshal through the neutral ChatTool schema.
// This locks in the fix for the user-reported bug where a raw JSON tool like
// {"type":"web_search_20260209","name":"web_search","max_uses":5} was being
// dropped at the neutral-schema layer because ChatTool had no slots for the
// server-tool metadata.
func TestChatTool_ServerToolRoundTrip(t *testing.T) {
	five := 5
	ptrTrue := true
	w, h := 1280, 800
	maxChars := 16000
	maxContent := 32000

	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "web_search_20260209",
			raw:  `{"type":"web_search_20260209","name":"web_search","max_uses":5,"allowed_callers":["direct"]}`,
		},
		{
			name: "web_search_with_domains",
			raw:  `{"type":"web_search_20250305","name":"web_search","allowed_domains":["example.com","docs.example.com"]}`,
		},
		{
			name: "web_search_with_user_location",
			raw:  `{"type":"web_search_20250305","name":"web_search","user_location":{"type":"approximate","city":"San Francisco","country":"US","timezone":"America/Los_Angeles"}}`,
		},
		{
			name: "web_fetch_20260309",
			raw:  `{"type":"web_fetch_20260309","name":"web_fetch","max_uses":5,"max_content_tokens":32000,"citations":{"enabled":true},"use_cache":true}`,
		},
		{
			name: "computer_20251124",
			raw:  `{"type":"computer_20251124","name":"computer","display_width_px":1280,"display_height_px":800,"display_number":1,"enable_zoom":true}`,
		},
		{
			name: "text_editor_20250728",
			raw:  `{"type":"text_editor_20250728","name":"str_replace_based_edit_tool","max_characters":16000}`,
		},
		{
			name: "bash_20250124",
			raw:  `{"type":"bash_20250124","name":"bash"}`,
		},
		{
			name: "memory_20250818",
			raw:  `{"type":"memory_20250818","name":"memory"}`,
		},
		{
			name: "code_execution_20250825",
			raw:  `{"type":"code_execution_20250825","name":"code_execution"}`,
		},
		{
			name: "tool_search_tool_bm25",
			raw:  `{"type":"tool_search_tool_bm25","name":"tool_search_tool_bm25"}`,
		},
		{
			name: "mcp_toolset",
			raw:  `{"type":"mcp_toolset","name":"my_mcp","mcp_server_name":"notion","configs":{"search":{"enabled":true}}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Variant-specific field assertions. Invoked twice — once after
			// initial decode, once after round-trip — so that a regression in
			// MarshalSorted that silently drops any variant-specific field
			// fails this test instead of sneaking through.
			assertVariantFields := func(label string, tl schemas.ChatTool) {
				t.Helper()
				switch tc.name {
				case "web_search_20260209":
					if tl.MaxUses == nil || *tl.MaxUses != five {
						t.Errorf("%s: MaxUses not preserved, got %v", label, tl.MaxUses)
					}
					if len(tl.AllowedCallers) != 1 || tl.AllowedCallers[0] != "direct" {
						t.Errorf("%s: AllowedCallers not preserved, got %v", label, tl.AllowedCallers)
					}
				case "web_fetch_20260309":
					if tl.MaxContentTokens == nil || *tl.MaxContentTokens != maxContent {
						t.Errorf("%s: MaxContentTokens not preserved, got %v", label, tl.MaxContentTokens)
					}
					if tl.Citations == nil || tl.Citations.Enabled == nil || !*tl.Citations.Enabled {
						t.Errorf("%s: Citations not preserved, got %v", label, tl.Citations)
					}
					if tl.UseCache == nil || !*tl.UseCache {
						t.Errorf("%s: UseCache not preserved", label)
					}
					_ = ptrTrue
				case "computer_20251124":
					if tl.DisplayWidthPx == nil || *tl.DisplayWidthPx != w {
						t.Errorf("%s: DisplayWidthPx not preserved, got %v", label, tl.DisplayWidthPx)
					}
					if tl.DisplayHeightPx == nil || *tl.DisplayHeightPx != h {
						t.Errorf("%s: DisplayHeightPx not preserved, got %v", label, tl.DisplayHeightPx)
					}
				case "text_editor_20250728":
					if tl.MaxCharacters == nil || *tl.MaxCharacters != maxChars {
						t.Errorf("%s: MaxCharacters not preserved, got %v", label, tl.MaxCharacters)
					}
				case "mcp_toolset":
					if tl.MCPServerName != "notion" {
						t.Errorf("%s: MCPServerName not preserved, got %q", label, tl.MCPServerName)
					}
					if len(tl.Configs) != 1 {
						t.Errorf("%s: Configs not preserved, got %v", label, tl.Configs)
					}
				}
			}

			var tool schemas.ChatTool
			if err := sonic.Unmarshal([]byte(tc.raw), &tool); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if string(tool.Type) == "" {
				t.Errorf("Type should be preserved, got empty")
			}
			if tool.Name == "" {
				t.Errorf("Name should be preserved, got empty")
			}
			assertVariantFields("first decode", tool)

			// Re-marshal and re-decode — all preserved fields should survive round trip.
			out, err := schemas.MarshalSorted(tool)
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}
			var tool2 schemas.ChatTool
			if err := sonic.Unmarshal(out, &tool2); err != nil {
				t.Fatalf("second unmarshal failed: %v\njson: %s", err, string(out))
			}
			if tool.Name != tool2.Name || tool.Type != tool2.Type {
				t.Errorf("round-trip mismatch\n  in: %s\n  out: %s", tc.raw, string(out))
			}
			assertVariantFields("round trip", tool2)
		})
	}
}

// TestToAnthropicChatRequest_ServerTools verifies every ChatTool server-tool
// shape converts correctly through ToAnthropicChatRequest.
func TestToAnthropicChatRequest_ServerTools(t *testing.T) {
	mk := func(rawTool string) *schemas.BifrostChatRequest {
		var tool schemas.ChatTool
		if err := sonic.Unmarshal([]byte(rawTool), &tool); err != nil {
			t.Fatalf("test setup: %v", err)
		}
		return &schemas.BifrostChatRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-sonnet-4-6",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
			Params:   &schemas.ChatParameters{Tools: []schemas.ChatTool{tool}},
		}
	}

	type check struct {
		expectName       string
		expectType       AnthropicToolType
		expectWebSearch  bool
		expectWebFetch   bool
		expectComputer   bool
		expectTextEditor bool
		expectMCPToolset bool
	}

	cases := []struct {
		name string
		raw  string
		want check
	}{
		{
			name: "web_search",
			raw:  `{"type":"web_search_20260209","name":"web_search","max_uses":5}`,
			want: check{expectName: "web_search", expectType: "web_search_20260209", expectWebSearch: true},
		},
		{
			name: "web_fetch",
			raw:  `{"type":"web_fetch_20260309","name":"web_fetch","max_uses":3,"use_cache":true}`,
			want: check{expectName: "web_fetch", expectType: "web_fetch_20260309", expectWebFetch: true},
		},
		{
			name: "computer_20251124",
			raw:  `{"type":"computer_20251124","name":"computer","display_width_px":1280,"display_height_px":800}`,
			want: check{expectName: "computer", expectType: "computer_20251124", expectComputer: true},
		},
		{
			name: "text_editor_20250728",
			raw:  `{"type":"text_editor_20250728","name":"str_replace_based_edit_tool","max_characters":16000}`,
			want: check{expectName: "str_replace_based_edit_tool", expectType: "text_editor_20250728", expectTextEditor: true},
		},
		{
			name: "bash_20250124",
			raw:  `{"type":"bash_20250124","name":"bash"}`,
			want: check{expectName: "bash", expectType: "bash_20250124"},
		},
		{
			name: "mcp_toolset",
			raw:  `{"type":"mcp_toolset","name":"notion","mcp_server_name":"notion"}`,
			want: check{expectMCPToolset: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()

			req := mk(tc.raw)
			out, err := ToAnthropicChatRequest(ctx, req)
			if err != nil {
				t.Fatalf("conversion failed: %v", err)
			}
			if len(out.Tools) != 1 {
				t.Fatalf("expected 1 tool, got %d (raw: %s)", len(out.Tools), tc.raw)
			}
			at := out.Tools[0]
			if tc.want.expectMCPToolset {
				if at.MCPToolset == nil {
					t.Errorf("expected MCPToolset to be set")
				}
				return
			}
			if at.Name != tc.want.expectName {
				t.Errorf("Name: got %q want %q", at.Name, tc.want.expectName)
			}
			if at.Type == nil || *at.Type != tc.want.expectType {
				t.Errorf("Type: got %v want %q", at.Type, tc.want.expectType)
			}
			if tc.want.expectWebSearch && at.AnthropicToolWebSearch == nil {
				t.Errorf("expected AnthropicToolWebSearch populated")
			}
			if tc.want.expectWebFetch && at.AnthropicToolWebFetch == nil {
				t.Errorf("expected AnthropicToolWebFetch populated")
			}
			if tc.want.expectComputer && at.AnthropicToolComputerUse == nil {
				t.Errorf("expected AnthropicToolComputerUse populated")
			}
			if tc.want.expectTextEditor && at.AnthropicToolTextEditor == nil {
				t.Errorf("expected AnthropicToolTextEditor populated")
			}
		})
	}
}

// TestToBifrostResponsesRequest_MCPToolsetPreservesAnthropicFlags verifies
// that when an Anthropic request carries an mcp_toolset tool with the four
// Anthropic-native flags (DeferLoading, AllowedCallers, InputExamples,
// EagerInputStreaming), those flags survive the inbound conversion into the
// neutral ResponsesTool on the mcp_servers merge path. Before the fix, the
// merge path only applied MCP configs (allowlist/cache-control) and dropped
// the flags because convertAnthropicToolToBifrost skips mcp_toolset entries.
func TestToBifrostResponsesRequest_MCPToolsetPreservesAnthropicFlags(t *testing.T) {
	toolsetType := "mcp_toolset"
	_ = toolsetType // shape documentation only; AnthropicTool.Type is pointer-to-enum and left nil for mcp_toolset

	req := &AnthropicMessageRequest{
		Model: "claude-sonnet-4-6",
		Tools: []AnthropicTool{
			{
				Name:                "notion",
				DeferLoading:        schemas.Ptr(true),
				AllowedCallers:      []string{"direct", "agent"},
				EagerInputStreaming: schemas.Ptr(false),
				InputExamples: []AnthropicToolInputExample{
					{Input: json.RawMessage(`{"q":"hello"}`), Description: schemas.Ptr("basic")},
				},
				MCPToolset: &AnthropicMCPToolsetTool{
					Type:          "mcp_toolset",
					MCPServerName: "notion",
					DefaultConfig: &AnthropicMCPToolsetConfig{Enabled: schemas.Ptr(true)},
				},
			},
		},
		MCPServers: []AnthropicMCPServerV2{
			{Type: "url", URL: "https://mcp.example.com", Name: "notion"},
		},
	}

	got := req.ToBifrostResponsesRequest(nil)
	if got == nil || got.Params == nil {
		t.Fatalf("ToBifrostResponsesRequest returned nil params")
	}

	// The mcp_toolset tool should have been dropped by convertAnthropicToolToBifrost
	// and re-created on the mcp_servers merge path — end result: exactly one tool,
	// of type mcp, carrying the Anthropic flags we set.
	if len(got.Params.Tools) != 1 {
		t.Fatalf("expected 1 mcp tool after merge, got %d", len(got.Params.Tools))
	}
	mcp := got.Params.Tools[0]
	if mcp.Type != schemas.ResponsesToolTypeMCP {
		t.Errorf("expected MCP tool, got type=%q", mcp.Type)
	}
	if mcp.DeferLoading == nil || !*mcp.DeferLoading {
		t.Errorf("DeferLoading dropped on mcp_toolset merge path")
	}
	if len(mcp.AllowedCallers) != 2 || mcp.AllowedCallers[0] != "direct" {
		t.Errorf("AllowedCallers dropped on mcp_toolset merge path, got %v", mcp.AllowedCallers)
	}
	if len(mcp.InputExamples) != 1 {
		t.Errorf("InputExamples dropped on mcp_toolset merge path, got len=%d", len(mcp.InputExamples))
	}
	if mcp.EagerInputStreaming == nil || *mcp.EagerInputStreaming {
		t.Errorf("EagerInputStreaming dropped on mcp_toolset merge path, got %v", mcp.EagerInputStreaming)
	}
}

// TestToAnthropicChatRequest_ServerTools_ReproUserBug is the exact shape
// from the reported curl — web_search_20260209 with max_uses + allowed_callers.
// Verifies the request reaches ToAnthropicChatRequest output with a populated
// tools array (previously it was silently dropped).
func TestToAnthropicChatRequest_ServerTools_ReproUserBug(t *testing.T) {
	raw := []byte(`{
      "model":"claude-sonnet-4-6",
      "messages":[{"role":"user","content":"What is the weather in SF?"}],
      "tools":[{"name":"web_search","type":"web_search_20260209","max_uses":5,"allowed_callers":["direct"]}]
    }`)
	// Unmarshal through the neutral schema the way the OpenAI endpoint does.
	var inner struct {
		Model    string             `json:"model"`
		Messages []json.RawMessage  `json:"messages"`
		Tools    []schemas.ChatTool `json:"tools"`
	}
	if err := sonic.Unmarshal(raw, &inner); err != nil {
		t.Fatalf("outer unmarshal: %v", err)
	}
	if len(inner.Tools) != 1 {
		t.Fatalf("setup: expected 1 tool in raw JSON, got %d", len(inner.Tools))
	}
	if inner.Tools[0].Name == "" {
		t.Errorf("Name lost at neutral-schema decode (was the bug). Got: %+v", inner.Tools[0])
	}
	if inner.Tools[0].MaxUses == nil {
		t.Errorf("MaxUses lost at neutral-schema decode (was the bug)")
	}

	req := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    inner.Model,
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
		Params:   &schemas.ChatParameters{Tools: inner.Tools},
	}
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	out, err := ToAnthropicChatRequest(ctx, req)
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	if len(out.Tools) != 1 {
		t.Fatalf("repro bug: expected 1 tool after conversion, got %d (tools array was empty — this was the bug)", len(out.Tools))
	}
	if out.Tools[0].Name != "web_search" {
		t.Errorf("tool Name: got %q, want %q", out.Tools[0].Name, "web_search")
	}
	if out.Tools[0].AnthropicToolWebSearch == nil ||
		out.Tools[0].AnthropicToolWebSearch.MaxUses == nil ||
		*out.Tools[0].AnthropicToolWebSearch.MaxUses != 5 {
		t.Errorf("tool max_uses lost: %+v", out.Tools[0])
	}
}

// rawWebSearchChatResponse is a non-streaming Anthropic response to a request
// that enabled the web_search server tool. The search runs on Anthropic's side:
// server_tool_use and web_search_tool_result are not OpenAI tool calls, and the
// cited text block carries web_search_result_location citations.
var rawWebSearchChatResponse = `{
  "model": "claude-sonnet-4-6",
  "id": "msg_websearch",
  "type": "message",
  "role": "assistant",
  "stop_reason": "end_turn",
  "content": [
    {"type":"text","text":"I'll search for that."},
    {"type":"server_tool_use","id":"srvtoolu_01","name":"web_search","input":{"query":"bbc top headline"}},
    {"type":"web_search_tool_result","tool_use_id":"srvtoolu_01","content":[{"type":"web_search_result","url":"https://www.bbc.com/news/a","title":"Story A","encrypted_content":"EqQ...","page_age":"1 hour ago"}]},
    {"type":"text","text":"The top story is A.","citations":[
      {"type":"web_search_result_location","url":"https://www.bbc.com/news/a","title":"Story A","encrypted_index":"Eo8...","cited_text":"Story A text..."},
      {"type":"web_search_result_location","url":"https://www.bbc.com/news/a","title":"Story A","encrypted_index":"Eo9...","cited_text":"Story A text again..."},
      {"type":"char_location","cited_text":"doc text","document_index":0,"start_char_index":0,"end_char_index":8}
    ]},
    {"type":"text","text":" Nothing else is notable."}
  ],
  "usage": {"input_tokens": 10, "output_tokens": 20, "server_tool_use": {"web_search_requests": 1}}
}`

const (
	wsIntro   = "I'll search for that."
	wsCited   = "The top story is A."
	wsTrailer = " Nothing else is notable."
	wsURL     = "https://www.bbc.com/news/a"
	wsTitle   = "Story A"
)

// Web search citations on a non-streamed response become one deduplicated
// url_citation annotation spanning the cited block, and the server-side search
// never appears as a tool call.
func TestWebSearchChat_NonStream_CitationsBecomeAnnotations(t *testing.T) {
	var anthropicResp AnthropicMessageResponse
	if err := sonic.Unmarshal([]byte(rawWebSearchChatResponse), &anthropicResp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	bifrostResp := anthropicResp.ToBifrostChatResponse(&schemas.BifrostContext{})
	if bifrostResp == nil || len(bifrostResp.Choices) != 1 {
		t.Fatal("ToBifrostChatResponse returned no choice")
	}
	msg := bifrostResp.Choices[0].Message
	if msg.Content == nil || msg.Content.ContentStr == nil {
		t.Fatalf("content did not collapse to a string: %+v", msg.Content)
	}
	content := *msg.Content.ContentStr
	if want := wsIntro + wsCited + wsTrailer; content != want {
		t.Fatalf("content = %q, want %q", content, want)
	}
	if msg.ChatAssistantMessage == nil {
		t.Fatal("expected an assistant message carrying annotations")
	}
	if len(msg.ChatAssistantMessage.ToolCalls) != 0 {
		t.Errorf("server-side web search leaked as tool_calls: %+v", msg.ChatAssistantMessage.ToolCalls)
	}

	anns := msg.ChatAssistantMessage.Annotations
	if len(anns) != 1 {
		t.Fatalf("expected 1 deduplicated url_citation annotation, got %d: %+v", len(anns), anns)
	}
	ann := anns[0]
	if ann.Type != "url_citation" {
		t.Errorf("annotation type = %q, want url_citation", ann.Type)
	}
	if ann.URLCitation.URL == nil || *ann.URLCitation.URL != wsURL {
		t.Errorf("annotation url = %v, want %q", ann.URLCitation.URL, wsURL)
	}
	if ann.URLCitation.Title != wsTitle {
		t.Errorf("annotation title = %q, want %q", ann.URLCitation.Title, wsTitle)
	}
	wantStart, wantEnd := len(wsIntro), len(wsIntro)+len(wsCited)
	if ann.URLCitation.StartIndex != wantStart || ann.URLCitation.EndIndex != wantEnd {
		t.Errorf("annotation span = [%d,%d), want [%d,%d)", ann.URLCitation.StartIndex, ann.URLCitation.EndIndex, wantStart, wantEnd)
	}
	if got := content[ann.URLCitation.StartIndex:ann.URLCitation.EndIndex]; got != wsCited {
		t.Errorf("span selects %q, want %q", got, wsCited)
	}

	if bifrostResp.Choices[0].FinishReason == nil || *bifrostResp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %v, want stop", bifrostResp.Choices[0].FinishReason)
	}

	out, err := sonic.Marshal(bifrostResp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if !strings.Contains(string(out), `"annotations":[{"type":"url_citation"`) {
		t.Errorf("wire response missing url_citation annotations: %s", out)
	}
}

// A real function tool_use in the same turn as a web search must still come
// back as an OpenAI tool call.
func TestWebSearchChat_NonStream_RealToolUseStillToolCall(t *testing.T) {
	raw := `{
  "model": "claude-sonnet-4-6",
  "id": "msg_mixed",
  "type": "message",
  "role": "assistant",
  "stop_reason": "tool_use",
  "content": [
    {"type":"server_tool_use","id":"srvtoolu_01","name":"web_search","input":{"query":"weather"}},
    {"type":"web_search_tool_result","tool_use_id":"srvtoolu_01","content":[]},
    {"type":"text","text":"Checking.","citations":[{"type":"web_search_result_location","url":"https://example.com/w","title":"W","cited_text":"w"}]},
    {"type":"tool_use","id":"toolu_01","name":"get_weather","input":{"city":"Paris"}}
  ],
  "usage": {"input_tokens": 10, "output_tokens": 20}
}`
	var anthropicResp AnthropicMessageResponse
	if err := sonic.Unmarshal([]byte(raw), &anthropicResp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	msg := anthropicResp.ToBifrostChatResponse(&schemas.BifrostContext{}).Choices[0].Message
	if msg.ChatAssistantMessage == nil || len(msg.ChatAssistantMessage.ToolCalls) != 1 {
		t.Fatalf("expected exactly the one real tool call, got %+v", msg.ChatAssistantMessage)
	}
	tc := msg.ChatAssistantMessage.ToolCalls[0]
	if tc.ID == nil || *tc.ID != "toolu_01" || tc.Function.Name == nil || *tc.Function.Name != "get_weather" {
		t.Errorf("unexpected tool call: %+v", tc)
	}
	if tc.Function.Arguments != `{"city":"Paris"}` {
		t.Errorf("tool call arguments = %q", tc.Function.Arguments)
	}
	anns := msg.ChatAssistantMessage.Annotations
	if len(anns) != 1 || anns[0].URLCitation.StartIndex != 0 || anns[0].URLCitation.EndIndex != len("Checking.") {
		t.Errorf("unexpected annotations: %+v", anns)
	}
}

// A structured-output tool_use next to cited text must not leave citation spans
// pointing outside the returned content: whichever string becomes the content,
// every annotation span has to select the cited text inside it.
func TestWebSearchChat_NonStream_StructuredOutputKeepsSpansInContent(t *testing.T) {
	const soToolName = "bf_so_answer"
	raw := `{
  "model": "claude-sonnet-4-6",
  "id": "msg_so",
  "type": "message",
  "role": "assistant",
  "stop_reason": "tool_use",
  "content": [
    {"type":"server_tool_use","id":"srvtoolu_01","name":"web_search","input":{"query":"bbc"}},
    {"type":"web_search_tool_result","tool_use_id":"srvtoolu_01","content":[]},
    {"type":"text","text":"The top story is A.","citations":[{"type":"web_search_result_location","url":"https://www.bbc.com/news/a","title":"Story A","cited_text":"a"}]},
    {"type":"tool_use","id":"toolu_so","name":"` + soToolName + `","input":{"headline":"A"}}
  ],
  "usage": {"input_tokens": 10, "output_tokens": 20}
}`
	var anthropicResp AnthropicMessageResponse
	if err := sonic.Unmarshal([]byte(raw), &anthropicResp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, soToolName)

	msg := anthropicResp.ToBifrostChatResponse(ctx).Choices[0].Message
	if msg.Content == nil || msg.Content.ContentStr == nil {
		t.Fatalf("content did not collapse to a string: %+v", msg.Content)
	}
	content := *msg.Content.ContentStr
	if msg.ChatAssistantMessage == nil {
		t.Fatal("expected an assistant message")
	}
	for _, ann := range msg.ChatAssistantMessage.Annotations {
		if ann.URLCitation.StartIndex < 0 || ann.URLCitation.EndIndex > len(content) || ann.URLCitation.StartIndex > ann.URLCitation.EndIndex {
			t.Fatalf("annotation span [%d,%d) is outside content %q", ann.URLCitation.StartIndex, ann.URLCitation.EndIndex, content)
		}
		if got := content[ann.URLCitation.StartIndex:ann.URLCitation.EndIndex]; got != "The top story is A." {
			t.Errorf("span selects %q, want the cited text", got)
		}
	}
}

// wsStreamResult collects what the OpenAI-format stream carries.
type wsStreamResult struct {
	content     string
	toolCalls   []schemas.ChatAssistantMessageToolCall
	annotations []schemas.ChatAssistantMessageAnnotation
	// annotationAfterEvent is the event index that produced each annotation chunk.
	annotationAfterEvent []int
}

// runWebSearchStream feeds raw Anthropic SSE event payloads through
// ToBifrostChatCompletionStream and collects the OpenAI-format content, tool
// call deltas and annotations it emits.
func runWebSearchStream(t *testing.T, events []string) wsStreamResult {
	t.Helper()
	state := NewAnthropicStreamState()
	var res wsStreamResult
	for i, raw := range events {
		var ev AnthropicStreamEvent
		if err := sonic.Unmarshal([]byte(raw), &ev); err != nil {
			t.Fatalf("unmarshal event %d: %v", i, err)
		}
		resp, bErr, _ := ev.ToBifrostChatCompletionStream(nil, "", state)
		if bErr != nil {
			t.Fatalf("event %d: unexpected error %+v", i, bErr)
		}
		if resp == nil {
			continue
		}
		delta := resp.Choices[0].ChatStreamResponseChoice.Delta
		if delta.Content != nil {
			res.content += *delta.Content
		}
		res.toolCalls = append(res.toolCalls, delta.ToolCalls...)
		if len(delta.Annotations) > 0 {
			res.annotations = append(res.annotations, delta.Annotations...)
			res.annotationAfterEvent = append(res.annotationAfterEvent, i)
		}
	}
	return res
}

var webSearchStreamEvents = []string{
	`{"type":"message_start","message":{"id":"msg_ws","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I'll search "}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"for that."}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu_01","name":"web_search","input":{}}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query"}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\": \"bbc top headline\"}"}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01","content":[{"type":"web_search_result","url":"https://www.bbc.com/news/a","title":"Story A","encrypted_content":"EqQ...","page_age":"1 hour ago"}]}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"content_block_start","index":3,"content_block":{"type":"text","text":"","citations":[]}}`,
	`{"type":"content_block_delta","index":3,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://www.bbc.com/news/a","title":"Story A","encrypted_index":"Eo8...","cited_text":"Story A text..."}}}`,
	`{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"The top story is A."}}`,
	`{"type":"content_block_stop","index":3}`,
	`{"type":"content_block_start","index":4,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":4,"delta":{"type":"text_delta","text":" Nothing else is notable."}}`,
	`{"type":"content_block_stop","index":4}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20,"server_tool_use":{"web_search_requests":1}}}`,
	`{"type":"message_stop"}`,
}

// A streamed web search emits no tool_calls delta for the server_tool_use
// input, and the cited block's annotation arrives at that block's stop.
func TestWebSearchChat_Stream_NoToolCallLeakAndAnnotationAtBlockStop(t *testing.T) {
	res := runWebSearchStream(t, webSearchStreamEvents)

	if len(res.toolCalls) != 0 {
		t.Fatalf("server_tool_use leaked %d tool_calls deltas: %+v", len(res.toolCalls), res.toolCalls)
	}
	if want := wsIntro + wsCited + wsTrailer; res.content != want {
		t.Fatalf("content = %q, want %q", res.content, want)
	}
	if len(res.annotations) != 1 {
		t.Fatalf("expected 1 annotation, got %d: %+v", len(res.annotations), res.annotations)
	}
	// Emitted on the cited block's content_block_stop (event 15), not later.
	if len(res.annotationAfterEvent) != 1 || res.annotationAfterEvent[0] != 15 {
		t.Errorf("annotation emitted after events %v, want [15] (index 3 content_block_stop)", res.annotationAfterEvent)
	}
	ann := res.annotations[0]
	if ann.Type != "url_citation" || ann.URLCitation.URL == nil || *ann.URLCitation.URL != wsURL || ann.URLCitation.Title != wsTitle {
		t.Errorf("unexpected annotation: %+v", ann)
	}
	wantStart, wantEnd := len(wsIntro), len(wsIntro)+len(wsCited)
	if ann.URLCitation.StartIndex != wantStart || ann.URLCitation.EndIndex != wantEnd {
		t.Errorf("annotation span = [%d,%d), want [%d,%d)", ann.URLCitation.StartIndex, ann.URLCitation.EndIndex, wantStart, wantEnd)
	}
	if got := res.content[ann.URLCitation.StartIndex:ann.URLCitation.EndIndex]; got != wsCited {
		t.Errorf("span selects %q, want %q", got, wsCited)
	}
}

// A real function tool_use streamed after a web search keeps its tool call
// index 0 and its full arguments; the server_tool_use input never mixes in.
func TestWebSearchChat_Stream_RealToolUseUnaffected(t *testing.T) {
	events := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_01","name":"web_search","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\": \"weather\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01","content":[]}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_stop"}`,
	}
	res := runWebSearchStream(t, events)

	if len(res.toolCalls) != 3 {
		t.Fatalf("expected setup + 2 argument deltas for the real tool, got %d: %+v", len(res.toolCalls), res.toolCalls)
	}
	setup := res.toolCalls[0]
	if setup.Index != 0 || setup.ID == nil || *setup.ID != "toolu_01" || setup.Function.Name == nil || *setup.Function.Name != "get_weather" {
		t.Errorf("unexpected setup chunk: %+v", setup)
	}
	var args string
	for _, tc := range res.toolCalls {
		if tc.Index != 0 {
			t.Errorf("tool call delta has index %d, want 0", tc.Index)
		}
		args += tc.Function.Arguments
	}
	if args != `{"city":"Paris"}` {
		t.Errorf("accumulated arguments = %q, want {\"city\":\"Paris\"}", args)
	}
	if len(res.annotations) != 0 {
		t.Errorf("unexpected annotations: %+v", res.annotations)
	}
}

// Non-web citation types are ignored without error, and a text block without
// citations emits nothing extra on stop.
func TestWebSearchChat_Stream_OtherCitationTypesIgnored(t *testing.T) {
	events := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"char_location","cited_text":"x","document_index":0,"start_char_index":0,"end_char_index":1}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"From the doc."}}`,
		`{"type":"content_block_stop","index":0}`,
	}
	res := runWebSearchStream(t, events)
	if res.content != "From the doc." {
		t.Errorf("content = %q", res.content)
	}
	if len(res.annotations) != 0 || len(res.toolCalls) != 0 {
		t.Errorf("expected no annotations or tool calls, got %+v / %+v", res.annotations, res.toolCalls)
	}
}
