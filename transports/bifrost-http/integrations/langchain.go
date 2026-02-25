package integrations

import (
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// LangChainRouter holds route registrations for LangChain endpoints.
// It supports standard chat completions and image-enabled vision capabilities.
// LangChain is fully OpenAI-compatible, so we reuse OpenAI types
// with aliases for clarity and minimal LangChain-specific extensions
type LangChainRouter struct {
	*GenericRouter
}

// NewLangChainRouter creates a new LangChainRouter with the given bifrost client.
func NewLangChainRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *LangChainRouter {
	routes := []RouteConfig{}

	// Add OpenAI routes to LangChain for OpenAI API compatibility
	routes = append(routes, CreateOpenAIRouteConfigs("/langchain", handlerStore)...)

	// Add Anthropic routes to LangChain for Anthropic API compatibility
	routes = append(routes, CreateAnthropicRouteConfigs("/langchain", logger)...)

	// Add Anthropic count tokens route for LangChain to ensure token counting uses the dedicated endpoint
	routes = append(routes, CreateAnthropicCountTokensRouteConfigs("/langchain", handlerStore)...)

	// Add GenAI routes to LangChain for Vertex AI compatibility
	routes = append(routes, CreateGenAIRouteConfigs("/langchain")...)

	// Add Bedrock routes to LangChain for AWS Bedrock API compatibility
	routes = append(routes, CreateBedrockRouteConfigs("/langchain", handlerStore)...)

	// Add Cohere routes to LangChain for Cohere API compatibility
	routes = append(routes, CreateCohereRouteConfigs("/langchain")...)

	return &LangChainRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, routes, nil, logger),
	}
}
