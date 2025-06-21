package genai

import (
	"errors"
	"fmt"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/providers"
	"github.com/valyala/fasthttp"
)

// GenAIRouter holds route registrations for genai endpoints.
type GenAIRouter struct {
	*providers.ProviderRouter
}

// NewGenAIRouter creates a new GenAIRouter with the given bifrost client.
func NewGenAIRouter(client *bifrost.Bifrost) *GenAIRouter {
	routes := []providers.RouteConfig{
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
			PreCallback: extractAndSetModelFromURL,
		},
	}

	return &GenAIRouter{
		ProviderRouter: providers.NewProviderRouter(client, routes),
	}
}

// extractAndSetModelFromURL extracts model from URL and sets it in the request
func extractAndSetModelFromURL(ctx *fasthttp.RequestCtx, req interface{}) error {
	model := ctx.UserValue("model")
	if model == nil {
		return fmt.Errorf("model parameter is required")
	}

	modelStr := model.(string)
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

	// Set the model in the request
	if geminiReq, ok := req.(*GeminiChatRequest); ok {
		geminiReq.Model = processedModel
		return nil
	}

	return fmt.Errorf("invalid request type for GenAI")
}
