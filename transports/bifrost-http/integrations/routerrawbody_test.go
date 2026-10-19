package integrations

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TestRawBodyForRequestDetachesAsyncBodies is the regression for async jobs reading
// a raw body that aliases fasthttp's pooled buffer: the job runs after the 202, when
// the next request on the keep-alive connection may already have overwritten it.
func TestRawBodyForRequestDetachesAsyncBodies(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBody([]byte(`{"model":"claude","messages":[]}`))
	ctx.Request.Header.Set(schemas.AsyncHeaderCreate, "true")

	got := rawBodyForRequest(ctx, ctx.Request.Body())

	// Simulate the next keep-alive request reusing the pooled buffer.
	copy(ctx.Request.Body(), []byte(`XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX`))
	if string(got) != `{"model":"claude","messages":[]}` {
		t.Fatalf("async raw body changed after buffer reuse: %q", got)
	}
}

// TestRawBodyForRequestKeepsSyncAlias pins that synchronous requests keep the
// zero-copy alias, since they finish with the body before fasthttp resets the ctx.
func TestRawBodyForRequestKeepsSyncAlias(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBody([]byte(`{"model":"claude"}`))
	body := ctx.Request.Body()

	got := rawBodyForRequest(ctx, body)
	if len(got) == 0 || &got[0] != &body[0] {
		t.Fatal("synchronous raw body was copied; want the original slice")
	}
}

// TestRawBodyForRequestNilBody checks a missing body stays nil for both paths.
func TestRawBodyForRequestNilBody(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	if got := rawBodyForRequest(ctx, nil); got != nil {
		t.Fatalf("sync nil body = %v, want nil", got)
	}
	ctx.Request.Header.Set(schemas.AsyncHeaderCreate, "true")
	if got := rawBodyForRequest(ctx, nil); got != nil {
		t.Fatalf("async nil body = %v, want nil", got)
	}
}
