package handlers

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestLiveContentRefusesBeforeFetching(t *testing.T) {
	t.Parallel()

	content := func(enforceAuth bool, id string) *fasthttp.RequestCtx {
		config := &lib.Config{ClientConfig: &configstore.ClientConfig{EnforceAuthOnInference: enforceAuth}}
		h := &LiveControlHandler{gateway: &liveGateway{config: config}}
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("/v1/live/sessions/" + id + "/content")
		ctx.SetUserValue("session_id", id)
		h.handleContent(ctx)
		return ctx
	}

	ctx := content(true, "live_1")
	assert.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode(), "anonymous callers are refused first")

	ctx = content(false, " ")
	assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	assert.Contains(t, string(ctx.Response.Body()), "session id is required")
}

// fakeLiveContentProvider answers the recording download with a fixed body or error.
type fakeLiveContentProvider struct {
	schemas.LiveProvider
	sessionID string
	content   *schemas.LiveContentResponse
	err       *schemas.BifrostError
}

func (f *fakeLiveContentProvider) LiveSessionContent(_ *schemas.BifrostContext, _ schemas.Key, sessionID string) (*schemas.LiveContentResponse, *schemas.BifrostError) {
	f.sessionID = sessionID
	return f.content, f.err
}

func TestServeLiveContentWritesTheRecording(t *testing.T) {
	t.Parallel()

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	wav := []byte("RIFF....WAVEfmt ")
	provider := &fakeLiveContentProvider{content: &schemas.LiveContentResponse{Content: wav, ContentType: "audio/wav"}}
	ctx := &fasthttp.RequestCtx{}
	serveLiveContent(ctx, bifrostCtx, provider, schemas.Key{ID: "key-1"}, "live_1")
	assert.Equal(t, "live_1", provider.sessionID)
	assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	assert.Equal(t, "audio/wav", string(ctx.Response.Header.ContentType()))
	assert.Equal(t, len(wav), ctx.Response.Header.ContentLength())
	assert.Equal(t, wav, ctx.Response.Body())

	// The provider's refusal, such as a session that was not stored, passes through with its status.
	refused := &fakeLiveContentProvider{err: newRealtimeWireBifrostError(404, "invalid_request_error", "Session not found.")}
	ctx = &fasthttp.RequestCtx{}
	serveLiveContent(ctx, bifrostCtx, refused, schemas.Key{ID: "key-1"}, "live_2")
	require.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode())
	assert.Contains(t, string(ctx.Response.Body()), "Session not found.")
}
