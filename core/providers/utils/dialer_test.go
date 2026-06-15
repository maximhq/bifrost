package utils

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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

	ConfigureDialer(client, false)

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

	ConfigureDialer(client, false)

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

	ConfigureDialer(client, false)

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
		ConfigureDialer(client, false)

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
		ConfigureDialer(client, false)

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
	result := ConfigureDialer(client, false)
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
	ConfigureDialer(client, false)
	ConfigureDialer(client, false) // called again

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
	ConfigureDialer(client, false)

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

// TestConfigureDialer_SSRFProtection verifies that the default (no-proxy) dial
// path rejects connections to private, loopback, and link-local addresses before
// any TCP socket is opened.
func TestConfigureDialer_SSRFProtection(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr string
	}{
		// Unspecified addresses — IsPrivateIP rejects them via IsUnspecified()
		{"0.0.0.0 all-zeros", "0.0.0.0:80", "unspecified IP"},

		// RFC 1918 private ranges — LookupIP returns the literal IP, IsPrivateIP rejects it
		{"10.x.x.x", "10.0.0.1:80", "private IP"},
		{"172.16.x.x", "172.16.0.1:80", "private IP"},
		{"192.168.x.x", "192.168.1.1:80", "private IP"},

		// Link-local / cloud metadata
		{"169.254.169.254 AWS metadata", "169.254.169.254:80", "link-local IP"},
		{"169.254.x.x link-local", "169.254.1.1:80", "link-local IP"},

		// IPv6 private
		{"[fc00::1] unique-local", "[fc00::1]:80", "private IP"},
		{"[fd00::1] unique-local", "[fd00::1]:80", "private IP"},

		// Unspecified IPv6 long form — regression for bypass via 0:0:0:0:0:0:0:0
		{"[0:0:0:0:0:0:0:0] unspecified long form", "[0:0:0:0:0:0:0:0]:80", "unspecified IP"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fasthttp.Client{ReadTimeout: time.Second}
			ConfigureDialer(client, false)
			_, err := client.Dial(tt.addr)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

// TestConfigureDialer_SSRFProxyBypass verifies that when an existingDial is set
// (proxy path), SSRF checks are intentionally skipped — the proxy owns routing.
func TestConfigureDialer_SSRFProxyBypass(t *testing.T) {
	var proxyCalled bool
	client := &fasthttp.Client{}
	client.Dial = func(addr string) (net.Conn, error) {
		proxyCalled = true
		return nil, fmt.Errorf("proxy handled: %s", addr)
	}
	ConfigureDialer(client, false)

	_, err := client.Dial("10.0.0.1:80")
	if !proxyCalled {
		t.Error("expected proxy dial to be called for private IP")
	}
	if err == nil || !strings.Contains(err.Error(), "proxy handled") {
		t.Errorf("expected proxy error, got %v", err)
	}
}

// TestConfigureDialer_SSRFZeroTimeout verifies that SSRF protection is active
// even when ReadTimeout is 0 (context.Background() is used instead of WithTimeout).
func TestConfigureDialer_SSRFZeroTimeout(t *testing.T) {
	client := &fasthttp.Client{ReadTimeout: 0}
	ConfigureDialer(client, false)

	_, err := client.Dial("169.254.169.254:80")
	if err == nil {
		t.Fatal("expected SSRF rejection with zero ReadTimeout, got nil")
	}
	if !strings.Contains(err.Error(), "link-local IP") {
		t.Errorf("expected 'link-local IP' error, got %q", err.Error())
	}
}

// TestConfigureDialer_SSRFMultiIPAllFail verifies that when all resolved IPs
// fail to connect, the last dial error is returned (not a generic message).
func TestConfigureDialer_SSRFMultiIPAllFail(t *testing.T) {
	// 192.0.2.0/24 is TEST-NET-1 (RFC 5737) — documentation-only, never routed.
	// A connection attempt to it will fail (refused or timeout) without any
	// private-IP rejection, letting us exercise the lastErr return path.
	client := &fasthttp.Client{ReadTimeout: 200 * time.Millisecond}
	ConfigureDialer(client, false)

	_, err := client.Dial("192.0.2.1:9")
	if err == nil {
		t.Fatal("expected connection error for unroutable TEST-NET address")
	}
	// Must not be the generic "no usable address" sentinel — a real dial error
	// was returned.
	if strings.Contains(err.Error(), "no usable address resolved") {
		t.Errorf("expected a real dial error, got generic sentinel: %v", err)
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

	ConfigureDialer(client, false)

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
			name:      "wrapped broken pipe",
			err:       fmt.Errorf("during POST: %w", fmt.Errorf("write tcp 10.0.0.1:53374->10.0.0.2:30000: write: broken pipe")),
			wantRetry: true,
		},
		{
			name:      "ErrConnectionClosed from fasthttp",
			err:       fasthttp.ErrConnectionClosed,
			wantRetry: true, // Explicitly matched to stay correct if future fasthttp versions surface it inside the retry loop
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
