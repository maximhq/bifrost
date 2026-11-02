package sagemaker

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

type seenRequest struct {
	path   string
	header http.Header
	body   string
}

// fakeRuntimeHost answers every request with handler and records what arrived.
func fakeRuntimeHost(t *testing.T, handler http.HandlerFunc) (*httptest.Server, func() []seenRequest) {
	t.Helper()
	var (
		mu   sync.Mutex
		seen []seenRequest
	)
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, seenRequest{path: r.URL.Path, header: r.Header.Clone(), body: string(body)})
		mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, func() []seenRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenRequest(nil), seen...)
	}
}

func newPassthroughProvider(t *testing.T) *SageMakerProvider {
	t.Helper()
	config := &schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{
		DefaultRequestTimeoutInSeconds: 5,
		StreamIdleTimeoutInSeconds:     2,
		InsecureSkipVerify:             true,
		AllowPrivateNetwork:            true,
	}}
	provider, err := NewSageMakerProvider(config, noopLogger{})
	require.NoError(t, err)
	return provider
}

func passthroughKey(ts *httptest.Server) schemas.Key {
	key := discoveryKey("*")
	key.SageMakerKeyConfig.Endpoints = &schemas.SageMakerEndpoints{Runtime: schemas.NewSecretVar(strings.TrimPrefix(ts.URL, "https://"))}
	return key
}

func invokeRequest(path, body string, headers map[string]string) *schemas.BifrostPassthroughRequest {
	return &schemas.BifrostPassthroughRequest{Provider: schemas.SageMaker, Method: http.MethodPost, Path: path, Body: []byte(body), SafeHeaders: headers}
}

func passthroughCtx(t *testing.T) *schemas.BifrostContext {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	t.Cleanup(ctx.Cancel)
	return ctx
}

func noHooks(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return resp, bErr
}

// eventStream encodes PayloadPart events, then an optional exception, as SageMaker frames them.
func eventStream(t *testing.T, parts []string, exception string) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := eventstream.NewEncoder()
	for _, p := range parts {
		msg := eventstream.Message{Payload: []byte(p)}
		msg.Headers.Set(":message-type", eventstream.StringValue("event"))
		msg.Headers.Set(":event-type", eventstream.StringValue("PayloadPart"))
		require.NoError(t, enc.Encode(&buf, msg))
	}
	if exception != "" {
		msg := eventstream.Message{Payload: []byte(`{"Message":"container crashed"}`)}
		msg.Headers.Set(":message-type", eventstream.StringValue("exception"))
		msg.Headers.Set(":exception-type", eventstream.StringValue(exception))
		require.NoError(t, enc.Encode(&buf, msg))
	}
	return buf.Bytes()
}

func drainPassthrough(t *testing.T, ch chan *schemas.BifrostStreamChunk) (string, *schemas.BifrostError) {
	t.Helper()
	var body strings.Builder
	timeout := time.After(10 * time.Second)
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return body.String(), nil
			}
			if c.BifrostError != nil {
				return body.String(), c.BifrostError
			}
			if c.BifrostPassthroughResponse != nil {
				body.Write(c.Body)
			}
		case <-timeout:
			t.Fatal("timed out waiting for the passthrough stream")
		}
	}
}

func TestPassthroughRouteMatching(t *testing.T) {
	cases := []struct {
		method, path string
		model        string
		streaming    bool
	}{
		{http.MethodPost, "/endpoints/my-ep/invocations", "my-ep", false},
		{http.MethodPost, "/endpoints/my-ep/invocations-response-stream", "my-ep", true},
		{http.MethodPost, "/endpoints/my-ep/inference-components/my-ic/invocations", "my-ep/my-ic", false},
		{http.MethodPost, "/endpoints/my-ep/inference-components/my-ic/invocations-response-stream", "my-ep/my-ic", true},
		// Refused: other methods, operations, the native OpenAI route, and malformed names.
		{http.MethodGet, "/endpoints/my-ep/invocations", "", false},
		{http.MethodPost, "/endpoints/my-ep/openai/v1/chat/completions", "", false},
		{http.MethodPost, "/endpoints/my-ep", "", false},
		{http.MethodPost, "/endpoints/-bad/invocations", "", false},
		{http.MethodPost, "/endpoints/my-ep/inference-components/../invocations", "", false},
		{http.MethodPost, "/models/x/invocations", "", false},
	}
	for _, tc := range cases {
		route, ok := matchPassthroughRoute(tc.method, tc.path)
		require.Equal(t, tc.model != "", ok, "%s %s", tc.method, tc.path)
		if tc.method == http.MethodPost {
			require.Equal(t, tc.model, PassthroughModel(tc.path), tc.path)
		}
		require.Equal(t, tc.streaming, ok && route.streaming, tc.path)
		require.Equal(t, tc.streaming, IsPassthroughStreamPath(tc.method, tc.path), tc.path)
	}
}

