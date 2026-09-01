package utils

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func autoInject() *schemas.PromptCacheConfig {
	return &schemas.PromptCacheConfig{AutoInject: true}
}

func blockMsg(role schemas.ResponsesMessageRoleType, blocks ...schemas.ResponsesMessageContentBlock) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Role:    schemas.Ptr(role),
		Content: &schemas.ResponsesMessageContent{ContentBlocks: blocks},
	}
}

func strMsg(role schemas.ResponsesMessageRoleType, text string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Role:    schemas.Ptr(role),
		Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr(text)},
	}
}

func textBlock(text string) schemas.ResponsesMessageContentBlock {
	return schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesInputMessageContentBlockTypeText,
		Text: schemas.Ptr(text),
	}
}

// markers returns, per message, the indices of blocks carrying cache_control.
func markers(msgs []schemas.ResponsesMessage) map[int][]int {
	out := map[int][]int{}
	for i := range msgs {
		if msgs[i].Content == nil {
			continue
		}
		for j := range msgs[i].Content.ContentBlocks {
			if msgs[i].Content.ContentBlocks[j].CacheControl != nil {
				out[i] = append(out[i], j)
			}
		}
	}
	return out
}

func countMarkers(msgs []schemas.ResponsesMessage) int {
	n := 0
	for _, idxs := range markers(msgs) {
		n += len(idxs)
	}
	return n
}

// TestInjectResponses_FirstCacheableBlock pins the default strategy. The FIRST
// cacheable block is the prefix an agent loop replays verbatim every turn, so marking
// it keeps the cached region stable and makes turn 2 onward a read. Marking the last
// block instead would move the boundary each turn and bill a cache write every time,
// which is the failure #6180 reported.
func TestInjectResponses_FirstCacheableBlock(t *testing.T) {
	cases := []struct {
		name       string
		blockType  schemas.ResponsesMessageContentBlockType
		wantMarked bool
	}{
		{"input_text", schemas.ResponsesInputMessageContentBlockTypeText, true},
		{"input_image", schemas.ResponsesInputMessageContentBlockTypeImage, true},
		{"input_file", schemas.ResponsesInputMessageContentBlockTypeFile, true},
		{"refusal is not cacheable", schemas.ResponsesOutputMessageContentTypeRefusal, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := []schemas.ResponsesMessage{
				blockMsg(schemas.ResponsesInputMessageRoleUser,
					schemas.ResponsesMessageContentBlock{Type: tc.blockType, Text: schemas.Ptr("a")},
				),
			}
			out := InjectResponsesCacheBreakpoints(autoInject(), in)
			if tc.wantMarked {
				require.Equal(t, 1, countMarkers(out), "expected the block to be marked")
				assert.Equal(t, schemas.CacheControlTypeEphemeral, out[0].Content.ContentBlocks[0].CacheControl.Type)
			} else {
				assert.Equal(t, 0, countMarkers(out), "non-cacheable block must not be marked")
			}
		})
	}
}

func TestInjectResponses_MarksFirstNotLast(t *testing.T) {
	in := []schemas.ResponsesMessage{
		blockMsg(schemas.ResponsesInputMessageRoleSystem, textBlock("stable prefix")),
		blockMsg(schemas.ResponsesInputMessageRoleUser, textBlock("turn 1")),
	}
	out := InjectResponsesCacheBreakpoints(autoInject(), in)

	require.Equal(t, map[int][]int{0: {0}}, markers(out),
		"only the first cacheable block may be marked; a later marker slides with the conversation")
}

// TestInjectResponses_PromotesContentStr covers the bare-string case. A string has
// nowhere to hang a marker, so it becomes a single text block. This is safe only
// because it is deterministic: the same message always renders the same way, so the
// cached prefix stays byte-identical across turns.
func TestInjectResponses_PromotesContentStr(t *testing.T) {
	in := []schemas.ResponsesMessage{strMsg(schemas.ResponsesInputMessageRoleUser, "stable prefix")}

	out := InjectResponsesCacheBreakpoints(autoInject(), in)

	require.Len(t, out[0].Content.ContentBlocks, 1)
	assert.Nil(t, out[0].Content.ContentStr, "the string form must be replaced, not duplicated")
	require.NotNil(t, out[0].Content.ContentBlocks[0].Text)
	assert.Equal(t, "stable prefix", *out[0].Content.ContentBlocks[0].Text, "text must survive promotion")
	assert.NotNil(t, out[0].Content.ContentBlocks[0].CacheControl)
}

