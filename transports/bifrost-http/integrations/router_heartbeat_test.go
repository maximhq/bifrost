package integrations

import (
	"encoding/json"
	"io"
	"strings"
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

// Test_handleStreamingGenAIEmitsNoHeartbeat verifies the typed GenAI integration sends no
// SSE comment heartbeat at all. The official google-genai Python SDK (which LangChain's
// ChatGoogleGenerativeAI wraps) only understands "data: " lines and blank lines; every other
// non-empty line is buffered as an error-JSON fragment and json.loads'ed as soon as its brace
// count balances, so any comment line - bare or blank-line delimited - raises
// UnknownApiResponseError and aborts the stream. The delimited block that PR 6252 introduced
// only helped the JavaScript SDK. The body is also replayed through a port of the Python
// parser so the guard fails on any future non-data frame, not only on this heartbeat text.
func Test_handleStreamingGenAIEmitsNoHeartbeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stream := make(chan *schemas.BifrostStreamChunk)
		router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, bifrost.NewNoOpLogger())
		ctx := &fasthttp.RequestCtx{}
		var streamRoute *RouteConfig
		routes := CreateGenAIRouteConfigs("/genai")
		for i := range routes {
			if routes[i].StreamConfig != nil {
				streamRoute = &routes[i]
				break
			}
		}
		require.NotNil(t, streamRoute)
		router.handleStreaming(ctx, nil, *streamRoute, stream, func() {})

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

		time.Sleep(3*lib.DefaultSSEHeartbeatInterval + time.Millisecond)
		close(stream)

		result := <-readDone
		require.NoError(t, result.err)
		assert.NotContains(t, result.body, ": heartbeat", "google-genai Python rejects any SSE comment line")
		for _, seg := range googleGenAIPythonSegments(result.body) {
			assert.True(t, json.Valid([]byte(seg)),
				"google-genai Python would raise UnknownApiResponseError on segment %q", seg)
		}
	})
}

// googleGenAIPythonSegments is a line-for-line port of HttpResponse._iter_response_stream in
// google/genai/_api_client.py (google-genai 1.75.0, unchanged on upstream main as of
// 2026-09-03). It yields the strings the SDK hands to json.loads: "data: " payloads grouped
// until a blank line, and every other non-empty line accumulated until its braces balance.
func googleGenAIPythonSegments(body string) []string {
	var segments []string
	var chunk strings.Builder
	var dataBuffer []string
	balance := 0
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			if len(dataBuffer) > 0 {
				segments = append(segments, strings.Join(dataBuffer, "\n"))
				dataBuffer = nil
			}
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			dataBuffer = append(dataBuffer, line[len("data: "):])
			continue
		}
		for _, c := range line {
			switch c {
			case '{':
				balance++
			case '}':
				balance--
			}
		}
		chunk.WriteString(line)
		if balance == 0 {
			segments = append(segments, chunk.String())
			chunk.Reset()
		}
	}
	if chunk.Len() > 0 {
		segments = append(segments, chunk.String())
	}
	if len(dataBuffer) > 0 {
		segments = append(segments, strings.Join(dataBuffer, "\n"))
	}
	return segments
}

// Test_passthroughHeartbeatEligible pins down the gate handlePassthroughStream uses to
// decide whether the heartbeat is safe to inject: only when the resolved content-type is
// actually SSE, and never for Gemini or Vertex, whose official Python SDK rejects any SSE
// comment line (see Test_handleStreamingGenAIEmitsNoHeartbeat). Injecting anything into a non-SSE passthrough body (e.g.
// Vertex/Gemini's raw incrementally-delivered JSON array) would corrupt a framing this
// path doesn't control, and lookalike media types (e.g. "text/event-stream+json") must not
// be mistaken for SSE either.
func Test_passthroughHeartbeatEligible(t *testing.T) {
	cases := []struct {
		name        string
		provider    schemas.ModelProvider
		contentType string
		want        bool
	}{
		{"plain SSE", schemas.OpenAI, "text/event-stream", true},
		{"SSE with charset param", schemas.OpenAI, "text/event-stream; charset=utf-8", true},
		{"SSE uppercase", schemas.OpenAI, "TEXT/EVENT-STREAM", true},
		{"SSE with surrounding space before param", schemas.OpenAI, "text/event-stream ; charset=utf-8", true},
		{"raw JSON passthrough", schemas.OpenAI, "application/json", false},
		{"empty content-type", schemas.OpenAI, "", false},
		{"binary passthrough", schemas.Bedrock, "application/vnd.amazon.eventstream", false},
		{"prefix-collision suffix", schemas.OpenAI, "text/event-stream+json", false},
		{"prefix-collision longer type", schemas.OpenAI, "text/event-streaming", false},
		{"Gemini SSE passthrough", schemas.Gemini, "text/event-stream", false},
		{"Vertex SSE passthrough", schemas.Vertex, "text/event-stream; charset=utf-8", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, passthroughHeartbeatEligible(tc.provider, tc.contentType))
		})
	}
}
