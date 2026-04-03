package bedrock

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

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

	provider.client = &http.Client{
		Transport: &redirectTransport{
			target:    targetURL,
			transport: ts.Client().Transport,
		},
		Timeout: 5 * time.Second,
	}
	return provider
}

// testBedrockKey returns a minimal Key with a bearer value so makeStreamingRequest
// skips IAM signing and proceeds to the HTTP call.
func testBedrockKey() schemas.Key {
	region := schemas.NewEnvVar("us-east-1")
	return schemas.Key{
		Value: *schemas.NewEnvVar("test-api-key"),
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

	_, _, bifrostErr := provider.makeStreamingRequest(ctx, []byte(`{}`), key, "anthropic.claude-sonnet-4-5", "converse-stream")

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

	streamChan, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHookRunner, key, testChatRequest())

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
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Write a partial EventStream frame header (3 bytes, not a valid frame).
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
	ts.Start()
	defer ts.Close()

	provider := newTestProviderWithServer(t, ts)
	ctx := testBedrockCtx()
	key := testBedrockKey()

	streamChan, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHookRunner, key, testChatRequest())
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
			DefaultRequestTimeoutInSeconds: 30,
			MaxConnsPerHost:                5000,
			EnforceHTTP2:                   true,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, nil)
	require.NoError(t, err)
	require.NotNil(t, provider)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok, "transport should be *http.Transport")

	assert.Equal(t, 5000, transport.MaxConnsPerHost)
	assert.Equal(t, schemas.DefaultMaxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
	assert.Equal(t, schemas.DefaultMaxIdleConnsPerHost, transport.MaxIdleConns)
	assert.True(t, transport.ForceAttemptHTTP2)
}

func TestBedrockTransportCustomMaxConns(t *testing.T) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 30,
			MaxConnsPerHost:                50,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, nil)
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
			DefaultRequestTimeoutInSeconds: 30,
			// MaxConnsPerHost left as 0 — should default to 5000
		},
	}
	config.CheckAndSetDefaults()

	assert.Equal(t, schemas.DefaultMaxConnsPerHost, config.NetworkConfig.MaxConnsPerHost)

	provider, err := NewBedrockProvider(config, nil)
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
			DefaultRequestTimeoutInSeconds: 30,
			InsecureSkipVerify:             true,
			EnforceHTTP2:                   true,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, nil)
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.TLSClientConfig)
	assert.True(t, transport.TLSClientConfig.InsecureSkipVerify)
	assert.Equal(t, uint16(tls.VersionTLS12), transport.TLSClientConfig.MinVersion)
	// ForceAttemptHTTP2 should still be true even with custom TLS config
	assert.True(t, transport.ForceAttemptHTTP2)
}

func TestBedrockTransportTLSCACert(t *testing.T) {
	testCACert := generateTestCACert(t)

	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 30,
			CACertPEM:                      testCACert,
			EnforceHTTP2:                   true,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, nil)
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
			DefaultRequestTimeoutInSeconds: 30,
			// No TLS settings — should use system defaults
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, nil)
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
			DefaultRequestTimeoutInSeconds: 30,
			EnforceHTTP2:                   true,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, nil)
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.True(t, transport.ForceAttemptHTTP2)
	// TLSNextProto should NOT be set when HTTP/2 is enforced, allowing ALPN negotiation
	assert.Nil(t, transport.TLSNextProto)
}

func TestBedrockTransportEnforceHTTP2Disabled(t *testing.T) {
	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 30,
			EnforceHTTP2:                   false,
		},
	}
	config.CheckAndSetDefaults()

	provider, err := NewBedrockProvider(config, nil)
	require.NoError(t, err)

	transport, ok := provider.client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.False(t, transport.ForceAttemptHTTP2)
	// TLSNextProto must be set to empty map to truly disable HTTP/2 ALPN negotiation
	assert.NotNil(t, transport.TLSNextProto)
	assert.Empty(t, transport.TLSNextProto)
}
