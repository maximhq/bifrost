package bedrock

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
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
	require.Len(t, toolConfig.Tools, 2, "the tool_search_tool_* entry is carried as a marker, not dropped (#7155)")
	// The invariant this test guards is unchanged: the entry must never become an
	// invocable ToolSpec. It is now carried on an ingress-only marker instead of
	// being discarded, so the egress predicate can route the request to InvokeModel.
	require.Nil(t, toolConfig.Tools[0].ToolSpec, "tool_search must never become an invocable tool")
	require.NotNil(t, toolConfig.Tools[0].AnthropicToolSearch)
	assert.Equal(t, "tool_search_tool_regex_20251119", toolConfig.Tools[0].AnthropicToolSearch.Type)
	assert.Equal(t, "tool_search_tool_regex", toolConfig.Tools[0].AnthropicToolSearch.Name)
	require.NotNil(t, toolConfig.Tools[1].ToolSpec)
	assert.Equal(t, "keep_me", toolConfig.Tools[1].ToolSpec.Name)
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

	frames, err := toAnthropicInvokeStreamBytes(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), resp)
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

	frames, err := toAnthropicInvokeStreamBytes(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), resp)
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

	frames, err := toAnthropicInvokeStreamBytes(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), resp)
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

	frames, err := toAnthropicInvokeStreamBytes(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), resp)
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

// usesAnthropicInvokePath decides which Claude requests leave Converse for
// InvokeModel (#6825). Only a compact_20260112 edit on an Anthropic-family
// model qualifies; everything else must keep the Converse path it has today.
func TestUsesAnthropicInvokePath(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	compact := json.RawMessage(`{"edits":[{"type":"compact_20260112","trigger":{"type":"input_tokens","value":50000}}]}`)
	clearOnly := json.RawMessage(`{"edits":[{"type":"clear_tool_uses_20250919"}]}`)
	mixed := json.RawMessage(`{"edits":[{"type":"clear_tool_uses_20250919"},{"type":"compact_20260112"}]}`)

	tests := []struct {
		name  string
		model string
		cm    json.RawMessage
		want  bool
	}{
		{"claude with compaction edit", "us.anthropic.claude-sonnet-4-6", compact, true},
		{"claude with region prefix and compaction edit", "us-west-2/anthropic.claude-opus-4-6-v1", compact, true},
		{"claude with compaction among other edits", "anthropic.claude-sonnet-4-6", mixed, true},
		{"claude with clear_tool_uses only stays on converse", "anthropic.claude-sonnet-4-6", clearOnly, false},
		{"claude without context_management stays on converse", "anthropic.claude-sonnet-4-6", nil, false},
		{"claude with empty edits stays on converse", "anthropic.claude-sonnet-4-6", json.RawMessage(`{"edits":[]}`), false},
		{"nova with compaction edit stays on converse", "amazon.nova-pro-v1:0", compact, false},
		{"llama with compaction edit stays on converse", "meta.llama3-70b-instruct-v1:0", compact, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, usesAnthropicInvokePath(ctx, tt.model, tt.cm, nil))
		})
	}
}

func TestUsesAnthropicInvokePath_NilParams(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	require.False(t, chatUsesAnthropicInvokePath(ctx, &schemas.BifrostChatRequest{Model: "anthropic.claude-sonnet-4-6"}))
	require.False(t, responsesUsesAnthropicInvokePath(ctx, &schemas.BifrostResponsesRequest{Model: "anthropic.claude-sonnet-4-6"}))
	require.False(t, chatUsesAnthropicInvokePath(ctx, nil))
	require.False(t, responsesUsesAnthropicInvokePath(ctx, nil))
}

func TestInvokeURL_UsesRuntimeHostAndAction(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	provider := &BedrockProvider{}
	key := schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{Region: schemas.NewSecretVar("us-east-1")}}

	url, region := provider.invokeURL(ctx, key, "us.anthropic.claude-sonnet-4-6", bedrockInvokeStreamAction)
	require.Equal(t, "us-east-1", region)
	require.Equal(t, "https://bedrock-runtime.us-east-1.amazonaws.com/model/us.anthropic.claude-sonnet-4-6/invoke-with-response-stream", url)

	url, _ = provider.invokeURL(ctx, key, "eu-west-1/anthropic.claude-opus-4-6-v1", bedrockInvokeAction)
	require.Equal(t, "https://bedrock-runtime.eu-west-1.amazonaws.com/model/anthropic.claude-opus-4-6-v1/invoke", url)
}

