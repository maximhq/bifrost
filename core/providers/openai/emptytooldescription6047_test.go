package openai_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// buildResponsesInput decodes a raw input array into ResponsesMessages,
// exercising the same verbatim-preservation path as the HTTP transport.
func buildResponsesInput(t *testing.T, raw string) []schemas.ResponsesMessage {
	t.Helper()
	var input []schemas.ResponsesMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &input))
	return input
}

// wireInput converts the request for provider and returns the marshalled
// `input` array as it would reach the upstream wire.
func wireInput(t *testing.T, provider schemas.ModelProvider, input []schemas.ResponsesMessage) gjson.Result {
	t.Helper()
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	oaReq := openai.ToOpenAIResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    "gpt-5.6",
		Input:    input,
	})
	require.NotNil(t, oaReq)
	wire, err := json.Marshal(oaReq)
	require.NoError(t, err)
	return gjson.GetBytes(wire, "input")
}

const codexAdditionalToolsInput = `[
	{
		"type": "additional_tools",
		"role": "developer",
		"tools": [
			{
				"type": "namespace",
				"name": "functions",
				"description": "",
				"tools": [
					{"type": "function", "name": "get_weather", "description": "  ", "parameters": {"type": "object"}},
					{"type": "function", "name": "described", "description": "Already documented", "parameters": {"type": "object"}}
				]
			},
			{"type": "function", "name": "no_description_key", "parameters": {"type": "object"}}
		]
	},
	{"role": "user", "content": "hi"}
]`

// Regression for #6047: Codex CLI 0.147.0 emits additional_tools entries with
// empty descriptions, which Azure Responses rejects with HTTP 400 ("Expected a
// string with minimum length 1"). For providers flagged with
// EmptyToolDescription: false, the preserved item must be forwarded with
// deterministic fallback descriptions instead.
func TestAdditionalToolsEmptyDescriptionFilledForAzure(t *testing.T) {
	input := wireInput(t, schemas.Azure, buildResponsesInput(t, codexAdditionalToolsInput))

	item := input.Get("0")
	assert.Equal(t, "additional_tools", item.Get("type").String())
	assert.Equal(t, "Tools in the functions namespace", item.Get("tools.0.description").String(),
		"empty namespace description must get the deterministic fallback")
	assert.Equal(t, "The get_weather tool", item.Get("tools.0.tools.0.description").String(),
		"whitespace-only descriptions on nested tools must be filled too")
	assert.Equal(t, "Already documented", item.Get("tools.0.tools.1.description").String(),
		"non-empty descriptions must remain unchanged")
	assert.False(t, item.Get("tools.1.description").Exists(),
		"absent description keys must stay absent; only empty strings are rejected upstream")

	// The rest of the preserved item must survive byte-level rewriting.
	assert.Equal(t, "developer", item.Get("role").String())
	assert.Equal(t, "object", item.Get("tools.0.tools.0.parameters.type").String())
	assert.Equal(t, "hi", input.Get("1.content").String())
}

// OpenAI proper accepts the empty description, so the item must keep
// round-tripping byte-for-byte (the #5103 preservation contract).
func TestAdditionalToolsEmptyDescriptionVerbatimForOpenAI(t *testing.T) {
	input := wireInput(t, schemas.OpenAI, buildResponsesInput(t, codexAdditionalToolsInput))

	item := input.Get("0")
	// encoding/json compacts MarshalJSON output when embedding, so the wire is
	// the preserved bytes minus insignificant whitespace: compare against the
	// compacted original to pin key order, unasserted fields, and escaping.
	var expected bytes.Buffer
	require.NoError(t, json.Compact(&expected, []byte(gjson.Parse(codexAdditionalToolsInput).Get("0").Raw)))
	assert.Equal(t, expected.String(), item.Raw, "OpenAI must preserve additional_tools bytes verbatim")
	assert.Equal(t, "", item.Get("tools.0.description").String())
	assert.True(t, item.Get("tools.0.description").Exists())
	assert.Equal(t, "  ", item.Get("tools.0.tools.0.description").String())
}

// A fully-described additional_tools item routed to Azure must not be
// rewritten at all.
func TestAdditionalToolsNonEmptyDescriptionsUntouchedForAzure(t *testing.T) {
	raw := `[
		{
			"type": "additional_tools",
			"role": "developer",
			"tools": [{"type": "namespace", "name": "functions", "description": "All functions", "tools": []}]
		}
	]`
	input := wireInput(t, schemas.Azure, buildResponsesInput(t, raw))
	assert.Equal(t, "All functions", input.Get("0.tools.0.description").String())
}
