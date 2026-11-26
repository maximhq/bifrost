package handlers

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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
	return serveStreamingResponseWithSetup(t, config, false, nil, chunks)
}

// serveStreamingResponseWithSetup is serveStreamingResponseWithConfig with a hook that
// runs on the request context before the handler, e.g. to install a trace completer.
func serveStreamingResponseWithSetup(t *testing.T, config *lib.Config, includeUsage bool, setup func(*fasthttp.RequestCtx), chunks []*schemas.BifrostStreamChunk) string {
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

		h.handleStreamingResponse(reqCtx, bifrostCtx, schemas.ChatCompletionStreamRequest, includeUsage, getStream, cancel)
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

	body := serveStreamingResponseWithSetup(t, config, false, func(reqCtx *fasthttp.RequestCtx) {
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
	body := serveStreamingResponseWithSetup(t, &lib.Config{}, false, func(reqCtx *fasthttp.RequestCtx) {
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

// Pin the neutral /v1/chat/completions writer independently of SDK integrations.
func TestStreamingResponseSeparateUsageChunk(t *testing.T) {
	usage := &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	terminal := providerUtils.CreateBifrostChatCompletionChunkResponse("chatcmpl-repro", usage, schemas.Ptr("stop"), 1, "repro-model", 123)
	body := serveStreamingResponseWithSetup(t, &lib.Config{}, true, nil, []*schemas.BifrostStreamChunk{contentChunk("hello"), {BifrostChatResponse: terminal}})
	t.Logf("wire SSE:\n%s", body)
	var events []string
	for _, line := range strings.Split(body, "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			events = append(events, data)
		}
	}
	require.Len(t, events, 4, "content, finish, usage, DONE: %s", body)
	require.Equal(t, "stop", gjson.Get(events[1], "choices.0.finish_reason").String())
	require.Equal(t, "null", gjson.Get(events[1], "usage").Raw)
	require.Equal(t, "[]", gjson.Get(events[2], "choices").Raw)
	require.Equal(t, int64(15), gjson.Get(events[2], "usage.total_tokens").Int())
	require.Equal(t, "[DONE]", events[3])
	require.Same(t, usage, terminal.Usage, "wire normalization must not mutate plugin/logging data")
	require.Len(t, terminal.Choices, 1)
}

func TestNativeChatStreamClientUsageOption(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"test","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}

data: {"id":"test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}

data: [DONE]

`)
	}))
	defer upstream.Close()
	client, err := bifrost.Init(t.Context(), schemas.BifrostConfig{
		Account: inferenceSetupAccount{url: upstream.URL}, Logger: bifrost.NewNoOpLogger(),
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)
	h := NewInferenceHandler(nil, client, &lib.Config{ClientConfig: &configstore.ClientConfig{}})
	for _, tc := range []struct {
		name, options string
		split         bool
	}{
		{"omitted", "", false},
		{"false", `,"stream_options":{"include_usage":false}`, false},
		{"true", `,"stream_options":{"include_usage":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ctx fasthttp.RequestCtx
			ctx.Request.Header.SetMethod(fasthttp.MethodPost)
			ctx.Request.SetRequestURI("/v1/chat/completions")
			ctx.Request.Header.SetContentType("application/json")
			ctx.Request.SetBodyString(`{"model":"openai/gpt-test","messages":[{"role":"user","content":"hi"}],"stream":true` + tc.options + "}")
			h.chatCompletion(&ctx)
			body := string(ctx.Response.Body())
			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "%s", body)
			frames := strings.Split(strings.TrimSpace(body), "\n\n")
			require.GreaterOrEqual(t, len(frames), 3, "%s", body)
			require.Equal(t, "data: [DONE]", frames[len(frames)-1])
			terminal := strings.TrimPrefix(frames[len(frames)-2], "data: ")
			require.Equal(t, int64(15), gjson.Get(terminal, "usage.total_tokens").Int(), "%s", body)
			if tc.split {
				require.Equal(t, "[]", gjson.Get(terminal, "choices").Raw)
				finish := strings.TrimPrefix(frames[len(frames)-3], "data: ")
				require.Equal(t, "stop", gjson.Get(finish, "choices.0.finish_reason").String())
			} else {
				require.Equal(t, "stop", gjson.Get(terminal, "choices.0.finish_reason").String())
			}
		})
	}
}
