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
			{Type: schemas.DecisionInputPartTypeImage, ID: schemas.Ptr("r-cam"), Data: []byte("\x89PNG\r\n\x1a\nCAM")},
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
}

// TestExtractInputHistoryExtractionInput pins how an extraction input is logged:
// text alone as one user message, and with images as a text block (only when
// text was sent) followed by one image block per image, in either image form.
// The payloads are synthetic.
func TestExtractInputHistoryExtractionInput(t *testing.T) {
	p := &LoggerPlugin{}
	history := func(request *schemas.BifrostExtractionRequest) []schemas.ChatMessage {
		t.Helper()
		messages, _ := p.extractInputHistory(&schemas.BifrostRequest{ExtractionRequest: request})
		return messages
	}

	text := history(&schemas.BifrostExtractionRequest{Input: "Invoice INV-2291, net 30."})
	require.Len(t, text, 1)
	assert.Equal(t, schemas.ChatMessageRoleUser, text[0].Role)
	assert.Equal(t, "Invoice INV-2291, net 30.", *text[0].Content.ContentStr)

	withImages := history(&schemas.BifrostExtractionRequest{
		Input:  "Receipt",
		Images: []schemas.ExtractionImage{{URL: schemas.Ptr("data:image/png;base64,AAAA")}, {ContentType: "image/jpeg", Base64: "BBBB"}},
	})
	require.Len(t, withImages, 1)
	blocks := withImages[0].Content.ContentBlocks
	require.Len(t, blocks, 3)
	assert.Equal(t, "Receipt", *blocks[0].Text)
	assert.Equal(t, "data:image/png;base64,AAAA", blocks[1].ImageURLStruct.URL)
	assert.Equal(t, "data:image/jpeg;base64,BBBB", blocks[2].ImageURLStruct.URL, "a base64 object is logged as a data URL")

	imageOnly := history(&schemas.BifrostExtractionRequest{Images: []schemas.ExtractionImage{{URL: schemas.Ptr("data:image/png;base64,AAAA")}}})
	require.Len(t, imageOnly, 1)
	require.Len(t, imageOnly[0].Content.ContentBlocks, 1, "an image-only extraction logs its image, not an empty message")
	assert.Equal(t, schemas.ChatContentBlockTypeImage, imageOnly[0].Content.ContentBlocks[0].Type)
}

// An extraction row logs its settings with its fields: two requests with the same
// input and fields but different reference dates must not log the same params.
func TestExtractionLogParamsKeepRequestSettings(t *testing.T) {
	fields := map[string]schemas.ExtractionFieldSpec{"due_date": {Type: schemas.ExtractionFieldTypeDate}}
	encoded, err := schemas.Marshal(extractionLogParams{Fields: fields, Params: &schemas.ExtractionParameters{ReferenceDate: schemas.Ptr("2026-09-02")}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"fields":{"due_date":{"type":"date"}},"params":{"reference_date":"2026-09-02"}}`, string(encoded))

	encoded, err = schemas.Marshal(extractionLogParams{Fields: fields})
	require.NoError(t, err)
	assert.JSONEq(t, `{"fields":{"due_date":{"type":"date"}}}`, string(encoded))
}
