package utils

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const responsesStreamBody = "event: response.created\ndata: {\"type\":\"response.created\"}\n\n" +
	"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":3}}}\n\n"

func endsResponsesStream(event []byte) bool {
	return strings.Contains(string(event), `"type":"response.completed"`)
}

// runParkedPassthroughStream serves responsesStreamBody as a chunked SSE response and then keeps
// the connection open, like an upstream that parks after response.completed. The request is
// cancelled after cancelAfter, the way Codex hangs up. It returns every post-hook call and
// whether the stream channel closed within the deadline.
func runParkedPassthroughStream(t *testing.T, endsStream func([]byte) bool, cancelAfter time.Duration) (finals []*schemas.BifrostPassthroughResponse, errs []*schemas.BifrostError, closed bool) {
	t.Helper()
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	srv := newScriptedServer(t, func(conn net.Conn, br *bufio.Reader) {
		if !readRequest(br) {
			return
		}
		writeAll(t, conn, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
		writeAll(t, conn, fmt.Sprintf("%x\r\n%s\r\n", len(responsesStreamBody), responsesStreamBody))
		<-hold
	})

	goCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := schemas.NewBifrostContext(goCtx, time.Time{})
	req, resp := streamGet(t, BuildStreamingClient(srv.client(5*time.Second)), "POST")
	defer fasthttp.ReleaseRequest(req)

	runner := func(_ *schemas.BifrostContext, r *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		if err != nil {
			errs = append(errs, err)
		} else if r != nil && r.PassthroughResponse != nil && r.PassthroughResponse.Body == nil {
			finals = append(finals, r.PassthroughResponse)
		}
		return r, err
	}

	ch := StreamPassthrough(ctx, runner, nil, resp, resp.BodyStream(), PassthroughStreamParams{
		StatusCode: 200,
		EndsStream: endsStream,
		Observe: func(event []byte) *schemas.BifrostPassthroughUsage {
			if strings.Contains(string(event), `"usage"`) {
				return &schemas.BifrostPassthroughUsage{}
			}
			return nil
		},
	})

	time.AfterFunc(cancelAfter, cancel)
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
		return finals, errs, true
	case <-time.After(2 * time.Second):
		return finals, errs, false
	}
}

func TestStreamPassthrough_EndsStreamAbandonsParkedUpstream(t *testing.T) {
	// No client hangup: release must not drain a body the upstream never closes.
	finals, errs, closed := runParkedPassthroughStream(t, endsResponsesStream, time.Hour)
	if !closed {
		t.Fatal("stream channel did not close: release is draining the parked upstream body")
	}
	if len(errs) != 0 {
		t.Fatalf("expected no error, got %q", errs[0].Error.Message)
	}
	if len(finals) != 1 || finals[0].PassthroughUsage == nil {
		t.Fatalf("expected one final chunk with usage from response.completed, got %d", len(finals))
	}
}

func TestStreamPassthrough_EndsStreamFinalizesBeforeClientHangup(t *testing.T) {
	finals, errs, closed := runParkedPassthroughStream(t, endsResponsesStream, 200*time.Millisecond)
	if !closed {
		t.Fatal("stream channel did not close")
	}
	if len(errs) != 0 {
		t.Fatalf("expected no cancellation error, got %q", errs[0].Error.Message)
	}
	if len(finals) != 1 || finals[0].PassthroughUsage == nil {
		t.Fatalf("expected one final chunk with usage from response.completed, got %d", len(finals))
	}
}
