package bedrock

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// anthropicToolMap builds an Anthropic-native tool map as it would arrive on the wire
// (i.e. the shape convertAnthropicTools reads out of the untyped r.Tools field).
func anthropicToolMap(name string, cacheControl map[string]interface{}) map[string]interface{} {
	m := map[string]interface{}{
		"name":        name,
		"description": "a test tool",
		"input_schema": map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}
	if cacheControl != nil {
		m["cache_control"] = cacheControl
	}
	return m
}

// TestConvertAnthropicTools_NoCacheControl_Unaffected locks in the pre-existing behavior
// when no tool carries cache_control: one BedrockTool per input tool, no CachePoint entries.
func TestConvertAnthropicTools_NoCacheControl_Unaffected(t *testing.T) {
	req := &BedrockInvokeRequest{
		Tools: []interface{}{
			anthropicToolMap("alpha", nil),
			anthropicToolMap("beta", nil),
		},
	}

	toolConfig := req.convertAnthropicTools()
	require.NotNil(t, toolConfig)
	require.Len(t, toolConfig.Tools, 2)
	for _, tool := range toolConfig.Tools {
		assert.NotNil(t, tool.ToolSpec)
		assert.Nil(t, tool.CachePoint)
	}
}

// TestConvertAnthropicTools_CarriesCacheControl is the regression test for #5629: a
// cache_control marker on an Anthropic-native tool must survive the invoke->Converse
// conversion as a positional cachePoint entry appended after the marked tool, the same
// way the Bifrost->Bedrock egress direction already does (utils.go convertChatTools).
func TestConvertAnthropicTools_CarriesCacheControl(t *testing.T) {
	req := &BedrockInvokeRequest{
		Tools: []interface{}{
			anthropicToolMap("alpha", nil),
			anthropicToolMap("beta", map[string]interface{}{"type": "ephemeral"}),
		},
	}

	toolConfig := req.convertAnthropicTools()
	require.NotNil(t, toolConfig)
	require.Len(t, toolConfig.Tools, 3, "expected an extra cachePoint entry after the marked tool")

	assert.NotNil(t, toolConfig.Tools[0].ToolSpec)
	assert.Equal(t, "alpha", toolConfig.Tools[0].ToolSpec.Name)
	assert.Nil(t, toolConfig.Tools[0].CachePoint)

	assert.NotNil(t, toolConfig.Tools[1].ToolSpec)
	assert.Equal(t, "beta", toolConfig.Tools[1].ToolSpec.Name)
	assert.Nil(t, toolConfig.Tools[1].CachePoint)

	require.NotNil(t, toolConfig.Tools[2].CachePoint)
	assert.Nil(t, toolConfig.Tools[2].ToolSpec)
	assert.Equal(t, BedrockCachePointTypeDefault, toolConfig.Tools[2].CachePoint.Type)
	assert.Nil(t, toolConfig.Tools[2].CachePoint.TTL)
}

// TestConvertAnthropicTools_CacheControlTTL confirms newBedrockCachePoint's existing
// TTL allow-list ("5m" | "1h") is honored via this new code path — an unsupported TTL
// (e.g. Anthropic's own "1m") is dropped to the Bedrock default rather than forwarded.
func TestConvertAnthropicTools_CacheControlTTL(t *testing.T) {
	t.Run("supported TTL is forwarded", func(t *testing.T) {
		req := &BedrockInvokeRequest{
			Tools: []interface{}{
				anthropicToolMap("alpha", map[string]interface{}{"type": "ephemeral", "ttl": "1h"}),
			},
		}
		toolConfig := req.convertAnthropicTools()
		require.NotNil(t, toolConfig)
		require.Len(t, toolConfig.Tools, 2)
		require.NotNil(t, toolConfig.Tools[1].CachePoint)
		require.NotNil(t, toolConfig.Tools[1].CachePoint.TTL)
		assert.Equal(t, "1h", *toolConfig.Tools[1].CachePoint.TTL)
	})

	t.Run("unsupported TTL falls back to default", func(t *testing.T) {
		req := &BedrockInvokeRequest{
			Tools: []interface{}{
				anthropicToolMap("alpha", map[string]interface{}{"type": "ephemeral", "ttl": "1m"}),
			},
		}
		toolConfig := req.convertAnthropicTools()
		require.NotNil(t, toolConfig)
		require.Len(t, toolConfig.Tools, 2)
		require.NotNil(t, toolConfig.Tools[1].CachePoint)
		assert.Nil(t, toolConfig.Tools[1].CachePoint.TTL)
	})
}

