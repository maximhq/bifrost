package otel

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/network/proxytest"
	"github.com/maximhq/bifrost/core/schemas"
)

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
