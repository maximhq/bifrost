package warp

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/stretchr/testify/require"
)

// An operator may type a provider-qualified model. The catalog keys on the bare
// name, so handing it the qualified form misses every entry and prices the turn
// at zero.
func TestWarpCatalogModelStripsProviderPrefix(t *testing.T) {
	require.Equal(t, "claude-sonnet-5", catalogModel("anthropic/claude-sonnet-5"))
	require.Equal(t, "gpt-5.5", catalogModel("openai/gpt-5.5"))
	require.Equal(t, "gpt-5.5", catalogModel("gpt-5.5"))
	require.Equal(t, "", catalogModel(""))

	// Only the first segment is a provider; a model whose own name has slashes
	// keeps the rest.
	require.Equal(t, "meta/llama-3/70b", catalogModel("bedrock/meta/llama-3/70b"))
}

// Pricing must follow routing. requestTarget routes a provider-qualified
// model by its own prefix, so a config of Provider "anthropic" with Model
// "vertex/gemini-2.5-pro" runs on Vertex - and pricing that turn against
// Anthropic's rate card reports a wrong or zero cost for every request.
func TestWarpCostProviderFollowsQualifiedModel(t *testing.T) {
	require.Equal(t, schemas.Vertex, costProviderFor(&schemas.WarpConfig{Provider: "anthropic", Model: "vertex/gemini-2.5-pro"}),
		"a known-provider prefix routes the request, so it prices it too")
	require.Equal(t, schemas.ModelProvider("anthropic"), costProviderFor(&schemas.WarpConfig{Provider: "anthropic", Model: "claude-sonnet-5"}),
		"an unqualified model prices against the configured provider")
	// A slash that is not a known provider is part of the model's own name, so
	// the configured provider still prices it - same distinction
	// requestTarget draws.
	require.Equal(t, schemas.ModelProvider("replicate"), costProviderFor(&schemas.WarpConfig{Provider: "replicate", Model: "meta/llama-3-8b"}))
}

// A slash in a model name does not make it provider-qualified.
//
// Native slugs carry slashes of their own - "meta/llama-3-8b" on Replicate,
// "meta-llama/Llama-3.1-8B" elsewhere - and "meta" is not a Bifrost provider.
// Treating any slash as a prefix would send the request to a provider named
// "meta", and hand the catalog lookup a truncated slug.
func TestWarpModelNameHandlingRespectsKnownProviders(t *testing.T) {
	t.Run("a slash-bearing slug stays whole under the configured provider", func(t *testing.T) {
		provider, model := requestTarget(&schemas.WarpConfig{Provider: "bedrock", Model: "meta/llama-3-8b"})
		require.Equal(t, schemas.Bedrock, provider, "meta is not a provider, so the configured one must still be used")
		require.Equal(t, "meta/llama-3-8b", model)
	})

	t.Run("a qualified model routes by its own prefix", func(t *testing.T) {
		provider, model := requestTarget(&schemas.WarpConfig{Provider: "bedrock", Model: "anthropic/claude-sonnet-5"})
		require.Equal(t, schemas.Anthropic, provider, "the operator typed a real provider prefix, so it stands")
		require.Equal(t, "claude-sonnet-5", model)
	})

	t.Run("a bare model takes the configured provider", func(t *testing.T) {
		provider, model := requestTarget(&schemas.WarpConfig{Provider: "openai", Model: "gpt-5.5"})
		require.Equal(t, schemas.OpenAI, provider)
		require.Equal(t, "gpt-5.5", model)
	})

	t.Run("the catalog strips only a real provider segment", func(t *testing.T) {
		require.Equal(t, "claude-sonnet-5", catalogModel("anthropic/claude-sonnet-5"))
		require.Equal(t, "meta/llama-3-8b", catalogModel("meta/llama-3-8b"),
			"meta is part of the slug, so stripping it would miss the catalog entry")
		require.Equal(t, "meta/llama-3-8b", catalogModel("bedrock/meta/llama-3-8b"),
			"only the provider segment comes off, not every segment")
		require.Equal(t, "gpt-5.5", catalogModel("gpt-5.5"))
	})
}