// TestParseSystemMessages_NoCacheControl_Unaffected locks in the pre-existing behavior
// for a plain Anthropic-native system block with no cache_control.
func TestParseSystemMessages_NoCacheControl_Unaffected(t *testing.T) {
	req := &BedrockInvokeRequest{
		System: []interface{}{
			map[string]interface{}{"type": "text", "text": "You are a helpful assistant."},
		},
	}

	result := req.parseSystemMessages()
	require.Len(t, result, 1)
	require.NotNil(t, result[0].Text)
	assert.Equal(t, "You are a helpful assistant.", *result[0].Text)
	assert.Nil(t, result[0].CachePoint)
}

// TestParseSystemMessages_CarriesCacheControl is the regression test for #5629's system-block
// half of the bug: an Anthropic-native system block with cache_control must produce a trailing
// standalone cachePoint entry, matching what Converse-native system arrays already carry (see
// TestStandaloneCachePointBlockHandling/SystemMessage_WithStandaloneCachePoint in bedrock_test.go).
func TestParseSystemMessages_CarriesCacheControl(t *testing.T) {
	req := &BedrockInvokeRequest{
		System: []interface{}{
			map[string]interface{}{
				"type":          "text",
				"text":          "You are a helpful assistant.",
				"cache_control": map[string]interface{}{"type": "ephemeral"},
			},
		},
	}

	result := req.parseSystemMessages()
	require.Len(t, result, 2)

	require.NotNil(t, result[0].Text)
	assert.Equal(t, "You are a helpful assistant.", *result[0].Text)
	assert.Nil(t, result[0].CachePoint)

	assert.Nil(t, result[1].Text)
	require.NotNil(t, result[1].CachePoint)
	assert.Equal(t, BedrockCachePointTypeDefault, result[1].CachePoint.Type)
}

// TestToBedrockConverseRequest_InvokeCacheControlEndToEnd is the full-pipeline regression
// test the issue reporter offered to write: an Anthropic-native invoke body with cache_control
// on both a tool and the system block must come out the other end of
// ToBedrockConverseRequest -> ToBifrostResponsesRequest (the same shared egress builder used
// by the native /converse route) with CacheControl set on both the tool and the system message.
func TestToBedrockConverseRequest_InvokeCacheControlEndToEnd(t *testing.T) {
	req := &BedrockInvokeRequest{
		ModelID: "us.anthropic.claude-haiku-4-5-20251001-v1:0",
		Messages: []BedrockMessage{
			{Role: BedrockMessageRoleUser, Content: []BedrockContentBlock{{Text: schemas.Ptr("Say OK.")}}},
		},
		System: []interface{}{
			map[string]interface{}{
				"type":          "text",
				"text":          "You are a helpful assistant.",
				"cache_control": map[string]interface{}{"type": "ephemeral"},
			},
		},
		Tools: []interface{}{
			anthropicToolMap("alpha", map[string]interface{}{"type": "ephemeral"}),
		},
	}

	converseReq := req.ToBedrockConverseRequest()
	require.NotNil(t, converseReq)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostReq, err := converseReq.ToBifrostResponsesRequest(ctx)
	require.NoError(t, err)
	require.NotNil(t, bifrostReq)

	// Tool cache breakpoint survived the full invoke -> Converse -> Bifrost pipeline.
	require.Len(t, bifrostReq.Params.Tools, 1)
	require.NotNil(t, bifrostReq.Params.Tools[0].CacheControl)
	assert.Equal(t, schemas.CacheControlTypeEphemeral, bifrostReq.Params.Tools[0].CacheControl.Type)

	// System cache breakpoint survived too — lands on the last content block of the
	// system message per convertBedrockSystemMessageToBifrostMessages.
	var systemMsg *schemas.ResponsesMessage
	for i := range bifrostReq.Input {
		if bifrostReq.Input[i].Role != nil && *bifrostReq.Input[i].Role == schemas.ResponsesInputMessageRoleSystem {
			systemMsg = &bifrostReq.Input[i]
			break
		}
	}
	require.NotNil(t, systemMsg, "expected a system message in the converted input")
	require.NotNil(t, systemMsg.Content)
	require.NotEmpty(t, systemMsg.Content.ContentBlocks)
	lastBlock := systemMsg.Content.ContentBlocks[len(systemMsg.Content.ContentBlocks)-1]
	require.NotNil(t, lastBlock.CacheControl)
	assert.Equal(t, schemas.CacheControlTypeEphemeral, lastBlock.CacheControl.Type)
}

