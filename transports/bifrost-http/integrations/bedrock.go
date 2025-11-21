package integrations

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// BedrockRouter handles AWS Bedrock-compatible API endpoints
type BedrockRouter struct {
	*GenericRouter
}

// createBedrockConverseRouteConfig creates a route configuration for the Bedrock Converse API endpoint
// Handles POST /bedrock/model/{modelId}/converse
func createBedrockConverseRouteConfig(pathPrefix string, handlerStore lib.HandlerStore) RouteConfig {
	return RouteConfig{
		Type:   RouteConfigTypeBedrock,
		Path:   pathPrefix + "/model/{modelId}/converse",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &bedrock.BedrockConverseRequest{}
		},
		RequestConverter: func(ctx *context.Context, req interface{}) (*schemas.BifrostRequest, error) {
			if bedrockReq, ok := req.(*bedrock.BedrockConverseRequest); ok {
				bifrostReq, err := bedrockReq.ToBifrostResponsesRequest()
				if err != nil {
					return nil, fmt.Errorf("failed to convert bedrock request: %w", err)
				}
				return &schemas.BifrostRequest{
					ResponsesRequest: bifrostReq,
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		ResponsesResponseConverter: func(ctx *context.Context, resp *schemas.BifrostResponsesResponse) (interface{}, error) {
			return bedrock.ToBedrockConverseResponse(resp)
		},
		ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
			return bedrock.ToBedrockError(err)
		},
		PreCallback: bedrockPreCallback(handlerStore),
	}
}

// createBedrockInvokeRouteConfig creates a route configuration for the Bedrock Invoke API endpoint
// Handles POST /bedrock/model/{modelId}/invoke
func createBedrockInvokeRouteConfig(pathPrefix string, handlerStore lib.HandlerStore) RouteConfig {
	return RouteConfig{
		Type:   RouteConfigTypeBedrock,
		Path:   pathPrefix + "/model/{modelId}/invoke",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &bedrock.BedrockTextCompletionRequest{}
		},
		RequestConverter: func(ctx *context.Context, req interface{}) (*schemas.BifrostRequest, error) {
			if bedrockReq, ok := req.(*bedrock.BedrockTextCompletionRequest); ok {
				return &schemas.BifrostRequest{
					TextCompletionRequest: bedrockReq.ToBifrostTextCompletionRequest(),
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		TextResponseConverter: func(ctx *context.Context, resp *schemas.BifrostTextCompletionResponse) (interface{}, error) {
			return bedrock.ToBedrockTextCompletionResponse(resp), nil
		},
		ErrorConverter: func(ctx *context.Context, err *schemas.BifrostError) interface{} {
			return bedrock.ToBedrockError(err)
		},
		PreCallback: bedrockPreCallback(handlerStore),
	}
}

// CreateBedrockRouteConfigs creates route configurations for Bedrock endpoints
func CreateBedrockRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	return []RouteConfig{
		createBedrockConverseRouteConfig(pathPrefix, handlerStore),
		createBedrockInvokeRouteConfig(pathPrefix, handlerStore),
	}
}

// NewBedrockRouter creates a new BedrockRouter with the given bifrost client
func NewBedrockRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *BedrockRouter {
	return &BedrockRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, CreateBedrockRouteConfigs("/bedrock", handlerStore), logger),
	}
}

// bedrockPreCallback returns a pre-callback that extracts model ID and handles direct authentication
func bedrockPreCallback(handlerStore lib.HandlerStore) func(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
	return func(ctx *fasthttp.RequestCtx, bifrostCtx *context.Context, req interface{}) error {
		// Extract modelId from path parameter
		modelIDVal := ctx.UserValue("modelId")
		if modelIDVal == nil {
			return errors.New("modelId not found in path")
		}

		modelIDStr, ok := modelIDVal.(string)
		if !ok {
			return fmt.Errorf("modelId must be a string, got %T", modelIDVal)
		}
		if modelIDStr == "" {
			return errors.New("modelId cannot be empty")
		}

		// Set the model ID with bedrock/ prefix for provider routing
		fullModelID := "bedrock/" + modelIDStr

		switch r := req.(type) {
		case *bedrock.BedrockConverseRequest:
			r.ModelID = fullModelID
		case *bedrock.BedrockTextCompletionRequest:
			r.ModelID = fullModelID
		default:
			return errors.New("invalid request type for bedrock model extraction")
		}

		// Handle direct key authentication if allowed
		if !handlerStore.ShouldAllowDirectKeys() {
			return nil
		}

		// Check for Bedrock API Key (alternative to AWS Credentials)
		apiKey := string(ctx.Request.Header.Peek("x-bf-bedrock-api-key"))

		// Check for AWS Credentials
		accessKey := string(ctx.Request.Header.Peek("x-bf-bedrock-access-key"))
		secretKey := string(ctx.Request.Header.Peek("x-bf-bedrock-secret-key"))
		region := string(ctx.Request.Header.Peek("x-bf-bedrock-region"))
		sessionToken := string(ctx.Request.Header.Peek("x-bf-bedrock-session-token"))

		if apiKey != "" {
			// Case 1: API Key Authentication
			key := schemas.Key{
				ID:    uuid.New().String(),
				Value: apiKey,
				// BedrockKeyConfig is required by the provider even if using API Key
				BedrockKeyConfig: &schemas.BedrockKeyConfig{},
			}

			if region != "" {
				key.BedrockKeyConfig.Region = &region
			}

			*bifrostCtx = context.WithValue(*bifrostCtx, schemas.BifrostContextKeyDirectKey, key)
			return nil
		} else if accessKey != "" && secretKey != "" {
			// Case 2: AWS Credentials Authentication
			if region == "" {
				return errors.New("x-bf-bedrock-region header is required when using direct keys")
			}

			key := schemas.Key{
				ID: uuid.New().String(),
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					AccessKey: accessKey,
					SecretKey: secretKey,
				},
			}

			if region != "" {
				key.BedrockKeyConfig.Region = &region
			}

			if sessionToken != "" {
				key.BedrockKeyConfig.SessionToken = &sessionToken
			}

			*bifrostCtx = context.WithValue(*bifrostCtx, schemas.BifrostContextKeyDirectKey, key)
		}

		return nil
	}
}
