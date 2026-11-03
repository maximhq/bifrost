package relay

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"time"

	ws "github.com/fasthttp/websocket"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// newHTTPClient builds the unary net/http client for one endpoint: proxy_config (or the proxy
// from the environment), the network bounds, and TLS settings layered on the proxy's. The relay
// never follows redirects: the caller gets the 3xx and decides, so the injected credential never
// reaches a host the upstream named and the base URL host pinning cannot be stepped around.
// Built once per endpoint (re)build, never on the request path.
func newHTTPClient(network *schemas.RelayNetworkConfig, proxyConfig *schemas.ProxyConfig) (*http.Client, error) {
	timeout := time.Duration(schemas.DefaultEndpointTimeoutSeconds) * time.Second
	maxConns := schemas.DefaultEndpointMaxConnsPerHost
	var insecure bool
	var caCertPEM string
	if network != nil {
		if network.TimeoutSeconds > 0 {
			timeout = time.Duration(network.TimeoutSeconds) * time.Second
		}
		if network.MaxConnsPerHost > 0 {
			maxConns = network.MaxConnsPerHost
		}
		insecure, caCertPEM = network.InsecureSkipVerify, network.CACertPEM
	}

	// No proxy means a direct connection, as for a provider: HTTP(S)_PROXY is read only for a
	// proxy_config of type "environment" (NetHTTPProxyWithHopTLS), never as a fallback.
	proxy, proxyTLS, proxyHopTLS, err := providerUtils.NetHTTPProxyWithHopTLS(proxyConfig)
	if err != nil {
		return nil, err
	}
	proxyFromConfig := proxy != nil
	transport := &http.Transport{
		Proxy:                 proxy,
		TLSClientConfig:       proxyTLS,
		MaxConnsPerHost:       maxConns,
		MaxIdleConns:          schemas.DefaultMaxIdleConnsPerHost,
		MaxIdleConnsPerHost:   schemas.DefaultMaxIdleConnsPerHost,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	if insecure || caCertPEM != "" {
		layered, err := layerRelayTLS(proxyTLS, insecure, caCertPEM)
		if err != nil {
			return nil, err
		}
		transport.TLSClientConfig = layered
	}
	// An https:// proxy from proxy_config is dialed with its own TLS config, so its
	// skip_tls_verify never relaxes verification of the target.
	if proxyFromConfig {
		providerUtils.DialHTTPSProxyWithHopTLS(transport, proxyHopTLS)
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// layerRelayTLS layers the relay's network TLS settings (insecure_skip_verify, ca_cert_pem) on top
// of the proxy's TLS config, if any, so both are trusted. The HTTP client and the WebSocket dialer
// share it, so a relay trusts the same upstream whichever way it is called.
func layerRelayTLS(proxyTLS *tls.Config, insecure bool, caCertPEM string) (*tls.Config, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if proxyTLS != nil {
		tlsConfig = proxyTLS.Clone()
	}
	// The target TLS config never carries the proxy's skip_tls_verify (that lives on the hop's own
	// config), so only the relay's own setting decides whether the target is verified.
	tlsConfig.InsecureSkipVerify = insecure
	if caCertPEM != "" {
		var pool *x509.CertPool
		var err error
		if tlsConfig.RootCAs != nil {
			pool = tlsConfig.RootCAs.Clone()
		} else if pool, err = x509.SystemCertPool(); err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(caCertPEM)) {
			return nil, fmt.Errorf("network.ca_cert_pem: failed to parse CA certificate PEM")
		}
		tlsConfig.RootCAs = pool
	}
	return tlsConfig, nil
}

// newWebSocketDialer builds the upstream WebSocket dialer for one endpoint with the same proxy,
// TLS and handshake bound as its HTTP client: the endpoint's effective proxy (HTTP(S)_PROXY only for
// type "environment", as for providers),
// timeout_seconds for the handshake, and the relay's TLS settings. Built once per endpoint
// (re)build, never on the request path; a session copies it to set its subprotocols.
func newWebSocketDialer(network *schemas.RelayNetworkConfig, proxyConfig *schemas.ProxyConfig) (*ws.Dialer, error) {
	timeout := time.Duration(schemas.DefaultEndpointTimeoutSeconds) * time.Second
	var insecure bool
	var caCertPEM string
	if network != nil {
		if network.TimeoutSeconds > 0 {
			timeout = time.Duration(network.TimeoutSeconds) * time.Second
		}
		insecure, caCertPEM = network.InsecureSkipVerify, network.CACertPEM
	}
	dialer, err := providerUtils.ConfigureWebSocketProxy(&ws.Dialer{
		HandshakeTimeout: timeout,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
	}, proxyConfig)
	if err != nil {
		return nil, err
	}
	if insecure || caCertPEM != "" {
		layered, err := layerRelayTLS(dialer.TLSClientConfig, insecure, caCertPEM)
		if err != nil {
			return nil, err
		}
		dialer.TLSClientConfig = layered
	}
	return dialer, nil
}
