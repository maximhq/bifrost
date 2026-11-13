package handlers

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/traceingest"
	"github.com/valyala/fasthttp"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	_ "google.golang.org/grpc/encoding/gzip" // exporters compress with gzip by default
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
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

// traceGRPCGracefulStopTimeout bounds how long Stop waits for in-flight exports before it closes
// them, so one slow export cannot hold up shutdown.
const traceGRPCGracefulStopTimeout = 10 * time.Second

// Stop finishes in-flight exports and closes the listener.
func (s *TraceIngestGRPCServer) Stop() {
	s.stop(traceGRPCGracefulStopTimeout)
}

func (s *TraceIngestGRPCServer) stop(gracefulTimeout time.Duration) {
	if s == nil || s.server == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		s.server.GracefulStop()
		close(done)
	}()
	timer := time.NewTimer(gracefulTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		s.server.Stop()
		<-done
	}
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

// principal resolves who an export acts as, the way the HTTP routes do: an installed authenticator
// decides, reading the credential metadata as request headers; without one, the dashboard
// credentials are checked.
func (s *TraceIngestGRPCServer) principal(md metadata.MD) (*traceingest.Principal, error) {
	if s.handler.authenticator == nil {
		return s.handler.authorize(grpcCredentials(md))
	}
	request := &fasthttp.RequestCtx{}
	for _, name := range ingestCredentialHeaders {
		for _, value := range md.Get(name) {
			request.Request.Header.Add(name, value)
		}
	}
	return s.handler.authenticator.AuthenticateTraceIngest(request)
}

// Export stores one OTLP export. Like the HTTP route it rejects nothing it can decode, so the
// answer never carries a partial success.
func (s *TraceIngestGRPCServer) Export(ctx context.Context, request *collectortrace.ExportTraceServiceRequest) (*collectortrace.ExportTraceServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	principal, err := s.principal(md)
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
		// Exporters retry RESOURCE_EXHAUSTED only when RetryInfo says when to come back.
		limited := status.New(codes.ResourceExhausted, err.Error())
		if withRetry, detailErr := limited.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(time.Second)}); detailErr == nil {
			limited = withRetry
		}
		return limited.Err()
	}
	// The cause is logged, not sent: a store error can name hosts and tables.
	return status.Error(codes.Unavailable, "trace ingest failed, retry")
}
