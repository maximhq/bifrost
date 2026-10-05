package openai

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

var _ schemas.LiveProvider = (*OpenAIProvider)(nil)

func TestLiveWebSocketURL(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{networkConfig: schemas.NetworkConfig{BaseURL: schemas.NewSecretVar("https://api.openai.com")}}
	cases := []struct {
		kind      schemas.LiveConnectionKind
		sessionID string
		want      string
	}{
		{schemas.LiveConnectionPrimary, "", "wss://api.openai.com/v1/live/sessions"},
		{schemas.LiveConnectionSideband, "live_123", "wss://api.openai.com/v1/live/sessions/live_123/attach"},
		{schemas.LiveConnectionFork, "live_123", "wss://api.openai.com/v1/live/sessions/live_123/fork"},
	}
	for _, tc := range cases {
		got, err := provider.LiveWebSocketURL(schemas.Key{}, tc.kind, tc.sessionID)
		if err != nil || got != tc.want {
			t.Fatalf("LiveWebSocketURL(%s, %q) = %q, %v; want %q", tc.kind, tc.sessionID, got, err, tc.want)
		}
	}

	local := &OpenAIProvider{networkConfig: schemas.NetworkConfig{BaseURL: schemas.NewSecretVar("http://localhost:9000")}}
	if got, err := local.LiveWebSocketURL(schemas.Key{}, schemas.LiveConnectionPrimary, ""); err != nil || got != "ws://localhost:9000/v1/live/sessions" {
		t.Fatalf("LiveWebSocketURL() = %q, %v", got, err)
	}
}

func TestLiveWebSocketURLRejectsUnsafeSessionID(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{networkConfig: schemas.NetworkConfig{BaseURL: schemas.NewSecretVar("https://api.openai.com")}}
	for _, sessionID := range []string{"", "..", "live_1/../../responses", "live_1%2Ffork", "live_1?x=1", "live_1#frag"} {
		for _, kind := range []schemas.LiveConnectionKind{schemas.LiveConnectionSideband, schemas.LiveConnectionFork} {
			if got, err := provider.LiveWebSocketURL(schemas.Key{}, kind, sessionID); err == nil {
				t.Fatalf("LiveWebSocketURL(%s, %q) = %q, want error", kind, sessionID, got)
			}
		}
	}
	if got, err := provider.LiveWebSocketURL(schemas.Key{}, schemas.LiveConnectionKind("bogus"), "live_123"); err == nil {
		t.Fatalf("LiveWebSocketURL(bogus) = %q, want error", got)
	}
}

func TestLiveWebSocketURLHonorsAllowedRequests(t *testing.T) {
	t.Parallel()

	blocked := &OpenAIProvider{
		networkConfig:        schemas.NetworkConfig{BaseURL: schemas.NewSecretVar("https://api.openai.com")},
		customProviderConfig: &schemas.CustomProviderConfig{AllowedRequests: &schemas.AllowedRequests{Realtime: true}},
	}
	if got, err := blocked.LiveWebSocketURL(schemas.Key{}, schemas.LiveConnectionPrimary, ""); err == nil {
		t.Fatalf("LiveWebSocketURL() = %q, want unsupported operation error", got)
	}

	allowed := &OpenAIProvider{
		networkConfig:        schemas.NetworkConfig{BaseURL: schemas.NewSecretVar("https://api.openai.com")},
		customProviderConfig: &schemas.CustomProviderConfig{AllowedRequests: &schemas.AllowedRequests{Live: true}},
	}
	if _, err := allowed.LiveWebSocketURL(schemas.Key{}, schemas.LiveConnectionPrimary, ""); err != nil {
		t.Fatalf("LiveWebSocketURL() error = %v", err)
	}
}

func TestLiveHeaders(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{networkConfig: schemas.NetworkConfig{ExtraHeaders: map[string]string{"X-Extra": "1"}}}
	headers, err := provider.LiveHeaders(nil, schemas.Key{Value: *schemas.NewSecretVar("sk-test")})
	if err != nil {
		t.Fatalf("LiveHeaders() error = %v", err)
	}
	if headers["Authorization"] != "Bearer sk-test" || headers["X-Extra"] != "1" {
		t.Fatalf("LiveHeaders() = %v", headers)
	}
}

