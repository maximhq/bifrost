package network

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/network/proxytest"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TestStaleConnectionRetryIfErr validates the error-matching logic of
// StaleConnectionRetryIfErr for different error types and attempt counts.
func TestStaleConnectionRetryIfErr(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		attempts  int
		wantReset bool
		wantRetry bool
	}{
		{
			name:      "retries on whitespace error (first attempt)",
			err:       fmt.Errorf(`error when reading response headers: cannot find whitespace in the first line of response "217\r\ndata: ..."`),
			attempts:  1,
			wantReset: true,
			wantRetry: true,
		},
		{
			name:      "retries on connection reset by peer",
			err:       fmt.Errorf("read tcp 10.0.0.1:54321->10.0.0.2:443: read: connection reset by peer"),
			attempts:  1,
			wantReset: true,
			wantRetry: true,
		},
		{
			name:      "retries on io.EOF (server closed connection)",
			err:       io.EOF,
			attempts:  1,
			wantReset: true,
			wantRetry: true,
		},
		{
			name:      "retries on wrapped io.EOF",
			err:       fmt.Errorf("read response: %w", io.EOF),
			attempts:  1,
			wantReset: true,
			wantRetry: true,
		},
		{
			name:      "retries on unexpected EOF",
			err:       io.ErrUnexpectedEOF,
			attempts:  1,
			wantReset: true,
			wantRetry: true,
		},
		{
			name:      "retries on wrapped unexpected EOF",
			err:       fmt.Errorf("read response: %w", io.ErrUnexpectedEOF),
			attempts:  1,
			wantReset: true,
			wantRetry: true,
		},
		{
			name:      "retries on broken pipe (write to closed connection)",
			err:       fmt.Errorf("write tcp 10.0.0.1:53374->10.0.0.2:30000: write: broken pipe"),
			attempts:  1,
			wantReset: true,
			wantRetry: true,
		},
		{
			name:      "retries on use of closed network connection",
			err:       fmt.Errorf("read tcp 10.0.0.1:53374->10.0.0.2:443: use of closed network connection"),
			attempts:  1,
			wantReset: true,
			wantRetry: true,
		},
		{
			name:      "retries on server closed connection",
			err:       fmt.Errorf("server closed connection before returning the first response byte"),
			attempts:  1,
			wantReset: true,
			wantRetry: true,
		},
		{
			// fasthttp.ErrConnectionClosed is treated as retryable: it means the server
			// closed an idle keep-alive connection before sending any response byte (a
			// stale connection). In fasthttp v1.68.0 the callback actually receives raw
			// io.EOF — the sentinel is only produced AFTER the retry loop (client.go:1413) —
			// but we match it explicitly to stay correct if a future version surfaces it.
			name:      "retries on fasthttp.ErrConnectionClosed sentinel",
			err:       fasthttp.ErrConnectionClosed,
			attempts:  1,
			wantReset: true,
			wantRetry: true,
		},
		{
			name:      "retries on second stale-connection attempt",
			err:       io.EOF,
			attempts:  2,
			wantReset: true,
			wantRetry: true,
		},
		{
			name:      "does not retry after max stale-connection attempts",
			err:       io.EOF,
			attempts:  4,
			wantReset: false,
			wantRetry: false,
		},
		{
			name:      "does not retry on nil error",
			err:       nil,
			attempts:  1,
			wantReset: false,
			wantRetry: false,
		},
		{
			name:      "does not retry on unrelated error",
			err:       fmt.Errorf("dial tcp: lookup api.example.com: no such host"),
			attempts:  1,
			wantReset: false,
			wantRetry: false,
		},
		{
			name:      "does not retry on timeout",
			err:       fasthttp.ErrTimeout,
			attempts:  1,
			wantReset: false,
			wantRetry: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetTimeout, retry := StaleConnectionRetryIfErr(nil, tt.attempts, tt.err)
			if resetTimeout != tt.wantReset {
				t.Errorf("resetTimeout = %v, want %v", resetTimeout, tt.wantReset)
			}
			if retry != tt.wantRetry {
				t.Errorf("retry = %v, want %v", retry, tt.wantRetry)
			}
		})
	}
}

