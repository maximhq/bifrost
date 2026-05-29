package otel

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// OtelClientHTTP is the implementation of the OpenTelemetry client for HTTP
type OtelClientHTTP struct {
	client   *http.Client
	endpoint string
	headers  map[string]string
}

// NewOtelClientHTTP creates a new OpenTelemetry client for HTTP
func NewOtelClientHTTP(endpoint string, headers map[string]string, tlsCACert string, insecureMode bool) (*OtelClientHTTP, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 10
	transport.IdleConnTimeout = 120 * time.Second

	tlsConfig, err := buildTLSConfig(tlsCACert, insecureMode)
	if err != nil {
		return nil, err
	}
	transport.TLSClientConfig = tlsConfig

	return &OtelClientHTTP{client: &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}, endpoint: endpoint, headers: headers}, nil
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
