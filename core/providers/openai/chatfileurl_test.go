package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for URL-sourced documents being dropped on the OpenAI/Azure chat path.
//
// A document block arrives as {"type":"document","source":{"type":"url","url":...}} and
// ChatContentBlock.UnmarshalJSON normalizes it to File.FileURL, documented as
// "provider fetches at convert time" (schemas/chatcompletions.go). Chat Completions does not
// accept file_url - only file_id, or file_data as a base64 data URI with a filename - so
// OpenAIChatRequest.MarshalJSON strips FileURL. Nothing ever fetched it, so the block went out
// as an empty object and the document vanished:
//
//	raw_request: {"type":"file","file":{}}
//	azure 400:   Missing required parameter: 'messages[0].content[0].file.file_id'
//
// Harness cells 47.10.A and 47.10.C. Shapes B and D (Responses API) passed throughout, because
// file_url IS valid there - which is why only the two chat shapes failed.

func TestBuildChatFileFromFetch_ProducesDataURIAndFilename(t *testing.T) {
	data, name := buildChatFileFromFetch("application/pdf", "QkFTRTY0",
		"https://www.berkshirehathaway.com/letters/2024ltr.pdf")

	assert.Equal(t, "data:application/pdf;base64,QkFTRTY0", data,
		"Chat Completions requires file_data as a base64 data URI, not raw base64")
	assert.Equal(t, "2024ltr.pdf", name, "filename should come from the URL path")
}

// Content-Type often carries parameters; they must not leak into the data URI.
func TestBuildChatFileFromFetch_NormalizesMediaTypeParameters(t *testing.T) {
	data, _ := buildChatFileFromFetch("application/pdf; charset=binary", "QUJD", "https://x.test/a.pdf")
	assert.Equal(t, "data:application/pdf;base64,QUJD", data)
}

// A URL with no usable basename still needs a filename, since OpenAI requires one alongside
// file_data.
func TestBuildChatFileFromFetch_FallsBackWhenPathHasNoFilename(t *testing.T) {
	_, name := buildChatFileFromFetch("application/pdf", "QUJD", "https://x.test/download?id=7")
	assert.NotEmpty(t, name, "filename is required whenever file_data is used")
	assert.Contains(t, name, ".pdf", "extension should follow the fetched media type")
}

// A block that already carries file_data or file_id must be left completely alone - no refetch,
// no overwrite.
func TestInlineChatFileURLs_LeavesResolvedBlocksUntouched(t *testing.T) {
	for _, tc := range []struct {
		name  string
		file  schemas.ChatInputFile
		check func(t *testing.T, f *schemas.ChatInputFile)
	}{
		{
			name: "already has file_id",
			file: schemas.ChatInputFile{FileID: schemas.Ptr("file-abc"), FileURL: schemas.Ptr("https://x.test/a.pdf")},
			check: func(t *testing.T, f *schemas.ChatInputFile) {
				require.NotNil(t, f.FileID)
				assert.Equal(t, "file-abc", *f.FileID)
				assert.Nil(t, f.FileData, "must not fetch when a file_id is already present")
			},
		},
		{
			name: "already has file_data",
			file: schemas.ChatInputFile{FileData: schemas.Ptr("data:application/pdf;base64,QUJD")},
			check: func(t *testing.T, f *schemas.ChatInputFile) {
				require.NotNil(t, f.FileData)
				assert.Equal(t, "data:application/pdf;base64,QUJD", *f.FileData)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.file
			msgs := []OpenAIMessage{{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeFile, File: &f},
				}},
			}}
			inlineChatFileURLs(nil, msgs)
			tc.check(t, msgs[0].Content.ContentBlocks[0].File)
		})
	}
}