// TestStaleConnectionRetryWithTTLMismatch simulates the scenario from issue #1613:
//
//   - Server idle timeout: 10 seconds (server closes keep-alive connections after 10s idle)
//   - Client MaxIdleConnDuration: 15 seconds (client holds connections for 15s)
//
// Between 10-15 seconds of idle time, the client still considers the connection
// valid, but the server has already closed it. The next request on the stale
// connection should be retried automatically via StaleConnectionRetryIfErr.
//
// Without the retry, POST requests fail because fasthttp's default isIdempotent
// only retries GET/HEAD/PUT.
func TestStaleConnectionRetryWithTTLMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TTL mismatch test in short mode (requires 11s wait)")
	}

	const (
		serverIdleTimeout = 10 * time.Second
		clientIdleTimeout = 15 * time.Second
		waitBetween       = 11 * time.Second // > server TTL, < client TTL
	)

	var requestCount atomic.Int32

	// Start a test server with a 10-second idle timeout.
	// After 10s of idle time on a keep-alive connection, the server closes it.
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: {\"message\": \"ok\", \"request\": %d}\n\n", requestCount.Load())
	}))
	server.Config.IdleTimeout = serverIdleTimeout
	server.Start()
	defer server.Close()

	t.Run("with_retry_policy_POST_succeeds", func(t *testing.T) {
		client := &fasthttp.Client{
			MaxIdleConnDuration: clientIdleTimeout,
			MaxConnsPerHost:     10,
			RetryIfErr:          StaleConnectionRetryIfErr,
		}

		// --- First request: fresh connection, must succeed ---
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)

		req.SetRequestURI(server.URL)
		req.Header.SetMethod(http.MethodPost)
		req.Header.SetContentType("application/json")
		req.SetBodyString(`{"prompt": "hello"}`)

		if err := client.Do(req, resp); err != nil {
			t.Fatalf("First POST request failed: %v", err)
		}
		if resp.StatusCode() != 200 {
			t.Fatalf("First POST request: expected 200, got %d", resp.StatusCode())
		}

		// Read body to ensure connection is returned to pool
		_ = resp.Body()
		t.Logf("First POST request succeeded (status=%d)", resp.StatusCode())

		// --- Wait for server's idle timeout to expire ---
		// The server will close the connection after 10s, but the client
		// still holds it in its pool (MaxIdleConnDuration=15s).
		t.Logf("Waiting %v for server idle timeout (%v) to expire...", waitBetween, serverIdleTimeout)
		time.Sleep(waitBetween)

		// --- Second request: stale connection, should retry and succeed ---
		req2 := fasthttp.AcquireRequest()
		resp2 := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req2)
		defer fasthttp.ReleaseResponse(resp2)

		req2.SetRequestURI(server.URL)
		req2.Header.SetMethod(http.MethodPost)
		req2.Header.SetContentType("application/json")
		req2.SetBodyString(`{"prompt": "world"}`)

		if err := client.Do(req2, resp2); err != nil {
			t.Fatalf("Second POST request failed (StaleConnectionRetryIfErr should have retried): %v", err)
		}
		if resp2.StatusCode() != 200 {
			t.Fatalf("Second POST request: expected 200, got %d", resp2.StatusCode())
		}
		t.Logf("Second POST request succeeded after TTL mismatch (status=%d)", resp2.StatusCode())
	})

	t.Run("without_retry_policy_POST_fails", func(t *testing.T) {
		// Reset request count
		requestCount.Store(0)

		client := &fasthttp.Client{
			MaxIdleConnDuration: clientIdleTimeout,
			MaxConnsPerHost:     10,
			// No RetryIfErr — uses default isIdempotent (POST not retried)
		}

		// --- First request: fresh connection, must succeed ---
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)

		req.SetRequestURI(server.URL)
		req.Header.SetMethod(http.MethodPost)
		req.Header.SetContentType("application/json")
		req.SetBodyString(`{"prompt": "hello"}`)

		if err := client.Do(req, resp); err != nil {
			t.Fatalf("First POST request failed: %v", err)
		}
		if resp.StatusCode() != 200 {
			t.Fatalf("First POST request: expected 200, got %d", resp.StatusCode())
		}
		_ = resp.Body()
		t.Logf("First POST request succeeded (status=%d)", resp.StatusCode())

		// --- Wait for server's idle timeout to expire ---
		t.Logf("Waiting %v for server idle timeout (%v) to expire...", waitBetween, serverIdleTimeout)
		time.Sleep(waitBetween)

		// --- Second request: stale connection, POST NOT retried by default ---
		req2 := fasthttp.AcquireRequest()
		resp2 := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req2)
		defer fasthttp.ReleaseResponse(resp2)

		req2.SetRequestURI(server.URL)
		req2.Header.SetMethod(http.MethodPost)
		req2.Header.SetContentType("application/json")
		req2.SetBodyString(`{"prompt": "world"}`)

		err := client.Do(req2, resp2)
		if err != nil {
			// Expected: POST request fails on stale connection without retry
			t.Logf("Second POST request failed as expected without retry policy: %v", err)
		} else {
			// The OS may have already delivered the FIN and fasthttp detected it,
			// creating a new connection transparently. This is acceptable — the
			// retry policy provides defense-in-depth for cases where FIN delivery
			// is delayed (common with TLS, proxies, and load balancers in K8s).
			t.Logf("Second POST request succeeded (OS delivered FIN before reuse) — retry policy still provides defense-in-depth")
		}
	})
}

