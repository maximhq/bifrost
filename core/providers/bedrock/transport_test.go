package bedrock

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/maximhq/bifrost/core/network/proxytest"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/utils/awsauth"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redirectTransport is an http.RoundTripper that rewrites every request's
// host/scheme to a fixed target URL, used to redirect provider requests to a
// local httptest.Server without modifying provider code.
type redirectTransport struct {
	target    *url.URL
	transport http.RoundTripper
}

func (r *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = r.target.Scheme
	cloned.URL.Host = r.target.Host
	cloned.Host = r.target.Host
	return r.transport.RoundTrip(cloned)
}

// noopLogger is a no-op schemas.Logger for use in tests.
type noopLogger struct{}

func (noopLogger) Debug(string, ...any)                   {}
func (noopLogger) Info(string, ...any)                    {}
func (noopLogger) Warn(string, ...any)                    {}
func (noopLogger) Error(string, ...any)                   {}
func (noopLogger) Fatal(string, ...any)                   {}
func (noopLogger) SetLevel(schemas.LogLevel)              {}
func (noopLogger) SetOutputType(schemas.LoggerOutputType) {}
func (noopLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// newTestProviderWithServer returns a BedrockProvider whose HTTP client is
// redirected to the given httptest.Server.
func newTestProviderWithServer(t *testing.T, ts *httptest.Server) *BedrockProvider {
	t.Helper()
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 5,
		},
	}
	config.CheckAndSetDefaults()
	provider, err := NewBedrockProvider(config, noopLogger{})
	require.NoError(t, err)

	targetURL, err := url.Parse(ts.URL)
	require.NoError(t, err)

	redirect := &redirectTransport{
		target:    targetURL,
		transport: ts.Client().Transport,
	}
	provider.client = &http.Client{
		Transport: redirect,
		Timeout:   5 * time.Second,
	}
	// Streaming paths use streamingClient (no Timeout); redirect it to the
	// test server too, otherwise Bedrock streaming tests would hit the real
	// AWS endpoint.
	provider.streamingClient = &http.Client{Transport: redirect}
	return provider
}

// testBedrockKey returns a minimal Key with a bearer value so makeStreamingRequest
// skips IAM signing and proceeds to the HTTP call.
func testBedrockKey() schemas.Key {
	region := schemas.NewSecretVar("us-east-1")
	return schemas.Key{
		Value: *schemas.NewSecretVar("test-api-key"),
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			Region: region,
		},
	}
}

// testBedrockCtx returns a BifrostContext suitable for unit tests.
func testBedrockCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

// noopPostHookRunner is a PostHookRunner that passes through results unchanged.
func noopPostHookRunner(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

// testConverseStreamModel is a non-Anthropic, non-OpenAI Bedrock model that routes through
// the Converse streaming path (and thus the AWS EventStream decoder). OpenAI-family models
// stream via the Mantle endpoint instead; Anthropic/Claude also route through Converse, but
// Nova is used here to keep the EventStream-exception cases provider-agnostic.
const testConverseStreamModel = "amazon.nova-lite-v1:0"

// testChatRequest returns a minimal BifrostChatRequest for streaming tests.
func testChatRequest() *schemas.BifrostChatRequest {
	content := "hello"
	return &schemas.BifrostChatRequest{
		Model: "anthropic.claude-sonnet-4-5",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &content},
			},
		},
	}
}

// TestMakeStreamingRequest_StaleConnection_IsRetryable verifies that when the
// HTTP server closes the connection before sending a response (simulating a
// stale HTTP/2 connection), makeStreamingRequest returns a BifrostError with
// IsBifrostError:false so the retry gate in executeRequestWithRetries retries.
func TestMakeStreamingRequest_StaleConnection_IsRetryable(t *testing.T) {
	// Server that immediately closes the connection without sending anything.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack not supported", http.StatusInternalServerError)
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close() // close without writing any response
	}))
	defer ts.Close()

	provider := newTestProviderWithServer(t, ts)
	ctx := testBedrockCtx()
	key := testBedrockKey()

	_, bifrostErr := provider.makeStreamingRequest(ctx, []byte(`{}`), key, "anthropic.claude-sonnet-4-5", "converse-stream")

	require.NotNil(t, bifrostErr, "expected error when server closes connection")
	assert.False(t, bifrostErr.IsBifrostError,
		"stale-connection error must be IsBifrostError:false so the retry gate can retry it")
	require.NotNil(t, bifrostErr.Error)
	// Either ErrProviderNetworkError (net.OpError) or ErrProviderDoRequest (EOF/connection-reset)
	// are both retryable — the key invariant is IsBifrostError:false.
	assert.Contains(t, []string{schemas.ErrProviderNetworkError, schemas.ErrProviderDoRequest}, bifrostErr.Error.Message,
		"stale-connection error must use a retryable error message")
}

// TestChatCompletionStream_StaleConnection_ChunkIsRetryable verifies that when
// the server returns HTTP 200 but closes the body immediately (simulating a
// stale connection mid-stream before any EventStream data arrives), the first
// chunk received from the stream channel carries a BifrostError with
// IsBifrostError:false so that CheckFirstStreamChunkForError + the retry gate
// can transparently retry the request.
func TestChatCompletionStream_StaleConnection_ChunkIsRetryable(t *testing.T) {
	// Server: returns 200 with the correct content-type but closes body immediately.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close() // close without any EventStream bytes
	}))
	defer ts.Close()

	provider := newTestProviderWithServer(t, ts)
	ctx := testBedrockCtx()
	key := testBedrockKey()

	streamChan, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHookRunner, nil, key, testChatRequest())

	if bifrostErr != nil {
		// Error surfaced synchronously (e.g. connection refused before HTTP 200).
		assert.False(t, bifrostErr.IsBifrostError,
			"pre-stream network error must be IsBifrostError:false")
		return
	}

	// Error surfaced as the first stream chunk.
	require.NotNil(t, streamChan)
	chunk, ok := <-streamChan
	require.True(t, ok, "channel must not be empty")
	require.NotNil(t, chunk)
	require.NotNil(t, chunk.BifrostError, "expected an error chunk from the stream")

	assert.False(t, chunk.BifrostError.IsBifrostError,
		"stream transport error must be IsBifrostError:false so the retry gate can retry it")
	require.NotNil(t, chunk.BifrostError.Error)
	assert.Equal(t, schemas.ErrProviderNetworkError, chunk.BifrostError.Error.Message,
		"stream transport error must use ErrProviderNetworkError message")

	// Drain any remaining chunks.
	for range streamChan {
	}
}

// TestChatCompletionStream_NetOpError_ChunkIsRetryable verifies the specific
// "use of closed network connection" *net.OpError scenario from issue #2424:
// a successful HTTP connection that is then closed server-side produces a
// *net.OpError during EventStream decoding, which must arrive as a retryable
// IsBifrostError:false chunk.
func TestChatCompletionStream_NetOpError_ChunkIsRetryable(t *testing.T) {
	// Server: returns 200 + correct headers, writes a truncated EventStream
	// prelude (not a valid frame), then forcibly resets the TCP connection —
	// producing a *net.OpError on the client's read side.
	ts := newMidStreamResetServer()
	defer ts.Close()

	provider := newTestProviderWithServer(t, ts)
	ctx := testBedrockCtx()
	key := testBedrockKey()

	streamChan, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHookRunner, nil, key, testChatRequest())
	if bifrostErr != nil {
		assert.False(t, bifrostErr.IsBifrostError,
			"pre-stream network error must be IsBifrostError:false")
		return
	}

	require.NotNil(t, streamChan)

	// Collect chunks until we find an error chunk (may not be the very first
	// if the OS buffers the partial write, but it must appear before close).
	var errChunk *schemas.BifrostStreamChunk
	for chunk := range streamChan {
		if chunk != nil && chunk.BifrostError != nil {
			errChunk = chunk
			break
		}
	}
	// Drain remaining.
	for range streamChan {
	}

	require.NotNil(t, errChunk, "expected an error chunk from the stream")
	assert.False(t, errChunk.BifrostError.IsBifrostError,
		"net.OpError during EventStream decoding must be IsBifrostError:false so the retry gate can retry it")
	require.NotNil(t, errChunk.BifrostError.Error)
	assert.Equal(t, schemas.ErrProviderNetworkError, errChunk.BifrostError.Error.Message,
		"net.OpError during EventStream decoding must use ErrProviderNetworkError message")
}

// newMidStreamResetServer returns a server that answers 200, writes a truncated
// EventStream prelude (not a valid frame), then resets the TCP connection, which
// produces a *net.OpError on the client's read side.
func newMidStreamResetServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte{0x00, 0x00, 0x00})
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, _ := hj.Hijack()
		// RST instead of FIN — guarantees a *net.OpError on the client read.
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetLinger(0)
		}
		conn.Close()
	}))
}

// TestStream_MidStreamDrop_IsUpstreamError verifies that a connection dropped after
// the 200 reaches clients as a 502 upstream failure. Without a status the error
// resolves to 400, which the Anthropic surface reports as invalid_request_error, a
// type clients do not retry.
func TestStream_MidStreamDrop_IsUpstreamError(t *testing.T) {
	content := "hello"
	streams := map[string]func(*BedrockProvider) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError){
		"text": func(p *BedrockProvider) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			return p.TextCompletionStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), &schemas.BifrostTextCompletionRequest{
				Model: "mistral.mistral-7b-instruct-v0:2",
				Input: &schemas.TextCompletionInput{PromptStr: &content},
			})
		},
		"chat": func(p *BedrockProvider) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			return p.ChatCompletionStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), testChatRequest())
		},
		"responses": func(p *BedrockProvider) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			return p.ResponsesStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), &schemas.BifrostResponsesRequest{
				Model: "anthropic.claude-sonnet-4-5",
				Input: []schemas.ResponsesMessage{{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: &content},
				}},
			})
		},
	}
	for name, stream := range streams {
		t.Run(name, func(t *testing.T) {
			ts := newMidStreamResetServer()
			defer ts.Close()

			streamChan, bifrostErr := stream(newTestProviderWithServer(t, ts))
			require.Nil(t, bifrostErr)

			var streamErr *schemas.BifrostError
			for chunk := range streamChan {
				if chunk != nil && chunk.BifrostError != nil && streamErr == nil {
					streamErr = chunk.BifrostError
				}
			}

			require.NotNil(t, streamErr, "expected an error chunk from the stream")
			assert.False(t, streamErr.IsBifrostError)
			require.NotNil(t, streamErr.StatusCode)
			assert.Equal(t, http.StatusBadGateway, *streamErr.StatusCode)
			assert.Equal(t, "api_error", anthropic.ToAnthropicChatCompletionError(streamErr).Error.Type)
		})
	}
}

