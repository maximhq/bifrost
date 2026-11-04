package promptcompression

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func rmType(t schemas.ResponsesMessageType) *schemas.ResponsesMessageType { return &t }

func rmRole(r schemas.ResponsesMessageRoleType) *schemas.ResponsesMessageRoleType { return &r }

func rmText(role schemas.ResponsesMessageRoleType, text string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type:    rmType(schemas.ResponsesMessageTypeMessage),
		Role:    rmRole(role),
		Content: &schemas.ResponsesMessageContent{ContentStr: &text},
	}
}

func rmCall(id string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type:                 rmType(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: strp(id), Arguments: strp("{}")},
	}
}

func rmOut(id, text string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: rmType(schemas.ResponsesMessageTypeFunctionCallOutput),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: strp(id),
			Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &text},
		},
	}
}

func rmOutBlocks(id string, blocks ...schemas.ResponsesMessageContentBlock) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: rmType(schemas.ResponsesMessageTypeFunctionCallOutput),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: strp(id),
			Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesFunctionToolCallOutputBlocks: blocks},
		},
	}
}

func rmBlock(typ schemas.ResponsesMessageContentBlockType, text string) schemas.ResponsesMessageContentBlock {
	return schemas.ResponsesMessageContentBlock{Type: typ, Text: &text}
}

func rmOutStr(m schemas.ResponsesMessage) string {
	return *m.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
}

func runResp(t *testing.T, msgs []schemas.ResponsesMessage, s settings) []schemas.ResponsesMessage {
	t.Helper()
	before, err := json.Marshal(msgs)
	require.NoError(t, err)
	out := compressResponses(msgs, s)
	after, err := json.Marshal(msgs)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "input mutated")
	assertResponsesInvariants(t, msgs, out, responsesProtectedPrefix(msgs, s.ProtectedLeading))
	return out
}

// respRounds builds system, user, then call/output rounds.
func respRounds(outputs ...string) []schemas.ResponsesMessage {
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmText(schemas.ResponsesInputMessageRoleUser, "go"),
	}
	for i, o := range outputs {
		id := fmt.Sprintf("call_%d", i+1)
		msgs = append(msgs, rmCall(id), rmOut(id, o))
	}
	return msgs
}

func TestResponsesTrimsOldOutput(t *testing.T) {
	big, last := bigOutput(200), bigOutput(150)
	msgs := respRounds(big, "small", last)
	out := runResp(t, msgs, defaults(t))
	require.Contains(t, rmOutStr(out[3]), "tokens trimmed by promptcompression")
	require.Less(t, len(rmOutStr(out[3])), len(big))
	require.Equal(t, last, rmOutStr(out[7]), "age 0 untouched")
	require.Equal(t, big, rmOutStr(msgs[3]), "input untouched")
}

func TestResponsesKeepsYoungOutput(t *testing.T) {
	big := bigOutput(200)
	msgs := respRounds(big, big+"x", big+"y") // ages 2,1,0; all distinct
	out := runResp(t, msgs, defaults(t))
	require.Contains(t, rmOutStr(out[3]), "trimmed")
	require.Equal(t, big+"x", rmOutStr(out[5]), "age 1 untouched")
	require.Equal(t, big+"y", rmOutStr(out[7]))
}

func TestResponsesParallelCallsSameAge(t *testing.T) {
	// Three parallel calls then their outputs, followed by n later turns. Each
	// later turn is a reasoning item plus a function_call (one run = one turn).
	mk := func(laterTurns int) []schemas.ResponsesMessage {
		msgs := []schemas.ResponsesMessage{
			rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
			rmText(schemas.ResponsesInputMessageRoleUser, "go"),
			rmCall("a"), rmCall("b"), rmCall("c"),
			rmOut("a", bigOutput(200)), rmOut("b", bigOutput(201)), rmOut("c", bigOutput(202)),
		}
		for i := 0; i < laterTurns; i++ {
			id := fmt.Sprintf("n%d", i)
			msgs = append(msgs,
				schemas.ResponsesMessage{Type: rmType(schemas.ResponsesMessageTypeReasoning)},
				rmCall(id), rmOut(id, "ok"))
		}
		return msgs
	}
	s := defaults(t)

	young := mk(1) // group age 1 < MinAgeTurns 2
	out := runResp(t, young, s)
	for _, i := range []int{5, 6, 7} {
		require.Equal(t, rmOutStr(young[i]), rmOutStr(out[i]), "age 1 untouched at %d", i)
	}

	old := mk(2) // group age 2
	out = runResp(t, old, s)
	for _, i := range []int{5, 6, 7} {
		require.Contains(t, rmOutStr(out[i]), "tokens trimmed", "age 2 trimmed at %d", i)
	}
}