// TestToBedrockConverseRequest_InvokeCacheControlNovaExcluded confirms Change 1 doesn't
// need its own Nova-family gate: convertAnthropicTools always emits the cachePoint entry,
// but the shared downstream builder (responses.go's tool.CachePoint handling) already
// excludes Nova models, so the exclusion applies uniformly regardless of ingress route.
func TestToBedrockConverseRequest_InvokeCacheControlNovaExcluded(t *testing.T) {
	req := &BedrockInvokeRequest{
		ModelID: "amazon.nova-pro-v1:0",
		Messages: []BedrockMessage{
			{Role: BedrockMessageRoleUser, Content: []BedrockContentBlock{{Text: schemas.Ptr("Say OK.")}}},
		},
		Tools: []interface{}{
			anthropicToolMap("alpha", map[string]interface{}{"type": "ephemeral"}),
		},
	}

	converseReq := req.ToBedrockConverseRequest()
	require.NotNil(t, converseReq)
	require.NotNil(t, converseReq.ToolConfig)
	require.Len(t, converseReq.ToolConfig.Tools, 2, "convertAnthropicTools itself is family-agnostic")

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostReq, err := converseReq.ToBifrostResponsesRequest(ctx)
	require.NoError(t, err)

	require.Len(t, bifrostReq.Params.Tools, 1)
	assert.Nil(t, bifrostReq.Params.Tools[0].CacheControl, "Nova models don't support tool-level cache points")
}

// TestToAnthropicInvokeStreamBytes_MessageDeltaCarriesUsage is the regression test for the
// reporter's "Additional observation": /invoke-with-response-stream emitted no input_tokens
// at all. Bedrock Converse only reports usage on the terminal stream event (unlike native
// Anthropic, which also populates message_start.message.usage), so this asserts the fix
// lands on message_delta — the only event where Bifrost actually has the data. Figures match
// the issue's own reproduction: 8666 raw input tokens, 8336 of them a cache read, netting 330.
func TestToAnthropicInvokeStreamBytes_MessageDeltaCarriesUsage(t *testing.T) {
	resp := &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeCompleted,
		Response: &schemas.BifrostResponsesResponse{
			Usage: &schemas.ResponsesResponseUsage{
				InputTokens:  8666,
				OutputTokens: 5,
				InputTokensDetails: &schemas.ResponsesResponseInputTokens{
					CachedReadTokens: 8336,
				},
			},
		},
	}

	frames, err := toAnthropicInvokeStreamBytes(resp)
	require.NoError(t, err)
	require.Len(t, frames, 2, "expected message_delta + message_stop")

	var messageDelta map[string]interface{}
	require.NoError(t, json.Unmarshal(frames[0], &messageDelta))

	usage, ok := messageDelta["usage"].(map[string]interface{})
	require.True(t, ok, "message_delta must carry a usage object")

	assert.EqualValues(t, 330, usage["input_tokens"], "input_tokens must be net of the cache read")
	assert.EqualValues(t, 5, usage["output_tokens"])
	assert.EqualValues(t, 8336, usage["cache_read_input_tokens"])
	_, hasCreation := usage["cache_creation_input_tokens"]
	assert.False(t, hasCreation, "no cache write occurred on this turn")
}

// TestToBedrockInvokeAnthropicResponse_IncludesCacheFields covers the gap found during
// investigation: even the non-streaming /invoke response never surfaced cache_creation/
// cache_read fields at all, so a client couldn't observe caching working even after the
// ingress fix. Figures again match the issue's turn-1 reproduction numbers.
func TestToBedrockInvokeAnthropicResponse_IncludesCacheFields(t *testing.T) {
	model := "us.anthropic.claude-haiku-4-5-20251001-v1:0"
	resp := &schemas.BifrostResponsesResponse{
		Model: model,
		Usage: &schemas.ResponsesResponseUsage{
			InputTokens:  8666,
			OutputTokens: 5,
			InputTokensDetails: &schemas.ResponsesResponseInputTokens{
				CachedWriteTokens: 8336,
			},
		},
	}

	result := toBedrockInvokeAnthropicResponse(resp, model)
	require.NotNil(t, result.Usage)
	assert.Equal(t, 330, result.Usage.InputTokens)
	assert.Equal(t, 5, result.Usage.OutputTokens)
	assert.Equal(t, 8336, result.Usage.CacheCreationInputTokens)
	assert.Equal(t, 0, result.Usage.CacheReadInputTokens)
}
