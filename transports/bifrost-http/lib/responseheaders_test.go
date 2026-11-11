package lib

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"
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

	// A routing rule rewrote gpt-4o-mini to gpt-4o: the caller's model must be
	// recoverable from the headers alongside the routed one.
	t.Run("routing rewrite emits requested route next to the routed model", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()

		extra := schemas.BifrostResponseExtraFields{
			RoutingInfo: schemas.RoutingInfo{
				Provider:          schemas.OpenAI,
				Model:             "gpt-4o",
				RequestedProvider: schemas.OpenAI,
				RequestedModel:    "gpt-4o-mini",
			},
		}

		ApplyBifrostResponseHeaders(ctx, bifrostCtx, extra)

		assert.Equal(t, "gpt-4o", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoModel)))
		assert.Equal(t, "openai", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoRequestedProvider)))
		assert.Equal(t, "gpt-4o-mini", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoRequestedModel)))
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
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoRequestedProvider)))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoRequestedModel)))
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

	// The provider is another Bifrost: its x-bifrost-* headers describe its own hop and must not
	// replace or add to this gateway's, including the ones this hop leaves unset. Its other
	// headers, x-request-id among them, are forwarded as before.
	t.Run("a chained upstream's x-bifrost headers are not forwarded", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ctx.Response.Header.Set("x-bifrost-request-id", "gateway-req")
		bifrostCtx := newBifrostCtx()

		ApplyBifrostResponseHeaders(ctx, bifrostCtx, schemas.BifrostResponseExtraFields{
			Provider: schemas.OpenAI,
			ProviderResponseHeaders: map[string]string{
				"X-Bifrost-Request-Id":       "upstream-req",
				"X-Bifrost-Routing-Info-Key": "upstream-key",
				"X-Request-Id":               "req_upstream",
				"x-ratelimit-remaining":      "41",
			},
		})

		assert.Equal(t, "gateway-req", string(ctx.Response.Header.Peek("x-bifrost-request-id")))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoKey)),
			"the upstream hop's key name must not surface as this hop's")
		assert.Equal(t, "openai", string(ctx.Response.Header.Peek(HeaderBifrostProvider)))
		assert.Equal(t, "req_upstream", string(ctx.Response.Header.Peek("x-request-id")))
		assert.Equal(t, "41", string(ctx.Response.Header.Peek("x-ratelimit-remaining")))
	})
}

// TestIsGatewayOwnedResponseHeader pins which provider response header names are never forwarded:
// the x-bifrost-* family in any case, and nothing that merely shares its first letters.
func TestIsGatewayOwnedResponseHeader(t *testing.T) {
	for name, want := range map[string]bool{
		"x-bifrost-request-id":           true,
		"X-Bifrost-Trace-Id":             true,
		"X-BIFROST-ROUTING-INFO-KEY":     true,
		"x-bifrost-":                     true,
		"x-bifrost":                      false,
		"x-bifrostish":                   false,
		"x-request-id":                   false,
		"x-bf-vk":                        false,
		"x-ratelimit-remaining-requests": false,
		"":                               false,
	} {
		assert.Equal(t, want, IsGatewayOwnedResponseHeader(name), name)
	}
}

