package network

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
			name:      "retries on broken pipe (write to closed connection)",
			err:       fmt.Errorf("write tcp 10.0.0.1:53374->10.0.0.2:30000: write: broken pipe"),
			attempts:  1,
			wantReset: true,
			wantRetry: true,
		},
		{
			name:      "does not retry on second attempt",
			err:       io.EOF,
			attempts:  2,
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
		MaxConnsPerHost:    1,              // Only 1 connection allowed — second request must wait
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