func TestResponsesDedupe(t *testing.T) {
	same := bigOutput(200)
	msgs := respRounds(same, same, "tail")
	s := defaults(t)
	s.MinAgeTurns = 99 // isolate dedupe from trimming
	out := runResp(t, msgs, s)
	require.Equal(t, "[identical to the output of call call_2]", rmOutStr(out[3]))
	require.Equal(t, same, rmOutStr(out[5]))
	require.Nil(t, out[3].ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks)
}

func TestResponsesDedupeBlocksAndMixed(t *testing.T) {
	s := defaults(t)
	s.MinAgeTurns = 99
	txt := bigOutput(100)
	img := schemas.ResponsesMessageContentBlock{Type: schemas.ResponsesInputMessageContentBlockTypeImage}
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmCall("1"), rmOutBlocks("1", rmBlock(schemas.ResponsesInputMessageContentBlockTypeText, txt)),
		rmCall("2"), rmOut("2", txt),
		rmCall("3"), rmOutBlocks("3", rmBlock(schemas.ResponsesInputMessageContentBlockTypeText, txt), img),
		rmCall("4"), rmOutBlocks("4", rmBlock(schemas.ResponsesInputMessageContentBlockTypeText, txt), img),
	}
	out := runResp(t, msgs, s)
	require.Equal(t, "[identical to the output of call 2]", rmOutStr(out[2]))
	require.Nil(t, out[2].ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks)
	require.Equal(t, 2, len(out[6].ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks), "mixed never deduped")
	require.Nil(t, out[6].ResponsesToolMessage.Output.ResponsesToolCallOutputStr)
}

func TestResponsesDedupeNilCallIDSkipped(t *testing.T) {
	s := defaults(t)
	s.MinAgeTurns = 99
	same := bigOutput(100)
	newest := rmOut("x", same)
	newest.ResponsesToolMessage.CallID = nil
	msgs := []schemas.ResponsesMessage{rmText(schemas.ResponsesInputMessageRoleSystem, "sys"), rmCall("1"), rmOut("1", same), rmCall("2"), newest}
	out := runResp(t, msgs, s)
	require.Equal(t, same, rmOutStr(out[2]))
}

func TestResponsesProtectedPrefixUntouched(t *testing.T) {
	big := bigOutput(200)
	msgs := respRounds(big, "mid", "tail", "tail2")
	msgs[3].CacheControl = &schemas.CacheControl{Type: "ephemeral"}
	s := defaults(t)
	out := runResp(t, msgs, s)
	require.Equal(t, big, rmOutStr(out[3]), "protected by cache marker")
	big2 := bigOutput(210)
	msgs = respRounds(big2, "x", "y", "z")
	s.ProtectedLeading = 4
	out = runResp(t, msgs, s)
	require.Equal(t, big2, rmOutStr(out[3]))
}

func TestResponsesNilOutput(t *testing.T) {
	noTool := schemas.ResponsesMessage{Type: rmType(schemas.ResponsesMessageTypeFunctionCallOutput)}
	noOut := schemas.ResponsesMessage{
		Type:                 rmType(schemas.ResponsesMessageTypeFunctionCallOutput),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: strp("n")},
	}
	empty := rmOutBlocks("e")
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmCall("n"), noTool, noOut, empty,
		rmCall("1"), rmOut("1", "a"), rmCall("2"), rmOut("2", "b"), rmCall("3"), rmOut("3", "c"),
	}
	require.NotPanics(t, func() { runResp(t, msgs, defaults(t)) })
	require.Equal(t, 0, estimateResponses([]schemas.ResponsesMessage{noTool, noOut, empty}))
}

func TestResponsesMixedBlocksOnlyTextChanges(t *testing.T) {
	big := bigOutput(200)
	img := schemas.ResponsesMessageContentBlock{Type: schemas.ResponsesInputMessageContentBlockTypeImage}
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmCall("1"),
		rmOutBlocks("1", rmBlock(schemas.ResponsesInputMessageContentBlockTypeText, big), img, rmBlock(schemas.ResponsesOutputMessageContentTypeText, big)),
		rmCall("2"), rmOut("2", "a"), rmCall("3"), rmOut("3", "b"),
	}
	out := runResp(t, msgs, defaults(t))
	bl := out[2].ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks
	require.Len(t, bl, 3)
	require.Contains(t, *bl[0].Text, "trimmed")
	require.Contains(t, *bl[2].Text, "trimmed")
	a, _ := json.Marshal(img)
	b, _ := json.Marshal(bl[1])
	require.Equal(t, string(a), string(b))
}

