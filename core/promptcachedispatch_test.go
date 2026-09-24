package bifrost

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/openai"

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

// TestPrepareResponsesAdditionalTools preserves embedded local MCP declarations and their call identities across providers.
func TestPrepareResponsesAdditionalTools(test *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	var incoming openai.OpenAIResponsesRequest
	require.NoError(test, schemas.Unmarshal([]byte(`{
		"model":"anthropic/claude-sonnet-4-5",
		"input":[
			{"type":"additional_tools","role":"developer","tools":[
				{"type":"namespace","name":"functions","tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{}}}]},
				{"type":"namespace","name":"mcp__local","tools":[
					{"type":"function","name":"read","description":"Read a local note","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}},
					{"type":"function","name":"search","parameters":{"type":"object","properties":{}}},
					{"type":"function","name":"list","parameters":{"type":"object","properties":{}}},
					{"type":"function","name":"write","parameters":{"type":"object","properties":{}}}
				]}
			]},
			{"type":"message","role":"user","content":"Read the note"}
		]
	}`), &incoming))
	request := incoming.ToBifrostResponsesRequest(ctx)
	before, err := schemas.Marshal(request)
	require.NoError(test, err)

	prepared, bifrostErr := prepareResponsesRequest(ctx, &schemas.ProviderConfig{}, stubProvider{key: schemas.Anthropic}, schemas.Key{}, request)
	require.Nil(test, bifrostErr)
	require.NotNil(test, prepared.Params)
	require.Len(test, prepared.Params.Tools, 5)
	assert.Equal(test, "shell", *prepared.Params.Tools[0].Name)
	assert.Equal(test, "mcp__local__read", *prepared.Params.Tools[1].Name)
	require.Len(test, prepared.Input, 1)

	wire, err := anthropic.ToAnthropicResponsesRequest(ctx, prepared)
	require.NoError(test, err)
	require.Len(test, wire.Tools, 5)
	encoded, err := schemas.Marshal(wire)
	require.NoError(test, err)
	assert.Contains(test, string(encoded), "\"name\":\"mcp__local__read\"")
	assert.Contains(test, string(encoded), "\"path\":{\"type\":\"string\"}")
	assert.NotContains(test, string(encoded), "additional_tools")
	assert.NotContains(test, string(encoded), "\"messages\":null")

	for _, eventType := range []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeOutputItemAdded,
		schemas.ResponsesStreamResponseTypeOutputItemDone,
	} {
		response := &schemas.BifrostResponse{ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type: eventType,
			Item: &schemas.ResponsesMessage{
				Type: new(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Name: new("mcp__local__read"), CallID: new("call_local"), Arguments: new("{\"path\":\"note\"}"),
				},
			},
		}}
		providerUtils.RestoreResponsesNamespaceToolCalls(prepared.NamespaceToolAliases, response)
		assert.Equal(test, "read", *response.ResponsesStreamResponse.Item.Name)
		assert.Equal(test, "mcp__local", *response.ResponsesStreamResponse.Item.Namespace)
		assert.Equal(test, "call_local", *response.ResponsesStreamResponse.Item.CallID)
	}

	after, err := schemas.Marshal(request)
	require.NoError(test, err)
	assert.JSONEq(test, string(before), string(after))
	native, bifrostErr := prepareResponsesRequest(ctx, &schemas.ProviderConfig{}, stubProvider{key: schemas.OpenAI}, schemas.Key{}, request)
	require.Nil(test, bifrostErr)
	assert.Same(test, request, native)
	assert.Nil(test, native.NamespaceToolAliases)
}

// TestPrepareResponsesClaudeLocalMCP verifies that Claude's local function declarations survive conversion to OpenAI.
func TestPrepareResponsesClaudeLocalMCP(test *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	var incoming anthropic.AnthropicMessageRequest
	require.NoError(test, schemas.Unmarshal([]byte(`{
		"model":"openai/gpt-4o-mini","max_tokens":128,
		"messages":[{"role":"user","content":"Read the note"}],
		"tools":[{"name":"mcp__local__read","description":"Read a local note","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]
	}`), &incoming))
	request := incoming.ToBifrostResponsesRequest(ctx)
	prepared, bifrostErr := prepareResponsesRequest(ctx, &schemas.ProviderConfig{}, stubProvider{key: schemas.OpenAI}, schemas.Key{}, request)
	require.Nil(test, bifrostErr)
	wire := openai.ToOpenAIResponsesRequest(ctx, prepared)
	require.Len(test, wire.Tools, 1)
	assert.Equal(test, "mcp__local__read", *wire.Tools[0].Name)
	assert.Equal(test, schemas.ResponsesToolTypeFunction, wire.Tools[0].Type)
}

