package handlers

import (
	"strings"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// BrandingHandler serves the white-label branding read API.
//
// Only reads live here. The mutation endpoint is registered by the enterprise
// build, so on OSS there is no route that can write the blob — and the blob
// itself lives in an enterprise-owned table that OSS has no model or migration
// for, so there is nothing for these reads to find. Should a store ever reach
// an OSS process anyway, lib.BrandingService is constructed inert there and the
// reads below still report default branding.
type BrandingHandler struct {
	branding *lib.BrandingService
}

// NewBrandingHandler creates a new BrandingHandler.
func NewBrandingHandler(branding *lib.BrandingService) *BrandingHandler {
	return &BrandingHandler{branding: branding}
}

// RegisterRoutes wires the branding read endpoints onto r, composing whatever
// middleware the caller supplies.
//
// The server registers these as open routes — it passes no middleware at all,
// because the login screen renders the logo before any session exists and
// gating these behind auth would guarantee the default Bifrost logo flashes on
// every login. That decision lives at the call site rather than here, so this
// handler never silently drops a middleware it was handed.
func (h *BrandingHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/branding", lib.ChainMiddlewares(h.getBranding, middlewares...))
	r.GET("/api/branding/asset/{kind}", lib.ChainMiddlewares(h.getAsset, middlewares...))
}

// brandingResponse describes which slots are overridden and where to fetch
// them. The base64 payloads are deliberately excluded: the UI renders these
// through <img src>, so shipping the bytes inline in JSON would bloat the
// response and defeat browser caching of the images.
type brandingResponse struct {
	// Enabled is true when at least one slot is overridden. The UI uses it as a
	// single check for "this deployment is white-labelled".
	Enabled bool `json:"enabled"`
	// HasLogo/HasIcon let the UI fall back per slot, since a deployment may
	// override only one of the two.
	HasLogo bool `json:"has_logo"`
	HasIcon bool `json:"has_icon"`
	// LogoURL/IconURL are empty when the slot is not overridden. They carry a
	// content-derived version query so a re-upload busts any cached copy.
	LogoURL   string `json:"logo_url,omitempty"`
	IconURL   string `json:"icon_url,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// getBranding handles GET /api/branding - report the active branding overrides.
// A deployment with no branding configured gets a well-formed "everything is
// default" response rather than a 404, so the UI has one code path.
func (h *BrandingHandler) getBranding(ctx *fasthttp.RequestCtx) {
	blob := h.branding.Get(ctx)
	resp := brandingResponse{
		HasLogo: blob.HasLogo(),
		HasIcon: blob.HasIcon(),
	}
	resp.Enabled = resp.HasLogo || resp.HasIcon
	if blob != nil {
		resp.UpdatedAt = blob.UpdatedAt
	}
	// Version the asset URLs by ETag so a new upload is fetched immediately
	// despite the long cache lifetime set on the asset responses.
	if resp.HasLogo {
		if asset := h.branding.Asset(ctx, lib.BrandingAssetLogo); asset != nil {
			resp.LogoURL = "/api/branding/asset/logo?v=" + strings.Trim(asset.ETag, `"`)
		}
	}
	if resp.HasIcon {
		if asset := h.branding.Asset(ctx, lib.BrandingAssetIcon); asset != nil {
			resp.IconURL = "/api/branding/asset/icon?v=" + strings.Trim(asset.ETag, `"`)
		}
	}
	// The metadata document itself must not be cached, otherwise a browser
	// would keep resolving stale asset URLs after a re-upload.
	ctx.Response.Header.Set("Cache-Control", "no-cache")
	SendJSON(ctx, resp)
}

// getAsset handles GET /api/branding/asset/{kind} - serve the raw image bytes
// for the logo or icon slot. Returns 404 when the slot is not overridden, which
// is the signal for the caller to use the bundled default asset.
func (h *BrandingHandler) getAsset(ctx *fasthttp.RequestCtx) {
	kind, _ := ctx.UserValue("kind").(string)
	if kind != lib.BrandingAssetLogo && kind != lib.BrandingAssetIcon {
		SendError(ctx, fasthttp.StatusNotFound, "unknown branding asset")
		return
	}

	asset := h.branding.Asset(ctx, kind)
	if asset == nil {
		SendError(ctx, fasthttp.StatusNotFound, "no custom branding configured for this asset")
		return
	}

	// nosniff keeps a browser from re-interpreting the bytes as anything other
	// than the declared image type, and the restrictive CSP means that even if
	// a payload slipped past the MIME allowlist it could not load scripts or
	// subresources when opened directly.
	ctx.Response.Header.Set("X-Content-Type-Options", "nosniff")
	ctx.Response.Header.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	ctx.Response.Header.Set("ETag", asset.ETag)

	// The URL is content-versioned by the metadata endpoint, so the bytes at a
	// given ?v= never change and can be cached hard. private: branding may be
	// specific to an internal deployment and should not sit in shared proxies.
	ctx.Response.Header.Set("Cache-Control", "private, max-age=86400")

	// Honour conditional requests so repeat loads cost a 304 rather than the
	// full image.
	if string(ctx.Request.Header.Peek("If-None-Match")) == asset.ETag {
		ctx.SetStatusCode(fasthttp.StatusNotModified)
		return
	}

	ctx.SetContentType(asset.MIME)
	ctx.SetBody(asset.Data)
}