// TestApplyBifrostStreamResponseHeaders covers the streaming variant: identity
// comes from the RoutingInfo snapshot core stashes in the context at stream
// setup, since no chunk (and hence no ExtraFields) exists at header-write time.
func TestApplyBifrostStreamResponseHeaders(t *testing.T) {
	newBifrostCtx := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}

	t.Run("context snapshot emits identity and derived deprecated headers", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()

		bifrostCtx.SetValue(schemas.BifrostContextKeyRoutingInfo, schemas.RoutingInfo{
			Provider: schemas.Bedrock,
			Model:    "claude-sonnet-4-6",
			Key:      "prod-key-1",
			ResolvedKeyAlias: &schemas.ResolvedKeyAlias{
				ModelID: "us.anthropic.claude-sonnet-4-6",
			},
		})

		ApplyBifrostStreamResponseHeaders(ctx, bifrostCtx, schemas.ChatCompletionStreamRequest)

		assert.Equal(t, string(schemas.ChatCompletionStreamRequest), string(ctx.Response.Header.Peek(HeaderBifrostRequestType)))
		assert.Equal(t, "bedrock", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoProvider)))
		assert.Equal(t, "claude-sonnet-4-6", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoModel)))
		assert.Equal(t, "prod-key-1", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoKey)))
		assert.Equal(t, "us.anthropic.claude-sonnet-4-6", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoAliasModelID)))
		// Deprecated triplet derived from RoutingInfo via the shared sync rules.
		assert.Equal(t, "bedrock", string(ctx.Response.Header.Peek(HeaderBifrostProvider)))
		assert.Equal(t, "claude-sonnet-4-6", string(ctx.Response.Header.Peek(HeaderBifrostOriginalModel)))
		assert.Equal(t, "us.anthropic.claude-sonnet-4-6", string(ctx.Response.Header.Peek(HeaderBifrostResolvedModel)))
	})

	t.Run("fallback-layered snapshot emits fallback headers", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()

		primaryProvider := schemas.Anthropic
		primaryModel := "claude-sonnet-4-6"
		bifrostCtx.SetValue(schemas.BifrostContextKeyRoutingInfo, schemas.RoutingInfo{
			Provider:        schemas.Bedrock,
			Model:           "us.anthropic.claude-sonnet-4-6",
			Key:             "bedrock-key",
			IsFallback:      true,
			PrimaryProvider: &primaryProvider,
			PrimaryModel:    &primaryModel,
		})
		bifrostCtx.SetValue(schemas.BifrostContextKeyFallbackIndex, 1)

		ApplyBifrostStreamResponseHeaders(ctx, bifrostCtx, schemas.ChatCompletionStreamRequest)

		assert.Equal(t, "true", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoIsFallback)))
		assert.Equal(t, "anthropic", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoPrimaryProvider)))
		assert.Equal(t, "claude-sonnet-4-6", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoPrimaryModel)))
		assert.Equal(t, "1", string(ctx.Response.Header.Peek(HeaderBifrostFallbackIndex)))
		// Deprecated original-model derives from the primary on fallback.
		assert.Equal(t, "claude-sonnet-4-6", string(ctx.Response.Header.Peek(HeaderBifrostOriginalModel)))
		assert.Equal(t, "us.anthropic.claude-sonnet-4-6", string(ctx.Response.Header.Peek(HeaderBifrostResolvedModel)))
	})

	// Stream headers are written from the context snapshot before the first
	// chunk, so the requested route has to ride on it as well.
	t.Run("snapshot emits requested route", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()

		bifrostCtx.SetValue(schemas.BifrostContextKeyRoutingInfo, schemas.RoutingInfo{
			Provider:       schemas.OpenAI,
			Model:          "gpt-4o",
			RequestedModel: "gpt-4o-mini",
		})

		ApplyBifrostStreamResponseHeaders(ctx, bifrostCtx, schemas.ChatCompletionStreamRequest)

		assert.Equal(t, "gpt-4o", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoModel)))
		assert.Equal(t, "gpt-4o-mini", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoRequestedModel)))
		// The caller sent a bare model, so there is no requested provider to report.
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoRequestedProvider)))
	})

	t.Run("missing snapshot emits only request type", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()

		ApplyBifrostStreamResponseHeaders(ctx, bifrostCtx, schemas.ResponsesStreamRequest)

		assert.Equal(t, string(schemas.ResponsesStreamRequest), string(ctx.Response.Header.Peek(HeaderBifrostRequestType)))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoProvider)))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostProvider)))
	})
}

