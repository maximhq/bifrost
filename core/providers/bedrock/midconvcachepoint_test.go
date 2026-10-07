package bedrock_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Wire-level counterpart to the "Cross-Cut Round 34: Mid-Conversation System Cache-Anchor
// Parity" harness folder (tests/e2e/api/runners/lib/midconv-system-cache-parity.mjs). The
// harness measures the symptom in billed tokens against a live Bedrock account; these tests
// assert the cause on the emitted Converse request, with no credentials and no spend.
//
// The shape under test is Claude Code's: three cache_control breakpoints - two on the leading
// system prompt, one riding a mid-conversation role=system <system-reminder>. Bedrock has no
// message-level system role, so ConvertBifrostMessagesToBedrockMessages inlines that reminder
// as a user turn (TestMidConversationSystemReminderStaysInline covers why: hoisting it would
// grow the system block in front of the cached conversation prefix). The inlining must carry
// the breakpoint with it. Dropping it leaves both surviving cachePoints in `system`, which pins
// the cacheable prefix at the system floor and re-reads the whole conversation body uncached on
// every turn - the exact collapse the inlining exists to prevent.

// cachedTextBlock builds a text content block carrying an ephemeral cache_control breakpoint,
// the way an Anthropic-dialect client marks a cache anchor.
func cachedTextBlock(text string) schemas.ResponsesMessageContentBlock {
	return schemas.ResponsesMessageContentBlock{
		Type:         schemas.ResponsesInputMessageContentBlockTypeText,
		Text:         schemas.Ptr(text),
		CacheControl: &schemas.CacheControl{Type: "ephemeral"},
	}
}

// cachedRoleMsg builds a single-block message of the given role whose block carries a breakpoint.
func cachedRoleMsg(role schemas.ResponsesMessageRoleType, text string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role:    schemas.Ptr(role),
		Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{cachedTextBlock(text)}},
	}
}

// assistantTextMsg builds a role=assistant message with a single text block, so the fixtures
// below alternate turns the way a real transcript does.
func assistantTextMsg(text string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr(text)},
			},
		},
	}
}

// leadingSystemMsg is the two-breakpoint leading system prompt every case below shares.
func leadingSystemMsg() schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{
				cachedTextBlock("You are Claude Code. Long stable instructions."),
				cachedTextBlock("Tool definitions and environment details."),
			},
		},
	}
}

// countCachePoints returns the cachePoint totals across the converted request, split by where
// they landed. The split is the whole point: a request with 3 cachePoints all sitting in
// `system` caches exactly as badly as one with 2.
func countCachePoints(messages []bedrock.BedrockMessage, systemMessages []bedrock.BedrockSystemMessage) (inSystem, inMessages int) {
	for _, sysMsg := range systemMessages {
		if sysMsg.CachePoint != nil {
			inSystem++
		}
	}
	for _, msg := range messages {
		for _, block := range msg.Content {
			if block.CachePoint != nil {
				inMessages++
			}
		}
	}
	return inSystem, inMessages
}

// TestMidConversationSystemReminderPreservesCachePoint is the regression test for the dropped
// conversation-level cache anchor. Three cache_control breakpoints in must produce three
// cachePoint elements out, and exactly one of them must land inside `messages` - otherwise the
// cacheable prefix never extends past the system block.
func TestMidConversationSystemReminderPreservesCachePoint(t *testing.T) {
	input := []schemas.ResponsesMessage{
		leadingSystemMsg(),
		userReminderTextMsg("first user turn"),
		assistantTextMsg("ok"),
		userReminderTextMsg("second user turn"),
		cachedRoleMsg(schemas.ResponsesInputMessageRoleSystem, "Reminder: stay concise."),
		userReminderTextMsg("what next?"),
	}

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), anthropicModel, input, true)
	require.NoError(t, err)

	inSystem, inMessages := countCachePoints(messages, systemMessages)
	assert.Equal(t, 2, inSystem, "the two leading-system breakpoints must be hoisted into the system block")
	assert.Equal(t, 1, inMessages,
		"the mid-conversation reminder carries the conversation-level cache anchor; dropping it during inlining "+
			"pins the cacheable prefix at the system floor and re-reads the whole conversation body uncached")

	// Placement matters as much as presence: per Converse semantics a cachePoint element
	// terminates the cacheable prefix rather than opening one, so it has to FOLLOW the wrapped
	// reminder text - the same ordering the converter already uses for tool results.
	var foundOrdered bool
	for _, msg := range messages {
		for i, block := range msg.Content {
			if block.Text == nil || !strings.Contains(*block.Text, "<system-reminder>") {
				continue
			}
			require.Greater(t, len(msg.Content), i+1, "reminder text block must be followed by its cachePoint")
			assert.NotNil(t, msg.Content[i+1].CachePoint, "cachePoint must immediately follow the wrapped reminder text")
			assert.Equal(t, bedrock.BedrockMessageRoleUser, msg.Role, "inlined reminder must be a user turn")
			foundOrdered = true
		}
	}
	assert.True(t, foundOrdered, "expected the inlined <system-reminder> block in the converted messages")
}

