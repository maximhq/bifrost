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

// run compresses a deep copy check: input JSON must be unchanged after the call.
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

func TestChatWhitespaceDisabled(t *testing.T) {
	msgs := []schemas.ChatMessage{textMsg(schemas.ChatMessageRoleSystem, "sys"), textMsg(schemas.ChatMessageRoleUser, "a   \nb")}
	s := defaults(t)
	require.Equal(t, "a\nb", strOf(run(t, msgs, s)[1]))
	s.Whitespace = false
	require.Equal(t, "a   \nb", strOf(run(t, msgs, s)[1]))
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
	// 21 long lines with head=0 tail=20: the marker costs more than the one removed line.
	line := strings.Repeat("x", 120)
	lines := make([]string, 21)
	for i := range lines {
		lines[i] = line
	}
	s := defaults(t)
	s.HeadLines, s.TailLines, s.MinTokens = 0, 20, 1
	msgs := rounds(strings.Join(lines[:2], "\n"), "a", "b")
	s.TailLines = 1
	out := run(t, msgs, s)
	require.LessOrEqual(t, len(strOf(out[3])), len(strings.Join(lines[:2], "\n")))
}

func TestEstimateChat(t *testing.T) {
	img := schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: "xxxxxxxxxxxxxxxx"}}
	m2 := schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
		{Type: schemas.ChatContentBlockTypeText, Text: strp("abcd")}, img,
	}}}
	msgs := []schemas.ChatMessage{textMsg(schemas.ChatMessageRoleUser, strings.Repeat("a", 8)), m2}
	require.Equal(t, 2+1, estimateChat(msgs))
}
