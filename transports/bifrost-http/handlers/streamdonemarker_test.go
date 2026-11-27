package handlers

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// A "data: [DONE]" marker states that the stream finished normally. Emitting it
// after an error frame reproduces, one layer up, the same ambiguity that made
// truncated upstream streams look successful: a client keyed on the marker sees
// a clean end either way. See https://github.com/maximhq/bifrost/issues/5546.

// serveStreamingResponse runs handleStreamingResponse against an in-process
// connection and returns the raw SSE body the client would receive.
func serveStreamingResponse(t *testing.T, chunks []*schemas.BifrostStreamChunk) string {
	t.Helper()
	// A zero Config loads no transport plugins, so no chunk interceptor runs.
	return serveStreamingResponseWithConfig(t, &lib.Config{}, chunks)
}

// serveStreamingResponseWithConfig is serveStreamingResponse with the handler's Config
// supplied, so a test can load transport plugins whose chunk hook runs on the stream.
func serveStreamingResponseWithConfig(t *testing.T, config *lib.Config, chunks []*schemas.BifrostStreamChunk) string {
	t.Helper()
	return serveStreamingResponseWithSetup(t, config, nil, chunks)
}

// serveStreamingResponseWithSetup is serveStreamingResponseWithConfig with a hook that
// runs on the request context before the handler, e.g. to install a trace completer.
func serveStreamingResponseWithSetup(t *testing.T, config *lib.Config, setup func(*fasthttp.RequestCtx), chunks []*schemas.BifrostStreamChunk) string {
	t.Helper()
	return serveStreamingResponseWithClient(t, config, nil, setup, nil, chunks)
}

// serveStreamingResponseWithClient is serveStreamingResponseWithSetup with the handler's
// core client and a hook on the request's BifrostContext, for tests of send-back cost.
func serveStreamingResponseWithClient(t *testing.T, config *lib.Config, client *bifrost.Bifrost, setup func(*fasthttp.RequestCtx), ctxSetup func(*schemas.BifrostContext), chunks []*schemas.BifrostStreamChunk) string {
	t.Helper()

	handler := func(reqCtx *fasthttp.RequestCtx) {
		if setup != nil {
			setup(reqCtx)
		}
		h := &CompletionHandler{config: config, client: client}

		base, cancel := context.WithCancel(context.Background())
		bifrostCtx := schemas.NewBifrostContext(base, schemas.NoDeadline)
		if ctxSetup != nil {
			ctxSetup(bifrostCtx)
		}

		getStream := func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			stream := make(chan *schemas.BifrostStreamChunk, len(chunks))
			for _, chunk := range chunks {
				stream <- chunk
			}
			close(stream)
			return stream, nil
		}

		h.handleStreamingResponse(reqCtx, bifrostCtx, schemas.ChatCompletionStreamRequest, getStream, cancel)
	}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	go func() {
		_ = fasthttp.ServeConn(serverConn, handler)
	}()

	if err := clientConn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatalf("failed to set deadline: %v", err)
	}
	if _, err := clientConn.Write([]byte("POST /v1/chat/completions HTTP/1.1\r\nHost: test\r\n\r\n")); err != nil {
		t.Fatalf("failed to write request: %v", err)
	}

	br := bufio.NewReader(clientConn)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("failed to read response header: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	var body strings.Builder
	for {
		sizeLine, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("failed to read chunk size: %v", err)
		}
		var chunkSize int
		if _, err := fmt.Sscanf(strings.TrimSpace(sizeLine), "%x", &chunkSize); err != nil {
			t.Fatalf("failed to parse chunk size %q: %v", sizeLine, err)
		}
		if chunkSize == 0 {
			break
		}
		data := make([]byte, chunkSize+2) // +2 for the trailing CRLF
		for n := 0; n < len(data); {
			nn, err := br.Read(data[n:])
			if err != nil {
				t.Fatalf("failed to read chunk data: %v", err)
			}
			n += nn
		}
		body.Write(data[:chunkSize])
	}
	return body.String()
}

func contentChunk(text string) *schemas.BifrostStreamChunk {
	return &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			ID:     "chatcmpl-repro",
			Object: "chat.completion.chunk",
			Model:  "repro-model",
			Choices: []schemas.BifrostResponseChoice{{
				Index: 0,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
					Delta: &schemas.ChatStreamResponseChoiceDelta{Content: schemas.Ptr(text)},
				},
			}},
		},
	}
}

func truncationErrorChunk() *schemas.BifrostStreamChunk {
	statusCode := 502
	errorType := schemas.ProviderConnectionFailed
	return &schemas.BifrostStreamChunk{
		BifrostError: &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderStreamTruncated,
				Type:    &errorType,
			},
		},
	}
}