// The /anthropic/v1/messages ingress (the path in #6825) does not populate the
// raw Params.ContextManagement field. AnthropicMessageRequest.ToBifrostResponsesRequest
// stores a typed *anthropic.ContextManagement under ExtraParams["context_management"],
// and older callers may store a plain map there. The routing predicate must see
// all three shapes, or the reporter's traffic never leaves Converse.
func TestUsesAnthropicInvokePath_ExtraParamsShapes(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	model := "us.anthropic.claude-opus-4-8"
	typed := &anthropic.ContextManagement{
		Edits: []anthropic.ContextManagementEdit{{Type: anthropic.ContextManagementEditTypeCompact}},
	}
	asMap := map[string]interface{}{
		"edits": []interface{}{map[string]interface{}{"type": "compact_20260112"}},
	}
	clearTyped := &anthropic.ContextManagement{
		Edits: []anthropic.ContextManagementEdit{{Type: anthropic.ContextManagementEditTypeClearToolUses}},
	}

	t.Run("responses typed pointer in extra params routes to invoke", func(t *testing.T) {
		req := &schemas.BifrostResponsesRequest{Model: model, Params: &schemas.ResponsesParameters{
			ExtraParams: map[string]interface{}{"context_management": typed},
		}}
		require.True(t, responsesUsesAnthropicInvokePath(ctx, req))
	})
	t.Run("responses map in extra params routes to invoke", func(t *testing.T) {
		req := &schemas.BifrostResponsesRequest{Model: model, Params: &schemas.ResponsesParameters{
			ExtraParams: map[string]interface{}{"context_management": asMap},
		}}
		require.True(t, responsesUsesAnthropicInvokePath(ctx, req))
	})
	t.Run("chat typed pointer in extra params routes to invoke", func(t *testing.T) {
		req := &schemas.BifrostChatRequest{Model: model, Params: &schemas.ChatParameters{
			ExtraParams: map[string]interface{}{"context_management": typed},
		}}
		require.True(t, chatUsesAnthropicInvokePath(ctx, req))
	})
	t.Run("clear-only typed pointer stays on converse", func(t *testing.T) {
		req := &schemas.BifrostResponsesRequest{Model: model, Params: &schemas.ResponsesParameters{
			ExtraParams: map[string]interface{}{"context_management": clearTyped},
		}}
		require.False(t, responsesUsesAnthropicInvokePath(ctx, req))
	})
	t.Run("raw field wins even when extra params is unrelated", func(t *testing.T) {
		req := &schemas.BifrostResponsesRequest{Model: model, Params: &schemas.ResponsesParameters{
			ContextManagement: json.RawMessage(`{"edits":[{"type":"compact_20260112"}]}`),
			ExtraParams:       map[string]interface{}{"context_management": clearTyped},
		}}
		require.True(t, responsesUsesAnthropicInvokePath(ctx, req))
	})
}

// Supported safeguards requests use InvokeModel so the native field and beta
// reach Claude together. Absent or unsupported safeguards preserve Converse.
func TestResponsesUsesAnthropicInvokePath_Safeguards(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	model := "us.anthropic.claude-sonnet-5"

	t.Run("nonempty safeguards diverts to invoke", func(t *testing.T) {
		for _, v := range []interface{}{
			json.RawMessage(`{"check":"auto_mode"}`),
			[]byte(`{"check":"auto_mode"}`),
			json.RawMessage(``),
			nil,
		} {
			req := &schemas.BifrostResponsesRequest{Model: model, Params: &schemas.ResponsesParameters{
				ExtraParams: map[string]interface{}{"safeguards": v},
			}}
			want := extraParamsHasSafeguards(req.Params.ExtraParams)
			require.Equal(t, want, responsesUsesAnthropicInvokePath(ctx, req))
			require.Equal(t, want, chatUsesAnthropicInvokePath(ctx, &schemas.BifrostChatRequest{Model: model, Params: &schemas.ChatParameters{ExtraParams: req.Params.ExtraParams}}))
		}
	})
	t.Run("absent safeguards stays on converse", func(t *testing.T) {
		req := &schemas.BifrostResponsesRequest{Model: model, Params: &schemas.ResponsesParameters{}}
		require.False(t, responsesUsesAnthropicInvokePath(ctx, req))
	})
	t.Run("unsupported model stays on converse", func(t *testing.T) {
		extra := map[string]interface{}{"safeguards": json.RawMessage(`{"check":"auto_mode"}`)}
		require.False(t, responsesUsesAnthropicInvokePath(ctx, &schemas.BifrostResponsesRequest{Model: "us.anthropic.claude-haiku-4-5", Params: &schemas.ResponsesParameters{ExtraParams: extra}}))
		require.False(t, chatUsesAnthropicInvokePath(ctx, &schemas.BifrostChatRequest{Model: "us.anthropic.claude-haiku-4-5", Params: &schemas.ChatParameters{ExtraParams: extra}}))
	})
	t.Run("catalog can disable invoke routing", func(t *testing.T) {
		schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities {
			return &schemas.ModelCapabilities{SupportsSafeguards: schemas.Ptr(false)}
		})
		defer schemas.SetCapabilityResolver(nil)
		extra := map[string]interface{}{"safeguards": json.RawMessage(`{"check":"auto_mode"}`)}
		require.False(t, responsesUsesAnthropicInvokePath(ctx, &schemas.BifrostResponsesRequest{Model: model, Params: &schemas.ResponsesParameters{ExtraParams: extra}}))
		require.False(t, chatUsesAnthropicInvokePath(ctx, &schemas.BifrostChatRequest{Model: model, Params: &schemas.ChatParameters{ExtraParams: extra}}))
	})
	t.Run("non-anthropic model stays on converse even with safeguards", func(t *testing.T) {
		req := &schemas.BifrostResponsesRequest{Model: "us.amazon.nova-lite-v1:0", Params: &schemas.ResponsesParameters{
			ExtraParams: map[string]interface{}{"safeguards": json.RawMessage(`{"check":"auto_mode"}`)},
		}}
		require.False(t, responsesUsesAnthropicInvokePath(ctx, req))
	})
	// Catalog overrides remain authoritative for model support.
	t.Run("datasheet-enabled pair diverts to invoke", func(t *testing.T) {
		yes := true
		schemas.SetCapabilityResolver(func(p schemas.ModelProvider, m string) *schemas.ModelCapabilities {
			if p == schemas.Bedrock {
				return &schemas.ModelCapabilities{SupportsSafeguards: &yes}
			}
			return nil
		})
		defer schemas.SetCapabilityResolver(nil)

		req := &schemas.BifrostResponsesRequest{Model: model, Params: &schemas.ResponsesParameters{
			ExtraParams: map[string]interface{}{"safeguards": json.RawMessage(`{"check":"auto_mode"}`)},
		}}
		require.True(t, responsesUsesAnthropicInvokePath(ctx, req))
	})
}

