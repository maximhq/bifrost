package handlers

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A turn that failed is recorded with an error and no text, so the next request
// replays it as an assistant message with empty content. Anthropic rejects that
// outright - "messages: text content blocks must be non-empty" - which means one
// failed turn poisons the thread: every retry fails on the previous failure
// rather than on anything the retry did.
func TestOdinConversationDropsEmptyTurns(t *testing.T) {
	converted, err := odinConversation([]odinChatMessage{
		{Role: "user", Content: "what is failing?"},
		{Role: "assistant", Content: ""},
		{Role: "user", Content: "retry"},
	})
	require.NoError(t, err)
	require.Len(t, converted, 2, "the empty assistant turn must not be replayed")
	for _, message := range converted {
		require.NotNil(t, message.Content)
		require.NotEmpty(t, *message.Content.ContentStr)
	}
}

// Whitespace is not content either: a message of spaces serialises to a text
// block the provider still considers empty.
func TestOdinConversationDropsWhitespaceOnlyTurns(t *testing.T) {
	converted, err := odinConversation([]odinChatMessage{
		{Role: "user", Content: "   \n\t "},
		{Role: "user", Content: "real question"},
	})
	require.NoError(t, err)
	require.Len(t, converted, 1)
	require.Equal(t, "real question", *converted[0].Content.ContentStr)
}

// Dropping empties must not be able to empty the whole conversation.
func TestOdinConversationRejectsAllEmpty(t *testing.T) {
	_, err := odinConversation([]odinChatMessage{{Role: "user", Content: "  "}})
	require.ErrorIs(t, err, errOdinEmptyConversation)
}
