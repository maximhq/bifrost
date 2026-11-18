package anthropic

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// These pin the two claims the doc comment on inlineMidConversationSystem makes about
// cache markers, which are not the same claim and were once conflated into "lossless":
//
//   - it never SYNTHESIZES a marker, because inventing one spends a checkpoint out of a
//     budget of four and changes the caller's billing profile behind their back; and
//   - it deliberately DROPS intermediate markers, keeping only the last one in a message, on
//     the block the caller put it on, since an earlier marker buys nothing the last one does
//     not already cover.
//
// Only the first is a losslessness guarantee. The second is a documented, intentional loss.

func plainTextBlock(text string) AnthropicContentBlock {
	return AnthropicContentBlock{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr(text)}
}

// cachedBlockTTL gives a marker a distinguishing payload, so an assertion can tell the
// caller's own marker apart from a freshly minted default one. Without that, a
// regression that synthesized a new marker in the right position would still pass.
func cachedBlockTTL(text, ttl string) AnthropicContentBlock {
	return AnthropicContentBlock{
		Type:         AnthropicContentBlockTypeText,
		Text:         schemas.Ptr(text),
		CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: schemas.Ptr(ttl)},
	}
}

func markedBlockCount(msg *AnthropicMessage) int {
	n := 0
	for _, b := range msg.Content.ContentBlocks {
		if b.CacheControl != nil {
			n++
		}
	}
	return n
}

func TestInlineMidConversationSystem_NeverSynthesizesAMarker(t *testing.T) {
	t.Run("string form", func(t *testing.T) {
		msg := inlineMidConversationSystem(&AnthropicContent{ContentStr: schemas.Ptr("be concise")})
		require.NotNil(t, msg)
		assert.Zero(t, markedBlockCount(msg),
			"the string form carries no marker, so the inlined turn must carry none either")
	})

	t.Run("block form with no markers", func(t *testing.T) {
		msg := inlineMidConversationSystem(&AnthropicContent{ContentBlocks: []AnthropicContentBlock{
			plainTextBlock("first"),
			plainTextBlock("second"),
		}})
		require.NotNil(t, msg)
		require.Len(t, msg.Content.ContentBlocks, 2)
		assert.Zero(t, markedBlockCount(msg),
			"an unmarked system block must not acquire a breakpoint in translation")
	})
}

func TestInlineMidConversationSystem_KeepsOnlyTheLastMarker(t *testing.T) {
	t.Run("intermediate markers are dropped", func(t *testing.T) {
		// Distinct TTLs so the surviving marker can be identified, not merely counted.
		first := cachedBlockTTL("first", "1h")
		last := cachedBlock("third")
		msg := inlineMidConversationSystem(&AnthropicContent{ContentBlocks: []AnthropicContentBlock{
			first,
			plainTextBlock("second"),
			last,
		}})
		require.NotNil(t, msg)
		require.Len(t, msg.Content.ContentBlocks, 3)
		assert.Equal(t, 1, markedBlockCount(msg), "a message may spend at most one checkpoint here")

		surviving := msg.Content.ContentBlocks[2].CacheControl
		require.NotNil(t, surviving, "the surviving marker is the last one")
		assert.Equal(t, *last.CacheControl, *surviving,
			"the caller's LAST marker must survive verbatim, not a freshly synthesized one")
		assert.NotEqual(t, *first.CacheControl, *surviving, "the earlier marker must not be the one kept")
	})

	// Claude Code marks its token-budget reminder and sends a short nudge after it, which it
	// drops on the next turn. A marker moved onto the nudge writes a checkpoint covering text
	// no later request carries, so every turn re-writes the conversation instead of reading it.
	t.Run("the marker stays on the block the caller marked", func(t *testing.T) {
		marked := cachedBlockTTL("first", "1h")
		msg := inlineMidConversationSystem(&AnthropicContent{ContentBlocks: []AnthropicContentBlock{
			marked,
			plainTextBlock("second"),
		}})
		require.NotNil(t, msg)
		require.Len(t, msg.Content.ContentBlocks, 2)
		assert.Equal(t, 1, markedBlockCount(msg))

		kept := msg.Content.ContentBlocks[0].CacheControl
		require.NotNil(t, kept, "the marker stays on the block the caller marked")
		assert.Equal(t, *marked.CacheControl, *kept, "the caller's marker is kept verbatim, TTL included")
		assert.Nil(t, msg.Content.ContentBlocks[1].CacheControl, "the trailing block must not inherit the marker")
	})
}

