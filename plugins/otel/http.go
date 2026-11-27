package otel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/network"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// errRedirectRefused is wrapped with the status code and nothing else: the endpoint
// controls the Location header and the redirect body, and net/http puts the Location URL
// in the error it builds when CheckRedirect refuses, so neither may reach logs or error text.
var errRedirectRefused = errors.New("otel export: redirect refused")

// refuseRedirects fails any 3xx response before net/http can follow it, so the batch is
// never resent to the redirect target.
type refuseRedirects struct {
	next http.RoundTripper
}

func (r refuseRedirects) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 == 3 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w (%d); configure the final collector URL", errRedirectRefused, resp.StatusCode)
	}
	return resp, nil
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the wrapped transport.
func (r refuseRedirects) CloseIdleConnections() {
	if c, ok := r.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// newExportClient returns an http.Client over transport that refuses redirects.
func newExportClient(transport http.RoundTripper, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: refuseRedirects{next: transport}}
}

// OtelClientHTTP is the implementation of the OpenTelemetry client for HTTP
type OtelClientHTTP struct {
	client   *http.Client
	endpoint string
	headers  map[string]string
}

// NewOtelClientHTTP creates a new OpenTelemetry client for HTTP.
// timeout bounds a single export; it comes from the profile's export_timeout and is
// also applied as a per-export context deadline by the caller.
func NewOtelClientHTTP(endpoint string, headers map[string]string, tlsCACert string, insecureMode bool, timeout time.Duration) (*OtelClientHTTP, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The global proxy when it is enabled for API traffic, else the environment.
	transport.Proxy = network.DefaultProxyFunc(network.ClientPurposeAPI)
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 10
	transport.IdleConnTimeout = 120 * time.Second

	tlsConfig, err := buildTLSConfig(tlsCACert, insecureMode)
	if err != nil {
		return nil, err
	}
	transport.TLSClientConfig = tlsConfig

	return &OtelClientHTTP{client: newExportClient(transport, timeout), endpoint: endpoint, headers: headers}, nil
}

// Emit sends a trace to the OpenTelemetry collector
func (c *OtelClientHTTP) Emit(ctx context.Context, rs []*ResourceSpan) error {
	payload, err := proto.Marshal(&collectorpb.ExportTraceServiceRequest{ResourceSpans: rs})
	if err != nil {
		logger.Error("[otel] failed to marshal trace: %v", err)
		return err
	}
	var body bytes.Buffer
	body.Write(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, &body)
	if err != nil {
		logger.Error("[otel] failed to create request: %v", err)
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	if c.headers != nil {
		for key, value := range c.headers {
			if strings.ToLower(key) == "content-type" {
				continue
			}
			req.Header.Set(key, value)
		}
	}
	resp, err := c.client.Do(req)
	if err != nil {
		logger.Error("[otel] failed to send request to %s: %v", c.endpoint, err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		// Discard the body to avoid leaking memory
		_, _ = io.Copy(io.Discard, resp.Body)
		logger.Error("[otel] collector at %s returned status %s", c.endpoint, resp.Status)
		return fmt.Errorf("collector returned %s", resp.Status)
	}
	logger.Debug("[otel] successfully sent trace to %s, status: %s", c.endpoint, resp.Status)
	return nil
}

// Close closes the HTTP client
func (c *OtelClientHTTP) Close() error {
	if c.client != nil {
		c.client.CloseIdleConnections()
	}
	return nil
}