// TestMaxConnDurationForcesReconnection verifies that MaxConnDuration causes
// fasthttp to close and replace connections after the configured lifetime,
// preventing stale long-lived connections from accumulating during sustained
// back-to-back request traffic.
//
// Uses the server's ConnState callback to reliably count new TCP connections
// (r.RemoteAddr is unreliable because the OS can reuse ephemeral ports).
func TestMaxConnDurationForcesReconnection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping MaxConnDuration test in short mode (requires ~4s wait)")
	}

	const maxConnDuration = 2 * time.Second

	// Track new connections via ConnState (fires once per new TCP accept)
	var newConnCount atomic.Int32

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConnCount.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	t.Run("with_MaxConnDuration_connection_is_recycled", func(t *testing.T) {
		newConnCount.Store(0)

		client := &fasthttp.Client{
			MaxConnsPerHost: 1,
			MaxConnDuration: maxConnDuration,
		}

		// First request: establishes connection A
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)

		req.SetRequestURI(server.URL)
		req.Header.SetMethod(http.MethodPost)
		req.SetBodyString(`{"test": 1}`)

		if err := client.Do(req, resp); err != nil {
			t.Fatalf("First request failed: %v", err)
		}
		_ = resp.Body()

		connsAfterFirst := newConnCount.Load()
		t.Logf("After first request: %d new connections", connsAfterFirst)

		// Wait for MaxConnDuration to expire
		t.Logf("Waiting %v for MaxConnDuration to expire...", maxConnDuration+500*time.Millisecond)
		time.Sleep(maxConnDuration + 500*time.Millisecond)

		// Second request: reuses connection A but sends Connection: close
		// (fasthttp's MaxConnDuration sets Connection: close on expired conns,
		// telling the server to close the connection after the response)
		req2 := fasthttp.AcquireRequest()
		resp2 := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req2)
		defer fasthttp.ReleaseResponse(resp2)

		req2.SetRequestURI(server.URL)
		req2.Header.SetMethod(http.MethodPost)
		req2.SetBodyString(`{"test": 2}`)

		if err := client.Do(req2, resp2); err != nil {
			t.Fatalf("Second request failed: %v", err)
		}
		_ = resp2.Body()

		// Third request: connection A is now closed by server → must create connection B
		req3 := fasthttp.AcquireRequest()
		resp3 := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req3)
		defer fasthttp.ReleaseResponse(resp3)

		req3.SetRequestURI(server.URL)
		req3.Header.SetMethod(http.MethodPost)
		req3.SetBodyString(`{"test": 3}`)

		if err := client.Do(req3, resp3); err != nil {
			t.Fatalf("Third request failed: %v", err)
		}

		connsAfterThird := newConnCount.Load()
		if connsAfterThird < 2 {
			t.Errorf("expected at least 2 new connections after MaxConnDuration recycling, got %d", connsAfterThird)
		} else {
			t.Logf("Connection recycled: %d total new connections", connsAfterThird)
		}
	})

	t.Run("without_MaxConnDuration_connection_is_reused", func(t *testing.T) {
		newConnCount.Store(0)

		client := &fasthttp.Client{
			MaxConnsPerHost: 1,
			// No MaxConnDuration — connections live forever
		}

		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)

		req.SetRequestURI(server.URL)
		req.Header.SetMethod(http.MethodPost)
		req.SetBodyString(`{"test": 1}`)

		if err := client.Do(req, resp); err != nil {
			t.Fatalf("First request failed: %v", err)
		}
		_ = resp.Body()

		// Wait same duration as above
		time.Sleep(maxConnDuration + 500*time.Millisecond)

		req2 := fasthttp.AcquireRequest()
		resp2 := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req2)
		defer fasthttp.ReleaseResponse(resp2)

		req2.SetRequestURI(server.URL)
		req2.Header.SetMethod(http.MethodPost)
		req2.SetBodyString(`{"test": 2}`)

		if err := client.Do(req2, resp2); err != nil {
			t.Fatalf("Second request failed: %v", err)
		}

		totalConns := newConnCount.Load()
		// Without MaxConnDuration, the same connection should be reused
		if totalConns == 1 {
			t.Logf("Connection reused as expected: only 1 new connection total")
		} else {
			// OS/server may have closed it — that's acceptable
			t.Logf("Saw %d new connections (OS/server may have recycled)", totalConns)
		}
	})
}

// TestMaxConnWaitTimeoutAlignedWithReadTimeout verifies that when the connection
// pool is exhausted, requests wait for MaxConnWaitTimeout (aligned with ReadTimeout)
// before failing, not the old hardcoded 10s.
func TestMaxConnWaitTimeoutAlignedWithReadTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping pool exhaustion test in short mode (requires ~4s wait)")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hold the connection for 3 seconds to simulate a slow provider
		time.Sleep(3 * time.Second)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	client := &fasthttp.Client{
		MaxConnsPerHost:    1,               // Only 1 connection allowed — second request must wait
		MaxConnWaitTimeout: 2 * time.Second, // Wait up to 2s for a free connection slot
		ReadTimeout:        5 * time.Second,
		WriteTimeout:       5 * time.Second,
	}

	// Fire first request (occupies the only connection slot for 3s)
	var wg sync.WaitGroup
	firstReqErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)

		req.SetRequestURI(server.URL)
		req.Header.SetMethod(http.MethodPost)
		req.SetBodyString(`{"slot": "occupied"}`)

		firstReqErr <- client.Do(req, resp)
	}()

	// Brief pause to ensure first request is in-flight
	time.Sleep(100 * time.Millisecond)

	// Second request: pool is full, should timeout after ~2s (MaxConnWaitTimeout)
	start := time.Now()
	req2 := fasthttp.AcquireRequest()
	resp2 := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req2)
	defer fasthttp.ReleaseResponse(resp2)

	req2.SetRequestURI(server.URL)
	req2.Header.SetMethod(http.MethodPost)
	req2.SetBodyString(`{"waiting": true}`)

	err := client.Do(req2, resp2)
	elapsed := time.Since(start)

	wg.Wait()

	if firstErr := <-firstReqErr; firstErr != nil {
		t.Fatalf("first request failed; pool-exhaustion scenario was not exercised: %v", firstErr)
	}

	if err == nil {
		// The first request may have finished before MaxConnWaitTimeout expired,
		// allowing the second request to succeed. This is acceptable.
		t.Logf("Second request succeeded (first request completed in time, elapsed=%v)", elapsed)
		return
	}

	// Verify the wait time is close to MaxConnWaitTimeout (2s), not 0s or 5s
	if elapsed < 1500*time.Millisecond || elapsed > 3500*time.Millisecond {
		t.Errorf("expected pool wait ~2s, but elapsed=%v (err=%v)", elapsed, err)
	} else {
		t.Logf("Pool exhaustion timeout at %v as expected (err=%v)", elapsed, err)
	}
}

