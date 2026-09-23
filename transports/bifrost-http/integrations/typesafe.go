package integrations

import (
	"context"
	"errors"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// TypesafeRouter holds route registrations for Typesafe endpoints: the native
// System One evaluation endpoint and model listing.
type TypesafeRouter struct {
	*GenericRouter
}

// NewTypesafeRouter creates a new TypesafeRouter with the given bifrost client.
func NewTypesafeRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, accessResolver AccessResolver, logger schemas.Logger) *TypesafeRouter {
	return &TypesafeRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, accessResolver, CreateTypesafeRouteConfigs("/typesafe"), nil, logger),
	}
}

// CreateTypesafeRouteConfigs creates route configurations for Typesafe API endpoints.
func CreateTypesafeRouteConfigs(pathPrefix string) []RouteConfig {
	var routes []RouteConfig

	// Decision endpoint (v1/systemone)
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeTypesafe,
		Path:   pathPrefix + "/v1/systemone",
		Method: "POST",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.DecisionRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &typesafe.TypesafeDecisionRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if typesafeReq, ok := req.(*typesafe.TypesafeDecisionRequest); ok {
				decisionReq, err := typesafeReq.ToBifrostDecisionRequest(ctx)
				if err != nil {
					return nil, err
				}
				return &schemas.BifrostRequest{
					DecisionRequest: decisionReq,
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		DecisionResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostDecisionResponse) (interface{}, error) {
			if resp.ExtraFields.Provider == schemas.Typesafe && resp.ExtraFields.RawResponse != nil {
				return resp.ExtraFields.RawResponse, nil
			}
			return typesafe.ToTypesafeNativeDecisionResponse(resp)
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return typesafe.ToTypesafeNativeError(err)
		},
	})

	// Models endpoint. Typesafe documents no upstream listing API; the response
	// is synthesized from the provider's static catalog in native shape.
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeTypesafe,
		Path:   pathPrefix + "/v1/models",
		Method: "GET",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.ListModelsRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &schemas.BifrostListModelsRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if listModelsReq, ok := req.(*schemas.BifrostListModelsRequest); ok {
				if listModelsReq.Provider == "" {
					listModelsReq.Provider = schemas.Typesafe
				}
				return &schemas.BifrostRequest{
					ListModelsRequest: listModelsReq,
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		ListModelsResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostListModelsResponse) (interface{}, error) {
			return typesafe.ToTypesafeNativeListModelsResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return typesafe.ToTypesafeNativeError(err)
		},
	})

	return routes
}
