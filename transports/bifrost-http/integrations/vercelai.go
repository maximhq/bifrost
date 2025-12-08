package integrations

import (
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// VercelAIRouter holds route registrations for Vercel AI SDK endpoints.
// Vercel AI SDK is fully OpenAI-compatible, so we reuse OpenAI types
// with aliases for clarity and minimal Vercel AI SDK-specific extensions
type VercelAIRouter struct {
	*GenericRouter
}

// NewVercelAIRouter creates a new VercelAIRouter with the given bifrost client.
func NewVercelAIRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *VercelAIRouter {
	routes := []RouteConfig{}

	// Add OpenAI routes to Vercel AI SDK for OpenAI API compatibility
	routes = append(routes, CreateOpenAIRouteConfigs("/vercelai", handlerStore)...)

	// Add Anthropic routes to Vercel AI SDK for Anthropic API compatibility
	routes = append(routes, CreateAnthropicRouteConfigs("/vercelai")...)

	// Add GenAI routes to Vercel AI SDK for Google Gemini API compatibility
	routes = append(routes, CreateGenAIRouteConfigs("/vercelai")...)

	return &VercelAIRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, routes, logger),
	}
}
