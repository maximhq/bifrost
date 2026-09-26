package bedrock

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/bytedance/sonic"
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
		}, converseReasoningShape("xai/grok-4-0709"), converseRequiresSignedReasoning("xai/grok-4-0709"), false)
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

// Converse declares reasoningContent.redactedContent a blob, so strict clients
// (aws-sdk-go-v2, boto3) base64-decode it. A non-Bedrock upstream's encrypted
// reasoning is an arbitrary token: OpenAI and Azure mint URL-safe Fernet tokens,
// whose '-' and '_' are illegal in standard base64. Rendering one raw made the
// SDK fail with "decode base64 blob: illegal base64 data" (#7514).
const foreignReasoningToken = "gAAAAABo1x-Kq2_Zr8vJ3mNcY0tW5bQpL7hEfGdA=="

func renderRedactedReasoningWithToolCall(t *testing.T, token string) string {
	t.Helper()
	resp, err := ToBedrockConverseResponse(&schemas.BifrostResponsesResponse{
		Model: "gpt-5.6-luna",
		Output: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{},
					EncryptedContent: &token,
				},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr("call_1"),
					Name:      schemas.Ptr("get_weather"),
					Arguments: schemas.Ptr(`{"city":"Paris"}`),
				},
			},
		},
	})
	require.NoError(t, err)
	content := resp.Output.Message.Content
	require.Len(t, content, 2, "reasoning block then toolUse block, got %+v", content)
	require.NotNil(t, content[0].ReasoningContent)
	require.NotNil(t, content[0].ReasoningContent.RedactedContent, "the encrypted token must render as redactedContent")
	require.NotNil(t, content[1].ToolUse)
	return *content[0].ReasoningContent.RedactedContent
}

func TestConverseResponseRedactedContentIsStandardBase64(t *testing.T) {
	redacted := renderRedactedReasoningWithToolCall(t, foreignReasoningToken)
	_, err := base64.StdEncoding.DecodeString(redacted)
	require.NoError(t, err, "redactedContent is a Converse blob and must be standard base64, got %q", redacted)
}

// A Converse client replays redactedContent as the bytes it decoded, re-encoded
// canonically. The next turn must hand the upstream its original token back.
func TestConverseRedactedContentSurvivesSDKReplay(t *testing.T) {
	for name, token := range map[string]string{
		"foreign token":       foreignReasoningToken,
		"native bedrock blob": "cnNuXzVaVnJpZjRKMGJYSXFtV2RsZWRqN1FJRmVGZWdz",
	} {
		t.Run(name, func(t *testing.T) {
			redacted := renderRedactedReasoningWithToolCall(t, token)
			sdkBytes, err := base64.StdEncoding.DecodeString(redacted)
			require.NoError(t, err, "the SDK rejects a non-base64 blob, got %q", redacted)
			replayed := base64.StdEncoding.EncodeToString(sdkBytes)

			req := &BedrockConverseRequest{
				ModelID: "azure/gpt-5.6-luna",
				Messages: []BedrockMessage{
					{Role: BedrockMessageRoleUser, Content: []BedrockContentBlock{{Text: schemas.Ptr("weather in Paris?")}}},
					{Role: BedrockMessageRoleAssistant, Content: []BedrockContentBlock{
						{ReasoningContent: &BedrockReasoningContent{RedactedContent: &replayed}},
						{ToolUse: &BedrockToolUse{ToolUseID: "call_1", Name: "get_weather", Input: json.RawMessage(`{"city":"Paris"}`)}},
					}},
				},
			}
			bifrostReq, err := req.ToBifrostResponsesRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))
			require.NoError(t, err)
			var encrypted *string
			for _, msg := range bifrostReq.Input {
				if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeReasoning && msg.ResponsesReasoning != nil {
					encrypted = msg.ResponsesReasoning.EncryptedContent
				}
			}
			require.NotNil(t, encrypted, "the replayed reasoning item must carry encrypted_content")
			require.Equal(t, token, *encrypted, "the upstream must get back exactly the token it minted")
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

// Over HTTP, BedrockConverseRequest.UnmarshalJSON keeps unknown top-level fields as
// json.RawMessage. include and reasoning_summary were read with SafeExtract*, which
// has no RawMessage case, so a Converse client could never ask a non-Bedrock
// upstream for encrypted reasoning or a summary level: both were silently dropped.
func TestConverseRequestHonorsIncludeAndReasoningSummaryFromHTTPBody(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"text":"hi"}]}],` +
		`"additionalModelRequestFields":{"reasoning_config":{"type":"enabled","budget_tokens":1024}},` +
		`"include":["reasoning.encrypted_content"],"reasoning_summary":"detailed"}`
	var req BedrockConverseRequest
	require.NoError(t, sonic.Unmarshal([]byte(body), &req))
	req.ModelID = "azure/gpt-5.6-luna"

	bifrostReq, err := req.ToBifrostResponsesRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))
	require.NoError(t, err)
	require.Equal(t, []string{"reasoning.encrypted_content"}, bifrostReq.Params.Include, "include from the HTTP body must reach the upstream request")
	require.NotNil(t, bifrostReq.Params.Reasoning)
	require.NotNil(t, bifrostReq.Params.Reasoning.Summary)
	require.Equal(t, "detailed", *bifrostReq.Params.Reasoning.Summary, "reasoning_summary from the HTTP body must win over the auto default")
}

// A JSON null decodes into a Go string without error, so a raw `null` used to come
// back as a pointer to "": non-nil, which suppressed the "auto" summary default and
// left an OpenAI reasoning model returning no reasoning text. null means absent.
func TestConverseRequestTreatsNullReasoningSummaryAsAbsent(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"text":"hi"}]}],` +
		`"additionalModelRequestFields":{"reasoning_config":{"type":"enabled","budget_tokens":1024}},` +
		`"include":null,"reasoning_summary":null}`
	var req BedrockConverseRequest
	require.NoError(t, sonic.Unmarshal([]byte(body), &req))
	req.ModelID = "azure/gpt-5.6-luna"

	bifrostReq, err := req.ToBifrostResponsesRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))
	require.NoError(t, err)
	require.Nil(t, bifrostReq.Params.Include, "a null include is absent")
	require.NotNil(t, bifrostReq.Params.Reasoning)
	require.NotNil(t, bifrostReq.Params.Reasoning.Summary, "a null reasoning_summary must still get the auto default")
	require.Equal(t, "auto", *bifrostReq.Params.Reasoning.Summary, "a null reasoning_summary must not suppress the auto default")
}
