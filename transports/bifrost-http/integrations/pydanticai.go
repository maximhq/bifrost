package integrations

import (
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// PydanticAIRouter holds route registrations for Pydantic AI endpoints.
// It supports standard chat completions, tool calling, streaming, and multi-provider capabilities.
// Pydantic AI uses standard provider SDKs (OpenAI, Anthropic, Google GenAI), so we reuse
// existing route configurations with aliases for clarity and Pydantic AI-specific extensions.
type PydanticAIRouter struct {
	*GenericRouter
}

// NewPydanticAIRouter creates a new PydanticAIRouter with the given bifrost client.
func NewPydanticAIRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *PydanticAIRouter {
	routes := []RouteConfig{}
	// Add OpenAI routes to Pydantic AI for OpenAI API compatibility
	// Supports: chat completions, embeddings, speech, transcriptions, responses
	routes = append(routes, CreateOpenAIRouteConfigs("/pydanticai", handlerStore)...)
	// Add Anthropic routes to Pydantic AI for Anthropic API compatibility
	// Supports: messages API (Claude models)
	routes = append(routes, CreateAnthropicRouteConfigs("/pydanticai", logger)...)
	// Add GenAI routes to Pydantic AI for Google Gemini API compatibility
	// Supports: generateContent, streamGenerateContent, embedContent
	routes = append(routes, CreateGenAIRouteConfigs("/pydanticai")...)
	// Add Cohere routes to Pydantic AI for Cohere API compatibility
	// Supports: v2/chat (chat completions with streaming), v2/embed (embeddings)
	routes = append(routes, CreateCohereRouteConfigs("/pydanticai")...)
	// Add Bedrock routes to Pydantic AI for AWS Bedrock API compatibility
	// Supports: converse, converse-stream, invoke, invoke-with-response-stream
	routes = append(routes, CreateBedrockRouteConfigs("/pydanticai", handlerStore)...)
	return &PydanticAIRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, routes, logger),
	}
}
