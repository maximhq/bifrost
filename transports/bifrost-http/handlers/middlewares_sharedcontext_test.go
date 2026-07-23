package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// traceIDCtxKey is the context key the echo plugin round-trips: a value written
// on the request context earlier in the pipeline and read back in the post-hook.
const traceIDCtxKey = "request_trace_id"

// traceIDHeader is the response header the echo plugin sets from the value it
// reads in its post-hook.
const traceIDHeader = "X-Trace-Id"

// traceEchoPlugin is a minimal HTTPTransportPlugin whose post-hook copies a value
// read from the request context into a response header — the common pattern of a
// plugin surfacing something computed during the request (here, a trace id) on
// the response. It exercises the guarantee that a plugin can, in its post-hook,
// observe a value written on the context earlier in the request pipeline.
type traceEchoPlugin struct{}

func (p *traceEchoPlugin) GetName() string { return "trace-echo" }
func (p *traceEchoPlugin) Cleanup() error  { return nil }

func (p *traceEchoPlugin) HTTPTransportPreHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

func (p *traceEchoPlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	if v, ok := ctx.Value(traceIDCtxKey).(string); ok && v != "" {
		if resp.Headers == nil {
			resp.Headers = make(map[string]string, 1)
		}
		resp.Headers[traceIDHeader] = v
	}
	return nil
}

func (p *traceEchoPlugin) HTTPTransportStreamChunkHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// TestEnsureSharedBifrostContext_ReusedByInnerPipeline is the focused proof of
// the fix's mechanism: the context the transport middleware establishes is the
// exact context the inner request pipeline (lib.ConvertToBifrostContext) adopts,
// so a value written via the inner pipeline is visible on the shared context.
//
// Before the fix the middleware handed the inner pipeline nothing to reuse, so
// ConvertToBifrostContext built a separate context and this assertion fails.
func TestEnsureSharedBifrostContext_ReusedByInnerPipeline(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}

	shared, cancel := ensureSharedBifrostContext(ctx)
	defer cancel()

	// The inner pipeline resolves the request context exactly as core handlers do.
	inner, innerCancel := lib.ConvertToBifrostContext(ctx, nil)
	defer innerCancel()

	if inner != shared {
		t.Fatalf("inner pipeline created a separate context instead of adopting the shared one; post-hook context sharing is broken")
	}

	inner.SetValue(traceIDCtxKey, "trace-abc123")
	if got, _ := shared.Value(traceIDCtxKey).(string); got != "trace-abc123" {
		t.Fatalf("value written via the inner pipeline is not visible on the shared context: got %q, want %q", got, "trace-abc123")
	}
}

// TestTransportInterceptorMiddleware_PostHookSeesInnerPipelineValue drives the
// full middleware: a transport plugin sets a response header from a context
// value that the (simulated) inner pipeline wrote during the request. This is
// the end-to-end guarantee that lets a plugin attach a response header derived
// from per-request work.
//
// Before the fix the value written by the inner pipeline lands on a different
// context than the one the post-hook reads, so the header is never set.
func TestTransportInterceptorMiddleware_PostHookSeesInnerPipelineValue(t *testing.T) {
	cfg := &lib.Config{}
	plugins := []schemas.HTTPTransportPlugin{&traceEchoPlugin{}}
	cfg.HTTPTransportPlugins.Store(&plugins)

	// next stands in for the inner request pipeline: it resolves the shared
	// request context and writes a value on it, the way a core plugin would.
	next := func(ctx *fasthttp.RequestCtx) {
		inner, cancel := lib.ConvertToBifrostContext(ctx, nil)
		defer cancel()
		inner.SetValue(traceIDCtxKey, "trace-abc123")
	}

	handler := TransportInterceptorMiddleware(cfg)(next)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetRequestURI("/v1/chat/completions")
	handler(ctx)

	got := string(ctx.Response.Header.Peek(traceIDHeader))
	if got != "trace-abc123" {
		t.Fatalf("post-hook did not observe the inner-pipeline value; response header %q = %q, want %q", traceIDHeader, got, "trace-abc123")
	}
}
