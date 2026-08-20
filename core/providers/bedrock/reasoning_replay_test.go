package bedrock

import (
	"context"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Bedrock Converse requires reasoningContent.reasoningText.text on every reasoning
// block. Replaying reasoning history to Anthropic-on-Bedrock over the Responses
// path returns, once per prior assistant turn:
//
//	N validation errors detected: Value at 'messages.2.member.content.1.member.
//	reasoningContent.reasoningText.text' failed to satisfy constraint:
//	Member must not be null
//
// The tests below pin the invariant that prevents it. They are deliberately
// written against the serialised wire form as well as the struct, because
// BedrockReasoningContentText.Text is `*string json:"text,omitempty"` -- a nil
// Text does not serialise as `"text":null`, it vanishes from the request
// entirely, which is what made this defect invisible to every struct-level check.

// reasoningTextInvariant asserts the one rule that matters: no block leaves this
// converter without a Text. Run over every case's output, not just the ones the
// case author thought to check.
func reasoningTextInvariant(t *testing.T, blocks []BedrockContentBlock) {
	t.Helper()
	for i, block := range blocks {
		require.NotNil(t, block.ReasoningContent, "block %d: nil ReasoningContent", i)
		require.NotNil(t, block.ReasoningContent.ReasoningText, "block %d: nil ReasoningText", i)
		require.NotNil(t, block.ReasoningContent.ReasoningText.Text,
			"block %d: nil Text -- Bedrock rejects this with \"reasoningContent.reasoningText.text ... Member must not be null\"", i)
	}
}

func reasoningTextBlock(text string, signature *string) schemas.ResponsesMessageContentBlock {
	block := schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesOutputMessageContentTypeReasoning,
		Text: &text,
	}
	block.Signature = signature
	return block
}

func TestConvertBifrostReasoningToBedrockReasoning(t *testing.T) {
	signature := "EqQBCgIYAhIM...fixture"

	tests := []struct {
		name           string
		msg            *schemas.ResponsesMessage
		wantBlocks     int
		wantTexts      []string
		wantSignatures []*string
	}{
		{
			// The defect. This is exactly what the STREAMING ingress path emits
			// (responses.go, output_item.added: Summary is a non-nil empty slice and
			// EncryptedContent carries the Bedrock reasoning signature), so a client
			// replaying what Bifrost itself streamed lands here.
			name: "streaming shape: empty summary plus encrypted content",
			msg: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{},
					EncryptedContent: &signature,
				},
			},
			wantBlocks:     1,
			wantTexts:      []string{""},
			wantSignatures: []*string{&signature},
		},
		{
			// Non-nil but empty ContentBlocks must not shadow the encrypted payload.
			// The `else if` on Content made this drop the item entirely; the sibling
			// invoke path guards it with emittedFromContentBlocks (invoke.go).
			name: "empty content blocks slice does not shadow encrypted content",
			msg: &schemas.ResponsesMessage{
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{}},
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{},
					EncryptedContent: &signature,
				},
			},
			wantBlocks:     1,
			wantTexts:      []string{""},
			wantSignatures: []*string{&signature},
		},
		{
			// Content blocks present but none of them reasoning: the summary is the
			// only reasoning data available and must not be lost.
			name: "content blocks without a reasoning block falls back to summary",
			msg: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{Type: schemas.ResponsesOutputMessageContentTypeText, Text: schemas.Ptr("not reasoning")},
				}},
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary: []schemas.ResponsesReasoningSummary{{Text: "thinking about it"}},
				},
			},
			wantBlocks:     1,
			wantTexts:      []string{"thinking about it"},
			wantSignatures: []*string{nil},
		},
		{
			// Branch A baseline -- the NON-streaming ingress shape. Bedrock returns
			// reasoning here as content blocks with a per-block signature, which is
			// why replaying a non-streamed turn has always worked.
			name: "content block with text and signature",
			msg: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
					reasoningTextBlock("step by step", &signature),
				}},
			},
			wantBlocks:     1,
			wantTexts:      []string{"step by step"},
			wantSignatures: []*string{&signature},
		},
		{
			name: "content block with empty text keeps its signature",
			msg: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
					reasoningTextBlock("", &signature),
				}},
			},
			wantBlocks:     1,
			wantTexts:      []string{""},
			wantSignatures: []*string{&signature},
		},
		{
			name: "summary only",
			msg: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary: []schemas.ResponsesReasoningSummary{{Text: "first"}, {Text: "second"}},
				},
			},
			wantBlocks:     2,
			wantTexts:      []string{"first", "second"},
			wantSignatures: []*string{nil, nil},
		},
		{
			// An empty signature is not a signature. Echoing signature:"" back 400s
			// with "This model doesn't support the ... signature field" (see
			// reasoningSignatureForBedrock), so emitting nothing is correct.
			name: "empty encrypted content string emits nothing",
			msg: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{},
					EncryptedContent: schemas.Ptr(""),
				},
			},
			wantBlocks: 0,
		},
		{
			name:       "no reasoning and no content",
			msg:        &schemas.ResponsesMessage{Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning)},
			wantBlocks: 0,
		},
		{
			name: "multiple content blocks preserve order",
			msg: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
					reasoningTextBlock("one", nil),
					reasoningTextBlock("two", &signature),
				}},
			},
			wantBlocks:     2,
			wantTexts:      []string{"one", "two"},
			wantSignatures: []*string{nil, &signature},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blocks := convertBifrostReasoningToBedrockReasoning(tc.msg)

			require.Len(t, blocks, tc.wantBlocks)
			reasoningTextInvariant(t, blocks)

			for i := range tc.wantTexts {
				require.Equal(t, tc.wantTexts[i], *blocks[i].ReasoningContent.ReasoningText.Text,
					"block %d text", i)
			}
			for i := range tc.wantSignatures {
				got := blocks[i].ReasoningContent.ReasoningText.Signature
				if tc.wantSignatures[i] == nil {
					require.Nil(t, got, "block %d expected no signature", i)
					continue
				}
				require.NotNil(t, got, "block %d expected a signature", i)
				require.Equal(t, *tc.wantSignatures[i], *got, "block %d signature", i)
			}
		})
	}
}

