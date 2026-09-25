package anthropic

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// server_tool_use must survive the round trip through the neutral usage shape.
func TestServerToolUseRoundTrip(t *testing.T) {
	in := &AnthropicUsage{
		InputTokens:  100,
		OutputTokens: 50,
		ServerToolUse: &AnthropicServerToolUseUsage{
			WebSearchRequests: 2,
			WebFetchRequests:  3,
		},
	}

	neutral := ConvertAnthropicUsageToBifrostUsage(in)
	require.NotNil(t, neutral.OutputTokensDetails)
	require.NotNil(t, neutral.OutputTokensDetails.NumSearchQueries)
	require.NotNil(t, neutral.OutputTokensDetails.NumWebFetchRequests)
	assert.Equal(t, 2, *neutral.OutputTokensDetails.NumSearchQueries)
	assert.Equal(t, 3, *neutral.OutputTokensDetails.NumWebFetchRequests)

	back := ConvertBifrostUsageToAnthropicUsage(neutral)
	require.NotNil(t, back.ServerToolUse)
	assert.Equal(t, 2, back.ServerToolUse.WebSearchRequests)
	assert.Equal(t, 3, back.ServerToolUse.WebFetchRequests)
}

// An Anthropic-sourced response served over the OpenAI wire reports its web
// search calls in tool_usage.
func TestServerToolUseBecomesOpenAIToolUsage(t *testing.T) {
	resp := &schemas.BifrostResponsesResponse{
		Usage: ConvertAnthropicUsageToBifrostUsage(&AnthropicUsage{
			InputTokens:   100,
			OutputTokens:  50,
			ServerToolUse: &AnthropicServerToolUseUsage{WebSearchRequests: 2},
		}),
	}
	resp.SyncToolUsage()

	require.NotNil(t, resp.ToolUsage)
	require.NotNil(t, resp.ToolUsage.WebSearch)
	assert.Equal(t, 2, resp.ToolUsage.WebSearch.NumRequests)
}

// A neutral usage with no server-tool calls must not emit an empty block.
func TestSyncToolUsageNoOpWithoutSearches(t *testing.T) {
	resp := &schemas.BifrostResponsesResponse{
		Usage: &schemas.ResponsesResponseUsage{InputTokens: 10, OutputTokens: 5},
	}
	resp.SyncToolUsage()
	assert.Nil(t, resp.ToolUsage)
}
