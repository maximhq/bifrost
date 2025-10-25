package integrations

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/providers/gemini"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// GenAIRouter holds route registrations for genai endpoints.
type GenAIRouter struct {
	*GenericRouter
}

// CreateGenAIRouteConfigs creates a route configurations for GenAI endpoints.
func CreateGenAIRouteConfigs(pathPrefix string) []RouteConfig {
	var routes []RouteConfig

	// Chat completions endpoint
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/models/{model:*}",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &gemini.GeminiGenerationRequest{}
		},
		RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
			if geminiReq, ok := req.(*gemini.GeminiGenerationRequest); ok {
				if geminiReq.IsEmbedding {
					return &schemas.BifrostRequest{
						EmbeddingRequest: geminiReq.ToBifrostEmbeddingRequest(),
					}, nil
				} else {
					return &schemas.BifrostRequest{
						ChatRequest: geminiReq.ToBifrostChatRequest(),
					}, nil
				}
			}
			return nil, errors.New("invalid request type")
		},
		EmbeddingResponseConverter: func(resp *schemas.BifrostEmbeddingResponse) (interface{}, error) {
			return gemini.ToGeminiEmbeddingResponse(resp), nil
		},
		ChatResponseConverter: func(resp *schemas.BifrostChatResponse) (interface{}, error) {
			return gemini.ToGeminiChatResponse(resp), nil
		},
		ErrorConverter: func(err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		StreamConfig: &StreamConfig{
			ChatStreamResponseConverter: func(resp *schemas.BifrostChatResponse) (interface{}, error) {
				return gemini.ToGeminiChatResponse(resp), nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return gemini.ToGeminiError(err)
			},
		},
		PreCallback: extractAndSetModelFromURL,
	})

	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeGenAI,
		Path:   pathPrefix + "/v1beta/models",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
			return &schemas.BifrostListModelsRequest{}
		},
		RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
			if listModelsReq, ok := req.(*schemas.BifrostListModelsRequest); ok {
				return &schemas.BifrostRequest{
					ListModelsRequest: listModelsReq,
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		ListModelsResponseConverter: func(resp *schemas.BifrostListModelsResponse) (interface{}, error) {
			return gemini.ToGeminiListModelsResponse(resp), nil
		},
		ErrorConverter: func(err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		PreCallback: extractGeminiListModelsParams,
	})

	return routes
}

// NewGenAIRouter creates a new GenAIRouter with the given bifrost client.
func NewGenAIRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *GenAIRouter {
	return &GenAIRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, CreateGenAIRouteConfigs("/genai"), logger),
	}
}

var embeddingPaths = []string{
	":embedContent",
	":batchEmbedContents",
	":predict",
}

// extractAndSetModelFromURL extracts model from URL and sets it in the request
func extractAndSetModelFromURL(ctx *fasthttp.RequestCtx, req interface{}) error {
	model := ctx.UserValue("model")
	if model == nil {
		return fmt.Errorf("model parameter is required")
	}

	modelStr := model.(string)

	// Check if this is an embedding request
	isEmbedding := false
	for _, path := range embeddingPaths {
		if strings.HasSuffix(modelStr, path) {
			isEmbedding = true
			break
		}
	}

	// Check if this is a streaming request
	isStreaming := strings.HasSuffix(modelStr, ":streamGenerateContent")

	// Remove Google GenAI API endpoint suffixes if present
	for _, sfx := range []string{
		":streamGenerateContent",
		":generateContent",
		":countTokens",
		":embedContent",
		":batchEmbedContents",
		":predict",
	} {
		modelStr = strings.TrimSuffix(modelStr, sfx)
	}

	// Remove trailing colon if present
	if len(modelStr) > 0 && modelStr[len(modelStr)-1] == ':' {
		modelStr = modelStr[:len(modelStr)-1]
	}

	// Set the model and flags in the request
	if geminiReq, ok := req.(*gemini.GeminiGenerationRequest); ok {
		geminiReq.Model = modelStr
		geminiReq.Stream = isStreaming
		geminiReq.IsEmbedding = isEmbedding
		return nil
	}

	return fmt.Errorf("invalid request type for GenAI")
}

// extractGeminiListModelsParams extracts query parameters for list models request
func extractGeminiListModelsParams(ctx *fasthttp.RequestCtx, req interface{}) error {
	if listModelsReq, ok := req.(*schemas.BifrostListModelsRequest); ok {
		// Set provider to Gemini
		listModelsReq.Provider = schemas.Gemini

		// Extract pageSize from query parameters (Gemini uses pageSize instead of limit)
		if pageSizeStr := string(ctx.QueryArgs().Peek("pageSize")); pageSizeStr != "" {
			if pageSize, err := strconv.Atoi(pageSizeStr); err == nil {
				listModelsReq.PageSize = pageSize
			}
		}

		// Extract pageToken from query parameters
		if pageToken := string(ctx.QueryArgs().Peek("pageToken")); pageToken != "" {
			listModelsReq.PageToken = pageToken
		}

		return nil
	}
	return errors.New("invalid request type for Gemini list models")
}
