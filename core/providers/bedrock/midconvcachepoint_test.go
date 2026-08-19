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

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
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

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
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

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
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

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
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

	messages, systemMessages, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
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
		context.Background(), claudeCodeShape(schemas.ResponsesInputMessageRoleSystem), true)
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
		context.Background(), claudeCodeShape(schemas.ResponsesInputMessageRoleSystem), true)
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
		context.Background(), claudeCodeShape(schemas.ResponsesInputMessageRoleUser), true)
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

	messages, _, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
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

	messages, _, err := bedrock.ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
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
			context.Background(), bifrostReq.Input, true)
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