func TestPassthroughSignsAndForwardsInvokeEndpoint(t *testing.T) {
	ts, seen := fakeRuntimeHost(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Amzn-Invoked-Production-Variant", "AllTraffic")
		_, _ = w.Write([]byte(`[[0.1,0.2]]`))
	})
	provider := newPassthroughProvider(t)

	resp, bErr := provider.Passthrough(passthroughCtx(t), passthroughKey(ts), invokeRequest(
		"/endpoints/my-ep/inference-components/my-ic/invocations", `{"inputs":"hi"}`,
		map[string]string{"content-type": "application/json", "X-Amzn-SageMaker-Custom-Attributes": "trace=1", "Authorization": "Bearer caller", "X-Amz-Date": "spoofed"}))
	require.Nil(t, bErr, "%+v", bErr)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, `[[0.1,0.2]]`, string(resp.Body))
	require.Equal(t, "AllTraffic", resp.Headers["X-Amzn-Invoked-Production-Variant"])

	got := seen()
	require.Len(t, got, 1)
	require.Equal(t, "/endpoints/my-ep/invocations", got[0].path, "the component travels in a header, not the path")
	require.Equal(t, "my-ic", got[0].header.Get("X-Amzn-SageMaker-Inference-Component"))
	require.Equal(t, "trace=1", got[0].header.Get("X-Amzn-SageMaker-Custom-Attributes"))
	require.Equal(t, `{"inputs":"hi"}`, got[0].body)
	auth := got[0].header.Get("Authorization")
	require.True(t, strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDTEST/"), auth)
	require.Contains(t, auth, "/us-east-1/sagemaker/aws4_request")
	require.NotEqual(t, "spoofed", got[0].header.Get("X-Amz-Date"), "caller x-amz headers are never forwarded")
}

func TestPassthroughForwardsContainerErrorsVerbatim(t *testing.T) {
	ts, _ := fakeRuntimeHost(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusFailedDependency)
		_, _ = w.Write([]byte(`{"ErrorCode":"CLIENT_ERROR_FROM_MODEL","Message":"bad input"}`))
	})
	resp, bErr := newPassthroughProvider(t).Passthrough(passthroughCtx(t), passthroughKey(ts), invokeRequest("/endpoints/my-ep/invocations", `{}`, nil))
	require.Nil(t, bErr, "%+v", bErr)
	require.Equal(t, http.StatusFailedDependency, resp.StatusCode)
	require.Contains(t, string(resp.Body), "CLIENT_ERROR_FROM_MODEL")
}

func TestPassthroughRefusals(t *testing.T) {
	ts, seen := fakeRuntimeHost(t, func(w http.ResponseWriter, _ *http.Request) {})
	provider := newPassthroughProvider(t)

	_, bErr := provider.Passthrough(passthroughCtx(t), passthroughKey(ts), invokeRequest("/endpoints/my-ep/openai/v1/chat/completions", `{}`, nil))
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "only forwards")

	bearer := passthroughKey(ts)
	bearer.Value = *schemas.NewSecretVar("sagemaker-api-key-test")
	_, bErr = provider.Passthrough(passthroughCtx(t), bearer, invokeRequest("/endpoints/my-ep/invocations", `{}`, nil))
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "needs AWS credentials")

	_, bErr = provider.Passthrough(passthroughCtx(t), passthroughKey(ts), invokeRequest("/endpoints/my-ep/invocations-response-stream", `{}`, nil))
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "streaming passthrough")

	_, bErr = provider.PassthroughStream(passthroughCtx(t), noHooks, nil, passthroughKey(ts), invokeRequest("/endpoints/my-ep/invocations", `{}`, nil))
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message, "non-streaming passthrough")
	require.Empty(t, seen(), "nothing refused reaches SageMaker")
}