// TestMidConversationDeveloperReminderPreservesCachePoint pins the developer role to the same
// behaviour. The dispatch in responses.go tests for system OR developer, so the two roles must
// never diverge - including after a fix.
func TestMidConversationDeveloperReminderPreservesCachePoint(t *testing.T) {
	input := []schemas.ResponsesMessage{
		leadingSystemMsg(),
		userReminderTextMsg("first user turn"),
		cachedRoleMsg(schemas.ResponsesInputMessageRoleDeveloper, "Reminder: stay concise."),
		userReminderTextMsg("what next?"),
	}

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), anthropicModel, input, true)
	require.NoError(t, err)

	inSystem, inMessages := countCachePoints(messages, systemMessages)
	assert.Equal(t, 2, inSystem)
	assert.Equal(t, 1, inMessages, "a mid-conversation role=developer reminder must keep its breakpoint, same as role=system")
}

// TestMidConversationSystemReminderContentStrAddsNoCachePoint is the guard against the opposite
// error. The string content form has no per-block cache_control, so the client sent only two
// breakpoints and only two may be emitted - a fix that unconditionally anchors every inlined
// reminder would burn a checkpoint the client never asked for.
func TestMidConversationSystemReminderContentStrAddsNoCachePoint(t *testing.T) {
	input := []schemas.ResponsesMessage{
		leadingSystemMsg(),
		userReminderTextMsg("first user turn"),
		systemReminderTextMsg("Reminder: stay concise."), // ContentBlocks, but no cache_control
		userReminderTextMsg("what next?"),
	}

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), anthropicModel, input, true)
	require.NoError(t, err)

	inSystem, inMessages := countCachePoints(messages, systemMessages)
	assert.Equal(t, 2, inSystem)
	assert.Equal(t, 0, inMessages, "no cache_control on the reminder means no cachePoint may be invented for it")
}

// TestMidConversationSystemRemindersRespectCheckpointBudget documents marker accounting at the
// message-conversion layer. Claude on Bedrock caps cache checkpoints at 4 (BedrockMaxCachePoints,
// per the AWS prompt-caching limits table, which lists a maximum of 4 for every Claude model).
//
// The clamp that enforces that cap, clampBedrockCachePoints, runs in ToBedrockResponsesRequest —
// NOT in ConvertBifrostMessagesToBedrockMessages — so the counts asserted here are pre-clamp and
// describe only what the converter emits. See cachepointclamp_test.go for the clamp itself.
func TestMidConversationSystemRemindersRespectCheckpointBudget(t *testing.T) {
	input := []schemas.ResponsesMessage{
		leadingSystemMsg(),
		userReminderTextMsg("first user turn"),
		cachedRoleMsg(schemas.ResponsesInputMessageRoleSystem, "Reminder one."),
		userReminderTextMsg("second user turn"),
		cachedRoleMsg(schemas.ResponsesInputMessageRoleSystem, "Reminder two."),
		userReminderTextMsg("what next?"),
	}

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), anthropicModel, input, true)
	require.NoError(t, err)

	inSystem, inMessages := countCachePoints(messages, systemMessages)
	assert.Equal(t, 2, inSystem)
	assert.Equal(t, 2, inMessages, "each cache_control-bearing reminder contributes its own breakpoint (unclamped today)")
	assert.LessOrEqual(t, inSystem+inMessages, 4, "Claude on Bedrock accepts at most 4 cache checkpoints")
}

// TestLeadingSystemRunCachePointsUnaffected pins the control arm of the harness experiment: a
// breakpoint that rides a role=user block was never at risk, and must keep working. If this
// ever fails alongside the tests above, the problem is broader than the inlining path.
func TestLeadingSystemRunCachePointsUnaffected(t *testing.T) {
	input := []schemas.ResponsesMessage{
		leadingSystemMsg(),
		userReminderTextMsg("first user turn"),
		assistantTextMsg("ok"),
		cachedRoleMsg(schemas.ResponsesInputMessageRoleUser, "Reminder: stay concise."),
	}

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), anthropicModel, input, true)
	require.NoError(t, err)

	inSystem, inMessages := countCachePoints(messages, systemMessages)
	assert.Equal(t, 2, inSystem)
	assert.Equal(t, 1, inMessages, "a breakpoint on a role=user block is passed through untouched")
}