// TestDefaultClientConfigValues verifies that DefaultClientConfig contains
// the expected values for connection pool settings.
func TestDefaultClientConfigValues(t *testing.T) {
	if DefaultClientConfig.ReadTimeout != 60*time.Second {
		t.Errorf("ReadTimeout = %v, want 60s", DefaultClientConfig.ReadTimeout)
	}
	if DefaultClientConfig.WriteTimeout != 60*time.Second {
		t.Errorf("WriteTimeout = %v, want 60s", DefaultClientConfig.WriteTimeout)
	}
	if DefaultClientConfig.MaxIdleConnDuration != 30*time.Second {
		t.Errorf("MaxIdleConnDuration = %v, want 30s", DefaultClientConfig.MaxIdleConnDuration)
	}
	if DefaultClientConfig.MaxConnDuration != 300*time.Second {
		t.Errorf("MaxConnDuration = %v, want 300s", DefaultClientConfig.MaxConnDuration)
	}
	if DefaultClientConfig.MaxConnsPerHost != 200 {
		t.Errorf("MaxConnsPerHost = %d, want 200", DefaultClientConfig.MaxConnsPerHost)
	}
	// Verify the provider-level constant matches
	if schemas.DefaultMaxConnDurationInSeconds != 300 {
		t.Errorf("DefaultMaxConnDurationInSeconds = %d, want 300", schemas.DefaultMaxConnDurationInSeconds)
	}
}

// TestCreateFasthttpClientPoolSettings verifies that the HTTPClientFactory
// creates fasthttp clients with the correct pool settings including
// MaxConnDuration, MaxConnWaitTimeout, and FIFO ConnPoolStrategy.
func TestCreateFasthttpClientPoolSettings(t *testing.T) {
	factory := NewHTTPClientFactory(nil, nil)
	client := factory.GetFasthttpClient(ClientPurposeInference)

	if client.MaxConnDuration != DefaultClientConfig.MaxConnDuration {
		t.Errorf("MaxConnDuration = %v, want %v", client.MaxConnDuration, DefaultClientConfig.MaxConnDuration)
	}
	if client.MaxConnWaitTimeout != DefaultClientConfig.ReadTimeout {
		t.Errorf("MaxConnWaitTimeout = %v, want %v (aligned with ReadTimeout)", client.MaxConnWaitTimeout, DefaultClientConfig.ReadTimeout)
	}
	if client.ConnPoolStrategy != fasthttp.FIFO {
		t.Errorf("ConnPoolStrategy = %v, want FIFO (%v)", client.ConnPoolStrategy, fasthttp.FIFO)
	}
	if client.MaxIdleConnDuration != DefaultClientConfig.MaxIdleConnDuration {
		t.Errorf("MaxIdleConnDuration = %v, want %v", client.MaxIdleConnDuration, DefaultClientConfig.MaxIdleConnDuration)
	}
	if client.MaxConnsPerHost != DefaultClientConfig.MaxConnsPerHost {
		t.Errorf("MaxConnsPerHost = %d, want %d", client.MaxConnsPerHost, DefaultClientConfig.MaxConnsPerHost)
	}
}

// TestFactoryFasthttpProxyReachesIPv6ProxyAndHonoursNoProxyList pins two things on the
// factory's fasthttp proxy dialer (SCIM, guardrails and other API clients):
//   - a proxy given as an IPv6 literal is reachable. The non-DualStack fasthttpproxy
//     constructors dial the proxy over tcp4 only and fail with "couldn't find dns entries".
//   - every entry of a comma-separated no_proxy list is honoured, not just a
//     single-entry list.
func TestFactoryFasthttpProxyReachesIPv6ProxyAndHonoursNoProxyList(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatalf("listen on IPv6 loopback: %v", err)
	}
	var mu sync.Mutex
	var targets []string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		targets = append(targets, r.Host)
		mu.Unlock()
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
			conn.Close()
		}
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())

	factory := NewHTTPClientFactory(&GlobalProxyConfig{
		Enabled:      true,
		Type:         GlobalProxyTypeHTTP,
		URL:          "http://[::1]:" + port,
		NoProxy:      "other.test, bypass.bifrost.test",
		EnableForAPI: true,
	}, nil)
	client := factory.GetFasthttpClient(ClientPurposeAPI)
	send := func(url string) {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)
		req.SetRequestURI(url)
		// The recorder closes every tunnel, and a direct dial to a .test name has
		// nowhere to go, so only the proxy's log tells the cases apart.
		_ = client.DoTimeout(req, resp, 3*time.Second)
	}

	send("https://api.bifrost.test/v1/check")
	// The second no_proxy entry must bypass the proxy.
	send("https://bypass.bifrost.test/v1/check")

	mu.Lock()
	defer mu.Unlock()
	if len(targets) != 1 || targets[0] != "api.bifrost.test:443" {
		t.Fatalf("proxy saw %v, want exactly [api.bifrost.test:443]", targets)
	}
}

// factoryMatrixSource is one state of the global proxy config for a purpose.
type factoryMatrixSource struct {
	name   string
	config func(s *proxytest.Set, purpose ClientPurpose) *GlobalProxyConfig
	route  proxytest.Route
}

// enabledFor returns a global proxy config enabled only for purpose.
func enabledFor(purpose ClientPurpose, proxyType GlobalProxyType, proxyURL string) *GlobalProxyConfig {
	return &GlobalProxyConfig{
		Enabled:            true,
		Type:               proxyType,
		URL:                proxyURL,
		EnableForSCIM:      purpose == ClientPurposeSCIM,
		EnableForAPI:       purpose == ClientPurposeAPI,
		EnableForInference: purpose == ClientPurposeInference,
	}
}

func otherPurpose(purpose ClientPurpose) ClientPurpose {
	if purpose == ClientPurposeAPI {
		return ClientPurposeSCIM
	}
	return ClientPurposeAPI
}

