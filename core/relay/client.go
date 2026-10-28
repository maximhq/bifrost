package relay

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"time"

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

	proxy, proxyTLS, err := providerUtils.NetHTTPProxy(proxyConfig)
	if err != nil {
		return nil, err
	}
	if proxy == nil {
		proxy = providerUtils.EnvProxyFunc()
	}
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
		// Layer on top of the proxy's CA, if any, so both are trusted.
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
		if proxyTLS != nil {
			tlsConfig = proxyTLS.Clone()
		}
		tlsConfig.InsecureSkipVerify = tlsConfig.InsecureSkipVerify || insecure
		if caCertPEM != "" {
			var pool *x509.CertPool
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
		transport.TLSClientConfig = providerUtils.RescopeProxySkipVerify(proxyTLS, tlsConfig, insecure)
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}
