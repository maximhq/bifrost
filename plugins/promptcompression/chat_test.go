package promptcompression

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func strp(s string) *string { return &s }

func textMsg(role schemas.ChatMessageRole, text string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: &text}}
}

func assistantCall(id string) schemas.ChatMessage {
	m := textMsg(schemas.ChatMessageRoleAssistant, "calling")
	m.ChatAssistantMessage = &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{{ID: strp(id)}}}
	return m
}

func toolMsg(id, text string) schemas.ChatMessage {
	m := textMsg(schemas.ChatMessageRoleTool, text)
	m.ChatToolMessage = &schemas.ChatToolMessage{ToolCallID: strp(id)}
	return m
}

// bigOutput builds n numbered lines of about 40 bytes each.
func bigOutput(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %04d: some tool output content here", i)
	}
	return strings.Join(lines, "\n")
}

func strOf(m schemas.ChatMessage) string { return *m.Content.ContentStr }

// run compresses msgs with compressChat and checks that the input JSON is unchanged
// afterwards and that the structural invariants hold.
func run(t *testing.T, msgs []schemas.ChatMessage, s settings) []schemas.ChatMessage {
	t.Helper()
	before, err := json.Marshal(msgs)
	require.NoError(t, err)
	out := compressChat(msgs, s)
	after, err := json.Marshal(msgs)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "input mutated")
	assertChatInvariants(t, msgs, out, chatProtectedPrefix(msgs, s.ProtectedLeading))
	return out
}

func defaults(t *testing.T) settings {
	s, err := (&Config{}).resolve()
	require.NoError(t, err)
	return s
}

// rounds builds system, user, then assistant/tool rounds with given outputs.
func rounds(outputs ...string) []schemas.ChatMessage {
	msgs := []schemas.ChatMessage{textMsg(schemas.ChatMessageRoleSystem, "sys"), textMsg(schemas.ChatMessageRoleUser, "go")}
	for i, o := range outputs {
		id := fmt.Sprintf("call_%d", i+1)
		msgs = append(msgs, assistantCall(id), toolMsg(id, o))
	}
	return msgs
}

func TestChatTrimsOldToolOutput(t *testing.T) {
	big := bigOutput(200)
	last := bigOutput(150)
	msgs := rounds(big, "small", last)
	out := run(t, msgs, defaults(t))
	require.Contains(t, strOf(out[3]), "tokens trimmed by promptcompression")
	require.Less(t, len(strOf(out[3])), len(big))
	require.Equal(t, last, strOf(out[7]), "age 0 untouched")
}

func TestChatKeepsYoungToolOutput(t *testing.T) {
	msgs := rounds(bigOutput(200), "x") // round 1 has age 1
	out := run(t, msgs, defaults(t))
	require.Equal(t, bigOutput(200), strOf(out[3]))
}

func TestChatParallelToolCallsSameAge(t *testing.T) {
	a := assistantCall("a")
	a.ChatAssistantMessage.ToolCalls = append(a.ChatAssistantMessage.ToolCalls,
		schemas.ChatAssistantMessageToolCall{ID: strp("b")}, schemas.ChatAssistantMessageToolCall{ID: strp("c")})
	msgs := []schemas.ChatMessage{
		textMsg(schemas.ChatMessageRoleSystem, "sys"), textMsg(schemas.ChatMessageRoleUser, "go"),
		a, toolMsg("a", bigOutput(200)), toolMsg("b", bigOutput(201)), toolMsg("c", bigOutput(202)),
		assistantCall("d"), toolMsg("d", "ok"),
		assistantCall("e"), toolMsg("e", "ok"),
	}
	out := run(t, msgs, defaults(t))
	for i := 3; i <= 5; i++ {
		require.Contains(t, strOf(out[i]), "tokens trimmed", "message %d", i)
	}
}

func TestChatDedupe(t *testing.T) {
	file := bigOutput(50)
	msgs := rounds(file, "other", file)
	s := defaults(t)
	s.MinAgeTurns = 99 // isolate dedupe from trimming
	out := run(t, msgs, s)
	require.Equal(t, "[identical to the output of call call_3]", strOf(out[3]))
	require.Equal(t, file, strOf(out[7]))
	require.Equal(t, "other", strOf(out[5]))
}

func TestChatDedupeSkipsWhenNotShorter(t *testing.T) {
	msgs := rounds("same", "same")
	out := run(t, msgs, defaults(t))
	require.Equal(t, "same", strOf(out[3]))
}

func TestChatDedupeDisabled(t *testing.T) {
	file := bigOutput(50)
	msgs := rounds(file, "other", file)
	s := defaults(t)
	s.Dedupe = false
	s.MinAgeTurns = 99
	out := run(t, msgs, s)
	require.Equal(t, file, strOf(out[3]))
}

func TestChatWhitespaceOnlyOldToolOutputs(t *testing.T) {
	const padded = "a   \nb  \n\n\n\n\nc"
	user := textMsg(schemas.ChatMessageRoleUser, padded)
	msgs := []schemas.ChatMessage{textMsg(schemas.ChatMessageRoleSystem, "sys  "), user}
	// tool outputs at 3, 5, 7 (plus the one after asst): ages 3, 2, 1, 0
	msgs = append(msgs, rounds(padded, padded, padded)[2:]...)
	asst := assistantCall("x")
	asst.Content.ContentStr = strp(padded)
	msgs = append(msgs, asst, toolMsg("x", "ok"))
	s := defaults(t)
	s.Dedupe = false
	out := run(t, msgs, s)
	require.Equal(t, "sys  ", strOf(out[0]), "system untouched")
	require.Equal(t, padded, strOf(out[1]), "user untouched")
	require.Equal(t, "a\nb\n\nc", strOf(out[3]), "old (age 3) tool output normalised")
	require.Equal(t, "a\nb\n\nc", strOf(out[5]), "old (age 2) tool output normalised")
	require.Equal(t, padded, strOf(out[7]), "young (age 1) tool output unchanged")
	require.Equal(t, padded, strOf(out[8]), "assistant untouched")
	s.Whitespace = false
	require.Equal(t, padded, strOf(run(t, msgs, s)[3]), "disabled")
}

