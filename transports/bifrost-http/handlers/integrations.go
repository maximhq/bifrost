// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains integration management handlers for AI provider integrations.
package handlers

import (
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// IntegrationHandler manages HTTP requests for AI provider integrations
type IntegrationHandler struct {
	extensions []integrations.ExtensionRouter
}

// NewIntegrationHandler creates a new integration handler instance
func NewIntegrationHandler(client *bifrost.Bifrost, handlerStore lib.HandlerStore) *IntegrationHandler {
	// Initialize all available integration routers
	extensions := []integrations.ExtensionRouter{
		integrations.NewOpenAIRouter(client, handlerStore, logger),
		integrations.NewAnthropicRouter(client, handlerStore, logger),
		integrations.NewGenAIRouter(client, handlerStore, logger),
		integrations.NewLiteLLMRouter(client, handlerStore, logger),
		integrations.NewLangChainRouter(client, handlerStore, logger),
		integrations.NewPydanticAIRouter(client, handlerStore, logger),
		integrations.NewBedrockRouter(client, handlerStore, logger),
	}

	return &IntegrationHandler{
		extensions: extensions,
	}
}

// RegisterRoutes registers all integration routes for AI provider compatibility endpoints
func (h *IntegrationHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	// Register routes for each integration extension
	for _, extension := range h.extensions {
		extension.RegisterRoutes(r, middlewares...)
	}
}

// SetLargePayloadHook sets the large payload detection hook on all integration routers
// that support it. This is used by enterprise to inject large payload optimization.
func (h *IntegrationHandler) SetLargePayloadHook(hook integrations.LargePayloadHook) {
	for _, extension := range h.extensions {
		if setter, ok := extension.(interface {
			SetLargePayloadHook(integrations.LargePayloadHook)
		}); ok {
			setter.SetLargePayloadHook(hook)
		}
	}
}

// SetLargeResponseHook sets the large response scanning hook on all integration routers
// that support it. Enterprise uses this to inject Phase B usage extraction into the
// response stream without embedding scanning logic in the OSS router.
func (h *IntegrationHandler) SetLargeResponseHook(hook integrations.LargeResponseHook) {
	for _, extension := range h.extensions {
		if setter, ok := extension.(interface {
			SetLargeResponseHook(integrations.LargeResponseHook)
		}); ok {
			setter.SetLargeResponseHook(hook)
		}
	}
}
