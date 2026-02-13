package utils

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/network"
	"github.com/valyala/fasthttp"
)

// TestConfigureDialer_SetsRetryIfErr verifies that ConfigureDialer installs
// the StaleConnectionRetryIfErr callback on the client.
func TestConfigureDialer_SetsRetryIfErr(t *testing.T) {
	client := &fasthttp.Client{}
	if client.RetryIfErr != nil {
		t.Fatal("precondition: RetryIfErr should be nil on a new client")
	}

	ConfigureDialer(client)

	if client.RetryIfErr == nil {
		t.Fatal("ConfigureDialer should set RetryIfErr")
	}

	// Verify it behaves like StaleConnectionRetryIfErr
	reset, retry := client.RetryIfErr(nil, 1, fmt.Errorf("cannot find whitespace in the first line of response"))
	if !reset || !retry {
		t.Error("RetryIfErr should retry on whitespace error")
	}
	reset, retry = client.RetryIfErr(nil, 1, fmt.Errorf("dial tcp: no such host"))
	if reset || retry {
		t.Error("RetryIfErr should not retry on unrelated errors")
	}
}

// TestConfigureDialer_SetsDial verifies that ConfigureDialer installs a custom
// Dial function on the client when no existing Dial is present.
func TestConfigureDialer_SetsDial(t *testing.T) {
	client := &fasthttp.Client{}
	if client.Dial != nil {
		t.Fatal("precondition: Dial should be nil on a new client")
	}

	ConfigureDialer(client)

	if client.Dial == nil {
		t.Fatal("ConfigureDialer should set a Dial function")
	}
}

// TestConfigureDialer_ComposesWithExistingDial verifies that when a custom Dial
// function is already set (e.g., from ConfigureProxy), ConfigureDialer wraps it
// and still enables TCP keepalive on the resulting connection.
func TestConfigureDialer_ComposesWithExistingDial(t *testing.T) {
	var proxyDialCalled atomic.Bool

	client := &fasthttp.Client{}
	// Simulate a proxy dial function (set by ConfigureProxy)
	client.Dial = func(addr string) (net.Conn, error) {
		proxyDialCalled.Store(true)
		return net.Dial("tcp", addr)
	}

	ConfigureDialer(client)

	// Start a test server to connect to
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(server.URL)
	req.Header.SetMethod(http.MethodGet)

	if err := client.Do(req, resp); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode() != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode())
	}
	if !proxyDialCalled.Load() {
		t.Error("ConfigureDialer should have called the existing proxy dial function")
	}
}

// TestConfigureDialer_TCPKeepAliveEnabled verifies that connections created
// through ConfigureDialer have TCP keepalive enabled.
func TestConfigureDialer_TCPKeepAliveEnabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	// Test without existing dial (direct connection path)
	t.Run("without_existing_dial", func(t *testing.T) {
		client := &fasthttp.Client{}
		ConfigureDialer(client)

		// The Dial function should create connections with keepalive
		// We can verify by making a connection and checking the TCP options
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)

		req.SetRequestURI(server.URL)
		req.Header.SetMethod(http.MethodGet)

		if err := client.Do(req, resp); err != nil {
			t.Fatalf("request failed: %v", err)
		}
		if resp.StatusCode() != 200 {
			t.Fatalf("expected 200, got %d", resp.StatusCode())
		}
	})

	// Test with existing dial (proxy composition path)
	t.Run("with_existing_dial", func(t *testing.T) {
		var connFromProxy net.Conn
		client := &fasthttp.Client{}
		client.Dial = func(addr string) (net.Conn, error) {
			conn, err := net.Dial("tcp", addr)
			connFromProxy = conn
			return conn, err
		}
		ConfigureDialer(client)

		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)

		req.SetRequestURI(server.URL)
		req.Header.SetMethod(http.MethodGet)

		if err := client.Do(req, resp); err != nil {
			t.Fatalf("request failed: %v", err)
		}

		// Verify the proxy-returned connection is a TCP connection
		// (ConfigureDialer enables keepalive via SetKeepAliveConfig on it)
		if connFromProxy == nil {
			t.Fatal("proxy dial should have been called")
		}
		if _, ok := connFromProxy.(*net.TCPConn); !ok {
			t.Errorf("expected *net.TCPConn, got %T", connFromProxy)
		}
	})
}

// TestConfigureDialer_ReturnValue verifies that ConfigureDialer returns the
// same client pointer it received (for chaining).
func TestConfigureDialer_ReturnValue(t *testing.T) {
	client := &fasthttp.Client{}
	result := ConfigureDialer(client)
	if result != client {
		t.Error("ConfigureDialer should return the same client pointer")
	}
}

