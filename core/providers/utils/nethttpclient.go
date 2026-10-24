package utils

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// NewNetHTTPClient builds the unary net/http client for providers that cannot use fasthttp
// (SigV4-signed relays). It applies proxy_config, the NetworkConfig timeouts and connection
// limits, TLS settings (CA PEM, insecure_skip_verify) and the HTTP/2 policy. Callers derive the
// streaming variant with BuildStreamingHTTPClient. Constructor-time only, never on the request path.
func NewNetHTTPClient(config *schemas.ProviderConfig) (*http.Client, error) {
	requestTimeout := time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)

	// With no proxy configured, keep proxying from the environment.
	proxy, proxyTLS, err := NetHTTPProxy(config.ProxyConfig)
	if err != nil {
		return nil, err
	}
	if proxy == nil {
		proxy = EnvProxyFunc()
	}

	transport := &http.Transport{
		Proxy:                 proxy,
		TLSClientConfig:       proxyTLS,
		MaxConnsPerHost:       config.NetworkConfig.MaxConnsPerHost,
		MaxIdleConns:          schemas.DefaultMaxIdleConnsPerHost,
		MaxIdleConnsPerHost:   schemas.DefaultMaxIdleConnsPerHost,
		IdleConnTimeout:       time.Second * time.Duration(config.NetworkConfig.KeepAliveTimeoutInSeconds),
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: requestTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     config.NetworkConfig.EnforceHTTP2,
	}

	// Disable HTTP/2 auto-negotiation when not explicitly enforced.
	// ForceAttemptHTTP2=false alone does NOT prevent HTTP/2 — Go's http2 package
	// auto-registers h2 via TLSNextProto in init(). Setting TLSNextProto to an
	// empty map prevents ALPN negotiation from upgrading connections to h2.
	if !config.NetworkConfig.EnforceHTTP2 {
		transport.TLSNextProto = make(map[string]func(authority string, c *tls.Conn) http.RoundTripper)
	}

	// Apply TLS settings from NetworkConfig
	caCertPEM := ""
	if config.NetworkConfig.CACertPEM != nil {
		caCertPEM = config.NetworkConfig.CACertPEM.GetValue()
	}
	if config.NetworkConfig.InsecureSkipVerify || caCertPEM != "" {
		// Layer on top of the proxy's CA, if any, so both are trusted.
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
		if proxyTLS != nil {
			tlsConfig = proxyTLS.Clone()
		}
		if config.NetworkConfig.InsecureSkipVerify {
			tlsConfig.InsecureSkipVerify = true
		}
		if caCertPEM != "" {
			var certPool *x509.CertPool
			if tlsConfig.RootCAs != nil {
				certPool = tlsConfig.RootCAs.Clone()
			} else if certPool, err = x509.SystemCertPool(); err != nil {
				certPool = x509.NewCertPool()
			}
			if !certPool.AppendCertsFromPEM([]byte(caCertPEM)) {
				return nil, fmt.Errorf("failed to parse CA certificate PEM")
			}
			tlsConfig.RootCAs = certPool
		}
		// Keep an inherited proxy's skip_tls_verify scoped to proxied traffic, now
		// checked against the merged roots.
		transport.TLSClientConfig = RescopeProxySkipVerify(proxyTLS, tlsConfig, config.NetworkConfig.InsecureSkipVerify)
	}

	// When HTTP/2 is enforced and a ping interval is configured, send client-initiated
	// PING keepalives so an idle streaming connection isn't closed by an intermediary
	// (surfaces as "unexpected EOF"). Left off by default; opt in via the interval.
	if config.NetworkConfig.EnforceHTTP2 && config.NetworkConfig.HTTP2PingIntervalInSeconds > 0 {
		transport.HTTP2 = &http.HTTP2Config{
			SendPingTimeout: time.Duration(config.NetworkConfig.HTTP2PingIntervalInSeconds) * time.Second,
		}
	}

	return &http.Client{Transport: transport, Timeout: requestTimeout}, nil
}
