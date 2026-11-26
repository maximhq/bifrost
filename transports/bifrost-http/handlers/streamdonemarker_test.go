package handlers

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
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

	handler := func(reqCtx *fasthttp.RequestCtx) {
		if setup != nil {
			setup(reqCtx)
		}
		h := &CompletionHandler{config: config}

		base, cancel := context.WithCancel(context.Background())
		bifrostCtx := schemas.NewBifrostContext(base, schemas.NoDeadline)

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

// TestStreamIdleHarnessFixture runs the provider-harness assertions over HTTP
// using the real provider loops and gateway SSE writer. It uses only loopback
// listeners and is opt-in so the default Go suite does not require Node.
func TestStreamIdleHarnessFixture(t *testing.T) {
	if os.Getenv("BIFROST_TEST_STREAM_IDLE_HARNESS") != "1" {
		t.Skip("set BIFROST_TEST_STREAM_IDLE_HARNESS=1 to run the loopback collection fixture")
	}
	const idle = time.Second
	const responseID = "resp_idle_fixture"
	responsePath := "/v1/responses/" + responseID
	fixtureCtx, stop := context.WithTimeout(t.Context(), 25*time.Second)
	pauseStarted := make(chan struct{})
	secondWritten := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `data: {"id":"chatcmpl-idle","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"before-pause"}}]}`+"\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-pauseStarted:
			case <-r.Context().Done():
				return
			case <-fixtureCtx.Done():
				return
			}
			if _, err := io.WriteString(w, `data: {"id":"chatcmpl-idle","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"after-pause"}}]}`+"\n\n"+
				`data: {"id":"chatcmpl-idle","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n"); err == nil {
				w.(http.Flusher).Flush()
				close(secondWritten)
			}
		case responsePath:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_idle_fixture","model":"gpt-4o-mini","status":"in_progress"}}`+"\n\n"+
				`data: {"type":"error","sequence_number":1,"message":"terminal failure","code":"bifrost_terminal_error_fixture"}`+"\n\n")
			w.(http.Flusher).Flush()
			// Wait for the gateway to discard the incomplete body. A client
			// timeout cannot satisfy the assertion that the response completes.
			select {
			case <-r.Context().Done():
			case <-fixtureCtx.Done():
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	defer stop()

	provider := openai.NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: upstream.URL, StreamIdleTimeoutInSeconds: int(idle.Seconds())},
	}, &mockLogger{})
	key := schemas.Key{Value: *schemas.NewSecretVar("stream-idle-fixture")}
	transport := &CompletionHandler{config: &lib.Config{}}
	gateway := &fasthttp.Server{Handler: func(req *fasthttp.RequestCtx) {
		parent, cancel := context.WithCancel(fixtureCtx)
		ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
		switch string(req.Request.Header.Peek("x-bf-test-fixture")) {
		case "stream-idle-pause-v1":
			var payload struct {
				Model    string                `json:"model"`
				Messages []schemas.ChatMessage `json:"messages"`
				Stream   bool                  `json:"stream"`
			}
			if !req.IsPost() || string(req.Path()) != "/v1/chat/completions" ||
				schemas.Unmarshal(req.PostBody(), &payload) != nil || !payload.Stream || payload.Model != "openai/gpt-4o-mini" {
				cancel()
				req.Error("invalid chat fixture request", http.StatusBadRequest)
				return
			}
			paused := false
			hook := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				if paused || result == nil || result.ChatResponse == nil {
					return result, err
				}
				paused = true
				// The provider calls this hook synchronously after reading the
				// first event. Its loop cannot start the next Read until we return.
				start := time.Now()
				close(pauseStarted)
				timer := time.NewTimer(3 * idle)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					return result, err
				}
				select {
				case <-secondWritten:
					result.ChatResponse.ExtraFields.RawResponse = map[string]any{
						"stream_idle_pause_fixture": map[string]any{
							"version":                          1,
							"paused_in_provider_post_hook":     true,
							"idle_timeout_ms":                  providerUtils.GetStreamIdleTimeout(ctx).Milliseconds(),
							"pause_ms":                         time.Since(start).Milliseconds(),
							"second_chunk_ready_before_resume": true,
						},
					}
				case <-ctx.Done():
				}
				return result, err
			}
			transport.handleStreamingResponse(req, ctx, schemas.ChatCompletionStreamRequest, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
				return provider.ChatCompletionStream(ctx, hook, nil, key, &schemas.BifrostChatRequest{
					Provider: schemas.OpenAI, Model: "gpt-4o-mini", Input: payload.Messages,
				})
			}, cancel)
		case "stream-terminal-error-v1":
			if !req.IsGet() || string(req.Path()) != responsePath || string(req.QueryArgs().Peek("stream")) != "true" {
				cancel()
				req.Error("invalid Responses fixture request", http.StatusBadRequest)
				return
			}
			hook := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				if err != nil {
					// The SDK post-hook runner sets the request type before the HTTP writer sees the error.
					err.ExtraFields.RequestType = schemas.ResponsesRetrieveStreamRequest
					timer := time.NewTimer(3 * idle)
					defer timer.Stop()
					select {
					case <-timer.C:
					case <-ctx.Done():
					}
				}
				return result, err
			}
			transport.handleStreamingResponse(req, ctx, schemas.ResponsesRetrieveStreamRequest, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
				return provider.ResponsesRetrieveStream(ctx, hook, nil, key, &schemas.BifrostResponsesRetrieveRequest{
					Provider: schemas.OpenAI, ResponseID: responseID,
				})
			}, cancel)
		default:
			cancel()
			req.Error("unknown stream fixture", http.StatusNotFound)
		}
	}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() { _ = gateway.Serve(listener) }()
	defer func() {
		stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := gateway.ShutdownWithContext(shutdownCtx); err != nil {
			t.Errorf("fixture shutdown: %v", err)
		}
	}()

	cmd := exec.CommandContext(fixtureCtx, "node", "../../../tests/e2e/api/collections/collection-scripts.test.mjs")
	cmd.Env = append(os.Environ(), "BIFROST_STREAM_IDLE_FIXTURE_URL=http://"+listener.Addr().String())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("live fixture collection assertions: %v\n%s", err, output)
	}
	t.Logf("%s", output)
}