var factoryMatrixSources = []factoryMatrixSource{
	{name: "no-global-proxy", config: func(*proxytest.Set, ClientPurpose) *GlobalProxyConfig { return nil }},
	{name: "disabled", config: func(s *proxytest.Set, p ClientPurpose) *GlobalProxyConfig {
		cfg := enabledFor(p, GlobalProxyTypeHTTP, "http://127.0.0.1:"+s.Config.Port())
		cfg.Enabled = false
		return cfg
	}},
	{name: "enabled-for-other-purpose", config: func(s *proxytest.Set, p ClientPurpose) *GlobalProxyConfig {
		return enabledFor(otherPurpose(p), GlobalProxyTypeHTTP, "http://127.0.0.1:"+s.Config.Port())
	}},
	{name: "http-ip", route: proxytest.Route{Proxy: "config"}, config: func(s *proxytest.Set, p ClientPurpose) *GlobalProxyConfig {
		return enabledFor(p, GlobalProxyTypeHTTP, "http://127.0.0.1:"+s.Config.Port())
	}},
	{name: "http-hostname", route: proxytest.Route{Proxy: "config"}, config: func(s *proxytest.Set, p ClientPurpose) *GlobalProxyConfig {
		return enabledFor(p, GlobalProxyTypeHTTP, "http://localhost:"+s.Config.Port())
	}},
	{name: "http-ipv6", route: proxytest.Route{Proxy: "config6"}, config: func(s *proxytest.Set, p ClientPurpose) *GlobalProxyConfig {
		return enabledFor(p, GlobalProxyTypeHTTP, "http://[::1]:"+s.Config6.Port())
	}},
	{name: "http-credentials", route: proxytest.Route{Proxy: "config", Auth: proxytest.BasicAuth}, config: func(s *proxytest.Set, p ClientPurpose) *GlobalProxyConfig {
		cfg := enabledFor(p, GlobalProxyTypeHTTP, "http://127.0.0.1:"+s.Config.Port())
		cfg.Username, cfg.Password = proxytest.User, proxytest.Pass
		return cfg
	}},
	{name: "socks5", route: proxytest.Route{Proxy: "socks"}, config: func(s *proxytest.Set, p ClientPurpose) *GlobalProxyConfig {
		return enabledFor(p, GlobalProxyTypeSOCKS5, "socks5://127.0.0.1:"+s.Socks.Port())
	}},
	{name: "socks5-credentials", route: proxytest.Route{Proxy: "socks", Auth: proxytest.SOCKSAuth(proxytest.User, proxytest.Pass)}, config: func(s *proxytest.Set, p ClientPurpose) *GlobalProxyConfig {
		cfg := enabledFor(p, GlobalProxyTypeSOCKS5, "socks5://127.0.0.1:"+s.Socks.Port())
		cfg.Username, cfg.Password = proxytest.User, proxytest.Pass
		return cfg
	}},
	{name: "socks5-rejected", route: proxytest.Route{Proxy: "socks", Auth: proxytest.SOCKSAuth(proxytest.User, proxytest.RejectedPass), MustFail: true}, config: func(s *proxytest.Set, p ClientPurpose) *GlobalProxyConfig {
		cfg := enabledFor(p, GlobalProxyTypeSOCKS5, "socks5://127.0.0.1:"+s.Socks.Port())
		cfg.Username, cfg.Password = proxytest.User, proxytest.RejectedPass
		return cfg
	}},
	{name: "http+no_proxy", config: func(s *proxytest.Set, p ClientPurpose) *GlobalProxyConfig {
		cfg := enabledFor(p, GlobalProxyTypeHTTP, "http://127.0.0.1:"+s.Config.Port())
		cfg.NoProxy = "other.example, " + proxytest.TargetHost + ", " + proxytest.FetchTargetHost
		return cfg
	}},
}

