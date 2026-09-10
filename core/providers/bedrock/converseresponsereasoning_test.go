package bedrock

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// The Converse-shaped ingress routes (/bedrock converse and the framework drop-ins
// that reuse it) render a Bifrost response back through the request-direction
// message converter. That converter picks a reasoning shape per model family so
// that Bedrock never receives a block it did not sign, which is right for a
// replay and wrong for a response: a native xai Grok reasoning item carries a
// summary and no encrypted content, and the redacted shape silently dropped it
// (harness 47.5.G, 48.1.B, 48.2.B, 48.3.B). The response direction must render
// whatever the upstream exposed.
func TestConverseResponseRendersUpstreamReasoning(t *testing.T) {
	answer := "44"

	t.Run("summary without encrypted content becomes reasoningText", func(t *testing.T) {
		resp, err := ToBedrockConverseResponse(&schemas.BifrostResponsesResponse{
			Model: "xai/grok-4-0709",
			Output: []schemas.ResponsesMessage{
				{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					ResponsesReasoning: &schemas.ResponsesReasoning{
						Summary: []schemas.ResponsesReasoningSummary{{Type: "summary_text", Text: "simulate night by night"}},
					},
				},
				{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{Type: schemas.ResponsesOutputMessageContentTypeText, Text: &answer},
						},
					},
				},
			},
		})
		require.NoError(t, err)
		require.NotNil(t, resp.Output)
		require.NotNil(t, resp.Output.Message)
		content := resp.Output.Message.Content
		require.Len(t, content, 2, "reasoning block must precede the text block, got %+v", content)
		require.NotNil(t, content[0].ReasoningContent, "first block must be the reasoning block")
		require.NotNil(t, content[0].ReasoningContent.ReasoningText, "an exposed summary renders as reasoningText")
		require.NotNil(t, content[0].ReasoningContent.ReasoningText.Text)
		require.Equal(t, "simulate night by night", *content[0].ReasoningContent.ReasoningText.Text)
		require.Nil(t, content[0].ReasoningContent.ReasoningText.Signature, "no signature exists to echo")
		require.Nil(t, content[0].ReasoningContent.RedactedContent)
		require.NotNil(t, content[1].Text)
		require.Equal(t, answer, *content[1].Text)
	})

	t.Run("encrypted content without text stays redactedContent", func(t *testing.T) {
		blob := "cnNuXzVaVnJpZjRKMGJYSXFtV2RsZWRqN1FJRmVGZWdz"
		resp, err := ToBedrockConverseResponse(&schemas.BifrostResponsesResponse{
			Model: "us.xai.grok-4.6",
			Output: []schemas.ResponsesMessage{
				{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					ResponsesReasoning: &schemas.ResponsesReasoning{
						Summary:          []schemas.ResponsesReasoningSummary{},
						EncryptedContent: &blob,
					},
				},
				{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{Type: schemas.ResponsesOutputMessageContentTypeText, Text: &answer},
						},
					},
				},
			},
		})
		require.NoError(t, err)
		content := resp.Output.Message.Content
		require.Len(t, content, 2)
		require.NotNil(t, content[0].ReasoningContent)
		require.NotNil(t, content[0].ReasoningContent.RedactedContent, "an opaque block must stay redacted")
		require.Equal(t, blob, *content[0].ReasoningContent.RedactedContent)
		require.Nil(t, content[0].ReasoningContent.ReasoningText, "reasoningText must never accompany a redacted block")
	})

	t.Run("request direction still drops unsigned Grok text", func(t *testing.T) {
		blocks := convertBifrostReasoningToBedrockReasoning(&schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary: []schemas.ResponsesReasoningSummary{{Type: "summary_text", Text: "simulate night by night"}},
			},
		}, converseReasoningShape("xai/grok-4-0709"), converseRequiresSignedReasoning("xai/grok-4-0709"))
		require.Empty(t, blocks, "a replay to Bedrock must not send a block Bedrock cannot verify")
	})
}

func TestConverseResponseRendersEmbeddedReasoning(t *testing.T) {
	for _, model := range []string{unsignedReasoningClaude, unsignedReasoningNova, "xai/grok-4-0709"} {
		for name, signature := range map[string]*string{"absent": nil, "empty": schemas.Ptr(""), "signed": schemas.Ptr("signed-fixture")} {
			t.Run(model+"/"+name, func(t *testing.T) {
				resp, err := ToBedrockConverseResponse(&schemas.BifrostResponsesResponse{
					Model: model,
					Output: []schemas.ResponsesMessage{{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{Type: schemas.ResponsesOutputMessageContentTypeReasoning, Text: schemas.Ptr("thinking"), Signature: signature},
							{Type: schemas.ResponsesOutputMessageContentTypeText, Text: schemas.Ptr("answer")},
						}},
					}},
				})
				require.NoError(t, err)
				require.Len(t, resp.Output.Message.Content, 2)
				reasoning := resp.Output.Message.Content[0].ReasoningContent
				require.NotNil(t, reasoning)
				require.NotNil(t, reasoning.ReasoningText)
				require.Equal(t, "thinking", *reasoning.ReasoningText.Text)
				require.Equal(t, reasoningSignatureForBedrock(signature), reasoning.ReasoningText.Signature)
				require.Equal(t, "answer", *resp.Output.Message.Content[1].Text)
			})
		}
	}
}

func TestConverseResponseMixedReasoningRoundTrip(t *testing.T) {
	for name, text := range map[string]string{"text": "visible thinking", "empty text": ""} {
		t.Run(name, func(t *testing.T) {
			content := []BedrockContentBlock{
				{ReasoningContent: &BedrockReasoningContent{ReasoningText: &BedrockReasoningContentText{Text: &text, Signature: schemas.Ptr("text-signature")}}},
				{ReasoningContent: &BedrockReasoningContent{RedactedContent: schemas.Ptr("b3BhcXVl")}},
				{Text: schemas.Ptr("answer")},
			}
			upstream := &BedrockConverseResponse{Output: &BedrockConverseOutput{Message: &BedrockMessage{Role: BedrockMessageRoleAssistant, Content: content}}}
			bifrost, err := upstream.ToBifrostResponsesResponse(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))
			require.NoError(t, err)
			bifrost.Model = unsignedReasoningClaude
			rendered, err := ToBedrockConverseResponse(bifrost)
			require.NoError(t, err)
			require.Equal(t, content, rendered.Output.Message.Content, "both reasoning variants and their signatures must survive in order")
		})
	}
}

func TestConverseResponseSummarySignatureIsNotRedactedContent(t *testing.T) {
	blocks := convertBifrostReasoningToConverseResponseReasoning(&schemas.ResponsesMessage{
		ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary:          []schemas.ResponsesReasoningSummary{{Text: "summary"}},
			EncryptedContent: schemas.Ptr("summary-signature"),
		},
	})
	require.Len(t, blocks, 1, "summary encrypted_content is its signature, not a second opaque block")
	require.Equal(t, "summary-signature", *blocks[0].ReasoningContent.ReasoningText.Signature)
	require.Nil(t, blocks[0].ReasoningContent.RedactedContent)
}
