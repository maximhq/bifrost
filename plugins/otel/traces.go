// Implements gRPC and HTTP clients for exporting traces
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
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// OtelClientGRPC is the implementation of the OpenTelemetry client for gRPC
type OtelClientGRPC struct {
	client  collectorpb.TraceServiceClient
	conn    *grpc.ClientConn
	headers map[string]string
}

// NewOtelClientGRPC creates a new OpenTelemetry client for gRPC
func NewOtelClientGRPC(endpoint string, headers map[string]string, tlsCACert string, insecureMode bool) (*OtelClientGRPC, error) {
	var creds credentials.TransportCredentials
	if tlsCACert == "" && insecureMode {
		creds = insecure.NewCredentials()
	} else {
		tlsConfig, err := createTLSConfig(tlsCACert, insecureMode)
		if err != nil {
			return nil, err
		}
		creds = credentials.NewTLS(tlsConfig)
	}
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, err
	}
	return &OtelClientGRPC{client: collectorpb.NewTraceServiceClient(conn), conn: conn, headers: headers}, nil
}

// Emit sends a trace to the OpenTelemetry collector
func (c *OtelClientGRPC) Emit(ctx context.Context, rs []*ResourceSpan) error {
	if c.headers != nil {
		ctx = metadata.NewOutgoingContext(ctx, metadata.New(c.headers))
	}
	_, err := c.client.Export(ctx, &collectorpb.ExportTraceServiceRequest{ResourceSpans: rs})
	return err
}

// Close closes the gRPC connection
func (c *OtelClientGRPC) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

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

	if tlsConfig, err := createTLSConfig(tlsCACert, insecureMode); err == nil {
		transport.TLSClientConfig = tlsConfig
	} else {
		return nil, err
	}

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

// initOTELTraceExportClient init OTEL client for exporting traces to an OTEL collector endpoint.
func initOTELTraceExportClient(config *Config) (OtelClient, error) {
	// Resolve env. prefixed header values from environment variables
	if config.Headers != nil {
		if err := resolveEnvHeaders(config.Headers); err != nil {
			return nil, err
		}
	}
	var (
		client OtelClient
		err    error
	)
	if config.Protocol == ProtocolGRPC {
		client, err = NewOtelClientGRPC(config.CollectorURL, config.Headers, config.TLSCACert, config.Insecure)
		if err != nil {
			return nil, err
		}
	}
	if config.Protocol == ProtocolHTTP {
		client, err = NewOtelClientHTTP(config.CollectorURL, config.Headers, config.TLSCACert, config.Insecure)
		if err != nil {
			return nil, err
		}
	}
	if client == nil {
		return nil, fmt.Errorf("otel client is not initialized. invalid protocol type")
	}
	return client, nil
}