// TestPrepareResponsesAdditionalToolsHistory keeps forced calls and replay aligned with the promoted declarations.
func TestPrepareResponsesAdditionalToolsHistory(test *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	var request schemas.BifrostResponsesRequest
	require.NoError(test, schemas.Unmarshal([]byte(`{
		"provider":"anthropic","model":"claude-sonnet-4-5",
		"input":[
			{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"mcp__local","tools":[{"type":"function","name":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]}]},
			{"type":"function_call","namespace":"mcp__local","name":"read","call_id":"call_1","arguments":"{\"path\":\"note\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"local note contents"}
		],
		"params":{
			"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{}}}],
			"tool_choice":{"type":"function","name":"read"}
		}
	}`), &request))
	before, err := schemas.Marshal(request)
	require.NoError(test, err)
	prepared, bifrostErr := prepareResponsesRequest(ctx, &schemas.ProviderConfig{}, stubProvider{key: schemas.Anthropic}, schemas.Key{}, &request)
	require.Nil(test, bifrostErr)
	require.Len(test, prepared.Params.Tools, 2)
	assert.Equal(test, "shell", *prepared.Params.Tools[0].Name)
	require.Len(test, prepared.Input, 2)
	assert.Equal(test, "mcp__local__read", *prepared.Input[0].Name)
	assert.Nil(test, prepared.Input[0].Namespace)
	assert.Equal(test, "call_1", *prepared.Input[1].CallID)
	choice, err := schemas.Marshal(prepared.Params.ToolChoice)
	require.NoError(test, err)
	assert.Contains(test, string(choice), "mcp__local__read")
	after, err := schemas.Marshal(request)
	require.NoError(test, err)
	assert.JSONEq(test, string(before), string(after))
}

// TestHoistResponsesAdditionalTools validates malformed declarations instead of silently losing tools.
func TestHoistResponsesAdditionalTools(test *testing.T) {
	for _, testCase := range []struct {
		name      string
		raw       string
		wantError bool
	}{
		{name: "flat function with nil params", raw: `[{"type":"function","name":"mcp__local__read","parameters":{"type":"object","properties":{}}}]`},
		{name: "empty list", raw: `[]`},
		{name: "object instead of array", raw: `{"type":"function","name":"read"}`, wantError: true},
		{name: "missing tools", wantError: true},
	} {
		test.Run(testCase.name, func(test *testing.T) {
			request := &schemas.BifrostResponsesRequest{
				Input: []schemas.ResponsesMessage{{
					Type:            new(schemas.ResponsesMessageTypeAdditionalTools),
					AdditionalTools: []byte(testCase.raw),
				}},
			}
			prepared, bifrostErr := hoistResponsesAdditionalTools(request)
			if testCase.wantError {
				require.NotNil(test, bifrostErr)
				assert.Nil(test, prepared)
			} else {
				require.Nil(test, bifrostErr)
				require.NotNil(test, prepared.Params)
				assert.Empty(test, prepared.Input)
				if testCase.name == "flat function with nil params" {
					require.Len(test, prepared.Params.Tools, 1)
					assert.Equal(test, "mcp__local__read", *prepared.Params.Tools[0].Name)
				}
			}
			assert.Nil(test, request.Params)
			require.Len(test, request.Input, 1)
			assert.Equal(test, testCase.raw, string(request.Input[0].AdditionalTools))
		})
	}
}

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

