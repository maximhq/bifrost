package bedrock

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bytedance/sonic"
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

// TestConvertAnthropicTools_ToolSearchTypeNeverBecomesInvocable is a regression test:
// classic Bedrock's Converse API has no concept of Anthropic's tool_search_tool_*
// entries (tool search is InvokeModel/InvokeModelWithResponseStream only per AWS's
// docs), so an inbound tool_search_tool_regex entry must never be built into an
// ordinary invocable BedrockToolSpec — that would present a broken, schema-less
// "function" tool to the model instead of being dropped.
func TestConvertAnthropicTools_ToolSearchTypeNeverBecomesInvocable(t *testing.T) {
	req := &BedrockInvokeRequest{
		Tools: []interface{}{
			map[string]interface{}{
				"type": "tool_search_tool_regex_20251119",
				"name": "tool_search_tool_regex",
			},
			anthropicToolMap("keep_me", nil),
		},
	}

	toolConfig := req.convertAnthropicTools()
	require.NotNil(t, toolConfig)
	require.Len(t, toolConfig.Tools, 1, "the tool_search_tool_* entry must be skipped, only the real tool kept")
	require.NotNil(t, toolConfig.Tools[0].ToolSpec)
	assert.Equal(t, "keep_me", toolConfig.Tools[0].ToolSpec.Name)
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

// TestToAnthropicInvokeStreamBytes_MessageStartCarriesUsage is the /invoke-with-response-stream
// half of #5885. The sibling test above pins the authoritative figures on message_delta, which
// is correct — but message_start was being built with no usage key at all, and Anthropic-dialect
// clients that validate the frame (e.g. @ai-sdk/anthropic, whose schema marks
// message.usage.input_tokens required) abort the stream before the first token. Bedrock Converse
// has no counts this early, so the placeholder is all-zero: neutral for clients that sum, and
// superseded by the real message_delta figures for clients that follow Anthropic's contract.
func TestToAnthropicInvokeStreamBytes_MessageStartCarriesUsage(t *testing.T) {
	resp := &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeCreated,
		Response: &schemas.BifrostResponsesResponse{
			ID: schemas.Ptr("msg_bedrock_1"),
			// Usage intentionally nil — Converse reports nothing until its terminal event.
		},
	}

	frames, err := toAnthropicInvokeStreamBytes(resp)
	require.NoError(t, err)
	require.Len(t, frames, 1, "expected a single message_start frame")

	var messageStart map[string]interface{}
	require.NoError(t, json.Unmarshal(frames[0], &messageStart))

	message, ok := messageStart["message"].(map[string]interface{})
	require.True(t, ok, "message_start must carry a message object")

	usage, ok := message["usage"].(map[string]interface{})
	require.True(t, ok, "message_start.message must carry a usage object, got %v", message)

	assert.EqualValues(t, 0, usage["input_tokens"])
	assert.EqualValues(t, 0, usage["output_tokens"])
}

// TestToAnthropicInvokeStreamBytes_MessageStartPrefersKnownUsage guards the other direction:
// when a provider does hand Bifrost usage at created time, message_start must report those
// figures rather than the zero placeholder.
func TestToAnthropicInvokeStreamBytes_MessageStartPrefersKnownUsage(t *testing.T) {
	resp := &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeCreated,
		Response: &schemas.BifrostResponsesResponse{
			ID: schemas.Ptr("msg_bedrock_2"),
			Usage: &schemas.ResponsesResponseUsage{
				InputTokens:  8666,
				OutputTokens: 1,
				InputTokensDetails: &schemas.ResponsesResponseInputTokens{
					CachedReadTokens: 8336,
				},
			},
		},
	}

	frames, err := toAnthropicInvokeStreamBytes(resp)
	require.NoError(t, err)
	require.Len(t, frames, 1)

	var messageStart map[string]interface{}
	require.NoError(t, json.Unmarshal(frames[0], &messageStart))

	message := messageStart["message"].(map[string]interface{})
	usage, ok := message["usage"].(map[string]interface{})
	require.True(t, ok, "message_start.message must carry a usage object, got %v", message)

	assert.EqualValues(t, 330, usage["input_tokens"], "input_tokens must be net of the cache read")
	assert.EqualValues(t, 1, usage["output_tokens"])
	assert.EqualValues(t, 8336, usage["cache_read_input_tokens"])
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

