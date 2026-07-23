package lib

// Routed-identity response headers. Shared by the native `/v1` handlers and
// the drop-in integration routes: both surfaces emit the same `x-bifrost-*`
// header contract on successful responses, so the writer lives here rather
// than in either package.

import (
	"strconv"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// HTTP response header names for the routed identity. Set on every successful
// response from any integration so callers can recover the actual provider /
// model that handled the request — even when the integration converts the
// body to a provider-native shape (Anthropic, OpenAI, Bedrock) that has no
// place to surface Bifrost's `extra_fields`.
//
// Header values are derived from BifrostResponseExtraFields (populated by
// PopulateExtraFields at the end of every request path, including after
// fallback / routing-rule resolution). FallbackIndex is read from the
// BifrostContext because it isn't a field on the response struct.
//
// Naming follows the existing `x-bf-*` request-side convention (see
// `x-bf-vk`, `x-bf-key-id`, etc.).
const (
	HeaderBifrostProvider      = "x-bifrost-provider"
	HeaderBifrostOriginalModel = "x-bifrost-original-model"
	HeaderBifrostResolvedModel = "x-bifrost-resolved-model"
	HeaderBifrostFallbackIndex = "x-bifrost-fallback-index"
	HeaderBifrostRequestType   = "x-bifrost-request-type"
	// Cumulative milliseconds this request spent blocked on upstream sockets,
	// summed across every attempt and fallback. Subtract from the caller's own
	// elapsed time to get what Bifrost cost. Distinct from the per-attempt
	// latency in the response body's extra_fields, which only holds the last try.
	HeaderBifrostUpstreamLatency = "x-bifrost-upstream-latency-ms"
)

// Headers mirroring the non-deprecated ExtraFields.RoutingInfo fields 1:1.
// Emitted alongside the deprecated set above so header consumers get both
// generations, matching the JSON body contract on native routes.
const (
	HeaderBifrostRoutingInfoProvider                = "x-bifrost-routing-info-provider"
	HeaderBifrostRoutingInfoModel                   = "x-bifrost-routing-info-model"
	HeaderBifrostRoutingInfoKey                     = "x-bifrost-routing-info-key"
	HeaderBifrostRoutingInfoAliasModelID            = "x-bifrost-routing-info-alias-model-id"
	HeaderBifrostRoutingInfoAliasModelName          = "x-bifrost-routing-info-alias-model-name"
	HeaderBifrostRoutingInfoAliasModelFamily        = "x-bifrost-routing-info-alias-model-family"
	HeaderBifrostRoutingInfoIsFallback              = "x-bifrost-routing-info-is-fallback"
	HeaderBifrostRoutingInfoPrimaryProvider         = "x-bifrost-routing-info-primary-provider"
	HeaderBifrostRoutingInfoPrimaryModel            = "x-bifrost-routing-info-primary-model"
	HeaderBifrostRoutingInfoServerSideFallbackModel = "x-bifrost-routing-info-server-side-fallback-model"
)

// ApplyBifrostStreamResponseHeaders emits the routed-identity headers for a
// streaming response, before the first SSE write. Streams only carry
// ExtraFields on chunks — none exist at header-write time — so the identity
// comes from the RoutingInfo snapshot core stashes in the context at stream
// setup (BifrostContextKeyRoutingInfo). Absent snapshot (e.g. a plugin
// short-circuited the stream) emits only the request-type header.
func ApplyBifrostStreamResponseHeaders(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, requestType schemas.RequestType) {
	if bifrostCtx == nil {
		return
	}
	extra := schemas.BifrostResponseExtraFields{RequestType: requestType}
	if ri, ok := bifrostCtx.Value(schemas.BifrostContextKeyRoutingInfo).(schemas.RoutingInfo); ok {
		extra = ri.ToExtraFields(requestType)
	}
	ApplyBifrostResponseHeaders(ctx, bifrostCtx, extra)
}

// ApplyBifrostResponseHeaders writes both the upstream provider response
// headers (forwarded verbatim) and the bifrost-level `x-bifrost-*` routing
// identity headers onto the fasthttp response. Empty fields are skipped so
// the headers never appear with a blank value. Safe to call when the caller
// didn't populate `extra` — the zero value for ExtraFields produces no
// headers.
func ApplyBifrostResponseHeaders(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, extra schemas.BifrostResponseExtraFields) {
	for key, value := range extra.ProviderResponseHeaders {
		ctx.Response.Header.Set(key, value)
	}
	if extra.Provider != "" {
		ctx.Response.Header.Set(HeaderBifrostProvider, string(extra.Provider))
	}
	if extra.OriginalModelRequested != "" {
		ctx.Response.Header.Set(HeaderBifrostOriginalModel, extra.OriginalModelRequested)
	}
	if extra.ResolvedModelUsed != "" {
		ctx.Response.Header.Set(HeaderBifrostResolvedModel, extra.ResolvedModelUsed)
	}
	if extra.RequestType != "" {
		ctx.Response.Header.Set(HeaderBifrostRequestType, string(extra.RequestType))
	}
	ri := extra.RoutingInfo
	if ri.Provider != "" {
		ctx.Response.Header.Set(HeaderBifrostRoutingInfoProvider, string(ri.Provider))
	}
	if ri.Model != "" {
		ctx.Response.Header.Set(HeaderBifrostRoutingInfoModel, ri.Model)
	}
	if ri.Key != "" {
		ctx.Response.Header.Set(HeaderBifrostRoutingInfoKey, ri.Key)
	}
	if ri.ResolvedKeyAlias != nil {
		if ri.ResolvedKeyAlias.ModelID != "" {
			ctx.Response.Header.Set(HeaderBifrostRoutingInfoAliasModelID, ri.ResolvedKeyAlias.ModelID)
		}
		if ri.ResolvedKeyAlias.ModelName != nil && *ri.ResolvedKeyAlias.ModelName != "" {
			ctx.Response.Header.Set(HeaderBifrostRoutingInfoAliasModelName, *ri.ResolvedKeyAlias.ModelName)
		}
		if ri.ResolvedKeyAlias.ModelFamily != nil && *ri.ResolvedKeyAlias.ModelFamily != "" {
			ctx.Response.Header.Set(HeaderBifrostRoutingInfoAliasModelFamily, string(*ri.ResolvedKeyAlias.ModelFamily))
		}
	}
	// Booleans follow the fallback-index convention: absent = false.
	if ri.IsFallback {
		ctx.Response.Header.Set(HeaderBifrostRoutingInfoIsFallback, "true")
	}
	if ri.PrimaryProvider != nil && *ri.PrimaryProvider != "" {
		ctx.Response.Header.Set(HeaderBifrostRoutingInfoPrimaryProvider, string(*ri.PrimaryProvider))
	}
	if ri.PrimaryModel != nil && *ri.PrimaryModel != "" {
		ctx.Response.Header.Set(HeaderBifrostRoutingInfoPrimaryModel, *ri.PrimaryModel)
	}
	if ri.ServerSideFallbackModel != nil && *ri.ServerSideFallbackModel != "" {
		ctx.Response.Header.Set(HeaderBifrostRoutingInfoServerSideFallbackModel, *ri.ServerSideFallbackModel)
	}
	// Fallback index lives on the request context, not the response struct.
	// 0 = primary provider succeeded; non-zero = which fallback fired
	// (1-indexed). Only emit when non-zero so the absence of the header is
	// the unambiguous "no fallback fired" signal.
	if bifrostCtx != nil {
		if idx, ok := bifrostCtx.Value(schemas.BifrostContextKeyFallbackIndex).(int); ok && idx > 0 {
			ctx.Response.Header.Set(HeaderBifrostFallbackIndex, strconv.Itoa(idx))
		}
		if upstream, ok := schemas.GetUpstreamLatency(bifrostCtx); ok {
			ctx.Response.Header.Set(HeaderBifrostUpstreamLatency,
				strconv.FormatFloat(float64(upstream)/float64(time.Millisecond), 'f', 3, 64))
		}
	}
}
