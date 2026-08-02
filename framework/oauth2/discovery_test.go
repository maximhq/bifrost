package oauth2

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestNewOAuthDiscoveryHTTPClientBlocksLoopback proves the OAuth discovery HTTP
// client is wired through the SSRF-safe dialer: a target the discovery chain
// picked up from a (potentially malicious) upstream MCP server's own response
// must not be reachable if it points at loopback or any other private/
// link-local/CGNAT address. TestMain overrides the dialer package-wide for
// this package's other tests (which talk to real httptest.Server loopback
// addresses) - restore the production dialer for the duration of this test so
// it exercises the actual guard.
func TestNewOAuthDiscoveryHTTPClientBlocksLoopback(t *testing.T) {
	prev := testDialContextOverride
	testDialContextOverride = nil
	t.Cleanup(func() { testDialContextOverride = prev })

	client := newOAuthDiscoveryHTTPClient(time.Second)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.DialContext)

	_, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:80")
	require.Error(t, err)
	require.Contains(t, err.Error(), "blocked connection to non-public address")
}