// TestApplyBifrostErrorResponseHeaders covers the error adapter used on failed /
// fallback-exhausted requests: it bridges BifrostErrorExtraFields to the shared
// writer so callers still learn which provider ran and whether a fallback fired.
func TestApplyBifrostErrorResponseHeaders(t *testing.T) {
	newBifrostCtx := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}

	t.Run("fallback-exhausted error emits identity and fallback headers", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()
		bifrostCtx.SetValue(schemas.BifrostContextKeyFallbackIndex, 1)

		primaryProvider := schemas.Anthropic
		primaryModel := "claude-sonnet-4-6"
		ApplyBifrostErrorResponseHeaders(ctx, bifrostCtx, schemas.BifrostErrorExtraFields{
			RequestType:            schemas.ChatCompletionRequest,
			Provider:               schemas.Bedrock,
			OriginalModelRequested: "claude-sonnet-4-6",
			ResolvedModelUsed:      "us.anthropic.claude-sonnet-4-6",
			RoutingInfo: schemas.RoutingInfo{
				Provider:        schemas.Bedrock,
				Model:           "us.anthropic.claude-sonnet-4-6",
				IsFallback:      true,
				PrimaryProvider: &primaryProvider,
				PrimaryModel:    &primaryModel,
			},
		})

		assert.Equal(t, "bedrock", string(ctx.Response.Header.Peek(HeaderBifrostProvider)))
		assert.Equal(t, "claude-sonnet-4-6", string(ctx.Response.Header.Peek(HeaderBifrostOriginalModel)))
		assert.Equal(t, "us.anthropic.claude-sonnet-4-6", string(ctx.Response.Header.Peek(HeaderBifrostResolvedModel)))
		assert.Equal(t, string(schemas.ChatCompletionRequest), string(ctx.Response.Header.Peek(HeaderBifrostRequestType)))
		assert.Equal(t, "bedrock", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoProvider)))
		assert.Equal(t, "true", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoIsFallback)))
		assert.Equal(t, "anthropic", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoPrimaryProvider)))
		assert.Equal(t, "1", string(ctx.Response.Header.Peek(HeaderBifrostFallbackIndex)))
	})

	t.Run("nil ctx (native error path) emits identity but no ctx-only headers", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}

		ApplyBifrostErrorResponseHeaders(ctx, nil, schemas.BifrostErrorExtraFields{
			RequestType: schemas.ChatCompletionRequest,
			Provider:    schemas.OpenAI,
			RoutingInfo: schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "gpt-5.6"},
		})

		assert.Equal(t, "openai", string(ctx.Response.Header.Peek(HeaderBifrostProvider)))
		assert.Equal(t, "gpt-5.6", string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoModel)))
		// ctx-only headers are absent when no context is threaded through.
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostFallbackIndex)))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostUpstreamLatency)))
	})

	t.Run("empty error extra fields emit nothing", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()

		ApplyBifrostErrorResponseHeaders(ctx, bifrostCtx, schemas.BifrostErrorExtraFields{})

		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostProvider)))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostRequestType)))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostRoutingInfoProvider)))
	})
}

// cacheHeaderNames is every x-bifrost-cache-* header, one per cache_debug field.
var cacheHeaderNames = []string{
	"x-bifrost-cache-hit",
	"x-bifrost-cache-id",
	"x-bifrost-cache-hit-type",
	"x-bifrost-cache-hit-latency-ms",
	"x-bifrost-cache-requested-provider",
	"x-bifrost-cache-requested-model",
	"x-bifrost-cache-similarity",
	"x-bifrost-cache-threshold",
	"x-bifrost-cache-provider-used",
	"x-bifrost-cache-model-used",
	"x-bifrost-cache-input-tokens",
}

func cacheHeaders(ctx *fasthttp.RequestCtx) map[string]string {
	out := map[string]string{}
	for _, name := range cacheHeaderNames {
		if v := ctx.Response.Header.Peek(name); len(v) > 0 {
			out[name] = string(v)
		}
	}
	return out
}

