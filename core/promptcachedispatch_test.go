package bifrost

import (
	"context"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
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

// stubProvider satisfies schemas.Provider for the dispatch seam; only GetProviderKey is
// ever called by prepareResponsesRequest. Everything else panics on the nil embed.
type stubProvider struct {
	schemas.Provider
	key schemas.ModelProvider
}

func (s stubProvider) GetProviderKey() schemas.ModelProvider { return s.key }

// namespaceCapableStub is a provider that answers the namespace question itself, the
// way Bedrock does from its surface resolver.
type namespaceCapableStub struct {
	stubProvider
	supported bool
}

func (s namespaceCapableStub) SupportsResponsesNamespaceTools(*schemas.BifrostContext, schemas.Key, string) bool {
	return s.supported
}

func responsesReqWithNamespaces(provider schemas.ModelProvider) *schemas.BifrostResponsesRequest {
	req := responsesReqWithText("Test the tools")
	req.Provider = provider
	nested := func(name string) schemas.ResponsesTool {
		return schemas.ResponsesTool{Type: schemas.ResponsesToolTypeFunction, Name: new(name), ResponsesToolFunction: &schemas.ResponsesToolFunction{}}
	}
	req.Params = &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{
		{Type: schemas.ResponsesToolTypeNamespace, Name: new("namespace_a"), ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{nested("js")}}},
		{Type: schemas.ResponsesToolTypeNamespace, Name: new("namespace_b"), ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{nested("js")}}},
	}}
	return req
}

// TestPrepareResponsesRequest_FlattensForUnsupportedWireAndIsolatesSharedRequest is the
// dispatch-seam half of issue #7048: an attempt against a wire without namespace support
// gets flattened, prefixed tools, and the shared request keeps the caller's namespaces
// for the next attempt.
func TestPrepareResponsesRequest_FlattensForUnsupportedWireAndIsolatesSharedRequest(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := responsesReqWithNamespaces(schemas.Anthropic)

	out, bifrostErr := prepareResponsesRequest(ctx, &schemas.ProviderConfig{}, stubProvider{key: schemas.Anthropic}, schemas.Key{}, req)

	require.Nil(t, bifrostErr)
	require.NotSame(t, req, out)
	require.Len(t, out.Params.Tools, 2)
	assert.Equal(t, "namespace_a__js", *out.Params.Tools[0].Name)
	assert.Equal(t, "namespace_b__js", *out.Params.Tools[1].Name)
	assert.Equal(t, schemas.ResponsesToolTypeNamespace, req.Params.Tools[0].Type, "the shared request was mutated")

	// The chat fallback must see function tools, not an empty list.
	chat := out.ToChatRequest()
	require.NotNil(t, chat)
	require.Len(t, chat.Params.Tools, 2)
}

func TestPrepareResponsesRequest_PassesThroughForOpenAI(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := responsesReqWithNamespaces(schemas.OpenAI)

	out, bifrostErr := prepareResponsesRequest(ctx, &schemas.ProviderConfig{}, stubProvider{key: schemas.OpenAI}, schemas.Key{}, req)

	require.Nil(t, bifrostErr)
	assert.Same(t, req, out, "OpenAI understands namespace tools; the request must dispatch unchanged")
}

func TestPrepareResponsesRequest_ProviderAnswerWins(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := responsesReqWithNamespaces(schemas.Bedrock)

	out, bifrostErr := prepareResponsesRequest(ctx, &schemas.ProviderConfig{},
		namespaceCapableStub{stubProvider: stubProvider{key: schemas.Bedrock}, supported: true}, schemas.Key{}, req)

	require.Nil(t, bifrostErr)
	assert.Same(t, req, out, "a provider that reports namespace support must not be flattened")
}

// TestPrepareResponsesRequest_AliasesTravelWithThePreparedRequest pins the ownership
// rule: the alias map is request state, carried on the prepared copy and applied to
// that attempt's response. A later attempt on a wire that accepts namespaces gets the
// shared request back with no map, so nothing from the earlier attempt can leak into
// its response, without any context key or process-wide store.
func TestPrepareResponsesRequest_AliasesTravelWithThePreparedRequest(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := responsesReqWithNamespaces(schemas.Anthropic)

	// Attempt 1: unsupported wire, the prepared copy carries the map.
	first, bifrostErr := prepareResponsesRequest(ctx, &schemas.ProviderConfig{}, stubProvider{key: schemas.Anthropic}, schemas.Key{}, req)
	require.Nil(t, bifrostErr)
	require.Len(t, first.NamespaceToolAliases, 2, "attempt 1 must carry the aliases it produced")
	assert.Nil(t, req.NamespaceToolAliases, "the shared request never carries a map")

	// Attempt 2: supported wire on the same request, nothing to restore.
	req.Provider = schemas.OpenAI
	second, bifrostErr := prepareResponsesRequest(ctx, &schemas.ProviderConfig{}, stubProvider{key: schemas.OpenAI}, schemas.Key{}, req)
	require.Nil(t, bifrostErr)
	assert.Same(t, req, second)
	assert.Nil(t, second.NamespaceToolAliases)

	// Restoring attempt 2's response with attempt 2's (empty) map leaves it untouched.
	resp := &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{Output: []schemas.ResponsesMessage{{
		Type:                 new(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: new("c"), Name: new("namespace_a__js"), Arguments: new("{}")},
	}}}}
	providerUtils.RestoreResponsesNamespaceToolCalls(second.NamespaceToolAliases, resp)
	assert.Equal(t, "namespace_a__js", *resp.ResponsesResponse.Output[0].Name)
	assert.Nil(t, resp.ResponsesResponse.Output[0].Namespace)
}

