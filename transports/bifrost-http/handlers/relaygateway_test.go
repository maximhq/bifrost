package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/relay"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestEndpointRequestPath(t *testing.T) {
	cases := []struct {
		in, name, path string
		ok             bool
	}{
		{"/relay/tavily/search", "tavily", "/search", true},
		{"/relay/tavily", "tavily", "/", true},
		{"/relay/tavily/v1/a/b", "tavily", "/v1/a/b", true},
		{"/relay/bad name/x", "", "", false},
		{"/relay//x", "", "", false},
		{"/other/tavily/x", "", "", false},
	}
	for _, tc := range cases {
		name, path, ok := endpointRequestPath(tc.in)
		assert.Equal(t, tc.ok, ok, tc.in)
		assert.Equal(t, tc.name, name, tc.in)
		assert.Equal(t, tc.path, path, tc.in)
	}
}

func TestRelayGatewayHandlerRelays(t *testing.T) {
	var got *http.Request
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream", "yes")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	manager := relay.NewManager(nil, nil)
	require.NoError(t, manager.Upsert(&schemas.RelayConfig{
		Name: "tavily", BaseURL: upstream.URL, SkipRouteCheck: true,
		Auth:       schemas.RelayAuthConfig{Type: schemas.RelayAuthBearer},
		Credential: &schemas.RelayCredential{Value: schemas.NewSecretVar("sk-endpoint")},
	}))
	router := NewRelayGatewayHandler(manager, nil, nil)

	var ctx fasthttp.RequestCtx
	ctx.Request.SetRequestURI("/relay/tavily/search?q=1")
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.Header.Set("Authorization", "Bearer sk-bf-virtual-key")
	ctx.Request.Header.Set("x-bf-vk", "sk-bf-virtual-key")
	ctx.Request.Header.Set("Cookie", "session=1")
	ctx.Request.Header.Set("X-Custom", "kept")
	ctx.Request.SetBodyString(`{"query":"x"}`)
	router.handle(&ctx)

	require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.Equal(t, `{"ok":true}`, string(ctx.Response.Body()))
	assert.Equal(t, "yes", string(ctx.Response.Header.Peek("X-Upstream")))
	require.NotNil(t, got)
	assert.Equal(t, "Bearer sk-endpoint", got.Header.Get("Authorization"), "the caller's virtual key never reaches the upstream")
	assert.Empty(t, got.Header.Get("x-bf-vk"))
	assert.Empty(t, got.Header.Get("Cookie"))
	assert.Equal(t, "kept", got.Header.Get("X-Custom"))
	assert.Equal(t, "/search", got.URL.Path)
	assert.Equal(t, "q=1", got.URL.RawQuery)
	assert.Equal(t, `{"query":"x"}`, gotBody)

	var missing fasthttp.RequestCtx
	missing.Request.SetRequestURI("/relay/unknown/x")
	missing.Request.Header.SetMethod(fasthttp.MethodGet)
	router.handle(&missing)
	assert.Equal(t, 404, missing.Response.StatusCode())

	var traversal fasthttp.RequestCtx
	traversal.Request.SetRequestURI("/relay/tavily/x")
	traversal.Request.Header.SetMethod(fasthttp.MethodGet)
	traversal.URI().SetPathBytes([]byte("/relay/tavily//evil.com/x"))
	router.handle(&traversal)
	require.Equal(t, 200, traversal.Response.StatusCode())
	assert.Equal(t, upstream.Listener.Addr().String(), got.Host, "a doubled slash cannot move the request off the endpoint's host")
	assert.Equal(t, "/evil.com/x", got.URL.Path)
}

func TestRelayStreamRunsDeferredTransportPostHooks(t *testing.T) {
	var slot atomic.Value
	ran := false
	slot.Store(func() ([]schemas.PluginLogEntry, error) {
		ran = true
		return []schemas.PluginLogEntry{{PluginName: "transport"}}, nil
	})
	logs := runTransportPostHookCompleter(&slot, nil)
	assert.True(t, ran, "the transport post-hooks run once the stream ends")
	assert.Len(t, logs, 1)

	var empty atomic.Value
	assert.Nil(t, runTransportPostHookCompleter(&empty, nil), "no completer published means no transport plugins to run")
}

