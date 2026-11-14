package bedrock_test

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/providers/bedrock"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// s3Location is a documented member of the Converse source unions, but the API reference
// is explicit that support is per-model: "To see which models support S3 uploads, see
// Supported models and features for Converse."
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_DocumentSource.html
//
// Anthropic models are not among them. Converse validates the union, then translates the
// request into the model's native Messages format, where no S3 source type exists -- the
// member is dropped and the model receives an empty source. The failure surfaces one layer
// down, in Anthropic's vocabulary rather than Converse's:
//
//	ValidationException: The model returned the following errors:
//	messages.0.content.0.document.source.type: Field required
//
// which names a field Converse does not even have. Forwarding s3Location to a model that
// cannot read it is therefore never a working request, so refuse it here where the error
// can say what to do instead.
const (
	novaModel      = "amazon.nova-lite-v1:0"
	anthropicModel = "anthropic.claude-haiku-4-5-20251001-v1:0"
)

func chatRequestWithS3Document(model, uri string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    model,
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{
						{
							Type: schemas.ChatContentBlockTypeFile,
							File: &schemas.ChatInputFile{
								FileURL:  schemas.Ptr(uri),
								FileType: schemas.Ptr("application/pdf"),
							},
						},
						{
							Type: schemas.ChatContentBlockTypeText,
							Text: schemas.Ptr("Summarize this document."),
						},
					},
				},
			},
		},
	}
}

func chatRequestWithS3Image(model, uri string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    model,
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{
						{
							Type:           schemas.ChatContentBlockTypeImage,
							ImageURLStruct: &schemas.ChatInputImage{URL: uri},
						},
					},
				},
			},
		},
	}
}

// TestS3LocationRefusedForModelsThatCannotReadIt pins the refusal for both content kinds
// on both request surfaces. The assertion is on the actionable half of the message, not
// merely on the model name: an error that echoes the model without saying what to send
// instead leaves the caller exactly as stuck as the raw Bedrock 400 did.
func TestS3LocationRefusedForModelsThatCannotReadIt(t *testing.T) {
	t.Parallel()

	t.Run("ChatDocument", func(t *testing.T) {
		t.Parallel()
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		_, err := bedrock.ToBedrockChatCompletionRequest(ctx, chatRequestWithS3Document(anthropicModel, "s3://bucket/doc.pdf"))

		require.Error(t, err, "an s3:// document must not be forwarded to a model that cannot read it")
		assert.Contains(t, err.Error(), "does not support s3:// references")
		assert.Contains(t, err.Error(), "file_data", "the error must name the working alternative")
	})

	t.Run("ChatImage", func(t *testing.T) {
		t.Parallel()
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		_, err := bedrock.ToBedrockChatCompletionRequest(ctx, chatRequestWithS3Image(anthropicModel, "s3://bucket/shot.png"))

		require.Error(t, err, "an s3:// image must not be forwarded to a model that cannot read it")
		assert.Contains(t, err.Error(), "does not support s3:// references")
	})

	t.Run("ResponsesDocument", func(t *testing.T) {
		t.Parallel()
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		_, err := bedrock.ToBedrockResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
			Provider: schemas.Bedrock,
			Model:    anthropicModel,
			Input: []schemas.ResponsesMessage{
				{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesInputMessageContentBlockTypeFile,
								ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
									FileURL:  schemas.Ptr("s3://bucket/doc.pdf"),
									FileType: schemas.Ptr("application/pdf"),
								},
							},
						},
					},
				},
			},
		})

		require.Error(t, err, "the Responses surface must refuse it too, not just Chat")
		assert.Contains(t, err.Error(), "does not support s3:// references")
	})
}

// TestS3LocationStillForwardedForSupportingModels is the other half of the gate. Nova is
// the family AWS's own Converse examples use s3Location with, and the whole point of the
// member is that Converse resolves the object itself: no download round trip in Bifrost
// and no squeezing the payload under the inline byte cap. A gate that refused everything
// would "fix" the 400 by deleting a working feature.
func TestS3LocationStillForwardedForSupportingModels(t *testing.T) {
	t.Parallel()

	t.Run("ChatDocument", func(t *testing.T) {
		t.Parallel()
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		const uri = "s3://bucket/doc.pdf"

		got, err := bedrock.ToBedrockChatCompletionRequest(ctx, chatRequestWithS3Document(novaModel, uri))
		require.NoError(t, err)
		require.Len(t, got.Messages, 1)

		var doc *bedrock.BedrockDocumentSource
		for _, block := range got.Messages[0].Content {
			if block.Document != nil {
				doc = block.Document
				break
			}
		}
		require.NotNil(t, doc, "expected a document block")
		require.NotNil(t, doc.Source.S3Location, "Nova reads s3Location, so the reference must still travel")
		assert.Equal(t, uri, doc.Source.S3Location.URI)
		assert.Nil(t, doc.Source.Bytes, "nothing is downloaded on the s3Location path")
	})

	t.Run("ChatImage", func(t *testing.T) {
		t.Parallel()
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		const uri = "s3://bucket/shot.png"

		got, err := bedrock.ToBedrockChatCompletionRequest(ctx, chatRequestWithS3Image(novaModel, uri))
		require.NoError(t, err)
		require.Len(t, got.Messages, 1)

		var img *bedrock.BedrockImageSource
		for _, block := range got.Messages[0].Content {
			if block.Image != nil {
				img = block.Image
				break
			}
		}
		require.NotNil(t, img, "expected an image block")
		require.NotNil(t, img.Source.S3Location, "Nova reads s3Location, so the reference must still travel")
		assert.Equal(t, uri, img.Source.S3Location.URI)
	})
}