// The streaming path restores through a wrapped PostHookRunner built from the prepared
// request's map, so every chunk reaches the post hooks with the caller's names.
func TestWrapNamespaceRestorePostHookRunner(t *testing.T) {
	aliases := map[string]schemas.NamespaceToolAlias{"namespace_a__js": {Namespace: "namespace_a", Name: "js"}}
	var seen *schemas.BifrostResponse
	inner := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		seen = result
		return result, err
	}
	chunk := func() *schemas.BifrostResponse {
		return &schemas.BifrostResponse{ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
			Item: &schemas.ResponsesMessage{Type: new(schemas.ResponsesMessageTypeFunctionCall), ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: new("c"), Name: new("namespace_a__js"), Arguments: new("{}")}},
		}}
	}

	wrapped := providerUtils.WrapNamespaceRestorePostHookRunner(inner, aliases)
	wrapped(nil, chunk(), nil)
	require.NotNil(t, seen)
	assert.Equal(t, "js", *seen.ResponsesStreamResponse.Item.Name)
	assert.Equal(t, "namespace_a", *seen.ResponsesStreamResponse.Item.Namespace)

	// No aliases: the runner is returned as is and chunks pass through untouched.
	plain := providerUtils.WrapNamespaceRestorePostHookRunner(inner, nil)
	plain(nil, chunk(), nil)
	assert.Equal(t, "namespace_a__js", *seen.ResponsesStreamResponse.Item.Name)
}

// Codex >= 0.147 sends a "functions" namespace on every request. It is the default
// namespace by definition, so it is unwrapped for EVERY provider, including ones
// whose wire accepts namespaces: Bedrock Mantle reserves the name and 400s, and on
// OpenAI the unwrap is a no-op semantically.
func TestPrepareResponsesRequest_UnwrapsFunctionsNamespaceForEveryWire(t *testing.T) {
	functionsNS := schemas.ResponsesTool{
		Type: schemas.ResponsesToolTypeNamespace,
		Name: new("functions"),
		ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeFunction, Name: new("wait"), ResponsesToolFunction: &schemas.ResponsesToolFunction{}},
		}},
	}

	t.Run("namespace-capable provider still gets it unwrapped", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		req := responsesReqWithNamespaces(schemas.Bedrock)
		req.Params.Tools = []schemas.ResponsesTool{functionsNS, req.Params.Tools[0]}

		out, bifrostErr := prepareResponsesRequest(ctx, &schemas.ProviderConfig{},
			namespaceCapableStub{stubProvider: stubProvider{key: schemas.Bedrock}, supported: true}, schemas.Key{}, req)

		require.Nil(t, bifrostErr)
		require.NotSame(t, req, out)
		require.Len(t, out.Params.Tools, 2)
		assert.Equal(t, schemas.ResponsesToolTypeFunction, out.Params.Tools[0].Type)
		assert.Equal(t, "wait", *out.Params.Tools[0].Name, "functions members are hoisted without a prefix")
		assert.Equal(t, schemas.ResponsesToolTypeNamespace, out.Params.Tools[1].Type, "other namespaces pass through on a capable wire")
		assert.Equal(t, schemas.ResponsesToolTypeNamespace, req.Params.Tools[0].Type, "the shared request was mutated")
	})

	t.Run("flattening wire hoists functions members without a prefix", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		req := responsesReqWithNamespaces(schemas.Anthropic)
		req.Params.Tools = []schemas.ResponsesTool{functionsNS, req.Params.Tools[0]}

		out, bifrostErr := prepareResponsesRequest(ctx, &schemas.ProviderConfig{}, stubProvider{key: schemas.Anthropic}, schemas.Key{}, req)

		require.Nil(t, bifrostErr)
		require.Len(t, out.Params.Tools, 2)
		assert.Equal(t, "wait", *out.Params.Tools[0].Name)
		assert.Equal(t, "namespace_a__js", *out.Params.Tools[1].Name)
	})
}