// A stream that ends on an error frame is already terminated for the client;
// appending [DONE] would report it as a clean finish.
func TestStreamingResponseSkipsDoneAfterErrorChunk(t *testing.T) {
	body := serveStreamingResponse(t, []*schemas.BifrostStreamChunk{
		contentChunk("partial answer"),
		truncationErrorChunk(),
	})

	if !strings.Contains(body, schemas.ErrProviderStreamTruncated) {
		t.Errorf("expected the error frame to reach the client, got:\n%s", body)
	}
	if strings.Contains(body, "data: [DONE]") {
		t.Errorf("[DONE] must not follow an error frame, got:\n%s", body)
	}
}

// Guard against over-suppression: a clean stream still gets its marker.
func TestStreamingResponseSendsDoneOnCleanStream(t *testing.T) {
	body := serveStreamingResponse(t, []*schemas.BifrostStreamChunk{
		contentChunk("hello"),
		contentChunk(" world"),
	})

	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("expected [DONE] to terminate a clean stream, got:\n%s", body)
	}
}

// panickingChunkPlugin is a transport plugin whose stream-chunk hook panics.
type panickingChunkPlugin struct{ fakeResponseHeadersPlugin }

func (p *panickingChunkPlugin) HTTPTransportStreamChunkHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	var resp *schemas.BifrostChatResponse
	_ = resp.ID // nil dereference, as a plugin bug would
	return chunk, nil
}

// A panic in a transport plugin's stream-chunk hook runs on the SSE producer goroutine,
// after the handler has returned and outside RecoveryMiddleware. It must end only this
// stream; without the producer's own recover it ends the process (and this test binary).
func TestStreamingResponsePluginChunkHookPanicIsRecovered(t *testing.T) {
	plugin := &panickingChunkPlugin{fakeResponseHeadersPlugin{name: "panicking-chunk-hook"}}
	config := responseHeadersTestConfig(t, plugin)

	body := serveStreamingResponseWithConfig(t, config, []*schemas.BifrostStreamChunk{
		contentChunk("first"),
		contentChunk("second"),
	})

	if strings.Contains(body, "data: [DONE]") {
		t.Errorf("a stream cut short by a panic must not end with [DONE], got:\n%s", body)
	}
	if !strings.Contains(body, "event: error\n") || !strings.Contains(body, lib.StreamProducerPanicMessage) {
		t.Errorf("a stream cut short by a panic must end with an error frame, got:\n%s", body)
	}
}

// In headers mode every frame on this stream drops extra_fields, and the error frame a
// recovered panic writes must follow the same rule as the other error frames.
func TestStreamingResponsePanicErrorFrameStripsExtraFieldsInHeadersMode(t *testing.T) {
	plugin := &panickingChunkPlugin{fakeResponseHeadersPlugin{name: "panicking-chunk-hook"}}
	config := responseHeadersTestConfig(t, plugin)

	body := serveStreamingResponseWithSetup(t, config, func(reqCtx *fasthttp.RequestCtx) {
		reqCtx.SetUserValue(lib.FastHTTPUserValueExtraFieldsInHeaders, true)
	}, []*schemas.BifrostStreamChunk{contentChunk("first")})

	if !strings.Contains(body, "event: error\n") || !strings.Contains(body, lib.StreamProducerPanicMessage) {
		t.Fatalf("expected the panic error frame, got:\n%s", body)
	}
	if strings.Contains(body, "extra_fields") {
		t.Errorf("headers mode must strip extra_fields from the panic error frame, got:\n%s", body)
	}
}

// streamBlockingPlugin stops every stream from its chunk hook with a structured error, as an
// Enterprise guardrail does when it blocks a buffered stream.
type streamBlockingPlugin struct {
	fakeResponseHeadersPlugin
	err *schemas.BifrostError
}

func (p *streamBlockingPlugin) HTTPTransportStreamChunkHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, _ *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return nil, &schemas.StreamInterceptionError{BifrostError: p.err}
}

// errorCostCatalog prices every error at a fixed total. ctx.CalculateErrorCostBreakdown only
// needs schemas.ErrorCostProvider from the catalog on the context.
type errorCostCatalog struct{ total float64 }

func (c errorCostCatalog) CalculateErrorCostBreakdown(*schemas.BifrostContext, *schemas.BifrostError) *schemas.BifrostCost {
	return &schemas.BifrostCost{AdditionalCost: c.total, TotalCost: c.total}
}

