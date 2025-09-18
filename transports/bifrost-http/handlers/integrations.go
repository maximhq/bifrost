// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains integration management handlers for AI provider integrations.
package handlers

import (
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations/genai"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations/langchain"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations/litellm"
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
		integrations.NewOpenAIRouter(client, handlerStore),
		integrations.NewAnthropicRouter(client, handlerStore),
		genai.NewGenAIRouter(client, handlerStore),
		litellm.NewLiteLLMRouter(client, handlerStore),
		langchain.NewLangChainRouter(client, handlerStore),
	}

	return &IntegrationHandler{
		extensions: extensions,
	}
}

// RegisterRoutes registers all integration routes for AI provider compatibility endpoints
func (h *IntegrationHandler) RegisterRoutes(r *router.Router) {
	// Register routes for each integration extension
	for _, extension := range h.extensions {
		extension.RegisterRoutes(r)
	}
}
