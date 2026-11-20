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

// A passthrough stream the client cuts off after the provider reported usage (Anthropic's message_start)
// bills that usage: the cancellation error carries it as BilledUsage, as the typed streams' errors do.
func TestStreamPassthrough_ClientHangupBillsObservedUsage(t *testing.T) {
	const body = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":42,\"output_tokens\":1}}}\n\n"
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	srv := newScriptedServer(t, func(conn net.Conn, br *bufio.Reader) {
		if !readRequest(br) {
			return
		}
		writeAll(t, conn, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
		writeAll(t, conn, fmt.Sprintf("%x\r\n%s\r\n", len(body), body))
		<-hold // the provider is still generating when the client leaves
	})

	goCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := schemas.NewBifrostContext(goCtx, time.Time{})
	req, resp := streamGet(t, BuildStreamingClient(srv.client(5*time.Second)), "POST")
	defer fasthttp.ReleaseRequest(req)

	var errs []*schemas.BifrostError
	runner := func(_ *schemas.BifrostContext, r *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		if err != nil {
			errs = append(errs, err)
		}
		return r, err
	}
	ch := StreamPassthrough(ctx, runner, nil, resp, &hangupBody{Reader: resp.BodyStream()}, PassthroughStreamParams{
		StatusCode: 200,
		Observe: func(event []byte) *schemas.BifrostPassthroughUsage {
			if strings.Contains(string(event), `"usage"`) {
				return &schemas.BifrostPassthroughUsage{LLMUsage: &schemas.BifrostLLMUsage{
					PromptTokens: 42, CompletionTokens: 1, TotalTokens: 43}}
			}
			return nil
		},
	})

	done := make(chan struct{})
	go func() {
		for chunk := range ch {
			if chunk.BifrostPassthroughResponse != nil &&
				strings.Contains(string(chunk.BifrostPassthroughResponse.Body), "message_start") {
				cancel() // the client hangs up mid-stream
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream channel did not close")
	}
	if len(errs) != 1 {
		t.Fatalf("expected one cancellation error, got %d", len(errs))
	}
	billed := errs[0].ExtraFields.BilledUsage
	if billed == nil || billed.PromptTokens != 42 || billed.CompletionTokens != 1 {
		t.Fatalf("expected the observed usage (42 prompt, 1 completion) billed on the cancellation error, got %+v", billed)
	}
}
