package bifrost

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover the seam between provider config and the injector: the dispatch
// helpers in bifrost.go that decide whether an attempt gets breakpoints, and - more
// importantly - guarantee that deciding so never writes back onto the shared request.
//
// The injector's own behaviour is covered in core/providers/utils/promptcache_test.go.
// What can only be tested here is fallback isolation, because req.BifrostRequest
// outlives a single attempt.

func promptCacheOn() *schemas.ProviderConfig {
	return &schemas.ProviderConfig{PromptCache: &schemas.PromptCacheConfig{AutoInject: true}}
}

func responsesReqWithText(text string) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4",
		Input: []schemas.ResponsesMessage{{
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type: schemas.ResponsesInputMessageContentBlockTypeText,
					Text: schemas.Ptr(text),
				}},
			},
		}},
	}
}

func chatReqWithText(text string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{
				ContentBlocks: []schemas.ChatContentBlock{{
					Type: schemas.ChatContentBlockTypeText,
					Text: schemas.Ptr(text),
				}},
			},
		}},
	}
}

func TestPromptCacheResponsesRequest_InjectsWhenEnabled(t *testing.T) {
	req := responsesReqWithText("stable prefix")

	out := promptCacheResponsesRequest(nil, promptCacheOn(), schemas.Anthropic, req)

	require.NotNil(t, out)
	require.NotNil(t, out.Input[0].Content.ContentBlocks[0].CacheControl, "expected a breakpoint on the first cacheable block")
	assert.Equal(t, schemas.CacheControlTypeEphemeral, out.Input[0].Content.ContentBlocks[0].CacheControl.Type)
}

// TestPromptCacheResponsesRequest_DoesNotMutateSharedRequest is the fallback-isolation
// guarantee. req.BifrostRequest survives across retries and fallbacks, so writing an
// injected marker back onto it would let a later attempt against a provider with
// injection disabled inherit a breakpoint the caller never sent. That is the failure
// CodeRabbit flagged on the closed PR #6181, one level further up than the injector.
func TestPromptCacheResponsesRequest_DoesNotMutateSharedRequest(t *testing.T) {
	req := responsesReqWithText("stable prefix")
	sharedContent := req.Input[0].Content

	out := promptCacheResponsesRequest(nil, promptCacheOn(), schemas.Anthropic, req)

	require.NotSame(t, req, out, "an injecting attempt must dispatch a copy, not the shared request")
	assert.Nil(t, req.Input[0].Content.ContentBlocks[0].CacheControl,
		"the shared request was mutated; a fallback would inherit this marker")
	assert.Nil(t, sharedContent.ContentBlocks[0].CacheControl,
		"the shared Content pointer was mutated")
	assert.Equal(t, req.Model, out.Model, "the copy must otherwise be identical")
	assert.Equal(t, req.Provider, out.Provider)
}

// TestPromptCacheResponsesRequest_FallbackDoesNotInherit walks the actual scenario:
// attempt 1 goes to a provider with injection on, attempt 2 falls back to one with it
// off. The second attempt must see a clean request.
func TestPromptCacheResponsesRequest_FallbackDoesNotInherit(t *testing.T) {
	req := responsesReqWithText("stable prefix")

	first := promptCacheResponsesRequest(nil, promptCacheOn(), schemas.Anthropic, req)
	require.NotNil(t, first.Input[0].Content.ContentBlocks[0].CacheControl, "sanity: attempt 1 injected")

	// Attempt 2: a provider whose config has no prompt_cache at all.
	second := promptCacheResponsesRequest(nil, &schemas.ProviderConfig{}, schemas.Anthropic, req)

	assert.Same(t, req, second, "a non-injecting attempt should dispatch the request unchanged")
	assert.Nil(t, second.Input[0].Content.ContentBlocks[0].CacheControl,
		"the fallback attempt inherited a marker from the previous attempt")
}

func TestPromptCacheResponsesRequest_PassesThroughWhenNotApplicable(t *testing.T) {
	cases := []struct {
		name     string
		config   *schemas.ProviderConfig
		provider schemas.ModelProvider
		model    string
	}{
		{"nil config", nil, schemas.Anthropic, "claude-sonnet-4"},
		{"no prompt_cache", &schemas.ProviderConfig{}, schemas.Anthropic, "claude-sonnet-4"},
		{"auto_inject off", &schemas.ProviderConfig{PromptCache: &schemas.PromptCacheConfig{}}, schemas.Anthropic, "claude-sonnet-4"},
		// The capability gate is what makes it safe to call this for every provider:
		// implicit-caching models are never handed a marker they would ignore.
		{"model without explicit caching", promptCacheOn(), schemas.OpenAI, "gpt-4o"},
		{"gemini uses cachedContent, not markers", promptCacheOn(), schemas.Gemini, "gemini-2.5-pro"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := responsesReqWithText("stable prefix")
			req.Provider, req.Model = tc.provider, tc.model

			out := promptCacheResponsesRequest(nil, tc.config, tc.provider, req)

			assert.Same(t, req, out, "expected the request to pass through untouched")
			assert.Nil(t, req.Input[0].Content.ContentBlocks[0].CacheControl)
		})
	}
}