// TestRequest_ConnectionFailure_IsUpstreamError verifies that a connection the server
// closes before responding reaches clients as a 502 upstream failure. Without a
// status the error resolves to 400, which reads as a malformed request.
func TestRequest_ConnectionFailure_IsUpstreamError(t *testing.T) {
	requests := map[string]func(*BedrockProvider) *schemas.BifrostError{
		"converse": func(p *BedrockProvider) *schemas.BifrostError {
			req, err := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/model/m/converse", strings.NewReader(`{}`))
			require.NoError(t, err)
			_, _, _, bifrostErr := p.executeBedrockRequest(req)
			return bifrostErr
		},
		"agent runtime": func(p *BedrockProvider) *schemas.BifrostError {
			_, _, _, bifrostErr := p.completeAgentRuntimeRequest(testBedrockCtx(), []byte(`{}`), "/rerank", testBedrockKey())
			return bifrostErr
		},
		"stream": func(p *BedrockProvider) *schemas.BifrostError {
			_, bifrostErr := p.makeStreamingRequest(testBedrockCtx(), []byte(`{}`), testBedrockKey(), "anthropic.claude-sonnet-4-5", "converse-stream")
			return bifrostErr
		},
		"list models": func(p *BedrockProvider) *schemas.BifrostError {
			_, bifrostErr := p.listModelsByKey(testBedrockCtx(), testBedrockKey(), &schemas.BifrostListModelsRequest{Provider: schemas.Bedrock})
			return bifrostErr
		},
	}
	for name, send := range requests {
		t.Run(name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hj, ok := w.(http.Hijacker)
				if !ok {
					http.Error(w, "hijack not supported", http.StatusInternalServerError)
					return
				}
				conn, _, _ := hj.Hijack()
				conn.Close()
			}))
			defer ts.Close()

			bifrostErr := send(newTestProviderWithServer(t, ts))

			require.NotNil(t, bifrostErr)
			assert.False(t, bifrostErr.IsBifrostError)
			require.NotNil(t, bifrostErr.StatusCode)
			assert.Equal(t, http.StatusBadGateway, *bifrostErr.StatusCode)
			require.NotNil(t, bifrostErr.Error)
			require.NotNil(t, bifrostErr.Error.Type)
			assert.Equal(t, schemas.ProviderConnectionFailed, *bifrostErr.Error.Type)
			assert.Contains(t, []string{schemas.ErrProviderNetworkError, schemas.ErrProviderDoRequest}, bifrostErr.Error.Message)
		})
	}
}

func TestNewRequestError(t *testing.T) {
	cases := []struct {
		name           string
		err            error
		isBifrostError bool
		status         int
		errType        string
		message        string
	}{
		{"cancelled", context.Canceled, false, 0, schemas.RequestCancelled, schemas.ErrRequestCancelled},
		{"deadline", context.DeadlineExceeded, true, http.StatusGatewayTimeout, schemas.RequestTimedOut, schemas.ErrProviderRequestTimedOut},
		{"network", &net.OpError{Op: "dial", Err: io.EOF}, false, http.StatusBadGateway, schemas.ProviderConnectionFailed, schemas.ErrProviderNetworkError},
		{"other", io.ErrUnexpectedEOF, false, http.StatusBadGateway, schemas.ProviderConnectionFailed, schemas.ErrProviderDoRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bifrostErr := newRequestError(tc.err, 25*time.Millisecond)

			assert.Equal(t, tc.isBifrostError, bifrostErr.IsBifrostError)
			if tc.status == 0 {
				assert.Nil(t, bifrostErr.StatusCode)
			} else {
				require.NotNil(t, bifrostErr.StatusCode)
				assert.Equal(t, tc.status, *bifrostErr.StatusCode)
			}
			require.NotNil(t, bifrostErr.Error.Type)
			assert.Equal(t, tc.errType, *bifrostErr.Error.Type)
			assert.Equal(t, tc.message, bifrostErr.Error.Message)
			assert.Equal(t, int64(25), bifrostErr.ExtraFields.Latency)
		})
	}
}

// writeEventStreamException encodes a well-formed AWS EventStream exception
// frame with the given exception type and message into w.
// The frame format is: prelude (total_len + headers_len + CRC) + headers + payload + message_CRC.
// We use the AWS SDK's eventstream.Encoder so the binary framing is correct.
func writeEventStreamException(t *testing.T, w io.Writer, excType, msg string) {
	t.Helper()
	enc := eventstream.NewEncoder()
	payload, err := json.Marshal(map[string]string{"message": msg})
	require.NoError(t, err, "failed to marshal exception payload")
	headers := eventstream.Headers{
		{Name: ":message-type", Value: eventstream.StringValue("exception")},
		{Name: ":exception-type", Value: eventstream.StringValue(excType)},
		{Name: ":content-type", Value: eventstream.StringValue("application/json")},
	}
	err = enc.Encode(w, eventstream.Message{Headers: headers, Payload: payload})
	require.NoError(t, err, "failed to encode EventStream exception frame")
}

// TestChatCompletionStream_RetryableException_ChunkIsRetryable verifies that
// when AWS Bedrock sends a retryable exception (serviceUnavailableException,
// throttlingException, etc.) through the EventStream, the resulting error chunk
// has IsBifrostError:false and the correct HTTP StatusCode so that the retry
// gate in executeRequestWithRetries can retry the request.
func TestChatCompletionStream_RetryableException_ChunkIsRetryable(t *testing.T) {
	tests := []struct {
		excType        string
		expectedStatus int
	}{
		{"serviceUnavailableException", 503},
		{"throttlingException", 429},
		{"modelNotReadyException", 503},
		{"internalServerException", 500},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.excType, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
				w.WriteHeader(http.StatusOK)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				writeEventStreamException(t, w, tc.excType, "service is unavailable, please retry")
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}))
			defer ts.Close()

			provider := newTestProviderWithServer(t, ts)
			ctx := testBedrockCtx()
			key := testBedrockKey()

			req := testChatRequest()
			req.Model = testConverseStreamModel
			streamChan, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHookRunner, nil, key, req)
			require.Nil(t, bifrostErr, "expected EventStream exception to surface as a stream chunk")

			require.NotNil(t, streamChan)

			var errChunk *schemas.BifrostStreamChunk
			for chunk := range streamChan {
				if chunk != nil && chunk.BifrostError != nil {
					errChunk = chunk
					break
				}
			}
			for range streamChan {
			}

			require.NotNil(t, errChunk, "expected error chunk for %s", tc.excType)
			assert.False(t, errChunk.BifrostError.IsBifrostError,
				"%s must be IsBifrostError:false so retry gate can retry it", tc.excType)
			require.NotNil(t, errChunk.BifrostError.StatusCode,
				"%s must carry a StatusCode for the retry gate", tc.excType)
			assert.Equal(t, tc.expectedStatus, *errChunk.BifrostError.StatusCode,
				"%s must map to HTTP %d", tc.excType, tc.expectedStatus)
		})
	}
}

// TestChatCompletionStream_NonRetryableException_IsTerminal verifies that
// non-retryable exception types (e.g. validationException, accessDeniedException)
// continue to use ProcessAndSendError (IsBifrostError:true) and are NOT retried.
func TestChatCompletionStream_NonRetryableException_IsTerminal(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		writeEventStreamException(t, w, "validationException", "input validation failed")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer ts.Close()

	provider := newTestProviderWithServer(t, ts)
	ctx := testBedrockCtx()
	key := testBedrockKey()

	req := testChatRequest()
	req.Model = testConverseStreamModel
	streamChan, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHookRunner, nil, key, req)
	require.Nil(t, bifrostErr, "expected EventStream exception to surface as a stream chunk")

	require.NotNil(t, streamChan)

	var errChunk *schemas.BifrostStreamChunk
	for chunk := range streamChan {
		if chunk != nil && chunk.BifrostError != nil {
			errChunk = chunk
			break
		}
	}
	for range streamChan {
	}

	require.NotNil(t, errChunk, "expected error chunk for validationException")
	assert.True(t, errChunk.BifrostError.IsBifrostError,
		"non-retryable validationException must remain IsBifrostError:true")
}

// testTextCompletionRequest returns a minimal BifrostTextCompletionRequest for streaming tests.
func testTextCompletionRequest() *schemas.BifrostTextCompletionRequest {
	prompt := "hello"
	return &schemas.BifrostTextCompletionRequest{
		Model: "anthropic.claude-sonnet-4-5",
		Input: &schemas.TextCompletionInput{PromptStr: &prompt},
	}
}

// testResponsesRequest returns a minimal BifrostResponsesRequest for streaming tests.
func TestConverseStreamRequiresMessageStop(t *testing.T) {
	testCases := []struct {
		name      string
		payloads  []string
		wantError bool
	}{
		{name: "empty", wantError: true},
		{name: "unrecognized", payloads: []string{`{"unexpected":"upstream failure"}`}, wantError: true},
		{name: "partial", payloads: []string{`{"role":"assistant"}`, `{"contentBlockIndex":0,"delta":{"text":"hello"}}`}, wantError: true},
		{name: "usage_without_stop", payloads: []string{`{"usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`}, wantError: true},
		{name: "completed", payloads: []string{`{"role":"assistant"}`, `{"contentBlockIndex":0,"delta":{"text":"hello"}}`, `{"stopReason":"end_turn"}`, `{"usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`}},
		{name: "filtered", payloads: []string{`{"role":"assistant"}`, `{"stopReason":"guardrail_intervened"}`}},
	}
	for _, api := range []string{"chat", "responses"} {
		for _, testCase := range testCases {
			t.Run(api+"/"+testCase.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					writer.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
					writer.WriteHeader(http.StatusOK)
					encoder := eventstream.NewEncoder()
					for _, payload := range testCase.payloads {
						assert.NoError(t, encoder.Encode(writer, eventstream.Message{
							Headers: eventstream.Headers{
								{Name: ":message-type", Value: eventstream.StringValue("event")},
								{Name: ":event-type", Value: eventstream.StringValue("testEvent")},
							},
							Payload: []byte(payload),
						}))
					}
				}))
				defer server.Close()
				provider := newTestProviderWithServer(t, server)
				provider.sendBackRawResponse = true
				ctx := testBedrockCtx()
				var stream chan *schemas.BifrostStreamChunk
				var requestError *schemas.BifrostError
				if api == "chat" {
					request := testChatRequest()
					request.Model = testConverseStreamModel
					stream, requestError = provider.ChatCompletionStream(ctx, noopPostHookRunner, nil, testBedrockKey(), request)
				} else {
					request := testResponsesRequest()
					request.Model = testConverseStreamModel
					stream, requestError = provider.ResponsesStream(ctx, noopPostHookRunner, nil, testBedrockKey(), request)
				}
				require.Nil(t, requestError)
				var streamError *schemas.BifrostError
				completed := false
				for chunk := range stream {
					if chunk.BifrostError != nil {
						streamError = chunk.BifrostError
					}
					if chunk.BifrostResponsesStreamResponse != nil && chunk.BifrostResponsesStreamResponse.Type == schemas.ResponsesStreamResponseTypeCompleted {
						completed = true
					}
					if chunk.BifrostChatResponse != nil {
						for _, choice := range chunk.BifrostChatResponse.Choices {
							if choice.FinishReason != nil {
								completed = true
							}
						}
					}
				}
				if testCase.wantError {
					require.NotNil(t, streamError)
					assert.False(t, completed)
					assert.False(t, streamError.IsBifrostError)
					require.NotNil(t, streamError.StatusCode)
					assert.Equal(t, http.StatusBadGateway, *streamError.StatusCode)
					assert.Contains(t, streamError.Error.Message, "before messageStop")
					if len(testCase.payloads) > 0 {
						assert.NotNil(t, streamError.ExtraFields.RawResponse)
					}
					if testCase.name == "usage_without_stop" {
						require.NotNil(t, streamError.ExtraFields.BilledUsage)
						assert.Equal(t, 2, streamError.ExtraFields.BilledUsage.TotalTokens)
					}
				} else {
					assert.Nil(t, streamError)
				}
			})
		}
	}
}

