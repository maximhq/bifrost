package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// CorsMiddleware handles CORS headers for localhost and configured allowed origins
func CorsMiddleware(config *lib.Config) lib.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			origin := string(ctx.Request.Header.Peek("Origin"))
			allowed := IsOriginAllowed(origin, config.ClientConfig.AllowedOrigins)
			// Check if origin is allowed (localhost always allowed + configured origins)
			if allowed {
				ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
				ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				ctx.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
				ctx.Response.Header.Set("Access-Control-Allow-Credentials", "true")
				ctx.Response.Header.Set("Access-Control-Max-Age", "86400")
			}
			// Handle preflight OPTIONS requests
			if string(ctx.Method()) == "OPTIONS" {
				if allowed {
					ctx.SetStatusCode(fasthttp.StatusOK)
				} else {
					ctx.SetStatusCode(fasthttp.StatusForbidden)
				}
				return
			}
			next(ctx)
		}
	}
}

func TransportInterceptorMiddleware(config *lib.Config) lib.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// Get plugins from config - lock-free read
			plugins := config.GetLoadedPlugins()
			if len(plugins) == 0 {
				next(ctx)
				return
			}

			// If governance plugin is not loaded, skip interception
			hasGovernance := false
			for _, p := range plugins {
				if p.GetName() == governance.PluginName {
					hasGovernance = true
					break
				}
			}
			if !hasGovernance {
				next(ctx)
				return
			}

			// Parse headers
			headers := make(map[string]string)
			originalHeaderNames := make([]string, 0, 16)
			ctx.Request.Header.All()(func(key, value []byte) bool {
				name := string(key)
				headers[name] = string(value)
				originalHeaderNames = append(originalHeaderNames, name)

				return true
			})

			// Unmarshal request body
			requestBody := make(map[string]any)
			bodyBytes := ctx.Request.Body()
			if len(bodyBytes) > 0 {
				if err := json.Unmarshal(bodyBytes, &requestBody); err != nil {
					// If body is not valid JSON, log warning and continue without interception
					logger.Warn(fmt.Sprintf("TransportInterceptor: Failed to unmarshal request body: %v", err))
					next(ctx)
					return
				}
			}

			// Call TransportInterceptor on all plugins
			for _, plugin := range plugins {
				modifiedHeaders, modifiedBody, err := plugin.TransportInterceptor(string(ctx.Request.URI().RequestURI()), headers, requestBody)
				if err != nil {
					logger.Warn(fmt.Sprintf("TransportInterceptor: Plugin '%s' returned error: %v", plugin.GetName(), err))
					// Continue with unmodified headers/body
					continue
				}
				// Update headers and body with modifications
				if modifiedHeaders != nil {
					headers = modifiedHeaders
				}
				if modifiedBody != nil {
					requestBody = modifiedBody
				}
			}

			// Marshal the body back to JSON
			updatedBody, err := json.Marshal(requestBody)
			if err != nil {
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("TransportInterceptor: Failed to marshal request body: %v", err), logger)
				return
			}
			ctx.Request.SetBody(updatedBody)

			// Remove headers that were present originally but removed by plugins
			for _, name := range originalHeaderNames {
				if _, exists := headers[name]; !exists {
					ctx.Request.Header.Del(name)
				}
			}

			// Set modified headers back on the request
			for key, value := range headers {
				ctx.Request.Header.Set(key, value)
			}

			next(ctx)
		}
	}
}
