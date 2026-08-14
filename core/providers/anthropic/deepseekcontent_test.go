package anthropic

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestBuildAnthropicRequestBodiesRejectDeepSeekV4UnsupportedContent pins the
// pre-egress contract for both neutral APIs and both execution modes.
func TestBuildAnthropicRequestBodiesRejectDeepSeekV4UnsupportedContent(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	imageURL := "data:image/png;base64,cGF5bG9hZC1tdXN0LW5vdC1hcHBlYXI="
	chatRequest := &schemas.BifrostChatRequest{
		Model: deepSeekV4ProModel,
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{
				Type:           schemas.ChatContentBlockTypeImage,
				ImageURLStruct: &schemas.ChatInputImage{URL: imageURL},
			}}},
		}},
	}

	fileData := "payload-must-not-appear"
	fileType := "text/plain"
	role := schemas.ResponsesInputMessageRoleUser
	messageType := schemas.ResponsesMessageTypeMessage
	responsesRequest := &schemas.BifrostResponsesRequest{
		Model: deepSeekV4FlashModel,
		Input: []schemas.ResponsesMessage{{
			Type: &messageType,
			Role: &role,
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesInputMessageContentBlockTypeFile,
				ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
					FileData: &fileData,
					FileType: &fileType,
				},
			}}},
		}},
	}

	for _, streaming := range []bool{false, true} {
		t.Run("chat/stream="+strconv.FormatBool(streaming), func(t *testing.T) {
			body, err := BuildAnthropicChatRequestBody(ctx, chatRequest, AnthropicRequestBuildConfig{
				Provider:    schemas.DeepSeek,
				IsStreaming: streaming,
			})
			if err == nil {
				t.Fatalf("unsupported image reached Anthropic serialization: %s", body)
			}
			assertDeepSeekV4UnsupportedContentError(t, err, "image")
		})

		t.Run("responses/stream="+strconv.FormatBool(streaming), func(t *testing.T) {
			body, err := BuildAnthropicResponsesRequestBody(ctx, responsesRequest, AnthropicRequestBuildConfig{
				Provider:    schemas.DeepSeek,
				IsStreaming: streaming,
			})
			if err == nil {
				t.Fatalf("unsupported document reached Anthropic serialization: %s", body)
			}
			assertDeepSeekV4UnsupportedContentError(t, err, "document")
		})
	}
}

// TestRejectDeepSeekV4UnsupportedResponsesContent covers both unsupported
// neutral Responses discriminators on the exact Pro identity.
func TestRejectDeepSeekV4UnsupportedResponsesContent(t *testing.T) {
	role := schemas.ResponsesInputMessageRoleUser
	messageType := schemas.ResponsesMessageTypeMessage
	for _, tc := range []struct {
		name string
		kind schemas.ResponsesMessageContentBlockType
		want deepSeekV4UnsupportedContentKind
	}{
		{name: "image", kind: schemas.ResponsesInputMessageContentBlockTypeImage, want: deepSeekV4UnsupportedImage},
		{name: "document", kind: schemas.ResponsesInputMessageContentBlockTypeFile, want: deepSeekV4UnsupportedDocument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := &schemas.BifrostResponsesRequest{
				Model: deepSeekV4ProModel,
				Input: []schemas.ResponsesMessage{{
					Type: &messageType,
					Role: &role,
					Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
						Type: tc.kind,
					}}},
				}},
			}
			err := rejectDeepSeekV4UnsupportedResponsesContent(nil, schemas.DeepSeek, request)
			assertDeepSeekV4UnsupportedContentError(t, err, string(tc.want))
		})
	}
}

// TestRejectDeepSeekV4UnsupportedContentInToolResults covers content nested in
// rich function output and computer screenshot output.
func TestRejectDeepSeekV4UnsupportedContentInToolResults(t *testing.T) {
	imageBlock := schemas.ResponsesMessageContentBlock{Type: schemas.ResponsesInputMessageContentBlockTypeImage}
	request := &schemas.BifrostResponsesRequest{
		Model: deepSeekV4FlashModel,
		Input: []schemas.ResponsesMessage{{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{imageBlock},
				},
			},
		}},
	}
	assertDeepSeekV4UnsupportedContentError(t, rejectDeepSeekV4UnsupportedResponsesContent(nil, schemas.DeepSeek, request), string(deepSeekV4UnsupportedImage))

	request.Input[0].ResponsesToolMessage.Output = &schemas.ResponsesToolMessageOutputStruct{
		ResponsesComputerToolCallOutput: &schemas.ResponsesComputerToolCallOutputData{FileID: schemas.Ptr("payload-must-not-appear")},
	}
	assertDeepSeekV4UnsupportedContentError(t, rejectDeepSeekV4UnsupportedResponsesContent(nil, schemas.DeepSeek, request), string(deepSeekV4UnsupportedImage))
}

