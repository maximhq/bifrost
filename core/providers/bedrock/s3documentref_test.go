package bedrock_test

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/providers/bedrock"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bedrockS3LocationFromURL deliberately rejects a bucket-only URI ("s3://bucket") because
// Converse needs an object key. The caller only acted on ok == true, so such a value fell
// through to the http(s) fetch path and surfaced as a low-level dial failure instead of
// naming the real problem. Other FetchAndEncodeURL call sites (anthropic/urlsourceinlining.go,
// openai/chatfileurl.go) gate on the scheme before fetching for the same reason.
func TestMalformedS3DocumentReferenceIsRejectedClearly(t *testing.T) {
	t.Parallel()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	t.Run("Chat", func(t *testing.T) {
		t.Parallel()

		_, err := bedrock.ToBedrockChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
			Provider: schemas.Bedrock,
			Model:    "anthropic.claude-sonnet-4-5-20250929-v1:0",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentBlocks: []schemas.ChatContentBlock{
							{
								Type: schemas.ChatContentBlockTypeFile,
								File: &schemas.ChatInputFile{
									Filename: schemas.Ptr("report.pdf"),
									FileURL:  schemas.Ptr("s3://bucket-only"),
								},
							},
						},
					},
				},
			},
		})

		require.Error(t, err, "a bucket-only s3:// reference must not reach the URL fetch path")
		// The specific validation reason, not just any mention of s3://. A low-level fetch
		// failure would also contain "s3://" (it echoes the URL), so matching on that alone
		// would keep passing if this stopped being caught by the s3 branch at all.
		assert.Contains(t, err.Error(), "expected s3://bucket/key",
			"the error must name the missing object key, not merely echo the URL")
	})

	t.Run("Responses", func(t *testing.T) {
		t.Parallel()

		_, err := bedrock.ToBedrockResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
			Provider: schemas.Bedrock,
			Model:    "anthropic.claude-sonnet-4-5-20250929-v1:0",
			Input: []schemas.ResponsesMessage{
				{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesInputMessageContentBlockTypeFile,
								ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
									Filename: schemas.Ptr("report.pdf"),
									FileURL:  schemas.Ptr("s3://bucket-only"),
								},
							},
						},
					},
				},
			},
		})

		require.Error(t, err, "a bucket-only s3:// reference must not reach the URL fetch path")
		// The specific validation reason, not just any mention of s3://. A low-level fetch
		// failure would also contain "s3://" (it echoes the URL), so matching on that alone
		// would keep passing if this stopped being caught by the s3 branch at all.
		assert.Contains(t, err.Error(), "expected s3://bucket/key",
			"the error must name the missing object key, not merely echo the URL")
	})
}

// Pins the claim bedrockImageFormatFromPath's doc comment makes -- "the four names are
// the formats Converse accepts" -- now that the comment sits on that function rather than
// on its document twin. Reached through the public converter: an s3:// image resolves its
// format from the object key alone, because nothing is downloaded on that path.
func TestBedrockImageS3URIFormatVocabulary(t *testing.T) {
	t.Parallel()

	imageBlock := func(t *testing.T, uri string) (*schemas.BifrostChatRequest, string) {
		t.Helper()
		return &schemas.BifrostChatRequest{
			Provider: schemas.Bedrock,
			Model:    "anthropic.claude-sonnet-4-5-20250929-v1:0",
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
		}, uri
	}

	for _, tc := range []struct {
		name   string
		uri    string
		format string
	}{
		{"png", "s3://bucket/img/a.png", "png"},
		{"gif", "s3://bucket/img/a.gif", "gif"},
		{"webp", "s3://bucket/img/a.webp", "webp"},
		{"jpeg", "s3://bucket/img/a.jpeg", "jpeg"},
		{"jpg maps to jpeg", "s3://bucket/img/a.jpg", "jpeg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			req, _ := imageBlock(t, tc.uri)

			got, err := bedrock.ToBedrockChatCompletionRequest(ctx, req)
			require.NoError(t, err)
			require.Len(t, got.Messages, 1)

			var img *bedrock.BedrockImageSource
			for _, block := range got.Messages[0].Content {
				if block.Image != nil {
					img = block.Image
					break
				}
			}
			require.NotNil(t, img, "an s3:// image must produce an image block")
			assert.Equal(t, tc.format, img.Format)

			// Format inference is only half of it: the point of the s3Location path is that
			// Converse resolves the object itself. Pin the union so a regression that keeps
			// the format but stops forwarding the reference (or starts inlining bytes) fails.
			require.NotNil(t, img.Source.S3Location, "the s3:// reference must travel as s3Location")
			assert.Equal(t, tc.uri, img.Source.S3Location.URI)
			assert.Nil(t, img.Source.Bytes, "nothing is downloaded on the s3Location path")
		})
	}

	t.Run("anything else is refused", func(t *testing.T) {
		t.Parallel()
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		req, _ := imageBlock(t, "s3://bucket/img/a.bmp")

		_, err := bedrock.ToBedrockChatCompletionRequest(ctx, req)
		require.Error(t, err, "bmp is outside the four formats Converse accepts")
		assert.Contains(t, err.Error(), "png, jpeg, gif or webp")
	})
}