func TestConverseStreamDiagnosticsBounded(t *testing.T) {
	var progress converseStreamProgress
	progress.observe(eventstream.Message{Payload: []byte(strings.Repeat("x", 8192))}, &BedrockStreamEvent{})
	assert.Len(t, progress.lastPayload, 4096)
	progress.observe(eventstream.Message{Payload: []byte("next")}, &BedrockStreamEvent{})
	assert.Equal(t, "next", string(progress.lastPayload))
	assert.Equal(t, 2, progress.eventCount)
}

func testResponsesRequest() *schemas.BifrostResponsesRequest {
	msgType := schemas.ResponsesMessageType("message")
	roleUser := schemas.ResponsesMessageRoleType("user")
	content := "hello"
	return &schemas.BifrostResponsesRequest{
		Model: "anthropic.claude-sonnet-4-5",
		Input: []schemas.ResponsesMessage{
			{
				Type:    &msgType,
				Role:    &roleUser,
				Content: &schemas.ResponsesMessageContent{ContentStr: &content},
			},
		},
	}
}

// assertRetryableExceptionChunk is the shared assertion helper for all three
// streaming-method retryable-exception tests.
func assertRetryableExceptionChunk(t *testing.T, streamChan chan *schemas.BifrostStreamChunk, bifrostErr *schemas.BifrostError, excType string, expectedStatus int) {
	t.Helper()
	require.Nil(t, bifrostErr, "expected EventStream exception to surface as a stream chunk, not a pre-stream error")
	require.NotNil(t, streamChan)

	var errChunk *schemas.BifrostStreamChunk
	for chunk := range streamChan {
		if chunk != nil && chunk.BifrostError != nil {
			errChunk = chunk
			break
		}
	}
	for range streamChan {
	}

	require.NotNil(t, errChunk, "expected error chunk for %s", excType)
	assert.False(t, errChunk.BifrostError.IsBifrostError,
		"%s must be IsBifrostError:false so retry gate can retry it", excType)
	require.NotNil(t, errChunk.BifrostError.StatusCode,
		"%s must carry a StatusCode for the retry gate", excType)
	assert.Equal(t, expectedStatus, *errChunk.BifrostError.StatusCode,
		"%s must map to HTTP %d", excType, expectedStatus)
}

// TestTextCompletionStream_RetryableException_ChunkIsRetryable mirrors the
// ChatCompletionStream test for the TextCompletionStream path, which has
// slightly different payload-parsing logic (extra BedrockError JSON unmarshal).
func TestTextCompletionStream_RetryableException_ChunkIsRetryable(t *testing.T) {
	tests := []struct {
		excType        string
		expectedStatus int
	}{
		{"serviceUnavailableException", 503},
		{"throttlingException", 429},
		{"modelNotReadyException", 503},
		{"internalServerException", 500},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.excType, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
				w.WriteHeader(http.StatusOK)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				writeEventStreamException(t, w, tc.excType, "service is unavailable, please retry")
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}))
			defer ts.Close()

			provider := newTestProviderWithServer(t, ts)
			streamChan, bifrostErr := provider.TextCompletionStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), testTextCompletionRequest())
			assertRetryableExceptionChunk(t, streamChan, bifrostErr, tc.excType, tc.expectedStatus)
		})
	}
}

// TestResponsesStream_RetryableException_ChunkIsRetryable mirrors the
// ChatCompletionStream test for the ResponsesStream path.
func TestResponsesStream_RetryableException_ChunkIsRetryable(t *testing.T) {
	tests := []struct {
		excType        string
		expectedStatus int
	}{
		{"serviceUnavailableException", 503},
		{"throttlingException", 429},
		{"modelNotReadyException", 503},
		{"internalServerException", 500},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.excType, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
				w.WriteHeader(http.StatusOK)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				writeEventStreamException(t, w, tc.excType, "service is unavailable, please retry")
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}))
			defer ts.Close()

			provider := newTestProviderWithServer(t, ts)
			req := testResponsesRequest()
			req.Model = testConverseStreamModel
			streamChan, bifrostErr := provider.ResponsesStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), req)
			assertRetryableExceptionChunk(t, streamChan, bifrostErr, tc.excType, tc.expectedStatus)
		})
	}
}

func generateTestCACert(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "testca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return string(certPEM)
}

func TestBedrockTransportHTTP2Config(t *testing.T) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 300,
			MaxConnsPerHost:                5000,
			EnforceHTTP2:                   true,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, noopLogger{})
	require.NoError(t, err)
	require.NotNil(t, provider)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok, "transport should be *http.Transport")

	assert.Equal(t, 5000, transport.MaxConnsPerHost)
	assert.Equal(t, schemas.DefaultMaxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
	assert.Equal(t, schemas.DefaultMaxIdleConnsPerHost, transport.MaxIdleConns)
	assert.True(t, transport.ForceAttemptHTTP2)
	assert.Nil(t, transport.HTTP2, "ping keepalive must stay off when the interval is unset")
}

func TestBedrockTransportHTTP2PingKeepalive(t *testing.T) {
	newTransport := func(t *testing.T, enforceHTTP2 bool, pingIntervalSeconds int) *http.Transport {
		t.Helper()
		config := &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 300,
				EnforceHTTP2:                   enforceHTTP2,
				HTTP2PingIntervalInSeconds:     pingIntervalSeconds,
			},
		}
		config.CheckAndSetDefaults()

		provider, err := NewBedrockProvider(config, noopLogger{})
		require.NoError(t, err)

		transport, ok := provider.client.Transport.(*http.Transport)
		require.True(t, ok, "transport should be *http.Transport")
		return transport
	}

	t.Run("enforced with a positive interval configures the PING keepalive", func(t *testing.T) {
		transport := newTransport(t, true, 45)
		require.NotNil(t, transport.HTTP2, "enforce_http2 + positive interval must set transport.HTTP2")
		assert.Equal(t, 45*time.Second, transport.HTTP2.SendPingTimeout)
	})

	t.Run("enforced with a zero interval leaves the keepalive off", func(t *testing.T) {
		transport := newTransport(t, true, 0)
		assert.Nil(t, transport.HTTP2, "enforce_http2 alone must not imply pinging")
	})

	t.Run("non-enforced with a positive interval leaves the keepalive off", func(t *testing.T) {
		transport := newTransport(t, false, 45)
		assert.Nil(t, transport.HTTP2, "a ping interval without enforce_http2 must be a no-op")
	})
}

func TestBedrockTransportCustomMaxConns(t *testing.T) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 300,
			MaxConnsPerHost:                50,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, noopLogger{})
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)

	assert.Equal(t, 50, transport.MaxConnsPerHost)
	assert.Equal(t, schemas.DefaultMaxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
	assert.Equal(t, schemas.DefaultMaxIdleConnsPerHost, transport.MaxIdleConns)
}

func TestBedrockTransportDefaultMaxConns(t *testing.T) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 300,
			// MaxConnsPerHost left as 0 — should default to 5000
		},
	}
	config.CheckAndSetDefaults()

	assert.Equal(t, schemas.DefaultMaxConnsPerHost, config.NetworkConfig.MaxConnsPerHost)

	provider, err := NewBedrockProvider(config, noopLogger{})
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)

	assert.Equal(t, schemas.DefaultMaxConnsPerHost, transport.MaxConnsPerHost)
	assert.Equal(t, schemas.DefaultMaxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
	assert.Equal(t, schemas.DefaultMaxIdleConnsPerHost, transport.MaxIdleConns)
}

func TestBedrockTransportTLSInsecureSkipVerify(t *testing.T) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 300,
			InsecureSkipVerify:             true,
			EnforceHTTP2:                   true,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, noopLogger{})
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.TLSClientConfig)
	assert.True(t, transport.TLSClientConfig.InsecureSkipVerify)
	assert.Equal(t, uint16(tls.VersionTLS12), transport.TLSClientConfig.MinVersion)
	// ForceAttemptHTTP2 should still be true even with custom TLS config
	assert.True(t, transport.ForceAttemptHTTP2)
}

// TestBedrockTransportInsecureTargetsStillVerifyHTTPSProxy pins that
// network_config.insecure_skip_verify only reaches targets: net/http would handshake
// with an https:// proxy using TLSClientConfig, so without the hop dialer the proxy's
// certificate would go unchecked although proxy_config.skip_tls_verify is off.
func TestBedrockTransportInsecureTargetsStillVerifyHTTPSProxy(t *testing.T) {
	recorder := proxytest.NewHTTPSRecorder(t, "bedrock-tls")
	for _, enforceHTTP2 := range []bool{true, false} {
		recorder.Reset()
		config := &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
				InsecureSkipVerify:             true,
				EnforceHTTP2:                   enforceHTTP2,
			},
			ProxyConfig: &schemas.ProxyConfig{
				Type: schemas.HTTPProxy,
				URL:  schemas.NewSecretVar("https://127.0.0.1:" + recorder.Port()),
			},
		}
		config.CheckAndSetDefaults()

		provider, err := NewBedrockProvider(config, noopLogger{})
		require.NoError(t, err)

		for name, client := range map[string]*http.Client{"client": provider.client, "realtime": provider.realtimeClient} {
			resp, err := client.Get("http://bedrock-runtime.us-east-1.amazonaws.com/")
			if err == nil {
				resp.Body.Close()
				t.Errorf("enforce_http2=%v %s: an https proxy whose certificate is not trusted was used", enforceHTTP2, name)
			}
		}
		assert.Empty(t, recorder.Seen(), "enforce_http2=%v: the unverified proxy saw traffic", enforceHTTP2)

		// With the proxy's CA in proxy_config, the same proxy is verified and used.
		config.ProxyConfig.CACertPEM = schemas.NewSecretVar(recorder.CAPEM)
		trusted, err := NewBedrockProvider(config, noopLogger{})
		require.NoError(t, err)
		resp, err := trusted.client.Get("http://bedrock-runtime.us-east-1.amazonaws.com/")
		require.NoError(t, err, "enforce_http2=%v: the trusted https proxy was refused", enforceHTTP2)
		resp.Body.Close()
		assert.NotEmpty(t, recorder.Seen(), "enforce_http2=%v: the trusted proxy saw no traffic", enforceHTTP2)
	}
}

func TestBedrockTransportTLSCACert(t *testing.T) {
	testCACert := generateTestCACert(t)

	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 300,
			CACertPEM:                      schemas.NewSecretVar(testCACert),
			EnforceHTTP2:                   true,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, noopLogger{})
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.TLSClientConfig)
	assert.NotNil(t, transport.TLSClientConfig.RootCAs)
	assert.Equal(t, uint16(tls.VersionTLS12), transport.TLSClientConfig.MinVersion)
	assert.True(t, transport.ForceAttemptHTTP2)
}

func TestBedrockTransportDefaultTLS(t *testing.T) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 300,
			// No TLS settings — should use system defaults
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, noopLogger{})
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)
	// No custom TLS config should be set
	assert.Nil(t, transport.TLSClientConfig)
	// EnforceHTTP2 not set — ForceAttemptHTTP2 should be false
	assert.False(t, transport.ForceAttemptHTTP2)
}

func TestBedrockTransportEnforceHTTP2(t *testing.T) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 300,
			EnforceHTTP2:                   true,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, noopLogger{})
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.True(t, transport.ForceAttemptHTTP2)
	// TLSNextProto should NOT be set when HTTP/2 is enforced, allowing ALPN negotiation
	assert.Nil(t, transport.TLSNextProto)
	// Nova Sonic needs HTTP/2, which this transport already speaks, so it shares the pool
	assert.Same(t, transport, provider.realtimeClient.Transport)
	assert.Zero(t, provider.realtimeClient.Timeout, "a realtime session outlives the request timeout")
}