// Warp's calls run on the gateway client in-process, so what used to travel as
// HTTP headers must arrive as the context values the HTTP transport would have
// derived from them. Each one is load-bearing downstream: the logging plugin
// reads the User-Agent and x-bf-lh- label only from the request-headers map
// (app=Warp, and the indexer skipping Warp's own rows), the session id keeps a
// thread on one key, the key id honours the pinned key, and the MCP allowlist
// keeps the deployment's end-user tools out of Warp's context window.
func TestWarpChatCarriesSettingsAsContextValues(t *testing.T) {
	cases := []struct {
		name           string
		config         *schemas.WarpConfig
		conversationID string
		wantKeyID      string
	}{
		{name: "pinned key and conversation", config: &schemas.WarpConfig{APIKeyID: "key-123", RequestTimeoutSeconds: 45}, conversationID: "conv-1", wantKeyID: "key-123"},
		{name: "pinned key, no conversation", config: &schemas.WarpConfig{APIKeyID: "key-123"}, wantKeyID: "key-123"},
		{name: "conversation, no pinned key", config: &schemas.WarpConfig{}, conversationID: "conv-1"},
		{name: "neither", config: &schemas.WarpConfig{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seenCtx *schemas.BifrostContext
			var seenReq *schemas.BifrostResponsesRequest
			want := TextTurn("ok")
			executor := func(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
				seenCtx, seenReq = ctx, req
				return want, nil
			}
			req := &schemas.BifrostResponsesRequest{Provider: schemas.Anthropic, Model: "claude-sonnet-5"}

			started := time.Now()
			got, bifrostErr := NewChat(executor, tc.config, tc.conversationID)(context.Background(), req)
			require.Nil(t, bifrostErr)
			require.Same(t, want, got)
			require.Same(t, req, seenReq, "the request reaches the gateway client untouched")

			headers, _ := seenCtx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
			require.Equal(t, UserAgent, headers["user-agent"], "Warp's traffic is always labelled")
			require.Equal(t, []string{excludeMCPToolsValue}, seenCtx.Value(schemas.MCPContextKeyIncludeClients),
				"every call must exclude the deployment's MCP tools")

			if tc.conversationID != "" {
				require.Equal(t, tc.conversationID, headers[ConversationHeader])
				require.Equal(t, tc.conversationID, seenCtx.Value(schemas.BifrostContextKeySessionID))
			} else {
				require.NotContains(t, headers, ConversationHeader, "no conversation means no grouping label")
				require.Nil(t, seenCtx.Value(schemas.BifrostContextKeySessionID), "no conversation means no session")
			}
			if tc.wantKeyID != "" {
				require.Equal(t, tc.wantKeyID, seenCtx.Value(schemas.BifrostContextKeyAPIKeyID))
			} else {
				require.Nil(t, seenCtx.Value(schemas.BifrostContextKeyAPIKeyID), "no key pinned means no pin")
			}

			deadline, ok := seenCtx.Deadline()
			require.True(t, ok, "every call is bounded by Warp's per-call timeout")
			timeout := time.Duration(tc.config.EffectiveRequestTimeoutSeconds()) * time.Second
			require.WithinDuration(t, started.Add(timeout), deadline, 5*time.Second)
		})
	}
}

// A service with a log reader but no gateway client has no way to reach a
// model, so it must not advertise chat.
func TestWarpCanChatRequiresGatewayClient(t *testing.T) {
	executor := func(*schemas.BifrostContext, *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		return TextTurn("ok"), nil
	}
	require.False(t, NewService(nil, WithLogReader(&fakeLogReader{})).CanChat())
	require.True(t, NewService(nil, WithLogReader(&fakeLogReader{}), WithResponsesExecutor(executor)).CanChat())
	require.NotNil(t, NewService(nil, WithLogReader(&fakeLogReader{}), WithResponsesExecutor(executor)).chatFuncFor(context.Background(), &schemas.WarpConfig{}, ""))
}

// Governance refuses a request with no grant ("the transport did not settle who
// the request is"). Warp's calls skip the HTTP transport that would install
// one, so each call must carry the grant the chat handler settled for the
// dashboard request - the same grant for every call of the turn, not a copy.
func TestWarpChatCarriesTheTurnsGrant(t *testing.T) {
	var seen []schemas.Grant
	executor := func(ctx *schemas.BifrostContext, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		seen = append(seen, ctx.Grant())
		return TextTurn("ok"), nil
	}
	g := grant.New()
	chat := NewChat(executor, &schemas.WarpConfig{}, "conv-1")
	turnCtx := WithGrant(context.Background(), g)
	for range 2 {
		_, bifrostErr := chat(turnCtx, &schemas.BifrostResponsesRequest{})
		require.Nil(t, bifrostErr)
	}
	require.Len(t, seen, 2)
	for _, got := range seen {
		require.NotNil(t, got, "a model call without a grant is refused by governance")
		require.Same(t, g, got.(*grant.Grant), "every model call of the turn carries the turn's grant")
	}
}