// TestPromptCacheChatRequest_CachePointOnlyReachesBedrock covers a Bedrock -> OpenAI fallback on the same shared request.
func TestPromptCacheChatRequest_CachePointOnlyReachesBedrock(t *testing.T) {
	req := chatReqWithText("stable prefix")
	req.Input[0].Content.ContentBlocks = append(req.Input[0].Content.ContentBlocks, schemas.ChatContentBlock{CachePoint: &schemas.CachePoint{Type: "default"}})

	assert.Same(t, req, promptCacheChatRequest(nil, nil, schemas.Bedrock, req))

	out := promptCacheChatRequest(nil, nil, schemas.OpenAI, req)
	require.NotSame(t, req, out)
	assert.Len(t, out.Input[0].Content.ContentBlocks, 1)
	assert.Len(t, req.Input[0].Content.ContentBlocks, 2, "the shared request lost its cachePoint; a Bedrock fallback would not see it")
}

// TestPromptCacheChatRequest_CachePointGatedOnModel covers the second half of the gate:
// Converse itself rejects a cachePoint on a model that does not publish support for one,
// and the datasheet is what decides, with the model name only the fallback.
func TestPromptCacheChatRequest_CachePointGatedOnModel(t *testing.T) {
	withCachePoint := func(model string) *schemas.BifrostChatRequest {
		req := chatReqWithText("stable prefix")
		req.Provider = schemas.Bedrock
		req.Model = model
		req.Input[0].Content.ContentBlocks = append(req.Input[0].Content.ContentBlocks, schemas.ChatContentBlock{CachePoint: &schemas.CachePoint{Type: "default"}})
		return req
	}

	llama := withCachePoint("meta.llama3-70b-instruct-v1:0")
	out := promptCacheChatRequest(nil, nil, schemas.Bedrock, llama)
	require.NotSame(t, llama, out)
	assert.Len(t, out.Input[0].Content.ContentBlocks, 1, "Converse rejects a cachePoint on a model without support for one")
	assert.Len(t, llama.Input[0].Content.ContentBlocks, 2, "the shared request was mutated")

	schemas.SetCapabilityResolver(func(_ schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		if model == "meta.llama3-70b-instruct-v1:0" {
			return &schemas.ModelCapabilities{SupportsCachePoint: schemas.Ptr(true)}
		}
		return &schemas.ModelCapabilities{SupportsCachePoint: schemas.Ptr(false)}
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	assert.Same(t, llama, promptCacheChatRequest(nil, nil, schemas.Bedrock, llama),
		"a datasheet row saying yes must win over the name-based fallback")

	claude := withCachePoint("anthropic.claude-3-5-haiku-20241022-v1:0")
	out = promptCacheChatRequest(nil, nil, schemas.Bedrock, claude)
	require.NotSame(t, claude, out)
	assert.Len(t, out.Input[0].Content.ContentBlocks, 1, "a datasheet row saying no must win too")
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

// captureBody records each request body the server receives, then replies with status and body.
func captureBody(mu *sync.Mutex, bodies *[]string, status int, reply string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		*bodies = append(*bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}
}

const billingHeaderFixture = "x-anthropic-billing-header: cc_version=2.1.270.42c; cc_entrypoint=cli; cc_is_subagent=true;"

func billingHeaderRequest(t testing.TB, header ...string) (*schemas.BifrostContext, *schemas.BifrostResponsesRequest) {
	t.Helper()
	ctx := schemas.NewBifrostContextWithValue(context.Background(), schemas.NoDeadline, schemas.BifrostContextKeyIntegrationType, "anthropic")
	var incoming anthropic.AnthropicMessageRequest
	require.NoError(t, schemas.Unmarshal([]byte(`{
		"model":"openai/gpt-4o-mini","max_tokens":64,
		"system":[
			{"type":"text","text":"`+billingHeaderFixture+`"},
			{"type":"text","text":"Stable instructions","cache_control":{"type":"ephemeral"}}
		],
		"messages":[{"role":"user","content":"Hello"}]
	}`), &incoming))
	if len(header) > 0 {
		incoming.System.ContentBlocks[0].Text = schemas.Ptr(header[0])
	}
	req := incoming.ToBifrostResponsesRequest(ctx)
	req.ExtractAnthropicBillingHeader()
	return ctx, req
}

func TestBillingHeaderNormalizedIngress(t *testing.T) {
	_, req := billingHeaderRequest(t)
	require.Len(t, req.Input[0].Content.ContentBlocks, 1)
	assert.Equal(t, "Stable instructions", *req.Input[0].Content.ContentBlocks[0].Text)
}

func TestBillingHeaderNonAnthropicNoCopy(t *testing.T) {
	ctx, req := billingHeaderRequest(t)
	out, bifrostErr := prepareResponsesRequest(ctx, nil, stubProvider{key: schemas.OpenAI}, schemas.Key{}, req)
	require.Nil(t, bifrostErr)
	assert.Same(t, req, out, "non-Anthropic attempts must reuse the normalized input")
}

func TestBillingHeaderRawPassthrough(t *testing.T) {
	ctx, req := billingHeaderRequest(t)
	req.Model = "claude-sonnet-4"
	req.RawRequestBody = []byte(`{"system":"` + billingHeaderFixture + `"}`)
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	out := restoreResponsesBillingHeader(ctx, schemas.Anthropic, req)
	assert.Same(t, req, out, "raw passthrough does not need a normalized copy")
	assert.Contains(t, string(out.RawRequestBody), billingHeaderFixture)
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, false)
	assert.Len(t, restoreResponsesBillingHeader(ctx, schemas.Anthropic, req).Input[0].Content.ContentBlocks, 2)
}

func BenchmarkBillingHeaderNonAnthropic100MB(b *testing.B) {
	ctx, req := billingHeaderRequest(b)
	// Allocate 100 distinct 1 MiB payloads before timing. Filtering should neither
	// inspect them nor allocate a new message slice on subsequent GPT attempts.
	for i := 0; i < 100; i++ {
		req.Input = append(req.Input, schemas.ResponsesMessage{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr(strings.Repeat("x", 1<<20))},
		})
	}
	b.ReportAllocs()
	var out *schemas.BifrostResponsesRequest
	for b.Loop() {
		out = restoreResponsesBillingHeader(ctx, schemas.OpenAI, req)
	}
	if out != req {
		b.Fatal("non-Anthropic attempt copied the request")
	}
}

func TestPrepareResponsesBillingHeaderModelFamily(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider schemas.ModelProvider
		model    string
		base     schemas.ModelProvider
		alias    *schemas.AliasConfig
		keep     bool
	}{
		{name: "GPT", provider: schemas.OpenAI, model: "gpt-4o-mini"},
		{name: "Gemini", provider: schemas.Gemini, model: "gemini-2.5-flash"},
		{name: "self hosted opaque", provider: schemas.Ollama, model: "local-model"},
		{name: "custom OpenAI", provider: customOpenAIProviderKey, base: schemas.OpenAI, model: "deployment"},
		{name: "Anthropic opaque", provider: schemas.Anthropic, model: "deployment", keep: true},
		{name: "custom Anthropic opaque", provider: customAnthropicProviderKey, base: schemas.Anthropic, model: "deployment", keep: true},
		{name: "Anthropic", provider: schemas.Anthropic, model: "claude-sonnet-4", keep: true},
		{name: "Bedrock Claude", provider: schemas.Bedrock, model: "anthropic.claude-sonnet-4", keep: true},
		{name: "Vertex Claude", provider: schemas.Vertex, model: "claude-sonnet-4", keep: true},
		{name: "Azure Claude", provider: schemas.Azure, model: "claude-sonnet-4", keep: true},
		{name: "OpenRouter Claude", provider: schemas.OpenRouter, model: "anthropic/claude-sonnet-4", keep: true},
		{name: "Claude named GPT alias", provider: schemas.OpenAI, model: "claude-alias", alias: &schemas.AliasConfig{ModelID: "opaque", ModelFamily: schemas.Ptr(schemas.ModelFamilyOpenAI)}},
		{name: "opaque Claude alias", provider: schemas.Azure, model: "deployment", alias: &schemas.AliasConfig{ModelID: "opaque", ModelFamily: schemas.Ptr(schemas.ModelFamilyAnthropic)}, keep: true},
		{name: "canonical Claude alias", provider: schemas.OpenRouter, model: "deployment", alias: &schemas.AliasConfig{ModelID: "opaque", ModelName: schemas.Ptr("claude-sonnet-4")}, keep: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, req := billingHeaderRequest(t)
			req.Provider, req.Model = tc.provider, tc.model
			if tc.base != "" {
				ctx.SetValue(schemas.BifrostContextKeyBaseProviderType, tc.base)
			}
			if tc.alias != nil {
				ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: tc.model, Config: tc.alias})
			}
			before, err := providerUtils.MarshalSorted(req)
			require.NoError(t, err)
			out, bifrostErr := prepareResponsesRequest(ctx, nil, stubProvider{key: tc.provider}, schemas.Key{}, req)
			require.Nil(t, bifrostErr)
			if tc.keep {
				require.Len(t, out.Input[0].Content.ContentBlocks, 2)
				assert.Equal(t, billingHeaderFixture, *out.Input[0].Content.ContentBlocks[0].Text)
			} else {
				assert.Same(t, req, out)
				require.Len(t, out.Input[0].Content.ContentBlocks, 1)
				assert.Equal(t, "Stable instructions", *out.Input[0].Content.ContentBlocks[0].Text)
				assert.Equal(t, req.Input[0].Content.ContentBlocks[0].CacheControl, out.Input[0].Content.ContentBlocks[0].CacheControl)
			}
			after, err := providerUtils.MarshalSorted(req)
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after), "preparation mutated the original request")
		})
	}
}