// sendFactoryMatrixRequest sends one request for targetURL through a client of kind and
// returns its error. The route is read from the recorders.
func sendFactoryMatrixRequest(t *testing.T, factory *HTTPClientFactory, purpose ClientPurpose, kind, targetURL, hostPort string, expectDirect bool) error {
	t.Helper()
	timeout := 3 * time.Second
	if expectDirect && kind == "ssrf" {
		// A direct fetch to the documentation-range target never connects.
		timeout = 150 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	switch kind {
	case "fasthttp":
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)
		req.SetRequestURI(targetURL)
		return factory.GetFasthttpClient(purpose).DoTimeout(req, resp, timeout)
	case "grpc":
		conn, err := factory.GRPCDialer(purpose)(ctx, hostPort)
		if err == nil {
			conn.Close()
		}
		return err
	case "http", "tls", "ssrf":
		client := factory.GetHTTPClient(purpose)
		switch kind {
		case "tls":
			client = factory.HTTPClientWithTLS(purpose, factoryMatrixTLS)
		case "ssrf":
			client = &http.Client{Transport: factory.PolicyTransport(purpose, factoryMatrixSSRFPolicy)}
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	t.Fatalf("unknown client kind %q", kind)
	return nil
}

// factoryMatrixSSRFPolicy is the policy the "ssrf" client kind enforces.
var factoryMatrixSSRFPolicy = SSRFPolicy(nil)

// factoryMatrixTLS stands in for a caller's own TLS settings (HTTPClientWithTLS).
var factoryMatrixTLS = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "custom.example"}

// TestHTTPClientFactoryProxyMatrix pins, for every purpose and every kind of client the
// factory hands out, where a request goes for every global proxy state: which proxy saw
// it, for which target, with which credentials, that it went direct, or that it failed
// rather than going direct when the proxy refused the login.
func TestHTTPClientFactoryProxyMatrix(t *testing.T) {
	set := proxytest.NewSet(t)
	for _, purpose := range []ClientPurpose{ClientPurposeSCIM, ClientPurposeAPI, ClientPurposeInference} {
		for _, source := range factoryMatrixSources {
			for _, kind := range []string{"fasthttp", "http", "tls", "grpc", "ssrf"} {
				for _, scheme := range []string{"https", "http"} {
					t.Run(fmt.Sprintf("%s/%s/%s/%s", purpose, source.name, kind, scheme), func(t *testing.T) {
						set.Reset()
						host := proxytest.TargetHost
						if kind == "ssrf" {
							host = proxytest.FetchTargetHost
						}
						port := "443"
						if scheme == "http" {
							port = "80"
						}
						hostPort := net.JoinHostPort(host, port)
						factory := NewHTTPClientFactory(source.config(set, purpose), noopTestLogger{})
						err := sendFactoryMatrixRequest(t, factory, purpose, kind, scheme+"://"+hostPort+"/resource", hostPort, source.route.Proxy == "")
						proxytest.AssertRoute(t, set, source.route, hostPort, err)
					})
				}
			}
		}
	}
}

// TestHTTPClientFactoryClientsAreLive pins that a client handed out before a proxy
// change follows it: the same object goes direct, then through the proxy, then direct
// again. Callers keep factory clients for their whole life (alerting senders, JWT
// validators, guardrail providers), so a client that captured the proxy at creation
// would silently ignore every later change.
func TestHTTPClientFactoryClientsAreLive(t *testing.T) {
	set := proxytest.NewSet(t)
	factory := NewHTTPClientFactory(nil, noopTestLogger{})
	hostPort := net.JoinHostPort(proxytest.TargetHost, "443")
	targetURL := "https://" + hostPort + "/resource"
	proxied := enabledFor(ClientPurposeAPI, GlobalProxyTypeHTTP, "http://127.0.0.1:"+set.Config.Port())

	fasthttpClient := factory.GetFasthttpClient(ClientPurposeAPI)
	httpClient := factory.GetHTTPClient(ClientPurposeAPI)
	tlsClient := factory.HTTPClientWithTLS(ClientPurposeAPI, factoryMatrixTLS)
	dialer := factory.GRPCDialer(ClientPurposeAPI)

	for _, step := range []struct {
		name   string
		config *GlobalProxyConfig
		route  proxytest.Route
	}{
		{"before any proxy", nil, proxytest.Direct},
		{"after enabling the proxy", proxied, proxytest.Route{Proxy: "config"}},
		{"after disabling it again", nil, proxytest.Direct},
	} {
		factory.UpdateProxyConfig(step.config)
		for _, kind := range []string{"fasthttp", "http", "tls", "grpc"} {
			set.Reset()
			err := sendFactoryMatrixRequest(t, factory, ClientPurposeAPI, kind, targetURL, hostPort, step.route.Proxy == "")
			t.Run(step.name+"/"+kind, func(t *testing.T) { proxytest.AssertRoute(t, set, step.route, hostPort, err) })
		}
	}

	if factory.GetFasthttpClient(ClientPurposeAPI) != fasthttpClient || factory.GetHTTPClient(ClientPurposeAPI) != httpClient ||
		factory.HTTPClientWithTLS(ClientPurposeAPI, factoryMatrixTLS) != tlsClient {
		t.Error("the factory must hand out the same client objects across proxy changes")
	}
	_ = dialer
}

// hangingProxy accepts connections and never answers.
func hangingProxy(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var conns []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
	})
	return "http://" + listener.Addr().String()
}

// TestHTTPClientFactoryLiveClientsKeepTimeouts pins that the live wrappers do not lose
// deadlines: fasthttp's DoTimeout (kept on the request) and the global proxy timeout on
// the net/http client both still end a request stuck on an unresponsive proxy.
func TestHTTPClientFactoryLiveClientsKeepTimeouts(t *testing.T) {
	cfg := enabledFor(ClientPurposeAPI, GlobalProxyTypeHTTP, hangingProxy(t))
	cfg.Timeout = 1
	factory := NewHTTPClientFactory(cfg, noopTestLogger{})

	start := time.Now()
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI("https://api.bifrost.test/resource")
	if err := factory.GetFasthttpClient(ClientPurposeAPI).DoTimeout(req, resp, 200*time.Millisecond); err == nil {
		t.Error("fasthttp request through a hanging proxy succeeded")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("DoTimeout(200ms) took %v through the live client", elapsed)
	}

	start = time.Now()
	if resp, err := factory.GetHTTPClient(ClientPurposeAPI).Get("https://api.bifrost.test/resource"); err == nil {
		resp.Body.Close()
		t.Error("net/http request through a hanging proxy succeeded")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the 1s global proxy timeout took %v to end the request", elapsed)
	}
}