// A marked block whose text ends in whitespace stops matching once the content after it in the
// same turn changes: live on Bedrock InvokeModel and Converse, a "\n"-terminated reminder followed
// by a trailer read 0 cached tokens on the next turn, the same text without it read the full prefix.
func TestInlineMidConversationSystem_EnvelopeHasNoTrailingWhitespace(t *testing.T) {
	msg := inlineMidConversationSystem(&AnthropicContent{ContentBlocks: []AnthropicContentBlock{
		cachedBlock("<total_tokens>812345 tokens left</total_tokens>"),
		plainTextBlock("keep going"),
	}})
	require.NotNil(t, msg)
	require.Len(t, msg.Content.ContentBlocks, 2)
	assert.Equal(t, "<system-reminder>\n<total_tokens>812345 tokens left</total_tokens>\n</system-reminder>",
		*msg.Content.ContentBlocks[0].Text)
	assert.Equal(t, "<system-reminder>\nkeep going\n</system-reminder>", *msg.Content.ContentBlocks[1].Text)
}

// Claude 5 models drop trailing whitespace from the text that ends a user turn but keep it
// mid-turn. A marked user block ending in "\n" (Claude Code's own <system-reminder> blocks do) is
// mid-turn while an inlined reminder follows it and ends the turn once the reminder is dropped, so
// the next turn read 0 cached tokens, live on Bedrock and Anthropic; trimmed, it reads the prefix.
// Claude 4 models keep the whitespace everywhere, so there the block must go out as sent: trimmed,
// it stops matching its own history (live on Haiku 4.5 and Sonnet 4.6).
func TestInlinedSystemReminder_TrimsTheUserTextItJoins(t *testing.T) {
	const marked = "<system-reminder>\ncontext\n</system-reminder>\n"
	const nudge = "keep going"
	trimmed := strings.TrimSuffix(marked, "\n")
	models := []struct {
		name, model string
		row         *schemas.ModelCapabilities // datasheet row; nil ⇒ name detection
		want        string
	}{
		{"claude 4 keeps", "global.anthropic.claude-haiku-4-5-20251001-v1:0", nil, marked},
		{"unrecognised model keeps", "global.anthropic.claude-opus-6", nil, marked},
		{"datasheet trims an unrecognised model", "global.anthropic.claude-opus-6", &schemas.ModelCapabilities{StripsUserTurnTrailingWhitespace: new(true)}, trimmed},
	}

	for _, tc := range models {
		t.Run(tc.name, func(t *testing.T) {
			if tc.row != nil {
				schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return tc.row })
				t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
			}
			t.Run("anthropic route", func(t *testing.T) {
				userBlocks := []AnthropicContentBlock{cachedBlock(marked)}
				outMsgs, _ := roundTrip(t, []AnthropicMessage{
					anthMsg(AnthropicMessageRoleUser, "Hello"),
					anthMsg(AnthropicMessageRoleAssistant, "Hi!"),
					{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: userBlocks}},
					anthMsg(AnthropicMessageRoleSystem, nudge),
				}, systemStr("You are Claude Code."), schemas.Bedrock, tc.model)

				var found bool
				for _, m := range outMsgs {
					for _, b := range m.Content.ContentBlocks {
						if b.Text != nil && strings.HasPrefix(*b.Text, "<system-reminder>\ncontext") {
							found = true
							assert.Equal(t, tc.want, *b.Text)
							assert.NotNil(t, b.CacheControl, "the caller's marker stays on the block")
						}
					}
				}
				require.True(t, found, "the marked user block is missing; roles = %q", roleSeq(outMsgs))
				assertInlined(t, outMsgs, nil, nudge)
				assert.Equal(t, marked, *userBlocks[0].Text, "the caller's request must not be modified")
			})

			t.Run("chat", func(t *testing.T) {
				user := marked
				ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
				defer cancel()
				result, err := ToAnthropicChatRequest(ctx, &schemas.BifrostChatRequest{
					Provider: schemas.Bedrock,
					Model:    tc.model,
					Input: []schemas.ChatMessage{
						{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Initial.")}},
						{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
							{Type: schemas.ChatContentBlockTypeText, Text: &user, CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral}},
						}}},
						{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(nudge)}},
					},
					Params: &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(1024)},
				})
				require.NoError(t, err)
				require.NotEmpty(t, result.Messages)
				assert.Equal(t, []string{tc.want}, textBlocks(&result.Messages[0].Content))
				assertInlinedReminder(t, result, nudge)
				assert.Equal(t, marked, user, "the caller's request must not be modified")
			})
		})
	}

	// The marker does not decide the trim: an unmarked block is inside a later checkpoint's prefix.
	t.Run("trimmed whatever the marker", func(t *testing.T) {
		for name, msgs := range map[string][]AnthropicMessage{
			"marked text":   {{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{cachedBlock("done\n")}}}},
			"unmarked text": {{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{plainTextBlock("done\n")}}}},
			"string form":   {anthMsg(AnthropicMessageRoleUser, "done\n")},
		} {
			t.Run(name, func(t *testing.T) {
				trimUserTurnTail(msgs)
				assert.Equal(t, []string{"done"}, textBlocks(&msgs[0].Content))
			})
		}
	})

	t.Run("left alone", func(t *testing.T) {
		for name, msgs := range map[string][]AnthropicMessage{
			"no trailing whitespace": {{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{cachedBlock("done")}}}},
			"whitespace-only text":   {{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{cachedBlock(" \n")}}}},
			"assistant turn":         {{Role: AnthropicMessageRoleAssistant, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{cachedBlock("done\n")}}}},
			"tool_result last": {{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{
				cachedBlock("done\n"),
				{Type: AnthropicContentBlockTypeToolResult, ToolUseID: schemas.Ptr("toolu_1")},
			}}}},
		} {
			t.Run(name, func(t *testing.T) {
				before := textBlocks(&msgs[0].Content)
				trimUserTurnTail(msgs)
				assert.Equal(t, before, textBlocks(&msgs[0].Content))
			})
		}
	})
}

