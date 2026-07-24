package bedrock

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBedrockDocumentFormatFromMediaType(t *testing.T) {
	t.Parallel()

	format, isText, ok := bedrockDocumentFormatFromMediaType(
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet; charset=binary")
	require.True(t, ok)
	assert.Equal(t, "xlsx", format)
	assert.False(t, isText)

	format, isText, ok = bedrockDocumentFormatFromMediaType("text/plain; charset=utf-8")
	require.True(t, ok)
	assert.Equal(t, "txt", format)
	assert.True(t, isText)

	format, isText, ok = bedrockDocumentFormatFromMediaType("text/x-custom")
	require.True(t, ok)
	assert.Equal(t, "txt", format)
	assert.True(t, isText)

	_, _, ok = bedrockDocumentFormatFromMediaType("application/octet-stream")
	assert.False(t, ok)

	format, isText, ok = bedrockDocumentFormatFromFilename("Quarterly Report.XLSX")
	require.True(t, ok)
	assert.Equal(t, "xlsx", format)
	assert.False(t, isText)

	_, _, ok = bedrockDocumentFormatFromFilename("noext")
	assert.False(t, ok)
}

// TestResponsesFileDataURLMediaTypeMapsDocumentFormat covers the Responses API
// sibling of #5472: input_file with MIME only in file_data and no file_type.
func TestResponsesFileDataURLMediaTypeMapsDocumentFormat(t *testing.T) {
	t.Parallel()

	payload := "ZmFrZS14bHN4"
	fileData := "data:application/vnd.openxmlformats-officedocument.spreadsheetml.sheet;base64," + payload
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-3-5-sonnet-v2",
		Input: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesInputMessageContentBlockTypeText,
							Text: schemas.Ptr("Summarize this spreadsheet."),
						},
						{
							Type: schemas.ResponsesInputMessageContentBlockTypeFile,
							ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
								Filename: schemas.Ptr("sheet.xlsx"),
								FileData: &fileData,
							},
						},
					},
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bedrockReq, err := ToBedrockResponsesRequest(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, bedrockReq)
	require.NotEmpty(t, bedrockReq.Messages)
	require.GreaterOrEqual(t, len(bedrockReq.Messages[0].Content), 2)
	doc := bedrockReq.Messages[0].Content[1].Document
	require.NotNil(t, doc, "Responses input_file must become a Bedrock document block")
	assert.Nil(t, bedrockReq.Messages[0].Content[1].Image)
	assert.Equal(t, "xlsx", doc.Format)
	require.NotNil(t, doc.Source)
	require.NotNil(t, doc.Source.Bytes)
	assert.Equal(t, payload, *doc.Source.Bytes)
}

// TestResponsesUnknownFileTypeFallsBackToFilename covers CodeRabbit #5503:
// a non-nil unrecognized file_type (e.g. application/octet-stream) must not
// skip filename inference, or report.xlsx would stay the Bedrock pdf default.
func TestResponsesUnknownFileTypeFallsBackToFilename(t *testing.T) {
	t.Parallel()

	payload := "ZmFrZS14bHN4"
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-3-5-sonnet-v2",
		Input: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesInputMessageContentBlockTypeText,
							Text: schemas.Ptr("Summarize this spreadsheet."),
						},
						{
							Type: schemas.ResponsesInputMessageContentBlockTypeFile,
							ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
								Filename: schemas.Ptr("report.xlsx"),
								FileType: schemas.Ptr("application/octet-stream"),
								FileData: &payload,
							},
						},
					},
				},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bedrockReq, err := ToBedrockResponsesRequest(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, bedrockReq)
	require.NotEmpty(t, bedrockReq.Messages)
	require.GreaterOrEqual(t, len(bedrockReq.Messages[0].Content), 2)
	doc := bedrockReq.Messages[0].Content[1].Document
	require.NotNil(t, doc, "Responses input_file must become a Bedrock document block")
	assert.Equal(t, "xlsx", doc.Format,
		"unrecognized file_type must fall back to filename extension")
	require.NotNil(t, doc.Source)
	require.NotNil(t, doc.Source.Bytes)
	assert.Equal(t, payload, *doc.Source.Bytes)
}