// A caller pointing an existing SDK (Brave Search, ElevenLabs, Deepgram, SerpApi) at a relay sends
// its Bifrost virtual key in the header or query parameter the relay is configured to authenticate
// with. The handler must settle that virtual key onto the request's identity grant while stripping
// the caller's header/query value and replacing it with the relay's upstream credential.
func TestRelayGatewayHandlerExtractsVirtualKeyFromRelayAuthSlot(t *testing.T) {
	var got *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	manager := relay.NewManager(nil, nil)
	var seenVK string
	var seenGrantVK string
	var seenRawQuery string
	var seenCallerHeaders map[string]string
	var seenCtxReqHeaders map[string]string
	pipeline := &testRelayVKPipeline{onPre: func(ctx *schemas.BifrostContext, req *schemas.BifrostRelayRequest) {
		seenVK, _ = ctx.Value(schemas.BifrostContextKeyVirtualKey).(string)
		if ctx.Grant() != nil && ctx.Grant().Identity() != nil {
			cred := ctx.Grant().Identity().Credential()
			if cred.Kind == string(grant.CredentialVirtualKey) {
				seenGrantVK = cred.Value
			}
		}
		seenRawQuery = req.RawQuery
		seenCallerHeaders = req.Headers
		seenCtxReqHeaders, _ = ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	}}
	manager.SetPluginPipeline(func() relay.PluginPipeline { return pipeline }, func(relay.PluginPipeline) {})

	require.NoError(t, manager.Upsert(&schemas.RelayConfig{
		Name: "brave", BaseURL: upstream.URL, SkipRouteCheck: true,
		Auth:       schemas.RelayAuthConfig{Type: schemas.RelayAuthHeader, HeaderName: "X-Subscription-Token"},
		Credential: &schemas.RelayCredential{Value: schemas.NewSecretVar("BSA-upstream-secret")},
	}))
	require.NoError(t, manager.Upsert(&schemas.RelayConfig{
		Name: "custom-hdr", BaseURL: upstream.URL, SkipRouteCheck: true,
		Auth:       schemas.RelayAuthConfig{Type: schemas.RelayAuthHeader, HeaderName: "X-Vendor-Key", ValuePrefix: "Key "},
		Credential: &schemas.RelayCredential{Value: schemas.NewSecretVar("vendor-upstream-secret")},
	}))
	require.NoError(t, manager.Upsert(&schemas.RelayConfig{
		Name: "maps", BaseURL: upstream.URL, SkipRouteCheck: true,
		Auth:       schemas.RelayAuthConfig{Type: schemas.RelayAuthQuery, QueryParam: "appid"},
		Credential: &schemas.RelayCredential{Value: schemas.NewSecretVar("maps-upstream-secret")},
	}))
	require.NoError(t, manager.Upsert(&schemas.RelayConfig{
		Name: "basic-svc", BaseURL: upstream.URL, SkipRouteCheck: true,
		Auth:       schemas.RelayAuthConfig{Type: schemas.RelayAuthBasic},
		Credential: &schemas.RelayCredential{Value: schemas.NewSecretVar("upstream:secret")},
	}))

	router := NewRelayGatewayHandler(manager, nil, nil)

	t.Run("X-Subscription-Token virtual key", func(t *testing.T) {
		seenVK, seenGrantVK, seenRawQuery, seenCallerHeaders, seenCtxReqHeaders = "", "", "", nil, nil
		var ctx fasthttp.RequestCtx
		ctx.Request.SetRequestURI("/relay/brave/res/v1/web/search?q=bifrost")
		ctx.Request.Header.SetMethod(fasthttp.MethodGet)
		ctx.Request.Header.Set("X-Subscription-Token", "sk-bf-brave-caller-vk")
		router.handle(&ctx)

		require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		assert.Equal(t, "sk-bf-brave-caller-vk", seenVK)
		assert.Equal(t, "sk-bf-brave-caller-vk", seenGrantVK, "identity grant must be settled with the extracted virtual key")
		assert.Empty(t, seenCallerHeaders["x-subscription-token"], "caller credential header must be stripped")
		assert.Empty(t, seenCtxReqHeaders["x-subscription-token"], "caller credential header must be scrubbed from context request headers")
		require.NotNil(t, got)
		assert.Equal(t, "BSA-upstream-secret", got.Header.Get("X-Subscription-Token"), "upstream receives the relay's stored credential")
	})

	t.Run("custom relay header with value_prefix", func(t *testing.T) {
		seenVK, seenGrantVK, seenRawQuery, seenCallerHeaders, seenCtxReqHeaders = "", "", "", nil, nil
		var ctx fasthttp.RequestCtx
		ctx.Request.SetRequestURI("/relay/custom-hdr/v1/run")
		ctx.Request.Header.SetMethod(fasthttp.MethodPost)
		ctx.Request.Header.Set("X-Vendor-Key", "Key sk-bf-vendor-vk")
		router.handle(&ctx)

		require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		assert.Equal(t, "sk-bf-vendor-vk", seenVK)
		assert.Equal(t, "sk-bf-vendor-vk", seenGrantVK)
		assert.Empty(t, seenCallerHeaders["x-vendor-key"], "configured relay auth header is dropped even when not in the static denylist")
		assert.Empty(t, seenCtxReqHeaders["x-vendor-key"], "configured relay auth header is scrubbed from context request headers")
		require.NotNil(t, got)
		assert.Equal(t, "Key vendor-upstream-secret", got.Header.Get("X-Vendor-Key"))
	})

	t.Run("query param virtual key is stripped before pre-hooks", func(t *testing.T) {
		seenVK, seenGrantVK, seenRawQuery, seenCallerHeaders, seenCtxReqHeaders = "", "", "", nil, nil
		var ctx fasthttp.RequestCtx
		ctx.Request.SetRequestURI("/relay/maps/geocode?q=tokyo&appid=sk-bf-maps-vk")
		ctx.Request.Header.SetMethod(fasthttp.MethodGet)
		router.handle(&ctx)

		require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		assert.Equal(t, "sk-bf-maps-vk", seenVK)
		assert.Equal(t, "sk-bf-maps-vk", seenGrantVK)
		assert.Equal(t, "q=tokyo", seenRawQuery, "caller query credential is stripped before PreRelayHook runs")
		require.NotNil(t, got)
		assert.Equal(t, "q=tokyo&appid=maps-upstream-secret", got.URL.RawQuery)
	})

	t.Run("basic auth virtual key", func(t *testing.T) {
		seenVK, seenGrantVK, seenRawQuery, seenCallerHeaders, seenCtxReqHeaders = "", "", "", nil, nil
		var ctx fasthttp.RequestCtx
		ctx.Request.SetRequestURI("/relay/basic-svc/v1/op")
		ctx.Request.Header.SetMethod(fasthttp.MethodPost)
		// "user:sk-bf-basic-vk" -> base64 "dXNlcjpzay1iZi1iYXNpYy12aw=="
		ctx.Request.Header.Set("Authorization", "Basic dXNlcjpzay1iZi1iYXNpYy12aw==")
		router.handle(&ctx)

		require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		assert.Equal(t, "sk-bf-basic-vk", seenVK)
		assert.Equal(t, "sk-bf-basic-vk", seenGrantVK)
		require.NotNil(t, got)
		assert.Equal(t, "Basic dXBzdHJlYW06c2VjcmV0", got.Header.Get("Authorization"))
	})

	t.Run("explicit x-bf-vk takes precedence over relay auth slot", func(t *testing.T) {
		seenVK, seenGrantVK, seenRawQuery, seenCallerHeaders, seenCtxReqHeaders = "", "", "", nil, nil
		var ctx fasthttp.RequestCtx
		ctx.Request.SetRequestURI("/relay/brave/res/v1/web/search?q=bifrost")
		ctx.Request.Header.SetMethod(fasthttp.MethodGet)
		ctx.Request.Header.Set("x-bf-vk", "sk-bf-primary-vk")
		ctx.Request.Header.Set("X-Subscription-Token", "sk-bf-secondary-vk")
		router.handle(&ctx)

		require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		assert.Equal(t, "sk-bf-primary-vk", seenVK)
		assert.Equal(t, "sk-bf-primary-vk", seenGrantVK)
		require.NotNil(t, got)
		assert.Equal(t, "BSA-upstream-secret", got.Header.Get("X-Subscription-Token"))
	})

	t.Run("non-VK caller credential in relay slot is ignored as VK and still replaced", func(t *testing.T) {
		seenVK, seenGrantVK, seenRawQuery, seenCallerHeaders, seenCtxReqHeaders = "", "", "", nil, nil
		var ctx fasthttp.RequestCtx
		ctx.Request.SetRequestURI("/relay/custom-hdr/v1/run")
		ctx.Request.Header.SetMethod(fasthttp.MethodPost)
		ctx.Request.Header.Set("X-Vendor-Key", "Key vendor-caller-secret")
		router.handle(&ctx)

		require.Equal(t, 200, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		assert.Empty(t, seenVK)
		assert.Empty(t, seenGrantVK)
		assert.Empty(t, seenCallerHeaders["x-vendor-key"])
		require.NotNil(t, got)
		assert.Equal(t, "Key vendor-upstream-secret", got.Header.Get("X-Vendor-Key"))
	})
}

type testRelayVKPipeline struct {
	onPre func(ctx *schemas.BifrostContext, req *schemas.BifrostRelayRequest)
}

func (p *testRelayVKPipeline) RunRelayPreHooks(ctx *schemas.BifrostContext, req *schemas.BifrostRelayRequest, entered func(int)) (*schemas.BifrostRelayRequest, *schemas.RelayPluginShortCircuit, int) {
	entered(1)
	if p.onPre != nil {
		p.onPre(ctx, req)
	}
	return req, nil, 1
}

func (p *testRelayVKPipeline) RunRelayPostHooks(_ *schemas.BifrostContext, resp *schemas.BifrostRelayResponse, bErr *schemas.BifrostError, _ int) (*schemas.BifrostRelayResponse, *schemas.BifrostError) {
	return resp, bErr
}
