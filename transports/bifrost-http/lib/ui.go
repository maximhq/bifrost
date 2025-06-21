package lib

import (
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/valyala/fasthttp"
)

// UIConfig holds configuration for UI serving
type UIConfig struct {
	DevMode         bool   // Whether to use development proxy
	DevServerURL    string // URL of the development server (default: http://localhost:3000)
	StaticDir       string // Directory containing static UI files (default: ../../ui/out)
	BasePath        string // Base path for UI routes (default: /ui)
}

// UIHandler handles UI requests, either by serving static files or proxying to dev server
type UIHandler struct {
	config    UIConfig
	proxy     *httputil.ReverseProxy
	staticDir string
}

// NewUIHandler creates a new UI handler with the given configuration
func NewUIHandler(config UIConfig) *UIHandler {
	// Set defaults
	if config.DevServerURL == "" {
		config.DevServerURL = "http://localhost:3000"
	}
	if config.StaticDir == "" {
		// Default path when running from bifrost-http directory
		config.StaticDir = "../../ui/out"
		// Check if running from root directory
		if _, err := os.Stat("ui/out"); err == nil {
			config.StaticDir = "ui/out"
		}
	}
	if config.BasePath == "" {
		config.BasePath = "/ui"
	}

	handler := &UIHandler{
		config: config,
	}

	// Set up proxy for development mode
	if config.DevMode {
		target, err := url.Parse(config.DevServerURL)
		if err != nil {
			log.Printf("Warning: Invalid dev server URL %s: %v", config.DevServerURL, err)
			config.DevMode = false
		} else {
			handler.proxy = httputil.NewSingleHostReverseProxy(target)
			log.Printf("UI: Development mode enabled, proxying to %s", config.DevServerURL)
			log.Printf("UI: Make sure to start the UI dev server with 'make ui-dev' in another terminal")
		}
	}

	// Set up static file serving
	if !config.DevMode {
		// Convert relative path to absolute
		absPath, err := filepath.Abs(config.StaticDir)
		if err != nil {
			log.Printf("Warning: Could not resolve static directory %s: %v", config.StaticDir, err)
			handler.staticDir = config.StaticDir
		} else {
			handler.staticDir = absPath
		}

		// Check if static directory exists
		if _, err := os.Stat(handler.staticDir); os.IsNotExist(err) {
			log.Printf("Warning: Static UI directory does not exist: %s", handler.staticDir)
			log.Printf("Run 'make ui-build' to generate static files")
		} else {
			log.Printf("UI: Production mode enabled, serving static files from %s", handler.staticDir)
		}
	}

	return handler
}

// HandleUI handles UI requests using FastHTTP
func (h *UIHandler) HandleUI(ctx *fasthttp.RequestCtx) {
	if h.config.DevMode && h.proxy != nil {
		h.handleDevProxy(ctx)
	} else {
		h.handleStaticFiles(ctx)
	}
}

// handleDevProxy proxies requests to the development server
func (h *UIHandler) handleDevProxy(ctx *fasthttp.RequestCtx) {
	// First, try to connect to the dev server to check if it's running
	target, _ := url.Parse(h.config.DevServerURL)
	conn, err := net.DialTimeout("tcp", target.Host, 1*time.Second)
	if err != nil {
		// Dev server is not running, show helpful error page
		h.showDevServerError(ctx)
		return
	}
	conn.Close()

	// Convert FastHTTP request to net/http request
	req := &http.Request{
		Method: string(ctx.Method()),
		URL: &url.URL{
			Path:     string(ctx.Path()),
			RawQuery: string(ctx.QueryArgs().QueryString()),
		},
		Header: make(http.Header),
		Body:   nil,
	}

	// Copy headers
	ctx.Request.Header.VisitAll(func(key, value []byte) {
		req.Header.Add(string(key), string(value))
	})

	// Create a response recorder
	recorder := &responseRecorder{
		statusCode: 200,
		headers:    make(http.Header),
	}

	// Proxy the request
	h.proxy.ServeHTTP(recorder, req)

	// Copy response back to FastHTTP
	ctx.SetStatusCode(recorder.statusCode)
	for key, values := range recorder.headers {
		for _, value := range values {
			ctx.Response.Header.Add(key, value)
		}
	}
	ctx.SetBody(recorder.body)
}

// showDevServerError displays a helpful error page when the dev server is not running
func (h *UIHandler) showDevServerError(ctx *fasthttp.RequestCtx) {
	ctx.SetStatusCode(fasthttp.StatusServiceUnavailable)
	ctx.SetContentType("text/html; charset=utf-8")
	ctx.SetBodyString(`<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <title>UI Development Server Not Running</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 40px; background: #f5f5f5; }
        .container { background: white; padding: 30px; border-radius: 8px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); max-width: 800px; }
        .error { color: #d32f2f; margin-bottom: 20px; }
        .command { background: #f5f5f5; padding: 10px; border-radius: 4px; font-family: monospace; margin: 10px 0; }
        .steps { background: #e3f2fd; padding: 15px; border-radius: 4px; margin: 15px 0; }
        .icon { font-size: 24px; margin-right: 10px; }
    </style>
</head>
<body>
    <div class="container">
        <h1><span class="icon">🚧</span>UI Development Server Not Running</h1>
        <div class="error">
            <strong>Error:</strong> Cannot connect to UI development server at localhost:3000
        </div>
        
        <div class="steps">
            <h3>To fix this issue:</h3>
            <p><strong>Use the integrated development command:</strong></p>
            <div class="command">make dev-ui</div>
            <p><em>This will automatically start both the UI dev server and API server with proxy.</em></p>
        </div>
        
        <p><strong>Alternative:</strong> If you want to use static files instead of development proxy, use:</p>
        <div class="command">make dev</div>
        
        <p>The UI development server should start automatically with <code>make dev-ui</code>.</p>
    </div>
</body>
</html>`)
}

// handleStaticFiles serves static files from the build directory
func (h *UIHandler) handleStaticFiles(ctx *fasthttp.RequestCtx) {
	// Extract the filepath parameter from the router
	routeFilepath := ctx.UserValue("filepath")
	
	var path string
	if routeFilepath != nil {
		path = "/" + routeFilepath.(string)
	} else {
		// This is the root /ui/ route
		path = "/"
	}

	// Default to index.html for root path or paths without extension
	if path == "" || path == "/" {
		path = "/index.html"
	}

	// Ensure path starts with /
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	// Security: prevent directory traversal
	if strings.Contains(path, "..") {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		ctx.SetBodyString("Invalid path")
		return
	}

	// Build full file path
	filePath := filepath.Join(h.staticDir, path)

	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		// For SPA routing, serve index.html for non-API routes
		if !strings.HasPrefix(path, "/api/") && !strings.Contains(path, ".") {
			filePath = filepath.Join(h.staticDir, "index.html")
		} else {
			ctx.SetStatusCode(fasthttp.StatusNotFound)
			ctx.SetBodyString("File not found")
			return
		}
	}

	// Serve the file
	fasthttp.ServeFile(ctx, filePath)
}

// responseRecorder implements http.ResponseWriter for proxy compatibility
type responseRecorder struct {
	statusCode int
	headers    http.Header
	body       []byte
}

func (r *responseRecorder) Header() http.Header {
	return r.headers
}

func (r *responseRecorder) Write(data []byte) (int, error) {
	r.body = append(r.body, data...)
	return len(data), nil
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
}

// IsDevModeEnabled checks if development mode should be enabled
// It checks for the presence of BIFROST_UI_DEV environment variable
func IsDevModeEnabled() bool {
	return os.Getenv("BIFROST_UI_DEV") == "true"
} 