// TestInjectResponses_CallerMarkerWins is the "never fight the caller" rule.
// Injection is a default for clients that say nothing, never an override of a client
// that spoke. Both marker dialects count as speaking.
func TestInjectResponses_CallerMarkerWins(t *testing.T) {
	ephemeral := &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral}
	explicit := schemas.Ptr("explicit")

	cases := []struct {
		name string
		in   []schemas.ResponsesMessage
	}{
		{
			name: "block cache_control",
			in: []schemas.ResponsesMessage{
				blockMsg(schemas.ResponsesInputMessageRoleUser, textBlock("a")),
				blockMsg(schemas.ResponsesInputMessageRoleUser,
					schemas.ResponsesMessageContentBlock{
						Type: schemas.ResponsesInputMessageContentBlockTypeText,
						Text: schemas.Ptr("b"), CacheControl: ephemeral,
					}),
			},
		},
		{
			name: "block prompt_cache_breakpoint",
			in: []schemas.ResponsesMessage{
				blockMsg(schemas.ResponsesInputMessageRoleUser, textBlock("a")),
				blockMsg(schemas.ResponsesInputMessageRoleUser,
					schemas.ResponsesMessageContentBlock{
						Type:                  schemas.ResponsesInputMessageContentBlockTypeText,
						Text:                  schemas.Ptr("b"),
						PromptCacheBreakpoint: &schemas.PromptCacheBreakpoint{Mode: explicit},
					}),
			},
		},
		{
			name: "message-level cache_control",
			in: []schemas.ResponsesMessage{
				func() schemas.ResponsesMessage {
					m := blockMsg(schemas.ResponsesInputMessageRoleUser, textBlock("a"))
					m.CacheControl = ephemeral
					return m
				}(),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := InjectResponsesCacheBreakpoints(autoInject(), tc.in)
			assert.Equal(t, &tc.in[0], &out[0], "a request that already expresses caching intent must be returned untouched")
			for i := range out {
				if out[i].Content == nil {
					continue
				}
				for j := range out[i].Content.ContentBlocks {
					if tc.in[i].Content.ContentBlocks[j].CacheControl == nil {
						assert.Nil(t, out[i].Content.ContentBlocks[j].CacheControl,
							"no marker may be added when the caller supplied one elsewhere")
					}
				}
			}
		})
	}
}

// TestInjectResponses_DoesNotMutateInput is the assertion that would have caught the
// defect that sank PR #6181. bifrostReq.Input is shared with the plugin pipeline and
// the fallback chain, so writing a marker in place leaks provider-specific cache
// settings into a retry against a different provider.
func TestInjectResponses_DoesNotMutateInput(t *testing.T) {
	sharedContent := &schemas.ResponsesMessageContent{
		ContentBlocks: []schemas.ResponsesMessageContentBlock{textBlock("stable prefix")},
	}
	in := []schemas.ResponsesMessage{{
		Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: sharedContent,
	}}

	out := InjectResponsesCacheBreakpoints(autoInject(), in)

	require.Equal(t, 1, countMarkers(out), "sanity: the call must actually have injected something")
	assert.Nil(t, in[0].Content.ContentBlocks[0].CacheControl,
		"caller's block was mutated in place - this is the #6181 leak")
	assert.Nil(t, sharedContent.ContentBlocks[0].CacheControl,
		"caller's Content pointer was mutated - aliased state escaped the copy")
	assert.NotSame(t, sharedContent, out[0].Content,
		"a marked message must not share its Content pointer with the caller")
}

func TestInjectResponses_ContentStrPromotionDoesNotMutateInput(t *testing.T) {
	in := []schemas.ResponsesMessage{strMsg(schemas.ResponsesInputMessageRoleUser, "stable prefix")}

	out := InjectResponsesCacheBreakpoints(autoInject(), in)

	require.Len(t, out[0].Content.ContentBlocks, 1)
	require.NotNil(t, in[0].Content.ContentStr, "caller's string form must survive")
	assert.Equal(t, "stable prefix", *in[0].Content.ContentStr)
	assert.Empty(t, in[0].Content.ContentBlocks, "promotion must not write blocks back onto the caller")
}

