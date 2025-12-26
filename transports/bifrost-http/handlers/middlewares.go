package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// CorsMiddleware handles CORS headers for localhost and configured allowed origins
func CorsMiddleware(config *lib.Config) lib.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			logger.Debug("CorsMiddleware: %s", string(ctx.Path()))
			origin := string(ctx.Request.Header.Peek("Origin"))
			allowed := IsOriginAllowed(origin, config.ClientConfig.AllowedOrigins)
			// Check if origin is allowed (localhost always allowed + configured origins)
			if allowed {
				ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
				ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD")
				ctx.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-Stainless-Timeout")
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

// TransportInterceptorMiddleware collects all plugin interceptors and calls them one by one
func TransportInterceptorMiddleware(config *lib.Config, enterpriseOverrides lib.EnterpriseOverrides) lib.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// Get plugins from config - lock-free read
			plugins := config.GetLoadedPlugins()
			if len(plugins) == 0 {
				next(ctx)
				return
			}
			if enterpriseOverrides == nil {
				next(ctx)
				return
			}
			// If governance plugin is not loaded, skip interception
			hasGovernance := false
			for _, p := range plugins {
				if p.GetName() == enterpriseOverrides.GetGovernancePluginName() {
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
			requestBody := make(map[string]any)
			// Only read body if Content-Type is JSON to avoid consuming multipart/form-data streams
			contentType := string(ctx.Request.Header.Peek("Content-Type"))
			isJSONRequest := strings.HasPrefix(contentType, "application/json")

			// Only run interceptors for JSON requests
			if isJSONRequest {
				bodyBytes := ctx.Request.Body()
				if len(bodyBytes) > 0 {
					if err := json.Unmarshal(bodyBytes, &requestBody); err != nil {
						// If body is not valid JSON, log warning and continue without interception
						logger.Warn(fmt.Sprintf("[transportInterceptor]: Failed to unmarshal request body: %v, skipping interceptor", err))
						next(ctx)
						return
					}
				}
				for _, plugin := range plugins {
					// Call TransportInterceptor on all plugins
					pluginCtx, cancel := schemas.NewBifrostContextWithTimeout(ctx, 10*time.Second)
					modifiedHeaders, modifiedBody, err := plugin.TransportInterceptor(pluginCtx, string(ctx.Request.URI().RequestURI()), headers, requestBody)
					cancel()
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
					// Capturing plugin ctx values and putting them in the request context
					for k, v := range pluginCtx.GetUserValues() {
						ctx.SetUserValue(k, v)
					}
				}

				// Marshal the body back to JSON
				updatedBody, err := json.Marshal(requestBody)
				if err != nil {
					SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("TransportInterceptor: Failed to marshal request body: %v", err))
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
			}

			next(ctx)
		}
	}
}

// validateSession checks if a session token is valid
func validateSession(_ *fasthttp.RequestCtx, store configstore.ConfigStore, token string) bool {
	session, err := store.GetSession(context.Background(), token)
	if err != nil || session == nil {
		return false
	}
	if session.ExpiresAt.Before(time.Now()) {
		return false
	}
	return true
}

// AuthMiddleware if authConfig is set, it will verify the auth cookie in the header
// This uses basic auth style username + password based authentication
// No session tracking is used, so this is not suitable for production environments
// These basicauth routes are only used for the dashboard and API routes
func AuthMiddleware(store configstore.ConfigStore) lib.BifrostHTTPMiddleware {
	if store == nil {
		logger.Info("auth middleware is disabled because store is not present")
		return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
			return next
		}
	}
	authConfig, err := store.GetAuthConfig(context.Background())
	if err != nil || authConfig == nil || !authConfig.IsEnabled {
		return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
			return next
		}
	}
	whitelistedRoutes := []string{
		"/api/session/is-auth-enabled",
		"/api/session/login",
		"/api/session/logout",
		"/health",
	}
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// We skip authorization for the login route
			if slices.Contains(whitelistedRoutes, string(ctx.Request.URI().RequestURI())) {
				next(ctx)
				return
			}
			// Get the authorization header
			authorization := string(ctx.Request.Header.Peek("Authorization"))
			if authorization == "" {
				// Check if its a websocket 101 upgrade request
				if string(ctx.Request.Header.Peek("Upgrade")) == "websocket" {
					// Here we get the token from query params
					token := string(ctx.Request.URI().QueryArgs().Peek("token"))
					if token == "" {
						SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
						return
					}
					// Verify the session
					if !validateSession(ctx, store, token) {
						SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
						return
					}
					// Continue with the next handler
					next(ctx)
					return
				}
				SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
				return
			}
			// Split the authorization header into the scheme and the token
			scheme, token, ok := strings.Cut(authorization, " ")
			if !ok {
				SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
				return
			}
			// Checking basic auth for inference calls
			if scheme == "Basic" {
				// Decode the base64 token
				decodedBytes, err := base64.StdEncoding.DecodeString(token)
				if err != nil {
					SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
					return
				}
				// Split the decoded token into the username and password
				username, password, ok := strings.Cut(string(decodedBytes), ":")
				if !ok {
					SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
					return
				}
				// Verify the username and password
				if username != authConfig.AdminUserName {
					SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
					return
				}
				compare, err := encrypt.CompareHash(authConfig.AdminPassword, password)
				if err != nil {
					SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to compare password: %v", err))
					return
				}
				if !compare {
					SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
					return
				}
				// Continue with the next handler
				next(ctx)
				return
			}
			// Checking bearer auth for dashboard calls
			if scheme == "Bearer" {
				// Verify the session
				if !validateSession(ctx, store, token) {
					SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
					return
				}
				// Continue with the next handler
				next(ctx)
				return
			}
			SendError(ctx, fasthttp.StatusUnauthorized, "Unauthorized")
		}
	}
}
