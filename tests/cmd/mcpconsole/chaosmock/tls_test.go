package chaosmock_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestServeTLSServesTheFleetUnderS: /s/ paths on the TLS listener reach the
// fleet's servers, and a client that trusts the certificate GET /control
// reports reaches them with verification on.
func TestServeTLSServesTheFleetUnderS(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	base, stop, err := fleet.ServeTLS("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ServeTLS: %v", err)
	}
	t.Cleanup(func() { _ = stop(context.Background()) })

	resp, err := http.Get(fleet.URL() + "/control")
	if err != nil {
		t.Fatalf("GET /control: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	ctl := decode(t, string(raw))
	if ctl["tls_url"] != base {
		t.Fatalf("expected tls_url %s, got %v", base, ctl["tls_url"])
	}
	pool := x509.NewCertPool()
	if certPEM, _ := ctl["tls_cert_pem"].(string); !pool.AppendCertsFromPEM([]byte(certPEM)) {
		t.Fatalf("expected a PEM certificate in tls_cert_pem, got %v", ctl["tls_cert_pem"])
	}

	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	req, _ := http.NewRequest("POST", base+"/s/alpha/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("initialize over TLS with the reported certificate trusted: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "serverInfo") {
		t.Errorf("expected alpha to answer initialize, got %d %s", resp.StatusCode, body)
	}

	if _, err := (&http.Client{Timeout: 10 * time.Second}).Get(base + "/s/alpha/mcp"); err == nil {
		t.Errorf("expected a client that trusts nothing extra to refuse the certificate")
	}
}

// TestServeTLSStopEndsAnOpenStream: stopping the TLS listener ends a stream a client still holds
// open, as stopping the plain listener does, instead of waiting for the client to let go.
func TestServeTLSStopEndsAnOpenStream(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	base, stop, err := fleet.ServeTLS("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ServeTLS: %v", err)
	}
	resp, err := http.Get(fleet.URL() + "/control")
	if err != nil {
		t.Fatalf("GET /control: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	pool := x509.NewCertPool()
	certPEM, _ := decode(t, string(raw))["tls_cert_pem"].(string)
	pool.AppendCertsFromPEM([]byte(certPEM))

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	req, _ := http.NewRequest(http.MethodGet, base+"/s/alpha/mcp", nil)
	req.Header.Set("Accept", "text/event-stream")
	stream, err := client.Do(req)
	if err != nil || stream.StatusCode != http.StatusOK {
		t.Fatalf("open the GET stream over TLS: %v %v", stream, err)
	}
	defer stream.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := stop(ctx); err != nil {
		t.Fatalf("stop with a stream open: %v after %s", err, time.Since(start))
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("stop took %s with a stream open", took)
	}
}
