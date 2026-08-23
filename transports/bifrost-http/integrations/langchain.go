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
			return addLangChainCohereEmbeddingAlias(converted), nil
		}
	}
	return routes
}

// langChainCohereEmbedding is the Bedrock Cohere invoke envelope plus the singular
// "embedding" field LangChain's BedrockEmbeddings reads: that class parses
// body.embedding for every model, while the AWS envelope only carries the plural.
type langChainCohereEmbedding struct {
	Embedding    []float32   `json:"embedding"`
	Embeddings   [][]float32 `json:"embeddings"`
	ResponseType string      `json:"response_type"`
}

// langChainCohereTypedEmbedding is the same alias over the typed encoding envelope.
type langChainCohereTypedEmbedding struct {
	Embedding    []float32                             `json:"embedding"`
	Embeddings   bedrock.BedrockCohereEmbeddingsByType `json:"embeddings"`
	ResponseType string                                `json:"response_type"`
}

// addLangChainCohereEmbeddingAlias re-emits a Cohere embedding envelope with the
// singular alias. Only a float vector is aliased, since LangChain expects number[].
// The native fields are carried through unchanged.
func addLangChainCohereEmbeddingAlias(response interface{}) interface{} {
	switch value := response.(type) {
	case *bedrock.BedrockInvokeCohereEmbeddingResp:
		if len(value.Embeddings) == 0 {
			return response
		}
		return &langChainCohereEmbedding{
			Embedding:    value.Embeddings[0],
			Embeddings:   value.Embeddings,
			ResponseType: value.ResponseType,
		}
	case *bedrock.BedrockInvokeCohereTypedEmbeddingResp:
		if len(value.Embeddings.Float) == 0 {
			return response
		}
		return &langChainCohereTypedEmbedding{
			Embedding:    value.Embeddings.Float[0],
			Embeddings:   value.Embeddings,
			ResponseType: value.ResponseType,
		}
	default:
		return response
	}
}