func TestChatDedupeChainsToNextCopy(t *testing.T) {
	file := bigOutput(50)
	s := defaults(t)
	s.MinAgeTurns = 99
	three := run(t, rounds(file, file, file), s)
	require.Equal(t, "[identical to the output of call call_2]", strOf(three[3]))
	require.Equal(t, "[identical to the output of call call_3]", strOf(three[5]))
	require.Equal(t, file, strOf(three[7]))
	four := run(t, rounds(file, file, file, file), s)
	require.Equal(t, strOf(three[3]), strOf(four[3]), "existing stub unchanged when a copy is appended")
	require.Equal(t, strOf(three[5]), strOf(four[5]))
	require.Equal(t, "[identical to the output of call call_4]", strOf(four[7]))
	require.Equal(t, file, strOf(four[9]))
}

func TestChatProtectedPrefixUntouched(t *testing.T) {
	msgs := rounds(bigOutput(200), "a", "b", "c")
	cc := &schemas.CacheControl{Type: "ephemeral"}
	msgs[4].Content = &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{Type: schemas.ChatContentBlockTypeText, Text: strp("calling  "), CacheControl: cc}}}
	out := run(t, msgs, defaults(t))
	for i := 0; i <= 4; i++ {
		a, _ := json.Marshal(msgs[i])
		b, _ := json.Marshal(out[i])
		require.Equal(t, string(a), string(b))
	}
	require.Equal(t, bigOutput(200), strOf(out[3]))
}

func TestChatNilContent(t *testing.T) {
	empty := ""
	msgs := rounds("x", "y", "z")
	msgs[3].Content = nil
	msgs[5].Content = &schemas.ChatMessageContent{}
	msgs[7].Content = &schemas.ChatMessageContent{ContentStr: &empty}
	msgs = append(msgs, schemas.ChatMessage{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{}}})
	require.NotPanics(t, func() { run(t, msgs, defaults(t)) })
	require.Equal(t, 0, estimateChat([]schemas.ChatMessage{msgs[3], msgs[5], msgs[7], msgs[8]}))
}

func TestChatMixedBlocksImageUntouched(t *testing.T) {
	img := schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: "data:image/png;base64,AAAA"}}
	msgs := rounds("x", "y", "z")
	big := bigOutput(200)
	msgs[3].Content = &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
		{Type: schemas.ChatContentBlockTypeText, Text: &big}, img,
	}}
	out := run(t, msgs, defaults(t))
	blocks := out[3].Content.ContentBlocks
	require.Len(t, blocks, 2)
	require.Contains(t, *blocks[0].Text, "tokens trimmed")
	ib, _ := json.Marshal(img)
	ob, _ := json.Marshal(blocks[1])
	require.Equal(t, string(ib), string(ob))
	require.Equal(t, big, *msgs[3].Content.ContentBlocks[0].Text)
}

func TestChatTrimNotLongerThanOriginal(t *testing.T) {
	// Two tiny lines with head=0, tail=1: trimming would swap one 2-byte line
	// for a ~48-byte marker, making the message longer, so it must be left as is.
	tiny := "ab\nab"
	s := defaults(t)
	s.HeadLines, s.TailLines, s.MinTokens = 0, 1, 1
	out := run(t, rounds(tiny, "a", "b"), s)
	require.Equal(t, tiny, strOf(out[3]))
}

func TestEstimateChat(t *testing.T) {
	img := schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: "xxxxxxxxxxxxxxxx"}}
	m2 := schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
		{Type: schemas.ChatContentBlockTypeText, Text: strp("abcd")}, img,
	}}}
	msgs := []schemas.ChatMessage{textMsg(schemas.ChatMessageRoleUser, strings.Repeat("a", 8)), m2}
	require.Equal(t, 2+1, estimateChat(msgs))
}

// assertChatInvariants checks the guarantees every compression must keep: no
// message added, removed or reordered, roles and tool-call IDs unchanged, and
// the first protected messages byte-identical (cache prefix).
func assertChatInvariants(t *testing.T, before, after []schemas.ChatMessage, protected int) {
	t.Helper()
	require.Len(t, after, len(before))
	for i := range before {
		require.Equal(t, before[i].Role, after[i].Role, "role at %d", i)

		var bid, aid *string
		if before[i].ChatToolMessage != nil {
			bid = before[i].ChatToolMessage.ToolCallID
		}
		if after[i].ChatToolMessage != nil {
			aid = after[i].ChatToolMessage.ToolCallID
		}
		require.Equal(t, bid, aid, "tool_call_id at %d", i)

		require.Equal(t, toolCallIDs(before[i]), toolCallIDs(after[i]), "assistant tool calls at %d", i)

		if i < protected {
			bj, err := json.Marshal(before[i])
			require.NoError(t, err)
			aj, err := json.Marshal(after[i])
			require.NoError(t, err)
			require.Equal(t, string(bj), string(aj), "protected message %d changed", i)
		}
	}
}

func toolCallIDs(m schemas.ChatMessage) []string {
	if m.ChatAssistantMessage == nil {
		return nil
	}
	var ids []string
	for _, tc := range m.ChatAssistantMessage.ToolCalls {
		if tc.ID != nil {
			ids = append(ids, *tc.ID)
		}
	}
	return ids
}