// TestApplyBifrostResponseHeaders_CacheMetadata covers the x-bifrost-cache-* headers: one per
// cache_debug field, so a caller on a route whose body has no extra_fields (the Anthropic, GenAI,
// Bedrock and Cohere SDK routes) still learns whether it was served from cache and which entry
// served it. A normal response reads them from its own cache_debug; a stream, whose headers go out
// before any chunk carries cache_debug, reads the lookup the cache plugin recorded on the context.
func TestApplyBifrostResponseHeaders_CacheMetadata(t *testing.T) {
	newBifrostCtx := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}
	str := func(v string) *string { return &v }
	threshold, similarity := 0.85, 0.9375
	inputTokens := 12
	latency := int64(4)

	t.Run("semantic hit emits every field", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ApplyBifrostResponseHeaders(ctx, newBifrostCtx(), schemas.BifrostResponseExtraFields{
			RequestType: schemas.ChatCompletionRequest,
			CacheDebug: &schemas.BifrostCacheMetadata{
				CacheHit:          true,
				CacheID:           str("entry-1"),
				HitType:           str("semantic"),
				RequestedProvider: str("anthropic"),
				RequestedModel:    str("claude-haiku-4-5"),
				ProviderUsed:      str("openai"),
				ModelUsed:         str("text-embedding-3-small"),
				InputTokens:       &inputTokens,
				Threshold:         &threshold,
				Similarity:        &similarity,
				CacheHitLatency:   &latency,
			},
		})

		assert.Equal(t, map[string]string{
			"x-bifrost-cache-hit":                "true",
			"x-bifrost-cache-id":                 "entry-1",
			"x-bifrost-cache-hit-type":           "semantic",
			"x-bifrost-cache-hit-latency-ms":     "4",
			"x-bifrost-cache-requested-provider": "anthropic",
			"x-bifrost-cache-requested-model":    "claude-haiku-4-5",
			"x-bifrost-cache-similarity":         "0.9375",
			"x-bifrost-cache-threshold":          "0.85",
			"x-bifrost-cache-provider-used":      "openai",
			"x-bifrost-cache-model-used":         "text-embedding-3-small",
			"x-bifrost-cache-input-tokens":       "12",
		}, cacheHeaders(ctx))
	})

	t.Run("miss emits only what the miss carries", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ApplyBifrostResponseHeaders(ctx, newBifrostCtx(), schemas.BifrostResponseExtraFields{
			RequestType: schemas.ChatCompletionRequest,
			CacheDebug:  &schemas.BifrostCacheMetadata{CacheHit: false, CacheID: str("entry-2")},
		})

		assert.Equal(t, map[string]string{
			"x-bifrost-cache-hit": "false",
			"x-bifrost-cache-id":  "entry-2",
		}, cacheHeaders(ctx))
	})

	t.Run("no cache_debug emits no cache headers", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ApplyBifrostResponseHeaders(ctx, newBifrostCtx(), schemas.BifrostResponseExtraFields{RequestType: schemas.ChatCompletionRequest})
		assert.Empty(t, cacheHeaders(ctx))
	})

	t.Run("stream emits the recorded lookup", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()
		schemas.SetCacheLookupOnContext(bifrostCtx, &schemas.BifrostCacheMetadata{
			CacheHit: true, CacheID: str("entry-3"), HitType: str("direct"), CacheHitLatency: &latency,
		})

		ApplyBifrostStreamResponseHeaders(ctx, bifrostCtx, schemas.ChatCompletionStreamRequest)

		assert.Equal(t, map[string]string{
			"x-bifrost-cache-hit":            "true",
			"x-bifrost-cache-id":             "entry-3",
			"x-bifrost-cache-hit-type":       "direct",
			"x-bifrost-cache-hit-latency-ms": "4",
		}, cacheHeaders(ctx))
	})

	t.Run("stream with no lookup emits no cache headers", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ApplyBifrostStreamResponseHeaders(ctx, newBifrostCtx(), schemas.ChatCompletionStreamRequest)
		assert.Empty(t, cacheHeaders(ctx))
	})

	// An error body carries no cache_debug, so neither do its headers, even after a lookup ran.
	t.Run("error response emits no cache headers", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		bifrostCtx := newBifrostCtx()
		schemas.SetCacheLookupOnContext(bifrostCtx, &schemas.BifrostCacheMetadata{CacheHit: false, CacheID: str("entry-4")})

		ApplyBifrostErrorResponseHeaders(ctx, bifrostCtx, schemas.BifrostErrorExtraFields{RequestType: schemas.ChatCompletionRequest})

		assert.Empty(t, cacheHeaders(ctx))
	})
}

