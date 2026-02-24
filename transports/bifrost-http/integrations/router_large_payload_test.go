package integrations

import (
	"context"
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/valyala/fasthttp"
)

func TestCreateHandler_SkipsRequestParserInLargePayloadMode(t *testing.T) {
	handlerStore := &mockHandlerStore{allowDirectKeys: true}
	parserCalls := 0

	route := RouteConfig{
		Type:   RouteConfigTypeOpenAI,
		Path:   "/openai/v1/chat/completions",
		Method: "POST",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.ChatCompletionRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &struct{}{}
		},
		RequestParser: func(ctx *fasthttp.RequestCtx, req interface{}) error {
			parserCalls++
			return nil
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			return nil, errors.New("stop after parse phase")
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return err
		},
	}

	router := NewGenericRouter(nil, handlerStore, nil, nil)
	router.SetLargePayloadHook(func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, routeType RouteConfigType) (bool, error) {
		return true, nil
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetBodyString(`{"model":"openai/gpt-4o","messages":[]}`)
	ctx.SetUserValue(schemas.BifrostContextKeyHTTPRequestType, schemas.ChatCompletionRequest)

	handler := router.createHandler(route)
	handler(ctx)

	assert.Equal(t, 0, parserCalls)
}

func TestCreateHandler_UsesRequestParserWhenNotInLargePayloadMode(t *testing.T) {
	handlerStore := &mockHandlerStore{allowDirectKeys: true}
	parserCalls := 0

	route := RouteConfig{
		Type:   RouteConfigTypeOpenAI,
		Path:   "/openai/v1/chat/completions",
		Method: "POST",
		GetHTTPRequestType: func(ctx *fasthttp.RequestCtx) schemas.RequestType {
			return schemas.ChatCompletionRequest
		},
		GetRequestTypeInstance: func(ctx context.Context) interface{} {
			return &struct{}{}
		},
		RequestParser: func(ctx *fasthttp.RequestCtx, req interface{}) error {
			parserCalls++
			return nil
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			return nil, errors.New("stop after parse phase")
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return err
		},
	}

	router := NewGenericRouter(nil, handlerStore, nil, nil)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetBodyString(`{"model":"openai/gpt-4o","messages":[]}`)
	ctx.SetUserValue(schemas.BifrostContextKeyHTTPRequestType, schemas.ChatCompletionRequest)

	handler := router.createHandler(route)
	handler(ctx)

	assert.Equal(t, 1, parserCalls)
}