func TestBedrockTransportEnforceHTTP2Disabled(t *testing.T) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 300,
			EnforceHTTP2:                   false,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, noopLogger{})
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.False(t, transport.ForceAttemptHTTP2)
	// TLSNextProto must be set to empty map to truly disable HTTP/2 ALPN negotiation
	assert.NotNil(t, transport.TLSNextProto)
	assert.Empty(t, transport.TLSNextProto)
	// Nova Sonic's bidirectional stream is HTTP/2 only, so it gets a transport of its own
	realtimeTransport, ok := provider.realtimeClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.NotSame(t, transport, realtimeTransport)
	assert.True(t, realtimeTransport.ForceAttemptHTTP2)
	assert.Nil(t, realtimeTransport.TLSNextProto)
}

// TestSignAWSRequest_ExcludesVolatileHeadersFromSignature locks in the fix for the
// "signature we calculated does not match" 403s on Bedrock. The AWS SDK signs every header
// left on the request, so client/proxy headers forwarded from the /anthropic integration
// ended up in SignedHeaders. Any hop that rewrites one of them (x-forwarded-for gains the
// NAT address in transit) then invalidates the signature. AWS's signing guide requires only
// host and x-amz-*, and explicitly warns against signing headers "mutated by proxies, load
// balancers, and the nodes in a distributed system". Volatile headers are lifted out for
// signing and restored afterwards; only Bifrost-internal x-bf-* is dropped for good.
func TestSignAWSRequest_ExcludesVolatileHeadersFromSignature(t *testing.T) {
	volatile := map[string]string{
		"x-forwarded-for":   "10.30.10.147",
		"x-forwarded-proto": "https",
		"x-real-ip":         "10.30.10.147",
		"x-request-id":      "57bac1b7-1831-4a0a-97cb-af8e87329147",
		"x-bf-vk":           "sk-bf-secret-should-never-reach-aws",
		"connection":        "keep-alive",
	}
	kept := map[string]string{
		"anthropic-beta":  "interleaved-thinking-2025-05-14",
		"accept-encoding": "identity", // deliberately set for eventstream TTFB (#4542)
		"x-amz-target":    "converse",
	}

	req, err := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/model/m/converse-stream", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range volatile {
		req.Header.Set(k, v)
	}
	for k, v := range kept {
		req.Header.Set(k, v)
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	keyCfg := &schemas.BedrockKeyConfig{
		AccessKey: *schemas.NewSecretVar("AKIAIOSFODNN7EXAMPLE"),
		SecretKey: *schemas.NewSecretVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
	}
	if bifrostErr := signAWSRequest(ctx, req, keyCfg, "us-east-1", bedrockSigningService); bifrostErr != nil {
		t.Fatalf("signAWSRequest failed: %s", bifrostErr.Error.Message)
	}

	auth := req.Header.Get("Authorization")
	_, signedPart, found := strings.Cut(auth, "SignedHeaders=")
	if !found {
		t.Fatalf("no SignedHeaders in Authorization: %q", auth)
	}
	signedHeadersStr, _, _ := strings.Cut(signedPart, ",")
	signedHeaders := strings.Split(signedHeadersStr, ";")

	// None of the volatile headers may be covered by the signature.
	for name := range volatile {
		if slices.Contains(signedHeaders, name) {
			t.Errorf("%q is in SignedHeaders (%q) — a proxy rewriting it breaks SigV4", name, signedHeadersStr)
		}
	}

	// Bifrost-internal headers carry the caller's virtual key and must not reach AWS at all.
	if got := req.Header.Get("x-bf-vk"); got != "" {
		t.Errorf("x-bf-vk was forwarded upstream with value %q", got)
	}

	// Every other volatile header is restored after signing, so the wire is unchanged.
	for name, want := range volatile {
		if strings.HasPrefix(name, awsauth.InternalHeaderPrefix) {
			continue
		}
		if got := req.Header.Get(name); got != want {
			t.Errorf("%q should be restored after signing: got %q, want %q", name, got, want)
		}
	}

	// Headers Bifrost controls must survive untouched and stay signed where required.
	for name, want := range kept {
		if got := req.Header.Get(name); got != want {
			t.Errorf("%q should survive signing: got %q, want %q", name, got, want)
		}
	}
	if !slices.Contains(signedHeaders, "host") {
		t.Errorf("host must be signed; SignedHeaders=%q", signedHeadersStr)
	}
}

// fakeContainerCredentials points the default AWS credential chain at a local container
// credentials endpoint (the one EKS Pod Identity serves) and counts the fetches it gets.
func fakeContainerCredentials(t testing.TB) *atomic.Int32 {
	t.Helper()
	var fetches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		_, _ = w.Write([]byte(`{"AccessKeyId":"ASIAFAKE","SecretAccessKey":"secret","Token":"token","Expiration":"` + expiry + `"}`))
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	for k, v := range map[string]string{
		"AWS_ACCESS_KEY_ID":                      "",
		"AWS_SECRET_ACCESS_KEY":                  "",
		"AWS_ACCESS_KEY":                         "",
		"AWS_SECRET_KEY":                         "",
		"AWS_DEFAULT_PROFILE":                    "",
		"AWS_SESSION_TOKEN":                      "",
		"AWS_PROFILE":                            "",
		"AWS_ROLE_ARN":                           "",
		"AWS_WEB_IDENTITY_TOKEN_FILE":            "",
		"AWS_CONFIG_FILE":                        dir + "/config",
		"AWS_SHARED_CREDENTIALS_FILE":            dir + "/credentials",
		"AWS_EC2_METADATA_DISABLED":              "true",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI":     server.URL,
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN":      "pod-identity-token",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": "",
	} {
		t.Setenv(k, v)
	}
	return &fetches
}

// testRegions numbers the regions testRegion hands out.
var testRegions atomic.Int32

// testRegion returns a region no earlier test or run has used. The resolved providers are cached
// per region for the life of the process, so a reused region would answer from a fake endpoint
// that has since closed (go test -count=2).
func testRegion(name string) string {
	return "us-test-" + name + "-" + strconv.Itoa(int(testRegions.Add(1)))
}

// TestResolveAWSCredentials_DefaultChainFetchesOnce: a key with no access key resolves
// through the default chain, and the chain's credentials are reused until they near expiry
// rather than fetched again for every signed request.
func TestResolveAWSCredentials_DefaultChainFetchesOnce(t *testing.T) {
	fetches := fakeContainerCredentials(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	region := testRegion("default-chain")
	for range 20 {
		creds, bifrostErr := ResolveAWSCredentials(ctx, &schemas.BedrockKeyConfig{}, region)
		require.Nil(t, bifrostErr)
		assert.Equal(t, "ASIAFAKE", creds.AccessKeyID)
	}
	assert.Equal(t, int32(1), fetches.Load(), "the default chain must be fetched once, not once per request")
}

// fakeSTS answers STS AssumeRole from a local server. It counts the calls it gets and keeps
// the form of the last one.
func fakeSTS(t testing.TB) (*atomic.Int32, *atomic.Value) {
	t.Helper()
	var calls atomic.Int32
	var lastForm atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = r.ParseForm()
		lastForm.Store(r.PostForm)
		expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(`<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>ASIAASSUMED</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>token</SessionToken><Expiration>` + expiry + `</Expiration></Credentials><AssumedRoleUser><Arn>arn:aws:sts::123456789012:assumed-role/r/s</Arn><AssumedRoleId>AROA:s</AssumedRoleId></AssumedRoleUser></AssumeRoleResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></AssumeRoleResponse>`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("AWS_ENDPOINT_URL_STS", server.URL)
	return &calls, &lastForm
}

// TestResolveAWSCredentials_KeySources: a key's credential fields reach the shared resolver —
// none means the default chain, static keys are used as given, and the role fields reach STS.
func TestResolveAWSCredentials_KeySources(t *testing.T) {
	fetches := fakeContainerCredentials(t)
	stsCalls, stsForm := fakeSTS(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	region := testRegion("sources")

	creds, bifrostErr := ResolveAWSCredentials(ctx, nil, region)
	require.Nil(t, bifrostErr)
	assert.Equal(t, "ASIAFAKE", creds.AccessKeyID, "a nil key config uses the default chain")

	creds, bifrostErr = ResolveAWSCredentials(ctx, &schemas.BedrockKeyConfig{
		AccessKey:    *schemas.NewSecretVar("AKIAIOSFODNN7EXAMPLE"),
		SecretKey:    *schemas.NewSecretVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
		SessionToken: schemas.NewSecretVar("session-token"),
	}, region)
	require.Nil(t, bifrostErr)
	assert.Equal(t, "AKIAIOSFODNN7EXAMPLE", creds.AccessKeyID)
	assert.Equal(t, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", creds.SecretAccessKey)
	assert.Equal(t, "session-token", creds.SessionToken)

	creds, bifrostErr = ResolveAWSCredentials(ctx, &schemas.BedrockKeyConfig{
		RoleARN:         schemas.NewSecretVar("arn:aws:iam::123456789012:role/bedrock"),
		ExternalID:      schemas.NewSecretVar("ext-123"),
		RoleSessionName: schemas.NewSecretVar("bifrost-bedrock"),
	}, region)
	require.Nil(t, bifrostErr)
	assert.Equal(t, "ASIAASSUMED", creds.AccessKeyID)
	form, _ := stsForm.Load().(url.Values)
	assert.Equal(t, "arn:aws:iam::123456789012:role/bedrock", form.Get("RoleArn"))
	assert.Equal(t, "ext-123", form.Get("ExternalId"))
	assert.Equal(t, "bifrost-bedrock", form.Get("RoleSessionName"))

	assert.Equal(t, int32(1), fetches.Load())
	assert.Equal(t, int32(1), stsCalls.Load())
}

// TestResolveAWSCredentials_ErrorMessages: the two failure messages callers and logs match on
// are unchanged.
func TestResolveAWSCredentials_ErrorMessages(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	t.Run("config fails to load", func(t *testing.T) {
		fakeContainerCredentials(t)
		t.Setenv("AWS_PROFILE", "missing-profile")
		_, bifrostErr := ResolveAWSCredentials(ctx, &schemas.BedrockKeyConfig{}, testRegion("errors"))
		require.NotNil(t, bifrostErr)
		assert.Equal(t, "failed to load aws config", bifrostErr.Error.Message)
	})
	t.Run("credentials fail to resolve", func(t *testing.T) {
		fakeContainerCredentials(t)
		failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(failing.Close)
		t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", failing.URL)
		_, bifrostErr := ResolveAWSCredentials(ctx, &schemas.BedrockKeyConfig{}, testRegion("errors"))
		require.NotNil(t, bifrostErr)
		assert.Equal(t, "failed to retrieve aws credentials", bifrostErr.Error.Message)
	})
}

// TestSignAWSRequest_BodyHashAndHeaderDefaults: the payload hash is the body's SHA-256 and the
// body stays readable; Bedrock requests default to JSON, relayed ones are signed as sent.
func TestSignAWSRequest_BodyHashAndHeaderDefaults(t *testing.T) {
	fakeContainerCredentials(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	keyCfg := &schemas.BedrockKeyConfig{
		AccessKey: *schemas.NewSecretVar("AKIAIOSFODNN7EXAMPLE"),
		SecretKey: *schemas.NewSecretVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
	}
	body := []byte(`{"messages":[]}`)
	sum := sha256.Sum256(body)
	const target = "https://bedrock-runtime.us-east-1.amazonaws.com/model/m/converse"

	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	require.NoError(t, err)
	require.Nil(t, signAWSRequest(ctx, req, keyCfg, "us-east-1", bedrockSigningService))
	assert.Equal(t, hex.EncodeToString(sum[:]), req.Header.Get("X-Amz-Content-Sha256"))
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Equal(t, "application/json", req.Header.Get("Accept"))
	assert.Contains(t, req.Header.Get("Authorization"), "/us-east-1/bedrock/aws4_request")
	rest, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, body, rest)

	req, err = http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	require.Nil(t, SignAWSRequest(ctx, req, keyCfg, "us-east-1", "textract"))
	assert.Equal(t, "application/x-amz-json-1.1", req.Header.Get("Content-Type"))
	assert.Empty(t, req.Header.Get("Accept"), "a relayed request is signed as the caller sent it")

	req, err = http.NewRequest(http.MethodGet, target, nil)
	require.NoError(t, err)
	require.Nil(t, SignAWSRequest(ctx, req, keyCfg, "us-east-1", "textract"))
	assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", req.Header.Get("X-Amz-Content-Sha256"))
}

// BenchmarkResolveAWSCredentials is the per-request credential cost of a SigV4-signed call
// for each credential source a key can use.
func BenchmarkResolveAWSCredentials(b *testing.B) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	run := func(b *testing.B, keyCfg *schemas.BedrockKeyConfig, region string) {
		b.ReportAllocs()
		for b.Loop() {
			if _, bifrostErr := ResolveAWSCredentials(ctx, keyCfg, region); bifrostErr != nil {
				b.Fatal(bifrostErr.Error.Message)
			}
		}
	}
	b.Run("default_chain", func(b *testing.B) {
		fakeContainerCredentials(b)
		run(b, &schemas.BedrockKeyConfig{}, "us-bench-default-chain-1")
	})
	b.Run("static", func(b *testing.B) {
		fakeContainerCredentials(b)
		run(b, &schemas.BedrockKeyConfig{
			AccessKey: *schemas.NewSecretVar("AKIAIOSFODNN7EXAMPLE"),
			SecretKey: *schemas.NewSecretVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
		}, "us-bench-static-1")
	})
	b.Run("assume_role", func(b *testing.B) {
		fakeContainerCredentials(b)
		fakeSTS(b)
		run(b, &schemas.BedrockKeyConfig{RoleARN: schemas.NewSecretVar("arn:aws:iam::123456789012:role/bench")}, "us-bench-assume-role-1")
	})
}

// BenchmarkSignAWSRequest is the whole SigV4 cost of one request with static keys: body hash,
// credential lookup and signature.
func BenchmarkSignAWSRequest(b *testing.B) {
	fakeContainerCredentials(b)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	keyCfg := &schemas.BedrockKeyConfig{
		AccessKey: *schemas.NewSecretVar("AKIAIOSFODNN7EXAMPLE"),
		SecretKey: *schemas.NewSecretVar("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
	}
	body := []byte(`{"messages":[{"role":"user","content":[{"text":"hello"}]}]}`)
	b.ReportAllocs()
	for b.Loop() {
		req, err := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/model/m/converse", bytes.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}
		if bifrostErr := signAWSRequest(ctx, req, keyCfg, "us-east-1", bedrockSigningService); bifrostErr != nil {
			b.Fatal(bifrostErr.Error.Message)
		}
	}
}

// TestBedrockTransportUsesProxyConfig pins that Bedrock's net/http runtime client honours
// proxy_config. It used to hard-code http.ProxyFromEnvironment, so a proxy set on the
// provider reached only the fasthttp Mantle clients and runtime calls went direct.
func TestBedrockTransportUsesProxyConfig(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/model/x/converse", nil)
	require.NoError(t, err)

	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 300},
		ProxyConfig:   &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar("http://10.0.0.9:3128")},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, noopLogger{})
	require.NoError(t, err)
	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.Proxy)
	proxyURL, err := transport.Proxy(req)
	require.NoError(t, err)
	require.NotNil(t, proxyURL, "runtime requests must go through the configured proxy")
	assert.Equal(t, "10.0.0.9:3128", proxyURL.Host)

	// No proxy_config (or the UI's "none") keeps the environment-driven default.
	for _, proxyConfig := range []*schemas.ProxyConfig{nil, {Type: schemas.NoProxy}} {
		config := &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 300},
			ProxyConfig:   proxyConfig,
		}
		config.CheckAndSetDefaults()
		provider, err := NewBedrockProvider(config, noopLogger{})
		require.NoError(t, err)
		transport, ok := provider.client.Transport.(*http.Transport)
		require.True(t, ok)
		assert.NotNil(t, transport.Proxy, "unconfigured proxy must still honour HTTPS_PROXY")
	}
}

