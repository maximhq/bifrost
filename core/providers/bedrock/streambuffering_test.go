package bedrock

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeEventStreamEvent encodes a well-formed AWS EventStream "event" frame
// (":message-type" == "event") carrying the given JSON payload into w. Used to
// emit normal streaming chunks (messageStart / contentBlockDelta) that the
// Bedrock decode loop forwards to the response channel.
func writeEventStreamEvent(t *testing.T, w io.Writer, eventType string, payload []byte) {
	t.Helper()
	enc := eventstream.NewEncoder()
	headers := eventstream.Headers{
		{Name: ":message-type", Value: eventstream.StringValue("event")},
		{Name: ":event-type", Value: eventstream.StringValue(eventType)},
		{Name: ":content-type", Value: eventstream.StringValue("application/json")},
	}
	require.NoError(t, enc.Encode(w, eventstream.Message{Headers: headers, Payload: payload}),
		"failed to encode EventStream event frame")
}

// TestChatCompletionStream_StreamsIncrementally_NotBuffered reproduces issue #4542:
// streaming Bedrock responses arrive in a single end-of-stream burst instead of
// incrementally, because Go's net/http transport auto-negotiates gzip and the
// gzip-compressed upstream buffers until the stream completes.
//
// The fake Bedrock server branches on the inbound Accept-Encoding:
//   - gzip (Go's auto default, pre-fix): writes events into a gzip.Writer that is
//     NOT flushed and blocks before Close, so the client receives nothing until the
//     whole upstream stream finishes — TTFB == total generation time.
//   - identity (post-fix): flushes the first event immediately, then blocks, so the
//     client gets the first chunk well before the upstream completes.
//
// The test asserts the first chunk arrives before the upstream is released. It is
// deterministic (channel-gated, no wall-clock sleeps): pre-fix the first read times
// out; post-fix it succeeds.
func TestChatCompletionStream_StreamsIncrementally_NotBuffered(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }

	roleStart := []byte(`{"role":"assistant"}`)
	textDelta := []byte(`{"delta":{"text":"Hello"},"contentBlockIndex":0}`)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptEncoding := r.Header.Get("Accept-Encoding")
		flusher, ok := w.(http.Flusher)
		require.True(t, ok, "test server ResponseWriter must support Flush")

		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		if strings.Contains(acceptEncoding, "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
		}
		w.WriteHeader(http.StatusOK)
		// Flush headers immediately so makeStreamingRequest's client.Do returns and
		// the decode goroutine begins reading the body. The body is what we withhold.
		flusher.Flush()

		if strings.Contains(acceptEncoding, "gzip") {
			// Pre-fix path: Bedrock compresses the eventstream. gzip.Writer buffers
			// small writes in memory and we never Flush, so no complete gzip block
			// reaches the wire until Close(). We block before Close, so the client's
			// (transparently decompressing) body Read starves — no chunk until the
			// upstream stream completes. This is the issue #4542 symptom.
			gz := gzip.NewWriter(w)
			writeEventStreamEvent(t, gz, "messageStart", roleStart)
			<-release // hold the entire (still-buffered) stream
			writeEventStreamEvent(t, gz, "contentBlockDelta", textDelta)
			_ = gz.Close()
			flusher.Flush()
			return
		}

		// Post-fix path: identity encoding, raw eventstream, flush per event.
		writeEventStreamEvent(t, w, "messageStart", roleStart)
		flusher.Flush()
		<-release // first chunk already on the wire; hold the rest
		writeEventStreamEvent(t, w, "contentBlockDelta", textDelta)
		flusher.Flush()
	}))
	defer ts.Close()
	// Must unblock the handler before ts.Close(), which waits for in-flight requests.
	defer closeRelease()

	provider := newTestProviderWithServer(t, ts)
	ctx := testBedrockCtx()
	key := testBedrockKey()

	streamChan, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHookRunner, nil, key, testChatRequest())
	require.Nil(t, bifrostErr, "stream setup should not error")
	require.NotNil(t, streamChan)

	// The first chunk MUST arrive while the upstream is still held open. If the
	// response is buffered (issue #4542), nothing arrives until release is closed,
	// so this read times out.
	select {
	case chunk, ok := <-streamChan:
		require.True(t, ok, "stream closed before delivering any chunk")
		require.NotNil(t, chunk)
		require.Nil(t, chunk.BifrostError, "first chunk should be data, not an error: %+v", chunk.BifrostError)
	case <-time.After(2 * time.Second):
		t.Fatal("first chunk not received before upstream completed — response is buffered server-side (issue #4542)")
	}

	// Release the rest of the upstream and drain.
	closeRelease()
	for range streamChan {
	}
}

// TestMakeStreamingRequest_SendsIdentityAcceptEncoding is the deterministic
// mechanism guard for issue #4542: the Bedrock streaming request must send
// Accept-Encoding: identity so Go's net/http transport does not auto-negotiate
// gzip (which buffers the eventstream). Pre-fix, Go auto-adds "gzip".
func TestMakeStreamingRequest_SendsIdentityAcceptEncoding(t *testing.T) {
	var mu sync.Mutex
	var gotAcceptEncoding string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAcceptEncoding = r.Header.Get("Accept-Encoding")
		mu.Unlock()

		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		writeEventStreamEvent(t, w, "messageStart", []byte(`{"role":"assistant"}`))
		writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"text":"Hi"},"contentBlockIndex":0}`))
	}))
	defer ts.Close()

	provider := newTestProviderWithServer(t, ts)
	ctx := testBedrockCtx()
	key := testBedrockKey()

	streamChan, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHookRunner, nil, key, testChatRequest())
	require.Nil(t, bifrostErr)
	require.NotNil(t, streamChan)
	for range streamChan { // drain so the request fully completes
	}

	mu.Lock()
	got := gotAcceptEncoding
	mu.Unlock()
	assert.Equal(t, "identity", got,
		"Bedrock streaming request must send Accept-Encoding: identity to prevent net/http auto-gzip buffering (issue #4542)")
}