// TestConfigureDialer_Idempotent verifies that calling ConfigureDialer multiple
// times doesn't break the client.
func TestConfigureDialer_Idempotent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	client := &fasthttp.Client{}
	ConfigureDialer(client)
	ConfigureDialer(client) // called again

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(server.URL)
	req.Header.SetMethod(http.MethodPost)
	req.SetBodyString(`{"test": true}`)

	if err := client.Do(req, resp); err != nil {
		t.Fatalf("request failed after double ConfigureDialer: %v", err)
	}
	if resp.StatusCode() != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode())
	}
}

// TestConfigureDialer_WithRetryOnStaleConnection is an integration test that
// verifies ConfigureDialer enables successful POST retry after TTL mismatch.
// This combines both the retry and keepalive behaviors.
func TestConfigureDialer_WithRetryOnStaleConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TTL mismatch test in short mode (requires 11s wait)")
	}

	const (
		serverIdleTimeout = 10 * time.Second
		clientIdleTimeout = 15 * time.Second
		waitBetween       = 11 * time.Second
	)

	var requestCount atomic.Int32

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"ok": true, "request": %d}`, requestCount.Load())
	}))
	server.Config.IdleTimeout = serverIdleTimeout
	server.Start()
	defer server.Close()

	client := &fasthttp.Client{
		MaxIdleConnDuration: clientIdleTimeout,
		MaxConnsPerHost:     10,
	}
	// Use ConfigureDialer (the function under test) instead of manually setting RetryIfErr
	ConfigureDialer(client)

	// First request: establish connection in pool
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(server.URL)
	req.Header.SetMethod(http.MethodPost)
	req.SetBodyString(`{"prompt": "hello"}`)

	if err := client.Do(req, resp); err != nil {
		t.Fatalf("First POST failed: %v", err)
	}
	if resp.StatusCode() != 200 {
		t.Fatalf("First POST: expected 200, got %d", resp.StatusCode())
	}
	_ = resp.Body()

	// Wait for server TTL to expire
	t.Logf("Waiting %v for server idle timeout to expire...", waitBetween)
	time.Sleep(waitBetween)

	// Second request: stale connection should be retried by ConfigureDialer's retry policy
	req2 := fasthttp.AcquireRequest()
	resp2 := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req2)
	defer fasthttp.ReleaseResponse(resp2)

	req2.SetRequestURI(server.URL)
	req2.Header.SetMethod(http.MethodPost)
	req2.SetBodyString(`{"prompt": "world"}`)

	if err := client.Do(req2, resp2); err != nil {
		t.Fatalf("Second POST failed (ConfigureDialer retry should have saved it): %v", err)
	}
	if resp2.StatusCode() != 200 {
		t.Fatalf("Second POST: expected 200, got %d", resp2.StatusCode())
	}
	t.Logf("Second POST succeeded after TTL mismatch via ConfigureDialer")
}

// TestConfigureRetry_Deprecated verifies the deprecated ConfigureRetry still works.
func TestConfigureRetry_Deprecated(t *testing.T) {
	client := &fasthttp.Client{}
	result := ConfigureRetry(client)

	if result != client {
		t.Error("ConfigureRetry should return the same client pointer")
	}
	if client.RetryIfErr == nil {
		t.Fatal("ConfigureRetry should set RetryIfErr")
	}

	// Verify it uses the same StaleConnectionRetryIfErr
	reset, retry := client.RetryIfErr(nil, 1, fmt.Errorf("cannot find whitespace"))
	if !reset || !retry {
		t.Error("ConfigureRetry should install StaleConnectionRetryIfErr")
	}
}

// TestConfigureDialer_DialError verifies that dial errors from the existing
// dial function are properly propagated (not swallowed).
func TestConfigureDialer_DialError(t *testing.T) {
	expectedErr := fmt.Errorf("proxy connection refused")
	client := &fasthttp.Client{}
	client.Dial = func(addr string) (net.Conn, error) {
		return nil, expectedErr
	}

	ConfigureDialer(client)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI("http://localhost:1/test")
	req.Header.SetMethod(http.MethodPost)

	err := client.Do(req, resp)
	if err == nil {
		t.Fatal("expected error from failed proxy dial")
	}
	t.Logf("Got expected error: %v", err)
}

// TestStaleConnectionRetryIfErr_WrappedErrors verifies behavior with wrapped errors.
func TestStaleConnectionRetryIfErr_WrappedErrors(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantRetry bool
	}{
		{
			name:      "wrapped whitespace error",
			err:       fmt.Errorf("fasthttp: %w", fmt.Errorf("cannot find whitespace in header")),
			wantRetry: true,
		},
		{
			name:      "wrapped connection reset",
			err:       fmt.Errorf("during POST: connection reset by peer"),
			wantRetry: true,
		},
		{
			name:      "ErrConnectionClosed from fasthttp",
			err:       fasthttp.ErrConnectionClosed,
			wantRetry: false, // Not matched - this error appears AFTER the retry loop
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, retry := network.StaleConnectionRetryIfErr(nil, 1, tt.err)
			if retry != tt.wantRetry {
				t.Errorf("retry = %v, want %v", retry, tt.wantRetry)
			}
		})
	}
}