// TestBedrockTransportProxyMatrix pins the Bedrock runtime client's route for every
// combination of proxy_config source, proxy env vars and target, sending real requests
// through provider.client (the net/http client Bedrock keeps for HTTP/2) and the
// core/network/proxytest recorders. Bedrock follows net/http's rule for the
// environment: the variable is picked by scheme, and with no proxy_config it keeps
// proxying from the environment. Like every stack it reads the environment through
// golang.org/x/net's httpproxy, so the lowercase spelling wins when both are set. The
// fasthttp stacks are covered by TestProxyRoutingMatrix in core/providers/utils.
func TestBedrockTransportProxyMatrix(t *testing.T) {
	set := proxytest.NewSet(t)
	for _, source := range proxytest.Sources {
		for _, env := range proxytest.Envs {
			for _, target := range proxytest.Targets {
				t.Run(source.Name+"/"+env.Name+"/"+target.Name, func(t *testing.T) {
					set.Reset()
					proxytest.SetEnv(t, set, env, proxytest.TargetHost)
					want := proxytest.Expect(set, source, env, target, proxytest.ByScheme, true)

					config := &schemas.ProviderConfig{
						NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 5},
						ProxyConfig:   source.Config(set),
					}
					config.CheckAndSetDefaults()
					provider, err := NewBedrockProvider(config, noopLogger{})
					require.NoError(t, err)

					hostPort := net.JoinHostPort(proxytest.TargetHost, target.Port)
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.Scheme+"://"+hostPort+"/model/x/converse", nil)
					require.NoError(t, err)
					resp, err := provider.client.Do(req)
					if err == nil {
						resp.Body.Close()
					}
					proxytest.AssertRoute(t, set, want, hostPort, err)
				})
			}
		}
	}
}

// TestResponsesStreamAttachesEachRawEventOnce verifies a Converse event that expands into several Responses events
// rides on exactly one of them, the one streaming its text, declared as Bedrock's format, so its raw text never
// reaches the client ahead of (or apart from) the normalized text it backs.
func TestResponsesStreamAttachesEachRawEventOnce(t *testing.T) {
	events := []struct{ eventType, payload string }{
		{"messageStart", `{"role":"assistant"}`},
		{"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"mail alex@example.com"}}`},
		{"contentBlockStop", `{"contentBlockIndex":0}`},
		{"messageStop", `{"stopReason":"end_turn"}`},
		{"metadata", `{"usage":{"inputTokens":3,"outputTokens":4,"totalTokens":7},"metrics":{"latencyMs":10}}`},
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		for _, event := range events {
			writeEventStreamEvent(t, w, event.eventType, []byte(event.payload))
		}
	}))
	defer ts.Close()

	ctx := testBedrockCtx()
	ctx.SetValue(schemas.BifrostContextKeyCaptureRawResponse, true)
	req := testResponsesRequest()
	req.Model = testConverseStreamModel
	stream, bifrostErr := newTestProviderWithServer(t, ts).ResponsesStream(ctx, noopPostHookRunner, nil, testBedrockKey(), req)
	require.Nil(t, bifrostErr)

	carried := map[string]int{}
	var textCarrier *schemas.BifrostResponsesStreamResponse
	for chunk := range stream {
		event := chunk.BifrostResponsesStreamResponse
		if event == nil {
			continue
		}
		if raw, ok := event.ExtraFields.RawResponse.(string); ok {
			carried[raw]++
			if raw == events[1].payload {
				textCarrier = event
			}
		}
	}
	for _, event := range events {
		assert.LessOrEqual(t, carried[event.payload], 1, "%s raw attached more than once", event.eventType)
	}
	require.NotNil(t, textCarrier, "the text event's raw payload was not attached")
	assert.Equal(t, schemas.ResponsesStreamResponseTypeOutputTextDelta, textCarrier.Type)
	assert.Equal(t, schemas.RawResponseFormatBedrockConverseStreamEvent, textCarrier.ExtraFields.RawResponseFormat)

	redacted := *textCarrier
	redacted.Delta = schemas.Ptr("mail [EMAIL]")
	synced, err := schemas.SyncRawResponseText(textCarrier.ExtraFields.RawResponse, textCarrier.ExtraFields.RawResponseFormat, textCarrier, &redacted)
	require.NoError(t, err)
	assert.JSONEq(t, `{"contentBlockIndex":0,"delta":{"text":"mail [EMAIL]"}}`, synced.(string))
}

// TestConverseStreamRecordsEveryUpstreamEventRaw pins that the raw response a converse stream
// records carries every upstream event exactly once. Events that produce no Bifrost chunk of their
// own (messageStop, metadata) used to be dropped from the raw record, and the chunks emitted at the
// end of the stream carried a placeholder "{}" instead, so a logged stream lost its stop reason,
// its additionalModelResponseFields and its usage, and a missing field could not be told apart from
// one AWS never sent.
func TestConverseStreamRecordsEveryUpstreamEventRaw(t *testing.T) {
	cases := []struct {
		name     string
		payloads []string
		ordered  bool // false where a held event may only find a payload-free chunk after a later event
	}{
		{"one text block", []string{
			`{"p":"m0","role":"assistant"}`,
			`{"contentBlockIndex":0,"delta":{"text":"one, two, "},"p":"m1"}`,
			`{"contentBlockIndex":0,"p":"m2"}`,
			`{"additionalModelResponseFields":{"stop_sequence":"three"},"p":"m3","stopReason":"stop_sequence"}`,
			`{"metrics":{"latencyMs":760},"p":"m4","usage":{"inputTokens":32,"outputTokens":5,"totalTokens":37}}`,
		}, true},
		{"a held stop before more text", []string{
			`{"p":"n0","role":"assistant"}`,
			`{"contentBlockIndex":0,"delta":{"text":"first"},"p":"n1"}`,
			`{"contentBlockIndex":0,"p":"n2"}`,
			`{"contentBlockIndex":1,"delta":{"text":"second"},"p":"n3"}`,
			`{"contentBlockIndex":1,"p":"n4"}`,
			`{"p":"n5","stopReason":"end_turn"}`,
			`{"p":"n6","usage":{"inputTokens":3,"outputTokens":2,"totalTokens":5}}`,
		}, false},
	}
	for _, api := range []string{"chat", "responses"} {
		for _, tc := range cases {
			payloads := tc.payloads
			t.Run(api+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					writer.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
					writer.WriteHeader(http.StatusOK)
					encoder := eventstream.NewEncoder()
					for _, payload := range payloads {
						assert.NoError(t, encoder.Encode(writer, eventstream.Message{
							Headers: eventstream.Headers{
								{Name: ":message-type", Value: eventstream.StringValue("event")},
								{Name: ":event-type", Value: eventstream.StringValue("testEvent")},
							},
							Payload: []byte(payload),
						}))
					}
				}))
				defer server.Close()
				provider := newTestProviderWithServer(t, server)
				provider.sendBackRawResponse = true
				ctx := testBedrockCtx()
				var stream chan *schemas.BifrostStreamChunk
				var requestError *schemas.BifrostError
				if api == "chat" {
					request := testChatRequest()
					request.Model = testConverseStreamModel
					stream, requestError = provider.ChatCompletionStream(ctx, noopPostHookRunner, nil, testBedrockKey(), request)
				} else {
					request := testResponsesRequest()
					request.Model = testConverseStreamModel
					stream, requestError = provider.ResponsesStream(ctx, noopPostHookRunner, nil, testBedrockKey(), request)
				}
				require.Nil(t, requestError)
				var raws []string
				for chunk := range stream {
					require.Nil(t, chunk.BifrostError, "stream error: %+v", chunk.BifrostError)
					var raw interface{}
					switch {
					case chunk.BifrostChatResponse != nil:
						raw = chunk.BifrostChatResponse.ExtraFields.RawResponse
					case chunk.BifrostResponsesStreamResponse != nil:
						raw = chunk.BifrostResponsesStreamResponse.ExtraFields.RawResponse
					}
					if s, ok := raw.(string); ok && s != "" {
						raws = append(raws, s)
						var format schemas.RawResponseFormat
						if chunk.BifrostChatResponse != nil {
							format = chunk.BifrostChatResponse.ExtraFields.RawResponseFormat
						} else {
							format = chunk.BifrostResponsesStreamResponse.ExtraFields.RawResponseFormat
						}
						// Raw text plugins edit a declared stream event in place, so a chunk that declares
						// the format must carry exactly one event, the text it streams included.
						if format == schemas.RawResponseFormatBedrockConverseStreamEvent {
							assert.NotContains(t, s, "\n\n", "a chunk declaring one stream event carries several: %q", s)
						}
					}
				}
				recorded := strings.Join(raws, "\n")
				assert.NotContains(t, raws, "{}", "a placeholder stands in for an upstream event: %q", raws)
				last := -1
				for _, payload := range payloads {
					assert.Equal(t, 1, strings.Count(recorded, payload), "upstream event %s recorded %d times in %q", payload, strings.Count(recorded, payload), raws)
					if idx := strings.Index(recorded, payload); idx >= 0 && tc.ordered {
						assert.Greater(t, idx, last, "upstream event %s recorded out of order in %q", payload, raws)
						last = idx
					}
				}
			})
		}
	}
}