// --------------------------------------------------------------------------
// Contract ported from PR #5929 (anduril-rvanderzee), the report's author. The core fix landed
// independently here; these cases cover ground the original tests did not, most importantly the
// collapse of multiple breakpoints within a single reminder.
// --------------------------------------------------------------------------

// claudeCodeShape mirrors what Claude Code sends: two breakpoints in the leading system run, then
// a mid-conversation reminder carrying the third, conversation-level breakpoint.
func claudeCodeShape(reminderRole schemas.ResponsesMessageRoleType) []schemas.ResponsesMessage {
	return []schemas.ResponsesMessage{
		{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
				cachedTextBlock("You are Claude Code."),
				cachedTextBlock("Extra instructions."),
			}},
		},
		userReminderTextMsg("Analyze this."),
		assistantTextMsg("Understood."),
		cachedRoleMsg(reminderRole, "Keep going."),
	}
}

// TestInlinedSystemReminder_KeepsCachePoint — the client sends 3 cache_control breakpoints, so 3
// cachePoints must reach Bedrock: 2 hoisted into system, 1 on the inlined reminder.
func TestInlinedSystemReminder_KeepsCachePoint(t *testing.T) {
	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(
		context.Background(), anthropicModel, claudeCodeShape(schemas.ResponsesInputMessageRoleSystem), true)
	require.NoError(t, err)

	inSystem, inMessages := countCachePoints(messages, systemMessages)
	assert.Equal(t, 2, inSystem, "the hoisted leading run keeps its two breakpoints")
	assert.Equal(t, 1, inMessages, "the inlined reminder carries the conversation-level anchor")
	assert.Equal(t, 3, inSystem+inMessages, "3 client breakpoints must produce 3 cachePoints")
}

// TestInlinedSystemReminder_CachePointFollowsText — a cachePoint terminates the cacheable prefix,
// so it must come after the text it closes over, never before and never first.
func TestInlinedSystemReminder_CachePointFollowsText(t *testing.T) {
	messages, _, err := bedrock.ConvertBifrostMessagesToBedrockMessages(
		context.Background(), anthropicModel, claudeCodeShape(schemas.ResponsesInputMessageRoleSystem), true)
	require.NoError(t, err)

	for _, msg := range messages {
		for i, block := range msg.Content {
			if block.CachePoint == nil {
				continue
			}
			require.Greater(t, i, 0, "cachePoint must not be the first block in its message")
			assert.NotNil(t, msg.Content[i-1].Text, "cachePoint must terminate a text block")
		}
	}
}

// TestInlinedSystemReminder_UserRoleTailUnchanged — the equivalent user-role tail is the control:
// it already worked and must keep working identically.
func TestInlinedSystemReminder_UserRoleTailUnchanged(t *testing.T) {
	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(
		context.Background(), anthropicModel, claudeCodeShape(schemas.ResponsesInputMessageRoleUser), true)
	require.NoError(t, err)

	inSystem, inMessages := countCachePoints(messages, systemMessages)
	assert.Equal(t, 3, inSystem+inMessages, "the control shape must still emit 3 cachePoints")
}

// TestInlinedSystemReminder_MultipleBreakpointsEmitOne — only the last breakpoint in a single
// reminder is emitted. An intermediate marker inside one message closes over nothing the final
// one doesn't, so emitting each would spend the 4-checkpoint budget for no cache benefit.
func TestInlinedSystemReminder_MultipleBreakpointsEmitOne(t *testing.T) {
	input := []schemas.ResponsesMessage{
		leadingSystemMsg(),
		userReminderTextMsg("Analyze this."),
		{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
				cachedTextBlock("reminder one"),
				cachedTextBlock("reminder two"),
				cachedTextBlock("reminder three"),
			}},
		},
	}

	messages, _, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), anthropicModel, input, true)
	require.NoError(t, err)

	_, inMessages := countCachePoints(messages, nil)
	assert.Equal(t, 1, inMessages, "three breakpoints in one reminder must collapse to the last")
}