// The InvokeModel route (and Mantle) go through fasthttp, whose path
// normalisation decodes a percent-encoded inference-profile ARN in the model
// segment. The provider must build its fasthttp clients with that disabled;
// the streaming client is a clone, so the flag must survive cloning too.
func TestNewBedrockProvider_FasthttpClientsPreservePathEncoding(t *testing.T) {
	provider, err := NewBedrockProvider(&schemas.ProviderConfig{
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 1},
	}, noopLogger{})
	require.NoError(t, err)
	require.True(t, provider.mantleClient.DisablePathNormalizing, "unary fasthttp client must preserve percent-encoded model paths")
	require.True(t, provider.mantleStreamingClient.DisablePathNormalizing, "streaming fasthttp client (a clone) must preserve percent-encoded model paths")
}

// writeInvokeChunk frames one native Anthropic SSE event the way
// InvokeModelWithResponseStream does: a "chunk" event whose payload is
// {"bytes": base64(event JSON)}.
func writeInvokeChunk(t *testing.T, w io.Writer, eventJSON string) {
	t.Helper()
	payload := []byte(`{"bytes":"` + base64.StdEncoding.EncodeToString([]byte(eventJSON)) + `"}`)
	writeEventStreamEvent(t, w, "chunk", payload)
}

func TestInvokeEventStreamReader_YieldsAnthropicEvents(t *testing.T) {
	var buf bytes.Buffer
	writeInvokeChunk(t, &buf, `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":12,"output_tokens":1}}}`)
	writeInvokeChunk(t, &buf, `{"type":"content_block_start","index":0,"content_block":{"type":"compaction","content":""}}`)
	writeInvokeChunk(t, &buf, `{"type":"content_block_delta","index":0,"delta":{"type":"compaction_delta","content":"summary"}}`)

	reader := newInvokeEventStreamReader(&buf)

	eventType, data, err := reader.ReadEvent()
	require.NoError(t, err)
	require.Equal(t, "message_start", eventType)
	require.Contains(t, string(data), `"id":"msg_1"`)

	eventType, data, err = reader.ReadEvent()
	require.NoError(t, err)
	require.Equal(t, "content_block_start", eventType)
	require.Contains(t, string(data), `"type":"compaction"`)

	eventType, data, err = reader.ReadEvent()
	require.NoError(t, err)
	require.Equal(t, "content_block_delta", eventType)
	require.Contains(t, string(data), `"compaction_delta"`)

	_, _, err = reader.ReadEvent()
	require.ErrorIs(t, err, io.EOF)
}

func TestInvokeEventStreamReader_ExceptionFrameIsAnError(t *testing.T) {
	var buf bytes.Buffer
	enc := eventstream.NewEncoder()
	headers := eventstream.Headers{
		{Name: ":message-type", Value: eventstream.StringValue("exception")},
		{Name: ":exception-type", Value: eventstream.StringValue("validationException")},
		{Name: ":content-type", Value: eventstream.StringValue("application/json")},
	}
	require.NoError(t, enc.Encode(&buf, eventstream.Message{
		Headers: headers,
		Payload: []byte(`{"message":"compaction trigger must be at least 50000 tokens"}`),
	}))

	reader := newInvokeEventStreamReader(&buf)
	_, _, err := reader.ReadEvent()
	require.Error(t, err)
	require.Contains(t, err.Error(), "compaction trigger must be at least 50000 tokens")

	// The error must carry the classified BifrostError so the shared stream
	// loop can forward it unchanged: validationException is terminal.
	var carrier providerUtils.BifrostErrorCarrier
	require.ErrorAs(t, err, &carrier)
	typed := carrier.BifrostError()
	require.NotNil(t, typed)
	assert.True(t, typed.IsBifrostError, "validationException is not retryable")
	require.NotNil(t, typed.Type)
	assert.Equal(t, "validationException", *typed.Type)
}

func TestInvokeEventStreamReader_RetryableExceptionKeepsClassification(t *testing.T) {
	var buf bytes.Buffer
	writeEventStreamException(t, &buf, "throttlingException", "slow down")

	_, _, err := newInvokeEventStreamReader(&buf).ReadEvent()
	var carrier providerUtils.BifrostErrorCarrier
	require.ErrorAs(t, err, &carrier)
	typed := carrier.BifrostError()
	require.NotNil(t, typed)
	assert.False(t, typed.IsBifrostError, "throttlingException must stay retryable")
	require.NotNil(t, typed.StatusCode)
	assert.Equal(t, 429, *typed.StatusCode)
}

func TestInvokeEventStreamReader_SkipsEmptyChunks(t *testing.T) {
	var buf bytes.Buffer
	writeEventStreamEvent(t, &buf, "chunk", []byte(`{"bytes":""}`))
	writeInvokeChunk(t, &buf, `{"type":"message_stop"}`)

	reader := newInvokeEventStreamReader(&buf)
	eventType, _, err := reader.ReadEvent()
	require.NoError(t, err)
	require.Equal(t, "message_stop", eventType)
}

// newTestInvokeStreamServer serves one AWS event-stream exception frame on any
// request, over TLS because the InvokeModel URL is always https, and points the
// provider's fasthttp streaming client at it.
func newTestInvokeStreamServer(t *testing.T, excType, msg string) (*BedrockProvider, func()) {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		writeEventStreamException(t, w, excType, msg)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	provider := newTestProviderWithServer(t, ts)
	addr := strings.TrimPrefix(ts.URL, "https://")
	provider.mantleStreamingClient = &fasthttp.Client{
		Dial:                   func(string) (net.Conn, error) { return net.Dial("tcp", addr) },
		TLSConfig:              &tls.Config{InsecureSkipVerify: true},
		DisablePathNormalizing: true,
		ReadTimeout:            5 * time.Second,
		WriteTimeout:           5 * time.Second,
	}
	return provider, ts.Close
}

