package oauth2

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/network/proxytest"
)

// TestDiscoverOAuthMetadataUsesGlobalProxy pins that MCP OAuth discovery honours the
// global proxy when it is enabled for API traffic. It used to follow only the
// environment proxy.
func TestDiscoverOAuthMetadataUsesGlobalProxy(t *testing.T) {
	set := proxytest.NewSet(t)
	network.SetDefaultHTTPClientFactory(network.NewHTTPClientFactory(&network.GlobalProxyConfig{
		Enabled: true, Type: network.GlobalProxyTypeHTTP, URL: "http://127.0.0.1:" + set.Config.Port(), EnableForAPI: true,
	}, nil))
	t.Cleanup(func() { network.SetDefaultHTTPClientFactory(nil) })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = DiscoverOAuthMetadata(ctx, "https://mcp.bifrost.test/mcp")
	seen := set.Config.Seen()
	if len(seen) == 0 {
		t.Fatal("OAuth discovery never reached the global proxy")
	}
	for _, hit := range seen {
		if hit.Target != "mcp.bifrost.test:443" {
			t.Errorf("proxy saw %+v, want only CONNECTs to mcp.bifrost.test:443", hit)
		}
	}
}
