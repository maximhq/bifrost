package otel

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/network"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// Bound both compressed and decoded OTLP/HTTP 2xx acknowledgement bodies.
const maxTraceResponseBytes = 64 << 10

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
	transport.DisableCompression = true // Bound gzip input before decoding it.
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 10
	transport.IdleConnTimeout = 120 * time.Second

	tlsConfig, err := buildTLSConfig(tlsCACert, insecureMode)
	if err != nil {
		return nil, err
	}
	transport.TLSClientConfig = tlsConfig

	return &OtelClientHTTP{client: &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}, endpoint: endpoint, headers: headers}, nil
}

// Emit sends a trace to the OpenTelemetry collector
func (c *OtelClientHTTP) Emit(ctx context.Context, rs []*ResourceSpan) error {
	_, err := c.emit(ctx, rs)
	return err
}

func (c *OtelClientHTTP) emit(ctx context.Context, rs []*ResourceSpan) (traceExportResult, error) {
	payload, err := proto.Marshal(&collectorpb.ExportTraceServiceRequest{ResourceSpans: rs})
	if err != nil {
		logger.Error("[otel] failed to marshal trace: %v", err)
		return traceExportResult{}, err
	}
	var body bytes.Buffer
	body.Write(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, &body)
	if err != nil {
		logger.Error("[otel] failed to create request: %v", err)
		return traceExportResult{}, err
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
	if req.Header.Get("Accept-Encoding") == "" {
		req.Header.Set("Accept-Encoding", "gzip")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		logger.Error("[otel] failed to send request to %s: %v", c.endpoint, err)
		return traceExportResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		// Discard the body to avoid leaking memory
		_, _ = io.Copy(io.Discard, resp.Body)
		logger.Error("[otel] collector at %s returned status %s", c.endpoint, resp.Status)
		return traceExportResult{}, fmt.Errorf("collector returned %s", resp.Status)
	}
	logger.Debug("[otel] received a trace acknowledgement from %s, status: %s", c.endpoint, resp.Status)
	if resp.ContentLength == 0 {
		return traceExportResult{}, nil
	}
	if resp.ContentLength > maxTraceResponseBytes {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxTraceResponseBytes))
		return traceExportResult{ackWarning: fmt.Sprintf("OTLP trace acknowledgement exceeds %d bytes", maxTraceResponseBytes)}, nil
	}
	contentEncoding := strings.TrimSpace(resp.Header.Get("Content-Encoding"))
	if contentEncoding != "" && !strings.EqualFold(contentEncoding, "identity") && !strings.EqualFold(contentEncoding, "gzip") {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxTraceResponseBytes))
		return traceExportResult{ackWarning: fmt.Sprintf("unsupported OTLP trace acknowledgement encoding %q", contentEncoding)}, nil
	}
	compressed := &io.LimitedReader{R: resp.Body, N: maxTraceResponseBytes + 1}
	var responseBody io.Reader = compressed
	if strings.EqualFold(contentEncoding, "gzip") {
		decompressed, err := gzip.NewReader(compressed)
		if err != nil {
			return traceExportResult{ackWarning: fmt.Sprintf("read gzip OTLP trace acknowledgement: %v", err)}, nil
		}
		defer decompressed.Close()
		responseBody = decompressed
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(responseBody, maxTraceResponseBytes+1))
	if compressed.N == 0 {
		return traceExportResult{ackWarning: fmt.Sprintf("compressed OTLP trace acknowledgement exceeds %d bytes", maxTraceResponseBytes)}, nil
	}
	if err != nil {
		return traceExportResult{ackWarning: fmt.Sprintf("read OTLP trace acknowledgement: %v", err)}, nil
	}
	if len(bodyBytes) == 0 {
		return traceExportResult{}, nil
	}
	if len(bodyBytes) > maxTraceResponseBytes {
		return traceExportResult{ackWarning: fmt.Sprintf("OTLP trace acknowledgement exceeds %d bytes", maxTraceResponseBytes)}, nil
	}
	var reply collectorpb.ExportTraceServiceResponse
	if err := proto.Unmarshal(bodyBytes, &reply); err != nil {
		return traceExportResult{ackWarning: "non-protobuf OTLP trace acknowledgement"}, nil
	}
	return traceExportResult{partialSuccess: reply.GetPartialSuccess(), headers: c.headers}, nil
}

// Close closes the HTTP client
func (c *OtelClientHTTP) Close() error {
	if c.client != nil {
		c.client.CloseIdleConnections()
	}
	return nil
}
