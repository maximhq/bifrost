package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover the REQUEST side of Anthropic's server-side tool search on
// the OpenAI-shaped /v1/responses path (issue #5279). The response side is
// covered by toolsearch_test.go.
//
// The bug: a client declares the meta-tool by its dated type
// (tool_search_tool_regex_20251119). normalizeResponsesToolType had no case for
// it, so ResponsesTool.Type kept the dated string, every switch comparing
// against ResponsesToolTypeToolSearch missed, and convertBifrostToolToAnthropic
// fell through to its custom-tool tail — emitting the meta-tool with a name and
// an empty input_schema but NO type. Anthropic reads that as a client tool:
// it returns a plain tool_use, never a server_tool_use, and the server-side
// search never runs.
//
// Cite: https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool

// responsesToolFromJSON decodes through the real ResponsesTool.UnmarshalJSON so
// these tests exercise the same normalization path an inbound HTTP request does.
func responsesToolFromJSON(t *testing.T, raw string) schemas.ResponsesTool {
	t.Helper()
	var tool schemas.ResponsesTool
	require.NoError(t, schemas.Unmarshal([]byte(raw), &tool))
	return tool
}

// TestConvertBifrostToolToAnthropicDatedToolSearch is the core #5279 regression
// test: a dated tool_search declaration must reach Anthropic as a *typed server
// tool*, not as a custom tool.
func TestConvertBifrostToolToAnthropicDatedToolSearch(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantType AnthropicToolType
		wantName AnthropicToolName
	}{
		{
			name:     "regex dated, doc canonical shape",
			raw:      `{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"}`,
			wantType: AnthropicToolTypeToolSearchRegex20251119,
			wantName: AnthropicToolNameToolSearchRegex,
		},
		{
			name:     "bm25 dated, doc canonical shape",
			raw:      `{"type":"tool_search_tool_bm25_20251119","name":"tool_search_tool_bm25"}`,
			wantType: AnthropicToolTypeToolSearchBM2520251119,
			wantName: AnthropicToolNameToolSearchBM25,
		},
		{
			// Anthropic's Go/C# SDK examples declare the tool with a type and no
			// name. The regex/bm25 variant must survive normalization anyway —
			// the two use different query grammars (Python re.search patterns vs
			// natural language), so a silent downgrade sends the model the wrong
			// one.
			name:     "regex dated, no name",
			raw:      `{"type":"tool_search_tool_regex_20251119"}`,
			wantType: AnthropicToolTypeToolSearchRegex20251119,
			wantName: AnthropicToolNameToolSearchRegex,
		},
		{
			name:     "bm25 dated, no name",
			raw:      `{"type":"tool_search_tool_bm25_20251119"}`,
			wantType: AnthropicToolTypeToolSearchBM2520251119,
			wantName: AnthropicToolNameToolSearchBM25,
		},
		{
			name:     "undated regex variant",
			raw:      `{"type":"tool_search_tool_regex","name":"tool_search_tool_regex"}`,
			wantType: AnthropicToolTypeToolSearchRegex20251119,
			wantName: AnthropicToolNameToolSearchRegex,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := responsesToolFromJSON(t, tc.raw)

			got := convertBifrostToolToAnthropic("claude-sonnet-4-5-20250929", &tool, schemas.Anthropic, false)
			require.NotNil(t, got, "tool_search meta-tool must not be dropped")

			require.NotNil(t, got.Type,
				"a server tool must carry a type; a nil type is exactly the custom-tool "+
					"downcast that makes Anthropic treat tool_search as a client tool")
			assert.Equal(t, tc.wantType, *got.Type)
			assert.Equal(t, string(tc.wantName), got.Name)

			// The custom-tool tail of convertBifrostToolToAnthropic backfills an
			// empty object input_schema. Its presence is the fingerprint of the
			// bug, so assert it is absent.
			assert.Nil(t, got.InputSchema,
				"tool_search is a server tool and must not carry an input_schema")
			assert.Nil(t, got.Description)
		})
	}
}