func TestCreateLiveWebRTCSession(t *testing.T) {
	t.Parallel()

	body := []byte(`{"session":{"model":"gpt-live-1"},"transport":{"type":"webrtc","sdp":"v=0 offer"}}`)
	var status int
	var reply string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/live/sessions" || r.Header.Get("Authorization") != "Bearer sk-test" ||
			r.Header.Get("Content-Type") != "application/json" || string(got) != string(body) {
			t.Errorf("unexpected request %s %s auth=%q type=%q body=%s", r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)

	provider := &OpenAIProvider{client: &fasthttp.Client{}, networkConfig: schemas.NetworkConfig{BaseURL: schemas.NewSecretVar(srv.URL)}}
	key := schemas.Key{Value: *schemas.NewSecretVar("sk-test")}
	newCtx := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}

	status, reply = http.StatusCreated, `{"session":{"id":"live_123"},"transport":{"type":"webrtc","sdp":"v=0 answer"}}`
	created, bifrostErr := provider.CreateLiveWebRTCSession(newCtx(), key, body)
	if bifrostErr != nil {
		t.Fatalf("CreateLiveWebRTCSession() error = %v", bifrostErr.Error)
	}
	if created.Session.ID != "live_123" || created.Transport.SDP != "v=0 answer" {
		t.Fatalf("CreateLiveWebRTCSession() = %+v", created)
	}

	status, reply = http.StatusBadRequest, `{"error":{"type":"invalid_request_error","message":"Invalid SDP offer."}}`
	_, bifrostErr = provider.CreateLiveWebRTCSession(newCtx(), key, body)
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest ||
		bifrostErr.ExtraFields.RequestType != schemas.LiveRequest || bifrostErr.Error.Message != "Invalid SDP offer." {
		t.Fatalf("upstream error = %+v", bifrostErr)
	}

	status, reply = http.StatusCreated, `{"session":{"id":"live_123"}}`
	if _, bifrostErr = provider.CreateLiveWebRTCSession(newCtx(), key, body); bifrostErr == nil {
		t.Fatal("a create response without an SDP answer must be an error")
	}

	blocked := &OpenAIProvider{client: &fasthttp.Client{}, networkConfig: schemas.NetworkConfig{BaseURL: schemas.NewSecretVar(srv.URL)},
		customProviderConfig: &schemas.CustomProviderConfig{AllowedRequests: &schemas.AllowedRequests{Realtime: true}}}
	if _, bifrostErr = blocked.CreateLiveWebRTCSession(newCtx(), key, body); bifrostErr == nil {
		t.Fatal("allowed_requests without live must block session create")
	}
}

