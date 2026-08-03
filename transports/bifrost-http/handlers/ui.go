package handlers

import (
	"bytes"
	"embed"
	"mime"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const uiDevServerAddr = "localhost:3000"

// shellLogoPaths are the bundled logo paths referenced by the static
// pre-hydration shell in index.html. The shell embeds both the light and dark
// asset and hides one with CSS; a single uploaded logo serves both, so every
// occurrence is rewritten to the same URL.
//
// Neither path is a substring of the other, so the replacement order does not
// matter.
var shellLogoPaths = []string{
	"/bifrost-logo.webp",
	"/bifrost-logo-dark.webp",
}

// shellBrandedLogoPath is the endpoint the shell's logo is repointed at when a
// deployment has uploaded a custom one.
const shellBrandedLogoPath = "/api/branding/asset/logo"

// UIHandler handles UI routes.
type UIHandler struct {
	uiContent embed.FS
	// uiDevClient proxies dashboard requests to the local Vite dev server.
	// It is only set when dev mode is enabled (see NewUIHandler); nil otherwise.
	uiDevClient *fasthttp.HostClient
	// branding rewrites the logo in the pre-hydration shell. nil disables the
	// rewrite entirely, which is the OSS path.
	branding *lib.BrandingService
}

// NewUIHandler creates a new UIHandler instance. branding may be nil, in which
// case index.html is always served exactly as embedded.
func NewUIHandler(uiContent embed.FS, branding *lib.BrandingService) *UIHandler {
	h := &UIHandler{
		uiContent: uiContent,
		branding:  branding,
	}
	// Only wire the dev-server proxy client when running in dev mode. Timeouts
	// guard against the local Vite server hanging dashboard requests if it is
	// unresponsive, falling back to the embedded UI instead.
	if IsDevMode() {
		h.uiDevClient = &fasthttp.HostClient{
			Addr:         uiDevServerAddr,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		}
	}
	return h
}

// RegisterRoutes registers the UI routes with the provided router.
func (h *UIHandler) RegisterRoutes(router *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	router.GET("/", lib.ChainMiddlewares(h.serveDashboard, middlewares...))
	router.GET("/{filepath:*}", lib.ChainMiddlewares(h.serveDashboard, middlewares...))
}

// serveDashboard serves the dashboard UI.
func (h *UIHandler) serveDashboard(ctx *fasthttp.RequestCtx) {
	if IsDevMode() && h.serveDevDashboard(ctx) {
		return
	}

	// Get the request path
	requestPath := string(ctx.Path())

	// Clean the path to prevent directory traversal
	cleanPath := path.Clean(requestPath)

	// Handle .txt files - map from /{page}.txt to /{page}/index.txt
	if strings.HasSuffix(cleanPath, ".txt") {
		// Remove .txt extension and add /index.txt
		basePath := strings.TrimSuffix(cleanPath, ".txt")
		if basePath == "/" || basePath == "" {
			basePath = "/index"
		}
		cleanPath = basePath + "/index.txt"
	}

	// Remove leading slash and add ui prefix
	if cleanPath == "/" {
		cleanPath = "ui/index.html"
	} else {
		cleanPath = "ui" + cleanPath
	}

	// Block hidden directories and files (any path segment starting with .)
	segments := strings.Split(cleanPath, "/")
	for _, segment := range segments {
		if strings.HasPrefix(segment, ".") {
			ctx.SetStatusCode(fasthttp.StatusNotFound)
			ctx.SetBodyString("404 - Not found")
			return
		}
	}

	// Block sensitive files
	baseName := filepath.Base(cleanPath)
	sensitiveFiles := []string{"package.json", "package-lock.json"}
	for _, sensitive := range sensitiveFiles {
		if baseName == sensitive {
			ctx.SetStatusCode(fasthttp.StatusNotFound)
			ctx.SetBodyString("404 - Not found")
			return
		}
	}

	// Check if this is a static asset request (has file extension)
	hasExtension := strings.Contains(filepath.Base(cleanPath), ".")

	// Try to read the file from embedded filesystem
	data, err := h.uiContent.ReadFile(cleanPath)
	if err != nil {

		// If it's a static asset (has extension) and not found, return 404
		if hasExtension {
			ctx.SetStatusCode(fasthttp.StatusNotFound)
			ctx.SetBodyString("404 - Static asset not found: " + requestPath)
			return
		}

		// For routes without extensions (SPA routing), try {path}/index.html first
		if !hasExtension {
			indexPath := cleanPath + "/index.html"
			data, err = h.uiContent.ReadFile(indexPath)
			if err == nil {
				cleanPath = indexPath
			} else {
				// If that fails, serve root index.html as fallback
				data, err = h.uiContent.ReadFile("ui/index.html")
				if err != nil {
					ctx.SetStatusCode(fasthttp.StatusNotFound)
					ctx.SetBodyString("404 - File not found")
					return
				}
				cleanPath = "ui/index.html"
			}
		} else {
			ctx.SetStatusCode(fasthttp.StatusNotFound)
			ctx.SetBodyString("404 - File not found")
			return
		}
	}

	// Point the static skeleton at the custom logo. Without this the shell
	// renders the bundled Bifrost logo for the moment before the bundle boots
	// and React swaps in the real branding, which reads as a flash of the wrong
	// company's mark on every page load.
	//
	// The rewrite is skipped entirely when no branding is configured, so the
	// OSS path serves the embedded bytes untouched.
	if ext := filepath.Ext(cleanPath); ext == ".html" {
		data = h.applyShellBranding(ctx, data)
	}

	// Set content type based on file extension
	ext := filepath.Ext(cleanPath)
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	ctx.SetContentType(contentType)

	// Set cache headers for static assets
	if strings.HasPrefix(cleanPath, "ui/assets/") {
		ctx.Response.Header.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else if ext == ".html" {
		ctx.Response.Header.Set("Cache-Control", "no-cache")
	} else {
		ctx.Response.Header.Set("Cache-Control", "public, max-age=3600")
	}

	// Send the file content
	ctx.SetBody(data)
}

// applyShellBranding rewrites the bundled logo paths in the pre-hydration shell
// to the branding asset endpoint. It returns data unchanged when no custom logo
// is configured. On OSS that is the only outcome: the branding service is
// constructed there with no store and inert besides, so Asset short-circuits
// without any database work and the shell is always served exactly as embedded.
//
// Only the logo is rewritten: the shell's skeleton does not render the square
// icon, so there is nothing to swap for that slot.
func (h *UIHandler) applyShellBranding(ctx *fasthttp.RequestCtx, data []byte) []byte {
	if h.branding == nil {
		return data
	}
	asset := h.branding.Asset(ctx, lib.BrandingAssetLogo)
	if asset == nil {
		return data
	}

	// Version the URL by content hash so the shell picks up a re-upload
	// immediately despite the asset's long cache lifetime.
	target := []byte(shellBrandedLogoPath + "?v=" + strings.Trim(asset.ETag, `"`))

	out := data
	for _, original := range shellLogoPaths {
		out = bytes.ReplaceAll(out, []byte(original), target)
	}
	return out
}

// serveDevDashboard proxies dashboard requests to the local Vite dev server.
// Restricted to loopback clients: if the dev server happens to be bound to a
// non-loopback address, a remote client must not be able to tunnel to
// Vite-internal endpoints (e.g. /@fs/) via this proxy.
func (h *UIHandler) serveDevDashboard(ctx *fasthttp.RequestCtx) bool {
	if h.uiDevClient == nil {
		return false
	}
	if !ctx.RemoteIP().IsLoopback() {
		return false
	}

	var req fasthttp.Request
	var resp fasthttp.Response
	ctx.Request.CopyTo(&req)
	req.URI().SetScheme("http")
	req.URI().SetHost(uiDevServerAddr)
	req.Header.SetHost(uiDevServerAddr)

	if err := h.uiDevClient.Do(&req, &resp); err != nil {
		// Dev server unreachable (e.g. Vite not running); fall back to the
		// embedded UI by signalling the caller to serve from uiContent.
		return false
	}

	resp.CopyTo(&ctx.Response)
	return true
}
