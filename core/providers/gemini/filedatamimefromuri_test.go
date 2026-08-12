package gemini

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for the Vertex "empty mimeType parameter in fileData" 400.
//
// The harness sent BYTE-IDENTICAL bodies to gemini/gemini-2.5-flash and vertex/gemini-2.5-flash -
// a PDF referenced by URL through the /openai dialect - and only vertex failed:
//
//	"Unable to submit request because it has an empty mimeType parameter in fileData."
//
// Bifrost synthesises the fileData part itself here, and left mimeType unset because the OpenAI
// dialect carries no explicit file type. The Gemini API infers it; Vertex refuses to.
//
// The fix reads the type the URI already states, and ONLY that. It is deliberately not a default:
// filedata_mime_test.go pins an earlier bug where Bifrost stamped every typeless fileData as
// application/pdf, which then propagated upstream as a lie about the content. An extensionless URI
// (the Files API form, https://.../v1beta/files/abc) still yields no mimeType, which is what that
// test asserts and what these tests re-check from the other direction.

func TestMimeTypeFromURI_DerivesOnlyFromAKnownExtension(t *testing.T) {
	cases := []struct {
		uri  string
		want string
		why  string
	}{
		{"https://www.berkshirehathaway.com/letters/2024ltr.pdf", "application/pdf", "the failing harness row"},
		{"https://example.com/a/b/report.PDF", "application/pdf", "extension match is case-insensitive"},
		{"https://example.com/img.png", "image/png", ""},
		{"https://example.com/img.jpg", "image/jpeg", ""},
		{"https://example.com/clip.mp3", "audio/mpeg", ""},
		{"https://example.com/data.csv", "text/csv", "no charset parameter on the wire"},
		{"https://example.com/notes.txt", "text/plain", ""},
		{"https://example.com/movie.mp4", "video/mp4", ""},
		{"https://example.com/report.pdf?sig=abc&x=1", "application/pdf", "a query string must not defeat the match"},
		{"https://example.com/report.pdf#page=2", "application/pdf", "nor a fragment"},

		// Everything below must stay empty: guessing here is the bug this fix must not reintroduce.
		{"https://generativelanguage.googleapis.com/v1beta/files/abc", "", "Files API URI has no extension"},
		{"https://example.com/download", "", "no extension at all"},
		{"https://example.com/archive.xyz", "", "unknown extension is not a licence to guess"},
		{"gs://bucket/object", "", "GCS URI without an extension"},
		{"", "", "empty URI"},
		{"https://example.com/v1.2/files/abc", "", "a dot in a path segment is not an extension"},
	}

	for _, c := range cases {
		got := mimeTypeFromURI(c.uri)
		assert.Equal(t, c.want, got, "uri=%q %s", c.uri, c.why)
	}
}

// The chat-completions synthesis site (utils.go) - the /openai/v1/chat/completions -> vertex path.
func TestConvertBifrostMessagesToGemini_DerivesFileMimeFromURL(t *testing.T) {
	pdf := "https://www.berkshirehathaway.com/letters/2024ltr.pdf"
	msgs := []schemas.ChatMessage{{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{{
				Type: schemas.ChatContentBlockTypeFile,
				File: &schemas.ChatInputFile{FileURL: &pdf},
			}},
		},
	}}

	contents, _, err := convertBifrostMessagesToGemini(msgs)
	require.NoError(t, err)
	require.NotEmpty(t, contents)
	require.NotEmpty(t, contents[0].Parts)
	require.NotNil(t, contents[0].Parts[0].FileData)
	assert.Equal(t, "application/pdf", contents[0].Parts[0].FileData.MIMEType,
		"a .pdf URL must reach Vertex with its mimeType set")
	assert.Equal(t, pdf, contents[0].Parts[0].FileData.FileURI)
}

// An explicit FileType from the caller always wins over anything derived from the URI.
func TestConvertBifrostMessagesToGemini_CallerFileTypeWins(t *testing.T) {
	pdf := "https://example.com/report.pdf"
	declared := "application/x-custom"
	msgs := []schemas.ChatMessage{{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{{
				Type: schemas.ChatContentBlockTypeFile,
				File: &schemas.ChatInputFile{FileURL: &pdf, FileType: &declared},
			}},
		},
	}}

	contents, _, err := convertBifrostMessagesToGemini(msgs)
	require.NoError(t, err)
	require.NotEmpty(t, contents)
	require.NotNil(t, contents[0].Parts[0].FileData)
	assert.Equal(t, declared, contents[0].Parts[0].FileData.MIMEType)
}

// The responses synthesis site (responses.go) - the /openai/v1/responses -> vertex path.
func TestConvertContentBlockToGeminiPart_DerivesFileMimeFromURL(t *testing.T) {
	pdf := "https://www.berkshirehathaway.com/letters/2024ltr.pdf"
	part, err := convertContentBlockToGeminiPart(schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesInputMessageContentBlockTypeFile,
		ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
			FileURL: &pdf,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, part.FileData)
	assert.Equal(t, "application/pdf", part.FileData.MIMEType)
}

// The extensionless Files API URI must still produce no mimeType, which is the invariant
// filedata_mime_test.go exists to protect.
func TestConvertContentBlockToGeminiPart_StillOmitsMimeForExtensionlessURI(t *testing.T) {
	part, err := convertContentBlockToGeminiPart(schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesInputMessageContentBlockTypeFile,
		ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
			FileURL: sptr(testFileURI),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, part.FileData)
	assert.Empty(t, part.FileData.MIMEType, "no extension means no guess")
}
