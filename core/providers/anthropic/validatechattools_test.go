package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestValidateChatToolsForProvider locks in the partition:
// function/custom tools always survive; server tools survive only when the
// target provider's ProviderFeatures flag is true for that tool type.
func TestValidateChatToolsForProvider(t *testing.T) {
	fnTool := schemas.ChatTool{
		Type:     schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{Name: "get_weather"},
	}
	serverTool := func(tpe, name string) schemas.ChatTool {
		return schemas.ChatTool{Type: schemas.ChatToolType(tpe), Name: name}
	}

	cases := []struct {
		name        string
		provider    schemas.ModelProvider
		input       []schemas.ChatTool
		wantKeep    int
		wantDropped []string
		assertNotes string
	}{
		{
			name:     "function tools always survive on any provider",
			provider: schemas.Bedrock,
			input:    []schemas.ChatTool{fnTool, fnTool},
			wantKeep: 2,
		},
		{
			name:     "bedrock keeps web_search + code_execution via Nova carve-out, matching the Responses-path validator",
			provider: schemas.Bedrock,
			input: []schemas.ChatTool{
				serverTool("web_search_20260209", "web_search"),
				serverTool("code_execution_20250825", "code_execution"),
			},
			// WebSearch/CodeExecution are false for Bedrock, but WebSearchNova/
			// CodeExecNova are true — ValidateResponsesToolsForProvider already ORs
			// these in; the Chat path must match, or a Nova2 request loses the tool
			// at validation time before it ever reaches the Nova-system-tool
			// conversion in convertToolConfigFromFiltered.
			wantKeep:    2,
			assertNotes: "Bedrock has WebSearch=false, WebSearchNova=true (nova_grounding); CodeExecution=false, CodeExecNova=true (nova_code_interpreter)",
		},
		{
			name:     "bedrock drops web_fetch + mcp_toolset",
			provider: schemas.Bedrock,
			input: []schemas.ChatTool{
				serverTool("web_fetch_20260309", "web_fetch"),
				serverTool("mcp_toolset", "notion"),
			},
			wantKeep:    0,
			wantDropped: []string{"web_fetch_20260309", "mcp_toolset"},
		},
		{
			name:     "bedrock keeps computer/bash/memory/text_editor, drops tool_search",
			provider: schemas.Bedrock,
			input: []schemas.ChatTool{
				serverTool("computer_20251124", "computer"),
				serverTool("bash_20250124", "bash"),
				serverTool("memory_20250818", "memory"),
				serverTool("text_editor_20250728", "str_replace_based_edit_tool"),
				serverTool("tool_search_tool_bm25", "tool_search_tool_bm25"),
			},
			// tool-search-tool-2025-10-19 is InvokeModel/InvokeModelWithResponseStream
			// only per AWS's docs; classic Bedrock always dispatches via Converse.
			wantKeep:    4,
			wantDropped: []string{"tool_search_tool_bm25"},
		},
		{
			name:     "bedrock partial drop mixes function + server tools",
			provider: schemas.Bedrock,
			input: []schemas.ChatTool{
				fnTool,
				serverTool("web_fetch_20260309", "web_fetch"),
				serverTool("bash_20250124", "bash"),
			},
			wantKeep:    2, // fnTool + bash
			wantDropped: []string{"web_fetch_20260309"},
		},
		{
			name:        "vertex drops web_fetch",
			provider:    schemas.Vertex,
			input:       []schemas.ChatTool{serverTool("web_fetch_20260309", "web_fetch")},
			wantKeep:    0,
			wantDropped: []string{"web_fetch_20260309"},
			assertNotes: "Vertex has WebFetch=false per Table 20",
		},
		{
			name:        "vertex drops mcp_toolset",
			provider:    schemas.Vertex,
			input:       []schemas.ChatTool{serverTool("mcp_toolset", "notion")},
			wantKeep:    0,
			wantDropped: []string{"mcp_toolset"},
			assertNotes: "Vertex has MCP=false per MCP-excl (explicit exclusion in Anthropic docs)",
		},
		{
			name:     "anthropic keeps everything",
			provider: schemas.Anthropic,
			input: []schemas.ChatTool{
				serverTool("web_search_20260209", "web_search"),
				serverTool("web_fetch_20260309", "web_fetch"),
				serverTool("code_execution_20250825", "code_execution"),
				serverTool("mcp_toolset", "x"),
				serverTool("computer_20251124", "computer"),
			},
			wantKeep: 5,
		},
		{
			name:     "unknown provider keeps everything (forward-compat)",
			provider: schemas.ModelProvider("custom-new-provider"),
			input:    []schemas.ChatTool{serverTool("web_search_20260209", "web_search")},
			wantKeep: 1,
		},
		{
			name:     "unknown tool type on known provider is kept (forward-compat)",
			provider: schemas.Bedrock,
			input:    []schemas.ChatTool{serverTool("future_tool_20270101", "future")},
			wantKeep: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keep, dropped := ValidateChatToolsForProvider(tc.input, tc.provider)
			if len(keep) != tc.wantKeep {
				t.Errorf("keep count: got %d, want %d (%s)", len(keep), tc.wantKeep, tc.assertNotes)
			}
			if len(dropped) != len(tc.wantDropped) {
				t.Errorf("dropped count: got %v, want %v", dropped, tc.wantDropped)
			}
			for i, d := range tc.wantDropped {
				if i >= len(dropped) {
					break
				}
				if dropped[i] != d {
					t.Errorf("dropped[%d]: got %q, want %q", i, dropped[i], d)
				}
			}
		})
	}
}
