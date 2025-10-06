package otel

import (
	"context"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// OtelClientGRPC is the implementation of the OpenTelemetry client for gRPC
type OtelClientGRPC struct {
	client collectorpb.TraceServiceClient
	conn   *grpc.ClientConn
}

// NewOtelClientGRPC creates a new OpenTelemetry client for gRPC
func NewOtelClientGRPC(endpoint string) (*OtelClientGRPC, error) {
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &OtelClientGRPC{client: collectorpb.NewTraceServiceClient(conn), conn: conn}, nil
}

// Emit sends a trace to the OpenTelemetry collector
func (c *OtelClientGRPC) Emit(ctx context.Context, rs []*ResourceSpan) error {
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
