package integrations

import (
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/bedrock"
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
	routes = append(routes, withLangChainBedrockEmbeddingCompatibility(CreateBedrockRouteConfigs("/langchain", handlerStore))...)

	// Add Cohere routes to LangChain for Cohere API compatibility
	routes = append(routes, CreateCohereRouteConfigs("/langchain")...)

	return &LangChainRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, routes, nil, logger),
	}
}

// withLangChainBedrockEmbeddingCompatibility decorates the native Bedrock
// embedding converter with the singular field expected by LangChain's
// BedrockEmbeddings parser. The underlying Bedrock routes remain AWS-compatible.
func withLangChainBedrockEmbeddingCompatibility(routes []RouteConfig) []RouteConfig {
	for i := range routes {
		converter := routes[i].EmbeddingResponseConverter
		if converter == nil {
			continue
		}
		routes[i].EmbeddingResponseConverter = func(ctx *schemas.BifrostContext, resp *schemas.BifrostEmbeddingResponse) (interface{}, error) {
			converted, err := converter(ctx, resp)
			if err != nil {
				return converted, err
			}
			return addLangChainCohereEmbeddingAlias(ctx, resp, converted), nil
		}
	}
	return routes
}

func addLangChainCohereEmbeddingAlias(ctx *schemas.BifrostContext, bifrostResponse *schemas.BifrostEmbeddingResponse, response interface{}) interface{} {
	switch value := response.(type) {
	case *bedrock.BedrockInvokeCohereEmbeddingResp:
		if len(value.Embeddings) == 0 {
			return response
		}
		// This is a serialization-only compatibility envelope. Embedding the native
		// response keeps the AWS response schema owned by core/providers/bedrock.
		return &struct {
			Embedding []float32 `json:"embedding"`
			*bedrock.BedrockInvokeCohereEmbeddingResp
		}{
			Embedding:                        value.Embeddings[0],
			BedrockInvokeCohereEmbeddingResp: value,
		}
	case map[string]interface{}:
		// Raw typed responses lose their concrete Go type, so confirm the model
		// family before modifying the envelope. This prevents an unrelated Bedrock
		// model that happens to return an "embeddings" field from being changed.
		if !isCohereEmbeddingResponse(ctx, bifrostResponse) {
			return response
		}
		first := firstCohereEmbedding(value["embeddings"])
		if first == nil {
			return response
		}
		withAlias := make(map[string]interface{}, len(value)+1)
		for key, field := range value {
			withAlias[key] = field
		}
		withAlias["embedding"] = first
		return withAlias
	default:
		return response
	}
}

func isCohereEmbeddingResponse(ctx *schemas.BifrostContext, response *schemas.BifrostEmbeddingResponse) bool {
	if response == nil {
		return false
	}
	model := response.Model
	if model == "" {
		if response.ExtraFields.ResolvedModelUsed != "" {
			model = response.ExtraFields.ResolvedModelUsed
		} else {
			model = response.ExtraFields.OriginalModelRequested
		}
	}
	return schemas.IsCohereModelFamily(ctx, model)
}

func firstCohereEmbedding(embeddings interface{}) interface{} {
	switch value := embeddings.(type) {
	case []interface{}:
		if len(value) > 0 {
			return value[0]
		}
	case map[string]interface{}:
		for _, encodingType := range []string{"float", "int8", "uint8", "binary", "ubinary"} {
			vectors, ok := value[encodingType].([]interface{})
			if ok && len(vectors) > 0 {
				return vectors[0]
			}
		}
	}
	return nil
}
