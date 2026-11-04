package promptcompression

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
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

// chatTurnBounds returns, for each assistant message, the index of that
// message: the request for turn k is msgs[:bounds[k]], as a real agent loop
// would send it just before the model answers.
func chatTurnBounds(msgs []schemas.ChatMessage) []int {
	var b []int
	for i, m := range msgs {
		if m.Role == schemas.ChatMessageRoleAssistant {
			b = append(b, i)
		}
	}
	return b
}

// responsesTurnBounds is the Responses counterpart: a turn starts at an
// assistant message or at the first function_call of a parallel run.
func responsesTurnBounds(msgs []schemas.ResponsesMessage) []int {
	var b []int
	for i, m := range msgs {
		isCall := m.Type != nil && *m.Type == schemas.ResponsesMessageTypeFunctionCall
		isAssistant := m.Role != nil && *m.Role == schemas.ResponsesInputMessageRoleAssistant
		if !isCall && !isAssistant {
			continue
		}
		prevCall := i > 0 && msgs[i-1].Type != nil && *msgs[i-1].Type == schemas.ResponsesMessageTypeFunctionCall
		if isCall && prevCall {
			continue
		}
		b = append(b, i)
	}
	return b
}

type turnRow struct{ turn, messages, before, after int }

func (r turnRow) pct() float64 {
	if r.before == 0 {
		return 0
	}
	return 100 * float64(r.before-r.after) / float64(r.before)
}

// TestFixturePerTurnEstimates computes the per-turn estimated token cost of
// each request prefix with and without compression (default settings). When
// PROMPTCOMPRESSION_WRITE_ESTIMATES=1 it writes benchmark/estimates.md.
func TestFixturePerTurnEstimates(t *testing.T) {
	s := defaults(t)

	var chat []schemas.ChatMessage
	loadFixture(t, "testdata/agent_session_chat.json", &chat)
	var chatRows []turnRow
	for k, end := range chatTurnBounds(chat) {
		prefix := chat[:end]
		out := compressChat(prefix, s)
		require.Len(t, out, len(prefix), "chat turn %d: message count changed", k+1)
		r := turnRow{k + 1, len(prefix), estimateChat(prefix), estimateChat(out)}
		require.LessOrEqual(t, r.after, r.before, "chat turn %d", k+1)
		t.Logf("chat turn %2d: msgs=%2d before=%5d after=%5d saved=%5.1f%%", r.turn, r.messages, r.before, r.after, r.pct())
		chatRows = append(chatRows, r)
	}
	require.NotEmpty(t, chatRows)

	var resp []schemas.ResponsesMessage
	loadFixture(t, "testdata/agent_session_responses.json", &resp)
	var respRows []turnRow
	for k, end := range responsesTurnBounds(resp) {
		prefix := resp[:end]
		out := compressResponses(prefix, s)
		require.Len(t, out, len(prefix), "responses turn %d: item count changed", k+1)
		r := turnRow{k + 1, len(prefix), estimateResponses(prefix), estimateResponses(out)}
		require.LessOrEqual(t, r.after, r.before, "responses turn %d", k+1)
		t.Logf("responses turn %2d: items=%2d before=%5d after=%5d saved=%5.1f%%", r.turn, r.messages, r.before, r.after, r.pct())
		respRows = append(respRows, r)
	}
	require.NotEmpty(t, respRows)

	if os.Getenv("PROMPTCOMPRESSION_WRITE_ESTIMATES") != "1" {
		return
	}
	var sb strings.Builder
	sb.WriteString("# Per-turn token estimates\n\n")
	sb.WriteString("Estimates: chars÷4 on the synthetic fixture, not provider-reported usage; cache effects not modelled.\n\n")
	section := func(title, unit string, rows []turnRow) {
		var tb, ta int
		var sum float64
		fmt.Fprintf(&sb, "## %s\n\n| Turn | %s | Estimated tokens before | Estimated tokens after | %% saved |\n|---:|---:|---:|---:|---:|\n", title, unit)
		for _, r := range rows {
			fmt.Fprintf(&sb, "| %d | %d | %d | %d | %.1f%% |\n", r.turn, r.messages, r.before, r.after, r.pct())
			tb += r.before
			ta += r.after
			sum += r.pct()
		}
		fmt.Fprintf(&sb, "| **Total** | | **%d** | **%d** | **%.1f%%** |\n\n", tb, ta, 100*float64(tb-ta)/float64(tb))
		fmt.Fprintf(&sb, "Mean per-turn %% saved: **%.1f%%**\n\n", sum/float64(len(rows)))
	}
	section("Chat Completions", "Messages", chatRows)
	section("Responses", "Items", respRows)
	require.NoError(t, os.MkdirAll("benchmark", 0o755))
	require.NoError(t, os.WriteFile("benchmark/estimates.md", []byte(sb.String()), 0o644))
}
