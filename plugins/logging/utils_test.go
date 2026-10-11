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
			{Type: schemas.DecisionInputPartTypeText, Text: schemas.Ptr("look")},
			{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("data:image/png;base64,AAAA")},
			unknownPart,
		}}},
	}})
	require.Len(t, messages, 2)
	assert.Equal(t, "first", *messages[0].Content.ContentStr)
	assert.Equal(t, schemas.ChatMessageRoleUser, messages[1].Role, "a message without a role is logged as the user's")
	blocks := messages[1].Content.ContentBlocks
	require.Len(t, blocks, 3)
	assert.Equal(t, "look", *blocks[0].Text)
	assert.Equal(t, "data:image/png;base64,AAAA", blocks[1].ImageURLStruct.URL)
	assert.Equal(t, "[input_audio]", *blocks[2].Text)
}