func TestPrepareResponsesBillingHeaderStableWireAndFallback(t *testing.T) {
	var previous []byte
	for _, header := range []string{billingHeaderFixture, "x-anthropic-billing-header: cc_version=2.1.270.abc; cch=fffff;"} {
		ctx, req := billingHeaderRequest(t, header)
		// Both directions: Claude -> GPT -> Claude, sharing the original input.
		for _, family := range []schemas.ModelFamily{schemas.ModelFamilyAnthropic, schemas.ModelFamilyOpenAI, schemas.ModelFamilyAnthropic} {
			ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Config: &schemas.AliasConfig{ModelID: "deployment", ModelFamily: &family}})
			out, bifrostErr := prepareResponsesRequest(ctx, nil, stubProvider{key: schemas.OpenAI}, schemas.Key{}, req)
			require.Nil(t, bifrostErr)
			if family == schemas.ModelFamilyAnthropic {
				require.Len(t, out.Input[0].Content.ContentBlocks, 2)
				assert.Equal(t, header, *out.Input[0].Content.ContentBlocks[0].Text)
				continue
			}
			wire, err := providerUtils.MarshalSorted(openai.ToOpenAIResponsesRequest(ctx, out))
			require.NoError(t, err)
			assert.NotContains(t, string(wire), "x-anthropic-billing-header:")
			if previous != nil {
				assert.Equal(t, string(previous), string(wire))
			}
			previous = wire
		}
	}
}

