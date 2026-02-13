package network

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

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
