package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/relay"
	"github.com/maximhq/bifrost/core/schemas"
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