// TestInlinedSystemReminder_PreservesOneHourTTL — a 1h breakpoint must not be silently downgraded
// to the 5m default; the TTL rides through with the marker.
func TestInlinedSystemReminder_PreservesOneHourTTL(t *testing.T) {
	ttl := "1h"
	reminder := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
		Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
			Type:         schemas.ResponsesInputMessageContentBlockTypeText,
			Text:         schemas.Ptr("reminder"),
			CacheControl: &schemas.CacheControl{Type: "ephemeral", TTL: &ttl},
		}}},
	}
	input := []schemas.ResponsesMessage{
		systemReminderTextMsg("You are Claude Code."),
		userReminderTextMsg("Analyze this."),
		reminder,
	}

	messages, _, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), anthropicModel, input, true)
	require.NoError(t, err)

	var checked bool
	for _, msg := range messages {
		for _, block := range msg.Content {
			if block.CachePoint == nil {
				continue
			}
			require.NotNil(t, block.CachePoint.TTL, "1h TTL was dropped to the 5m default")
			assert.Equal(t, "1h", *block.CachePoint.TTL)
			checked = true
		}
	}
	assert.True(t, checked, "expected a cachePoint carrying the requested TTL")
}

// TestMidconvAnthropicWireShapeReachesBedrockWithAllBreakpoints closes the seam every test above
// leaves open. They all start from []schemas.ResponsesMessage, which is the shape the
// /openai/v1/responses leg sends. Claude Code's actual entry point is /anthropic/v1/messages, and
// that leg runs one extra conversion first: AnthropicMessageRequest.ToBifrostResponsesRequest,
// which folds the top-level `system` ARRAY into a single Responses message carrying two blocks,
// while the Responses leg sends two separate single-block system messages.
//
// Two shapes converging on one converter is exactly where a defect hides from tests written
// against only one of them, and the harness measures the two legs separately for that reason.
// This pins the Anthropic side at the wire level, with no credentials and no spend.
//
// The fixture is anthropicBody(midconv) from
// tests/e2e/api/runners/lib/midconv-system-cache-parity.mjs, shrunk to the structure that matters:
// two cache_control breakpoints in `system`, a third on a mid-conversation role:"system"
// reminder, and a conversation body between them.
func TestMidconvAnthropicWireShapeReachesBedrockWithAllBreakpoints(t *testing.T) {
	const midconvAnthropicBody = `{
	  "model": "bedrock/global.anthropic.claude-haiku-4-5-20251001-v1:0",
	  "max_tokens": 32,
	  "system": [
	    {"type":"text","text":"You are Claude Code. Long stable instructions.","cache_control":{"type":"ephemeral"}},
	    {"type":"text","text":"Tool definitions and environment details.","cache_control":{"type":"ephemeral"}}
	  ],
	  "messages": [
	    {"role":"user","content":[{"type":"text","text":"Document A. Reply OK."}]},
	    {"role":"assistant","content":[{"type":"text","text":"OK."}]},
	    {"role":"user","content":[{"type":"text","text":"Document B. Reply OK."}]},
	    {"role":"assistant","content":[{"type":"text","text":"OK."}]},
	    {"role":"system","content":[{"type":"text","text":"Reminder: stay concise.","cache_control":{"type":"ephemeral"}}]},
	    {"role":"user","content":[{"type":"text","text":"Reply with ONLY the word ACKNOWLEDGED."}]}
	  ]
	}`

	convert := func(t *testing.T) ([]bedrock.BedrockMessage, []bedrock.BedrockSystemMessage) {
		t.Helper()
		var req anthropic.AnthropicMessageRequest
		require.NoError(t, json.Unmarshal([]byte(midconvAnthropicBody), &req))

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bifrostReq := req.ToBifrostResponsesRequest(ctx)
		require.NotNil(t, bifrostReq)

		messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(
			context.Background(), anthropicModel, bifrostReq.Input, true)
		require.NoError(t, err)
		return messages, systemMessages
	}

	messages, systemMessages := convert(t)

	// Same contract the Responses shape is held to: 3 client breakpoints in, 3 cachePoints out,
	// and exactly one of them inside `messages`. All three in `system` caches exactly as badly
	// as two would.
	inSystem, inMessages := countCachePoints(messages, systemMessages)
	assert.Equal(t, 2, inSystem, "the top-level system array's two breakpoints must survive the Anthropic ingress")
	assert.Equal(t, 1, inMessages,
		"the mid-conversation reminder's breakpoint must survive BOTH conversions; losing it in the Anthropic "+
			"ingress pins the cacheable prefix at the system floor just as surely as losing it in the Bedrock egress")

	// Determinism is a cache-correctness property, not a style preference. Prompt caching keys on
	// exact bytes, so a converter that emits semantically equal but byte-unstable output (map
	// iteration order being the classic source) turns every repeat request into a cache write --
	// read 0, write the whole prompt, which is a different failure from a dropped breakpoint and
	// is invisible to any assertion that only counts cachePoints.
	secondMessages, secondSystem := convert(t)
	first, err := providerUtils.MarshalSorted(struct {
		Messages []bedrock.BedrockMessage
		System   []bedrock.BedrockSystemMessage
	}{messages, systemMessages})
	require.NoError(t, err)
	second, err := providerUtils.MarshalSorted(struct {
		Messages []bedrock.BedrockMessage
		System   []bedrock.BedrockSystemMessage
	}{secondMessages, secondSystem})
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second),
		"two conversions of one request body must be byte-identical, or Bedrock re-writes the cache every turn")
}

