package logging

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExtractInputHistoryDecisionState pins how a decision state is logged: a
// text or structured state as one user message, as before, and a state given
// as messages like a chat request, with its text and image parts and an
// unmodelled part kept as its type name. The payloads are synthetic.
func TestExtractInputHistoryDecisionState(t *testing.T) {
	p := &LoggerPlugin{}
	history := func(state interface{}) []schemas.ChatMessage {
		t.Helper()
		messages, _ := p.extractInputHistory(&schemas.BifrostRequest{DecisionRequest: &schemas.BifrostDecisionRequest{State: state}})
		return messages
	}

	text := history("I was charged twice.")
	require.Len(t, text, 1)
	assert.Equal(t, "I was charged twice.", *text[0].Content.ContentStr)

	structured := history(map[string]any{"ticket": "refund"})
	require.Len(t, structured, 1)
	assert.JSONEq(t, `{"ticket":"refund"}`, *structured[0].Content.ContentStr)

	var unknownPart schemas.DecisionInputPart
	require.NoError(t, schemas.Unmarshal([]byte(`{"type":"input_audio","audio":{"data":"AAAA"}}`), &unknownPart))
	messages := history([]schemas.DecisionInputMessage{
		{Role: "user", Content: schemas.DecisionInputContent{Text: schemas.Ptr("first")}},
		{Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
			{Type: schemas.DecisionInputPartTypeText, Text: schemas.Ptr("look")},
			{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("data:image/png;base64,AAAA")},
			unknownPart,
		}}},
	})
	require.Len(t, messages, 2)
	assert.Equal(t, "first", *messages[0].Content.ContentStr)
	assert.Equal(t, schemas.ChatMessageRoleUser, messages[1].Role, "a message without a role is logged as the user's")
	blocks := messages[1].Content.ContentBlocks
	require.Len(t, blocks, 3)
	assert.Equal(t, "look", *blocks[0].Text)
	assert.Equal(t, "data:image/png;base64,AAAA", blocks[1].ImageURLStruct.URL)
	assert.Equal(t, "[input_audio]", *blocks[2].Text)
}
