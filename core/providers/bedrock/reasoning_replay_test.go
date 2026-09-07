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
			blocks := convertBifrostReasoningToBedrockReasoning(tc.msg, schemas.BedrockReasoningShapeText)

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

	blocks := convertBifrostReasoningToBedrockReasoning(message, schemas.BedrockReasoningShapeText)
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
	}, schemas.BedrockReasoningShapeText)
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

	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), "anthropic.claude-sonnet-4-5-20250929-v1:0", input, false)
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

// ---- redactedContent: the OpenAI/xAI reasoning shape on Converse ----
//
// Verified against bedrock-runtime us-east-1. gpt-5.6-* and grok-4.6 return
// reasoningContent.redactedContent, one opaque blob delivered whole in a single
// contentBlockDelta. They reject reasoningContent.reasoningText in EVERY form --
// empty text, prose text, with or without a signature -- with an opaque
// "The system encountered an unexpected error during processing." 500. Anthropic
// (through 4.8 adaptive thinking) and DeepSeek R1 are the other way round, and
// answer a redactedContent replay with a 400. Replaying the matching shape
// verbatim is the only thing either family accepts.

func TestConverseReasoningShapeFamilyFallback(t *testing.T) {
	tests := []struct {
		model string
		want  schemas.BedrockReasoningShape
	}{
		{"openai.gpt-5.6-luna", schemas.BedrockReasoningShapeRedacted},
		{"us.openai.gpt-5.6-sol", schemas.BedrockReasoningShapeRedacted},
		{"xai.grok-4.6", schemas.BedrockReasoningShapeRedacted},
		{"us.anthropic.claude-sonnet-4-5-20250929-v1:0", schemas.BedrockReasoningShapeText},
		{"us.anthropic.claude-opus-4-8", schemas.BedrockReasoningShapeText},
		{"us.deepseek.r1-v1:0", schemas.BedrockReasoningShapeText},
		{"us.amazon.nova-premier-v1:0", schemas.BedrockReasoningShapeText},
	}
	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			require.Equal(t, tc.want, converseReasoningShape(tc.model))
		})
	}
}

func TestConverseReasoningShapeDatasheetWinsOverFamily(t *testing.T) {
	const model = "openai.gpt-5.6-luna"

	t.Run("row overrides the family fallback", func(t *testing.T) {
		installBedrockCapabilityRecord(t, model, &schemas.ModelCapabilities{
			BedrockReasoningShape: schemas.BedrockReasoningShapeText,
		})
		require.Equal(t, schemas.BedrockReasoningShapeText, converseReasoningShape(model))
	})

	t.Run("unrecognised value falls back to the family", func(t *testing.T) {
		installBedrockCapabilityRecord(t, model, &schemas.ModelCapabilities{
			BedrockReasoningShape: schemas.BedrockReasoningShape("something_new"),
		})
		require.Equal(t, schemas.BedrockReasoningShapeRedacted, converseReasoningShape(model))
	})

	t.Run("empty row falls back to the family", func(t *testing.T) {
		installBedrockCapabilityRecord(t, model, &schemas.ModelCapabilities{})
		require.Equal(t, schemas.BedrockReasoningShapeRedacted, converseReasoningShape(model))
	})
}

// A Grok alias resolves through capModel before reaching the converters, so the
// shape must follow the canonical name and not the opaque id the client sent.
func TestConverseReasoningShapeFollowsCanonicalModel(t *testing.T) {
	require.Equal(t, schemas.BedrockReasoningShapeRedacted, converseReasoningShape("grok-4.6"),
		"canonical Grok must pick the redacted shape")
	require.Equal(t, schemas.BedrockReasoningShapeText, converseReasoningShape("3dnkdwuaalc7"),
		"an unresolved opaque id carries no family and must not be guessed as redacted")
}

