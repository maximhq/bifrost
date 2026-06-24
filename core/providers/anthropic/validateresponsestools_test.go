package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestValidateResponsesToolsForProvider locks in the Responses-path partition:
// function/custom tools always survive; server tools (incl. mcp) survive only
// when the target provider's ProviderFeatures flag is true for that tool type.
// This is the strip-silently mirror of ValidateToolsForProvider and must keep
// identical per-type gating — only the control flow differs.
func TestValidateResponsesToolsForProvider(t *testing.T) {
	fnTool := schemas.ResponsesTool{Type: schemas.ResponsesToolTypeFunction}
	serverTool := func(tpe schemas.ResponsesToolType) schemas.ResponsesTool {
		return schemas.ResponsesTool{Type: tpe}
	}

	cases := []struct {
		name        string
		provider    schemas.ModelProvider
		input       []schemas.ResponsesTool
		wantKeep    int
		wantDropped []string
		assertNotes string
	}{
		{
			name:        "bedrock drops mcp (the reported regression)",
			provider:    schemas.Bedrock,
			input:       []schemas.ResponsesTool{serverTool(schemas.ResponsesToolTypeMCP)},
			wantKeep:    0,
			wantDropped: []string{string(schemas.ResponsesToolTypeMCP)},
			assertNotes: "Bedrock Converse has no remote-MCP connector (MCP=false)",
		},
		{
			name:     "bedrock keeps function tool alongside dropped mcp",
			provider: schemas.Bedrock,
			input: []schemas.ResponsesTool{
				fnTool,
				serverTool(schemas.ResponsesToolTypeMCP),
			},
			wantKeep:    1, // fnTool survives; mcp dropped
			wantDropped: []string{string(schemas.ResponsesToolTypeMCP)},
		},
		{
			name:        "vertex drops mcp",
			provider:    schemas.Vertex,
			input:       []schemas.ResponsesTool{serverTool(schemas.ResponsesToolTypeMCP)},
			wantKeep:    0,
			wantDropped: []string{string(schemas.ResponsesToolTypeMCP)},
			assertNotes: "Vertex has MCP=false (explicit exclusion)",
		},
		{
			name:     "anthropic keeps mcp (MCP=true)",
			provider: schemas.Anthropic,
			input:    []schemas.ResponsesTool{serverTool(schemas.ResponsesToolTypeMCP)},
			wantKeep: 1,
		},
		{
			name:     "azure keeps mcp (MCP=true)",
			provider: schemas.Azure,
			input:    []schemas.ResponsesTool{serverTool(schemas.ResponsesToolTypeMCP)},
			wantKeep: 1,
		},
		{
			name:     "bedrock keeps computer_use",
			provider: schemas.Bedrock,
			input:    []schemas.ResponsesTool{serverTool(schemas.ResponsesToolTypeComputerUsePreview)},
			wantKeep: 1,
		},
		{
			name:     "bedrock keeps web_search via nova_grounding",
			provider: schemas.Bedrock,
			input:    []schemas.ResponsesTool{serverTool(schemas.ResponsesToolTypeWebSearch)},
			wantKeep: 1,
		},
		{
			name:        "bedrock drops web_fetch",
			provider:    schemas.Bedrock,
			input:       []schemas.ResponsesTool{serverTool(schemas.ResponsesToolTypeWebFetch)},
			wantKeep:    0,
			wantDropped: []string{string(schemas.ResponsesToolTypeWebFetch)},
		},
		{
			name:     "function tools always survive on any provider",
			provider: schemas.Bedrock,
			input:    []schemas.ResponsesTool{fnTool, fnTool},
			wantKeep: 2,
		},
		{
			name:     "unknown provider keeps everything (forward-compat)",
			provider: schemas.ModelProvider("custom-new-provider"),
			input:    []schemas.ResponsesTool{serverTool(schemas.ResponsesToolTypeMCP)},
			wantKeep: 1,
		},
		{
			name:     "unknown tool type on known provider is kept (forward-compat)",
			provider: schemas.Bedrock,
			input:    []schemas.ResponsesTool{serverTool(schemas.ResponsesToolType("future_tool"))},
			wantKeep: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keep, dropped := ValidateResponsesToolsForProvider(tc.input, tc.provider)
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