func TestPromptCacheResponsesRequest_NilRequest(t *testing.T) {
	assert.Nil(t, promptCacheResponsesRequest(nil, promptCacheOn(), schemas.Anthropic, nil))
}

func TestPromptCacheChatRequest_InjectsAndIsolates(t *testing.T) {
	req := chatReqWithText("stable prefix")

	out := promptCacheChatRequest(nil, promptCacheOn(), schemas.Anthropic, req)

	require.NotSame(t, req, out)
	require.NotNil(t, out.Input[0].Content.ContentBlocks[0].CacheControl, "expected a breakpoint")
	assert.Nil(t, req.Input[0].Content.ContentBlocks[0].CacheControl,
		"the shared chat request was mutated; a fallback would inherit this marker")
}

func TestPromptCacheChatRequest_PassesThroughWhenDisabled(t *testing.T) {
	req := chatReqWithText("stable prefix")
	assert.Same(t, req, promptCacheChatRequest(nil, &schemas.ProviderConfig{}, schemas.Anthropic, req))
	assert.Nil(t, promptCacheChatRequest(nil, promptCacheOn(), schemas.Anthropic, nil))
}

// TestPromptCacheDispatch_CallerMarkerSurvivesUnchanged proves the two guarantees
// compose: a caller that set its own marker gets the request through untouched, and
// nothing extra is added on top of it.
func TestPromptCacheDispatch_CallerMarkerSurvivesUnchanged(t *testing.T) {
	req := responsesReqWithText("stable prefix")
	req.Input[0].Content.ContentBlocks = append(req.Input[0].Content.ContentBlocks,
		schemas.ResponsesMessageContentBlock{
			Type:         schemas.ResponsesInputMessageContentBlockTypeText,
			Text:         schemas.Ptr("caller marked this"),
			CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral},
		})

	out := promptCacheResponsesRequest(nil, promptCacheOn(), schemas.Anthropic, req)

	blocks := out.Input[0].Content.ContentBlocks
	assert.Nil(t, blocks[0].CacheControl, "no marker may be added when the caller already set one")
	require.NotNil(t, blocks[1].CacheControl, "the caller's own marker must survive")
}

// TestPromptCacheDispatch_HonoursPerRequestOverride checks the override reaches the
// dispatch helpers, and that turning it on for one request still does not write back
// onto the shared request or the shared provider config.
func TestPromptCacheDispatch_HonoursPerRequestOverride(t *testing.T) {
	ctxWith := func(v bool) *schemas.BifrostContext {
		c := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		c.SetValue(schemas.BifrostContextKeyPromptCacheAutoInject, v)
		return c
	}

	t.Run("header turns a configured-off provider on", func(t *testing.T) {
		config := &schemas.ProviderConfig{PromptCache: &schemas.PromptCacheConfig{AutoInject: false}}
		req := responsesReqWithText("stable prefix")

		out := promptCacheResponsesRequest(ctxWith(true), config, schemas.Anthropic, req)

		require.NotSame(t, req, out)
		assert.NotNil(t, out.Input[0].Content.ContentBlocks[0].CacheControl, "override should have enabled injection")
		assert.Nil(t, req.Input[0].Content.ContentBlocks[0].CacheControl, "the shared request was mutated")
		assert.False(t, config.PromptCache.AutoInject, "the shared provider config was written through")
	})

	t.Run("header opts a request out of an enabled provider", func(t *testing.T) {
		req := responsesReqWithText("stable prefix")

		out := promptCacheResponsesRequest(ctxWith(false), promptCacheOn(), schemas.Anthropic, req)

		assert.Same(t, req, out, "an opted-out request should dispatch unchanged")
		assert.Nil(t, req.Input[0].Content.ContentBlocks[0].CacheControl)
	})

	t.Run("header cannot enable an unconfigured provider", func(t *testing.T) {
		req := responsesReqWithText("stable prefix")

		out := promptCacheResponsesRequest(ctxWith(true), &schemas.ProviderConfig{}, schemas.Anthropic, req)

		assert.Same(t, req, out, "a header must not manufacture operator opt-in")
		assert.Nil(t, req.Input[0].Content.ContentBlocks[0].CacheControl)
	})
}
