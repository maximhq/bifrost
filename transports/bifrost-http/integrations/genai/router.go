package genai

import (
	"errors"
	"fmt"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
	"github.com/valyala/fasthttp"
)

// GenAIRouter holds route registrations for genai endpoints.
type GenAIRouter struct {
	*integrations.GenericRouter
}

// NewGenAIRouter creates a new GenAIRouter with the given bifrost client.
func NewGenAIRouter(client *bifrost.Bifrost) *GenAIRouter {
	routes := []integrations.RouteConfig{
		{
			Path:   "/genai/v1beta/models/{model}",
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &GeminiChatRequest{}
			},
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				if geminiReq, ok := req.(*GeminiChatRequest); ok {
					return geminiReq.ConvertToBifrostRequest(), nil
				}
				return nil, errors.New("invalid request type")
			},
			ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
				return DeriveGenAIFromBifrostResponse(resp), nil
			},
			ErrorConverter: func(err *schemas.BifrostError) interface{} {
				return DeriveGeminiErrorFromBifrostError(err)
			},
			StreamConfig: &integrations.StreamConfig{
				ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
					return DeriveGeminiStreamFromBifrostResponse(resp), nil
				},
				ErrorConverter: func(err *schemas.BifrostError) interface{} {
					return DeriveGeminiStreamFromBifrostError(err)
				},
			},
			PreCallback: extractAndSetModelFromURL,
		},
	}

	return &GenAIRouter{
		GenericRouter: integrations.NewGenericRouter(client, routes),
	}
}

// extractAndSetModelFromURL extracts model from URL and sets it in the request
func extractAndSetModelFromURL(ctx *fasthttp.RequestCtx, req interface{}) error {
	model := ctx.UserValue("model")
	if model == nil {
		return fmt.Errorf("model parameter is required")
	}

	modelStr := model.(string)

	// Check if this is a streaming request
	isStreaming := strings.HasSuffix(modelStr, ":streamGenerateContent")

	// Remove Google GenAI API endpoint suffixes if present
	for _, sfx := range []string{
		":streamGenerateContent",
		":generateContent",
		":countTokens",
	} {
		modelStr = strings.TrimSuffix(modelStr, sfx)
	}

	// Remove trailing colon if present
	if len(modelStr) > 0 && modelStr[len(modelStr)-1] == ':' {
		modelStr = modelStr[:len(modelStr)-1]
	}

	// Add google/ prefix for Bifrost if not already present
	processedModel := modelStr
	if !strings.HasPrefix(modelStr, "google/") {
		processedModel = "google/" + modelStr
	}

	// Set the model and streaming flag in the request
	if geminiReq, ok := req.(*GeminiChatRequest); ok {
		geminiReq.Model = processedModel
		geminiReq.Stream = isStreaming
		return nil
	}

	return fmt.Errorf("invalid request type for GenAI")
}
