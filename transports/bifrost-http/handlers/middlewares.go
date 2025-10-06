package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// BifrostHTTPMiddleware is a middleware function for the Bifrost HTTP transport
// It follows the standard pattern: receives the next handler and returns a new handler
type BifrostHTTPMiddleware func(next fasthttp.RequestHandler) fasthttp.RequestHandler

// CorsMiddleware handles CORS headers for localhost and configured allowed origins
func CorsMiddleware(config *lib.Config) BifrostHTTPMiddleware {
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

func VKProviderRoutingMiddleware(config *lib.Config, logger schemas.Logger) BifrostHTTPMiddleware {
	return func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) {
			if !config.LoadedPlugins[governance.PluginName] {
				next(ctx)
				return
			}
			var virtualKeyValue string
			// Extract x-bf-vk header
			ctx.Request.Header.All()(func(key, value []byte) bool {
				if strings.ToLower(string(key)) == "x-bf-vk" {
					virtualKeyValue = string(value)
				}
				return true
			})
			// If no virtual key, continue to next handler
			if virtualKeyValue == "" {
				next(ctx)
				return
			}
			// Only process POST requests with a body
			if string(ctx.Method()) != "POST" {
				next(ctx)
				return
			}
			// Get the request body
			body := ctx.Request.Body()
			if len(body) == 0 {
				next(ctx)
				return
			}
			// Parse the request body to extract the model field
			var requestBody map[string]interface{}
			if err := json.Unmarshal(body, &requestBody); err != nil {
				// If we can't parse as JSON, continue without modification
				next(ctx)
				return
			}
			// Check if the request has a model field
			modelValue, hasModel := requestBody["model"]
			if !hasModel {
				next(ctx)
				return
			}
			modelStr, ok := modelValue.(string)
			if !ok || modelStr == "" {
				next(ctx)
				return
			}
			// Check if model already has provider prefix (contains "/")
			if strings.Contains(modelStr, "/") {
				next(ctx)
				return
			}
			opCtx := context.Background()
			virtualKey, err := config.ConfigStore.GetVirtualKeyByValue(opCtx, virtualKeyValue)
			if err != nil {
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get virtual key: %v", err), logger)
				return
			}
			if virtualKey == nil {
				SendError(ctx, fasthttp.StatusBadRequest, "Invalid virtual key", logger)
				return
			}
			if !virtualKey.IsActive {
				next(ctx)
				return
			}
			// Get provider configs for this virtual key
			providerConfigs, err := config.ConfigStore.GetVirtualKeyProviderConfigs(opCtx, virtualKey.ID)
			if err != nil {
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get virtual key provider configs: %v", err), logger)
				return
			}
			if len(providerConfigs) == 0 {
				// No provider configs, continue without modification
				next(ctx)
				return
			}
			allowedProviderConfigs := make([]configstore.TableVirtualKeyProviderConfig, 0)
			for _, config := range providerConfigs {
				if len(config.AllowedModels) == 0 || slices.Contains(config.AllowedModels, modelStr) {
					allowedProviderConfigs = append(allowedProviderConfigs, config)
				}
			}
			if len(allowedProviderConfigs) == 0 {
				// No allowed provider configs, continue without modification
				next(ctx)
				return
			}

			// Weighted random selection from allowed providers for the main model
			totalWeight := 0.0
			for _, config := range allowedProviderConfigs {
				totalWeight += config.Weight
			}
			// Generate random number between 0 and totalWeight
			randomValue := rand.Float64() * totalWeight
			// Select provider based on weighted random selection
			var selectedProvider schemas.ModelProvider
			currentWeight := 0.0
			for _, config := range allowedProviderConfigs {
				currentWeight += config.Weight
				if randomValue <= currentWeight {
					selectedProvider = schemas.ModelProvider(config.Provider)
					break
				}
			}

			// Update the model field in the request body
			requestBody["model"] = string(selectedProvider) + "/" + modelStr

			// Check if fallbacks field is already present
			_, hasFallbacks := requestBody["fallbacks"]
			if !hasFallbacks && len(allowedProviderConfigs) > 1 {
				// Sort allowed provider configs by weight (descending)
				sort.Slice(allowedProviderConfigs, func(i, j int) bool {
					return allowedProviderConfigs[i].Weight > allowedProviderConfigs[j].Weight
				})

				// Filter out the selected provider and create fallbacks array
				fallbacks := make([]string, 0, len(allowedProviderConfigs)-1)
				for _, config := range allowedProviderConfigs {
					if config.Provider != string(selectedProvider) {
						fallbacks = append(fallbacks, string(schemas.ModelProvider(config.Provider))+"/"+modelStr)
					}
				}

				// Add fallbacks to request body
				requestBody["fallbacks"] = fallbacks
			}

			// Marshal the updated request body back to JSON
			updatedBody, err := json.Marshal(requestBody)
			if err != nil {
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to marshal updated request body: %v", err), logger)
				return
			}
			// Replace the request body with the updated one
			ctx.Request.SetBody(updatedBody)
			next(ctx)
		}
	}
}

// ChainMiddlewares chains multiple middlewares together
// Middlewares are applied in order: the first middleware wraps the second, etc.
// This allows earlier middlewares to short-circuit by not calling next(ctx)
func ChainMiddlewares(handler fasthttp.RequestHandler, middlewares ...BifrostHTTPMiddleware) fasthttp.RequestHandler {
	// If no middlewares, return the original handler
	if len(middlewares) == 0 {
		return handler
	}
	// Build the chain from right to left (last middleware wraps the handler)
	// This ensures execution order is left to right (first middleware executes first)
	chained := handler
	for i := len(middlewares) - 1; i >= 0; i-- {
		chained = middlewares[i](chained)
	}
	return chained
}