// A retryable AWS exception delivered as the first InvokeModelWithResponseStream
// frame must reach the caller with the same classification the Converse path
// gives it (IsBifrostError:false plus the mapped status), so the retry gate in
// executeRequestWithRetries can act on it. Mirrors
// TestChatCompletionStream_RetryableException_ChunkIsRetryable for the invoke route.
func TestInvokeAnthropicResponsesStream_RetryableException_ChunkIsRetryable(t *testing.T) {
	tests := []struct {
		excType        string
		expectedStatus int
	}{
		{"throttlingException", 429},
		{"serviceUnavailableException", 503},
	}
	for _, tc := range tests {
		t.Run(tc.excType, func(t *testing.T) {
			provider, closeServer := newTestInvokeStreamServer(t, tc.excType, "please retry")
			defer closeServer()

			role := schemas.ResponsesInputMessageRoleUser
			text := "hello"
			req := &schemas.BifrostResponsesRequest{
				Provider: schemas.Bedrock,
				Model:    "anthropic.claude-sonnet-4-5",
				Input:    []schemas.ResponsesMessage{{Role: &role, Content: &schemas.ResponsesMessageContent{ContentStr: &text}}},
				Params: &schemas.ResponsesParameters{
					ContextManagement: json.RawMessage(`{"edits":[{"type":"compact_20260112"}]}`),
				},
			}
			streamChan, bifrostErr := provider.ResponsesStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), req)
			require.Nil(t, bifrostErr, "expected the exception to surface as a stream chunk")
			require.NotNil(t, streamChan)

			var errChunk *schemas.BifrostStreamChunk
			for chunk := range streamChan {
				if chunk != nil && chunk.BifrostError != nil {
					errChunk = chunk
					break
				}
			}
			for range streamChan {
			}

			require.NotNil(t, errChunk, "expected error chunk for %s", tc.excType)
			assert.False(t, errChunk.BifrostError.IsBifrostError,
				"%s must be IsBifrostError:false so the retry gate can retry it", tc.excType)
			require.NotNil(t, errChunk.BifrostError.StatusCode,
				"%s must carry a StatusCode for the retry gate", tc.excType)
			assert.Equal(t, tc.expectedStatus, *errChunk.BifrostError.StatusCode,
				"%s must map to HTTP %d", tc.excType, tc.expectedStatus)
		})
	}
}

// Tool search is the second InvokeModel-only feature on Bedrock: "On Amazon
// Bedrock, server-side tool search is available only through the InvokeModel
// API, not the Converse API."
// (https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool)
// A request that carries a tool_search tool, or any tool with defer_loading
// (which only means something alongside tool search), must leave Converse.
func TestUsesAnthropicInvokePath_ToolSearch(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	model := "us.anthropic.claude-opus-4-6-v1"
	deferred := true

	t.Run("responses tool_search tool routes to invoke", func(t *testing.T) {
		req := &schemas.BifrostResponsesRequest{Model: model, Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeToolSearch, Name: schemas.Ptr("tool_search_tool_regex")}},
		}}
		require.True(t, responsesUsesAnthropicInvokePath(ctx, req))
	})
	t.Run("responses deferred function tool routes to invoke", func(t *testing.T) {
		req := &schemas.BifrostResponsesRequest{Model: model, Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("get_weather"), DeferLoading: &deferred}},
		}}
		require.True(t, responsesUsesAnthropicInvokePath(ctx, req))
	})
	t.Run("responses plain function tool stays on converse", func(t *testing.T) {
		req := &schemas.BifrostResponsesRequest{Model: model, Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("get_weather")}},
		}}
		require.False(t, responsesUsesAnthropicInvokePath(ctx, req))
	})
	t.Run("chat dated tool_search type routes to invoke", func(t *testing.T) {
		req := &schemas.BifrostChatRequest{Model: model, Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{Type: "tool_search_tool_regex_20251119"}},
		}}
		require.True(t, chatUsesAnthropicInvokePath(ctx, req))
	})
	t.Run("chat deferred function tool routes to invoke", func(t *testing.T) {
		req := &schemas.BifrostChatRequest{Model: model, Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "get_weather"}, DeferLoading: &deferred}},
		}}
		require.True(t, chatUsesAnthropicInvokePath(ctx, req))
	})
	t.Run("nova with tool_search stays on converse", func(t *testing.T) {
		req := &schemas.BifrostResponsesRequest{Model: "amazon.nova-pro-v1:0", Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeToolSearch, Name: schemas.Ptr("tool_search_tool_regex")}},
		}}
		require.False(t, responsesUsesAnthropicInvokePath(ctx, req))
	})
}