func TestLiveSessionContent(t *testing.T) {
	t.Parallel()

	wav := []byte("RIFF....WAVEfmt ")
	var status int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/live/sessions/live_123/content" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("unexpected request %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		if status == http.StatusOK {
			w.Header().Set("Content-Type", "audio/wav")
			w.WriteHeader(status)
			_, _ = w.Write(wav)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"Session not found."}}`))
	}))
	t.Cleanup(srv.Close)

	provider := &OpenAIProvider{client: &fasthttp.Client{}, streamingClient: &fasthttp.Client{}, networkConfig: schemas.NetworkConfig{BaseURL: schemas.NewSecretVar(srv.URL)}}
	key := schemas.Key{Value: *schemas.NewSecretVar("sk-test")}
	newCtx := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}

	status = http.StatusOK
	content, bifrostErr := provider.LiveSessionContent(newCtx(), key, "live_123")
	if bifrostErr != nil {
		t.Fatalf("LiveSessionContent() error = %v", bifrostErr.Error)
	}
	body, err := io.ReadAll(content.Body)
	if err != nil {
		t.Fatalf("read recording: %v", err)
	}
	if err := content.Body.Close(); err != nil {
		t.Fatalf("close recording: %v", err)
	}
	if content.SessionID != "live_123" || content.ContentType != "audio/wav" || content.ContentLength != int64(len(wav)) || string(body) != string(wav) {
		t.Fatalf("LiveSessionContent() = %q %q %d %q", content.SessionID, content.ContentType, content.ContentLength, body)
	}

	status = http.StatusNotFound
	_, bifrostErr = provider.LiveSessionContent(newCtx(), key, "live_123")
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusNotFound ||
		bifrostErr.ExtraFields.RequestType != schemas.LiveRequest || bifrostErr.Error.Message != "Session not found." {
		t.Fatalf("upstream error = %+v", bifrostErr)
	}

	if _, bifrostErr = provider.LiveSessionContent(newCtx(), key, "../files"); bifrostErr == nil {
		t.Fatal("an unsafe session id must be refused before any request is made")
	}
}

// TestLiveSessionContentStalledUpstreamTimesOut: a recording whose upstream stops mid-body must
// fail after the stream idle timeout instead of holding the download open.
func TestLiveSessionContentStalledUpstreamTimesOut(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("RIFF"))
		w.(http.Flusher).Flush()
		<-release
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	provider := &OpenAIProvider{client: &fasthttp.Client{}, streamingClient: &fasthttp.Client{}, networkConfig: schemas.NetworkConfig{BaseURL: schemas.NewSecretVar(srv.URL)}}
	key := schemas.Key{Value: *schemas.NewSecretVar("sk-test")}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyStreamIdleTimeout, 200*time.Millisecond)

	done := make(chan error, 1)
	go func() {
		content, bifrostErr := provider.LiveSessionContent(ctx, key, "live_123")
		if bifrostErr != nil {
			done <- errors.New(bifrostErr.Error.Message)
			return
		}
		done <- readLiveContent(content)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a stalled upstream must surface an error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a stalled upstream held the download open")
	}
}

// readLiveContent drains a recording the way the transport does.
func readLiveContent(content *schemas.LiveContentResponse) error {
	defer content.Body.Close()
	_, err := io.ReadAll(content.Body)
	return err
}

// TestLiveSessionContentStreamsWithoutBuffering: a recording is handed over as a stream, so draining
// a large one allocates a few buffers, not the recording. A buffered implementation allocates at
// least the whole body.
func TestLiveSessionContentStreamsWithoutBuffering(t *testing.T) {
	const recordingBytes = 64 << 20
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Content-Length", strconv.Itoa(recordingBytes))
		w.WriteHeader(http.StatusOK)
		chunk := make([]byte, 1<<20)
		for written := 0; written < recordingBytes; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	provider := &OpenAIProvider{client: &fasthttp.Client{}, streamingClient: &fasthttp.Client{}, networkConfig: schemas.NetworkConfig{BaseURL: schemas.NewSecretVar(srv.URL)}}
	key := schemas.Key{Value: *schemas.NewSecretVar("sk-test")}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	content, bifrostErr := provider.LiveSessionContent(ctx, key, "live_123")
	if bifrostErr != nil {
		t.Fatalf("LiveSessionContent() error = %v", bifrostErr.Error)
	}
	drained, err := io.CopyBuffer(io.Discard, content.Body, make([]byte, 64<<10))
	if err != nil {
		t.Fatalf("drain recording: %v", err)
	}
	_ = content.Body.Close()
	runtime.ReadMemStats(&after)

	if drained != recordingBytes || content.ContentLength != recordingBytes {
		t.Fatalf("drained %d bytes, content length %d, want %d", drained, content.ContentLength, recordingBytes)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated > 16<<20 {
		t.Fatalf("draining a %d MiB recording allocated %d MiB; the body is being buffered", recordingBytes>>20, allocated>>20)
	}
}

// TestLiveSessionContentGzipBodyHasNoStatedLength: a gzip-encoded recording is decoded on the way
// through, so the compressed Content-Length must not be reported as the body's length.
func TestLiveSessionContentGzipBodyHasNoStatedLength(t *testing.T) {
	t.Parallel()

	wav := []byte("RIFF....WAVEfmt ")
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, _ = gz.Write(wav)
	_ = gz.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(compressed.Len()))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(compressed.Bytes())
	}))
	t.Cleanup(srv.Close)

	provider := &OpenAIProvider{client: &fasthttp.Client{}, streamingClient: &fasthttp.Client{}, networkConfig: schemas.NetworkConfig{BaseURL: schemas.NewSecretVar(srv.URL)}}
	key := schemas.Key{Value: *schemas.NewSecretVar("sk-test")}
	content, bifrostErr := provider.LiveSessionContent(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, "live_123")
	if bifrostErr != nil {
		t.Fatalf("LiveSessionContent() error = %v", bifrostErr.Error)
	}
	body, err := io.ReadAll(content.Body)
	_ = content.Body.Close()
	if err != nil || string(body) != string(wav) {
		t.Fatalf("decoded body = %q err = %v, want the recording", body, err)
	}
	if content.ContentLength != 0 {
		t.Fatalf("ContentLength = %d for a gzip body, want 0 (unknown once decoded)", content.ContentLength)
	}
}