// A client that keeps the reminder in history and moves the breakpoint to a later turn sends the
// earlier block unmarked, still followed by the reminder. Its bytes must not change between the
// request that marked it and the next one, or the checkpoint written at it never matches again.
func TestInlinedSystemReminder_TrimIsStableAsTheBreakpointMoves(t *testing.T) {
	const marked = "<system-reminder>\ncontext\n</system-reminder>\n"
	const nudge = "keep going"
	for _, tc := range []struct {
		name, model, want string
		row               *schemas.ModelCapabilities // datasheet row; nil ⇒ name detection
	}{
		{"datasheet trims", "global.anthropic.claude-opus-6", strings.TrimSuffix(marked, "\n"), &schemas.ModelCapabilities{StripsUserTurnTrailingWhitespace: new(true)}},
		{"claude 4", "global.anthropic.claude-haiku-4-5-20251001-v1:0", marked, nil},
	} {
		if tc.row != nil {
			schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return tc.row })
		} else {
			schemas.SetCapabilityResolver(nil)
		}
		t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
		t.Run(tc.name+"/anthropic route", func(t *testing.T) {
			history := []AnthropicMessage{anthMsg(AnthropicMessageRoleUser, "Hello"), anthMsg(AnthropicMessageRoleAssistant, "Hi!")}
			first := append(slices.Clone(history),
				AnthropicMessage{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{cachedBlock(marked)}}},
				anthMsg(AnthropicMessageRoleSystem, nudge),
			)
			second := append(slices.Clone(history),
				AnthropicMessage{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{plainTextBlock(marked)}}},
				anthMsg(AnthropicMessageRoleSystem, nudge),
				anthMsg(AnthropicMessageRoleAssistant, "ACKNOWLEDGED"),
				AnthropicMessage{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{cachedBlock("Turn 2.")}}},
			)
			blockText := func(msgs []AnthropicMessage) string {
				t.Helper()
				for _, m := range msgs {
					for _, b := range m.Content.ContentBlocks {
						if b.Text != nil && strings.HasPrefix(*b.Text, "<system-reminder>\ncontext") {
							return *b.Text
						}
					}
				}
				t.Fatalf("the reminder-bearing user block is missing; roles = %q", roleSeq(msgs))
				return ""
			}
			out1, _ := roundTrip(t, first, systemStr("You are Claude Code."), schemas.Bedrock, tc.model)
			out2, _ := roundTrip(t, second, systemStr("You are Claude Code."), schemas.Bedrock, tc.model)
			assert.Equal(t, tc.want, blockText(out1))
			assert.Equal(t, blockText(out1), blockText(out2), "the block's bytes changed once the breakpoint moved off it")
		})

		t.Run(tc.name+"/chat", func(t *testing.T) {
			convert := func(input []schemas.ChatMessage) string {
				t.Helper()
				ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
				defer cancel()
				result, err := ToAnthropicChatRequest(ctx, &schemas.BifrostChatRequest{
					Provider: schemas.Bedrock,
					Model:    tc.model,
					Input:    input,
					Params:   &schemas.ChatParameters{MaxCompletionTokens: schemas.Ptr(1024)},
				})
				require.NoError(t, err)
				require.NotEmpty(t, result.Messages)
				got := textBlocks(&result.Messages[0].Content)
				require.Len(t, got, 1)
				return got[0]
			}
			userTurn := func(cc *schemas.CacheControl) schemas.ChatMessage {
				return schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr(marked), CacheControl: cc},
				}}}
			}
			lead := schemas.ChatMessage{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Initial.")}}
			reminder := schemas.ChatMessage{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(nudge)}}
			first := convert([]schemas.ChatMessage{lead, userTurn(&schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral}), reminder})
			second := convert([]schemas.ChatMessage{
				lead, userTurn(nil), reminder,
				{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("ACKNOWLEDGED")}},
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("Turn 2."), CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral}},
				}}},
			})
			assert.Equal(t, tc.want, first)
			assert.Equal(t, first, second, "the block's bytes changed once the breakpoint moved off it")
		})
	}
}