// TestMidconvNonAnthropicWireShapeKeepsConversePrefixStable reproduces the Claude Code ->
// /anthropic/v1/messages -> bedrock/global.openai.gpt-5.6-luna captures from the field: Claude
// Code (mid-conversation-system beta) appends a trailing role:"system"
// <total_tokens>...</total_tokens> reminder after every turn. On Converse, GPT-5.6 gets Bedrock's
// Implicit Prompt Caching, which is exact-prefix ("Changes to a prompt prefix in subsequent
// requests result in cache misses", AWS prompt-caching guide). Hoisting each new reminder into the
// top-level `system` block grows the very front of the prompt every turn, so the previous turn's
// cache can never be read back. The Converse `system` block must therefore be identical between
// turn N and turn N+1, turn N's messages must be a byte-identical prefix of turn N+1's, and the
// reminders must ride inline as user turns - exactly what the Anthropic-family branch already does.
func TestMidconvNonAnthropicWireShapeKeepsConversePrefixStable(t *testing.T) {
	const model = "bedrock/global.openai.gpt-5.6-luna"
	cc := map[string]any{"type": "ephemeral"}
	reminder := map[string]any{"role": "system", "content": "<total_tokens>15000000 tokens left</total_tokens>"}
	turn1 := []map[string]any{
		{"role": "user", "content": []map[string]any{{"type": "text", "text": "first user turn", "cache_control": cc}}},
		{"role": "system", "content": "Available agent types for the Agent tool: claude, Explore, Plan."},
		{"role": "assistant", "content": "ok"},
		{"role": "user", "content": []map[string]any{{"type": "text", "text": "second user turn", "cache_control": cc}}},
		reminder,
	}
	turn2 := append(append([]map[string]any{}, turn1...),
		map[string]any{"role": "assistant", "content": "done"},
		map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": "third user turn", "cache_control": cc}}},
		reminder,
	)

	convert := func(msgs []map[string]any) *bedrock.BedrockConverseRequest {
		body, err := providerUtils.MarshalSorted(map[string]any{
			"model":      model,
			"max_tokens": 32,
			"system":     []map[string]any{{"type": "text", "text": "You are Claude Code.", "cache_control": cc}},
			"messages":   msgs,
		})
		require.NoError(t, err)
		var ingress anthropic.AnthropicMessageRequest
		require.NoError(t, json.Unmarshal(body, &ingress))
		ctx := &schemas.BifrostContext{}
		req, err := bedrock.ToBedrockResponsesRequest(ctx, ingress.ToBifrostResponsesRequest(ctx))
		require.NoError(t, err)
		return req
	}
	r1, r2 := convert(turn1), convert(turn2)

	// 1. Only the leading system prompt belongs in `system`, and it must not grow between turns.
	require.Len(t, r1.System, 1, "only the leading system prompt belongs in the Converse system block; mid-conversation reminders must not be hoisted")
	assert.Equal(t, r1.System, r2.System, "turn N+1 must not grow the system block (the prefix front) with the new trailing reminder")

	// 2. Turn N's messages are a byte-identical prefix of turn N+1's.
	require.GreaterOrEqual(t, len(r2.Messages), len(r1.Messages))
	m1, err := providerUtils.MarshalSorted(r1.Messages)
	require.NoError(t, err)
	m2, err := providerUtils.MarshalSorted(r2.Messages[:len(r1.Messages)])
	require.NoError(t, err)
	assert.Equal(t, string(m1), string(m2), "Converse messages of turn N must be a byte-identical prefix of turn N+1, or the prompt cache misses every turn")

	// 3. Every reminder rides inline, wrapped, as a user turn - none hoisted.
	inline := 0
	for _, msg := range r2.Messages {
		for _, block := range msg.Content {
			if block.Text != nil && strings.Contains(*block.Text, "<system-reminder>\n<total_tokens>") {
				assert.Equal(t, bedrock.BedrockMessageRoleUser, msg.Role, "inlined reminder must be a user turn")
				inline++
			}
		}
	}
	assert.Equal(t, 2, inline, "both trailing <total_tokens> reminders must be inlined in place, none hoisted into system")
}

