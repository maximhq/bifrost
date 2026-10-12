package handlers

import (
	"bytes"
	"context"
	"io"
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

// closeTrackingReader is a recording body that remembers being closed.
type closeTrackingReader struct {
	io.Reader
	closed bool
}

func (r *closeTrackingReader) Close() error {
	r.closed = true
	return nil
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
	recording := &closeTrackingReader{Reader: bytes.NewReader(wav)}
	client := &fakeLiveContentClient{content: &schemas.LiveContentResponse{SessionID: "live_1", Body: recording, ContentType: "audio/wav", ContentLength: int64(len(wav))}}
	ctx := &fasthttp.RequestCtx{}
	released := false
	serveLiveContent(ctx, schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), client, schemas.OpenAI, "live_1", func() { released = true })

	// The download goes through the client, so the plugin pipeline counts, logs and traces it.
	require.NotNil(t, client.req)
	assert.Equal(t, &schemas.BifrostLiveContentRequest{Provider: schemas.OpenAI, SessionID: "live_1"}, client.req)
	assert.Equal(t, schemas.LiveContentRequest, client.ctx.Value(schemas.BifrostContextKeyHTTPRequestType))
	assert.Equal(t, "live_1", client.ctx.Value(schemas.BifrostContextKeyRealtimeProviderSessionID), "the log row names the session the recording belongs to")

	// The recording is streamed, not copied: the body is the reader, closed once it has been sent.
	assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	assert.Equal(t, "audio/wav", string(ctx.Response.Header.ContentType()))
	assert.Equal(t, len(wav), ctx.Response.Header.ContentLength())
	assert.False(t, released, "the request context outlives the handler while the body is still to be sent")
	assert.Equal(t, true, ctx.UserValue(lib.FastHTTPUserValueLargeResponseMode), "the post-hook middleware must not drain the stream")
	assert.Equal(t, wav, ctx.Response.Body())
	assert.True(t, recording.closed, "the upstream response is released once the body has been sent")
	assert.True(t, released, "the request context ends once the body has been sent")

	// A refusal from the provider is relayed with its status.
	refused := &fakeLiveContentClient{err: &schemas.BifrostError{StatusCode: new(fasthttp.StatusNotFound), Error: &schemas.ErrorField{Message: "Session not found."}}}
	ctx = &fasthttp.RequestCtx{}
	released = false
	serveLiveContent(ctx, schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), refused, schemas.OpenAI, "live_2", func() { released = true })
	assert.True(t, released, "a refusal ends the request context at once")
	assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode())
	assert.Contains(t, string(ctx.Response.Body()), "Session not found.")
}

// releaseOrderReader is a recording body that remembers whether the request was released
// before it was closed.
type releaseOrderReader struct {
	io.Reader
	released      *bool
	releasedFirst bool
}

func (r *releaseOrderReader) Close() error {
	r.releasedFirst = *r.released
	return nil
}

func TestLiveContentBodyAbandonedMidwayIsCancelledFirst(t *testing.T) {
	t.Parallel()

	body := func() (*liveContentBody, *releaseOrderReader, *bool) {
		released := false
		r := &releaseOrderReader{Reader: bytes.NewReader(make([]byte, 16)), released: &released}
		return &liveContentBody{ReadCloser: r, release: func() { released = true }}, r, &released
	}

	// The client hangs up after the first bytes: the request is cancelled before the upstream
	// body closes, so the stream is abandoned instead of drained to its end for nobody.
	b, r, released := body()
	_, err := b.Read(make([]byte, 4))
	require.NoError(t, err)
	require.NoError(t, b.Close())
	assert.True(t, r.releasedFirst, "an abandoned download cancels the request before closing the upstream body")
	assert.True(t, *released)

	// The whole recording was sent: the upstream body closes first and goes back to its pool.
	b, r, released = body()
	_, err = io.Copy(io.Discard, b)
	require.NoError(t, err)
	require.NoError(t, b.Close())
	assert.False(t, r.releasedFirst, "a finished download closes the upstream body before the request ends")
	assert.True(t, *released)
}
