package logging

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExtractInputHistoryDecisionInput pins how a decision input is logged:
// text and structured input as one user message, as a state always was, a null
// input as "null", and messages like a chat request, with their text and image
// parts and an unmodelled part kept as its type name. The payloads are
// synthetic.
func TestExtractInputHistoryDecisionInput(t *testing.T) {
	p := &LoggerPlugin{}
	history := func(input schemas.DecisionInput) []schemas.ChatMessage {
		t.Helper()
		messages, _ := p.extractInputHistory(&schemas.BifrostRequest{DecisionRequest: &schemas.BifrostDecisionRequest{Input: input}})
		return messages
	}

	text := history(schemas.DecisionInput{Text: schemas.Ptr("I was charged twice.")})
	require.Len(t, text, 1)
	assert.Equal(t, schemas.ChatMessageRoleUser, text[0].Role)
	assert.Equal(t, "I was charged twice.", *text[0].Content.ContentStr)

	structured := history(schemas.DecisionInput{Structured: map[string]any{"ticket": "refund"}})
	require.Len(t, structured, 1)
	assert.JSONEq(t, `{"ticket":"refund"}`, *structured[0].Content.ContentStr)

	null := history(schemas.DecisionInput{})
	require.Len(t, null, 1)
	assert.Equal(t, "null", *null[0].Content.ContentStr)

	var unknownPart schemas.DecisionInputPart
	require.NoError(t, schemas.Unmarshal([]byte(`{"type":"input_audio","audio":{"data":"AAAA"}}`), &unknownPart))
	messages := history(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
		{Role: "user", Content: schemas.DecisionInputContent{Text: schemas.Ptr("first")}},
		{Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
			{Type: schemas.DecisionInputPartTypeText, Text: schemas.NewDecisionText("look")},
			{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("data:image/png;base64,AAAA")},
			unknownPart,
			{Type: schemas.DecisionInputPartTypeText, Text: &schemas.DecisionText{Structured: map[string]any{"camera": "r-cam"}}},
			{Type: schemas.DecisionInputPartTypeImage, ID: schemas.Ptr("r-cam"), Data: schemas.DecisionData{File: []byte("\x89PNG\r\n\x1a\nCAM")}},
		}}},
	}})
	require.Len(t, messages, 2)
	assert.Equal(t, "first", *messages[0].Content.ContentStr)
	assert.Equal(t, schemas.ChatMessageRoleUser, messages[1].Role, "a message without a role is logged as the user's")
	blocks := messages[1].Content.ContentBlocks
	require.Len(t, blocks, 5)
	assert.Equal(t, "look", *blocks[0].Text)
	assert.Equal(t, "data:image/png;base64,AAAA", blocks[1].ImageURLStruct.URL)
	assert.Equal(t, "[input_audio]", *blocks[2].Text)
	assert.Equal(t, `{"camera":"r-cam"}`, *blocks[3].Text, "a structured text part is logged as JSON")
	assert.Equal(t, "data:image/png;base64,iVBORw0KGgpDQU0=", blocks[4].ImageURLStruct.URL, "an image sent by reference is logged as an image")

	files := history(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{Role: "user", Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
		{Type: schemas.DecisionInputPartTypeFile, MimeType: schemas.Ptr("video/mp4"), Name: schemas.Ptr("clip"), Data: schemas.DecisionData{File: []byte("MP4-BYTES")}},
		{Type: schemas.DecisionInputPartTypeFile, MimeType: schemas.Ptr("audio/mpeg"), URL: schemas.Ptr("https://example.com/call.mp3")},
		{Type: schemas.DecisionInputPartTypeFile, MimeType: schemas.Ptr("application/pdf"), Data: schemas.DecisionData{File: []byte("%PDF")}},
	}}}}})
	require.Len(t, files, 1)
	fileBlocks := files[0].Content.ContentBlocks
	require.Len(t, fileBlocks, 3)
	for _, block := range fileBlocks {
		assert.Equal(t, schemas.ChatContentBlockTypeFile, block.Type, "a file is logged as a chat file block")
		require.NotNil(t, block.File)
	}
	assert.Equal(t, schemas.ChatInputFile{FileData: schemas.Ptr("TVA0LUJZVEVT"), Filename: schemas.Ptr("clip"), FileType: schemas.Ptr("video/mp4")}, *fileBlocks[0].File, "a multipart file keeps its bytes and part name")
	assert.Equal(t, schemas.ChatInputFile{FileURL: schemas.Ptr("https://example.com/call.mp3"), FileType: schemas.Ptr("audio/mpeg")}, *fileBlocks[1].File, "a file by URL is logged as its URL")
	assert.Equal(t, schemas.ChatInputFile{FileData: schemas.Ptr("JVBERg=="), FileType: schemas.Ptr("application/pdf")}, *fileBlocks[2].File)
}