// TestHTTPClientFactoryNeverProxiesLocalOrMetadata pins that local and instance-metadata
// targets connect directly on every client kind, even with the proxy on.
func TestHTTPClientFactoryNeverProxiesLocalOrMetadata(t *testing.T) {
	for _, host := range []string{"169.254.169.254", "metadata.google.internal", "100.100.100.200", "fd00:ec2::254", "localhost", "127.0.0.1", "::1"} {
		if !IsLocalOrMetadataHost(host) {
			t.Errorf("IsLocalOrMetadataHost(%q) = false, want true", host)
		}
	}
	for _, host := range []string{"login.microsoftonline.com", "10.0.0.5", "203.0.113.10"} {
		if IsLocalOrMetadataHost(host) {
			t.Errorf("IsLocalOrMetadataHost(%q) = true, want false", host)
		}
	}

	set := proxytest.NewSet(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer target.Close()
	factory := NewHTTPClientFactory(enabledFor(ClientPurposeSCIM, GlobalProxyTypeHTTP, "http://127.0.0.1:"+set.Config.Port()), noopTestLogger{})

	resp, err := factory.GetHTTPClient(ClientPurposeSCIM).Get(target.URL)
	if err != nil {
		t.Fatalf("net/http request to a loopback target failed: %v", err)
	}
	resp.Body.Close()

	req := fasthttp.AcquireRequest()
	fresp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(fresp)
	req.SetRequestURI(target.URL)
	if err := factory.GetFasthttpClient(ClientPurposeSCIM).DoTimeout(req, fresp, 3*time.Second); err != nil {
		t.Fatalf("fasthttp request to a loopback target failed: %v", err)
	}

	conn, err := factory.GRPCDialer(ClientPurposeSCIM)(t.Context(), strings.TrimPrefix(target.URL, "http://"))
	if err != nil {
		t.Fatalf("gRPC dial to a loopback target failed: %v", err)
	}
	conn.Close()

	if seen := set.Config.Seen(); len(seen) != 0 {
		t.Errorf("the proxy saw %+v, want no requests for a loopback target", seen)
	}
}

// noopTestLogger satisfies schemas.Logger for factories built in tests.
type noopTestLogger struct{}

func (noopTestLogger) Debug(string, ...any)                   {}
func (noopTestLogger) Info(string, ...any)                    {}
func (noopTestLogger) Warn(string, ...any)                    {}
func (noopTestLogger) Error(string, ...any)                   {}
func (noopTestLogger) Fatal(string, ...any)                   {}
func (noopTestLogger) SetLevel(schemas.LogLevel)              {}
func (noopTestLogger) SetOutputType(schemas.LoggerOutputType) {}
func (noopTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// TestDefaultHTTPClientFactoryRouting pins the process defaults (DefaultTransport,
// DefaultProxyFunc, DefaultGRPCDialer) that outbound call sites without a factory handle
// use: they follow the registered factory's global proxy for the purpose, fall back to
// http.DefaultTransport's own selector when the global proxy is off or no factory is
// registered, and resolve the factory per request, so clients built before the server
// registers it still follow it.
func TestDefaultHTTPClientFactoryRouting(t *testing.T) {
	set := proxytest.NewSet(t)
	t.Cleanup(func() { SetDefaultHTTPClientFactory(nil) })

	// Stand-in for an environment proxy: the selector http.DefaultTransport carries.
	envProxy, _ := url.Parse("http://127.0.0.1:" + set.EnvHTTPS.Port())
	defaultTransport := http.DefaultTransport.(*http.Transport)
	previous := defaultTransport.Proxy
	defaultTransport.Proxy = http.ProxyURL(envProxy)
	t.Cleanup(func() { defaultTransport.Proxy = previous })

	// Built before any factory is registered.
	client := &http.Client{Transport: DefaultTransport(ClientPurposeAPI)}
	proxyFunc := DefaultProxyFunc(ClientPurposeAPI)
	dialer := DefaultGRPCDialer(ClientPurposeAPI)
	req, _ := http.NewRequest(http.MethodGet, "https://api.bifrost.test/v1", nil)

	send := func() {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		r, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.bifrost.test/v1", nil)
		if resp, err := client.Do(r); err == nil {
			resp.Body.Close()
		}
	}

	// No factory: exactly http.DefaultTransport.
	set.Reset()
	send()
	proxytest.AssertRoute(t, set, proxytest.Route{Proxy: "env-https"}, "api.bifrost.test:443", nil)
	if got, _ := proxyFunc(req); got == nil || got.Host != envProxy.Host {
		t.Errorf("no factory: DefaultProxyFunc = %v, want the DefaultTransport selector's %v", got, envProxy)
	}

	// Factory with the global proxy on for API: every default follows it.
	SetDefaultHTTPClientFactory(NewHTTPClientFactory(&GlobalProxyConfig{
		Enabled: true, Type: GlobalProxyTypeHTTP, URL: "http://127.0.0.1:" + set.Config.Port(),
		NoProxy: "bypass.bifrost.test", EnableForAPI: true,
	}, noopTestLogger{}))
	set.Reset()
	send()
	proxytest.AssertRoute(t, set, proxytest.Route{Proxy: "config"}, "api.bifrost.test:443", nil)
	if got, _ := proxyFunc(req); got == nil || got.Port() != set.Config.Port() {
		t.Errorf("global proxy on: DefaultProxyFunc = %v, want the global proxy", got)
	}
	for _, host := range []string{"bypass.bifrost.test", "169.254.169.254", "localhost"} {
		direct, _ := http.NewRequest(http.MethodGet, "https://"+host+"/v1", nil)
		if got, _ := proxyFunc(direct); got != nil {
			t.Errorf("DefaultProxyFunc(%s) = %v, want a direct connection", host, got)
		}
	}
	set.Reset()
	if conn, err := dialer(t.Context(), "api.bifrost.test:443"); err == nil {
		conn.Close()
	}
	proxytest.AssertRoute(t, set, proxytest.Route{Proxy: "config"}, "api.bifrost.test:443", nil)

	// Factory with the global proxy off for API: back to DefaultTransport's selector.
	SetDefaultHTTPClientFactory(NewHTTPClientFactory(&GlobalProxyConfig{
		Enabled: true, Type: GlobalProxyTypeHTTP, URL: "http://127.0.0.1:" + set.Config.Port(), EnableForSCIM: true,
	}, noopTestLogger{}))
	set.Reset()
	send()
	proxytest.AssertRoute(t, set, proxytest.Route{Proxy: "env-https"}, "api.bifrost.test:443", nil)
}

// TestGRPCDialersConnectUnixSockets pins that the gRPC dialers connect a unix-socket
// target to the socket itself, never over TCP or through a proxy, with or without a
// registered factory and with the global proxy on. GRPCPassthroughTarget leaves unix:
// targets alone, and gRPC then hands a custom dialer "unix:///abs/path" or
// "unix:relative-path" (abstract sockets arrive as "\x00name"); an OTel collector
// listening on a unix socket must keep working once the OTel plugin uses these dialers.
func TestGRPCDialersConnectUnixSockets(t *testing.T) {
	set := proxytest.NewSet(t)
	t.Cleanup(func() { SetDefaultHTTPClientFactory(nil) })
	// A short directory: unix socket paths are capped near 104 bytes on macOS.
	dir, err := os.MkdirTemp("", "grpcsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "otel.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen on %s: %v", socket, err)
	}
	t.Cleanup(func() { listener.Close() })
	var accepted atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			conn.Close()
		}
	}()
	t.Chdir(dir)
	addrs := []string{"unix://" + socket, "unix:otel.sock"}
	if runtime.GOOS == "linux" {
		abstract, err := net.Listen("unix", "@bifrost-grpc-dialer-test")
		if err != nil {
			t.Fatalf("listen on abstract socket: %v", err)
		}
		t.Cleanup(func() { abstract.Close() })
		go func() {
			for {
				conn, err := abstract.Accept()
				if err != nil {
					return
				}
				accepted.Add(1)
				conn.Close()
			}
		}()
		addrs = append(addrs, "\x00bifrost-grpc-dialer-test")
	}

	proxied := NewHTTPClientFactory(&GlobalProxyConfig{
		Enabled: true, Type: GlobalProxyTypeHTTP, URL: "http://127.0.0.1:" + set.Config.Port(), EnableForAPI: true,
	}, noopTestLogger{})
	dialers := []struct {
		name  string
		setup func()
		dial  func(context.Context, string) (net.Conn, error)
	}{
		{"default dialer, no factory", func() { SetDefaultHTTPClientFactory(nil) }, DefaultGRPCDialer(ClientPurposeAPI)},
		{"default dialer, global proxy on", func() { SetDefaultHTTPClientFactory(proxied) }, DefaultGRPCDialer(ClientPurposeAPI)},
		{"factory dialer, global proxy on", func() {}, proxied.GRPCDialer(ClientPurposeAPI)},
	}
	for _, d := range dialers {
		for _, addr := range addrs {
			t.Run(d.name+"/"+strings.ReplaceAll(addr, "\x00", "@"), func(t *testing.T) {
				d.setup()
				set.Reset()
				before := accepted.Load()
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				conn, err := d.dial(ctx, addr)
				if err != nil {
					t.Fatalf("dial %q: %v", addr, err)
				}
				conn.Close()
				deadline := time.Now().Add(2 * time.Second)
				for accepted.Load() == before && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if accepted.Load() == before {
					t.Fatalf("dial %q did not reach the unix socket", addr)
				}
				proxytest.AssertRoute(t, set, proxytest.Direct, "", nil)
			})
		}
	}
}

