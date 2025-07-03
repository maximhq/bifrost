// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains integration management handlers for AI provider integrations.
package handlers

import (
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations/anthropic"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations/genai"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations/litellm"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations/openai"
)

// IntegrationHandler manages HTTP requests for AI provider integrations
type IntegrationHandler struct {
	extensions []integrations.ExtensionRouter
}

// NewIntegrationHandler creates a new integration handler instance
func NewIntegrationHandler(client *bifrost.Bifrost) *IntegrationHandler {
	// Initialize all available integration routers
	extensions := []integrations.ExtensionRouter{
		genai.NewGenAIRouter(client),
		openai.NewOpenAIRouter(client),
		anthropic.NewAnthropicRouter(client),
		litellm.NewLiteLLMRouter(client),
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
