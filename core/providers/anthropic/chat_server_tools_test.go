package anthropic

import (
	"encoding/json"
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
			req := mk(tc.raw)
			out, err := ToAnthropicChatRequest(nil, req)
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
	out, err := ToAnthropicChatRequest(nil, req)
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