// --- Regression tests for #5638: /invoke egress dropped the reasoning signature (and,
// for Bedrock-originated reasoning, the thinking text itself) because neither the
// non-streaming response builder nor the streaming signature event carried it. ---

// TestToBedrockInvokeAnthropicResponse_ThinkingSignature covers the non-streaming path:
// Bedrock-originated reasoning lives in item.Content.ContentBlocks (not
// item.ResponsesReasoning.Summary, which is always empty for Bedrock — see
// convertSingleBedrockMessageToBifrostMessages), so the response builder must read
// ContentBlocks first and carry the signature through to the Anthropic-shaped thinking
// block, or a client replaying it back to Bedrock will 400 on a missing signature.
func TestToBedrockInvokeAnthropicResponse_ThinkingSignature(t *testing.T) {
	model := "us.anthropic.claude-haiku-4-5-20251001-v1:0"
	thinkingText := "Let me work through this."
	signature := "EqQBCgIYAhIM...fixture"
	resp := &schemas.BifrostResponsesResponse{
		Model: model,
		Output: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary: []schemas.ResponsesReasoningSummary{},
				},
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type:      schemas.ResponsesOutputMessageContentTypeReasoning,
							Text:      &thinkingText,
							Signature: &signature,
						},
					},
				},
			},
		},
	}

	result := toBedrockInvokeAnthropicResponse(resp, model)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "thinking", result.Content[0].Type)
	assert.Equal(t, thinkingText, result.Content[0].Thinking)
	assert.Equal(t, signature, result.Content[0].Signature)
}

// TestToBedrockInvokeAnthropicResponse_SummaryFallbackWhenContentBlocksUnusable covers
// a CodeRabbit finding on PR #5821: the branch decision was "does Content.ContentBlocks
// have any entries" rather than "did we actually emit a thinking block from it." If
// ContentBlocks is non-empty but contains no usable reasoning block (e.g. a
// non-reasoning content type), the ResponsesReasoning.Summary fallback — which may hold
// real data — was never consulted, silently losing thinking content on /invoke egress.
func TestToBedrockInvokeAnthropicResponse_SummaryFallbackWhenContentBlocksUnusable(t *testing.T) {
	model := "us.anthropic.claude-haiku-4-5-20251001-v1:0"
	resp := &schemas.BifrostResponsesResponse{
		Model: model,
		Output: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary: []schemas.ResponsesReasoningSummary{
						{Text: "fallback summary text"},
					},
				},
				Content: &schemas.ResponsesMessageContent{
					// Non-empty, but no reasoning-type block within it.
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{Type: schemas.ResponsesOutputMessageContentTypeText},
					},
				},
			},
		},
	}

	result := toBedrockInvokeAnthropicResponse(resp, model)
	require.Len(t, result.Content, 1, "expected the Summary fallback to be used when ContentBlocks yields no reasoning block")
	assert.Equal(t, "thinking", result.Content[0].Type)
	assert.Equal(t, "fallback summary text", result.Content[0].Thinking)
}

// TestToBedrockInvokeAnthropicResponse_EmptyTextSignaturePreserved guards against a
// stricter-than-necessary empty-text filter dropping the signature along with it.
// The sibling egress function (convertBifrostReasoningToBedrockReasoning, responses.go)
// only requires block.Text != nil — not non-empty — before carrying a reasoning block
// through; this response builder must match that, or a Bedrock-returned reasoning block
// with empty text but a real signature gets silently discarded here, losing the
// signature a client needs to replay history on the next turn.
func TestToBedrockInvokeAnthropicResponse_EmptyTextSignaturePreserved(t *testing.T) {
	model := "us.anthropic.claude-haiku-4-5-20251001-v1:0"
	emptyText := ""
	signature := "EqQBCgIYAhIM...fixture"
	resp := &schemas.BifrostResponsesResponse{
		Model: model,
		Output: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary: []schemas.ResponsesReasoningSummary{},
				},
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type:      schemas.ResponsesOutputMessageContentTypeReasoning,
							Text:      &emptyText,
							Signature: &signature,
						},
					},
				},
			},
		},
	}

	result := toBedrockInvokeAnthropicResponse(resp, model)
	require.Len(t, result.Content, 1, "the block must survive even with empty text, so its signature isn't lost")
	assert.Equal(t, "thinking", result.Content[0].Type)
	assert.Equal(t, "", result.Content[0].Thinking)
	assert.Equal(t, signature, result.Content[0].Signature)
}

