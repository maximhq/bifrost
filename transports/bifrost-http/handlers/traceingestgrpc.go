package handlers

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/traceingest"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	_ "google.golang.org/grpc/encoding/gzip" // exporters compress with gzip by default
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// traceGRPCMaxMessageBytes caps one OTLP export. Exporters batch a few hundred spans per call.
const traceGRPCMaxMessageBytes = 32 << 20

// TraceIngestGRPCServer serves OTLP trace export over gRPC, the transport arize-otel and many OTel
// SDKs use by default. It runs on its own port, beside the HTTP server, and stores spans through
// the same ingest path as the HTTP adapters.
type TraceIngestGRPCServer struct {
	collectortrace.UnimplementedTraceServiceServer
	handler *TraceIngestHandler
	server  *grpc.Server
	logger  schemas.Logger
}

// NewTraceIngestGRPCServer builds the gRPC server over the HTTP ingest handler's service.
func NewTraceIngestGRPCServer(handler *TraceIngestHandler, logger schemas.Logger) *TraceIngestGRPCServer {
	s := &TraceIngestGRPCServer{handler: handler, logger: logger}
	s.server = grpc.NewServer(grpc.MaxRecvMsgSize(traceGRPCMaxMessageBytes))
	collectortrace.RegisterTraceServiceServer(s.server, s)
	return s
}

// Serve accepts connections on the listener until Stop. It returns when the server stops.
func (s *TraceIngestGRPCServer) Serve(listener net.Listener) error {
	return s.server.Serve(listener)
}

// Stop finishes in-flight exports and closes the listener.
func (s *TraceIngestGRPCServer) Stop() {
	s.server.GracefulStop()
}

// grpcCredentials collects the credentials of a call from its metadata, from the same headers the
// HTTP routes read.
func grpcCredentials(md metadata.MD) []string {
	var candidates []string
	for _, name := range ingestCredentialHeaders {
		for _, value := range md.Get(name) {
			if strings.TrimSpace(value) != "" {
				candidates = append(candidates, strings.TrimSpace(value))
			}
		}
	}
	return candidates
}

// Export stores one OTLP export. Like the HTTP route it rejects nothing it can decode, so the
// answer never carries a partial success.
func (s *TraceIngestGRPCServer) Export(ctx context.Context, request *collectortrace.ExportTraceServiceRequest) (*collectortrace.ExportTraceServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	principal, err := s.handler.authorize(grpcCredentials(md))
	if err != nil {
		return nil, grpcIngestError(err)
	}
	explicitProject := ""
	if values := md.Get("x-project-name"); len(values) > 0 {
		explicitProject = values[0]
	}
	// The source is Arize: its SDKs are the ones that default to gRPC, and a span's own attributes
	// still decide how it is read.
	if _, err := s.handler.ingestOTLP(principal, logstore.TraceSourceArize, otlpResourcesOf(request), explicitProject, true); err != nil {
		if s.logger != nil && status.Code(grpcIngestError(err)) == codes.Unavailable {
			s.logger.Error("trace ingest over gRPC failed: %v", err)
		}
		return nil, grpcIngestError(err)
	}
	return &collectortrace.ExportTraceServiceResponse{}, nil
}

// grpcIngestError maps an ingest error onto the gRPC status an OTLP exporter acts on. Unavailable
// and ResourceExhausted are retried by exporters; the rest are not.
func grpcIngestError(err error) error {
	switch {
	case errors.Is(err, traceingest.ErrUnauthorized):
		return status.Error(codes.Unauthenticated, "send your Bifrost dashboard username and password as the API key")
	case errors.Is(err, traceingest.ErrForbidden):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, traceingest.ErrRateLimited):
		return status.Error(codes.ResourceExhausted, err.Error())
	}
	// The cause is logged, not sent: a store error can name hosts and tables.
	return status.Error(codes.Unavailable, "trace ingest failed, retry")
}