// BenchmarkHeldStreamRawStreamEnd measures what a converse stream with raw responses on now does
// at its end: hold the three payload-free events (contentBlockStop, messageStop, metadata) and hand
// them to the four closing chunks. With raw responses off none of this runs.
func BenchmarkHeldStreamRawStreamEnd(b *testing.B) {
	events := [][]byte{
		[]byte(`{"contentBlockIndex":0,"p":"abcdefghij"}`),
		[]byte(`{"additionalModelResponseFields":{"stop_sequence":"three"},"p":"abcdefghijklmnop","stopReason":"stop_sequence"}`),
		[]byte(`{"metrics":{"latencyMs":760},"p":"abcdefghijklmnopqrstuvwxyzABCDEF","usage":{"inputTokens":32,"outputTokens":5,"totalTokens":37}}`),
	}
	b.ReportAllocs()
	for b.Loop() {
		var held heldStreamRaw
		for _, e := range events {
			held.hold(e)
		}
		for i := 0; i < 4; i++ {
			var extra schemas.BifrostResponseExtraFields
			held.attach(&extra, i == 3)
		}
	}
}

// TestConverseSyntheticStructuredOutput_FinishReason pins the bf_so_* path's finish_reason: only a
// reply whose sole tool call is the synthetic tool becomes "stop"; a caller tool call keeps
// "tool_calls" so an agent loop runs the tool instead of stopping.
func TestConverseSyntheticStructuredOutput_FinishReason(t *testing.T) {
	soRequest := func() *schemas.BifrostChatRequest {
		req := testChatRequest()
		req.Model = testConverseStreamModel // ungated: keeps the synthetic tool
		responseFormat := any(map[string]any{"type": "json_schema", "json_schema": map[string]any{
			"name": "capital", "schema": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []string{"city"}},
		}})
		req.Params = &schemas.ChatParameters{
			ResponseFormat: &responseFormat,
			Tools: []schemas.ChatTool{{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{
				Name:       "lookup_capital",
				Parameters: &schemas.ToolFunctionParameters{Type: "object", Properties: schemas.NewOrderedMapFromPairs(schemas.KV("country", map[string]any{"type": "string"}))},
			}}},
		}
		return req
	}
	for name, tc := range map[string]struct{ tool, want string }{
		"caller tool keeps tool_calls": {"lookup_capital", "tool_calls"},
		"synthetic tool becomes stop":  {"bf_so_capital", "stop"},
	} {
		t.Run(name+"/chat", func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"output":{"message":{"role":"assistant","content":[{"toolUse":{"toolUseId":"t1","name":"` + tc.tool + `","input":{"country":"Australia","city":"Canberra"}}}]}},"stopReason":"tool_use","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`))
			}))
			defer ts.Close()
			resp, bifrostErr := newTestProviderWithServer(t, ts).ChatCompletion(testBedrockCtx(), testBedrockKey(), soRequest())
			require.Nil(t, bifrostErr)
			require.NotNil(t, resp.Choices[0].FinishReason)
			assert.Equal(t, tc.want, *resp.Choices[0].FinishReason)
		})
		t.Run(name+"/chat stream", func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
				writeEventStreamEvent(t, w, "messageStart", []byte(`{"role":"assistant"}`))
				writeEventStreamEvent(t, w, "contentBlockStart", []byte(`{"start":{"toolUse":{"toolUseId":"t1","name":"`+tc.tool+`"}},"contentBlockIndex":0}`))
				writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"toolUse":{"input":"{\"country\":\"Australia\"}"}},"contentBlockIndex":0}`))
				writeEventStreamEvent(t, w, "contentBlockStop", []byte(`{"contentBlockIndex":0}`))
				writeEventStreamEvent(t, w, "messageStop", []byte(`{"stopReason":"tool_use"}`))
				writeEventStreamEvent(t, w, "metadata", []byte(`{"usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2},"metrics":{"latencyMs":1}}`))
			}))
			defer ts.Close()
			stream, bifrostErr := newTestProviderWithServer(t, ts).ChatCompletionStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), soRequest())
			require.Nil(t, bifrostErr)
			var finish string
			for chunk := range stream {
				require.Nil(t, chunk.BifrostError)
				if chunk.BifrostChatResponse != nil {
					for _, c := range chunk.BifrostChatResponse.Choices {
						if c.FinishReason != nil {
							finish = *c.FinishReason
						}
					}
				}
			}
			assert.Equal(t, tc.want, finish)
		})
	}
}

// TestConverseSyntheticStructuredOutput_StreamCallerToolAfterSyntheticTool pins a reply that streams the
// bf_so_* block first and a caller tool block after it: the caller's argument deltas must stay a tool
// call, not be appended to the structured-output text.
func TestConverseSyntheticStructuredOutput_StreamCallerToolAfterSyntheticTool(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []string{"city"}}
	callerTool := schemas.ToolFunctionParameters{Type: "object", Properties: schemas.NewOrderedMapFromPairs(schemas.KV("country", map[string]any{"type": "string"}))}
	server := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
			writeEventStreamEvent(t, w, "messageStart", []byte(`{"role":"assistant"}`))
			writeEventStreamEvent(t, w, "contentBlockStart", []byte(`{"start":{"toolUse":{"toolUseId":"t0","name":"bf_so_capital"}},"contentBlockIndex":0}`))
			writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"toolUse":{"input":"{\"city\":\"Canberra\"}"}},"contentBlockIndex":0}`))
			writeEventStreamEvent(t, w, "contentBlockStop", []byte(`{"contentBlockIndex":0}`))
			writeEventStreamEvent(t, w, "contentBlockStart", []byte(`{"start":{"toolUse":{"toolUseId":"t1","name":"lookup_capital"}},"contentBlockIndex":1}`))
			writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"toolUse":{"input":"{\"country\":\"Australia\"}"}},"contentBlockIndex":1}`))
			writeEventStreamEvent(t, w, "contentBlockStop", []byte(`{"contentBlockIndex":1}`))
			writeEventStreamEvent(t, w, "messageStop", []byte(`{"stopReason":"tool_use"}`))
			writeEventStreamEvent(t, w, "metadata", []byte(`{"usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2},"metrics":{"latencyMs":1}}`))
		}))
	}

	t.Run("chat stream", func(t *testing.T) {
		ts := server()
		defer ts.Close()
		req := testChatRequest()
		req.Model = testConverseStreamModel // ungated: keeps the synthetic tool
		responseFormat := any(map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "capital", "schema": schema}})
		req.Params = &schemas.ChatParameters{ResponseFormat: &responseFormat, Tools: []schemas.ChatTool{{
			Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "lookup_capital", Parameters: &callerTool},
		}}}
		stream, bifrostErr := newTestProviderWithServer(t, ts).ChatCompletionStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), req)
		require.Nil(t, bifrostErr)
		var content, args strings.Builder
		var toolNames []string
		var finish string
		for chunk := range stream {
			require.Nil(t, chunk.BifrostError)
			if chunk.BifrostChatResponse == nil {
				continue
			}
			for _, c := range chunk.BifrostChatResponse.Choices {
				if c.FinishReason != nil {
					finish = *c.FinishReason
				}
				if c.ChatStreamResponseChoice == nil || c.Delta == nil {
					continue
				}
				if c.Delta.Content != nil {
					content.WriteString(*c.Delta.Content)
				}
				for _, tc := range c.Delta.ToolCalls {
					if tc.Function.Name != nil {
						toolNames = append(toolNames, *tc.Function.Name)
					}
					args.WriteString(tc.Function.Arguments)
				}
			}
		}
		assert.Equal(t, `{"city":"Canberra"}`, content.String(), "caller tool arguments leaked into content")
		assert.Equal(t, []string{"lookup_capital"}, toolNames)
		assert.Contains(t, args.String(), "Australia")
		assert.Equal(t, "tool_calls", finish)
	})

	t.Run("responses stream", func(t *testing.T) {
		ts := server()
		defer ts.Close()
		req := testResponsesRequest()
		req.Model = testConverseStreamModel
		req.Params = &schemas.ResponsesParameters{
			Text: &schemas.ResponsesTextConfig{Format: &schemas.ResponsesTextConfigFormat{
				Type: "json_schema", Name: schemas.Ptr("capital"), JSONSchema: schemas.JSONSchemaFromMap(schema),
			}},
			Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("lookup_capital"),
				ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: &callerTool}}},
		}
		stream, bifrostErr := newTestProviderWithServer(t, ts).ResponsesStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), req)
		require.Nil(t, bifrostErr)
		var text, args strings.Builder
		for chunk := range stream {
			require.Nil(t, chunk.BifrostError)
			resp := chunk.BifrostResponsesStreamResponse
			if resp == nil {
				continue
			}
			switch resp.Type {
			case schemas.ResponsesStreamResponseTypeOutputTextDelta:
				if resp.Delta != nil {
					text.WriteString(*resp.Delta)
				}
			case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
				if resp.Delta != nil {
					args.WriteString(*resp.Delta)
				}
			}
		}
		assert.Equal(t, `{"city":"Canberra"}`, text.String(), "caller tool arguments leaked into output_text")
		assert.Contains(t, args.String(), "Australia")
	})
}