// TestToAnthropicInvokeStreamBytes_ReasoningSignatureDelta covers the streaming path:
// Anthropic-compatible SDKs (including Claude Code) parse a thinking signature only off
// a dedicated signature_delta event, never nested inside thinking_delta — see
// https://platform.claude.com/docs/en/build-with-claude/thinking#streaming-thinking.
func TestToAnthropicInvokeStreamBytes_ReasoningSignatureDelta(t *testing.T) {
	signature := "EqQBCgIYAhIM...fixture"
	idx := 0
	resp := &schemas.BifrostResponsesStreamResponse{
		Type:         schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
		ContentIndex: &idx,
		Signature:    &signature,
	}

	frames, err := toAnthropicInvokeStreamBytes(resp)
	require.NoError(t, err)
	require.Len(t, frames, 1)

	var event map[string]interface{}
	require.NoError(t, json.Unmarshal(frames[0], &event))

	delta, ok := event["delta"].(map[string]interface{})
	require.True(t, ok, "expected a delta object")
	assert.Equal(t, "signature_delta", delta["type"], "signature must arrive as its own event type, not nested in thinking_delta")
	assert.Equal(t, signature, delta["signature"])
	_, hasThinking := delta["thinking"]
	assert.False(t, hasThinking, "signature_delta must not carry a thinking field")
}

// --- Regression tests for #5560: InvokeModel silently dropped image / tool_use /
// tool_result blocks because BedrockContentBlock only recognized Converse's
// field-name-discriminated shape, not Anthropic's type-discriminated one. ---

// pngBase64Fixture is a minimal 1x1 red PNG, reused across the tests below.
const pngBase64Fixture = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"

func TestBedrockContentBlock_AnthropicImageNormalized(t *testing.T) {
	raw := `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + pngBase64Fixture + `"}}`

	var block BedrockContentBlock
	require.NoError(t, sonic.Unmarshal([]byte(raw), &block))

	require.NotNil(t, block.Image, "Anthropic-native image block must normalize into Converse-shaped Image")
	assert.Equal(t, "png", block.Image.Format)
	require.NotNil(t, block.Image.Source.Bytes)
	assert.Equal(t, pngBase64Fixture, *block.Image.Source.Bytes)
}

func TestBedrockContentBlock_AnthropicToolUseNormalized(t *testing.T) {
	raw := `{"type":"tool_use","id":"toolu_xyz789","name":"get_time","input":{}}`

	var block BedrockContentBlock
	require.NoError(t, sonic.Unmarshal([]byte(raw), &block))

	require.NotNil(t, block.ToolUse, "Anthropic-native tool_use block must normalize into Converse-shaped ToolUse")
	assert.Equal(t, "toolu_xyz789", block.ToolUse.ToolUseID)
	assert.Equal(t, "get_time", block.ToolUse.Name)
	assert.JSONEq(t, "{}", string(block.ToolUse.Input))
}