// The error a transport plugin raises to stop a stream replaces the rest of the stream,
// including any error chunk core priced, so with send-back cost on it must carry the cost
// itself, priced on a copy so the plugin's own error is left as it was.
func TestStreamingResponseInterceptionErrorCarriesSendBackCost(t *testing.T) {
	client, err := bifrost.Init(t.Context(), schemas.BifrostConfig{
		Account:      inferenceSetupAccount{url: "http://127.0.0.1:1"},
		Logger:       bifrost.NewDefaultLogger(schemas.LogLevelError),
		SendBackCost: true,
	})
	if err != nil {
		t.Fatalf("init bifrost: %v", err)
	}
	defer client.Shutdown()

	for _, tc := range []struct {
		name     string
		override *bool
		wantCost bool
	}{
		{name: "setting on", wantCost: true},
		{name: "turned off for the request", override: schemas.Ptr(false), wantCost: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blockErr := &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error:      &schemas.ErrorField{Message: "blocked by guardrail"},
			}
			plugin := &streamBlockingPlugin{fakeResponseHeadersPlugin{name: "stream-blocker"}, blockErr}
			config := responseHeadersTestConfig(t, plugin)

			body := serveStreamingResponseWithClient(t, config, client, nil, func(ctx *schemas.BifrostContext) {
				ctx.SetValue(schemas.BifrostContextKeyModelCatalog, errorCostCatalog{total: 0.0042})
				if tc.override != nil {
					ctx.SetValue(schemas.BifrostContextKeySendBackCost, *tc.override)
				}
			}, []*schemas.BifrostStreamChunk{contentChunk("first")})

			if !strings.Contains(body, "event: error\n") || !strings.Contains(body, "blocked by guardrail") {
				t.Fatalf("expected the plugin's error frame, got:\n%s", body)
			}
			if got := strings.Contains(body, `"total_cost":0.0042`); got != tc.wantCost {
				t.Errorf("error frame carries the cost = %v, want %v; body:\n%s", got, tc.wantCost, body)
			}
			if blockErr.ExtraFields.Cost != nil {
				t.Errorf("the plugin's own error must not be written, got cost %+v", blockErr.ExtraFields.Cost)
			}
		})
	}
}

// A plugin that buffers a stream stops it on the chunk core priced: the final chunk,
// whose cost covers the tokens the stream consumed, or an error chunk. The plugin's
// error replaces that chunk, so it carries that chunk's cost rather than a price of
// the plugin's own error, which has no usage.
func TestStreamingResponseInterceptionErrorKeepsTheReplacedChunksCost(t *testing.T) {
	client, err := bifrost.Init(t.Context(), schemas.BifrostConfig{
		Account:      inferenceSetupAccount{url: "http://127.0.0.1:1"},
		Logger:       bifrost.NewDefaultLogger(schemas.LogLevelError),
		SendBackCost: true,
	})
	if err != nil {
		t.Fatalf("init bifrost: %v", err)
	}
	defer client.Shutdown()

	finalChunk := contentChunk("final")
	finalChunk.BifrostChatResponse.ExtraFields.Cost = &schemas.BifrostCost{InputCost: 0.25, TotalCost: 0.25}
	errorChunk := &schemas.BifrostStreamChunk{BifrostError: &schemas.BifrostError{
		Error:       &schemas.ErrorField{Message: "upstream failed"},
		ExtraFields: schemas.BifrostErrorExtraFields{Cost: &schemas.BifrostCost{InputCost: 0.1, TotalCost: 0.1}},
	}}

	for name, tc := range map[string]struct {
		chunk *schemas.BifrostStreamChunk
		want  string
	}{
		"final chunk": {chunk: finalChunk, want: `"total_cost":0.25`},
		"error chunk": {chunk: errorChunk, want: `"total_cost":0.1`},
	} {
		t.Run(name, func(t *testing.T) {
			blockErr := &schemas.BifrostError{
				StatusCode: schemas.Ptr(400),
				Error:      &schemas.ErrorField{Message: "blocked by guardrail"},
			}
			config := responseHeadersTestConfig(t, &streamBlockingPlugin{fakeResponseHeadersPlugin{name: "stream-blocker"}, blockErr})

			body := serveStreamingResponseWithClient(t, config, client, nil, func(ctx *schemas.BifrostContext) {
				ctx.SetValue(schemas.BifrostContextKeyModelCatalog, errorCostCatalog{total: 0.0042})
			}, []*schemas.BifrostStreamChunk{tc.chunk})

			if !strings.Contains(body, "blocked by guardrail") || !strings.Contains(body, tc.want) {
				t.Fatalf("want the plugin's error frame with %s, got:\n%s", tc.want, body)
			}
			if blockErr.ExtraFields.Cost != nil {
				t.Errorf("the plugin's own error must not be written, got cost %+v", blockErr.ExtraFields.Cost)
			}
		})
	}
}

// A panic in the producer's deferred teardown (the trace completer runs observability
// plugins) happens after the producer's recover, so it is a new panic nothing else
// catches. It must be contained: the stream still ends normally for the client.
func TestStreamingResponseTeardownPanicIsRecovered(t *testing.T) {
	traceRan := make(chan struct{})
	body := serveStreamingResponseWithSetup(t, &lib.Config{}, func(reqCtx *fasthttp.RequestCtx) {
		reqCtx.SetUserValue(schemas.BifrostContextKeyTraceCompleter, func([]schemas.PluginLogEntry) {
			close(traceRan)
			panic("trace completer")
		})
	}, []*schemas.BifrostStreamChunk{contentChunk("hello")})

	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("a clean stream still ends with [DONE], got:\n%s", body)
	}
	select {
	case <-traceRan:
	case <-time.After(5 * time.Second):
		t.Fatal("trace completer never ran")
	}
}
