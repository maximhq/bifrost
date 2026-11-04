package promptcompression

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// maxFixtureRatio is the headline target: the replayed agent session must
// shrink to at most 70% of its estimated tokens (at least 30% saved).
const maxFixtureRatio = 0.70

func loadFixture(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, v))
}

// TestFixtureChatSavings replays a recorded 12-round coding-agent session in
// Chat Completions shape and checks the default settings save at least 30%.
func TestFixtureChatSavings(t *testing.T) {
	var msgs []schemas.ChatMessage
	loadFixture(t, "testdata/agent_session_chat.json", &msgs)
	require.NotEmpty(t, msgs)

	s := defaults(t)
	inBefore, err := json.Marshal(msgs)
	require.NoError(t, err)

	out := compressChat(msgs, s)

	inAfter, err := json.Marshal(msgs)
	require.NoError(t, err)
	require.Equal(t, string(inBefore), string(inAfter), "input mutated")
	assertChatInvariants(t, msgs, out, chatProtectedPrefix(msgs, s.ProtectedLeading))

	before, after := estimateChat(msgs), estimateChat(out)
	t.Logf("chat fixture: %d -> %d tokens, saved %.1f%%", before, after, 100*float64(before-after)/float64(before))
	require.LessOrEqual(t, float64(after), maxFixtureRatio*float64(before))
}

// TestFixtureResponsesSavings is the Responses-shape counterpart.
func TestFixtureResponsesSavings(t *testing.T) {
	var msgs []schemas.ResponsesMessage
	loadFixture(t, "testdata/agent_session_responses.json", &msgs)
	require.NotEmpty(t, msgs)

	s := defaults(t)
	inBefore, err := json.Marshal(msgs)
	require.NoError(t, err)

	out := compressResponses(msgs, s)

	inAfter, err := json.Marshal(msgs)
	require.NoError(t, err)
	require.Equal(t, string(inBefore), string(inAfter), "input mutated")
	assertResponsesInvariants(t, msgs, out, responsesProtectedPrefix(msgs, s.ProtectedLeading))

	before, after := estimateResponses(msgs), estimateResponses(out)
	t.Logf("responses fixture: %d -> %d tokens, saved %.1f%%", before, after, 100*float64(before-after)/float64(before))
	require.LessOrEqual(t, float64(after), maxFixtureRatio*float64(before))
}
