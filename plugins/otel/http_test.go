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

// TestOtelClientHTTPUsesGlobalProxy pins that the OTLP/HTTP trace exporter honours the
// global proxy when it is enabled for API traffic. It used to follow only the
// environment proxy.
func TestOtelClientHTTPUsesGlobalProxy(t *testing.T) {
	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)
	set := proxytest.NewSet(t)
	network.SetDefaultHTTPClientFactory(network.NewHTTPClientFactory(&network.GlobalProxyConfig{
		Enabled: true, Type: network.GlobalProxyTypeHTTP, URL: "http://127.0.0.1:" + set.Config.Port(), EnableForAPI: true,
	}, nil))
	t.Cleanup(func() { network.SetDefaultHTTPClientFactory(nil) })

	client, err := NewOtelClientHTTP("http://203.0.113.10:4318", nil, "", true, 3*time.Second)
	if err != nil {
		t.Fatalf("NewOtelClientHTTP: %v", err)
	}
	defer client.Close()
	_ = client.Emit(context.Background(), nil)
	proxytest.AssertRoute(t, set, proxytest.Route{Proxy: "config"}, "203.0.113.10:4318", nil)
}