// TestDeepSeekV4ContentGuardIsExactAndTextToolSafe proves aliases activate the
// guard while other providers, near-miss models, and text/tool strings do not.
func TestDeepSeekV4ContentGuardIsExactAndTextToolSafe(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "frontier",
		Config: &schemas.AliasConfig{
			ModelID: deepSeekV4ProModel,
		},
	})

	text := "payload-must-not-appear"
	role := schemas.ResponsesInputMessageRoleUser
	messageType := schemas.ResponsesMessageTypeMessage
	request := &schemas.BifrostResponsesRequest{
		Model: "frontier",
		Input: []schemas.ResponsesMessage{
			{
				Type: &messageType,
				Role: &role,
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type: schemas.ResponsesInputMessageContentBlockTypeText,
					Text: &text,
				}}},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &text},
				},
			},
		},
	}
	if err := rejectDeepSeekV4UnsupportedResponsesContent(ctx, schemas.DeepSeek, request); err != nil {
		t.Fatalf("text/tool request was rejected: %v", err)
	}

	imageRequest := &schemas.BifrostResponsesRequest{
		Model: "frontier",
		Input: []schemas.ResponsesMessage{{
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesInputMessageContentBlockTypeImage,
			}}},
		}},
	}
	assertDeepSeekV4UnsupportedContentError(t, rejectDeepSeekV4UnsupportedResponsesContent(ctx, schemas.DeepSeek, imageRequest), string(deepSeekV4UnsupportedImage))

	for _, tc := range []struct {
		provider schemas.ModelProvider
		model    string
	}{
		{provider: schemas.Anthropic, model: deepSeekV4ProModel},
		{provider: schemas.DeepSeek, model: "deepseek-v4-pro-0424"},
		{provider: schemas.DeepSeek, model: "DeepSeek-V4-Pro"},
	} {
		imageRequest.Model = tc.model
		if err := rejectDeepSeekV4UnsupportedResponsesContent(nil, tc.provider, imageRequest); err != nil {
			t.Fatalf("near miss %s/%s activated guard: %v", tc.provider, tc.model, err)
		}
	}
}

// TestDeepSeekV4ContentGuardFailsClosedWithoutConsumingLargePayload proves the
// one-shot body remains untouched for a subsequent fallback attempt.
func TestDeepSeekV4ContentGuardFailsClosedWithoutConsumingLargePayload(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	reader := strings.NewReader("payload-must-not-appear")
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, reader)

	request := &schemas.BifrostResponsesRequest{Model: deepSeekV4ProModel}
	assertDeepSeekV4UnsupportedContentError(t, rejectDeepSeekV4UnsupportedResponsesContent(ctx, schemas.DeepSeek, request), string(deepSeekV4UninspectablePayload))
	if reader.Len() != len("payload-must-not-appear") {
		t.Fatalf("large-payload reader was consumed: %d bytes remain", reader.Len())
	}
}

// TestDeepSeekV4UnsupportedContentErrorIsPayloadFree verifies the stable error
// omits content-bearing field names and values.
func TestDeepSeekV4UnsupportedContentErrorIsPayloadFree(t *testing.T) {
	err := newDeepSeekV4UnsupportedContentError(deepSeekV4UnsupportedImage)
	assertDeepSeekV4UnsupportedContentError(t, err, string(deepSeekV4UnsupportedImage))
	for _, forbidden := range []string{"payload-must-not-appear", "image_url", "file_data"} {
		if strings.Contains(err.String(), forbidden) {
			t.Fatalf("typed error retained payload-bearing data %q: %s", forbidden, err.String())
		}
	}
}

// assertDeepSeekV4UnsupportedContentError checks the stable, payload-free
// error contract used by the core fallback engine and direct HTTP callers.
func assertDeepSeekV4UnsupportedContentError(t *testing.T, err *schemas.BifrostError, kind string) {
	t.Helper()
	if err == nil || err.StatusCode == nil || *err.StatusCode != 415 || err.Error == nil ||
		err.Error.Type == nil || *err.Error.Type != "unsupported_content_kind" ||
		err.Error.Code == nil || *err.Error.Code != "deepseek_unsupported_content_kind" {
		t.Fatalf("unexpected typed error: %#v", err)
	}
	if !err.IsBifrostError {
		t.Fatalf("content error must be local: %#v", err.IsBifrostError)
	}
	if err.AllowFallbacks == nil || !*err.AllowFallbacks {
		t.Fatalf("content error must permit enumerated fallback: %#v", err.AllowFallbacks)
	}
	if !strings.Contains(err.Error.Message, kind) {
		t.Fatalf("content kind missing from sanitized error: %q", err.Error.Message)
	}
}