// CountTokens stays on the Converse count-tokens envelope for ordinary
// requests, but a request that is routed to InvokeModel must be counted with
// the same body it will be sent with. AWS's CountTokens input is a union of
// "converse" and "invokeModel" ({"body": <base64 of the InvokeModel body>}):
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_CountTokensInput.html
func TestBedrockCountTokensBody_UsesInvokeModelInputForRoutedRequests(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	provider := &BedrockProvider{}
	role := schemas.ResponsesInputMessageRoleUser
	text := "Hello!"
	input := []schemas.ResponsesMessage{{Role: &role, Content: &schemas.ResponsesMessageContent{ContentStr: &text}}}

	t.Run("compaction request counts via invokeModel", func(t *testing.T) {
		req := &schemas.BifrostResponsesRequest{Provider: schemas.Bedrock, Model: "global.anthropic.claude-sonnet-4-6", Input: input, Params: &schemas.ResponsesParameters{
			ContextManagement: json.RawMessage(`{"edits":[{"type":"compact_20260112"}]}`),
		}}
		body, err := provider.buildCountTokensBody(ctx, req)
		require.NoError(t, err)
		encoded := gjson.GetBytes(body, "input.invokeModel.body").String()
		require.NotEmpty(t, encoded, "expected input.invokeModel.body, got %s", string(body))
		require.False(t, gjson.GetBytes(body, "input.converse").Exists(), "union must carry one member: %s", string(body))
		decoded, decErr := base64.StdEncoding.DecodeString(encoded)
		require.NoError(t, decErr)
		require.Equal(t, "bedrock-2023-05-31", gjson.GetBytes(decoded, "anthropic_version").String(), "decoded body: %s", string(decoded))
		require.Equal(t, "compact_20260112", gjson.GetBytes(decoded, "context_management.edits.0.type").String())
	})
	t.Run("tool_search request counts via invokeModel", func(t *testing.T) {
		req := &schemas.BifrostResponsesRequest{Provider: schemas.Bedrock, Model: "global.anthropic.claude-sonnet-4-6", Input: input, Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeToolSearch, Name: schemas.Ptr("tool_search_tool_regex")}},
		}}
		body, err := provider.buildCountTokensBody(ctx, req)
		require.NoError(t, err)
		require.True(t, gjson.GetBytes(body, "input.invokeModel.body").Exists(), "expected invokeModel input, got %s", string(body))
	})
	t.Run("plain request counts via converse", func(t *testing.T) {
		req := &schemas.BifrostResponsesRequest{Provider: schemas.Bedrock, Model: "global.anthropic.claude-sonnet-4-6", Input: input}
		body, err := provider.buildCountTokensBody(ctx, req)
		require.NoError(t, err)
		require.True(t, gjson.GetBytes(body, "input.converse").Exists(), "expected converse input, got %s", string(body))
		require.False(t, gjson.GetBytes(body, "input.invokeModel").Exists())
	})
}

// TestToBedrockConverseRequest_InvokeToolSearchEndToEnd is the full-pipeline regression
// test for #7155: a tool-search request arriving on the Bedrock-native invoke ingress
// (POST /bedrock/model/{modelId}/invoke) must keep the tool_search_tool_* server tool and
// the per-tool defer_loading flag through the mandatory invoke -> Converse -> neutral
// conversion, so #6908's egress predicate (responsesUsesAnthropicInvokePath) can pick
// InvokeModel. AWS restricts server-side tool search to InvokeModel, never Converse:
// "On Amazon Bedrock, server-side tool search is available only through the InvokeModel
// API, not the Converse API."
// (https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool)
func TestToBedrockConverseRequest_InvokeToolSearchEndToEnd(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

	deferredTool := anthropicToolMap("get_weather", nil)
	deferredTool["defer_loading"] = true

	req := &BedrockInvokeRequest{
		ModelID: "us.anthropic.claude-sonnet-4-6-v1:0",
		Messages: []BedrockMessage{{
			Role:    BedrockMessageRoleUser,
			Content: []BedrockContentBlock{{Text: schemas.Ptr("What is the weather in Paris? Use your tools.")}},
		}},
		Tools: []interface{}{
			map[string]interface{}{"type": "tool_search_tool_regex_20251119", "name": "tool_search_tool_regex"},
			deferredTool,
		},
	}

	converseReq := req.ToBedrockConverseRequest()
	responsesReq, err := converseReq.ToBifrostResponsesRequest(ctx)
	require.NoError(t, err)
	require.NotNil(t, responsesReq.Params)

	var sawToolSearch, sawDeferred bool
	for _, tool := range responsesReq.Params.Tools {
		if tool.Type == schemas.ResponsesToolTypeToolSearch {
			sawToolSearch = true
			require.NotNil(t, tool.Name, "tool_search variant must survive on Name")
			assert.Equal(t, "tool_search_tool_regex", *tool.Name)
		}
		if tool.Name != nil && *tool.Name == "get_weather" && tool.DeferLoading != nil {
			sawDeferred = *tool.DeferLoading
		}
	}
	assert.True(t, sawToolSearch, "tool_search_tool_* dropped by the invoke ingress: %+v", responsesReq.Params.Tools)
	assert.True(t, sawDeferred, "defer_loading dropped by the invoke ingress: %+v", responsesReq.Params.Tools)

	assert.True(t, responsesUsesAnthropicInvokePath(ctx, responsesReq),
		"a tool-search request on the invoke ingress must route to InvokeModel, not Converse")
}

// TestConvertAnthropicTools_DeferredToolSkipsCachePoint pins the one tool-level
// combination Anthropic rejects outright: "A tool with defer_loading: true can't
// also carry cache_control: the API returns a 400."
// (https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool)
// The invoke ingress converts cache_control into a positional cachePoint sibling
// (#5629), so it must not manufacture one for a deferred tool. A non-deferred
// neighbour in the same request still gets its breakpoint.
func TestConvertAnthropicTools_DeferredToolSkipsCachePoint(t *testing.T) {
	deferredCached := anthropicToolMap("deferred", map[string]interface{}{"type": "ephemeral"})
	deferredCached["defer_loading"] = true

	req := &BedrockInvokeRequest{
		Tools: []interface{}{
			deferredCached,
			anthropicToolMap("eager", map[string]interface{}{"type": "ephemeral"}),
		},
	}

	toolConfig := req.convertAnthropicTools()
	require.NotNil(t, toolConfig)

	// deferred tool, then eager tool, then the eager tool's cachePoint — three entries.
	require.Len(t, toolConfig.Tools, 3, "only the non-deferred tool may get a cachePoint: %+v", toolConfig.Tools)

	require.NotNil(t, toolConfig.Tools[0].ToolSpec)
	assert.Equal(t, "deferred", toolConfig.Tools[0].ToolSpec.Name)
	require.NotNil(t, toolConfig.Tools[0].ToolSpec.DeferLoading)
	assert.True(t, *toolConfig.Tools[0].ToolSpec.DeferLoading)
	assert.Nil(t, toolConfig.Tools[0].CachePoint)

	require.NotNil(t, toolConfig.Tools[1].ToolSpec)
	assert.Equal(t, "eager", toolConfig.Tools[1].ToolSpec.Name)
	assert.Nil(t, toolConfig.Tools[1].ToolSpec.DeferLoading)

	require.NotNil(t, toolConfig.Tools[2].CachePoint, "the non-deferred tool keeps its cache breakpoint")
	assert.Nil(t, toolConfig.Tools[2].ToolSpec)
}