// The malformed-reference refusal must keep firing ahead of the model gate. Both errors
// are correct for an unsupported model given "s3://bucket-only", but only one of them is
// actionable: telling the caller to switch to file_data hides the fact that the URI they
// wrote has no object key and would not have worked on Nova either.
func TestMalformedS3ReferenceIsReportedBeforeTheModelGate(t *testing.T) {
	t.Parallel()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, err := bedrock.ToBedrockChatCompletionRequest(ctx, chatRequestWithS3Document(anthropicModel, "s3://bucket-only"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected s3://bucket/key",
		"a malformed URI is the caller's more immediate problem than the model's capability")
}

// TestS3LocationRefusalIsA400 pins the status, not just the text. Converter errors default
// to ErrRequestBodyConversion, which the HTTP layer answers with 500 - correct for a bug in
// our conversion, wrong here: the caller sent a reference this model can never read, and no
// retry fixes it. It also matters operationally, because 5xx is what infrastructure alerting
// and the e2e harness both treat as "Bifrost is broken" rather than "the request was".
func TestS3LocationRefusalIsA400(t *testing.T) {
	t.Parallel()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, err := bedrock.ToBedrockChatCompletionRequest(ctx, chatRequestWithS3Document(anthropicModel, "s3://bucket/doc.pdf"))
	require.Error(t, err)

	// Through the same wrapping the real request path applies: ToBedrockChatCompletionRequest
	// has already prefixed "failed to convert messages:" by the time this is reached, so a
	// promotion that only worked on the unwrapped error would pass a narrower test and fail
	// in production.
	bifrostErr, ok := providerUtils.AsBifrostBadRequestError(err)
	require.True(t, ok, "the refusal must survive %%w wrapping and promote to a bad request")
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, 400, *bifrostErr.StatusCode)
	require.NotNil(t, bifrostErr.Error)
	assert.Contains(t, bifrostErr.Error.Message, "does not support s3:// references")
	assert.NotContains(t, bifrostErr.Error.Message, "failed to convert messages",
		"the caller gets their own mistake, not our call stack")
}

// responsesRequestWithToolResultImage builds the one Responses shape that reaches the
// tool-result image branch: a function_call registering the call, followed by the
// function_call_output that carries the image back. The two must share a call_id, since
// the converter routes results through the call it registered.
func responsesRequestWithToolResultImage(model, imageURL string) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.Bedrock,
		Model:    model,
		Input: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr("call_screenshot_1"),
					Name:      schemas.Ptr("take_screenshot"),
					Arguments: schemas.Ptr("{}"),
				},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("call_screenshot_1"),
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesInputMessageContentBlockTypeImage,
								ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
									ImageURL: schemas.Ptr(imageURL),
								},
							},
						},
					},
				},
			},
		},
	}
}

// TestS3ToolResultImageRefusalIsNotSwallowed covers the one call site that used to drop
// what convertImageToBedrockSource returned. The drop was written when the only possible
// failure was an unreachable remote image; the model gate then added a second, entirely
// different failure to the same call, and dropping that one turns a request Bifrost knows
// is wrong into a 200 carrying a toolResult with no image member at all. ImageSource is a
// union of bytes and s3Location, so there is no third thing an empty source could mean:
// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ImageSource.html
func TestS3ToolResultImageRefusalIsNotSwallowed(t *testing.T) {
	t.Parallel()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, err := bedrock.ToBedrockResponsesRequest(ctx, responsesRequestWithToolResultImage(anthropicModel, "s3://bucket/shot.png"))

	require.Error(t, err, "an s3:// image in a tool result must be refused, not silently dropped")
	assert.Contains(t, err.Error(), "does not support s3:// references")

	// Same promotion the other gate sites get. A refusal that reached the caller as a 500
	// would tell them to retry something that can never succeed.
	bifrostErr, ok := providerUtils.AsBifrostBadRequestError(err)
	require.True(t, ok, "the refusal must survive %w wrapping and promote to a bad request")
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, 400, *bifrostErr.StatusCode)
}