// TestToBedrockChatCompletionRequest_MidConversationSystemInlinedForEveryFamily is the Chat
// Completions twin of TestMidconvNonAnthropicWireShapeKeepsConversePrefixStable. convertMessages
// (bedrock/utils.go) hoisted every system/developer message into the Converse `system` block for
// every model, so a client that injects a role:"system" reminder mid-conversation grew the prompt
// front each turn and lost the prefix cache. Claude is in the table because the chat path never
// had the Anthropic-only inlining the Responses path had.
func TestToBedrockChatCompletionRequest_MidConversationSystemInlinedForEveryFamily(t *testing.T) {
	str := func(role schemas.ChatMessageRole, text string) schemas.ChatMessage {
		return schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(text)}}
	}
	const reminder = "<total_tokens>15000000 tokens left</total_tokens>"
	turn1 := []schemas.ChatMessage{
		str(schemas.ChatMessageRoleSystem, "You are Claude Code."),
		str(schemas.ChatMessageRoleUser, "first user turn"),
		str(schemas.ChatMessageRoleSystem, "Available agent types for the Agent tool: claude, Explore, Plan."),
		str(schemas.ChatMessageRoleAssistant, "ok"),
		str(schemas.ChatMessageRoleUser, "second user turn"),
		str(schemas.ChatMessageRoleSystem, reminder),
	}
	turn2 := append(append([]schemas.ChatMessage{}, turn1...),
		str(schemas.ChatMessageRoleAssistant, "done"),
		str(schemas.ChatMessageRoleUser, "third user turn"),
		str(schemas.ChatMessageRoleSystem, reminder),
	)

	for _, model := range []string{"global.openai.gpt-5.6-luna", anthropicModel} {
		t.Run(model, func(t *testing.T) {
			convert := func(msgs []schemas.ChatMessage) *bedrock.BedrockConverseRequest {
				req, err := bedrock.ToBedrockChatCompletionRequest(&schemas.BifrostContext{}, &schemas.BifrostChatRequest{
					Provider: schemas.Bedrock, Model: model, Input: msgs,
				})
				require.NoError(t, err)
				return req
			}
			r1, r2 := convert(turn1), convert(turn2)

			require.Len(t, r1.System, 1, "only the leading system prompt belongs in the Converse system block")
			assert.Equal(t, r1.System, r2.System, "turn N+1 must not grow the system block with the new trailing reminder")

			require.GreaterOrEqual(t, len(r2.Messages), len(r1.Messages))
			m1, err := providerUtils.MarshalSorted(r1.Messages)
			require.NoError(t, err)
			m2, err := providerUtils.MarshalSorted(r2.Messages[:len(r1.Messages)])
			require.NoError(t, err)
			assert.Equal(t, string(m1), string(m2), "Converse messages of turn N must be a byte-identical prefix of turn N+1")

			inline := 0
			for i, msg := range r2.Messages {
				if i > 0 {
					assert.NotEqual(t, r2.Messages[i-1].Role, msg.Role, "Converse turns must alternate; an inlined reminder must fold into the preceding user turn")
				}
				for _, block := range msg.Content {
					if block.Text != nil && strings.Contains(*block.Text, "<system-reminder>\n<total_tokens>") {
						assert.Equal(t, bedrock.BedrockMessageRoleUser, msg.Role)
						inline++
					}
				}
			}
			assert.Equal(t, 2, inline, "both trailing reminders must be inlined in place, none hoisted")
		})
	}
}

