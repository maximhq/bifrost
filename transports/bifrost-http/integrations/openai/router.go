package openai

import (
	"errors"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
)

// OpenAIRouter holds route registrations for OpenAI endpoints.
// It supports standard chat completions and image-enabled vision capabilities.
type OpenAIRouter struct {
	*integrations.GenericRouter
}

// NewOpenAIRouter creates a new OpenAIRouter with the given bifrost client.
func NewOpenAIRouter(client *bifrost.Bifrost) *OpenAIRouter {
	routes := []integrations.RouteConfig{
		{
			Path:   "/openai/chat/completions",
			Method: "POST",
			GetRequestTypeInstance: func() interface{} {
				return &OpenAIChatRequest{}
			},
			RequestConverter: func(req interface{}) (*schemas.BifrostRequest, error) {
				if openaiReq, ok := req.(*OpenAIChatRequest); ok {
					return openaiReq.ConvertToBifrostRequest(), nil
				}
				return nil, errors.New("invalid request type")
			},
			ResponseConverter: func(resp *schemas.BifrostResponse) (interface{}, error) {
				return DeriveOpenAIFromBifrostResponse(resp), nil
			},
		},
	}

	return &OpenAIRouter{
		GenericRouter: integrations.NewGenericRouter(client, routes),
	}
}
