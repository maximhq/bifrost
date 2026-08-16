package integrations

import (
	"io"
	"testing"
	"testing/synctest"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// A fast/bursty upstream can finish a stream before the reactive (write-failure-based)
// disconnect detector ever gets a second chance to fire (see lib.StartSSEHeartbeat's doc,
// and the inference.go handleStreamingResponse feature this mirrors). This test confirms
// handleStreaming's default (SSE) branch carries that same heartbeat coverage. Bedrock's
// route branch intentionally has no heartbeat -- see the comment at its call site in
// handleStreaming for why a synthetic AWS EventStream frame isn't safe there.

// Test_handleStreamingSSESendsHeartbeatDuringIdleGap verifies the default (SSE) route
// branch of handleStreaming emits SendHeartbeat's comment frame while the stream channel
// is held open, so a client-disconnect during a long idle gap is discovered proactively.
// Runs inside a synctest bubble so the sleep below advances the bubble's fake clock
// deterministically instead of real wall-clock time -- a busy CI worker delaying the
// heartbeat ticker or reader goroutine can no longer make this flaky, unlike a real
// time.Sleep.
func Test_handleStreamingSSESendsHeartbeatDuringIdleGap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stream := make(chan *schemas.BifrostStreamChunk)
		router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, bifrost.NewNoOpLogger())
		ctx := &fasthttp.RequestCtx{}
		router.handleStreaming(ctx, nil, RouteConfig{}, stream, func() {})

		bodyStream := ctx.Response.BodyStream()
		type readResult struct {
			body string
			err  error
		}
		readDone := make(chan readResult, 1)
		go func() {
			b, err := io.ReadAll(bodyStream)
			readDone <- readResult{body: string(b), err: err}
		}()

		// > 1 tick at lib.DefaultSSEHeartbeatInterval before the stream ends. Computed
		// relative to the constant (rather than hardcoded) so this stays correct if the
		// default interval changes.
		time.Sleep(2*lib.DefaultSSEHeartbeatInterval + time.Millisecond)
		close(stream)

		result := <-readDone
		require.NoError(t, result.err)
		assert.Contains(t, result.body, ": heartbeat\n", "expected at least one heartbeat comment frame during the idle gap")
		assert.NotContains(t, result.body, ": heartbeat\n\n", "heartbeat must not carry a blank line, which non-conforming decoders dispatch as an empty event (#5874)")
	})
}

// Test_passthroughHeartbeatEligible pins down the content-type gate handlePassthroughStream
// uses to decide whether the heartbeat is safe to inject: only when the resolved
// content-type is actually SSE. Injecting anything into a non-SSE passthrough body (e.g.
// Vertex/Gemini's raw incrementally-delivered JSON array) would corrupt a framing this
// path doesn't control, and lookalike media types (e.g. "text/event-stream+json") must not
// be mistaken for SSE either.
func Test_passthroughHeartbeatEligible(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		want        bool
	}{
		{"plain SSE", "text/event-stream", true},
		{"SSE with charset param", "text/event-stream; charset=utf-8", true},
		{"SSE uppercase", "TEXT/EVENT-STREAM", true},
		{"SSE with surrounding space before param", "text/event-stream ; charset=utf-8", true},
		{"raw JSON passthrough", "application/json", false},
		{"empty content-type", "", false},
		{"binary passthrough", "application/vnd.amazon.eventstream", false},
		{"prefix-collision suffix", "text/event-stream+json", false},
		{"prefix-collision longer type", "text/event-streaming", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, passthroughHeartbeatEligible(tc.contentType))
		})
	}
}
