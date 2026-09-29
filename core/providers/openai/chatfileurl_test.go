package openai

import (
	"encoding/base64"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Regression tests for URL-sourced documents being dropped on the OpenAI/Azure chat path.
//
// A document block arrives as {"type":"document","source":{"type":"url","url":...}} and
// ChatContentBlock.UnmarshalJSON normalizes it to File.FileURL, documented as
// "provider fetches at convert time" (schemas/chatcompletions.go). Chat Completions does not
// accept file_url - only file_id, or file_data as a base64 data URI with a filename - and
// OpenAIChatRequest.MarshalJSON used to strip FileURL. Nothing ever fetched it, so the block
// went out as an empty object and the document vanished:
//
//	raw_request: {"type":"file","file":{}}
//	azure 400:   Missing required parameter: 'messages[0].content[0].file.file_id'
//
// Harness cells 47.10.A and 47.10.C. Shapes B and D (Responses API) passed throughout, because
// file_url IS valid there - which is why only the two chat shapes failed.
//
// Two things fix that. ResolveChatFileURLs fetches http(s) sources and inlines them as
// file_data, and MarshalJSON no longer strips file_url - so a source Bifrost cannot fetch
// reaches the provider intact and is refused by name instead of disappearing.

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
			req := &OpenAIChatRequest{Messages: msgs}
			require.NoError(t, ResolveChatFileURLs(nil, schemas.OpenAI, req))
			tc.check(t, req.Messages[0].Content.ContentBlocks[0].File)
		})
	}
}

// The URL's extension and the fetched media type can disagree - a link ending in .txt that serves
// application/pdf, a content-negotiating endpoint, a redirect to a generated file. OpenAI validates
// the filename against file_data's media type and rejects the pair, so the fetched type has to win.
func TestBuildChatFileFromFetch_CorrectsFilenameExtensionForMediaType(t *testing.T) {
	for _, tt := range []struct {
		name      string
		mediaType string
		sourceURL string
		want      string
		why       string
	}{
		{
			name:      "pdf served under a .txt url",
			mediaType: "application/pdf",
			sourceURL: "https://x.test/report.txt",
			want:      "report.pdf",
			why:       "a .txt name alongside PDF file_data is the combination OpenAI rejects",
		},
		{
			name:      "matching extension is left alone",
			mediaType: "application/pdf",
			sourceURL: "https://x.test/report.pdf",
			want:      "report.pdf",
			why:       "a correct name must survive untouched",
		},
		{
			name:      "case-insensitive match is left alone",
			mediaType: "application/pdf",
			sourceURL: "https://x.test/REPORT.PDF",
			want:      "REPORT.PDF",
			why:       "extension comparison is case-insensitive, so this name is already correct",
		},
		{
			name:      "alias extension is accepted",
			mediaType: "text/html",
			sourceURL: "https://x.test/page.htm",
			want:      "page.htm",
			why:       ".htm is a legitimate spelling for text/html and must not be rewritten",
		},
		{
			name:      "unknown media type never renames",
			mediaType: "application/octet-stream",
			sourceURL: "https://x.test/archive.tar",
			want:      "archive.tar",
			why:       "with no mapping there is no basis to call the caller's extension wrong",
		},
		{
			name:      "stem with dots keeps everything but the final suffix",
			mediaType: "application/pdf",
			sourceURL: "https://x.test/2024.annual.report.txt",
			want:      "2024.annual.report.pdf",
			why:       "only the final extension is the type claim; the rest of the stem is the name",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, name := buildChatFileFromFetch(tt.mediaType, "QUJD", tt.sourceURL)
			assert.Equal(t, tt.want, name, tt.why)
		})
	}
}

// A URL with no usable basename still has to produce a filename, and that synthetic name must
// carry the extension for the media type actually fetched.
func TestBuildChatFileFromFetch_SyntheticNameUsesFetchedMediaType(t *testing.T) {
	_, name := buildChatFileFromFetch("text/csv", "QUJD", "https://x.test/download")
	assert.Equal(t, "document.csv", name)

	_, unknown := buildChatFileFromFetch("application/octet-stream", "QUJD", "https://x.test/download")
	assert.Equal(t, "document.bin", unknown, "an unmapped type falls back to .bin")
}