// TestGrammarLimitRejectionMarksContext pins the trigger for the one tool-path retry: a 400 naming a
// grammar limit on a request sent with outputConfig marks the context, and nothing else does.
func TestGrammarLimitRejectionMarksContext(t *testing.T) {
	const native = `{"messages":[],"outputConfig":{"textFormat":{"type":"json_schema"}}}`
	cases := []struct {
		name, body, message string
		status              int
		marked              bool
	}{
		{"grammar too large", native, "The model returned the following errors: The compiled grammar is too large, which would cause performance issues.", 400, true},
		{"compilation timed out", native, "Grammar compilation timed out.", 400, true},
		{"schema too complex", native, "compilation exceeded limits: Schema is too complex", 400, true},
		{"tool path body", `{"messages":[],"toolConfig":{"tools":[]}}`, "The compiled grammar is too large", 400, false},
		{"schema shape rejection", native, "The model returned the following errors: output_config.format.schema: For 'object' type, property 'x-order' is not supported", 400, true},
		{"prefill rejection", native, "When using output format, pre-filling the `assistant` response is not supported.", 400, false},
		{"other status", native, "The compiled grammar is too large", 500, false},
	}
	for _, tc := range cases {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Amzn-Errortype", "ValidationException:http://internal.amazon.com/coral/com.amazon.bedrock/")
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"message":` + strconv.Quote(tc.message) + `}`))
		}))
		provider := newTestProviderWithServer(t, ts)
		for _, route := range []string{"unary", "stream"} {
			t.Run(tc.name+"/"+route, func(t *testing.T) {
				ctx := testBedrockCtx()
				var bifrostErr *schemas.BifrostError
				if route == "unary" {
					_, _, _, bifrostErr = provider.completeRequest(ctx, []byte(tc.body), "anthropic.claude-sonnet-4-5/converse", testBedrockKey(), "anthropic.claude-sonnet-4-5")
				} else {
					_, bifrostErr = provider.makeStreamingRequest(ctx, []byte(tc.body), testBedrockKey(), "anthropic.claude-sonnet-4-5", "converse-stream")
				}
				require.NotNil(t, bifrostErr)
				state := ctx.Value(schemas.BifrostContextKeyStructuredOutputGrammarRetry)
				if tc.marked {
					assert.Equal(t, schemas.StructuredOutputGrammarRejected, state)
				} else {
					assert.Nil(t, state)
				}
			})
			// The forced attempt is never marked again, whatever its body carries.
			t.Run(tc.name+"/"+route+"/already forced", func(t *testing.T) {
				ctx := testBedrockCtx()
				ctx.SetValue(schemas.BifrostContextKeyStructuredOutputGrammarRetry, schemas.StructuredOutputForceTool)
				if route == "unary" {
					_, _, _, _ = provider.completeRequest(ctx, []byte(tc.body), "anthropic.claude-sonnet-4-5/converse", testBedrockKey(), "anthropic.claude-sonnet-4-5")
				} else {
					_, _ = provider.makeStreamingRequest(ctx, []byte(tc.body), testBedrockKey(), "anthropic.claude-sonnet-4-5", "converse-stream")
				}
				assert.Equal(t, schemas.StructuredOutputForceTool, ctx.Value(schemas.BifrostContextKeyStructuredOutputGrammarRetry))
			})
		}
		ts.Close()
	}
}

// anthropicTextBlocks runs a Bedrock Responses stream through the /anthropic egress converter and
// returns the text of every text block that was opened with content_block_start and closed with
// content_block_stop, plus the text of deltas that arrived outside any opened block.
func anthropicTextBlocks(t *testing.T, stream chan *schemas.BifrostStreamChunk) (blocks []string, orphanText string) {
	t.Helper()
	egressCtx := testBedrockCtx()
	open := map[int]*strings.Builder{}
	var orphan strings.Builder
	for chunk := range stream {
		require.Nil(t, chunk.BifrostError)
		if chunk.BifrostResponsesStreamResponse == nil {
			continue
		}
		for _, ev := range anthropic.ToAnthropicResponsesStreamResponse(egressCtx, chunk.BifrostResponsesStreamResponse) {
			switch ev.Type {
			case anthropic.AnthropicStreamEventTypeContentBlockStart:
				if ev.ContentBlock != nil && ev.ContentBlock.Type == anthropic.AnthropicContentBlockTypeText {
					open[*ev.Index] = &strings.Builder{}
				}
			case anthropic.AnthropicStreamEventTypeContentBlockDelta:
				if ev.Delta == nil || ev.Delta.Text == nil {
					continue
				}
				if b, ok := open[*ev.Index]; ok {
					b.WriteString(*ev.Delta.Text)
				} else {
					orphan.WriteString(*ev.Delta.Text)
				}
			case anthropic.AnthropicStreamEventTypeContentBlockStop:
				if b, ok := open[*ev.Index]; ok {
					blocks = append(blocks, b.String())
					delete(open, *ev.Index)
				}
			}
		}
	}
	return blocks, orphan.String()
}

// TestConverseSyntheticStructuredOutput_AnthropicEgressTextBlock pins GH #10128 / #8158: a streamed
// /anthropic/v1/messages request with output_config.format on a Bedrock Claude model that takes the
// synthetic bf_so_* tool must reach the client as one well-formed text block.
func TestConverseSyntheticStructuredOutput_AnthropicEgressTextBlock(t *testing.T) {
	const answer = `{"city":"Canberra","country":"Australia"}`
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"city": map[string]any{"type": "string"}, "country": map[string]any{"type": "string"},
	}, "required": []string{"city", "country"}, "additionalProperties": false}
	soRequest := func(model string) *schemas.BifrostResponsesRequest {
		req := testResponsesRequest()
		req.Model = model
		req.Params = &schemas.ResponsesParameters{Text: &schemas.ResponsesTextConfig{Format: &schemas.ResponsesTextConfigFormat{
			Type: "json_schema", JSONSchema: schemas.JSONSchemaFromMap(schema),
		}}}
		return req
	}

	t.Run("bf_so tool deltas become one text block", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
			writeEventStreamEvent(t, w, "messageStart", []byte(`{"role":"assistant"}`))
			writeEventStreamEvent(t, w, "contentBlockStart", []byte(`{"start":{"toolUse":{"toolUseId":"t0","name":"bf_so_json_response"}},"contentBlockIndex":0}`))
			for _, part := range []string{``, `{\"city\"`, `:\"Canberra\",\"country\":\"Australia\"}`} {
				writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"toolUse":{"input":"`+part+`"}},"contentBlockIndex":0}`))
			}
			writeEventStreamEvent(t, w, "contentBlockStop", []byte(`{"contentBlockIndex":0}`))
			writeEventStreamEvent(t, w, "messageStop", []byte(`{"stopReason":"tool_use"}`))
			writeEventStreamEvent(t, w, "metadata", []byte(`{"usage":{"inputTokens":1,"outputTokens":58,"totalTokens":59},"metrics":{"latencyMs":1}}`))
		}))
		defer ts.Close()
		stream, bifrostErr := newTestProviderWithServer(t, ts).ResponsesStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), soRequest("anthropic.claude-3-5-sonnet-20241022-v2:0"))
		require.Nil(t, bifrostErr)
		blocks, orphan := anthropicTextBlocks(t, stream)
		assert.Empty(t, orphan, "text_delta events arrived without a content_block_start for their index")
		assert.Equal(t, []string{answer}, blocks)
	})

	t.Run("unpinned plain text answer still streams", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
			writeEventStreamEvent(t, w, "messageStart", []byte(`{"role":"assistant"}`))
			writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"text":"{\"city\":\"Canberra\","},"contentBlockIndex":0}`))
			writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"text":"\"country\":\"Australia\"}"},"contentBlockIndex":0}`))
			writeEventStreamEvent(t, w, "contentBlockStop", []byte(`{"contentBlockIndex":0}`))
			writeEventStreamEvent(t, w, "messageStop", []byte(`{"stopReason":"end_turn"}`))
			writeEventStreamEvent(t, w, "metadata", []byte(`{"usage":{"inputTokens":1,"outputTokens":75,"totalTokens":76},"metrics":{"latencyMs":1}}`))
		}))
		defer ts.Close()
		req := soRequest("anthropic.claude-3-7-sonnet-20250219-v1:0")
		req.Params.Reasoning = &schemas.ResponsesParametersReasoning{MaxTokens: schemas.Ptr(1024)} // thinking leaves bf_so_* unpinned
		stream, bifrostErr := newTestProviderWithServer(t, ts).ResponsesStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), req)
		require.Nil(t, bifrostErr)
		blocks, orphan := anthropicTextBlocks(t, stream)
		assert.Empty(t, orphan)
		assert.Equal(t, []string{answer}, blocks, "the model's text answer was suppressed")
	})

	t.Run("prose before the bf_so tool is still dropped", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
			writeEventStreamEvent(t, w, "messageStart", []byte(`{"role":"assistant"}`))
			writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"text":"Here is the JSON:"},"contentBlockIndex":0}`))
			writeEventStreamEvent(t, w, "contentBlockStop", []byte(`{"contentBlockIndex":0}`))
			writeEventStreamEvent(t, w, "contentBlockStart", []byte(`{"start":{"toolUse":{"toolUseId":"t1","name":"bf_so_json_response"}},"contentBlockIndex":1}`))
			writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"toolUse":{"input":"{\"city\":\"Canberra\",\"country\":\"Australia\"}"}},"contentBlockIndex":1}`))
			writeEventStreamEvent(t, w, "contentBlockStop", []byte(`{"contentBlockIndex":1}`))
			writeEventStreamEvent(t, w, "messageStop", []byte(`{"stopReason":"tool_use"}`))
			writeEventStreamEvent(t, w, "metadata", []byte(`{"usage":{"inputTokens":1,"outputTokens":60,"totalTokens":61},"metrics":{"latencyMs":1}}`))
		}))
		defer ts.Close()
		req := soRequest("anthropic.claude-3-7-sonnet-20250219-v1:0")
		req.Params.Reasoning = &schemas.ResponsesParametersReasoning{MaxTokens: schemas.Ptr(1024)}
		stream, bifrostErr := newTestProviderWithServer(t, ts).ResponsesStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), req)
		require.Nil(t, bifrostErr)
		blocks, orphan := anthropicTextBlocks(t, stream)
		assert.Empty(t, orphan)
		assert.Equal(t, []string{answer}, blocks, "preamble leaked into the structured output")
	})

	// Unpinned bf_so_* (thinking on) answered in plain text: the held text events must still carry
	// their upstream payload once released.
	plainTextServer := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
			writeEventStreamEvent(t, w, "messageStart", []byte(`{"role":"assistant"}`))
			writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"text":"{\"city\":\"Canberra\","},"contentBlockIndex":0}`))
			writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"text":"\"country\":\"Australia\"}"},"contentBlockIndex":0}`))
			writeEventStreamEvent(t, w, "contentBlockStop", []byte(`{"contentBlockIndex":0}`))
			writeEventStreamEvent(t, w, "messageStop", []byte(`{"stopReason":"end_turn"}`))
			writeEventStreamEvent(t, w, "metadata", []byte(`{"usage":{"inputTokens":1,"outputTokens":75,"totalTokens":76},"metrics":{"latencyMs":1}}`))
		}))
	}
	t.Run("released plain text keeps its raw payload", func(t *testing.T) {
		ts := plainTextServer()
		defer ts.Close()
		provider := newTestProviderWithServer(t, ts)
		provider.sendBackRawResponse = true
		req := soRequest("anthropic.claude-3-7-sonnet-20250219-v1:0")
		req.Params.Reasoning = &schemas.ResponsesParametersReasoning{MaxTokens: schemas.Ptr(1024)}
		stream, bifrostErr := provider.ResponsesStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), req)
		require.Nil(t, bifrostErr)
		var raws []string
		for chunk := range stream {
			require.Nil(t, chunk.BifrostError)
			if resp := chunk.BifrostResponsesStreamResponse; resp != nil && resp.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta {
				raw, _ := resp.ExtraFields.RawResponse.(string)
				raws = append(raws, raw)
			}
		}
		require.Len(t, raws, 2)
		assert.Contains(t, raws[0], `Canberra`, "first released text delta lost its upstream payload")
		assert.Contains(t, raws[1], `Australia`, "second released text delta lost its upstream payload")
	})

	t.Run("held text keeps its place before a caller tool", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
			writeEventStreamEvent(t, w, "messageStart", []byte(`{"role":"assistant"}`))
			writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"text":"Let me look that up."},"contentBlockIndex":0}`))
			writeEventStreamEvent(t, w, "contentBlockStop", []byte(`{"contentBlockIndex":0}`))
			writeEventStreamEvent(t, w, "contentBlockStart", []byte(`{"start":{"toolUse":{"toolUseId":"t1","name":"lookup_capital"}},"contentBlockIndex":1}`))
			writeEventStreamEvent(t, w, "contentBlockDelta", []byte(`{"delta":{"toolUse":{"input":"{\"country\":\"Australia\"}"}},"contentBlockIndex":1}`))
			writeEventStreamEvent(t, w, "contentBlockStop", []byte(`{"contentBlockIndex":1}`))
			writeEventStreamEvent(t, w, "messageStop", []byte(`{"stopReason":"tool_use"}`))
			writeEventStreamEvent(t, w, "metadata", []byte(`{"usage":{"inputTokens":1,"outputTokens":20,"totalTokens":21},"metrics":{"latencyMs":1}}`))
		}))
		defer ts.Close()
		req := soRequest("anthropic.claude-3-7-sonnet-20250219-v1:0")
		req.Params.Reasoning = &schemas.ResponsesParametersReasoning{MaxTokens: schemas.Ptr(1024)}
		req.Params.Tools = []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("lookup_capital"),
			ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: &schemas.ToolFunctionParameters{
				Type: "object", Properties: schemas.NewOrderedMapFromPairs(schemas.KV("country", map[string]any{"type": "string"})),
			}}}}
		stream, bifrostErr := newTestProviderWithServer(t, ts).ResponsesStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), req)
		require.Nil(t, bifrostErr)
		egressCtx := testBedrockCtx()
		var starts []anthropic.AnthropicContentBlockType
		for chunk := range stream {
			require.Nil(t, chunk.BifrostError)
			if chunk.BifrostResponsesStreamResponse == nil {
				continue
			}
			for _, ev := range anthropic.ToAnthropicResponsesStreamResponse(egressCtx, chunk.BifrostResponsesStreamResponse) {
				if ev.Type == anthropic.AnthropicStreamEventTypeContentBlockStart && ev.ContentBlock != nil {
					starts = append(starts, ev.ContentBlock.Type)
				}
			}
		}
		assert.Equal(t, []anthropic.AnthropicContentBlockType{anthropic.AnthropicContentBlockTypeText, anthropic.AnthropicContentBlockTypeToolUse}, starts,
			"text block 0 must reach the client before caller tool block 1")
	})
}