// Capture at the provider boundary so all four dispatch paths must prepare the
// request, including Responses -> Chat conversion before streaming dispatch.
type billingHeaderCaptureProvider struct {
	stubProvider
	responses *schemas.BifrostResponsesRequest
	chat      *schemas.BifrostChatRequest
}

func (p *billingHeaderCaptureProvider) Responses(_ *schemas.BifrostContext, _ schemas.Key, r *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	p.responses = r
	return &schemas.BifrostResponsesResponse{}, nil
}

func (p *billingHeaderCaptureProvider) ChatCompletion(_ *schemas.BifrostContext, _ schemas.Key, r *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	p.chat = r
	return &schemas.BifrostChatResponse{}, nil
}

func (p *billingHeaderCaptureProvider) ResponsesStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, r *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	p.responses = r
	ch := make(chan *schemas.BifrostStreamChunk)
	close(ch)
	return ch, nil
}

func (p *billingHeaderCaptureProvider) ChatCompletionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ func(context.Context), _ schemas.Key, r *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	p.chat = r
	ch := make(chan *schemas.BifrostStreamChunk)
	close(ch)
	return ch, nil
}

func TestBillingHeaderDispatch(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, chat := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/chat=%t", stream, chat), func(t *testing.T) {
				ctx, req := billingHeaderRequest(t)
				if chat {
					ctx.SetValue(schemas.BifrostContextKeyChangeRequestType, schemas.ChatCompletionRequest)
				}
				message := &ChannelMessage{Context: ctx, BifrostRequest: schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: req}}
				provider := &billingHeaderCaptureProvider{stubProvider: stubProvider{key: schemas.OpenAI}}
				client := &Bifrost{}
				if stream {
					message.RequestType = schemas.ResponsesStreamRequest
					_, bifrostErr := client.handleProviderStreamRequest(provider, nil, message, schemas.Key{}, nil, nil)
					require.Nil(t, bifrostErr)
				} else {
					_, bifrostErr := client.handleProviderRequest(provider, nil, message, schemas.Key{}, nil)
					require.Nil(t, bifrostErr)
				}
				var captured any
				if chat {
					require.NotNil(t, provider.chat)
					captured = provider.chat
				} else {
					require.NotNil(t, provider.responses)
					captured = provider.responses
				}
				body, err := providerUtils.MarshalSorted(captured)
				require.NoError(t, err)
				assert.NotContains(t, string(body), "x-anthropic-billing-header:")
				assert.Contains(t, string(body), "Stable instructions")
				require.Len(t, req.Input[0].Content.ContentBlocks, 1)
				assert.Equal(t, "Stable instructions", *req.Input[0].Content.ContentBlocks[0].Text)
			})
		}
	}
}

