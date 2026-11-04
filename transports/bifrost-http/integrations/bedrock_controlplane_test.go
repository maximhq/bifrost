package integrations

import (
	"context"
	"testing"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// The Bedrock control-plane listings used to fall through to the dashboard catch-all and come back as the
// UI's index.html with a 200 (#9531). They now match their own routes ahead of the catch-all, next to the
// /bedrock runtime routes, and nothing else under /bedrock changes owner.
func TestBedrockControlPlaneRouterRegistersOnlyTheListings(t *testing.T) {
	r := router.New()
	const dashboard = 299
	r.GET("/{filepath:*}", func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(dashboard) })
	NewBedrockRouter(nil, &mockHandlerStore{}, nil, &testLogger{}).RegisterRoutes(r)
	NewBedrockControlPlaneRouter(nil, &mockHandlerStore{}, nil, &testLogger{}).RegisterRoutes(r, func(next fasthttp.RequestHandler) fasthttp.RequestHandler {
		return func(ctx *fasthttp.RequestCtx) { ctx.SetStatusCode(fasthttp.StatusNoContent) }
	})

	cases := []struct {
		method, uri string
		want        int
	}{
		{fasthttp.MethodGet, "/bedrock/inference-profiles?type=SYSTEM_DEFINED&maxResults=2", fasthttp.StatusNoContent},
		{fasthttp.MethodGet, "/bedrock/inference-profiles/us.anthropic.claude-sonnet-4-6", fasthttp.StatusNoContent},
		{fasthttp.MethodGet, "/bedrock/foundation-models", fasthttp.StatusNoContent},
		{fasthttp.MethodGet, "/bedrock/foundation-models/anthropic.claude-3-haiku-20240307-v1:0", fasthttp.StatusNoContent},
		// Only the four listings are claimed: other control-plane paths keep their previous owner.
		{fasthttp.MethodGet, "/bedrock/custom-models", dashboard},
		{fasthttp.MethodGet, "/bedrock/inference-profiles/a/b", dashboard},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.uri, func(t *testing.T) {
			var ctx fasthttp.RequestCtx
			ctx.Request.Header.SetMethod(tc.method)
			ctx.Request.SetRequestURI(tc.uri)
			r.Handler(&ctx)
			require.Equal(t, tc.want, ctx.Response.StatusCode())
		})
	}
}

// A listing reaches the Bedrock provider as a passthrough with the /bedrock prefix stripped and the query
// kept, names no model, and skips model allowlists: it runs nothing.
func TestBedrockControlPlaneListingReachesCoreAsModelFreePassthrough(t *testing.T) {
	plugin := &codexPassthroughReceipt{}
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{Account: codexPassthroughAccount{}, LLMPlugins: []schemas.LLMPlugin{plugin}, Logger: bifrost.NewNoOpLogger()})
	require.NoError(t, err)
	defer client.Shutdown()
	r := router.New()
	NewBedrockControlPlaneRouter(client, &mockHandlerStore{}, nil, &testLogger{}).RegisterRoutes(r)

	var ctx fasthttp.RequestCtx
	ctx.Request.Header.SetMethod(fasthttp.MethodGet)
	ctx.Request.SetRequestURI("/bedrock/inference-profiles?type=SYSTEM_DEFINED&maxResults=2")
	r.Handler(&ctx)

	require.Equal(t, 403, ctx.Response.StatusCode())
	require.NotNil(t, plugin.request)
	require.Equal(t, schemas.PassthroughRequest, plugin.request.RequestType, "a listing answers with plain JSON")
	require.True(t, plugin.skipModelCheck, "a listing runs no model, so model allowlists must not refuse it")
	pt := plugin.request.PassthroughRequest
	require.Equal(t, schemas.Bedrock, pt.Provider)
	require.Equal(t, fasthttp.MethodGet, pt.Method)
	require.Equal(t, "/inference-profiles", pt.Path)
	require.Equal(t, "type=SYSTEM_DEFINED&maxResults=2", pt.RawQuery)
	require.Empty(t, pt.Model)
}