func TestBedrockContentBlock_AnthropicToolResultNormalized(t *testing.T) {
	t.Run("string content", func(t *testing.T) {
		raw := `{"type":"tool_result","tool_use_id":"toolu_xyz789","content":"3:45 PM"}`

		var block BedrockContentBlock
		require.NoError(t, sonic.Unmarshal([]byte(raw), &block))

		require.NotNil(t, block.ToolResult, "Anthropic-native tool_result block must normalize into Converse-shaped ToolResult")
		assert.Equal(t, "toolu_xyz789", block.ToolResult.ToolUseID)
		require.Len(t, block.ToolResult.Content, 1)
		require.NotNil(t, block.ToolResult.Content[0].Text)
		assert.Equal(t, "3:45 PM", *block.ToolResult.Content[0].Text)
	})

	t.Run("array content with nested image", func(t *testing.T) {
		// Anthropic's tool_result.content can be an array of blocks, per platform.claude.com/docs/en/api/messages.
		// A nested image must be recursively normalized via BedrockContentBlock.UnmarshalJSON too.
		raw := `{"type":"tool_result","tool_use_id":"tooluse_screenshot_001","content":[
			{"type":"text","text":"Screenshot captured"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + pngBase64Fixture + `"}}
		]}`

		var block BedrockContentBlock
		require.NoError(t, sonic.Unmarshal([]byte(raw), &block))

		require.NotNil(t, block.ToolResult)
		require.Len(t, block.ToolResult.Content, 2)

		require.NotNil(t, block.ToolResult.Content[0].Text)
		assert.Equal(t, "Screenshot captured", *block.ToolResult.Content[0].Text)

		require.NotNil(t, block.ToolResult.Content[1].Image, "nested image block inside tool_result must also normalize")
		assert.Equal(t, "png", block.ToolResult.Content[1].Image.Format)
	})
}

func TestBedrockContentBlock_AnthropicThinkingNormalized(t *testing.T) {
	raw := `{"type":"thinking","thinking":"Let me check the time.","signature":"sig_abc"}`

	var block BedrockContentBlock
	require.NoError(t, sonic.Unmarshal([]byte(raw), &block))

	require.NotNil(t, block.ReasoningContent)
	require.NotNil(t, block.ReasoningContent.ReasoningText)
	require.NotNil(t, block.ReasoningContent.ReasoningText.Text)
	assert.Equal(t, "Let me check the time.", *block.ReasoningContent.ReasoningText.Text)
	require.NotNil(t, block.ReasoningContent.ReasoningText.Signature)
	assert.Equal(t, "sig_abc", *block.ReasoningContent.ReasoningText.Signature)
}

// TestBedrockContentBlock_ConverseShapeUnaffected is the regression guard for the claim that
// the new UnmarshalJSON is a strict superset: genuine Converse-shaped blocks (used by the
// /bedrock/converse route) have no top-level "type" field, so they must be parsed identically
// to the pre-fix behavior.
func TestBedrockContentBlock_ConverseShapeUnaffected(t *testing.T) {
	raw := `{"toolUse":{"toolUseId":"tooluse_1","name":"get_time","input":{}}}`

	var block BedrockContentBlock
	require.NoError(t, sonic.Unmarshal([]byte(raw), &block))

	require.NotNil(t, block.ToolUse)
	assert.Equal(t, "tooluse_1", block.ToolUse.ToolUseID)
	assert.Equal(t, "get_time", block.ToolUse.Name)
	assert.Nil(t, block.Image)
	assert.Nil(t, block.ToolResult)
	assert.Nil(t, block.ReasoningContent)
}

// TestToBedrockConverseRequest_InvokeAnthropicNativeContentBlocks is the full-pipeline
// regression test for issue #5560, using the exact two reproduction payloads from the report:
// an image block and a tool_use/tool_result pair sent to the InvokeModel route in Anthropic's
// native (non-Converse) shape must survive invoke -> Converse -> Bifrost conversion intact.
func TestToBedrockConverseRequest_InvokeAnthropicNativeContentBlocks(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	t.Run("image block survives (issue Test 1)", func(t *testing.T) {
		raw := `{
			"anthropic_version": "bedrock-2023-05-31",
			"max_tokens": 1024,
			"messages": [{"role":"user","content":[
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + pngBase64Fixture + `"}},
				{"type":"text","text":"What color is this image? One word."}
			]}]
		}`

		var req BedrockInvokeRequest
		require.NoError(t, sonic.Unmarshal([]byte(raw), &req))

		converseReq := req.ToBedrockConverseRequest()
		require.NotNil(t, converseReq)

		bifrostReq, err := converseReq.ToBifrostResponsesRequest(ctx)
		require.NoError(t, err)
		require.NotNil(t, bifrostReq)

		var foundImage bool
		for _, msg := range bifrostReq.Input {
			if msg.Content == nil {
				continue
			}
			for _, cb := range msg.Content.ContentBlocks {
				if cb.Type == schemas.ResponsesInputMessageContentBlockTypeImage {
					require.NotNil(t, cb.ResponsesInputMessageContentBlockImage)
					require.NotNil(t, cb.ResponsesInputMessageContentBlockImage.ImageURL)
					assert.Equal(t, "data:image/png;base64,"+pngBase64Fixture, *cb.ResponsesInputMessageContentBlockImage.ImageURL)
					foundImage = true
				}
			}
		}
		assert.True(t, foundImage, "expected an input_image content block to survive the invoke->Converse->Bifrost pipeline")
	})

	t.Run("tool_use/tool_result pair survives (issue Test 2)", func(t *testing.T) {
		raw := `{
			"anthropic_version": "bedrock-2023-05-31",
			"max_tokens": 1024,
			"tools": [{"name":"get_time","description":"get current time","input_schema":{"type":"object","properties":{}}}],
			"messages": [
				{"role":"user","content":[{"type":"text","text":"what time is it"}]},
				{"role":"assistant","content":[{"type":"tool_use","id":"toolu_xyz789","name":"get_time","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_xyz789","content":"3:45 PM"}]}
			]
		}`

		var req BedrockInvokeRequest
		require.NoError(t, sonic.Unmarshal([]byte(raw), &req))

		converseReq := req.ToBedrockConverseRequest()
		require.NotNil(t, converseReq)

		bifrostReq, err := converseReq.ToBifrostResponsesRequest(ctx)
		require.NoError(t, err)
		require.NotNil(t, bifrostReq)

		var foundToolCall, foundToolResult bool
		for _, msg := range bifrostReq.Input {
			if msg.Type == nil || msg.ResponsesToolMessage == nil {
				continue
			}
			switch *msg.Type {
			case schemas.ResponsesMessageTypeFunctionCall:
				if msg.ResponsesToolMessage.CallID != nil && *msg.ResponsesToolMessage.CallID == "toolu_xyz789" {
					require.NotNil(t, msg.ResponsesToolMessage.Name)
					assert.Equal(t, "get_time", *msg.ResponsesToolMessage.Name)
					foundToolCall = true
				}
			case schemas.ResponsesMessageTypeFunctionCallOutput:
				if msg.ResponsesToolMessage.CallID != nil && *msg.ResponsesToolMessage.CallID == "toolu_xyz789" {
					require.NotNil(t, msg.ResponsesToolMessage.Output)
					require.NotNil(t, msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr)
					assert.Equal(t, "3:45 PM", *msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr)
					foundToolResult = true
				}
			}
		}
		assert.True(t, foundToolCall, "expected the tool_use block to survive as a function_call message")
		assert.True(t, foundToolResult, "expected the tool_result block to survive as a function_call_output message")
	})
}

// TestConvertSingleBedrockMessageToBifrostMessages_ImageBlock unit-tests Change 2 in isolation,
// using a struct-literal (Converse-native, non-JSON) BedrockContentBlock so it's independent of
// the UnmarshalJSON normalization above. This proves the fix for the broader gap identified
// during investigation: convertSingleBedrockMessageToBifrostMessages had no Image branch at all,
// so genuine /bedrock/converse traffic dropped images too, not just InvokeModel's Anthropic-shaped input.
func TestConvertSingleBedrockMessageToBifrostMessages_ImageBlock(t *testing.T) {
	imgBytes := pngBase64Fixture
	msg := &BedrockMessage{
		Role: BedrockMessageRoleUser,
		Content: []BedrockContentBlock{
			{Image: &BedrockImageSource{
				Format: "png",
				Source: BedrockImageSourceData{Bytes: &imgBytes},
			}},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result := convertSingleBedrockMessageToBifrostMessages(ctx, msg, false)

	require.Len(t, result, 1)
	require.NotNil(t, result[0].Content)
	require.Len(t, result[0].Content.ContentBlocks, 1)

	cb := result[0].Content.ContentBlocks[0]
	assert.Equal(t, schemas.ResponsesInputMessageContentBlockTypeImage, cb.Type)
	require.NotNil(t, cb.ResponsesInputMessageContentBlockImage)
	require.NotNil(t, cb.ResponsesInputMessageContentBlockImage.ImageURL)
	assert.Equal(t, "data:image/png;base64,"+imgBytes, *cb.ResponsesInputMessageContentBlockImage.ImageURL)
}

// TestBedrockInvokeRequest_UnmarshalJSON_TextContentBlockCacheControlSurvives is the regression
// test for the reviewer comment on this file's system-message cache_control fix: cache_control
// is handled for system messages (parseSystemMessages) and tools (convertAnthropicTools), but not
// for ordinary message content blocks. A plain text content block's cache_control has no
// BedrockContentBlock field to land on (only "cachePoint" is a recognized JSON key), so it must be
// unmarshalled from real JSON — a struct literal can't reproduce the bug since there's no field to
// set. Bedrock Converse expects a standalone trailing cachePoint entry instead.
func TestBedrockInvokeRequest_UnmarshalJSON_TextContentBlockCacheControlSurvives(t *testing.T) {
	raw := `{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens": 16,
		"messages": [{"role":"user","content":[
			{"type":"text","text":"Long context to cache.","cache_control":{"type":"ephemeral"}}
		]}]
	}`

	var req BedrockInvokeRequest
	require.NoError(t, sonic.Unmarshal([]byte(raw), &req))

	require.Len(t, req.Messages, 1)
	require.Len(t, req.Messages[0].Content, 2, "expected the text block plus a trailing standalone cachePoint entry")

	require.NotNil(t, req.Messages[0].Content[0].Text)
	assert.Equal(t, "Long context to cache.", *req.Messages[0].Content[0].Text)
	assert.Nil(t, req.Messages[0].Content[0].CachePoint)

	assert.Nil(t, req.Messages[0].Content[1].Text)
	require.NotNil(t, req.Messages[0].Content[1].CachePoint)
	assert.Equal(t, BedrockCachePointTypeDefault, req.Messages[0].Content[1].CachePoint.Type)
}

// TestBedrockInvokeRequest_UnmarshalJSON_ImageContentBlockCacheControlSurvives extends the
// text-block case to an image block, confirming applyMessageContentCacheControl's cache_control
// detection is block-type-agnostic (it queries the raw "cache_control" path directly, independent
// of which BedrockContentBlock.UnmarshalJSON normalization branch produced the block).
func TestBedrockInvokeRequest_UnmarshalJSON_ImageContentBlockCacheControlSurvives(t *testing.T) {
	raw := `{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens": 16,
		"messages": [{"role":"user","content":[
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + pngBase64Fixture + `"},"cache_control":{"type":"ephemeral"}}
		]}]
	}`

	var req BedrockInvokeRequest
	require.NoError(t, sonic.Unmarshal([]byte(raw), &req))

	require.Len(t, req.Messages, 1)
	require.Len(t, req.Messages[0].Content, 2)

	require.NotNil(t, req.Messages[0].Content[0].Image)
	assert.Equal(t, "png", req.Messages[0].Content[0].Image.Format)

	require.NotNil(t, req.Messages[0].Content[1].CachePoint)
	assert.Equal(t, BedrockCachePointTypeDefault, req.Messages[0].Content[1].CachePoint.Type)
}

// TestBedrockInvokeRequest_UnmarshalJSON_ToolUseContentBlockCacheControlSurvives extends the
// same case to a tool_use block.
func TestBedrockInvokeRequest_UnmarshalJSON_ToolUseContentBlockCacheControlSurvives(t *testing.T) {
	raw := `{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens": 16,
		"messages": [{"role":"assistant","content":[
			{"type":"tool_use","id":"toolu_xyz789","name":"get_time","input":{},"cache_control":{"type":"ephemeral"}}
		]}]
	}`

	var req BedrockInvokeRequest
	require.NoError(t, sonic.Unmarshal([]byte(raw), &req))

	require.Len(t, req.Messages, 1)
	require.Len(t, req.Messages[0].Content, 2)

	require.NotNil(t, req.Messages[0].Content[0].ToolUse)
	assert.Equal(t, "toolu_xyz789", req.Messages[0].Content[0].ToolUse.ToolUseID)

	require.NotNil(t, req.Messages[0].Content[1].CachePoint)
	assert.Equal(t, BedrockCachePointTypeDefault, req.Messages[0].Content[1].CachePoint.Type)
}

// TestBedrockInvokeRequest_UnmarshalJSON_ToolResultContentBlockCacheControlSurvives covers
// cache_control on the tool_result block itself (not nested inside its content) — string content.
func TestBedrockInvokeRequest_UnmarshalJSON_ToolResultContentBlockCacheControlSurvives(t *testing.T) {
	raw := `{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens": 16,
		"messages": [{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_xyz789","content":"3:45 PM","cache_control":{"type":"ephemeral"}}
		]}]
	}`

	var req BedrockInvokeRequest
	require.NoError(t, sonic.Unmarshal([]byte(raw), &req))

	require.Len(t, req.Messages, 1)
	require.Len(t, req.Messages[0].Content, 2)

	require.NotNil(t, req.Messages[0].Content[0].ToolResult)
	assert.Equal(t, "toolu_xyz789", req.Messages[0].Content[0].ToolResult.ToolUseID)

	require.NotNil(t, req.Messages[0].Content[1].CachePoint)
	assert.Equal(t, BedrockCachePointTypeDefault, req.Messages[0].Content[1].CachePoint.Type)
}

// TestBedrockInvokeRequest_UnmarshalJSON_ToolResultNestedContentCacheControlSurvives is the
// recursion case: cache_control nested inside a tool_result block's own array-form content
// (Anthropic allows an independent cache breakpoint there, separate from one on the tool_result
// block itself). applyMessageContentCacheControl must recurse into ToolResult.Content.
func TestBedrockInvokeRequest_UnmarshalJSON_ToolResultNestedContentCacheControlSurvives(t *testing.T) {
	raw := `{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens": 16,
		"messages": [{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_xyz789","content":[
				{"type":"text","text":"Screenshot captured","cache_control":{"type":"ephemeral"}}
			]}
		]}]
	}`

	var req BedrockInvokeRequest
	require.NoError(t, sonic.Unmarshal([]byte(raw), &req))

	require.Len(t, req.Messages, 1)
	require.Len(t, req.Messages[0].Content, 1, "no cache_control on the tool_result block itself, so no top-level sibling")

	toolResult := req.Messages[0].Content[0].ToolResult
	require.NotNil(t, toolResult)
	require.Len(t, toolResult.Content, 2, "expected the nested text block plus a trailing standalone cachePoint entry")

	require.NotNil(t, toolResult.Content[0].Text)
	assert.Equal(t, "Screenshot captured", *toolResult.Content[0].Text)
	assert.Nil(t, toolResult.Content[0].CachePoint)

	assert.Nil(t, toolResult.Content[1].Text)
	require.NotNil(t, toolResult.Content[1].CachePoint)
	assert.Equal(t, BedrockCachePointTypeDefault, toolResult.Content[1].CachePoint.Type)
}

// TestBedrockInvokeRequest_UnmarshalJSON_ContentBlockNoCacheControlUnaffected is the regression
// guard: with no cache_control anywhere in the message content, applyMessageContentCacheControl
// must be a no-op — no injected CachePoint siblings, content byte-identical to pre-fix behavior.
func TestBedrockInvokeRequest_UnmarshalJSON_ContentBlockNoCacheControlUnaffected(t *testing.T) {
	raw := `{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens": 16,
		"messages": [
			{"role":"user","content":[{"type":"text","text":"what time is it"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_xyz789","name":"get_time","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_xyz789","content":"3:45 PM"}]}
		]
	}`

	var req BedrockInvokeRequest
	require.NoError(t, sonic.Unmarshal([]byte(raw), &req))

	require.Len(t, req.Messages, 3)
	for _, msg := range req.Messages {
		require.Len(t, msg.Content, 1, "no cache_control anywhere, so no CachePoint siblings should be injected")
		assert.Nil(t, msg.Content[0].CachePoint)
	}
}

// TestBedrockInvokeRequest_UnmarshalJSON_ConverseShapeContentUnaffected confirms genuine
// Converse-shaped messages (no Anthropic "type" discriminator, no cache_control keys at all —
// used by Nova and the native /bedrock/converse route sharing this same untyped-JSON path) are
// left untouched: applyMessageContentCacheControl's gjson lookups simply never match, since
// Converse-shaped input never carries a raw "cache_control" key.
func TestBedrockInvokeRequest_UnmarshalJSON_ConverseShapeContentUnaffected(t *testing.T) {
	raw := `{
		"messages": [{"role":"user","content":[
			{"text":"Say OK."},
			{"toolUse":{"toolUseId":"tooluse_1","name":"get_time","input":{}}},
			{"cachePoint":{"type":"default"}}
		]}]
	}`

	var req BedrockInvokeRequest
	require.NoError(t, sonic.Unmarshal([]byte(raw), &req))

	require.Len(t, req.Messages, 1)
	require.Len(t, req.Messages[0].Content, 3, "no extra CachePoint siblings injected beyond the genuine one already present")

	require.NotNil(t, req.Messages[0].Content[0].Text)
	assert.Equal(t, "Say OK.", *req.Messages[0].Content[0].Text)

	require.NotNil(t, req.Messages[0].Content[1].ToolUse)
	assert.Equal(t, "tooluse_1", req.Messages[0].Content[1].ToolUse.ToolUseID)

	require.NotNil(t, req.Messages[0].Content[2].CachePoint)
	assert.Equal(t, BedrockCachePointTypeDefault, req.Messages[0].Content[2].CachePoint.Type)
}

// TestToBedrockConverseRequest_InvokeContentBlockCacheControlEndToEnd is the full-pipeline
// regression test for the reviewer comment: cache_control on an ordinary message content block,
// and cache_control nested inside a tool_result's own content, must both survive
// invoke -> Converse -> Bifrost conversion, the same way system/tool cache_control already does
// (TestToBedrockConverseRequest_InvokeCacheControlEndToEnd).
func TestToBedrockConverseRequest_InvokeContentBlockCacheControlEndToEnd(t *testing.T) {
	raw := `{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens": 16,
		"tools": [{"name":"get_time","description":"get current time","input_schema":{"type":"object","properties":{}}}],
		"messages": [
			{"role":"user","content":[{"type":"text","text":"what time is it","cache_control":{"type":"ephemeral"}}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_xyz789","name":"get_time","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_xyz789","content":[
				{"type":"text","text":"3:45 PM","cache_control":{"type":"ephemeral"}}
			]}]}
		]
	}`

	var req BedrockInvokeRequest
	require.NoError(t, sonic.Unmarshal([]byte(raw), &req))

	converseReq := req.ToBedrockConverseRequest()
	require.NotNil(t, converseReq)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostReq, err := converseReq.ToBifrostResponsesRequest(ctx)
	require.NoError(t, err)
	require.NotNil(t, bifrostReq)

	// Plain content-block cache breakpoint survived, landing on the text content block itself.
	var userTextMsg *schemas.ResponsesMessage
	for i := range bifrostReq.Input {
		if bifrostReq.Input[i].Content != nil {
			for _, cb := range bifrostReq.Input[i].Content.ContentBlocks {
				if cb.Text != nil && *cb.Text == "what time is it" {
					userTextMsg = &bifrostReq.Input[i]
				}
			}
		}
	}
	require.NotNil(t, userTextMsg, "expected the plain user text message in the converted input")
	lastBlock := userTextMsg.Content.ContentBlocks[len(userTextMsg.Content.ContentBlocks)-1]
	require.NotNil(t, lastBlock.CacheControl, "cache_control on an ordinary content block must survive")
	assert.Equal(t, schemas.CacheControlTypeEphemeral, lastBlock.CacheControl.Type)

	// Nested tool_result cache breakpoint survived too.
	var foundToolResult bool
	for i := range bifrostReq.Input {
		msg := bifrostReq.Input[i]
		if msg.Type == nil || *msg.Type != schemas.ResponsesMessageTypeFunctionCallOutput || msg.ResponsesToolMessage == nil {
			continue
		}
		if msg.ResponsesToolMessage.CallID == nil || *msg.ResponsesToolMessage.CallID != "toolu_xyz789" {
			continue
		}
		require.NotNil(t, msg.CacheControl, "cache_control nested inside tool_result.content must survive")
		assert.Equal(t, schemas.CacheControlTypeEphemeral, msg.CacheControl.Type)
		foundToolResult = true
	}
	assert.True(t, foundToolResult, "expected the tool_result message to survive as a function_call_output message")
}
