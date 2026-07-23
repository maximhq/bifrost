package lib

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/valyala/fasthttp"
)

// TestApplyBifrostResponseHeaders covers the routed-identity headers added so
// drop-in integration callers (Anthropic SDK against `/anthropic/v1/messages`,
// OpenAI SDK against `/openai/v1/chat/completions`, etc.) can recover the
// actual provider/model that handled the request — including after fallback
// or routing-rule resolution. The body shape they get back has no place to
// surface this; headers do. Native `/v1` routes emit the same set alongside
// `extra_fields` in the body.
func TestApplyBifrostResponseHeaders(t *testing.T) {
	newBifrostCtx := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}

	t.Run("routed identity emits all headers", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()

		extra := schemas.BifrostResponseExtraFields{
			Provider:               schemas.Bedrock,
			OriginalModelRequested: "claude-sonnet-4-6",
			ResolvedModelUsed:      "us.anthropic.claude-sonnet-4-6",
			RequestType:            schemas.ChatCompletionRequest,
			ProviderResponseHeaders: map[string]string{
				"x-amzn-requestid": "req-789",
			},
		}

		ApplyBifrostResponseHeaders(ctx, bifrostCtx, extra)

		assert.Equal(t, "bedrock", string(ctx.Response.Header.Peek(HeaderBifrostProvider)))
		assert.Equal(t, "claude-sonnet-4-6", string(ctx.Response.Header.Peek(HeaderBifrostOriginalModel)))
		assert.Equal(t, "us.anthropic.claude-sonnet-4-6", string(ctx.Response.Header.Peek(HeaderBifrostResolvedModel)))
		assert.Equal(t, string(schemas.ChatCompletionRequest), string(ctx.Response.Header.Peek(HeaderBifrostRequestType)))
		assert.Equal(t, "req-789", string(ctx.Response.Header.Peek("x-amzn-requestid")))
		// No fallback fired — header must be absent.
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostFallbackIndex)))
	})

	t.Run("fallback index from context emits when non-zero", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()
		bifrostCtx.SetValue(schemas.BifrostContextKeyFallbackIndex, 2)

		extra := schemas.BifrostResponseExtraFields{
			Provider:          schemas.Anthropic,
			ResolvedModelUsed: "claude-haiku-4-5",
		}

		ApplyBifrostResponseHeaders(ctx, bifrostCtx, extra)

		assert.Equal(t, "2", string(ctx.Response.Header.Peek(HeaderBifrostFallbackIndex)))
	})

	t.Run("zero-value extra writes no headers", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()

		ApplyBifrostResponseHeaders(ctx, bifrostCtx, schemas.BifrostResponseExtraFields{})

		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostProvider)))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostOriginalModel)))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostResolvedModel)))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostRequestType)))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostFallbackIndex)))
		// No accumulator installed — unmeasured must stay distinguishable from zero.
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostUpstreamLatency)))
	})

	t.Run("measured upstream latency emits milliseconds", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()
		bifrostCtx.ResetUpstreamLatency()
		schemas.AddUpstreamLatency(bifrostCtx, 150*time.Millisecond)
		schemas.AddUpstreamLatency(bifrostCtx, 500*time.Microsecond)

		ApplyBifrostResponseHeaders(ctx, bifrostCtx, schemas.BifrostResponseExtraFields{})

		assert.Equal(t, "150.500", string(ctx.Response.Header.Peek(HeaderBifrostUpstreamLatency)))
	})

	t.Run("measured zero upstream latency emits 0.000, not absence", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()
		bifrostCtx.ResetUpstreamLatency()

		ApplyBifrostResponseHeaders(ctx, bifrostCtx, schemas.BifrostResponseExtraFields{})

		assert.Equal(t, "0.000", string(ctx.Response.Header.Peek(HeaderBifrostUpstreamLatency)))
	})

	t.Run("primary-provider success (FallbackIndex=0) does not emit fallback header", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()
		bifrostCtx.SetValue(schemas.BifrostContextKeyFallbackIndex, 0)

		ApplyBifrostResponseHeaders(ctx, bifrostCtx, schemas.BifrostResponseExtraFields{
			Provider: schemas.OpenAI,
		})

		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostFallbackIndex)),
			"FallbackIndex=0 means primary succeeded; absence of header is the signal")
	})

	t.Run("routing info emits full x-bifrost-routing-info-* header set", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()

		aliasName := "sonnet-prod"
		family := schemas.ModelFamily("claude")
		primaryProvider := schemas.Anthropic
		primaryModel := "claude-sonnet-4-6"
		serverSideFallback := "claude-haiku-4-5"

		ApplyBifrostResponseHeaders(ctx, bifrostCtx, schemas.BifrostResponseExtraFields{
			RoutingInfo: schemas.RoutingInfo{
				Provider: schemas.Bedrock,
				Model:    "claude-sonnet-4-6",
				Key:      "prod-key-1",
				ResolvedKeyAlias: &schemas.ResolvedKeyAlias{
					ModelID:     "us.anthropic.claude-sonnet-4-6",
					ModelName:   &aliasName,
					ModelFamily: &family,
				},
				IsFallback:              true,
				PrimaryProvider:         &primaryProvider,
				PrimaryModel:            &primaryModel,
				ServerSideFallbackModel: &serverSideFallback,
			},
		})

		assert.Equal(t, "bedrock", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoProvider)))
		assert.Equal(t, "claude-sonnet-4-6", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoModel)))
		assert.Equal(t, "prod-key-1", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoKey)))
		assert.Equal(t, "us.anthropic.claude-sonnet-4-6", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoAliasModelID)))
		assert.Equal(t, "sonnet-prod", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoAliasModelName)))
		assert.Equal(t, "claude", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoAliasModelFamily)))
		assert.Equal(t, "true", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoIsFallback)))
		assert.Equal(t, "anthropic", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoPrimaryProvider)))
		assert.Equal(t, "claude-sonnet-4-6", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoPrimaryModel)))
		assert.Equal(t, "claude-haiku-4-5", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoServerSideFallbackModel)))
	})

	t.Run("primary-route routing info skips fallback and alias headers", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()

		ApplyBifrostResponseHeaders(ctx, bifrostCtx, schemas.BifrostResponseExtraFields{
			RoutingInfo: schemas.RoutingInfo{
				Provider: schemas.OpenAI,
				Model:    "gpt-5.6",
				Key:      "openai-key",
			},
		})

		assert.Equal(t, "openai", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoProvider)))
		assert.Equal(t, "gpt-5.6", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoModel)))
		assert.Equal(t, "openai-key", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoKey)))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoAliasModelID)))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoIsFallback)),
			"is-fallback header must be absent on primary-route success")
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoPrimaryProvider)))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoPrimaryModel)))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoServerSideFallbackModel)))
	})
}
