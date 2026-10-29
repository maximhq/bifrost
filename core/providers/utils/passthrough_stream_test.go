package utils

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
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

// hangupBody makes a read that the cancellation close interrupts fail the way a live connection
// does. fasthttp's body reports io.EOF there, which the stream loop would take as a clean end.
type hangupBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *hangupBody) CloseWithError(err error) error {
	b.closed.Store(true)
	closeBodyStream(b.Reader, err)
	return nil
}

func (b *hangupBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF && b.closed.Load() {
		err = net.ErrClosed
	}
	return n, err
}

// runParkedPassthroughStream serves responsesStreamBody as a chunked SSE response and then keeps
// the connection open, like an upstream that parks after response.completed. With hangUp set the
// request is cancelled as soon as the client reads response.completed, the way Codex hangs up. It
// returns every post-hook call and whether the stream channel closed within the deadline.
func runParkedPassthroughStream(t *testing.T, endsStream func([]byte) bool, hangUp bool) (finals []*schemas.BifrostPassthroughResponse, errs []*schemas.BifrostError, closed bool) {
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

	ch := StreamPassthrough(ctx, runner, nil, resp, &hangupBody{Reader: resp.BodyStream()}, PassthroughStreamParams{
		StatusCode: 200,
		EndsStream: endsStream,
		Observe: func(event []byte) *schemas.BifrostPassthroughUsage {
			if strings.Contains(string(event), `"usage"`) {
				return &schemas.BifrostPassthroughUsage{}
			}
			return nil
		},
	})

	done := make(chan struct{})
	go func() {
		for chunk := range ch {
			if hangUp && chunk.BifrostPassthroughResponse != nil && strings.Contains(string(chunk.BifrostPassthroughResponse.Body), "response.completed") {
				cancel()
			}
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
	finals, errs, closed := runParkedPassthroughStream(t, endsResponsesStream, false)
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
	finals, errs, closed := runParkedPassthroughStream(t, endsResponsesStream, true)
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
