package warp

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// A turn that failed is recorded with an error and no text, so the next request
// replays it as an assistant message with empty content. Anthropic rejects that
// outright - "messages: text content blocks must be non-empty" - which means one
// failed turn poisons the thread: every retry fails on the previous failure
// rather than on anything the retry did.
func TestWarpConversationDropsEmptyTurns(t *testing.T) {
	converted, err := Conversation([]ChatMessage{
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
func TestWarpConversationDropsWhitespaceOnlyTurns(t *testing.T) {
	converted, err := Conversation([]ChatMessage{
		{Role: "user", Content: "   \n\t "},
		{Role: "user", Content: "real question"},
	})
	require.NoError(t, err)
	require.Len(t, converted, 1)
	require.Equal(t, "real question", *converted[0].Content.ContentStr)
}

// Dropping empties must not be able to empty the whole conversation.
func TestWarpConversationRejectsAllEmpty(t *testing.T) {
	_, err := Conversation([]ChatMessage{{Role: "user", Content: "  "}})
	require.ErrorIs(t, err, ErrEmptyConversation)
}

// A blank final turn is not the same as a blank historical one. History is
// replayed, so an empty entry there is noise worth dropping; the final turn is
// the question being asked, and dropping it leaves the agent answering the
// previous message while the transcript files a blank bubble beside it under a
// blank title.
func TestWarpConversationRejectsAnEmptyFinalTurn(t *testing.T) {
	for name, messages := range map[string][]ChatMessage{
		"whitespace only": {
			{Role: "user", Content: "how much did we spend?"},
			{Role: "assistant", Content: "$412."},
			{Role: "user", Content: "   "},
		},
		"empty": {
			{Role: "user", Content: "how much did we spend?"},
			{Role: "user", Content: ""},
		},
		"newlines only": {
			{Role: "user", Content: "how much did we spend?"},
			{Role: "user", Content: "\n\t\n"},
		},
	} {
		_, err := Conversation(messages)
		require.ErrorIs(t, err, ErrEmptyFinalTurn, name)
	}

	// A blank turn in the middle is still dropped rather than rejected: it
	// carries no information, and a failed turn comes back exactly that way.
	converted, err := Conversation([]ChatMessage{
		{Role: "user", Content: "how much did we spend?"},
		{Role: "assistant", Content: "   "},
		{Role: "user", Content: "and last week?"},
	})
	require.NoError(t, err)
	require.Len(t, converted, 2, "the blank middle turn is dropped, the real ones survive")
}

// The cap is on what gets replayed, and empty turns are not replayed - a failed
// turn comes back as an assistant message with no text. Trimming before they
// are removed spends the budget on entries that are then dropped, so a thread
// with several failures loses real turns it had room for.
func TestWarpConversationTrimsAfterDroppingEmptyTurns(t *testing.T) {
	messages := []ChatMessage{{Role: "user", Content: "opening question"}}
	// Real turns first, then a run of failed ones inside the tail the trim keeps,
	// then the question being asked. Trimming before the blanks are removed
	// spends the budget on them and discards real turns there was room for.
	for i := range 30 {
		messages = append(messages, ChatMessage{Role: "user", Content: fmt.Sprintf("q%d", i)})
	}
	for range 10 {
		messages = append(messages, ChatMessage{Role: "assistant", Content: ""})
	}
	for i := 30; i < MaxHistoryMessages; i++ {
		messages = append(messages, ChatMessage{Role: "user", Content: fmt.Sprintf("q%d", i)})
	}

	converted, err := Conversation(messages)
	require.NoError(t, err)
	require.Len(t, converted, MaxHistoryMessages, "the budget is spent on turns that are actually replayed")

	// The opening turn is still kept, since it carries the framing.
	require.Equal(t, "opening question", *converted[0].Content.ContentStr)

	// And the most recent turn survives, which is the question being asked.
	last := converted[len(converted)-1]
	require.Equal(t, fmt.Sprintf("q%d", MaxHistoryMessages-1), *last.Content.ContentStr)
}

// A failed turn comes back as an assistant message with empty content, and
// Conversation drops it before the model ever sees it. Counting it as a real
// answer reset the streak against a message that was never shown, so the agent
// could ask past MaxConsecutiveQuestions just by failing in between.
func TestWarpConsecutiveQuestionsIgnoresDroppedEmptyTurns(t *testing.T) {
	ask := func() ChatMessage { return ChatMessage{Role: "assistant", Content: "which window?", Question: true} }
	failed := ChatMessage{Role: "assistant", Content: ""}
	blank := ChatMessage{Role: "assistant", Content: "   "}
	user := ChatMessage{Role: "user", Content: "last week"}

	require.Equal(t, 2, consecutiveQuestions([]ChatMessage{ask(), user, failed, ask()}),
		"an empty turn between two questions is dropped on replay, so it breaks nothing")
	require.Equal(t, 2, consecutiveQuestions([]ChatMessage{ask(), user, blank, ask()}))

	// A genuine answer still ends the streak.
	require.Equal(t, 1, consecutiveQuestions([]ChatMessage{
		ask(), user, {Role: "assistant", Content: "you spent $12"}, user, ask(),
	}))
	require.Zero(t, consecutiveQuestions([]ChatMessage{{Role: "assistant", Content: "you spent $12"}}))
}
