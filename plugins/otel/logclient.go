package otel

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// OtelLogClient ships OTLP log records (GenAI events) to a collector. It mirrors
// OtelClient rather than reusing it because logs go to their own endpoint and use the
// logs service, but the transport behaviour (timeout, TLS, headers) is identical.
type OtelLogClient interface {
	EmitLogs(ctx context.Context, logs []*ResourceLog) error
	Close() error
}

// OtelLogClientHTTP exports log records over OTLP/HTTP (protobuf).
type OtelLogClientHTTP struct {
	client   *http.Client
	endpoint string
	headers  map[string]string
}

// NewOtelLogClientHTTP creates a new OTLP logs client for HTTP.
// timeout bounds a single export; it comes from the profile's export_timeout and is
// also applied as a per-export context deadline by the caller.
func NewOtelLogClientHTTP(endpoint string, headers map[string]string, tlsCACert string, insecureMode bool, timeout time.Duration) (*OtelLogClientHTTP, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 10
	transport.IdleConnTimeout = 120 * time.Second

	tlsConfig, err := buildTLSConfig(tlsCACert, insecureMode)
	if err != nil {
		return nil, err
	}
	transport.TLSClientConfig = tlsConfig

	return &OtelLogClientHTTP{client: &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}, endpoint: endpoint, headers: headers}, nil
}

// EmitLogs sends log records to the OpenTelemetry collector
func (c *OtelLogClientHTTP) EmitLogs(ctx context.Context, logs []*ResourceLog) error {
	payload, err := proto.Marshal(&collectorlogspb.ExportLogsServiceRequest{ResourceLogs: logs})
	if err != nil {
		logger.Error("[otel] failed to marshal logs: %v", err)
		return err
	}
	var body bytes.Buffer
	body.Write(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, &body)
	if err != nil {
		logger.Error("[otel] failed to create logs request: %v", err)
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
		logger.Error("[otel] failed to send logs request to %s: %v", c.endpoint, err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		// Discard the body to avoid leaking memory
		_, _ = io.Copy(io.Discard, resp.Body)
		logger.Error("[otel] collector at %s returned status %s for logs", c.endpoint, resp.Status)
		return fmt.Errorf("collector returned %s", resp.Status)
	}
	logger.Debug("[otel] successfully sent logs to %s, status: %s", c.endpoint, resp.Status)
	return nil
}

// Close closes the HTTP client
func (c *OtelLogClientHTTP) Close() error {
	if c.client != nil {
		c.client.CloseIdleConnections()
	}
	return nil
}

// OtelLogClientGRPC exports log records over OTLP/gRPC.
type OtelLogClientGRPC struct {
	client  collectorlogspb.LogsServiceClient
	conn    *grpc.ClientConn
	headers map[string]string
}

// NewOtelLogClientGRPC creates a new OTLP logs client for gRPC
func NewOtelLogClientGRPC(endpoint string, headers map[string]string, tlsCACert string, insecureMode bool) (*OtelLogClientGRPC, error) {
	var creds credentials.TransportCredentials

	// gRPC insecure mode uses plaintext (no TLS at all), not just skip-verify.
	// buildTLSConfig is bypassed here to preserve that behaviour.
	if tlsCACert == "" && insecureMode {
		creds = insecure.NewCredentials()
	} else {
		tlsConfig, err := buildTLSConfig(tlsCACert, false)
		if err != nil {
			return nil, err
		}
		creds = credentials.NewTLS(tlsConfig)
	}
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, err
	}
	return &OtelLogClientGRPC{client: collectorlogspb.NewLogsServiceClient(conn), conn: conn, headers: headers}, nil
}

// EmitLogs sends log records to the OpenTelemetry collector
func (c *OtelLogClientGRPC) EmitLogs(ctx context.Context, logs []*ResourceLog) error {
	if c.headers != nil {
		ctx = metadata.NewOutgoingContext(ctx, metadata.New(c.headers))
	}
	_, err := c.client.Export(ctx, &collectorlogspb.ExportLogsServiceRequest{ResourceLogs: logs})
	return err
}

// Close closes the gRPC connection
func (c *OtelLogClientGRPC) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
