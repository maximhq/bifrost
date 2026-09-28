package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Perplexity Agent API — request-side tool support
// (docs.perplexity.ai/docs/agent-api)
// =============================================================================

// TestResponsesToolPerplexityAgentAPIMarshalUnmarshalRoundTrip locks in the wire
// shape for the Agent API's server-side tools that don't fit any pre-existing
// ResponsesTool variant (sandbox, fetch_url, finance_search, people_search), plus
// the Agent API's Perplexity-specific extensions to the shared web_search tool
// (top-level max_results/max_tokens/max_tokens_per_page and the nested filters
// object's search_domain_filter/search_recency_filter/date filters).
func TestResponsesToolPerplexityAgentAPIMarshalUnmarshalRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "sandbox", in: `{"type":"sandbox"}`},
		{name: "fetch_url", in: `{"type":"fetch_url","max_urls":3}`},
		{name: "fetch_url without max_urls", in: `{"type":"fetch_url"}`},
		{name: "finance_search", in: `{"type":"finance_search"}`},
		{name: "people_search", in: `{"type":"people_search","max_tokens":10000,"max_tokens_per_page":1000}`},
		{
			name: "web_search with Perplexity filters and result-size overrides",
			in: `{"type":"web_search","search_context_size":"medium","max_results":10,"max_tokens":1000,"max_tokens_per_page":1000,` +
				`"filters":{"search_domain_filter":["example.com","-exclude.com"],"search_recency_filter":"month",` +
				`"search_after_date_filter":"01/15/2026","search_before_date_filter":"05/15/2026",` +
				`"last_updated_after_filter":"01/01/2026","last_updated_before_filter":"12/31/2026"},` +
				`"user_location":{"country":"US","region":"California","city":"San Francisco","latitude":37.7749,"longitude":-122.4194}}`,
		},
		{
			name: "mcp with defer_loading (already supported generically)",
			in:   `{"type":"mcp","server_label":"github","server_url":"https://api.githubcopilot.com/mcp/","authorization":"tok","defer_loading":true}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tool ResponsesTool
			require.NoError(t, Unmarshal([]byte(tt.in), &tool))

			out, err := MarshalSorted(tool)
			require.NoError(t, err)

			assert.JSONEq(t, tt.in, string(out))
		})
	}
}

// TestResponsesToolPerplexityAgentAPIUnknownTypeToolsOnlyEmitCommonFields verifies
// that the (rare) case of a tool type with no configurable fields at all doesn't
// carry any surprise data: sandbox/finance_search should marshal back to just
// their discriminator plus whatever common fields (name, description, ...) were
// explicitly set.
func TestResponsesToolPerplexityAgentAPIUnknownTypeToolsOnlyEmitCommonFields(t *testing.T) {
	var tool ResponsesTool
	require.NoError(t, Unmarshal([]byte(`{"type":"sandbox","description":"code sandbox"}`), &tool))

	out, err := MarshalSorted(tool)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"sandbox","description":"code sandbox"}`, string(out))
}

// =============================================================================
// Perplexity Agent API — response-side output items
// =============================================================================

// TestResponsesMessagePerplexityAgentAPIOutputItemsPreservedVerbatim locks in
// round-tripping of the Agent API's tool-result output items. Unlike OpenAI's
// call/output pairs, Perplexity appends these as flat data items straight to the
// `output` array (search_results, fetch_url_results, sandbox_results,
// finance_results, people_search_results); none fits an existing typed
// ResponsesMessage shape, so they're carried through the raw-preserved path
// (isRawPreservedItem) instead of being silently dropped to just `{"type":...}`.
func TestResponsesMessagePerplexityAgentAPIOutputItemsPreservedVerbatim(t *testing.T) {
	items := map[string]string{
		"search_results": `{"type":"search_results","queries":["latest ai agent developments"],"results":[` +
			`{"id":1,"title":"Agent Frameworks 2026","url":"https://example.com/agents","snippet":"...","date":"2026-01-01","source":"web"}]}`,
		"fetch_url_results": `{"type":"fetch_url_results","contents":[` +
			`{"url":"https://example.com/report","title":"Example Report","snippet":"Extracted content from the fetched page."}]}`,
		"sandbox_results": `{"type":"sandbox_results","call_id":"call_1","code":"print(1+1)","container_id":"ctr_1","language":"python","status":"completed",` +
			`"results":[{"stdout":"2\n","stderr":"","exit_code":0,"duration_ms":12,"status":"completed"}]}`,
		"finance_results": `{"type":"finance_results","categories":["quote"],"tickers":["NVDA"],"results":[` +
			`{"category":"quote","tickers":["NVDA"],"content":"Structured financial data table","sources":["https://www.perplexity.ai/finance/NVDA"]}]}`,
		"people_search_results": `{"type":"people_search_results","queries":["Sarah Chen"],"results":[` +
			`{"id":1,"url":"https://example.com/sarah-chen","title":"Sarah Chen","snippet":"...","source":"web","last_updated":"2026-01-01"}]}`,
		// Live-verified against api.perplexity.ai on 2026-09-17: this undocumented item
		// is emitted alongside finance_search's finance_results.
		"skill_loaded": `{"type":"skill_loaded","name":"finance"}`,
	}

	for name, raw := range items {
		t.Run(name, func(t *testing.T) {
			var msg ResponsesMessage
			require.NoError(t, Unmarshal([]byte(raw), &msg))

			require.NotNil(t, msg.Type)
			assert.Equal(t, name, string(*msg.Type))

			encoded, err := MarshalSorted(msg)
			require.NoError(t, err)
			assert.JSONEq(t, raw, string(encoded))
		})
	}
}