func TestInjectResponses_Disabled(t *testing.T) {
	in := []schemas.ResponsesMessage{blockMsg(schemas.ResponsesInputMessageRoleUser, textBlock("a"))}

	for _, tc := range []struct {
		name string
		cfg  *schemas.PromptCacheConfig
	}{
		{"nil config", nil},
		{"auto_inject false, no points", &schemas.PromptCacheConfig{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, 0, countMarkers(InjectResponsesCacheBreakpoints(tc.cfg, in)))
		})
	}
}

func TestInjectResponses_TTLCarried(t *testing.T) {
	cfg := &schemas.PromptCacheConfig{AutoInject: true, TTL: schemas.Ptr("1h")}
	in := []schemas.ResponsesMessage{blockMsg(schemas.ResponsesInputMessageRoleUser, textBlock("a"))}

	out := InjectResponsesCacheBreakpoints(cfg, in)

	cc := out[0].Content.ContentBlocks[0].CacheControl
	require.NotNil(t, cc)
	require.NotNil(t, cc.TTL)
	assert.Equal(t, "1h", *cc.TTL)
}

// TestInjectResponses_InjectionPoints covers the LiteLLM-parity override. Points
// replace the default strategy rather than adding to it, and mark the LAST cacheable
// block of each match.
func TestInjectResponses_InjectionPoints(t *testing.T) {
	convo := func() []schemas.ResponsesMessage {
		return []schemas.ResponsesMessage{
			blockMsg(schemas.ResponsesInputMessageRoleSystem, textBlock("sys a"), textBlock("sys b")),
			blockMsg(schemas.ResponsesInputMessageRoleUser, textBlock("u1")),
			blockMsg(schemas.ResponsesInputMessageRoleAssistant, textBlock("a1")),
			blockMsg(schemas.ResponsesInputMessageRoleUser, textBlock("u2")),
		}
	}
	point := func(role string, index *int) schemas.CacheControlInjectionPoint {
		p := schemas.CacheControlInjectionPoint{Location: schemas.CacheControlInjectionLocationMessage, Index: index}
		if role != "" {
			p.Role = schemas.Ptr(role)
		}
		return p
	}

	cases := []struct {
		name   string
		points []schemas.CacheControlInjectionPoint
		want   map[int][]int
	}{
		{
			name:   "role system marks the last block of the system message",
			points: []schemas.CacheControlInjectionPoint{point("system", nil)},
			want:   map[int][]int{0: {1}},
		},
		{
			name:   "role user matches every user message",
			points: []schemas.CacheControlInjectionPoint{point("user", nil)},
			want:   map[int][]int{1: {0}, 3: {0}},
		},
		{
			name:   "index -1 is the last message",
			points: []schemas.CacheControlInjectionPoint{point("", schemas.Ptr(-1))},
			want:   map[int][]int{3: {0}},
		},
		{
			name:   "index 0 is the first message",
			points: []schemas.CacheControlInjectionPoint{point("", schemas.Ptr(0))},
			want:   map[int][]int{0: {1}},
		},
		{
			name:   "index beyond the end matches nothing",
			points: []schemas.CacheControlInjectionPoint{point("", schemas.Ptr(99))},
			want:   map[int][]int{},
		},
		{
			name:   "negative index beyond the start matches nothing",
			points: []schemas.CacheControlInjectionPoint{point("", schemas.Ptr(-99))},
			want:   map[int][]int{},
		},
		{
			name:   "role and index must both match",
			points: []schemas.CacheControlInjectionPoint{point("assistant", schemas.Ptr(1))},
			want:   map[int][]int{},
		},
		{
			name:   "a point with neither role nor index matches nothing",
			points: []schemas.CacheControlInjectionPoint{point("", nil)},
			want:   map[int][]int{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &schemas.PromptCacheConfig{InjectionPoints: tc.points}
			assert.Equal(t, tc.want, markers(InjectResponsesCacheBreakpoints(cfg, convo())))
		})
	}
}

func TestInjectResponses_PointsReplaceAutoInject(t *testing.T) {
	cfg := &schemas.PromptCacheConfig{
		AutoInject: true,
		InjectionPoints: []schemas.CacheControlInjectionPoint{
			{Location: schemas.CacheControlInjectionLocationMessage, Index: schemas.Ptr(-1)},
		},
	}
	in := []schemas.ResponsesMessage{
		blockMsg(schemas.ResponsesInputMessageRoleSystem, textBlock("sys")),
		blockMsg(schemas.ResponsesInputMessageRoleUser, textBlock("u1")),
	}

	assert.Equal(t, map[int][]int{1: {0}}, markers(InjectResponsesCacheBreakpoints(cfg, in)),
		"injection points replace the default strategy; the first block must not also be marked")
}

