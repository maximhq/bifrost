package integrations

import (
	"context"
	"errors"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/cohere"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// hydrateCohereRequestFromLargePayloadMetadata populates model + stream from
// LargePayloadMetadata when body parsing is skipped under large payload mode.
func hydrateCohereRequestFromLargePayloadMetadata(bifrostCtx *schemas.BifrostContext, req interface{}) {
	if bifrostCtx == nil {
		return
	}
	isLargePayload, _ := bifrostCtx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool)
	if !isLargePayload {
		return
	}
	metadata, _ := bifrostCtx.Value(schemas.BifrostContextKeyLargePayloadMetadata).(*schemas.LargePayloadMetadata)
	if metadata == nil {
		return
	}

	switch r := req.(type) {
	case *cohere.CohereChatRequest:
		if r.Model == "" {
			r.Model = metadata.Model
		}
		if metadata.StreamRequested != nil && r.Stream == nil {
			r.Stream = schemas.Ptr(*metadata.StreamRequested)
		}
	case *cohere.CohereEmbeddingRequest:
		if r.Model == "" {
			r.Model = metadata.Model
		}
	case *cohere.CohereRerankRequest:
		if r.Model == "" {
			r.Model = metadata.Model
		}
	case *cohere.CohereCountTokensRequest:
		if r.Model == "" {
			r.Model = metadata.Model
		}
	}
}

// cohereLargePayloadPreHook populates model + stream from LargePayloadMetadata
// when body parsing is skipped under large payload mode.
func cohereLargePayloadPreHook(_ *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	hydrateCohereRequestFromLargePayloadMetadata(bifrostCtx, req)
	return nil
}

// CohereRouter holds route registrations for Cohere endpoints.
// It supports Cohere's v2 chat, embeddings, and rerank APIs.
type CohereRouter struct {
	*GenericRouter
}

// NewCohereRouter creates a new CohereRouter with the given bifrost client.
func NewCohereRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *CohereRouter {
	return &CohereRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, CreateCohereRouteConfigs("/cohere"), logger),
	}
}

// CreateCohereRouteConfigs creates route configurations for Cohere API endpoints.
func CreateCohereRouteConfigs(pathPrefix string) []RouteConfig {
	var routes []RouteConfig

	// Chat completions endpoint (v2/chat)
	routes = append(routes, RouteConfig{
		Type:        RouteConfigTypeCohere,
		Path:        pathPrefix + "/v2/chat",
		Method:      "POST",
		PreCallback: cohereLargePayloadPreHook,
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.ChatCompletionRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
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
		Type:        RouteConfigTypeCohere,
		Path:        pathPrefix + "/v2/embed",
		Method:      "POST",
		PreCallback: cohereLargePayloadPreHook,
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.EmbeddingRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
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

	// Rerank endpoint (v2/rerank)
	routes = append(routes, RouteConfig{
		Type:        RouteConfigTypeCohere,
		Path:        pathPrefix + "/v2/rerank",
		Method:      "POST",
		PreCallback: cohereLargePayloadPreHook,
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.RerankRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &cohere.CohereRerankRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if cohereReq, ok := req.(*cohere.CohereRerankRequest); ok {
				return &schemas.BifrostRequest{
					RerankRequest: cohereReq.ToBifrostRerankRequest(ctx),
				}, nil
			}
			return nil, errors.New("invalid rerank request type")
		},
		RerankResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostRerankResponse) (interface{}, error) {
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
		Type:        RouteConfigTypeCohere,
		Path:        pathPrefix + "/v1/tokenize",
		Method:      "POST",
		PreCallback: cohereLargePayloadPreHook,
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.CountTokensRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
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
