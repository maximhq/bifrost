package integrations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	router := NewGenericRouter(nil, handlerStore, nil, nil, nil)
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

	router := NewGenericRouter(nil, handlerStore, nil, nil, nil)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetBodyString(`{"model":"openai/gpt-4o","messages":[]}`)
	ctx.SetUserValue(schemas.BifrostContextKeyHTTPRequestType, schemas.ChatCompletionRequest)

	handler := router.createHandler(route)
	handler(ctx)

	assert.Equal(t, 1, parserCalls)
}

// ============================================================================
// resolveLargePayloadMetadata tests
// ============================================================================

func TestResolveLargePayloadMetadata_NilContext(t *testing.T) {
	assert.Nil(t, resolveLargePayloadMetadata(nil))
}

func TestResolveLargePayloadMetadata_SyncPath(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	meta := &schemas.LargePayloadMetadata{Model: "gpt-4o"}
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMetadata, meta)

	result := resolveLargePayloadMetadata(ctx)
	require.NotNil(t, result)
	assert.Equal(t, "gpt-4o", result.Model)
}

func TestResolveLargePayloadMetadata_DeferredReady(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ch := make(chan *schemas.LargePayloadMetadata, 1)
	ch <- &schemas.LargePayloadMetadata{Model: "claude-4"}
	ctx.SetValue(schemas.BifrostContextKeyDeferredLargePayloadMetadata, (<-chan *schemas.LargePayloadMetadata)(ch))

	result := resolveLargePayloadMetadata(ctx)
	require.NotNil(t, result)
	assert.Equal(t, "claude-4", result.Model)

	// Verify it was cached in the sync key.
	cached, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadMetadata).(*schemas.LargePayloadMetadata)
	require.True(t, ok)
	assert.Equal(t, "claude-4", cached.Model)
}

func TestResolveLargePayloadMetadata_DeferredNotReady(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ch := make(chan *schemas.LargePayloadMetadata, 1) // empty, not ready
	ctx.SetValue(schemas.BifrostContextKeyDeferredLargePayloadMetadata, (<-chan *schemas.LargePayloadMetadata)(ch))

	// Non-blocking: should return nil when channel has no value yet.
	result := resolveLargePayloadMetadata(ctx)
	assert.Nil(t, result)
}

func TestResolveLargePayloadMetadata_SyncTakesPrecedence(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	syncMeta := &schemas.LargePayloadMetadata{Model: "sync-model"}
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMetadata, syncMeta)

	ch := make(chan *schemas.LargePayloadMetadata, 1)
	ch <- &schemas.LargePayloadMetadata{Model: "deferred-model"}
	ctx.SetValue(schemas.BifrostContextKeyDeferredLargePayloadMetadata, (<-chan *schemas.LargePayloadMetadata)(ch))

	result := resolveLargePayloadMetadata(ctx)
	require.NotNil(t, result)
	assert.Equal(t, "sync-model", result.Model)
}
