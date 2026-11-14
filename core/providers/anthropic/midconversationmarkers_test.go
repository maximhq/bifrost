package anthropic

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		{"claude 5 trims", "global.anthropic.claude-haiku-5-5", nil, trimmed},
		{"claude 4 keeps", "global.anthropic.claude-haiku-4-5-20251001-v1:0", nil, marked},
		{"unrecognised model keeps", "global.anthropic.claude-opus-6", nil, marked},
		{"datasheet trims an unrecognised model", "global.anthropic.claude-opus-6", &schemas.ModelCapabilities{StripsUserTurnTrailingWhitespace: new(true)}, trimmed},
		{"datasheet keeps a claude 5 model", "global.anthropic.claude-haiku-5-5", &schemas.ModelCapabilities{StripsUserTurnTrailingWhitespace: new(false)}, marked},
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
	for _, tc := range []struct{ name, model, want string }{
		{"claude 5", "global.anthropic.claude-haiku-5-5", strings.TrimSuffix(marked, "\n")},
		{"claude 4", "global.anthropic.claude-haiku-4-5-20251001-v1:0", marked},
	} {
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
			second := convert([]schemas.ChatMessage{lead, userTurn(nil), reminder,
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