// TestConverseSyntheticStructuredOutput_ChatStreamUnpinnedTool pins the /v1/chat/completions side of
// GH #10128: with bf_so_* unpinned (thinking on), a plain-text answer must stream as content, prose
// before the synthetic tool must still be dropped, and text before a caller tool keeps its place.
func TestConverseSyntheticStructuredOutput_ChatStreamUnpinnedTool(t *testing.T) {
	const answer = `{"city":"Canberra","country":"Australia"}`
	soRequest := func(withCallerTool bool) *schemas.BifrostChatRequest {
		req := testChatRequest()
		req.Model = "anthropic.claude-3-7-sonnet-20250219-v1:0"
		responseFormat := any(map[string]any{"type": "json_schema", "json_schema": map[string]any{
			"name": "capital", "schema": map[string]any{"type": "object", "properties": map[string]any{
				"city": map[string]any{"type": "string"}, "country": map[string]any{"type": "string"},
			}, "required": []string{"city", "country"}},
		}})
		req.Params = &schemas.ChatParameters{ResponseFormat: &responseFormat, Reasoning: &schemas.ChatReasoning{MaxTokens: schemas.Ptr(1024)}}
		if withCallerTool {
			req.Params.Tools = []schemas.ChatTool{{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{
				Name:       "lookup_capital",
				Parameters: &schemas.ToolFunctionParameters{Type: "object", Properties: schemas.NewOrderedMapFromPairs(schemas.KV("country", map[string]any{"type": "string"}))},
			}}}
		}
		return req
	}
	type event struct{ kind, payload string }
	serve := func(events ...event) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
			for _, e := range events {
				writeEventStreamEvent(t, w, e.kind, []byte(e.payload))
			}
		}))
	}
	// collect returns the stream as an ordered list of "content:<text>" / "tool:<name>" entries
	// (consecutive content merged) plus the raw payloads carried by content chunks.
	collect := func(t *testing.T, provider *BedrockProvider, req *schemas.BifrostChatRequest) (order []string, raws []string, finish string) {
		t.Helper()
		stream, bifrostErr := provider.ChatCompletionStream(testBedrockCtx(), noopPostHookRunner, nil, testBedrockKey(), req)
		require.Nil(t, bifrostErr)
		for chunk := range stream {
			require.Nil(t, chunk.BifrostError)
			if chunk.BifrostChatResponse == nil {
				continue
			}
			for _, c := range chunk.BifrostChatResponse.Choices {
				if c.FinishReason != nil {
					finish = *c.FinishReason
				}
				if c.ChatStreamResponseChoice == nil || c.Delta == nil {
					continue
				}
				if c.Delta.Content != nil && *c.Delta.Content != "" {
					raw, _ := chunk.BifrostChatResponse.ExtraFields.RawResponse.(string)
					raws = append(raws, raw)
					if n := len(order); n > 0 && strings.HasPrefix(order[n-1], "content:") {
						order[n-1] += *c.Delta.Content
					} else {
						order = append(order, "content:"+*c.Delta.Content)
					}
				}
				for _, tc := range c.Delta.ToolCalls {
					if tc.Function.Name != nil {
						order = append(order, "tool:"+*tc.Function.Name)
					}
				}
			}
		}
		return order, raws, finish
	}
	plainText := []event{
		{"messageStart", `{"role":"assistant"}`},
		{"contentBlockDelta", `{"delta":{"text":"{\"city\":\"Canberra\","},"contentBlockIndex":0}`},
		{"contentBlockDelta", `{"delta":{"text":"\"country\":\"Australia\"}"},"contentBlockIndex":0}`},
		{"contentBlockStop", `{"contentBlockIndex":0}`},
		{"messageStop", `{"stopReason":"end_turn"}`},
		{"metadata", `{"usage":{"inputTokens":1,"outputTokens":75,"totalTokens":76},"metrics":{"latencyMs":1}}`},
	}

	t.Run("plain text answer streams as content", func(t *testing.T) {
		ts := serve(plainText...)
		defer ts.Close()
		order, _, finish := collect(t, newTestProviderWithServer(t, ts), soRequest(false))
		assert.Equal(t, []string{"content:" + answer}, order, "the model's text answer was suppressed")
		assert.Equal(t, "stop", finish)
	})

	t.Run("released plain text keeps its raw payload", func(t *testing.T) {
		ts := serve(plainText...)
		defer ts.Close()
		provider := newTestProviderWithServer(t, ts)
		provider.sendBackRawResponse = true
		_, raws, _ := collect(t, provider, soRequest(false))
		require.Len(t, raws, 2)
		assert.Contains(t, raws[0], "Canberra")
		assert.Contains(t, raws[1], "Australia")
	})

	t.Run("prose before the bf_so tool is still dropped", func(t *testing.T) {
		ts := serve(
			event{"messageStart", `{"role":"assistant"}`},
			event{"contentBlockDelta", `{"delta":{"text":"Here is the JSON:"},"contentBlockIndex":0}`},
			event{"contentBlockStop", `{"contentBlockIndex":0}`},
			event{"contentBlockStart", `{"start":{"toolUse":{"toolUseId":"t1","name":"bf_so_capital"}},"contentBlockIndex":1}`},
			event{"contentBlockDelta", `{"delta":{"toolUse":{"input":"{\"city\":\"Canberra\",\"country\":\"Australia\"}"}},"contentBlockIndex":1}`},
			event{"contentBlockStop", `{"contentBlockIndex":1}`},
			event{"messageStop", `{"stopReason":"tool_use"}`},
			event{"metadata", `{"usage":{"inputTokens":1,"outputTokens":60,"totalTokens":61},"metrics":{"latencyMs":1}}`},
		)
		defer ts.Close()
		order, _, finish := collect(t, newTestProviderWithServer(t, ts), soRequest(false))
		assert.Equal(t, []string{"content:" + answer}, order, "preamble leaked into the structured output")
		assert.Equal(t, "stop", finish)
	})

	t.Run("held text keeps its place before a caller tool", func(t *testing.T) {
		ts := serve(
			event{"messageStart", `{"role":"assistant"}`},
			event{"contentBlockDelta", `{"delta":{"text":"Let me look that up."},"contentBlockIndex":0}`},
			event{"contentBlockStop", `{"contentBlockIndex":0}`},
			event{"contentBlockStart", `{"start":{"toolUse":{"toolUseId":"t1","name":"lookup_capital"}},"contentBlockIndex":1}`},
			event{"contentBlockDelta", `{"delta":{"toolUse":{"input":"{\"country\":\"Australia\"}"}},"contentBlockIndex":1}`},
			event{"contentBlockStop", `{"contentBlockIndex":1}`},
			event{"messageStop", `{"stopReason":"tool_use"}`},
			event{"metadata", `{"usage":{"inputTokens":1,"outputTokens":20,"totalTokens":21},"metrics":{"latencyMs":1}}`},
		)
		defer ts.Close()
		order, _, finish := collect(t, newTestProviderWithServer(t, ts), soRequest(true))
		assert.Equal(t, []string{"content:Let me look that up.", "tool:lookup_capital"}, order)
		assert.Equal(t, "tool_calls", finish)
	})
}

var holdConverseEventSink heldConverseEvent

// BenchmarkHoldConverseEvent prices the per-event work the bf_so_* stream path adds when bf_so_* is
// unpinned and the model streams text: classify the event, then hold a copy (plus its payload when
// raw responses are on). Requests without structured output never reach this code.
func BenchmarkHoldConverseEvent(b *testing.B) {
	payload := []byte(`{"contentBlockIndex":0,"delta":{"text":"{\"city\":\"Canberra\","},"p":"abcdefghij"}`)
	text := `{"city":"Canberra",`
	ev := BedrockStreamEvent{ContentBlockIndex: schemas.Ptr(0), Delta: &BedrockContentBlockDelta{Text: &text}}
	blocks := map[int]struct{}{0: {}}
	for _, sendRaw := range []bool{false, true} {
		b.Run("sendRaw="+strconv.FormatBool(sendRaw), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if isTextBlockEvent(&ev, blocks) {
					holdConverseEventSink = holdConverseEvent(&ev, payload, sendRaw, true)
				}
			}
		})
	}
}