// TestConvertBifrostToolsToAnthropicToolSearchCatalog exercises the reporter's
// actual request shape: the meta-tool plus a deferred client tool. Per the doc,
// every tool definition is still sent on every request and at least one tool
// (normally the meta-tool) must stay non-deferred.
func TestConvertBifrostToolsToAnthropicToolSearchCatalog(t *testing.T) {
	tools := []schemas.ResponsesTool{
		responsesToolFromJSON(t, `{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"}`),
		responsesToolFromJSON(t, `{"type":"function","name":"get_weather","description":"Get the weather","parameters":{"type":"object","properties":{"location":{"type":"string"}}},"defer_loading":true}`),
	}

	got, mcpServers := convertBifrostToolsToAnthropic("claude-sonnet-4-5-20250929", tools, schemas.Anthropic)
	require.Empty(t, mcpServers)
	require.Len(t, got, 2)

	// The meta-tool: typed, no schema, and never deferred.
	require.NotNil(t, got[0].Type)
	assert.Equal(t, AnthropicToolTypeToolSearchRegex20251119, *got[0].Type)
	assert.Equal(t, string(AnthropicToolNameToolSearchRegex), got[0].Name)
	assert.Nil(t, got[0].InputSchema)
	assert.Nil(t, got[0].DeferLoading,
		"the doc is explicit: never set defer_loading on the tool search tool itself")

	// The deferred client tool: untyped custom tool, schema intact, defer_loading forwarded.
	assert.Nil(t, got[1].Type)
	assert.Equal(t, "get_weather", got[1].Name)
	assert.NotNil(t, got[1].InputSchema)
	require.NotNil(t, got[1].DeferLoading)
	assert.True(t, *got[1].DeferLoading)
}

// TestValidateResponsesToolsForProviderDatedToolSearch verifies the dated type
// now reaches the per-provider feature gate. Before the fix it matched no case
// and was kept unconditionally, so a dated tool_search leaked to Bedrock —
// whose Converse API cannot run it (AWS restricts server-side tool search to
// InvokeModel / InvokeModelWithResponseStream), arriving as a bogus custom tool.
func TestValidateResponsesToolsForProviderDatedToolSearch(t *testing.T) {
	cases := []struct {
		name        string
		provider    schemas.ModelProvider
		raw         string
		wantKeep    int
		wantDropped []string
	}{
		{
			name:     "anthropic keeps dated regex variant",
			provider: schemas.Anthropic,
			raw:      `{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"}`,
			wantKeep: 1,
		},
		{
			name:     "vertex keeps dated bm25 variant",
			provider: schemas.Vertex,
			raw:      `{"type":"tool_search_tool_bm25_20251119","name":"tool_search_tool_bm25"}`,
			wantKeep: 1,
		},
		{
			name:        "bedrock drops dated regex variant (Converse cannot run it)",
			provider:    schemas.Bedrock,
			raw:         `{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"}`,
			wantKeep:    0,
			wantDropped: []string{"tool_search"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := responsesToolFromJSON(t, tc.raw)
			keep, dropped := ValidateResponsesToolsForProvider([]schemas.ResponsesTool{tool}, tc.provider)
			assert.Len(t, keep, tc.wantKeep)
			assert.Equal(t, tc.wantDropped, dropped)
		})
	}
}

// TestConvertAnthropicToolToBifrostToolSearchRoundTrip closes the loop on the
// native /anthropic/v1/messages entry point: an inbound dated tool_search tool
// must normalize inbound and rebuild to the same dated type outbound.
func TestConvertAnthropicToolToBifrostToolSearchRoundTrip(t *testing.T) {
	cases := []struct {
		inType   AnthropicToolType
		inName   AnthropicToolName
		wantType AnthropicToolType
	}{
		{AnthropicToolTypeToolSearchRegex20251119, AnthropicToolNameToolSearchRegex, AnthropicToolTypeToolSearchRegex20251119},
		{AnthropicToolTypeToolSearchBM2520251119, AnthropicToolNameToolSearchBM25, AnthropicToolTypeToolSearchBM2520251119},
	}

	for _, tc := range cases {
		t.Run(string(tc.inType), func(t *testing.T) {
			in := &AnthropicTool{Type: schemas.Ptr(tc.inType), Name: string(tc.inName)}

			neutral := convertAnthropicToolToBifrost(in)
			require.NotNil(t, neutral)
			assert.Equal(t, schemas.ResponsesToolTypeToolSearch, neutral.Type)

			back := convertBifrostToolToAnthropic("claude-sonnet-4-5-20250929", neutral, schemas.Anthropic, false)
			require.NotNil(t, back)
			require.NotNil(t, back.Type)
			assert.Equal(t, tc.wantType, *back.Type, "variant must survive the round trip")
			assert.Nil(t, back.InputSchema)
		})
	}
}
