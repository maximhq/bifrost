package integrations

import (
	"errors"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/cohere"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// CohereRouter holds route registrations for Cohere endpoints.
// It supports Cohere's v2 chat API and embeddings API.
type CohereRouter struct {
	*GenericRouter
}

// NewCohereRouter creates a new CohereRouter with the given bifrost client.
func NewCohereRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *CohereRouter {
	return &CohereRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, CreateCohereRouteConfigs("/cohere"), logger),
	}
}

// CreateCohereRouteConfigs creates route configurations for Cohere v2 API endpoints.
func CreateCohereRouteConfigs(pathPrefix string) []RouteConfig {
	var routes []RouteConfig

	// Chat completions endpoint (v2/chat)
	routes = append(routes, RouteConfig{
		Path:   pathPrefix + "/v2/chat",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &cohere.CohereChatRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if cohereReq, ok := req.(*cohere.CohereChatRequest); ok {
				return &schemas.BifrostRequest{
					ChatRequest: cohereReq.ToBifrostChatRequest(ctx),
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		ChatResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostChatResponse) (interface{}, error) {
			if resp.ExtraFields.Provider == schemas.Cohere {
				if resp.ExtraFields.RawResponse != nil {
					return resp.ExtraFields.RawResponse, nil
				}
			}
			return resp, nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return err
		},
		StreamConfig: &StreamConfig{
			ChatStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostChatResponse) (string, interface{}, error) {
				if resp.ExtraFields.Provider == schemas.Cohere {
					if resp.ExtraFields.RawResponse != nil {
						return "", resp.ExtraFields.RawResponse, nil
					}
				}
				return "", resp, nil
			},
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return err
			},
		},
	})

	// Embeddings endpoint (v2/embed)
	routes = append(routes, RouteConfig{
		Path:   pathPrefix + "/v2/embed",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &cohere.CohereEmbeddingRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if cohereReq, ok := req.(*cohere.CohereEmbeddingRequest); ok {
				return &schemas.BifrostRequest{
					EmbeddingRequest: cohereReq.ToBifrostEmbeddingRequest(ctx),
				}, nil
			}
			return nil, errors.New("invalid embedding request type")
		},
		EmbeddingResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostEmbeddingResponse) (interface{}, error) {
			if resp.ExtraFields.Provider == schemas.Cohere {
				if resp.ExtraFields.RawResponse != nil {
					return resp.ExtraFields.RawResponse, nil
				}
			}
			return resp, nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return err
		},
	})

	// Tokenize endpoint (v1/tokenize)
	routes = append(routes, RouteConfig{
		Path:   pathPrefix + "/v1/tokenize",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &cohere.CohereCountTokensRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if cohereReq, ok := req.(*cohere.CohereCountTokensRequest); ok {
				return &schemas.BifrostRequest{
					CountTokensRequest: cohereReq.ToBifrostResponsesRequest(ctx),
				}, nil
			}
			return nil, errors.New("invalid count tokens request type")
		},
		CountTokensResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostCountTokensResponse) (interface{}, error) {
			if resp.ExtraFields.Provider == schemas.Cohere {
				if resp.ExtraFields.RawResponse != nil {
					return resp.ExtraFields.RawResponse, nil
				}
			}
			return resp, nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return err
		},
	})

	return routes
}
