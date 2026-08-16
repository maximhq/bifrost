package bedrock

import (
	"context"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// bedrockReasoningBlocksForChatMessage runs the real chat-completions converter
// and returns the reasoning blocks it produced.
func bedrockReasoningBlocksForChatMessage(t *testing.T, msg schemas.ChatMessage) []BedrockContentBlock {
	t.Helper()
	converted, err := convertMessage(context.Background(), msg)
	require.NoError(t, err)
	var blocks []BedrockContentBlock
	for _, block := range converted.Content {
		if block.ReasoningContent != nil {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

// invokeThinkingBlocksFor runs the real invoke converter and returns the
// thinking blocks it produced.
func invokeThinkingBlocksFor(t *testing.T, item *schemas.ResponsesMessage) []BedrockInvokeMessagesContentBlock {
	t.Helper()
	resp := &schemas.BifrostResponsesResponse{Output: []schemas.ResponsesMessage{*item}}
	converted := toBedrockInvokeAnthropicResponse(resp, "global.anthropic.claude-sonnet-5")
	require.NotNil(t, converted)
	return converted.Content
}

// An audit for the defect fixed in convertBifrostReasoningToBedrockReasoning
// found the same shape on Bedrock's OTHER two request paths. Same struct, same
// omitempty, same upstream rejection -- only the entry point differs, so fixing
// the Responses converter alone left two thirds of the surface broken:
//
//	Responses      convertBifrostReasoningToBedrockReasoning   (fixed)
//	ChatCompletion convertMessage -> utils.go                  (this file)
//	Invoke         toBedrockInvokeAnthropicResponse            (this file)
//
// Bedrock requires the thinking text on every reasoning block. Because the
// fields are omitempty, omitting it does not send an explicit null -- the key
// vanishes and the request is rejected before a token is read.

// TestChatReasoningDetailsAlwaysSerialiseText covers the chat-completions path.
//
// The trigger is Bifrost's own streaming ingress: on a signature delta it emits
// a ChatReasoningDetails with Signature set and Text nil (see chat.go's
// reasoningContentDelta handling). A client replaying that assistant turn sends
// it straight back, and utils.go copies detail.Text through unguarded -- so a
// nil Text becomes a reasoningText block with no text key at all.
func TestChatReasoningDetailsAlwaysSerialiseText(t *testing.T) {
	signature := "EqQBCgIYAhIM...fixture"

	for _, tc := range []struct {
		name   string
		detail schemas.ChatReasoningDetails
	}{
		{
			// Exactly what the streaming path emits for a signature-only delta.
			name: "signature with nil text",
			detail: schemas.ChatReasoningDetails{
				Index: 0, Type: schemas.BifrostReasoningDetailsTypeText, Signature: &signature,
			},
		},
		{
			name: "signature with empty text",
			detail: schemas.ChatReasoningDetails{
				Index: 0, Type: schemas.BifrostReasoningDetailsTypeText,
				Text: schemas.Ptr(""), Signature: &signature,
			},
		},
		{
			name: "text and signature",
			detail: schemas.ChatReasoningDetails{
				Index: 0, Type: schemas.BifrostReasoningDetailsTypeText,
				Text: schemas.Ptr("step by step"), Signature: &signature,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := schemas.ChatMessage{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ReasoningDetails: []schemas.ChatReasoningDetails{tc.detail},
				},
			}

			blocks := bedrockReasoningBlocksForChatMessage(t, msg)
			require.NotEmpty(t, blocks, "reasoning detail produced no Bedrock block at all")

			for i, block := range blocks {
				if block.ReasoningContent == nil {
					continue
				}
				require.NotNil(t, block.ReasoningContent.ReasoningText, "block %d", i)
				require.NotNil(t, block.ReasoningContent.ReasoningText.Text,
					"block %d: nil Text -- Bedrock rejects this with \"reasoningContent.reasoningText.text ... Member must not be null\"", i)

				raw, err := sonic.Marshal(block)
				require.NoError(t, err)
				require.True(t, gjson.GetBytes(raw, "reasoningContent.reasoningText.text").Exists(),
					"text key missing from the serialised block: %s", raw)
			}
		})
	}
}

// TestInvokeThinkingAlwaysSerialised covers the invoke path.
//
// BedrockInvokeMessagesContentBlock.Thinking is `string json:"thinking,omitempty"`,
// so an empty string is indistinguishable from an absent one on the wire. The
// converter deliberately allows an empty text with a real signature in order to
// preserve the replay token -- which produces {"type":"thinking","signature":...}
// with no thinking key.
//
// Bifrost's own re-ingest proves the shape is unusable: invoke.go's decoder
// returns nil when Thinking is absent, silently dropping the whole block.
func TestInvokeThinkingAlwaysSerialised(t *testing.T) {
	signature := "EqQBCgIYAhIM...fixture"

	// Both fields set, which is how a reasoning item actually arrives: the
	// converter gates the whole branch on ResponsesReasoning != nil and then
	// reads the content blocks.
	block := schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesOutputMessageContentTypeReasoning,
		Text: schemas.Ptr(""),
	}
	block.Signature = &signature

	blocks := invokeThinkingBlocksFor(t, &schemas.ResponsesMessage{
		Type:               schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
		Content:            &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{block}},
		ResponsesReasoning: &schemas.ResponsesReasoning{Summary: []schemas.ResponsesReasoningSummary{}},
	})

	require.NotEmpty(t, blocks, "signature-bearing reasoning produced no thinking block")
	inspected := 0
	for i, block := range blocks {
		if block.Type != "thinking" {
			continue
		}
		inspected++
		raw, err := sonic.Marshal(block)
		require.NoError(t, err)
		require.True(t, gjson.GetBytes(raw, "thinking").Exists(),
			"block %d: thinking key missing, so re-ingest drops the block entirely: %s", i, raw)
	}
	// NotEmpty above only proves SOME block came back. If the converter stops
	// emitting thinking blocks but still emits something else, every iteration
	// takes the continue and the assertion never runs - so the test would report
	// success for precisely the regression it exists to catch.
	require.NotZero(t, inspected, "no thinking block was inspected, so nothing above was actually asserted")
}
