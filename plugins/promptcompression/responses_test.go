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
	out, st := compressResponses(msgs, s)
	require.Equal(t, st.Changed, len(out) > 0 && &out[0] != &msgs[0], "Changed must match slice aliasing")
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
	s.MinAgeTurns, s.MinTokens = 1, 1<<30 // isolate dedupe from trimming
	out := runResp(t, msgs, s)
	require.Equal(t, "[identical to the output of call call_2]", rmOutStr(out[3]))
	require.Equal(t, same, rmOutStr(out[5]))
	require.Nil(t, out[3].ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks)
}

func TestResponsesDedupeBlocksAndMixed(t *testing.T) {
	s := defaults(t)
	s.MinAgeTurns, s.MinTokens = 1, 1<<30 // isolate dedupe from trimming
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
	s.MinAgeTurns, s.MinTokens = 1, 1<<30 // isolate dedupe from trimming
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

func TestResponsesWhitespaceOnlyOldToolOutputs(t *testing.T) {
	const padded = "o  \n\n\n\nz"
	s := defaults(t)
	s.Dedupe = false
	user := rmText(schemas.ResponsesInputMessageRoleUser, "hi   \n\n\n\n\nthere")
	asst := schemas.ResponsesMessage{
		Type: rmType(schemas.ResponsesMessageTypeMessage),
		Role: rmRole(schemas.ResponsesInputMessageRoleAssistant),
		Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
			rmBlock(schemas.ResponsesOutputMessageContentTypeText, "a \t\nb"),
		}},
	}
	// outputs at 4, 6, 8: ages 2, 1, 0
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys  "), user, asst,
		rmCall("1"), rmOut("1", padded), rmCall("2"), rmOut("2", padded), rmCall("3"), rmOut("3", padded),
	}
	out := runResp(t, msgs, s)
	require.Equal(t, "sys  ", *out[0].Content.ContentStr, "system untouched")
	require.Equal(t, "hi   \n\n\n\n\nthere", *out[1].Content.ContentStr, "user untouched")
	require.Equal(t, "a \t\nb", *out[2].Content.ContentBlocks[0].Text, "assistant untouched")
	require.Equal(t, "o\n\nz", rmOutStr(out[4]), "old tool output normalised")
	require.Equal(t, padded, rmOutStr(out[6]), "young (age 1) tool output unchanged")
	require.Equal(t, padded, rmOutStr(out[8]), "newest tool output unchanged")
	require.Equal(t, "{}", *out[3].ResponsesToolMessage.Arguments)
	s.Whitespace = false
	require.Equal(t, padded, rmOutStr(runResp(t, msgs, s)[4]), "disabled")
}

func TestResponsesDedupeChainsToNextCopy(t *testing.T) {
	same := bigOutput(200)
	s := defaults(t)
	s.MinAgeTurns, s.MinTokens = 1, 1<<30 // isolate dedupe from trimming
	three := runResp(t, respRounds(same, same, same), s)
	require.Equal(t, "[identical to the output of call call_2]", rmOutStr(three[3]))
	require.Equal(t, "[identical to the output of call call_3]", rmOutStr(three[5]))
	require.Equal(t, same, rmOutStr(three[7]))
	four := runResp(t, respRounds(same, same, same, same), s)
	require.Equal(t, rmOutStr(three[3]), rmOutStr(four[3]), "existing stub unchanged when a copy is appended")
	require.Equal(t, rmOutStr(three[5]), rmOutStr(four[5]))
	require.Equal(t, "[identical to the output of call call_4]", rmOutStr(four[7]))
	require.Equal(t, same, rmOutStr(four[9]))
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

// assertResponsesInvariants is the Responses counterpart of
// assertChatInvariants: same item count, and Type, Role and CallID unchanged at
// every index (nil versus non-nil counts as a difference), with the protected
// prefix byte-identical.
func assertResponsesInvariants(t *testing.T, before, after []schemas.ResponsesMessage, protected int) {
	t.Helper()
	require.Len(t, after, len(before))
	for i := range before {
		require.Equal(t, before[i].Type, after[i].Type, "type at %d", i)
		require.Equal(t, before[i].Role, after[i].Role, "role at %d", i)
		require.Equal(t, respCallID(before[i]), respCallID(after[i]), "call_id at %d", i)

		if i < protected {
			bj, err := json.Marshal(before[i])
			require.NoError(t, err)
			aj, err := json.Marshal(after[i])
			require.NoError(t, err)
			require.Equal(t, string(bj), string(aj), "protected item %d changed", i)
		}
	}
}

func respCallID(m schemas.ResponsesMessage) *string {
	if m.ResponsesToolMessage == nil {
		return nil
	}
	return m.ResponsesToolMessage.CallID
}

// rmItem builds a bare item of the given type (reasoning, web_search_call,
// shell_call, local_shell_call_output, ...) with no role and no content.
func rmItem(typ schemas.ResponsesMessageType) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{Type: rmType(typ)}
}

