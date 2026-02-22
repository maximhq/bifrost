package handlers

import (
	"io/fs"
	"mime"
	"path"
	"path/filepath"
	"strings"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// UIHandler handles UI routes.
type UIHandler struct {
	uiContent fs.FS
}

// NewUIHandler creates a new UIHandler instance.
func NewUIHandler(uiContent fs.FS) *UIHandler {
	return &UIHandler{
		uiContent: uiContent,
	}
}

// RegisterRoutes registers the UI routes with the provided router.
func (h *UIHandler) RegisterRoutes(router *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	router.GET("/", lib.ChainMiddlewares(h.serveDashboard, middlewares...))
	router.GET("/{filepath:*}", lib.ChainMiddlewares(h.serveDashboard, middlewares...))
}

// ServeDashboard serves the dashboard UI.
func (h *UIHandler) serveDashboard(ctx *fasthttp.RequestCtx) {
	// Get the request path
	requestPath := string(ctx.Path())

	// Clean the path to prevent directory traversal
	cleanPath := path.Clean(requestPath)

	// Handle .txt files (Next.js RSC payload files) - map from /{page}.txt to /{page}/index.txt
	if strings.HasSuffix(cleanPath, ".txt") {
		// Remove .txt extension and add /index.txt
		basePath := strings.TrimSuffix(cleanPath, ".txt")
		if basePath == "/" || basePath == "" {
			basePath = "/index"
		}
		cleanPath = basePath + "/index.txt"
	}

	// Remove leading slash for fs.FS (which uses relative paths)
	if cleanPath == "/" {
		cleanPath = "index.html"
	} else {
		cleanPath = strings.TrimPrefix(cleanPath, "/")
	}

	// Check if this is a static asset request (has file extension)
	hasExtension := strings.Contains(filepath.Base(cleanPath), ".")

	// Try to read the file from filesystem
	data, err := fs.ReadFile(h.uiContent, cleanPath)
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
			data, err = fs.ReadFile(h.uiContent, indexPath)
			if err == nil {
				cleanPath = indexPath
			} else {
				// If that fails, serve root index.html as fallback
				data, err = fs.ReadFile(h.uiContent, "index.html")
				if err != nil {
					ctx.SetStatusCode(fasthttp.StatusNotFound)
					ctx.SetBodyString("404 - File not found")
					return
				}
				cleanPath = "index.html"
			}
		} else {
			ctx.SetStatusCode(fasthttp.StatusNotFound)
			ctx.SetBodyString("404 - File not found")
			return
		}
	}

	// Set content type based on file extension
	ext := filepath.Ext(cleanPath)
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	ctx.SetContentType(contentType)

	// Set cache headers for static assets
	if strings.HasPrefix(cleanPath, "_next/static/") {
		ctx.Response.Header.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else if ext == ".html" {
		ctx.Response.Header.Set("Cache-Control", "no-cache")
	} else {
		ctx.Response.Header.Set("Cache-Control", "public, max-age=3600")
	}

	// Send the file content
	ctx.SetBody(data)
}