func TestGRPCPassthroughTarget(t *testing.T) {
	tests := map[string]string{
		"collector.example:4317":       "passthrough:///collector.example:4317",
		"10.0.0.9:4317":                "passthrough:///10.0.0.9:4317",
		"[::1]:4317":                   "passthrough:///[::1]:4317",
		"dns:///collector.example:443": "dns:///collector.example:443",
		"unix:/var/run/otel.sock":      "unix:/var/run/otel.sock",
	}
	for endpoint, want := range tests {
		if got := GRPCPassthroughTarget(endpoint); got != want {
			t.Errorf("GRPCPassthroughTarget(%q) = %q, want %q", endpoint, got, want)
		}
	}
}

// TestHTTPClientFactoryApplyOptions pins that options applied to a factory after it
// handed out clients reach them: enterprise adds its SCIM header buffer sizes to the
// factory the config loader built.
func TestHTTPClientFactoryApplyOptions(t *testing.T) {
	factory := NewHTTPClientFactory(nil, noopTestLogger{})
	live := factory.GetFasthttpClient(ClientPurposeSCIM)
	if inner := factory.currentFasthttpClient(ClientPurposeSCIM); inner.ReadBufferSize != 0 {
		t.Fatalf("ReadBufferSize = %d before any option, want fasthttp's default (0)", inner.ReadBufferSize)
	}
	factory.ApplyOptions(WithFasthttpBufferSizes(64*1024, 32*1024))
	inner := factory.currentFasthttpClient(ClientPurposeSCIM)
	if inner.ReadBufferSize != 64*1024 || inner.WriteBufferSize != 32*1024 {
		t.Errorf("SCIM inner client buffers = %d/%d, want 65536/32768", inner.ReadBufferSize, inner.WriteBufferSize)
	}
	if factory.GetFasthttpClient(ClientPurposeSCIM) != live {
		t.Error("ApplyOptions must keep the live client object")
	}
}

func TestDefaultHTTPClientFactoryGetter(t *testing.T) {
	t.Cleanup(func() { SetDefaultHTTPClientFactory(nil) })
	SetDefaultHTTPClientFactory(nil)
	if DefaultHTTPClientFactory() != nil {
		t.Fatal("no factory registered: want nil")
	}
	factory := NewHTTPClientFactory(nil, noopTestLogger{})
	SetDefaultHTTPClientFactory(factory)
	if DefaultHTTPClientFactory() != factory {
		t.Fatal("want the registered factory")
	}
}