// TestChatCachePoint_StrippedForPrimaryKeptForBedrockFallback runs an OpenAI -> Bedrock fallback end to end.
func TestChatCachePoint_StrippedForPrimaryKeptForBedrockFallback(t *testing.T) {
	var mu sync.Mutex
	var openaiBodies, bedrockBodies []string

	primary := httptest.NewServer(captureBody(&mu, &openaiBodies, http.StatusInternalServerError,
		`{"error":{"message":"boom","type":"server_error"}}`))
	defer primary.Close()
	fallback := httptest.NewTLSServer(captureBody(&mu, &bedrockBodies, http.StatusOK,
		`{"output":{"message":{"role":"assistant","content":[{"text":"hello"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`))
	defer fallback.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, primary.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "openai-key", Value: *schemas.NewSecretVar("sk-test"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	account.AddProviderWithBaseURL(schemas.Bedrock, 1, 1, "")
	account.configs[schemas.Bedrock].NetworkConfig.MaxRetries = 0
	account.configs[schemas.Bedrock].NetworkConfig.InsecureSkipVerify = true
	account.SetKeysForProvider(schemas.Bedrock, []schemas.Key{{
		ID: "bedrock-key", Models: schemas.WhiteList{"*"}, Weight: 100,
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			AccessKey: *schemas.NewSecretVar("AKIATEST"),
			SecretKey: *schemas.NewSecretVar("secret"),
			Region:    schemas.NewSecretVar("us-east-1"),
			Endpoints: &schemas.BedrockEndpoints{Runtime: schemas.NewSecretVar(strings.TrimPrefix(fallback.URL, "https://"))},
		},
	}})
	client := newStreamTestClient(t, account)

	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("stable prefix")},
				{CachePoint: &schemas.CachePoint{Type: "default"}},
			}},
		}},
		Fallbacks: []schemas.Fallback{{Provider: schemas.Bedrock, Model: "anthropic.claude-3-5-haiku-20241022-v1:0"}},
	}

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(10*time.Second))
	resp, bifrostErr := client.ChatCompletionRequest(ctx, req)
	if bifrostErr != nil && bifrostErr.Error != nil {
		t.Fatalf("fallback failed: %s", bifrostErr.Error.Message)
	}
	require.NotNil(t, resp)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, openaiBodies, 1, "primary was not attempted")
	require.Len(t, bedrockBodies, 1, "fallback was not attempted")
	assert.NotContains(t, openaiBodies[0], "cachePoint", "primary wire body leaked the Bedrock marker")
	assert.Contains(t, bedrockBodies[0], "cachePoint", "fallback lost the caller's cachePoint")
	assert.Len(t, req.Input[0].Content.ContentBlocks, 2, "caller's request was mutated")
}
