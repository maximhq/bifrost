package otel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
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
	// TLS priority: custom CA > system roots > insecure
	if tlsCACert != "" {
		// Validate the CA cert path to prevent path traversal attacks
		if err := validateCACertPath(tlsCACert); err != nil {
			return nil, err
		}
		// Use custom CA certificate with MinVersion
		caCert, err := os.ReadFile(tlsCACert)
		if err != nil {
			return nil, fmt.Errorf("fail to load provided CA cert: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("fail to parse provided CA cert")
		}
		tlsConfig := &tls.Config{
			RootCAs:    caCertPool,
			MinVersion: tls.VersionTLS12,
		}
		creds = credentials.NewTLS(tlsConfig)
	} else if insecureMode {
		// Skip TLS entirely
		creds = insecure.NewCredentials()
	} else {
		// Use system root CAs with MinVersion
		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
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
