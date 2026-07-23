package gemini

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for the fileData MIME-injection bug.
//
// Bifrost used to default a referenced file's MIME to "application/pdf" whenever the
// caller's fileData carried no mimeType. That wrong type propagated to the outgoing
// generateContent request, so a PNG/CSV got sent to Gemini labeled as a PDF and Gemini
// rejected it with 400 INVALID_ARGUMENT. The MIME must be preserved verbatim — omitted
// when the caller didn't provide one — so Gemini uses the file's stored type.

const testFileURI = "https://generativelanguage.googleapis.com/v1beta/files/abc"

func sptr(s string) *string { return &s }

// Incoming (Gemini -> Bifrost): no MIME must NOT be fabricated.
func TestConvertGeminiFileDataToContentBlock_DoesNotFabricateMime(t *testing.T) {
	// No MIME provided -> FileType stays nil (was wrongly defaulted to application/pdf).
	block := convertGeminiFileDataToContentBlock(&FileData{FileURI: testFileURI})
	require.NotNil(t, block)
	require.NotNil(t, block.ResponsesInputMessageContentBlockFile)
	assert.Nil(t, block.ResponsesInputMessageContentBlockFile.FileType,
		"MIME must not be fabricated when the caller did not provide one")

	// Explicit non-image MIME -> preserved.
	csv := convertGeminiFileDataToContentBlock(&FileData{FileURI: testFileURI, MIMEType: "text/csv"})
	require.NotNil(t, csv.ResponsesInputMessageContentBlockFile)
	require.NotNil(t, csv.ResponsesInputMessageContentBlockFile.FileType)
	assert.Equal(t, "text/csv", *csv.ResponsesInputMessageContentBlockFile.FileType)

	// Image MIME -> routed to an image block.
	img := convertGeminiFileDataToContentBlock(&FileData{FileURI: testFileURI, MIMEType: "image/png"})
	require.NotNil(t, img)
	assert.Equal(t, schemas.ResponsesInputMessageContentBlockTypeImage, img.Type)
}

// Outgoing (Bifrost -> Gemini), Responses path: omit MIME when FileType is unset.
func TestConvertContentBlockToGeminiPart_OmitsMimeWhenUnset(t *testing.T) {
	part, err := convertContentBlockToGeminiPart(schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesInputMessageContentBlockTypeFile,
		ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
			FileURL: sptr(testFileURI),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, part)
	require.NotNil(t, part.FileData)
	assert.Empty(t, part.FileData.MIMEType, "MIME must be omitted when FileType is unset")
	assert.Equal(t, testFileURI, part.FileData.FileURI)

	// FileType provided -> preserved.
	part2, err := convertContentBlockToGeminiPart(schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesInputMessageContentBlockTypeFile,
		ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
			FileURL:  sptr(testFileURI),
			FileType: sptr("text/csv"),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, part2.FileData)
	assert.Equal(t, "text/csv", part2.FileData.MIMEType)
}

// Outgoing (Bifrost -> Gemini), Chat path: omit MIME when FileType is unset.
func TestConvertBifrostMessagesToGemini_OmitsFileMimeWhenUnset(t *testing.T) {
	msgs := []schemas.ChatMessage{{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{{
				Type: schemas.ChatContentBlockTypeFile,
				File: &schemas.ChatInputFile{FileURL: sptr(testFileURI)},
			}},
		},
	}}
	contents, _, err := convertBifrostMessagesToGemini(msgs)
	require.NoError(t, err)
	require.Len(t, contents, 1)
	require.Len(t, contents[0].Parts, 1)
	require.NotNil(t, contents[0].Parts[0].FileData)
	assert.Empty(t, contents[0].Parts[0].FileData.MIMEType, "MIME must be omitted when FileType is unset")
}

// End-to-end: a generateContent request whose fileData has no MIME must round-trip
// (Gemini -> Bifrost -> Gemini) without a MIME being injected. This is the exact path
// that produced the INVALID_ARGUMENT.
func TestGenerateContentRoundTrip_NoMimeInjectionForFileData(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	in := &GeminiGenerationRequest{
		Model: "gemini-2.5-flash",
		Contents: []Content{{
			Role: "user",
			Parts: []*Part{
				{Text: "What is in this file?"},
				{FileData: &FileData{FileURI: testFileURI}},
			},
		}},
	}

	bifrostReq := in.ToBifrostResponsesRequest(ctx)
	require.NotNil(t, bifrostReq)

	out, err := ToGeminiResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.NotNil(t, out)

	var found *FileData
	for _, c := range out.Contents {
		for _, p := range c.Parts {
			if p.FileData != nil {
				found = p.FileData
			}
		}
	}
	require.NotNil(t, found, "outgoing request must contain a fileData part")
	assert.Equal(t, testFileURI, found.FileURI)
	assert.Empty(t, found.MIMEType, "no MIME should be injected when the caller didn't provide one")
}