// TestConvertAnthropicTools_UnknownToolSearchVariantNotRouted guards the boundary
// schemas.normalizeResponsesToolType already documents: an unrecognized tool_search
// sibling must reach unknown-tool handling rather than silently becoming a
// variant-less canonical tool_search. Without the variant gate the marker is created
// anyway, ToolSearchVariantName yields no name, and toolNeedsAnthropicInvokePath still
// matches the canonical "tool_search" prefix — so an unsupported tool would select the
// InvokeModel route and be forwarded to AWS as though Bifrost supported it.
func TestConvertAnthropicTools_UnknownToolSearchVariantNotRouted(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	req := &BedrockInvokeRequest{
		ModelID: "us.anthropic.claude-sonnet-4-6-v1:0",
		Messages: []BedrockMessage{{
			Role:    BedrockMessageRoleUser,
			Content: []BedrockContentBlock{{Text: schemas.Ptr("hi")}},
		}},
		Tools: []interface{}{
			map[string]interface{}{
				"type": "tool_search_tool_vector_20270101",
				"name": "tool_search_tool_vector",
			},
			anthropicToolMap("keep_me", nil),
		},
	}

	toolConfig := req.convertAnthropicTools()
	require.NotNil(t, toolConfig)
	for i, tool := range toolConfig.Tools {
		assert.Nil(t, tool.AnthropicToolSearch,
			"tool %d: an unrecognized tool_search variant must not become a tool-search marker", i)
	}
	require.Len(t, toolConfig.Tools, 1, "only the real tool survives: %+v", toolConfig.Tools)
	require.NotNil(t, toolConfig.Tools[0].ToolSpec)
	assert.Equal(t, "keep_me", toolConfig.Tools[0].ToolSpec.Name)

	// End to end: the unsupported variant must not select the InvokeModel route.
	responsesReq, err := req.ToBedrockConverseRequest().ToBifrostResponsesRequest(ctx)
	require.NoError(t, err)
	for _, tool := range responsesReq.Params.Tools {
		assert.NotEqual(t, schemas.ResponsesToolTypeToolSearch, tool.Type,
			"unrecognized variant leaked through as a canonical tool_search: %+v", responsesReq.Params.Tools)
	}
	assert.False(t, responsesUsesAnthropicInvokePath(ctx, responsesReq),
		"an unsupported tool_search variant must not route to InvokeModel")

	// The recognized variants still do route, so the gate is not over-broad.
	for _, known := range []string{"tool_search_tool_regex_20251119", "tool_search_tool_bm25"} {
		known := known
		t.Run(known, func(t *testing.T) {
			ok := &BedrockInvokeRequest{
				ModelID:  "us.anthropic.claude-sonnet-4-6-v1:0",
				Messages: req.Messages,
				Tools: []interface{}{
					map[string]interface{}{"type": known},
					anthropicToolMap("keep_me", nil),
				},
			}
			r, err := ok.ToBedrockConverseRequest().ToBifrostResponsesRequest(ctx)
			require.NoError(t, err)
			assert.True(t, responsesUsesAnthropicInvokePath(ctx, r), "%s must still route to InvokeModel", known)
		})
	}
}

// TestToBedrockInvokeMessagesResponse_ToolSearchCall covers the response direction
// of #7155: once tool search actually runs on the Bedrock-native invoke ingress, the
// server-side search must come back as the server_tool_use + tool_search_tool_result
// pair Anthropic documents, never as an invocable tool_use. "Never return a
// tool_result for its srvtoolu_... ID" — emitting tool_use makes the caller do
// exactly that, and the API rejects the next turn.
// (https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool)
func TestToBedrockInvokeMessagesResponse_ToolSearchCall(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	const (
		searchID  = "srvtoolu_01ABC"
		callID    = "toolu_01XYZ"
		found     = "get_weather"
		searchTag = "tool_search_tool_regex"
	)

	resp := &schemas.BifrostResponsesResponse{
		ID:    schemas.Ptr("msg_ts"),
		Model: "us.anthropic.claude-sonnet-4-6-v1:0",
		Output: []schemas.ResponsesMessage{
			{
				ID:     schemas.Ptr(searchID),
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeToolSearchCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:                  schemas.Ptr(searchID),
					Name:                    schemas.Ptr(searchTag),
					Arguments:               schemas.Ptr(`{"pattern":"weather"}`),
					ResponsesToolSearchCall: &schemas.ResponsesToolSearchCall{ToolReferences: []string{found}},
				},
			},
			{
				ID:   schemas.Ptr(callID),
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr(callID),
					Name:      schemas.Ptr(found),
					Arguments: schemas.Ptr(`{"city":"Paris"}`),
				},
			},
		},
	}

	out, err := ToBedrockInvokeMessagesResponse(ctx, resp)
	require.NoError(t, err)
	encoded, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)

	blocks := gjson.GetBytes(encoded, "content").Array()
	require.Len(t, blocks, 3, "expected server_tool_use + tool_search_tool_result + tool_use, got %s", string(encoded))

	assert.Equal(t, "server_tool_use", blocks[0].Get("type").String(), "body: %s", string(encoded))
	assert.Equal(t, searchID, blocks[0].Get("id").String())
	assert.Equal(t, searchTag, blocks[0].Get("name").String())
	// The query the model searched with must survive: Anthropic requires this block to
	// be echoed back unchanged, and an input rebuilt as {} silently rewrites it.
	assert.Equal(t, "weather", blocks[0].Get("input.pattern").String(),
		"server_tool_use.input lost the search query: %s", string(encoded))

	assert.Equal(t, "tool_search_tool_result", blocks[1].Get("type").String())
	assert.Equal(t, searchID, blocks[1].Get("tool_use_id").String())
	assert.Equal(t, "tool_search_tool_search_result", blocks[1].Get("content.type").String())
	refs := blocks[1].Get("content.tool_references").Array()
	require.Len(t, refs, 1)
	assert.Equal(t, "tool_reference", refs[0].Get("type").String())
	assert.Equal(t, found, refs[0].Get("tool_name").String())

	// The discovered tool's own call is a real client tool_use and still drives stop_reason.
	assert.Equal(t, "tool_use", blocks[2].Get("type").String())
	assert.Equal(t, callID, blocks[2].Get("id").String())
	assert.Equal(t, "tool_use", gjson.GetBytes(encoded, "stop_reason").String())

	// The server-side search must never be presented as an invocable tool.
	for _, b := range blocks {
		if b.Get("id").String() == searchID {
			assert.NotEqual(t, "tool_use", b.Get("type").String(),
				"the srvtoolu_ block must never be a client tool_use: %s", string(encoded))
		}
	}
}