func TestDefaultStripsUserTurnTrailingWhitespace(t *testing.T) {
	for model, want := range map[string]bool{
		"global.anthropic.claude-haiku-5-5":               true,
		"us.anthropic.claude-opus-5-5":                    true,
		"claude-sonnet-5-5":                               true,
		"us.anthropic.claude-fable-5-1":                   true,
		"global.anthropic.claude-haiku-4-5-20251001-v1:0": false,
		"global.anthropic.claude-sonnet-4-6":              false,
		"claude-opus-4-8":                                 false,
		"deepseek-v4":                                     false,
	} {
		assert.Equal(t, want, DefaultStripsUserTurnTrailingWhitespace(model), model)
	}
}

// setMidConvSystemRow installs row as the datasheet record of every model of the given
// providers.
func setMidConvSystemRow(t *testing.T, row schemas.ModelCapabilities, providers ...schemas.ModelProvider) {
	t.Helper()
	providerUtils.SetCapabilityResolver(func(provider schemas.ModelProvider, _ string) *schemas.ModelCapabilities {
		if slices.Contains(providers, provider) {
			return &row
		}
		return nil
	})
	t.Cleanup(func() { providerUtils.SetCapabilityResolver(nil) })
}

// holdMidConvSystemFalse holds the older supports_mid_conversation_system_messages key false,
// the datasheet state that keeps released gateways inlining.
func holdMidConvSystemFalse(t *testing.T, providers ...schemas.ModelProvider) {
	t.Helper()
	no := false
	setMidConvSystemRow(t, schemas.ModelCapabilities{SupportsMidConversationSystem: &no}, providers...)
}

// midConvTurnBody renders a Claude Code-shaped /v1/messages body: the given messages JSON,
// adaptive thinking, and a deferred tool behind tool search (the shape that takes Bedrock
// InvokeModel).
func midConvTurnBody(model, messages string) string {
	return `{"model":"` + model + `","max_tokens":64,"thinking":{"type":"adaptive"},
		"tools":[{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"},
			{"name":"read","description":"read a file","input_schema":{"type":"object"},"defer_loading":true}],
		"messages":[` + messages + `]}`
}

