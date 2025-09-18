package langchain

import (
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations/genai"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// LangChainRouter holds route registrations for LangChain endpoints.
// It supports standard chat completions and image-enabled vision capabilities.
// LangChain is fully OpenAI-compatible, so we reuse OpenAI types
// with aliases for clarity and minimal LangChain-specific extensions
type LangChainRouter struct {
	*integrations.GenericRouter
}

// NewLangChainRouter creates a new LangChainRouter with the given bifrost client.
func NewLangChainRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore) *LangChainRouter {
	routes := []integrations.RouteConfig{}

	// Add OpenAI routes to LangChain for OpenAI API compatibility
	routes = append(routes, integrations.CreateOpenAIRouteConfigs("/langchain", handlerStore)...)

	// Add Anthropic routes to LangChain for Anthropic API compatibility
	routes = append(routes, integrations.CreateAnthropicRouteConfigs("/langchain")...)

	// Add GenAI routes to LangChain for Vertex AI compatibility
	routes = append(routes, genai.CreateGenAIRouteConfigs("/langchain")...)

	return &LangChainRouter{
		GenericRouter: integrations.NewGenericRouter(client, handlerStore, routes),
	}
}