// TestToBedrockInvokeMessagesStreamResponse_ToolSearchNotToolUse is the streaming
// twin of TestToBedrockInvokeMessagesResponse_ToolSearchCall. output_item.added for
// a tool_search_call must open a server_tool_use block, not a tool_use: a caller that
// sees tool_use executes the srvtoolu_ id and returns a tool_result for it, which
// Anthropic rejects on the next turn.
// (https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool)
func TestToBedrockInvokeMessagesStreamResponse_ToolSearchNotToolUse(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	const searchID = "srvtoolu_01ABC"

	added := func(itemType schemas.ResponsesMessageType, id, name string) *schemas.BifrostResponsesStreamResponse {
		return &schemas.BifrostResponsesStreamResponse{
			Type:         schemas.ResponsesStreamResponseTypeOutputItemAdded,
			ContentIndex: schemas.Ptr(0),
			Item: &schemas.ResponsesMessage{
				ID:   schemas.Ptr(id),
				Type: schemas.Ptr(itemType),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr(id),
					Name:   schemas.Ptr(name),
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{ResolvedModelUsed: "us.anthropic.claude-sonnet-4-6-v1:0"},
		}
	}

	t.Run("tool_search_call opens server_tool_use", func(t *testing.T) {
		_, event, err := ToBedrockInvokeMessagesStreamResponse(ctx, added(schemas.ResponsesMessageTypeToolSearchCall, searchID, "tool_search_tool_regex"))
		require.NoError(t, err)
		bedrockEvent, ok := event.(*BedrockStreamEvent)
		require.True(t, ok, "expected a BedrockStreamEvent, got %T", event)
		require.Len(t, bedrockEvent.InvokeModelRawChunks, 1)
		raw := bedrockEvent.InvokeModelRawChunks[0]

		assert.Equal(t, "server_tool_use", gjson.GetBytes(raw, "content_block.type").String(),
			"the srvtoolu_ block must never open as a client tool_use: %s", string(raw))
		assert.Equal(t, searchID, gjson.GetBytes(raw, "content_block.id").String())
	})

	t.Run("ordinary function_call still opens tool_use", func(t *testing.T) {
		_, event, err := ToBedrockInvokeMessagesStreamResponse(ctx, added(schemas.ResponsesMessageTypeFunctionCall, "toolu_01XYZ", "get_weather"))
		require.NoError(t, err)
		bedrockEvent, ok := event.(*BedrockStreamEvent)
		require.True(t, ok)
		require.Len(t, bedrockEvent.InvokeModelRawChunks, 1)
		assert.Equal(t, "tool_use", gjson.GetBytes(bedrockEvent.InvokeModelRawChunks[0], "content_block.type").String())
	})
	t.Run("tool_search_call stop closes the block its start opened", func(t *testing.T) {
		streamCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		_, startEvent, err := ToBedrockInvokeMessagesStreamResponse(streamCtx, added(schemas.ResponsesMessageTypeToolSearchCall, searchID, "tool_search_tool_regex"))
		require.NoError(t, err)
		startBedrock, ok := startEvent.(*BedrockStreamEvent)
		require.True(t, ok, "expected a BedrockStreamEvent, got %T", startEvent)
		require.Len(t, startBedrock.InvokeModelRawChunks, 1)
		startRaw := startBedrock.InvokeModelRawChunks[0]
		require.Equal(t, "content_block_start", gjson.GetBytes(startRaw, "type").String())

		// The neutral stream collapses server_tool_use + tool_search_tool_result into one
		// item, so output_item.done arrives carrying the result block's content index.
		done := &schemas.BifrostResponsesStreamResponse{
			Type:         schemas.ResponsesStreamResponseTypeOutputItemDone,
			ContentIndex: schemas.Ptr(1),
			Item: &schemas.ResponsesMessage{
				ID:   schemas.Ptr(searchID),
				Type: schemas.Ptr(schemas.ResponsesMessageTypeToolSearchCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr(searchID),
					Name:   schemas.Ptr("tool_search_tool_regex"),
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{ResolvedModelUsed: "us.anthropic.claude-sonnet-4-6-v1:0"},
		}
		_, stopEvent, err := ToBedrockInvokeMessagesStreamResponse(streamCtx, done)
		require.NoError(t, err)
		stopBedrock, ok := stopEvent.(*BedrockStreamEvent)
		require.True(t, ok, "expected a BedrockStreamEvent, got %T", stopEvent)
		require.Len(t, stopBedrock.InvokeModelRawChunks, 1)
		stopRaw := stopBedrock.InvokeModelRawChunks[0]

		require.Equal(t, "content_block_stop", gjson.GetBytes(stopRaw, "type").String())
		assert.Equal(t, gjson.GetBytes(startRaw, "index").Int(), gjson.GetBytes(stopRaw, "index").Int(),
			"content_block_stop must close the block content_block_start opened: start=%s stop=%s", string(startRaw), string(stopRaw))
	})

	t.Run("an item with no recorded start keeps its own content index", func(t *testing.T) {
		streamCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		done := &schemas.BifrostResponsesStreamResponse{
			Type:         schemas.ResponsesStreamResponseTypeOutputItemDone,
			ContentIndex: schemas.Ptr(2),
			Item:         &schemas.ResponsesMessage{ID: schemas.Ptr("msg_text_block")},
		}
		_, event, err := ToBedrockInvokeMessagesStreamResponse(streamCtx, done)
		require.NoError(t, err)
		bedrockEvent, ok := event.(*BedrockStreamEvent)
		require.True(t, ok)
		require.Len(t, bedrockEvent.InvokeModelRawChunks, 1)
		assert.Equal(t, int64(2), gjson.GetBytes(bedrockEvent.InvokeModelRawChunks[0], "index").Int())
	})
}

// TestToBedrockConverseRequest_InvokeToolSearchReplay covers turn 2 of a tool-search
// conversation on the Bedrock-native invoke ingress. Anthropic requires the client to
// echo the assistant's server_tool_use and tool_search_tool_result back unchanged, but
// BedrockContentBlock.UnmarshalJSON decoded only image/tool_use/tool_result/thinking,
// so both blocks fell through to an empty struct and vanished — leaving the model a
// turn in which it called a tool it never discovered.
func TestToBedrockConverseRequest_InvokeToolSearchReplay(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	const (
		searchID = "srvtoolu_01ABC"
		callID   = "toolu_01XYZ"
		found    = "get_weather"
	)

	raw := `{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens": 512,
		"tools": [
			{"type": "tool_search_tool_regex_20251119", "name": "tool_search_tool_regex"},
			{"name": "` + found + `", "description": "weather", "input_schema": {"type":"object","properties":{}}, "defer_loading": true}
		],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "weather in Paris?"}]},
			{"role": "assistant", "content": [
				{"type": "server_tool_use", "id": "` + searchID + `", "name": "tool_search_tool_regex", "input": {"pattern": "weather"}},
				{"type": "tool_search_tool_result", "tool_use_id": "` + searchID + `",
				 "content": {"type": "tool_search_tool_search_result",
				             "tool_references": [{"type": "tool_reference", "tool_name": "` + found + `"}]}},
				{"type": "tool_use", "id": "` + callID + `", "name": "` + found + `", "input": {"city": "Paris"}}
			]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "` + callID + `", "content": "18C"}]}
		]
	}`

	var req BedrockInvokeRequest
	require.NoError(t, sonic.Unmarshal([]byte(raw), &req))
	req.ModelID = "us.anthropic.claude-sonnet-4-6-v1:0"

	converseReq := req.ToBedrockConverseRequest()
	bifrostReq, err := converseReq.ToBifrostResponsesRequest(ctx)
	require.NoError(t, err)

	var search *schemas.ResponsesMessage
	var sawDiscoveredCall bool
	for i := range bifrostReq.Input {
		m := &bifrostReq.Input[i]
		if m.Type == nil {
			continue
		}
		switch *m.Type {
		case schemas.ResponsesMessageTypeToolSearchCall:
			search = m
		case schemas.ResponsesMessageTypeFunctionCall:
			if m.ResponsesToolMessage != nil && m.ResponsesToolMessage.CallID != nil {
				switch *m.ResponsesToolMessage.CallID {
				case callID:
					sawDiscoveredCall = true
				case searchID:
					t.Errorf("the srvtoolu_ block replayed as a client function_call")
				}
			}
		}
	}

	require.NotNil(t, search, "the replayed tool_search pair was dropped: %+v", bifrostReq.Input)
	require.NotNil(t, search.ResponsesToolMessage)
	require.NotNil(t, search.ResponsesToolMessage.ResponsesToolSearchCall)
	assert.Equal(t, []string{found}, search.ResponsesToolMessage.ResponsesToolSearchCall.ToolReferences,
		"the discovered tool references must survive replay")
	require.NotNil(t, search.ResponsesToolMessage.Name)
	assert.Equal(t, "tool_search_tool_regex", *search.ResponsesToolMessage.Name)
	// The query the model searched with must survive ingress too: Anthropic requires
	// this block to be echoed back unchanged, so a replay that forgets the pattern
	// rewrites it on the next turn.
	require.NotNil(t, search.ResponsesToolMessage.Arguments,
		"server_tool_use.input was dropped at the invoke ingress")
	assert.JSONEq(t, `{"pattern":"weather"}`, *search.ResponsesToolMessage.Arguments)
	assert.True(t, sawDiscoveredCall, "the tool_use calling the discovered tool must still replay")

	// The turn must still route to InvokeModel — tool search never runs on Converse.
	assert.True(t, responsesUsesAnthropicInvokePath(ctx, bifrostReq))

	// Close the loop: the block this ingress decoded must come back out of the
	// InvokeModel serializer with the same input, which is what "echo the assistant's
	// content back unchanged" actually requires end to end.
	out, err := ToBedrockInvokeMessagesResponse(ctx, &schemas.BifrostResponsesResponse{
		ID:     schemas.Ptr("msg_replay"),
		Model:  req.ModelID,
		Output: []schemas.ResponsesMessage{*search},
	})
	require.NoError(t, err)
	encoded, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	roundTripped := gjson.GetBytes(encoded, "content").Array()
	require.NotEmpty(t, roundTripped)
	assert.Equal(t, "server_tool_use", roundTripped[0].Get("type").String())
	assert.Equal(t, "weather", roundTripped[0].Get("input.pattern").String(),
		"the replayed block lost its search query on the way back out: %s", string(encoded))
}