// A turn made only of server-tool items still counts, and a run of them is one
// turn: the output the model saw once must not look two turns old.
func TestResponsesServerToolItemsCountAsOneTurn(t *testing.T) {
	big := bigOutput(200)
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmText(schemas.ResponsesInputMessageRoleUser, "go"),
		rmItem(schemas.ResponsesMessageTypeReasoning),
		rmItem(schemas.ResponsesMessageTypeWebSearchCall),
		rmCall("1"),
		rmOut("1", big),
		rmItem(schemas.ResponsesMessageTypeMCPCall),
		rmText(schemas.ResponsesInputMessageRoleAssistant, "done"),
	}
	s := defaults(t) // MinAgeTurns 2; the output has age 1
	out := runResp(t, msgs, s)
	require.Equal(t, big, rmOutStr(out[5]), "age 1 untouched")
	s.MinAgeTurns = 1
	out = runResp(t, msgs, s)
	require.Contains(t, rmOutStr(out[5]), "tokens trimmed", "age 1 reaches MinAgeTurns 1")
}

// A turn that is only an mcp_call, split by a user message, is still a turn.
func TestResponsesServerToolOnlyTurnsAdvanceAge(t *testing.T) {
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmCall("1"), rmOut("1", bigOutput(200)),
		rmItem(schemas.ResponsesMessageTypeMCPCall),
		rmText(schemas.ResponsesInputMessageRoleUser, "more"),
		rmItem(schemas.ResponsesMessageTypeWebSearchCall),
	}
	out := runResp(t, msgs, defaults(t))
	require.Contains(t, rmOutStr(out[2]), "tokens trimmed", "two server-tool turns: age 2")
}

func TestResponsesShellCallAdvancesAge(t *testing.T) {
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmCall("1"), rmOut("1", bigOutput(200)),
		rmItem(schemas.ResponsesMessageTypeShellCall), rmItem(schemas.ResponsesMessageTypeLocalShellCallOutput),
		rmItem(schemas.ResponsesMessageTypeShellCall), rmItem(schemas.ResponsesMessageTypeLocalShellCallOutput),
	}
	out := runResp(t, msgs, defaults(t))
	require.Contains(t, rmOutStr(out[2]), "tokens trimmed", "age 2 via shell_call turns")
}

// Tool outputs of any type are not assistant items: they neither start nor
// extend a turn, so a call whose outputs follow it is one turn.
func TestResponsesOutputsAreNotAssistantItems(t *testing.T) {
	big := bigOutput(200)
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmCall("1"), rmOut("1", big),
		rmItem(schemas.ResponsesMessageTypeCustomToolCallOutput),
		rmItem(schemas.ResponsesMessageTypeComputerCallOutput),
		rmItem(schemas.ResponsesMessageType("mcp_call_output")),
		rmItem(schemas.ResponsesMessageType("future_tool_output")),
	}
	out := runResp(t, msgs, defaults(t))
	require.Equal(t, big, rmOutStr(out[2]), "no assistant turn after the output")
}

func TestResponsesDedupeOnlyEligibleOlderCopy(t *testing.T) {
	file := bigOutput(50)
	s := defaults(t)
	s.MinAgeTurns = 2
	young := runResp(t, respRounds(file, file), s) // ages 1 and 0
	require.Equal(t, file, rmOutStr(young[3]))
	require.Equal(t, file, rmOutStr(young[5]))
	old := runResp(t, respRounds(file, file, "x", "y"), s) // ages 3 and 2, both eligible
	require.Equal(t, "[identical to the output of call call_2]", rmOutStr(old[3]))
	require.NotContains(t, rmOutStr(old[5]), "identical to the output", "newest copy has no newer duplicate")
}

func TestResponsesLateDuplicateDoesNotRewriteEligibleOutput(t *testing.T) {
	big := bigOutput(200)
	s := defaults(t)
	reqN := respRounds(big, "x", "y") // item 3 has age 2 and is trimmed
	outN := runResp(t, reqN, s)
	require.Contains(t, rmOutStr(outN[3]), "tokens trimmed")
	reqN1 := append(append([]schemas.ResponsesMessage{}, reqN...), rmCall("call_9"), rmOut("call_10", big))
	outN1 := runResp(t, reqN1, s)
	require.Equal(t, rmOutStr(outN[3]), rmOutStr(outN1[3]), "late duplicate must not turn trimmed text into a stub")
	require.Equal(t, big, rmOutStr(outN1[9]))
}