// A cut whose marker outweighs the removed lines must be rejected: the output
// has to be strictly shorter in bytes or it is left as it was.
func TestResponsesTrimNotLonger(t *testing.T) {
	text := fmt.Sprintf("%02000d", 0) + "\ny\nend" // trimming drops 1 byte, adds a ~48 byte marker
	msgs := respRounds(text, "a", "b")
	s := defaults(t)
	s.HeadLines, s.TailLines = 1, 1
	require.GreaterOrEqual(t, estimateTokens(text), s.MinTokens)
	out := runResp(t, msgs, s)
	require.Equal(t, text, rmOutStr(out[3]), "marker longer than cut: unchanged")
}

func TestResponsesWhitespace(t *testing.T) {
	s := defaults(t)
	s.MinAgeTurns = 99
	user := rmText(schemas.ResponsesInputMessageRoleUser, "hi   \n\n\n\n\nthere")
	blocks := schemas.ResponsesMessage{
		Type: rmType(schemas.ResponsesMessageTypeMessage),
		Role: rmRole(schemas.ResponsesInputMessageRoleAssistant),
		Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
			rmBlock(schemas.ResponsesOutputMessageContentTypeText, "a \t\nb"),
		}},
	}
	msgs := []schemas.ResponsesMessage{rmText(schemas.ResponsesInputMessageRoleSystem, "sys  "), user, blocks, rmCall("1"), rmOut("1", "o  \n\n\n\nz")}
	out := runResp(t, msgs, s)
	require.Equal(t, "sys  ", *out[0].Content.ContentStr, "protected")
	require.Equal(t, "hi\n\nthere", *out[1].Content.ContentStr)
	require.Equal(t, "a\nb", *out[2].Content.ContentBlocks[0].Text)
	require.Equal(t, "o\n\nz", rmOutStr(out[4]))
	require.Equal(t, "{}", *out[3].ResponsesToolMessage.Arguments)
}

func TestEstimateResponses(t *testing.T) {
	blk := schemas.ResponsesMessage{
		Type: rmType(schemas.ResponsesMessageTypeMessage),
		Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
			rmBlock(schemas.ResponsesOutputMessageContentTypeText, "12345678"),
			{Type: schemas.ResponsesInputMessageContentBlockTypeImage},
		}},
	}
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleUser, "1234"), // 1
		blk,                     // 2
		rmOut("1", "123456789"), // 3
		rmOutBlocks("2", rmBlock(schemas.ResponsesInputMessageContentBlockTypeText, "12")), // 1
		rmCall("3"),
	}
	require.Equal(t, 7, estimateResponses(msgs))
}

func TestResponsesAssistantMessageEndsTurn(t *testing.T) {
	asstNilType := rmText(schemas.ResponsesInputMessageRoleAssistant, "thinking aloud")
	asstNilType.Type = nil
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmCall("1"), rmOut("1", bigOutput(200)),
		asstNilType, // turn 1 (Type nil still counts)
		rmText(schemas.ResponsesInputMessageRoleUser, "more"),
		rmText(schemas.ResponsesInputMessageRoleAssistant, "done"), // turn 2
	}
	out := runResp(t, msgs, defaults(t))
	require.Contains(t, rmOutStr(out[2]), "tokens trimmed", "age 2 via assistant messages")
}

func TestResponsesUserSplitsCallsIntoTurns(t *testing.T) {
	mk := func(split bool) []schemas.ResponsesMessage {
		msgs := []schemas.ResponsesMessage{
			rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
			rmCall("1"), rmOut("1", bigOutput(200)),
			rmCall("b"),
		}
		if split {
			msgs = append(msgs, rmText(schemas.ResponsesInputMessageRoleUser, "hm"))
		}
		return append(msgs, rmCall("c"))
	}
	out := runResp(t, mk(true), defaults(t))
	require.Contains(t, rmOutStr(out[2]), "tokens trimmed", "user between calls: two turns")
	joined := mk(false)
	out = runResp(t, joined, defaults(t))
	require.Equal(t, rmOutStr(joined[2]), rmOutStr(out[2]), "adjacent calls: one turn")
}
