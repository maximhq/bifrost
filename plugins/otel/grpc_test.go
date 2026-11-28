package otel

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/network/proxytest"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

type partialSuccessCollector struct {
	collectorpb.UnimplementedTraceServiceServer
	calls    atomic.Int64
	response *collectorpb.ExportTraceServiceResponse
}

func (c *partialSuccessCollector) Export(context.Context, *collectorpb.ExportTraceServiceRequest) (*collectorpb.ExportTraceServiceResponse, error) {
	c.calls.Add(1)
	return c.response, nil
}

// TestOtelClientGRPCUsesGlobalProxy pins that the OTLP/gRPC trace exporter dials through
// the global proxy when it is enabled for API traffic (network.DefaultGRPCDialer).
func TestOtelClientGRPCUsesGlobalProxy(t *testing.T) {
	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)
	set := proxytest.NewSet(t)
	network.SetDefaultHTTPClientFactory(network.NewHTTPClientFactory(&network.GlobalProxyConfig{
		Enabled: true, Type: network.GlobalProxyTypeHTTP, URL: "http://127.0.0.1:" + set.Config.Port(), EnableForAPI: true,
	}, nil))
	t.Cleanup(func() { network.SetDefaultHTTPClientFactory(nil) })

	client, err := NewOtelClientGRPC("collector.bifrost.test:4317", nil, "", true)
	if err != nil {
		t.Fatalf("NewOtelClientGRPC: %v", err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = client.Emit(ctx, nil)
	seen := set.Config.Seen()
	if len(seen) == 0 || seen[0].Target != "collector.bifrost.test:4317" {
		t.Fatalf("proxy saw %+v, want a CONNECT to collector.bifrost.test:4317", seen)
	}
}

func TestTraceAcknowledgementGRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	receiver := &partialSuccessCollector{
		response: &collectorpb.ExportTraceServiceResponse{
			PartialSuccess: &collectorpb.ExportTracePartialSuccess{
				RejectedSpans: 3,
				ErrorMessage:  "dropped spans",
			},
		},
	}
	server := grpc.NewServer()
	collectorpb.RegisterTraceServiceServer(server, receiver)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	client, err := NewOtelClientGRPC(listener.Addr().String(), map[string]string{"Authorization": "Bearer secret-token"}, "", true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	result, err := client.emit(t.Context(), nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, receiver.calls.Load())
	require.EqualValues(t, 3, result.partialSuccess.GetRejectedSpans())
	require.Equal(t, "dropped spans", result.partialSuccess.GetErrorMessage())
}