func TestResponsesDuplicatePresentAtEligibilityIsStubbed(t *testing.T) {
	file := bigOutput(50)
	s := defaults(t)
	const want = "[identical to the output of call call_2]"
	req := respRounds(file, file, "x") // item 3 reaches age 2 with its duplicate present
	for k, extra := range []string{"", "y", "z"} {
		if extra != "" {
			id := fmt.Sprintf("call_%d", (len(req)-2)/2+1)
			req = append(append([]schemas.ResponsesMessage{}, req...), rmCall(id), rmOut(id, extra))
		}
		out := runResp(t, req, s)
		require.Equal(t, want, rmOutStr(out[3]), "request %d", k+1)
	}
}

func TestResponsesDedupePointerAdvancesOverIneligibleCopies(t *testing.T) {
	file := bigOutput(50)
	s := defaults(t)
	s.MinAgeTurns = 2
	out := runResp(t, respRounds(file, "x", file, file), s) // ages 3,2,1,0
	require.Equal(t, "[identical to the output of call call_3]", rmOutStr(out[3]), "points at the next copy, not the newest")
}

func TestResponsesStatsCountsUnchangedEligibleOutputs(t *testing.T) {
	big := bigOutput(200)
	blocks := rmOutBlocks("b", rmBlock(schemas.ResponsesInputMessageContentBlockTypeText, "blk text"))
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmCall("1"), rmOut("1", "small1"),
		rmCall("2"), rmOut("2", big),
		rmCall("b"), blocks,
		rmCall("3"), rmOut("3", "x"),
		rmCall("4"), rmOut("4", "y"),
	}
	out, st := compressResponses(msgs, defaults(t))
	require.True(t, st.Changed)
	require.Equal(t, estimateTokens("small1")+estimateTokens(big)+estimateTokens("blk text"), st.EligibleBefore)
	require.Equal(t, estimateTokens("small1")+estimateTokens(rmOutStr(out[4]))+estimateTokens("blk text"), st.EligibleAfter)
}

func TestResponsesNoopEligibleSmallOutput(t *testing.T) {
	msgs := respRounds("small clean output", "x", "y")
	s := defaults(t)
	out, st := compressResponses(msgs, s)
	require.Equal(t, &msgs[0], &out[0], "same backing array")
	require.False(t, st.Changed)
	require.Equal(t, estimateTokens("small clean output"), st.EligibleBefore)
	require.Equal(t, st.EligibleBefore, st.EligibleAfter)
	require.Zero(t, testing.AllocsPerRun(100, func() { compressResponses(msgs, s) }))
}

func TestResponsesNoopReturnsSameSliceNoAllocs(t *testing.T) {
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmText(schemas.ResponsesInputMessageRoleUser, "go"),
		rmText(schemas.ResponsesInputMessageRoleAssistant, "ok"),
	}
	s := defaults(t)
	out, st := compressResponses(msgs, s)
	require.Equal(t, &msgs[0], &out[0])
	require.False(t, st.Changed)
	require.Zero(t, st.EligibleBefore)
	require.Zero(t, testing.AllocsPerRun(100, func() { compressResponses(msgs, s) }))
}

// Only function_call_output is rewritten; other output types keep their text
// even when old and large.
func TestResponsesOtherOutputTypesNeverEdited(t *testing.T) {
	big := bigOutput(200)
	shell := rmOut("s", big)
	shell.Type = rmType(schemas.ResponsesMessageTypeLocalShellCallOutput)
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmCall("1"), shell, rmCall("2"), rmCall("3"),
	}
	out, st := compressResponses(msgs, defaults(t))
	require.Equal(t, &msgs[0], &out[0])
	require.False(t, st.Changed)
	require.Zero(t, st.EligibleBefore)
}

// Client input that is not a message must split model responses: here two
// calls separated by the input are two turns, so the output is age 2.
func TestResponsesInputItemsSplitTurns(t *testing.T) {
	for _, typ := range []schemas.ResponsesMessageType{
		schemas.ResponsesMessageTypeMCPApprovalResponses,
		schemas.ResponsesMessageTypeConfigurationUpdate,
		schemas.ResponsesMessageTypeAdditionalTools,
	} {
		msgs := []schemas.ResponsesMessage{
			rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
			rmCall("1"), rmOut("1", bigOutput(200)),
			rmCall("b"), rmItem(typ), rmCall("c"),
		}
		out := runResp(t, msgs, defaults(t))
		require.Contains(t, rmOutStr(out[2]), "tokens trimmed", string(typ))
	}
}

