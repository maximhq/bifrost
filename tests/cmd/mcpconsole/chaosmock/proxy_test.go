package chaosmock_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

// startProxy starts the fleet's proxy and returns a client that sends everything through it, and the
// fleet's address under a proxy-only name.
func startProxy(t *testing.T, fleet *chaosmock.Fleet, user *url.Userinfo) (*http.Client, string) {
	t.Helper()
	proxyURL, stop, err := fleet.ServeProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ServeProxy: %v", err)
	}
	t.Cleanup(func() { _ = stop(context.Background()) })
	pu, _ := url.Parse(proxyURL)
	pu.User = user
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	fleetURL, _ := url.Parse(fleet.URL())
	return client, "http://chaos" + chaosmock.ProxyDomain + ":" + fleetURL.Port()
}

func controlPost(t *testing.T, fleet *chaosmock.Fleet, body string) {
	t.Helper()
	resp, err := http.Post(fleet.URL()+"/control", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /control: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("POST /control %s: %d %s", body, resp.StatusCode, raw)
	}
}

// enableOAuth makes alpha an OAuth protected resource, so its metadata endpoints answer.
func enableOAuth(t *testing.T, fleet *chaosmock.Fleet) {
	t.Helper()
	resp, err := http.Post(fleet.URL()+"/control/alpha/auth", "application/json", strings.NewReader(`{"auth":{"oauth":{}}}`))
	if err != nil {
		t.Fatalf("POST /control/alpha/auth: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("POST /control/alpha/auth: %d", resp.StatusCode)
	}
}

func proxyLog(t *testing.T, fleet *chaosmock.Fleet) []chaosmock.ProxyLogEntry {
	t.Helper()
	resp, err := http.Get(fleet.URL() + "/control")
	if err != nil {
		t.Fatalf("GET /control: %v", err)
	}
	defer resp.Body.Close()
	var ctl struct {
		Proxy struct {
			Log []chaosmock.ProxyLogEntry `json:"log"`
		} `json:"proxy"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ctl); err != nil {
		t.Fatalf("decode /control: %v", err)
	}
	return ctl.Proxy.Log
}

func initialize(client *http.Client, base string) (*http.Response, error) {
	req, _ := http.NewRequest("POST", base+"/s/alpha/mcp", strings.NewReader(initializeBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	return client.Do(req)
}

// TestProxyForwardsProxyOnlyNamesToTheFleet: a request for a *.proxy.test name reaches the fleet
// through the proxy, keeps that name as its Host, and is logged.
func TestProxyForwardsProxyOnlyNamesToTheFleet(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	client, base := startProxy(t, fleet, nil)

	resp, err := initialize(client, base)
	if err != nil {
		t.Fatalf("initialize through the proxy: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "serverInfo") {
		t.Fatalf("expected alpha to answer initialize, got %d %s", resp.StatusCode, body)
	}

	// The fleet builds metadata URLs from Host, so they stay on the proxied name.
	enableOAuth(t, fleet)
	resp, err = client.Get(base + "/.well-known/oauth-protected-resource/s/alpha/mcp")
	if err != nil {
		t.Fatalf("metadata through the proxy: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(raw), "http://chaos"+chaosmock.ProxyDomain) {
		t.Fatalf("expected 200 with metadata on the proxied name, got %d %s", resp.StatusCode, raw)
	}

	log := proxyLog(t, fleet)
	if len(log) == 0 || log[0].Method != "POST" || log[0].Host != "chaos"+chaosmock.ProxyDomain || log[0].Path != "/s/alpha/mcp" || log[0].Status != 200 {
		t.Fatalf("expected the initialize logged first, got %+v", log)
	}
}

// TestProxyRefusesNamesOutsideItsDomain: a name the proxy cannot resolve answers 502 and is logged.
func TestProxyRefusesNamesOutsideItsDomain(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	client, _ := startProxy(t, fleet, nil)
	resp, err := client.Get("http://elsewhere.example.test/x")
	if err != nil {
		t.Fatalf("GET through the proxy: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.StatusCode)
	}
	if log := proxyLog(t, fleet); len(log) != 1 || log[0].Status != http.StatusBadGateway {
		t.Fatalf("expected one 502 logged, got %+v", log)
	}
}

// TestProxyAuthAndDown: with auth set the proxy answers 407 to a client without credentials and
// forwards one with them; down fails the connection; clear_log empties the log.
func TestProxyAuthAndDown(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	anon, base := startProxy(t, fleet, nil)
	controlPost(t, fleet, `{"proxy":{"auth":"u:p"}}`)

	resp, err := initialize(anon, base)
	if err != nil {
		t.Fatalf("initialize without credentials: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired || resp.Header.Get("Proxy-Authenticate") == "" {
		t.Fatalf("expected 407 with Proxy-Authenticate, got %d %v", resp.StatusCode, resp.Header)
	}

	pu, _ := anon.Transport.(*http.Transport).Proxy(&http.Request{URL: &url.URL{}})
	authed := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: pu.Host, User: url.UserPassword("u", "p")})}}
	resp, err = initialize(authed, base)
	if err != nil {
		t.Fatalf("initialize with credentials: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 with credentials, got %d", resp.StatusCode)
	}

	controlPost(t, fleet, `{"proxy":{"down":true,"clear_log":true}}`)
	if _, err := initialize(authed, base); err == nil {
		t.Fatalf("expected the connection to fail while the proxy is down")
	}
	if log := proxyLog(t, fleet); len(log) != 0 {
		t.Fatalf("expected an empty log after clear_log and only refused connections, got %+v", log)
	}

	controlPost(t, fleet, `{"proxy":{}}`)
	resp, err = initialize(anon, base)
	if err != nil {
		t.Fatalf("initialize after the proxy is back: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 once auth and down are cleared, got %d", resp.StatusCode)
	}
}

// TestProxyTunnelsConnect: CONNECT to a proxy-only name opens a tunnel to the fleet at that port.
func TestProxyTunnelsConnect(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	client, base := startProxy(t, fleet, nil)
	pu, _ := client.Transport.(*http.Transport).Proxy(&http.Request{URL: &url.URL{}})
	target := strings.TrimPrefix(base, "http://")

	conn, err := net.DialTimeout("tcp", pu.Host, 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, " 200 ") {
		t.Fatalf("expected 200 Connection Established, got %q", status)
	}
	_, _ = br.ReadString('\n')

	fmt.Fprintf(conn, "POST /s/alpha/mcp HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nAccept: application/json, text/event-stream\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", target, len(initializeBody), initializeBody)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read the tunnelled response: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "serverInfo") {
		t.Fatalf("expected alpha to answer through the tunnel, got %d %s", resp.StatusCode, body)
	}
	if log := proxyLog(t, fleet); len(log) != 1 || log[0].Method != "CONNECT" || log[0].Path != "" {
		t.Fatalf("expected one CONNECT logged, got %+v", log)
	}
}

// TestProxyLogsAnOpenStream: a stream through the proxy is logged once its headers are out, while it
// is still open.
func TestProxyLogsAnOpenStream(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	client, base := startProxy(t, fleet, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/s/alpha/sse", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("open the SSE stream through the proxy: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected the stream to open, got %d", resp.StatusCode)
	}
	if log := proxyLog(t, fleet); len(log) != 1 || log[0].Method != "GET" || log[0].Path != "/s/alpha/sse" || log[0].Status != 200 {
		t.Fatalf("expected the open stream logged, got %+v", log)
	}
}

// TestProxyDenyAndStall: a denied host gets 403 while others pass; a stalled host never answers, so
// the client only sees its own timeout, and the attempt is logged with status 0.
func TestProxyDenyAndStall(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	client, base := startProxy(t, fleet, nil)
	port := base[strings.LastIndex(base, ":")+1:]
	controlPost(t, fleet, `{"proxy":{"deny":["blocked.proxy.test"],"stall":["dark.proxy.test"]}}`)

	resp, err := initialize(client, "http://blocked.proxy.test:"+port)
	if err != nil {
		t.Fatalf("initialize on a denied host: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "blocked by policy") {
		t.Fatalf("expected 403 blocked by policy, got %d %s", resp.StatusCode, body)
	}
	if resp, err = initialize(client, base); err != nil || resp.StatusCode != 200 {
		t.Fatalf("expected an allowed host to pass, got %v %v", resp, err)
	}
	resp.Body.Close()

	quick := &http.Client{Timeout: 500 * time.Millisecond, Transport: client.Transport}
	start := time.Now()
	if _, err := initialize(quick, "http://dark.proxy.test:"+port); err == nil {
		t.Fatalf("expected a stalled host to time out")
	}
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Fatalf("expected the client's own timeout to end the request, it ended after %v", elapsed)
	}
	log := proxyLog(t, fleet)
	if last := log[len(log)-1]; last.Host != "dark.proxy.test" || last.Status != 0 {
		t.Fatalf("expected the stalled attempt logged with status 0, got %+v", log)
	}
}

// TestProxyInterceptsTLS: with intercept on, an https request through the proxy is answered with a
// certificate for the requested name signed by the CA GET /control reports, and reaches the fleet.
func TestProxyInterceptsTLS(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	_, base := startProxy(t, fleet, nil)
	controlPost(t, fleet, `{"proxy":{"intercept":true}}`)
	resp, err := http.Get(fleet.URL() + "/control")
	if err != nil {
		t.Fatalf("GET /control: %v", err)
	}
	var ctl struct {
		Proxy struct {
			URL   string `json:"url"`
			CAPEM string `json:"ca_pem"`
		} `json:"proxy"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&ctl)
	resp.Body.Close()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(ctl.Proxy.CAPEM)) {
		t.Fatalf("expected a CA in proxy.ca_pem, got %q", ctl.Proxy.CAPEM)
	}
	pu, _ := url.Parse(ctl.Proxy.URL)
	httpsBase := "https://" + strings.TrimPrefix(base, "http://")

	trusting := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: pool}}}
	resp, err = initialize(trusting, httpsBase)
	if err != nil {
		t.Fatalf("initialize over the intercepted tunnel with the CA trusted: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "serverInfo") {
		t.Fatalf("expected alpha to answer through the interception, got %d %s", resp.StatusCode, body)
	}
	log := proxyLog(t, fleet)
	if len(log) != 2 || log[0].Method != "CONNECT" || log[1].Method != "POST" || log[1].Path != "/s/alpha/mcp" {
		t.Fatalf("expected the CONNECT and the decrypted POST logged, got %+v", log)
	}

	plain := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	if _, err := initialize(plain, httpsBase); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected a client without the CA to refuse the certificate, got %v", err)
	}
}