// Provider gating. Only OpenAI and Azure reject file_url, so nobody else should pay for a fetch.
//
// Note on coverage limits: the successful-fetch path cannot be exercised from a unit test.
// FetchAndEncodeURL dials through network.SSRFSafeDialContext, which rejects every non-public
// address unconditionally and has no test seam, so an httptest server on 127.0.0.1 is unreachable
// by design. Weakening that guard to make a test pass would be a bad trade. What a fetch produces
// once it succeeds is covered by the buildChatFileFromFetch tests above, which exercise the same
// data URI and filename assembly this function applies to the block.
func TestResolveChatFileURLs_OnlyFetchesForOpenAIAndAzure(t *testing.T) {
	newReq := func() *OpenAIChatRequest {
		return &OpenAIChatRequest{Messages: []OpenAIMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{
					Type: schemas.ChatContentBlockTypeFile,
					File: &schemas.ChatInputFile{FileURL: schemas.Ptr("https://x.invalid/report.pdf")},
				},
			}},
		}}}
	}

	for _, provider := range []schemas.ModelProvider{schemas.Vertex, schemas.Cerebras, schemas.DeepSeek} {
		t.Run(string(provider)+" is left untouched", func(t *testing.T) {
			req := newReq()
			require.NoError(t, ResolveChatFileURLs(nil, provider, req),
				"a provider that accepts file_url must not attempt a fetch at all")

			file := req.Messages[0].Content.ContentBlocks[0].File
			assert.Nil(t, file.FileData, "no fetch should have happened")
			require.NotNil(t, file.FileURL, "the original block must survive intact")
			assert.Equal(t, "https://x.invalid/report.pdf", *file.FileURL)
		})
	}

	t.Run("a nil request is handled", func(t *testing.T) {
		assert.NoError(t, ResolveChatFileURLs(nil, schemas.OpenAI, nil))
	})
}

// The reason this moved out of the converter: a fetch that cannot complete - unreachable host,
// non-public address, HTTP error - now reaches the caller. Previously the block silently kept its
// FileURL, MarshalJSON stripped it, and an empty {"type":"file","file":{}} went upstream, so the
// operator debugged a provider 400 about a missing file_id instead of the fetch that failed.
func TestResolveChatFileURLs_ReportsFetchFailure(t *testing.T) {
	req := &OpenAIChatRequest{Messages: []OpenAIMessage{{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
			{
				Type: schemas.ChatContentBlockTypeFile,
				File: &schemas.ChatInputFile{FileURL: schemas.Ptr("https://x.invalid/missing.pdf")},
			},
		}},
	}}}

	err := ResolveChatFileURLs(nil, schemas.OpenAI, req)
	require.Error(t, err, "a failed document fetch must not be silently dropped")
	assert.Contains(t, err.Error(), "missing.pdf",
		"the error should name the document that could not be fetched")
	assert.Contains(t, err.Error(), "messages[0].content[0]",
		"and locate it in the request, since a message can carry several documents")
}

// TestResolveChatFileURLsForwardsUnfetchableSchemes: bifrost inlines only what it can
// download. A scheme it cannot fetch stays on the block and now survives marshalling, so
// the provider answers for itself instead of receiving {"type":"file","file":{}} and
// complaining about a missing file_id while the source is silently gone.
func TestResolveChatFileURLsForwardsUnfetchableSchemes(t *testing.T) {
	for _, rawURL := range []string{"s3://my-bucket/doc.pdf", "gs://my-bucket/doc.pdf"} {
		t.Run(rawURL, func(t *testing.T) {
			req := &OpenAIChatRequest{Messages: []OpenAIMessage{{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{
						Type: schemas.ChatContentBlockTypeFile,
						File: &schemas.ChatInputFile{FileURL: schemas.Ptr(rawURL)},
					},
				}},
			}}}

			require.NoError(t, ResolveChatFileURLs(nil, schemas.OpenAI, req))

			file := req.Messages[0].Content.ContentBlocks[0].File
			require.NotNil(t, file.FileURL)
			assert.Equal(t, rawURL, *file.FileURL, "the reference must survive for the provider to judge")
			assert.Nil(t, file.FileData, "bifrost must not have fetched anything")

			// The whole point: it has to reach the wire, not vanish at marshal time.
			body, err := req.MarshalJSON()
			require.NoError(t, err)
			assert.Contains(t, string(body), rawURL, "file_url must not be silently stripped")
		})
	}
}

// Regression tests for bare-base64 file_data reaching OpenAI-shaped wires with no media type.
//
// Callers may send file_data as bare base64 and name the media type in Bifrost's file_type
// extension. Native converters (Gemini, Anthropic, Bedrock) read file_type, but the OpenAI
// wire has no such field, so MarshalJSON stripped it and shipped bare base64. OpenAI and
// Databricks both require a data URL there, and a Vertex -> Databricks fallback failed with
// "INVALID_PARAMETER_VALUE: Invalid base64 data URL format. Expected format:
// data:{mimeType};base64,...". file_type has to be folded into file_data before it is dropped.