// TestExtraFieldsPlacementHeaders covers client.extra_fields_placement = "headers": the scalar
// extra_fields with no body-mode header are written only when the request is marked, and the
// body helpers drop the top-level extra_fields key.
func TestExtraFieldsPlacementHeaders(t *testing.T) {
	extra := schemas.BifrostResponseExtraFields{
		RequestType:               schemas.ChatCompletionRequest,
		Provider:                  schemas.OpenAI,
		Latency:                   42,
		PricingRequestType:        schemas.ResponsesRequest,
		ConvertedRequestType:      schemas.ChatCompletionRequest,
		DroppedCompatPluginParams: []string{"temperature", "top_p"},
		DroppedUnsupportedTools:   []string{"web_search"},
		PassthroughPath:           "/v1/models",
	}
	scalarHeaders := []string{
		HeaderBifrostLatency, HeaderBifrostPricingRequestType, HeaderBifrostConvertedRequestType,
		HeaderBifrostDroppedCompatParams, HeaderBifrostDroppedUnsupportedTools, HeaderBifrostPassthroughPath,
	}

	t.Run("body mode keeps today's header set", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ApplyBifrostResponseHeaders(ctx, nil, extra)
		assert.Equal(t, "openai", string(ctx.Response.Header.Peek(HeaderBifrostProvider)))
		for _, name := range scalarHeaders {
			assert.Empty(t, string(ctx.Response.Header.Peek(name)), name)
		}
	})

	t.Run("headers mode writes the scalar fields", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue(FastHTTPUserValueExtraFieldsInHeaders, true)
		ApplyBifrostResponseHeaders(ctx, nil, extra)
		assert.Equal(t, "openai", string(ctx.Response.Header.Peek(HeaderBifrostProvider)))
		assert.Equal(t, "42", string(ctx.Response.Header.Peek(HeaderBifrostLatency)))
		assert.Equal(t, "responses", string(ctx.Response.Header.Peek(HeaderBifrostPricingRequestType)))
		assert.Equal(t, "chat_completion", string(ctx.Response.Header.Peek(HeaderBifrostConvertedRequestType)))
		assert.Equal(t, "temperature,top_p", string(ctx.Response.Header.Peek(HeaderBifrostDroppedCompatParams)))
		assert.Equal(t, "web_search", string(ctx.Response.Header.Peek(HeaderBifrostDroppedUnsupportedTools)))
		assert.Equal(t, "/v1/models", string(ctx.Response.Header.Peek(HeaderBifrostPassthroughPath)))
	})

	t.Run("headers mode writes error scalars", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue(FastHTTPUserValueExtraFieldsInHeaders, true)
		ApplyBifrostErrorResponseHeaders(ctx, nil, schemas.BifrostErrorExtraFields{
			Provider:   schemas.Anthropic,
			Latency:    7,
			RetryAfter: 1500,
			ErrorType:  schemas.ErrorType("rate_limit"),
		})
		assert.Equal(t, "anthropic", string(ctx.Response.Header.Peek(HeaderBifrostProvider)))
		assert.Equal(t, "7", string(ctx.Response.Header.Peek(HeaderBifrostLatency)))
		assert.Equal(t, "1500", string(ctx.Response.Header.Peek(HeaderBifrostRetryAfterMs)))
		assert.Equal(t, "rate_limit", string(ctx.Response.Header.Peek(HeaderBifrostErrorType)))
	})

	t.Run("body mode omits error scalars", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ApplyBifrostErrorResponseHeaders(ctx, nil, schemas.BifrostErrorExtraFields{Latency: 7, RetryAfter: 1500, ErrorType: schemas.ErrorType("rate_limit")})
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostLatency)))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostRetryAfterMs)))
		assert.Empty(t, string(ctx.Response.Header.Peek(HeaderBifrostErrorType)))
	})

	t.Run("stream headers carry compat dropped params from ctx in headers mode only", func(t *testing.T) {
		bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bifrostCtx.SetValue(schemas.BifrostContextKeyCompatDroppedParams, []string{"seed"})

		bodyCtx := &fasthttp.RequestCtx{}
		ApplyBifrostStreamResponseHeaders(bodyCtx, bifrostCtx, schemas.ChatCompletionStreamRequest)
		assert.Empty(t, string(bodyCtx.Response.Header.Peek(HeaderBifrostDroppedCompatParams)))

		headersCtx := &fasthttp.RequestCtx{}
		headersCtx.SetUserValue(FastHTTPUserValueExtraFieldsInHeaders, true)
		ApplyBifrostStreamResponseHeaders(headersCtx, bifrostCtx, schemas.ChatCompletionStreamRequest)
		assert.Equal(t, "seed", string(headersCtx.Response.Header.Peek(HeaderBifrostDroppedCompatParams)))
	})

	t.Run("headers mode keeps a zero-millisecond latency on finished responses", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue(FastHTTPUserValueExtraFieldsInHeaders, true)
		ApplyBifrostResponseHeaders(ctx, nil, schemas.BifrostResponseExtraFields{RequestType: schemas.ChatCompletionRequest, Latency: 0})
		assert.Equal(t, "0", string(ctx.Response.Header.Peek(HeaderBifrostLatency)), "body mode serializes latency 0, so headers mode must carry it")
	})

	t.Run("headers mode writes no latency where none is known", func(t *testing.T) {
		placeholder := &fasthttp.RequestCtx{}
		placeholder.SetUserValue(FastHTTPUserValueExtraFieldsInHeaders, true)
		ApplyBifrostResponseHeaders(placeholder, nil, schemas.BifrostResponseExtraFields{})
		assert.Empty(t, string(placeholder.Response.Header.Peek(HeaderBifrostLatency)), "an empty placeholder is not a response")

		stream := &fasthttp.RequestCtx{}
		stream.SetUserValue(FastHTTPUserValueExtraFieldsInHeaders, true)
		bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		bifrostCtx.SetValue(schemas.BifrostContextKeyRoutingInfo, schemas.RoutingInfo{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})
		ApplyBifrostStreamResponseHeaders(stream, bifrostCtx, schemas.ChatCompletionStreamRequest)
		assert.Equal(t, "openai", string(stream.Response.Header.Peek(HeaderBifrostRoutingInfoProvider)))
		assert.Empty(t, string(stream.Response.Header.Peek(HeaderBifrostLatency)), "stream headers go out before latency exists")

		errCtx := &fasthttp.RequestCtx{}
		errCtx.SetUserValue(FastHTTPUserValueExtraFieldsInHeaders, true)
		ApplyBifrostErrorResponseHeaders(errCtx, nil, schemas.BifrostErrorExtraFields{RequestType: schemas.ChatCompletionRequest})
		assert.Empty(t, string(errCtx.Response.Header.Peek(HeaderBifrostLatency)), "error latency is omitempty in the body, so 0 stays absent")
	})

	t.Run("strip removes only the top-level extra_fields", func(t *testing.T) {
		body := []byte(`{"id":"x","extra_fields":{"latency":1},"choices":[{"extra_fields":"keep"}]}`)
		assert.Equal(t, `{"id":"x","choices":[{"extra_fields":"keep"}]}`, string(StripExtraFields(body)))
		assert.Equal(t, `{"id":"x"}`, string(StripExtraFields([]byte(`{"id":"x"}`))))
		assert.Equal(t, `[1]`, string(StripExtraFields([]byte(`[1]`))))
	})

	// Responses bodies as the handlers marshal them: a stream lifecycle event that wraps a
	// response, and a unary response.
	responsesBodies := func(t *testing.T) (event, unary []byte) {
		extra := schemas.BifrostResponseExtraFields{RequestType: schemas.ResponsesStreamRequest, Provider: schemas.OpenAI}
		event, err := schemas.MarshalSorted(&schemas.BifrostStreamChunk{BifrostResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type:        schemas.ResponsesStreamResponseTypeCreated,
			Response:    &schemas.BifrostResponsesResponse{ID: new("resp_1"), Model: "gpt-4o"},
			ExtraFields: extra,
		}})
		assert.NoError(t, err)
		extra.RequestType = schemas.ResponsesRequest
		unary, err = schemas.MarshalSorted(&schemas.BifrostResponsesResponse{ID: new("resp_1"), Model: "gpt-4o", ExtraFields: extra})
		assert.NoError(t, err)
		return event, unary
	}

	t.Run("body mode keeps one top-level extra_fields on responses bodies", func(t *testing.T) {
		event, unary := responsesBodies(t)
		ctx := &fasthttp.RequestCtx{}
		for name, body := range map[string][]byte{"stream event": event, "unary": unary} {
			out := StripExtraFieldsIfInHeaders(ctx, body)
			assert.Equal(t, "openai", gjson.GetBytes(out, "extra_fields.provider").String(), "%s: %s", name, out)
			assert.Equal(t, 1, strings.Count(string(out), `"extra_fields"`), "%s carries exactly one extra_fields: %s", name, out)
		}
	})

	t.Run("headers mode leaves no extra_fields on responses bodies", func(t *testing.T) {
		event, unary := responsesBodies(t)
		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue(FastHTTPUserValueExtraFieldsInHeaders, true)
		for name, body := range map[string][]byte{"stream event": event, "unary": unary} {
			out := StripExtraFieldsIfInHeaders(ctx, body)
			assert.NotContains(t, string(out), `"extra_fields"`, "%s: %s", name, out)
			assert.Equal(t, "resp_1", gjson.GetBytes(out, "id").String()+gjson.GetBytes(out, "response.id").String(), "%s keeps its payload: %s", name, out)
		}
	})

	t.Run("strip-if is a no-op in body mode", func(t *testing.T) {
		body := []byte(`{"id":"x","extra_fields":{"latency":1}}`)
		assert.Equal(t, string(body), string(StripExtraFieldsIfInHeaders(&fasthttp.RequestCtx{}, body)))
		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue(FastHTTPUserValueExtraFieldsInHeaders, true)
		assert.Equal(t, `{"id":"x"}`, string(StripExtraFieldsIfInHeaders(ctx, body)))
	})
}

