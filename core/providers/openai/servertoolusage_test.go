package openai

import (
	"testing"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shape taken from a real gpt-5 web_search response: three web_search_call items,
// two of which are "search" actions, and a tool_usage block reporting 2 requests.
const gpt5WebSearchResponse = `{
  "id": "resp_07c9b6a134180bb4006aacf35ba59087d1b94e676cc3251df7",
  "object": "response",
  "status": "completed",
  "model": "gpt-5-2025-08-07",
  "output": [
    {"id": "rs_1", "type": "reasoning", "content": [], "summary": []},
    {"id": "ws_1", "type": "web_search_call", "status": "completed",
     "action": {"type": "search", "queries": ["a", "b"], "query": "a"}},
    {"id": "rs_2", "type": "reasoning", "content": [], "summary": []},
    {"id": "ws_2", "type": "web_search_call", "status": "completed",
     "action": {"type": "open_page", "url": "https://developers.openai.com/api/docs/models/all"}},
    {"id": "ws_3", "type": "web_search_call", "status": "completed",
     "action": {"type": "search", "queries": ["c"], "query": "c"}},
    {"id": "msg_1", "type": "message", "status": "completed", "role": "assistant",
     "content": [{"type": "output_text", "annotations": [], "text": "hi"}]}
  ],
  "tool_usage": {
    "image_gen": {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
    "web_search": {"num_requests": 2}
  },
  "usage": {
    "input_tokens": 19416,
    "input_tokens_details": {"cache_write_tokens": 0, "cached_tokens": 0},
    "output_tokens": 2542,
    "output_tokens_details": {"reasoning_tokens": 2176},
    "total_tokens": 21958
  }
}`

func TestApplyOpenAIServerToolUsage_PrefersToolUsageBlock(t *testing.T) {
	var resp schemas.BifrostResponsesResponse
	require.NoError(t, sonic.UnmarshalString(gpt5WebSearchResponse, &resp))

	require.NotNil(t, resp.ToolUsage)
	require.NotNil(t, resp.ToolUsage.WebSearch)
	assert.Equal(t, 2, resp.ToolUsage.WebSearch.NumRequests)

	ApplyOpenAIServerToolUsage(&resp)

	require.NotNil(t, resp.Usage.OutputTokensDetails)
	require.NotNil(t, resp.Usage.OutputTokensDetails.NumSearchQueries)
	// 2 billed requests, not the 3 web_search_call items in the output.
	assert.Equal(t, 2, *resp.Usage.OutputTokensDetails.NumSearchQueries)
}

func TestApplyOpenAIServerToolUsage_NoToolUsageBlockIsNotCounted(t *testing.T) {
	var raw map[string]interface{}
	require.NoError(t, sonic.UnmarshalString(gpt5WebSearchResponse, &raw))
	delete(raw, "tool_usage")
	body, err := sonic.Marshal(raw)
	require.NoError(t, err)

	var resp schemas.BifrostResponsesResponse
	require.NoError(t, sonic.Unmarshal(body, &resp))
	require.Nil(t, resp.ToolUsage)

	ApplyOpenAIServerToolUsage(&resp)

	// The three web_search_call items are not counted: only tool_usage bills.
	if resp.Usage.OutputTokensDetails != nil {
		assert.Nil(t, resp.Usage.OutputTokensDetails.NumSearchQueries)
	}
}

func TestExtractOAIResponsesUsage_CountsWebSearch(t *testing.T) {
	u := extractOAIResponsesUsage([]byte(gpt5WebSearchResponse))
	require.NotNil(t, u)
	require.NotNil(t, u.LLMUsage.CompletionTokensDetails)
	require.NotNil(t, u.LLMUsage.CompletionTokensDetails.NumSearchQueries)
	assert.Equal(t, 2, *u.LLMUsage.CompletionTokensDetails.NumSearchQueries)
}