// TestProxyCutsIdleConnections: a stream that stays quiet longer than idle_timeout_ms is cut.
func TestProxyCutsIdleConnections(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	client, base := startProxy(t, fleet, nil)
	controlPost(t, fleet, `{"proxy":{"idle_timeout_ms":300}}`)
	req, _ := http.NewRequest("GET", base+"/s/alpha/sse", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := (&http.Client{Transport: client.Transport}).Do(req)
	if err != nil {
		t.Fatalf("open the stream: %v", err)
	}
	defer resp.Body.Close()
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, resp.Body); done <- err }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("expected the idle stream to be cut")
	}
}

// TestProxyInterceptKeepsHTTPS: OAuth metadata fetched through an intercepted tunnel advertises https
// URLs, as the client connected over https.
func TestProxyInterceptKeepsHTTPS(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	_, base := startProxy(t, fleet, nil)
	enableOAuth(t, fleet)
	trusting := interceptingClient(t, fleet)
	resp, err := trusting.Get("https://" + strings.TrimPrefix(base, "http://") + "/.well-known/oauth-protected-resource/s/alpha/mcp")
	if err != nil {
		t.Fatalf("metadata over the intercepted tunnel: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(raw), "https://chaos"+chaosmock.ProxyDomain) || strings.Contains(string(raw), "http://chaos") {
		t.Fatalf("expected https URLs on the proxied name, got %d %s", resp.StatusCode, raw)
	}
}

// TestProxyModeChangeCutsInterceptedConnections: turning interception off cuts a connection opened
// under it, so the client's next request opens a new tunnel instead of reusing the intercepted one.
func TestProxyModeChangeCutsInterceptedConnections(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	_, base := startProxy(t, fleet, nil)
	trusting := interceptingClient(t, fleet)
	httpsBase := "https://" + strings.TrimPrefix(base, "http://")
	resp, err := initialize(trusting, httpsBase)
	if err != nil {
		t.Fatalf("initialize over the intercepted tunnel: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	controlPost(t, fleet, `{"proxy":{"clear_log":true}}`)
	if _, err := initialize(trusting, httpsBase); err == nil {
		t.Fatalf("expected the old intercepted connection to be gone, and a plain tunnel to the http fleet to fail TLS")
	}
	if log := proxyLog(t, fleet); len(log) == 0 || log[0].Method != "CONNECT" {
		t.Fatalf("expected the next request to open a new tunnel, got %+v", log)
	}
}

// interceptingClient turns interception on and returns a client that trusts the proxy's CA.
func interceptingClient(t *testing.T, fleet *chaosmock.Fleet) *http.Client {
	t.Helper()
	controlPost(t, fleet, `{"proxy":{"intercept":true}}`)
	resp, err := http.Get(fleet.URL() + "/control")
	if err != nil {
		t.Fatalf("GET /control: %v", err)
	}
	var ctl struct {
		Proxy struct {
			URL   string `json:"url"`
			CAPEM string `json:"ca_pem"`
		} `json:"proxy"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&ctl)
	resp.Body.Close()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(ctl.Proxy.CAPEM)) {
		t.Fatalf("expected a CA in proxy.ca_pem")
	}
	pu, _ := url.Parse(ctl.Proxy.URL)
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: pool}}}
}