// TestConvertBifrostReasoningToBedrockReasoningEncryptedContent keeps the original
// test's intent -- encrypted replay data must survive translation into the Bedrock
// signature field -- and corrects the shape it pinned.
//
// It previously asserted Text was nil. That is the state Bedrock rejects: `text` is
// omitempty, so a nil pointer means the key is absent from the serialised request,
// and Converse answers "reasoningContent.reasoningText.text ... Member must not be
// null". Preserving the signature was right; dropping the text was not.
func TestConvertBifrostReasoningToBedrockReasoningEncryptedContent(t *testing.T) {
	encryptedContent := "EqQBCgIYAhIM...fixture"
	message := &schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
		ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary:          []schemas.ResponsesReasoningSummary{},
			EncryptedContent: &encryptedContent,
		},
	}

	blocks := convertBifrostReasoningToBedrockReasoning(message)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].ReasoningContent)
	require.NotNil(t, blocks[0].ReasoningContent.ReasoningText)
	require.NotNil(t, blocks[0].ReasoningContent.ReasoningText.Text)
	require.Equal(t, encryptedContent, *blocks[0].ReasoningContent.ReasoningText.Signature)
}

// TestConvertBifrostReasoningToBedrockReasoningTextAlwaysSerialized is the
// assertion that maps one-to-one onto the upstream error message.
//
// A struct-level `Text != nil` check is not sufficient evidence here: omitempty is
// the mechanism that made this defect invisible. Only marshalling proves the key
// reaches the wire.
func TestConvertBifrostReasoningToBedrockReasoningTextAlwaysSerialized(t *testing.T) {
	signature := "EqQBCgIYAhIM...fixture"
	blocks := convertBifrostReasoningToBedrockReasoning(&schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
		ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary:          []schemas.ResponsesReasoningSummary{},
			EncryptedContent: &signature,
		},
	})
	require.Len(t, blocks, 1)

	raw, err := sonic.Marshal(blocks[0])
	require.NoError(t, err)

	text := gjson.GetBytes(raw, "reasoningContent.reasoningText.text")
	require.True(t, text.Exists(),
		"reasoningContent.reasoningText.text missing from the serialised block; Bedrock rejects this. got: %s", raw)
	require.Equal(t, gjson.String, text.Type, "text must serialise as a string, got: %s", raw)
	require.True(t, gjson.GetBytes(raw, "reasoningContent.reasoningText.signature").Exists(),
		"signature must survive alongside the text, got: %s", raw)
}

// TestStreamingReasoningReplaySerialisesForBedrock drives the full request
// conversion, not just the reasoning helper, so it fails the same way a live
// request does.
//
// The input is the assistant turn a STREAMING client replays: Bifrost's streaming
// ingress emits a reasoning item with an empty summary and the signature in
// encrypted_content, and a faithful client sends that straight back on the next
// turn. Every prior assistant turn contributes one block, which is why the live
// error count scales 1:1 with conversation depth -- so this asserts across three
// replayed turns rather than one.
func TestStreamingReasoningReplaySerialisesForBedrock(t *testing.T) {
	signature := "EqQBCgIYAhIM...fixture"

	var input []schemas.ResponsesMessage
	input = append(input, schemas.ResponsesMessage{
		Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Start.")},
	})
	for i := 0; i < 3; i++ {
		input = append(input,
			schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{},
					EncryptedContent: &signature,
				},
			},
			schemas.ResponsesMessage{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Answer.")},
			},
			schemas.ResponsesMessage{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Continue.")},
			},
		)
	}

	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), input, false)
	require.NoError(t, err)

	raw, err := sonic.Marshal(messages)
	require.NoError(t, err)

	reasoningBlocks := gjson.GetBytes(raw, `#.content.#(reasoningContent)#`)
	require.True(t, reasoningBlocks.Exists(), "no reasoning survived the conversion: %s", raw)

	var seen int
	gjson.GetBytes(raw, "#.content").ForEach(func(_, content gjson.Result) bool {
		content.ForEach(func(_, block gjson.Result) bool {
			reasoning := block.Get("reasoningContent.reasoningText")
			if !reasoning.Exists() {
				return true
			}
			seen++
			require.True(t, reasoning.Get("text").Exists(),
				"replayed reasoning block %d serialised without a text key -- this is the live 400: %s", seen, reasoning.Raw)
			return true
		})
		return true
	})
	require.Equal(t, 3, seen, "expected one reasoning block per replayed assistant turn, got %d: %s", seen, raw)
}