// reminderForm returns the role and text the upstream body carries the reminder in: "system"
// with the raw text when native, "user" with the <system-reminder> envelope when inlined.
func reminderForm(t *testing.T, body []byte, reminder string) (string, string) {
	t.Helper()
	for _, m := range gjson.GetBytes(body, "messages").Array() {
		for _, b := range m.Get("content").Array() {
			if text := b.Get("text").String(); strings.Contains(text, reminder) {
				return m.Get("role").String(), text
			}
		}
		if text := m.Get("content").String(); m.Get("content").Type == gjson.String && strings.Contains(text, reminder) {
			return m.Get("role").String(), text
		}
	}
	t.Fatalf("reminder %q not found in upstream messages: %s", reminder, gjson.GetBytes(body, "messages").Raw)
	return "", ""
}

// The reminder that ends turn N must reach the provider in the same form at turn N+1, after
// the model's reply is appended behind it. A reply that opens with tool_use is lowered to a
// role-less function_call item, which the old input-lookahead read as "not an assistant" and
// so inlined a reminder it had sent natively one turn earlier; the change of form broke the
// cached prefix at that reminder on every turn (Bedrock InvokeModel, Opus 5.5, 2026-11-16).
// The older key is covered false (as production holds it for released gateways) and true (the
// 2026-11-16 state); this build ignores it and goes native. native=false keeps the reminder inlined,
// and inlined must be just as stable. Opus 5.6 stands for a version the datasheet has not seen.
func TestMidConvSystemReminder_FormStableAcrossTurns(t *testing.T) {
	const (
		r1 = "R1: the date is 2026-11-18."
		r2 = "R2: a file changed."
	)
	head := `{"role":"user","content":"q1"},{"role":"assistant","content":[{"type":"text","text":"a1"}]},
		{"role":"user","content":"q2"},
		{"role":"system","content":[{"type":"text","text":"` + r1 + `","cache_control":{"type":"ephemeral"}}]}`
	toolResultAndNextReminder := `,{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]},
		{"role":"system","content":[{"type":"text","text":"` + r2 + `","cache_control":{"type":"ephemeral"}}]}`
	replies := map[string]string{
		"tool_use": `,{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read","input":{}}]}` + toolResultAndNextReminder,
		"thinking_then_tool_use": `,{"role":"assistant","content":[{"type":"thinking","thinking":"look","signature":"sig"},
			{"type":"tool_use","id":"toolu_1","name":"read","input":{}}]}` + toolResultAndNextReminder,
		"text": `,{"role":"assistant","content":[{"type":"text","text":"a2"}]},{"role":"user","content":"q3"},
			{"role":"system","content":[{"type":"text","text":"` + r2 + `","cache_control":{"type":"ephemeral"}}]}`,
	}

	for _, host := range []struct {
		provider schemas.ModelProvider
		model    string
	}{
		{schemas.Bedrock, "bedrock/us.anthropic.claude-opus-5-5"},
		{schemas.Vertex, "vertex/claude-opus-5-5"},
		{schemas.Azure, "azure/claude-opus-5-5"},
		{schemas.Bedrock, "bedrock/us.anthropic.claude-opus-5-6"},
	} {
		no, yes := false, true
		rows := []struct {
			name string
			row  schemas.ModelCapabilities
			role string
		}{
			{"older=false", schemas.ModelCapabilities{SupportsMidConversationSystem: &no}, "system"},
			{"older=true", schemas.ModelCapabilities{SupportsMidConversationSystem: &yes}, "system"},
			{"native=false", schemas.ModelCapabilities{SupportsNativeMidConversationSystem: &no}, "user"},
		}
		for name, reply := range replies {
			for _, row := range rows {
				t.Run(fmt.Sprintf("%s/%s/%s/%s", host.provider, host.model, name, row.name), func(t *testing.T) {
					setMidConvSystemRow(t, row.row, host.provider)
					turnN := requestRoundTrip(t, midConvTurnBody(host.model, head))
					turnN1 := requestRoundTrip(t, midConvTurnBody(host.model, head+reply))
					// The exact bytes of each form: the original text when native, the full envelope when wrapped.
					want := func(text string) string {
						if row.role == "user" {
							return inlinedText(text)
						}
						return text
					}

					roleN, textN := reminderForm(t, turnN, r1)
					roleN1, textN1 := reminderForm(t, turnN1, r1)
					assert.Equal(t, row.role, roleN, "turn N sent the tail reminder in the wrong form")
					assert.Equal(t, want(r1), textN, "turn N sent the tail reminder with the wrong text")
					assert.Equal(t, roleN, roleN1, "reminder changed role between turns: %s", gjson.GetBytes(turnN1, "messages").Raw)
					assert.Equal(t, textN, textN1, "reminder changed text between turns")
					role2, text2 := reminderForm(t, turnN1, r2)
					assert.Equal(t, row.role, role2, "the new tail reminder is in the wrong form")
					assert.Equal(t, want(r2), text2, "the new tail reminder has the wrong text")
				})
			}
		}
	}
}

