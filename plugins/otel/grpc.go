package otel

import (
	"context"

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