var barePDFBase64 = base64.StdEncoding.EncodeToString([]byte("%PDF-1.2\r\n1 0 obj\r\n<<>>\r\nendobj\r\n"))

func TestFileDataAsDataURL(t *testing.T) {
	plain := "Invoice 42\nTotal: 10 USD"
	cases := []struct {
		name     string
		fileData string
		fileType *string
		want     string
	}{
		{"file_type names the media type", barePDFBase64, schemas.Ptr("application/pdf"), "data:application/pdf;base64," + barePDFBase64},
		{"file_type parameters are dropped", barePDFBase64, schemas.Ptr("Application/PDF; charset=binary"), "data:application/pdf;base64," + barePDFBase64},
		{"short extension file_type resolves", barePDFBase64, schemas.Ptr("pdf"), "data:application/pdf;base64," + barePDFBase64},
		{"no file_type sniffs the bytes", barePDFBase64, nil, "data:application/pdf;base64," + barePDFBase64},
		{"existing data URL is untouched", "data:image/png;base64,iVBORw0KGgo=", schemas.Ptr("application/pdf"), "data:image/png;base64,iVBORw0KGgo="},
		{"plain text file_type is encoded as text", plain, schemas.Ptr("text/plain"), "data:text/plain;base64," + base64.StdEncoding.EncodeToString([]byte(plain))},
		{"txt shorthand is encoded as text", plain, schemas.Ptr("txt"), "data:text/plain;base64," + base64.StdEncoding.EncodeToString([]byte(plain))},
		// text/plain means raw text even when the text happens to be valid base64: it is the same
		// convention the Anthropic converters apply, and guessing would misread real text such as "Hello".
		{"text/plain payload that looks like base64 is still raw text", "SGVsbG8=", schemas.Ptr("text/plain"), "data:text/plain;base64," + base64.StdEncoding.EncodeToString([]byte("SGVsbG8="))},
		{"text/plain word that is valid base64 is still raw text", "Hello", schemas.Ptr("text/plain"), "data:text/plain;base64," + base64.StdEncoding.EncodeToString([]byte("Hello"))},
		{"non-base64 without a text type is left alone", "not base64 at all!", schemas.Ptr("application/pdf"), "not base64 at all!"},
		{"empty stays empty", "", schemas.Ptr("application/pdf"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, fileDataAsDataURL(tc.fileData, tc.fileType))
		})
	}
}

func TestChatMarshalFoldsFileTypeIntoFileData(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fileType *string
	}{
		{"with file_type", schemas.Ptr("application/pdf")},
		{"without file_type", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := &schemas.ChatInputFile{
				FileData: schemas.Ptr(barePDFBase64),
				Filename: schemas.Ptr("invoice.pdf"),
				FileType: tc.fileType,
			}
			req := &OpenAIChatRequest{Model: "databricks-gemini-3-8-flash", Messages: []OpenAIMessage{{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("Extract the bill.")},
					{Type: schemas.ChatContentBlockTypeFile, File: file},
				}},
			}}}

			body, err := req.MarshalJSON()
			require.NoError(t, err)

			wire := gjson.GetBytes(body, "messages.0.content.1.file")
			assert.Equal(t, "data:application/pdf;base64,"+barePDFBase64, wire.Get("file_data").String(),
				"the OpenAI wire has no file_type, so the media type must travel in file_data")
			assert.False(t, wire.Get("file_type").Exists(), "file_type is a Bifrost extension and must not reach the wire")
			assert.Equal(t, barePDFBase64, *file.FileData, "the caller's request is reused by fallbacks and must not change")
		})
	}
}

func TestResponsesMarshalFoldsFileTypeIntoFileData(t *testing.T) {
	file := &schemas.ResponsesInputMessageContentBlockFile{
		FileData: schemas.Ptr(barePDFBase64),
		Filename: schemas.Ptr("invoice.pdf"),
		FileType: schemas.Ptr("application/pdf"),
	}
	input := OpenAIResponsesRequestInput{OpenAIResponsesRequestInputArray: []schemas.ResponsesMessage{{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
			Type:                                  schemas.ResponsesInputMessageContentBlockTypeFile,
			ResponsesInputMessageContentBlockFile: file,
		}}},
	}}}

	body, err := input.MarshalJSON()
	require.NoError(t, err)

	wire := gjson.GetBytes(body, "0.content.0")
	assert.Equal(t, "data:application/pdf;base64,"+barePDFBase64, wire.Get("file_data").String())
	assert.False(t, wire.Get("file_type").Exists())
	assert.Equal(t, barePDFBase64, *file.FileData, "the caller's request must not change")
}