func TestResponsesMCPApprovalResponseIsInput(t *testing.T) {
	file := bigOutput(50)
	s := defaults(t) // K = 2
	reqN := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmText(schemas.ResponsesInputMessageRoleUser, "go"),
		rmCall("a"), rmOut("a", file), // X
		rmCall("b"), rmOut("b", "o"),
		rmItem(schemas.ResponsesMessageTypeMCPApprovalRequest),
		rmItem(schemas.ResponsesMessageTypeMCPApprovalResponses),
	}
	outN := runResp(t, reqN, s)
	reqN1 := append(append([]schemas.ResponsesMessage{}, reqN...),
		rmItem(schemas.ResponsesMessageTypeMCPCall), rmCall("c"), rmOut("c", file))
	outN1 := runResp(t, reqN1, s)
	require.Equal(t, rmOutStr(outN[3]), rmOutStr(outN1[3]), "X must not flip to a stub")
	require.NotContains(t, rmOutStr(outN1[3]), "identical to the output")
}

func TestResponsesConfigurationUpdateIsInput(t *testing.T) {
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmCall("1"), rmOut("1", bigOutput(200)),
		rmItem(schemas.ResponsesMessageTypeConfigurationUpdate), // not a turn: age stays 0
	}
	big := rmOutStr(msgs[2])
	require.Equal(t, big, rmOutStr(runResp(t, msgs, defaults(t))[2]))
}

func TestResponsesAdditionalToolsIsInput(t *testing.T) {
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmCall("1"), rmOut("1", bigOutput(200)),
		rmCall("2"),
		rmItem(schemas.ResponsesMessageTypeAdditionalTools), // not a turn: age 1
	}
	big := rmOutStr(msgs[2])
	require.Equal(t, big, rmOutStr(runResp(t, msgs, defaults(t))[2]))
}

// A hosted tool search lives inside one model response, so its output must not
// split the response into two turns.
func TestResponsesHostedToolSearchOutputStaysInTurn(t *testing.T) {
	big := bigOutput(200)
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmCall("1"), rmOut("1", big),
		rmItem(schemas.ResponsesMessageTypeToolSearchCall),
		rmItem(schemas.ResponsesMessageTypeToolSearchOutput),
		rmCall("2"),
	}
	out := runResp(t, msgs, defaults(t)) // one turn: age 1 < 2
	require.Equal(t, big, rmOutStr(out[2]))
}

// A client-executed tool search output is caller input: it ends the run.
func TestResponsesClientToolSearchOutputSplitsTurns(t *testing.T) {
	big := bigOutput(200)
	client := rmItem(schemas.ResponsesMessageTypeToolSearchOutput)
	client.ResponsesToolMessage = &schemas.ResponsesToolMessage{Execution: strp(schemas.ToolSearchExecutionClient)}
	msgs := []schemas.ResponsesMessage{
		rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
		rmCall("1"), rmOut("1", big),
		rmItem(schemas.ResponsesMessageTypeToolSearchCall), client, rmCall("2"),
	}
	out := runResp(t, msgs, defaults(t)) // two turns: age 2
	require.Contains(t, rmOutStr(out[2]), "tokens trimmed")
}

// A developer or system item in the middle of the history is input, not a turn.
func TestResponsesMidHistoryDeveloperSystemNotATurn(t *testing.T) {
	big := bigOutput(200)
	for _, role := range []schemas.ResponsesMessageRoleType{schemas.ResponsesInputMessageRoleDeveloper, schemas.ResponsesInputMessageRoleSystem} {
		msgs := []schemas.ResponsesMessage{
			rmText(schemas.ResponsesInputMessageRoleSystem, "sys"),
			rmCall("1"), rmOut("1", big),
			rmCall("2"), rmText(role, "note"), rmOut("2", "ok"),
		}
		out := runResp(t, msgs, defaults(t)) // one turn after the output: age 1
		require.Equal(t, big, rmOutStr(out[2]), string(role))
	}
}

// Pass 2 runs (an eligible value is big enough to be a trim candidate) but
// changes nothing: the input slice itself must come back.
func TestResponsesPass2WithoutChangeReturnsSameSlice(t *testing.T) {
	msgs := respRounds("abcd", "x", "y")
	s := defaults(t)
	s.MinTokens = 1
	out, st := compressResponses(msgs, s)
	require.Equal(t, &msgs[0], &out[0])
	require.False(t, st.Changed)
	require.Equal(t, st.EligibleBefore, st.EligibleAfter)
}