// A reminder followed by a real user turn breaks Anthropic's placement rule, so it is still
// inlined even on a model that takes the native form; the request must not 400.
func TestMidConvSystemReminder_FollowedByUserIsInlined(t *testing.T) {
	holdMidConvSystemFalse(t, schemas.Bedrock)
	const r = "R: be brief."
	body := requestRoundTrip(t, midConvTurnBody("bedrock/us.anthropic.claude-opus-5-5",
		`{"role":"user","content":"q1"},{"role":"assistant","content":[{"type":"text","text":"a1"}]},
		{"role":"user","content":"q2"},{"role":"system","content":"`+r+`"},{"role":"user","content":"q3"}`))
	role, text := reminderForm(t, body, r)
	assert.Equal(t, "user", role)
	assert.Equal(t, inlinedText(r), text)
}

// The rolling marker stays on the reminder the caller marked, with the caller's TTL, in both
// turns: the placement pass must not move, drop or mint a breakpoint.
func TestMidConvSystemReminder_MarkerKeptAcrossTurns(t *testing.T) {
	holdMidConvSystemFalse(t, schemas.Bedrock)
	const r1 = "R1: marked."
	head := `{"role":"user","content":"q1"},{"role":"assistant","content":[{"type":"text","text":"a1"}]},{"role":"user","content":"q2"},
		{"role":"system","content":[{"type":"text","text":"` + r1 + `","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`
	next := `,{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}`
	for _, raw := range []string{head, head + next} {
		body := requestRoundTrip(t, midConvTurnBody("bedrock/us.anthropic.claude-opus-5-5", raw))
		var found bool
		for _, m := range gjson.GetBytes(body, "messages").Array() {
			for _, b := range m.Get("content").Array() {
				if b.Get("text").String() == r1 {
					found = true
					assert.Equal(t, "system", m.Get("role").String())
					assert.Equal(t, "1h", b.Get("cache_control.ttl").String(), "marker lost or replaced: %s", b.Raw)
				}
			}
		}
		require.True(t, found, "native reminder not found: %s", gjson.GetBytes(body, "messages").Raw)
	}
}

// sysMsg and roleMsg build emitted messages for driving enforceMidConversationSystemPlacement.
func sysMsg(text string) AnthropicMessage {
	return AnthropicMessage{Role: AnthropicMessageRoleSystem, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{plainTextBlock(text)}}}
}

func roleMsg(role AnthropicMessageRole, blocks ...AnthropicContentBlock) AnthropicMessage {
	return AnthropicMessage{Role: role, Content: AnthropicContent{ContentBlocks: blocks}}
}

func effortOnlyMsg(effort string) AnthropicMessage {
	return AnthropicMessage{
		Role:         AnthropicMessageRoleSystem,
		Content:      AnthropicContent{ContentBlocks: []AnthropicContentBlock{}},
		OutputConfig: &AnthropicMessageOutputConfig{Effort: schemas.Ptr(effort)},
	}
}

