package handlers

import (
	"context"
	"encoding/base64"
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
func CorsMiddleware(config *lib.Config) schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			origin := string(ctx.Request.Header.Peek("Origin"))
			allowed := IsOriginAllowed(origin, config.ClientConfig.AllowedOrigins)
			// Check if origin is allowed (localhost always allowed + configured origins)
			if allowed {
				ctx.Response.Header.Set("Access-Control-Allow-Origin", origin)
				ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS")
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

// TransportInterceptorMiddleware collects all plugin interceptors and calls them one by one
func TransportInterceptorMiddleware(config *lib.Config) schemas.BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			// Get plugins from config - lock-free read
			plugins := config.GetLoadedPlugins()
			if len(plugins) == 0 {
				next(ctx)
				return
			}			
			pluginsMiddlewareChain := []schemas.BifrostHTTPMiddleware{}
			for _, plugin := range plugins {
				middleware := plugin.HTTPTransportMiddleware()
				// Call TransportInterceptor on all plugins
				if middleware == nil {
					continue
				}				
				pluginsMiddlewareChain = append(pluginsMiddlewareChain, middleware)
			}
			lib.ChainMiddlewares(next, pluginsMiddlewareChain...)(ctx)			
		}
	}
}

// validateSession checks if a session token is valid
func validateSession(ctx *fasthttp.RequestCtx, store configstore.ConfigStore, token string) bool {
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
func AuthMiddleware(store configstore.ConfigStore) schemas.BifrostHTTPMiddleware {
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
