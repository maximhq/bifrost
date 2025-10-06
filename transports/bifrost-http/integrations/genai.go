package integrations

import (
	"errors"
	"fmt"
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
		Path:   pathPrefix + "/v1beta/models/{model:*}",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &gemini.GeminiGenerationRequest{}
		},
		RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
			if geminiReq, ok := req.(*gemini.GeminiGenerationRequest); ok {
				return &schemas.BifrostRequest{
					ChatRequest: geminiReq.ToBifrostRequest(),
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
			return gemini.ToGeminiGenerationResponse(resp), nil
		},
		ErrorConverter: func(err *schemas.BifrostError) interface{} {
			return gemini.ToGeminiError(err)
		},
		StreamConfig: &StreamConfig{
			ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
				return gemini.ToGeminiGenerationResponse(resp), nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return gemini.ToGeminiError(err)
			},
		},
		PreCallback: extractAndSetModelFromURL,
	})

	return routes
}

// NewGenAIRouter creates a new GenAIRouter with the given bifrost client.
func NewGenAIRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore) *GenAIRouter {
	return &GenAIRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, CreateGenAIRouteConfigs("/genai")),
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