// TestInjectResponses_NeverExceedsFour guards the Anthropic ceiling. Exceeding it is a
// hard rejection, and relying on the downstream clamp would silently discard the
// earliest marker instead of failing loudly.
func TestInjectResponses_NeverExceedsFour(t *testing.T) {
	in := make([]schemas.ResponsesMessage, 10)
	for i := range in {
		in[i] = blockMsg(schemas.ResponsesInputMessageRoleUser, textBlock("turn"))
	}
	cfg := &schemas.PromptCacheConfig{
		InjectionPoints: []schemas.CacheControlInjectionPoint{
			{Location: schemas.CacheControlInjectionLocationMessage, Role: schemas.Ptr("user")},
		},
	}

	out := InjectResponsesCacheBreakpoints(cfg, in)

	assert.LessOrEqual(t, countMarkers(out), MaxInjectedCacheBreakpoints,
		"ten matching messages must still yield at most four markers")
	assert.Equal(t, MaxInjectedCacheBreakpoints, countMarkers(out), "and it should spend the full budget")
}

// TestPromptCacheInjectionEnabled proves the capability gate, which is what makes it
// safe to call the injector for every provider. Implicit-caching endpoints answer
// false and are never sent a marker they would reject or ignore.
func TestPromptCacheInjectionEnabled(t *testing.T) {
	on := autoInject()

	cases := []struct {
		name     string
		cfg      *schemas.PromptCacheConfig
		provider schemas.ModelProvider
		model    string
		want     bool
	}{
		{"anthropic claude", on, schemas.Anthropic, "claude-sonnet-4", true},
		{"openrouter claude", on, schemas.OpenRouter, "anthropic/claude-sonnet-4", true},
		{"bedrock claude", on, schemas.Bedrock, "anthropic.claude-sonnet-4", true},
		{"vertex claude", on, schemas.Vertex, "claude-sonnet-4", true},
		{"openai gpt-5.6", on, schemas.OpenAI, "gpt-5.6-sol", true},

		{"openai pre-5.6 is implicit", on, schemas.OpenAI, "gpt-4o", false},
		{"openrouter non-claude", on, schemas.OpenRouter, "openai/gpt-4o", false},
		{"vertex gemini uses cachedContent", on, schemas.Vertex, "gemini-2.5-pro", false},
		{"gemini is not markable", on, schemas.Gemini, "gemini-2.5-pro", false},
		{"deepseek is provider-managed", on, schemas.DeepSeek, "deepseek-chat", false},
		{"groq is provider-managed", on, schemas.Groq, "llama-3.3-70b", false},

		{"disabled config short-circuits", &schemas.PromptCacheConfig{}, schemas.Anthropic, "claude-sonnet-4", false},
		{"nil config short-circuits", nil, schemas.Anthropic, "claude-sonnet-4", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, PromptCacheInjectionEnabled(tc.cfg, tc.provider, tc.model))
		})
	}
}

// TestInjectChat_Basics keeps the Chat path honest; it shares every rule with the
// Responses path, so this covers the shape rather than re-testing the semantics.
func TestInjectChat_Basics(t *testing.T) {
	mk := func() []schemas.ChatMessage {
		return []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{
				ContentBlocks: []schemas.ChatContentBlock{{
					Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("stable prefix"),
				}},
			},
		}}
	}

	t.Run("marks the first cacheable block", func(t *testing.T) {
		in := mk()
		out := InjectChatCacheBreakpoints(autoInject(), in)
		require.NotNil(t, out[0].Content.ContentBlocks[0].CacheControl)
		assert.Nil(t, in[0].Content.ContentBlocks[0].CacheControl, "caller's input was mutated")
	})

	t.Run("caller marker wins", func(t *testing.T) {
		in := mk()
		in[0].Content.ContentBlocks[0].CacheControl = &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral}
		out := InjectChatCacheBreakpoints(autoInject(), in)
		assert.Equal(t, 1, func() int {
			n := 0
			for _, b := range out[0].Content.ContentBlocks {
				if b.CacheControl != nil {
					n++
				}
			}
			return n
		}(), "no extra marker may be added")
	})
}