func TestPassthroughStreamUnpacksEventStream(t *testing.T) {
	stream := eventStream(t, []string{"data: {\"a\":1}\n\n", "data: {\"a\":2}\n\n", "data: [DONE]\n\n"}, "")
	ts, seen := fakeRuntimeHost(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.Header().Set("X-Amzn-SageMaker-Content-Type", "text/event-stream")
		_, _ = w.Write(stream)
	})
	ctx := passthroughCtx(t)
	ch, bErr := newPassthroughProvider(t).PassthroughStream(ctx, noHooks, nil, passthroughKey(ts), invokeRequest("/endpoints/my-ep/invocations-response-stream", `{"stream":true}`, nil))
	require.Nil(t, bErr, "%+v", bErr)
	body, bErr := drainPassthrough(t, ch)
	require.Nil(t, bErr, "%+v", bErr)
	require.Equal(t, "data: {\"a\":1}\n\ndata: {\"a\":2}\n\ndata: [DONE]\n\n", body, "the container's own bytes, without AWS framing")

	headers, _ := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string)
	require.Equal(t, "text/event-stream", headers["Content-Type"])
	require.Equal(t, "/endpoints/my-ep/invocations-response-stream", seen()[0].path)
}

func TestPassthroughStreamSurfacesModelStreamError(t *testing.T) {
	stream := eventStream(t, []string{"partial"}, "ModelStreamError")
	ts, _ := fakeRuntimeHost(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(stream)
	})
	ch, bErr := newPassthroughProvider(t).PassthroughStream(passthroughCtx(t), noHooks, nil, passthroughKey(ts), invokeRequest("/endpoints/my-ep/invocations-response-stream", `{}`, nil))
	require.Nil(t, bErr, "%+v", bErr)
	body, bErr := drainPassthrough(t, ch)
	require.Equal(t, "partial", body)
	require.NotNil(t, bErr)
	require.Contains(t, bErr.Error.Message+errString(bErr), "ModelStreamError")
}

func TestPassthroughStreamRelaysUpstreamErrorBody(t *testing.T) {
	ts, _ := fakeRuntimeHost(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Endpoint my-ep not found"}`))
	})
	ch, bErr := newPassthroughProvider(t).PassthroughStream(passthroughCtx(t), noHooks, nil, passthroughKey(ts), invokeRequest("/endpoints/my-ep/invocations-response-stream", `{}`, nil))
	require.Nil(t, bErr, "%+v", bErr)
	body, bErr := drainPassthrough(t, ch)
	require.Nil(t, bErr, "%+v", bErr)
	require.Equal(t, `{"message":"Endpoint my-ep not found"}`, body, "a non-200 answer is not an event stream and is relayed as is")
}

func errString(bErr *schemas.BifrostError) string {
	if bErr.Error != nil && bErr.Error.Error != nil {
		return bErr.Error.Error.Error()
	}
	return ""
}

// stalledRuntimeHost sends one PayloadPart and then holds the connection open without sending more.
func stalledRuntimeHost(t *testing.T) *httptest.Server {
	t.Helper()
	first := eventStream(t, []string{"data: {\"a\":1}\n\n"}, "")
	release := make(chan struct{})
	ts, _ := fakeRuntimeHost(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(first)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	return ts
}

func TestPassthroughStreamIdleTimeoutClosesStalledStream(t *testing.T) {
	ts := stalledRuntimeHost(t)
	ch, bErr := newPassthroughProvider(t).PassthroughStream(passthroughCtx(t), noHooks, nil, passthroughKey(ts), invokeRequest("/endpoints/my-ep/invocations-response-stream", `{}`, nil))
	require.Nil(t, bErr, "%+v", bErr)
	start := time.Now()
	body, bErr := drainPassthrough(t, ch)
	require.Equal(t, "data: {\"a\":1}\n\n", body)
	require.NotNil(t, bErr, "a stalled stream ends with an idle-timeout error")
	require.Less(t, time.Since(start), 8*time.Second, "the idle timer (2s) must close the upstream connection")
}

func TestPassthroughStreamCancellationClosesStalledStream(t *testing.T) {
	ts := stalledRuntimeHost(t)
	ctx := passthroughCtx(t)
	ch, bErr := newPassthroughProvider(t).PassthroughStream(ctx, noHooks, nil, passthroughKey(ts), invokeRequest("/endpoints/my-ep/invocations-response-stream", `{}`, nil))
	require.Nil(t, bErr, "%+v", bErr)
	first := <-ch
	require.NotNil(t, first)
	start := time.Now()
	ctx.Cancel()
	_, _ = drainPassthrough(t, ch)
	require.Less(t, time.Since(start), time.Second, "cancellation must close the upstream connection, not wait for the idle timer")
}
