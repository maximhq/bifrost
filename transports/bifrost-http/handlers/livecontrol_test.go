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

// fakeLiveContentClient answers the recording download with a fixed body or error.
type fakeLiveContentClient struct {
	ctx     *schemas.BifrostContext
	req     *schemas.BifrostLiveContentRequest
	content *schemas.LiveContentResponse
	err     *schemas.BifrostError
}

func (f *fakeLiveContentClient) LiveSessionContentRequest(ctx *schemas.BifrostContext, req *schemas.BifrostLiveContentRequest) (*schemas.LiveContentResponse, *schemas.BifrostError) {
	f.ctx, f.req = ctx, req
	return f.content, f.err
}

func TestServeLiveContentWritesTheRecording(t *testing.T) {
	t.Parallel()

	wav := []byte("RIFF....WAVEfmt ")
	client := &fakeLiveContentClient{content: &schemas.LiveContentResponse{SessionID: "live_1", Content: wav, ContentType: "audio/wav"}}
	ctx := &fasthttp.RequestCtx{}
	serveLiveContent(ctx, schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), client, schemas.OpenAI, "live_1")

	// The download goes through the client, so the plugin pipeline counts, logs and traces it.
	require.NotNil(t, client.req)
	assert.Equal(t, &schemas.BifrostLiveContentRequest{Provider: schemas.OpenAI, SessionID: "live_1"}, client.req)
	assert.Equal(t, schemas.LiveContentRequest, client.ctx.Value(schemas.BifrostContextKeyHTTPRequestType))
	assert.Equal(t, "live_1", client.ctx.Value(schemas.BifrostContextKeyRealtimeProviderSessionID), "the log row names the session the recording belongs to")

	assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	assert.Equal(t, "audio/wav", string(ctx.Response.Header.ContentType()))
	assert.Equal(t, len(wav), ctx.Response.Header.ContentLength())
	assert.Equal(t, wav, ctx.Response.Body())

	// A refusal from the provider is relayed with its status.
	refused := &fakeLiveContentClient{err: &schemas.BifrostError{StatusCode: new(fasthttp.StatusNotFound), Error: &schemas.ErrorField{Message: "Session not found."}}}
	ctx = &fasthttp.RequestCtx{}
	serveLiveContent(ctx, schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), refused, schemas.OpenAI, "live_2")
	assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode())
	assert.Contains(t, string(ctx.Response.Body()), "Session not found.")
}