// The other half: refusing every failure at this site would delete the tolerance the drop
// existed for. A remote image Bifrost cannot resolve is not the caller's mistake and not
// something a retry of the same request fixes on their end, so the tool result still goes
// out without it rather than failing the whole conversation.
func TestUnresolvableToolResultImageIsStillDropped(t *testing.T) {
	t.Parallel()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// A data: URL with no comma fails inside SanitizeImageURL, which returns a plain
	// error rather than an InvalidRequestErrorf. That is the discriminator under test,
	// and it needs no network to reach.
	got, err := bedrock.ToBedrockResponsesRequest(ctx, responsesRequestWithToolResultImage(anthropicModel, "data:image/png;base64"))

	require.NoError(t, err, "an image Bifrost cannot resolve must not fail the request")
	require.NotNil(t, got)

	var toolResult *bedrock.BedrockToolResult
	for _, msg := range got.Messages {
		for _, block := range msg.Content {
			if block.ToolResult != nil {
				toolResult = block.ToolResult
			}
		}
	}
	require.NotNil(t, toolResult, "the tool result itself must still reach Converse")
	for _, block := range toolResult.Content {
		assert.Nil(t, block.Image, "the unresolvable image is the only thing dropped")
	}
}

// TestS3ToolResultImageStillForwardedForNova pins that the refusal is gated on the model,
// not on the tool-result surface. Nova reads s3Location, so the reference must travel
// untouched here exactly as it does in a plain user message.
func TestS3ToolResultImageStillForwardedForNova(t *testing.T) {
	t.Parallel()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	const uri = "s3://bucket/shot.png"

	got, err := bedrock.ToBedrockResponsesRequest(ctx, responsesRequestWithToolResultImage(novaModel, uri))
	require.NoError(t, err)

	var img *bedrock.BedrockImageSource
	for _, msg := range got.Messages {
		for _, block := range msg.Content {
			if block.ToolResult == nil {
				continue
			}
			for _, inner := range block.ToolResult.Content {
				if inner.Image != nil {
					img = inner.Image
				}
			}
		}
	}
	require.NotNil(t, img, "expected the tool result to carry an image block")
	require.NotNil(t, img.Source.S3Location, "Nova reads s3Location, so the reference must still travel")
	assert.Equal(t, uri, img.Source.S3Location.URI)
	assert.Nil(t, img.Source.Bytes, "nothing is downloaded on the s3Location path")
}

// responsesRequestWithContentImage is the plain user-message counterpart to
// responsesRequestWithToolResultImage, so the two Responses surfaces can be asserted
// against the same reference without either one standing in for the other.
func responsesRequestWithContentImage(model, imageURL string) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.Bedrock,
		Model:    model,
		Input: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesInputMessageContentBlockTypeImage,
							ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
								ImageURL: schemas.Ptr(imageURL),
							},
						},
					},
				},
			},
		},
	}
}

// TestMalformedS3ImageReferenceIsReportedLikeTheDocumentPath closes the gap between the
// two content kinds. A document with no object key is refused by name
// (TestMalformedS3ReferenceIsReportedBeforeTheModelGate); an image with no object key used
// to fall through to SanitizeImageURL instead, which answers with
//
//	URL scheme "s3" is not allowed; expected one of: http, https
//
// as a 500. Both halves are wrong for the same request. The status is wrong because no
// retry fixes a mistyped URI, and the message is wrong because s3:// is exactly what Nova
// wants: a caller who believes it would delete the working reference and start inlining
// bytes. The two kinds reach the same s3Location union member, so they must agree here.
func TestMalformedS3ImageReferenceIsReportedLikeTheDocumentPath(t *testing.T) {
	t.Parallel()

	// novaModel deliberately, not anthropicModel: on a model that cannot read s3Location
	// the capability gate would fire first and mask which error is under test.
	const malformed = "s3://bucket-only"

	assertMalformed := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err, "a keyless s3:// image reference names no object and can never resolve")
		assert.Contains(t, err.Error(), "expected s3://bucket/key",
			"the caller needs the missing object key named, the same way the document path names it")
		assert.NotContains(t, err.Error(), "is not allowed",
			"s3:// is allowed on this model, so blaming the scheme sends the caller away from the approach that works")

		bifrostErr, ok := providerUtils.AsBifrostBadRequestError(err)
		require.True(t, ok, "a mistyped URI is caller input, not a Bifrost fault")
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, 400, *bifrostErr.StatusCode)
	}

	t.Run("Chat", func(t *testing.T) {
		t.Parallel()
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		_, err := bedrock.ToBedrockChatCompletionRequest(ctx, chatRequestWithS3Image(novaModel, malformed))
		assertMalformed(t, err)
	})

	t.Run("ResponsesContent", func(t *testing.T) {
		t.Parallel()
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		_, err := bedrock.ToBedrockResponsesRequest(ctx, responsesRequestWithContentImage(novaModel, malformed))
		assertMalformed(t, err)
	})

	t.Run("ResponsesToolResult", func(t *testing.T) {
		t.Parallel()
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		// Reachable only because the tool-result branch stopped discarding what
		// convertImageToBedrockSource returns. It used to drop this and answer 200.
		_, err := bedrock.ToBedrockResponsesRequest(ctx, responsesRequestWithToolResultImage(novaModel, malformed))
		assertMalformed(t, err)
	})
}
