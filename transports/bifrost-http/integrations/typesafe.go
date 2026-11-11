package integrations

import (
	"context"
	"errors"
	"fmt"
	"sort"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
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
		RequestParser: parseTypesafeSystemOneRequest,
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if typesafeReq, ok := req.(*typesafe.TypesafeDecisionRequest); ok {
				decisionReq, err := typesafeReq.ToBifrostDecisionRequest(ctx)
				if err != nil {
					return nil, err
				}
				// The native surface is the wire shape: extensions the SDK
				// forwarded always reach the provider, no header needed.
				if len(decisionReq.ExtraParams) > 0 && ctx != nil {
					ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
				}
				return &schemas.BifrostRequest{
					DecisionRequest: decisionReq,
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		DecisionResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostDecisionResponse) (interface{}, error) {
			// Custom providers report their own name, so match on the base
			// provider type that served the attempt.
			if schemas.ResolveBaseProvider(ctx, resp.ExtraFields.Provider) == schemas.Typesafe {
				if resp.ExtraFields.RawResponse != nil {
					return resp.ExtraFields.RawResponse, nil
				}
				if len(resp.NativeResponse) > 0 {
					return resp.NativeResponse, nil
				}
			}
			return typesafe.ToTypesafeNativeDecisionResponse(resp)
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return typesafe.ToTypesafeNativeErrorBody(err)
		},
	})

	// Models endpoint: the endpoint's native GET /v1/models catalog in native
	// shape (the pinned jev catalog backs the default endpoint).
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
			return typesafe.ToTypesafeNativeErrorBody(err)
		},
	})

	return routes
}

// parseTypesafeSystemOneRequest reads a systemone body: JSON, or multipart with the
// JSON in the "request" part and each multipart asset's image in the file part its
// "part" names. Only what the request cannot be built without is checked here, each
// a 400 Typesafe also returns: every part has a distinct name, every named part is
// present, and every file part belongs to exactly one asset. The images are
// Typesafe's to judge; their format is read from the bytes when they are sent on,
// as Typesafe does, so a part's own content type is not kept.
func parseTypesafeSystemOneRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
	typesafeReq, ok := req.(*typesafe.TypesafeDecisionRequest)
	if !ok {
		return errors.New("invalid request type for systemone")
	}
	boundary := ctx.Request.Header.MultipartFormBoundary()
	if len(boundary) == 0 {
		return parseJSONRequestBody(ctx.Request.Body(), typesafeReq)
	}

	body, files, err := lib.ReadMultipartRequest(ctx.Request.Body(), string(boundary))
	if err != nil {
		return err
	}
	if err := parseJSONRequestBody(body, typesafeReq); err != nil {
		return err
	}

	refs := make([]string, 0, len(typesafeReq.Assets))
	for ref := range typesafeReq.Assets {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	used := make(map[string]string, len(files))
	for _, ref := range refs {
		asset := typesafeReq.Assets[ref]
		if asset.Part == "" {
			continue
		}
		file, ok := files[asset.Part]
		if !ok {
			return fmt.Errorf("asset %q names part %q, which the request does not carry", ref, asset.Part)
		}
		if other, taken := used[asset.Part]; taken {
			return fmt.Errorf("assets %q and %q both name part %q", other, ref, asset.Part)
		}
		used[asset.Part] = ref
		asset.Data = file
		typesafeReq.Assets[ref] = asset
	}
	for name := range files {
		if _, ok := used[name]; !ok {
			return fmt.Errorf("multipart part %q is not named by any asset", name)
		}
	}
	return nil
}