// TestToBedrockChatCompletionRequest_ReminderWithNoPrecedingUserTurn — a mid-conversation reminder
// folds into the preceding user turn when there is one. After an assistant turn there is none, and
// giving the reminder a turn of its own then reads as assistant, user, user once the next user
// message lands. Converse turns have to alternate (see convertMessages), so a reminder with no
// user turn behind it folds forward into the next user-role turn instead, and only takes a turn of
// its own when what follows is not one.
func TestToBedrockChatCompletionRequest_ReminderWithNoPrecedingUserTurn(t *testing.T) {
	str := func(role schemas.ChatMessageRole, text string) schemas.ChatMessage {
		return schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(text)}}
	}
	toolResult := func(id, text string) schemas.ChatMessage {
		return schemas.ChatMessage{
			Role:            schemas.ChatMessageRoleTool,
			Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr(text)},
			ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr(id)},
		}
	}
	const reminder = "<total_tokens>15000000 tokens left</total_tokens>"
	const wrapped = "<system-reminder>\n<total_tokens>15000000 tokens left</total_tokens>\n</system-reminder>\n"

	// Leading system prompt, a user turn, an assistant turn, then the reminder. In every case
	// below the reminder has no preceding user turn to fold back into.
	lead := func(tail ...schemas.ChatMessage) []schemas.ChatMessage {
		return append([]schemas.ChatMessage{
			str(schemas.ChatMessageRoleSystem, "You are Claude Code."),
			str(schemas.ChatMessageRoleUser, "first user turn"),
			str(schemas.ChatMessageRoleAssistant, "ok"),
			str(schemas.ChatMessageRoleSystem, reminder),
		}, tail...)
	}

	// textOf returns the text blocks of a turn in order. toolResult blocks carry their text nested
	// inside the result, so they do not show up here.
	textOf := func(msg bedrock.BedrockMessage) []string {
		var out []string
		for _, block := range msg.Content {
			if block.Text != nil {
				out = append(out, *block.Text)
			}
		}
		return out
	}

	for _, tc := range []struct {
		name   string
		input  []schemas.ChatMessage
		verify func(t *testing.T, messages []bedrock.BedrockMessage)
	}{
		{
			name:  "folds into the following user turn",
			input: lead(str(schemas.ChatMessageRoleUser, "second user turn")),
			verify: func(t *testing.T, messages []bedrock.BedrockMessage) {
				require.Len(t, messages, 3, "user, assistant, user")
				last := messages[2]
				assert.Equal(t, bedrock.BedrockMessageRoleUser, last.Role)
				assert.Equal(t, []string{wrapped, "second user turn"}, textOf(last),
					"the reminder leads the turn it preceded in the input")
			},
		},
		{
			name:  "folds into the following tool results",
			input: lead(toolResult("tooluse_Yl388l8ES0G_3TQtDcKq_g", "tool output")),
			verify: func(t *testing.T, messages []bedrock.BedrockMessage) {
				require.Len(t, messages, 3, "user, assistant, user")
				last := messages[2]
				assert.Equal(t, bedrock.BedrockMessageRoleUser, last.Role)
				require.NotEmpty(t, last.Content)
				assert.NotNil(t, last.Content[0].ToolResult,
					"toolResult blocks stay at the front of the turn they answer")
				assert.Equal(t, []string{wrapped}, textOf(last), "the reminder trails the tool results")
			},
		},
		{
			name:  "takes a turn of its own before another assistant turn",
			input: lead(str(schemas.ChatMessageRoleAssistant, "more")),
			verify: func(t *testing.T, messages []bedrock.BedrockMessage) {
				require.Len(t, messages, 4, "user, assistant, user, assistant")
				assert.Equal(t, bedrock.BedrockMessageRoleUser, messages[2].Role)
				assert.Equal(t, []string{wrapped}, textOf(messages[2]))
				assert.Equal(t, bedrock.BedrockMessageRoleAssistant, messages[3].Role)
			},
		},
		{
			name:  "becomes the final turn when nothing follows",
			input: lead(),
			verify: func(t *testing.T, messages []bedrock.BedrockMessage) {
				require.Len(t, messages, 3, "user, assistant, user")
				assert.Equal(t, bedrock.BedrockMessageRoleUser, messages[2].Role)
				assert.Equal(t, []string{wrapped}, textOf(messages[2]))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := bedrock.ToBedrockChatCompletionRequest(&schemas.BifrostContext{}, &schemas.BifrostChatRequest{
				Provider: schemas.Bedrock, Model: anthropicModel, Input: tc.input,
			})
			require.NoError(t, err)
			require.Len(t, req.System, 1, "only the leading system prompt belongs in the Converse system block")
			for i := 1; i < len(req.Messages); i++ {
				assert.NotEqual(t, req.Messages[i-1].Role, req.Messages[i].Role,
					"Converse turns must alternate; a reminder must never open a second user turn")
			}
			tc.verify(t, req.Messages)
		})
	}
}