func TestConvertBifrostReasoningToBedrockReasoningRedactedShape(t *testing.T) {
	blob := "cnNuXzVaVnJpZjRKMGJYSXFtV2RsZWRqN1FJRmVGZWdz"

	t.Run("blob round-trips as redactedContent", func(t *testing.T) {
		blocks := convertBifrostReasoningToBedrockReasoning(&schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary:          []schemas.ResponsesReasoningSummary{},
				EncryptedContent: &blob,
			},
		}, schemas.BedrockReasoningShapeRedacted)

		require.Len(t, blocks, 1)
		require.NotNil(t, blocks[0].ReasoningContent)
		require.NotNil(t, blocks[0].ReasoningContent.RedactedContent)
		require.Equal(t, blob, *blocks[0].ReasoningContent.RedactedContent)
		require.Nil(t, blocks[0].ReasoningContent.ReasoningText)

		// Wire level: the struct check above passes even if reasoningText leaks
		// back in as an empty object, and that is what earns the 500.
		raw, err := sonic.Marshal(blocks[0])
		require.NoError(t, err)
		require.Equal(t, blob, gjson.GetBytes(raw, "reasoningContent.redactedContent").String())
		require.False(t, gjson.GetBytes(raw, "reasoningContent.reasoningText").Exists(),
			"reasoningText must never accompany a redacted-shape block")
	})

	t.Run("reasoning text is dropped rather than sent as reasoningText", func(t *testing.T) {
		// Exactly the payload that reproduced the 500: an empty, unsigned thinking
		// block replayed by a client that received a dropped redacted blob.
		blocks := convertBifrostReasoningToBedrockReasoning(&schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{reasoningTextBlock("", nil)},
			},
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary: []schemas.ResponsesReasoningSummary{},
			},
		}, schemas.BedrockReasoningShapeRedacted)

		require.Empty(t, blocks, "an unreplayable block must be dropped, not reshaped")
	})

	t.Run("summary text is dropped too", func(t *testing.T) {
		blocks := convertBifrostReasoningToBedrockReasoning(&schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary: []schemas.ResponsesReasoningSummary{{Text: "step by step"}},
			},
		}, schemas.BedrockReasoningShapeRedacted)

		require.Empty(t, blocks)
	})
}

// The full loop that produced the bug: Bedrock returns a redacted blob, Bifrost
// converts it to canonical form, and a client replays that turn.
func TestRedactedReasoningSurvivesTheReplayLoop(t *testing.T) {
	blob := "cnNuX2pRenlYWklGSkxscEt5ZmxZa0NmdndJRmVMK2Jp"
	const model = "openai.gpt-5.6-luna"

	resp := &BedrockConverseResponse{
		Output: &BedrockConverseOutput{
			Message: &BedrockMessage{
				Role: BedrockMessageRoleAssistant,
				Content: []BedrockContentBlock{
					{ReasoningContent: &BedrockReasoningContent{RedactedContent: &blob}},
					{Text: schemas.Ptr("Hello!")},
				},
			},
		},
	}

	bifrostResp, err := resp.ToBifrostChatResponse(context.Background(), model)
	require.NoError(t, err)
	require.NotEmpty(t, bifrostResp.Choices)

	assistant := bifrostResp.Choices[0].ChatNonStreamResponseChoice.Message.ChatAssistantMessage
	require.NotNil(t, assistant, "the blob must reach the client, not be dropped")
	require.Len(t, assistant.ReasoningDetails, 1)
	require.Equal(t, schemas.BifrostReasoningDetailsTypeEncrypted, assistant.ReasoningDetails[0].Type)
	require.NotNil(t, assistant.ReasoningDetails[0].Data)
	require.Equal(t, blob, *assistant.ReasoningDetails[0].Data)

	// Replay that assistant turn back to Bedrock.
	replayed, err := convertMessage(context.Background(), model,
		*bifrostResp.Choices[0].ChatNonStreamResponseChoice.Message)
	require.NoError(t, err)

	raw, err := sonic.Marshal(replayed)
	require.NoError(t, err)
	require.Equal(t, blob, gjson.GetBytes(raw, "content.0.reasoningContent.redactedContent").String())
	require.False(t, gjson.GetBytes(raw, "content.0.reasoningContent.reasoningText").Exists())
}