func TestEnforceMidConversationSystemPlacement(t *testing.T) {
	user := roleMsg(AnthropicMessageRoleUser, plainTextBlock("q"))
	asstText := roleMsg(AnthropicMessageRoleAssistant, plainTextBlock("a"))
	asstToolUse := roleMsg(AnthropicMessageRoleAssistant, AnthropicContentBlock{Type: AnthropicContentBlockTypeToolUse, ID: schemas.Ptr("toolu_1"), Name: schemas.Ptr("read")})
	asstServerResult := roleMsg(AnthropicMessageRoleAssistant,
		AnthropicContentBlock{Type: AnthropicContentBlockTypeServerToolUse, ID: schemas.Ptr("srvtoolu_1"), Name: schemas.Ptr("web_search")},
		AnthropicContentBlock{Type: AnthropicContentBlockTypeWebSearchToolResult, ToolUseID: schemas.Ptr("srvtoolu_1")})

	for _, tc := range []struct {
		name string
		in   []AnthropicMessage
		want string // role sequence after the pass
	}{
		{"tail after user stays native", []AnthropicMessage{user, asstText, user, sysMsg("r")}, "user,assistant,user,system"},
		{"before tool_use reply stays native", []AnthropicMessage{user, sysMsg("r"), asstToolUse}, "user,system,assistant"},
		{"followed by user is inlined", []AnthropicMessage{user, sysMsg("r"), user}, "user,user,user"},
		{"after plain assistant is inlined", []AnthropicMessage{user, asstText, sysMsg("r")}, "user,assistant,user"},
		{"after server tool result stays native", []AnthropicMessage{user, asstServerResult, sysMsg("r")}, "user,assistant,system"},
		{"run judged as a whole", []AnthropicMessage{user, sysMsg("r1"), sysMsg("r2"), asstText}, "user,system,system,assistant"},
		{"invalid run keeps effort-only member", []AnthropicMessage{user, asstText, effortOnlyMsg("low"), sysMsg("r"), user}, "user,assistant,system,user,user"},
		{"effort-only alone is exempt", []AnthropicMessage{effortOnlyMsg("low"), user, asstText}, "system,user,assistant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := enforceMidConversationSystemPlacement(slices.Clone(tc.in), false)
			assert.Equal(t, tc.want, roleSeq(got))
			for _, m := range got {
				if m.Role == AnthropicMessageRoleUser && len(m.Content.ContentBlocks) > 0 && m.Content.ContentBlocks[0].Text != nil &&
					strings.HasPrefix(*m.Content.ContentBlocks[0].Text, "<system-reminder>") {
					assert.Contains(t, []string{inlinedText("r"), inlinedText("r1")}, *m.Content.ContentBlocks[0].Text)
				}
			}
		})
	}
}

// A text-bearing system message that carries an override keeps the effort when it is inlined:
// the override goes out first as an effort-only system message, as the inline branch sends it.
func TestEnforceMidConversationSystemPlacement_InlinedKeepsEffort(t *testing.T) {
	r := sysMsg("r")
	r.OutputConfig = &AnthropicMessageOutputConfig{Effort: schemas.Ptr("high")}
	got := enforceMidConversationSystemPlacement([]AnthropicMessage{
		roleMsg(AnthropicMessageRoleUser, plainTextBlock("q")), r, roleMsg(AnthropicMessageRoleUser, plainTextBlock("q2")),
	}, false)
	require.Equal(t, "user,system,user,user", roleSeq(got))
	require.NotNil(t, got[1].OutputConfig)
	assert.Equal(t, "high", *got[1].OutputConfig.Effort)
	assert.True(t, isEffortOnlySystemMessage(&got[1]))
	assert.Equal(t, inlinedText("r"), *got[2].Content.ContentBlocks[0].Text)
}

// When every run is placed legally the pass returns its input without allocating, so requests
// with reminders in a valid place, and requests with none, pay only the scan.
func TestEnforceMidConversationSystemPlacement_NoAllocWhenValid(t *testing.T) {
	msgs := []AnthropicMessage{
		roleMsg(AnthropicMessageRoleUser, plainTextBlock("q")),
		sysMsg("r"),
		roleMsg(AnthropicMessageRoleAssistant, plainTextBlock("a")),
		roleMsg(AnthropicMessageRoleUser, plainTextBlock("q2")),
		sysMsg("r2"),
	}
	allocs := testing.AllocsPerRun(100, func() {
		_ = enforceMidConversationSystemPlacement(msgs, true)
	})
	assert.Zero(t, allocs)
	got := enforceMidConversationSystemPlacement(msgs, true)
	assert.Same(t, &msgs[0], &got[0])
}
