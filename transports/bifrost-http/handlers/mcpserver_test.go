package handlers

import (
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertToolFunctionParametersToMCPInputSchemaPreservesDefs(t *testing.T) {
	params := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("preferences", map[string]any{"$ref": "#/$defs/Preferences"}),
		),
		Required: []string{"preferences"},
		Defs: schemas.NewOrderedMapFromPairs(
			schemas.KV("Preferences", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"startHour": map[string]any{"type": "string"},
				},
			}),
		),
	}

	inputSchema := convertToolFunctionParametersToMCPInputSchema(params)

	require.Contains(t, inputSchema.Defs, "Preferences")
	data, err := json.Marshal(inputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"$defs"`)
	assert.Contains(t, string(data), `"$ref":"#/$defs/Preferences"`)
}

func TestConvertToolFunctionParametersToMCPInputSchemaPreservesLegacyDefinitionsAsDefs(t *testing.T) {
	params := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("preferences", map[string]any{"$ref": "#/$defs/Preferences"}),
		),
		Definitions: schemas.NewOrderedMapFromPairs(
			schemas.KV("Preferences", map[string]any{"type": "object"}),
		),
	}

	inputSchema := convertToolFunctionParametersToMCPInputSchema(params)

	require.Contains(t, inputSchema.Defs, "Preferences")
	data, err := json.Marshal(inputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"$defs"`)
}

// The MCP spec uses isError to tell the model a tool call failed. The gateway
// executes the upstream tool and then rebuilds the reply for its own client, so
// the flag has to survive that rebuild: without it a failed call reads as a
// success, and anything counting tool errors undercounts them.
func TestToolCallResultFromMessagePreservesUpstreamIsError(t *testing.T) {
	failure := "{\"error\": \"bad input\"}"
	msg := &schemas.ChatMessage{
		Content:         &schemas.ChatMessageContent{ContentStr: &failure},
		ChatToolMessage: &schemas.ChatToolMessage{IsError: schemas.Ptr(true)},
	}

	result := toolCallResultFromMessage(msg)

	require.NotNil(t, result)
	assert.True(t, result.IsError, "upstream isError must reach the gateway's client")
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok, "content should stay text")
	assert.Equal(t, failure, text.Text, "the error text must not be lost with the flag")
}

// A tool that succeeded must not be reported as failed, so the flag is only
// raised when the upstream server raised it.
func TestToolCallResultFromMessageLeavesSuccessAlone(t *testing.T) {
	body := "ok"
	for name, toolMsg := range map[string]*schemas.ChatToolMessage{
		"absent tool message": nil,
		"flag unset":          {},
		"flag false":          {IsError: schemas.Ptr(false)},
	} {
		t.Run(name, func(t *testing.T) {
			result := toolCallResultFromMessage(&schemas.ChatMessage{
				Content:         &schemas.ChatMessageContent{ContentStr: &body},
				ChatToolMessage: toolMsg,
			})

			require.NotNil(t, result)
			assert.False(t, result.IsError)
			require.Len(t, result.Content, 1)
			text, ok := result.Content[0].(mcp.TextContent)
			require.True(t, ok)
			assert.Equal(t, body, text.Text)
		})
	}
}

// Structured replies arrive as content blocks rather than a single string, and
// the flag rides on the message either way.
func TestToolCallResultFromMessageJoinsContentBlocksAndKeepsIsError(t *testing.T) {
	first, second := "partial ", "failure"
	msg := &schemas.ChatMessage{
		Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
			{Type: schemas.ChatContentBlockTypeText, Text: &first},
			{Type: schemas.ChatContentBlockTypeText, Text: &second},
		}},
		ChatToolMessage: &schemas.ChatToolMessage{IsError: schemas.Ptr(true)},
	}

	result := toolCallResultFromMessage(msg)

	require.NotNil(t, result)
	assert.True(t, result.IsError)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "partial failure", text.Text)
}

// A nil message is the executor returning nothing; the gateway still owes its
// client a well-formed result rather than a panic.
func TestToolCallResultFromMessageHandlesNilMessage(t *testing.T) {
	result := toolCallResultFromMessage(nil)

	require.NotNil(t, result)
	assert.False(t, result.IsError)
}