// TestToBedrockChatCompletionRequest_MidConversationReminderKeepsStandaloneCachePoint — a
// Converse-native client marks a breakpoint with a standalone cachePoint block after the content it
// closes over, not with a cache_control on that content (see the `messages` example under
// https://docs.aws.amazon.com/bedrock/latest/userguide/prompt-caching.html). Both dialects land on
// the same schemas.ChatContentBlock, and convertSystemMessages honours both on the hoisted path, so
// the inlined path has to honour both as well. Dropping the standalone form loses the cache boundary
// the client asked for and re-reads the conversation prefix uncached.
func TestToBedrockChatCompletionRequest_MidConversationReminderKeepsStandaloneCachePoint(t *testing.T) {
	str := func(role schemas.ChatMessageRole, text string) schemas.ChatMessage {
		return schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(text)}}
	}
	textBlock := func(text string) schemas.ChatContentBlock {
		return schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr(text)}
	}
	cachedText := func(text, ttl string) schemas.ChatContentBlock {
		block := textBlock(text)
		block.CacheControl = &schemas.CacheControl{Type: "ephemeral", TTL: schemas.Ptr(ttl)}
		return block
	}
	standalone := func(ttl *string) schemas.ChatContentBlock {
		return schemas.ChatContentBlock{CachePoint: &schemas.CachePoint{Type: "default", TTL: ttl}}
	}
	reminder := func(blocks ...schemas.ChatContentBlock) schemas.ChatMessage {
		return schemas.ChatMessage{
			Role:    schemas.ChatMessageRoleSystem,
			Content: &schemas.ChatMessageContent{ContentBlocks: blocks},
		}
	}

	// cachePointsOf returns every cachePoint in `messages`, paired with whether it terminates a text
	// block. A cachePoint that leads a message or follows another cachePoint closes over nothing.
	cachePointsOf := func(messages []bedrock.BedrockMessage) (points []*bedrock.BedrockCachePoint, allFollowText bool) {
		allFollowText = true
		for _, msg := range messages {
			for i, block := range msg.Content {
				if block.CachePoint == nil {
					continue
				}
				points = append(points, block.CachePoint)
				if i == 0 || msg.Content[i-1].Text == nil {
					allFollowText = false
				}
			}
		}
		return points, allFollowText
	}

	for _, tc := range []struct {
		name    string
		blocks  []schemas.ChatContentBlock
		wantTTL *string
	}{
		{
			name:    "standalone cachePoint block",
			blocks:  []schemas.ChatContentBlock{textBlock("reminder body"), standalone(nil)},
			wantTTL: nil,
		},
		{
			name:    "standalone cachePoint block carries its ttl",
			blocks:  []schemas.ChatContentBlock{textBlock("reminder body"), standalone(schemas.Ptr("1h"))},
			wantTTL: schemas.Ptr("1h"),
		},
		{
			name:    "cache_control on the text block is the control",
			blocks:  []schemas.ChatContentBlock{cachedText("reminder body", "1h")},
			wantTTL: schemas.Ptr("1h"),
		},
		{
			name: "the last breakpoint wins across dialects",
			blocks: []schemas.ChatContentBlock{
				cachedText("reminder one", "5m"),
				textBlock("reminder two"),
				standalone(schemas.Ptr("1h")),
			},
			wantTTL: schemas.Ptr("1h"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := bedrock.ToBedrockChatCompletionRequest(&schemas.BifrostContext{}, &schemas.BifrostChatRequest{
				Provider: schemas.Bedrock,
				Model:    anthropicModel,
				Input: []schemas.ChatMessage{
					str(schemas.ChatMessageRoleSystem, "You are Claude Code."),
					str(schemas.ChatMessageRoleUser, "first user turn"),
					reminder(tc.blocks...),
				},
			})
			require.NoError(t, err)

			points, allFollowText := cachePointsOf(req.Messages)
			require.Len(t, points, 1, "one breakpoint in, exactly one cachePoint out; extra markers burn the 4-checkpoint budget")
			assert.True(t, allFollowText, "a cachePoint must terminate a text block, never lead the message")
			assert.Equal(t, bedrock.BedrockCachePointTypeDefault, points[0].Type)
			if tc.wantTTL == nil {
				assert.Nil(t, points[0].TTL, "no ttl asked for means Bedrock's default 5m applies")
			} else {
				require.NotNil(t, points[0].TTL)
				assert.Equal(t, *tc.wantTTL, *points[0].TTL, "the ttl on the breakpoint must survive the inlining")
			}
		})
	}
}