// BenchmarkApplyBifrostResponseHeaders_BodyMode pins the common-case cost: body mode adds one
// user-value lookup and no allocations over the pre-existing header writer.
func BenchmarkApplyBifrostResponseHeaders_BodyMode(b *testing.B) {
	ctx := &fasthttp.RequestCtx{}
	extra := schemas.BifrostResponseExtraFields{RequestType: schemas.ChatCompletionRequest, Provider: schemas.OpenAI, Latency: 42}
	b.ReportAllocs()
	for b.Loop() {
		ApplyBifrostResponseHeaders(ctx, nil, extra)
	}
}

// BenchmarkStripExtraFieldsIfInHeaders_BodyMode pins that body mode leaves the marshaled body untouched.
func BenchmarkStripExtraFieldsIfInHeaders_BodyMode(b *testing.B) {
	ctx := &fasthttp.RequestCtx{}
	body := []byte(`{"id":"x","extra_fields":{"latency":1}}`)
	b.ReportAllocs()
	for b.Loop() {
		_ = StripExtraFieldsIfInHeaders(ctx, body)
	}
}

// TestExposedResponseHeadersListsEveryGatewayHeader guards ExposedResponseHeaders against drift:
// every x-bifrost-* header constant in responseheaders.go must be exposed to browser clients.
func TestExposedResponseHeadersListsEveryGatewayHeader(t *testing.T) {
	src, err := os.ReadFile("responseheaders.go")
	assert.NoError(t, err)
	exposed := strings.Split(ExposedResponseHeaders, ", ")
	names := regexp.MustCompile(`= "(x-bifrost-[a-z0-9-]+)"`).FindAllStringSubmatch(string(src), -1)
	assert.NotEmpty(t, names)
	for _, m := range names {
		assert.Contains(t, exposed, m[1], "ExposedResponseHeaders is missing %s", m[1])
	}
	assert.Contains(t, exposed, HeaderRequestID)
}
