package mcp

import (
	"context"
	"net"
	"net/http"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestBuildTLSHTTPClientNeverFallsBackToUnguardedDefault proves buildTLSHTTPClient
// always returns a non-nil client (with or without TLS configured) so callers
// never fall back to the mcp-go library's own default HTTP client, which
// carries no dial guard at all.
func TestBuildTLSHTTPClientNeverFallsBackToUnguardedDefault(t *testing.T) {
	for _, tlsCfg := range []*schemas.MCPTLSConfig{nil, {InsecureSkipVerify: true}} {
		httpClient, err := (&MCPManager{logger: defaultLogger}).buildTLSHTTPClient(tlsCfg)
		require.NoError(t, err)
		require.NotNil(t, httpClient)

		transport, ok := httpClient.Transport.(*http.Transport)
		require.True(t, ok)
		require.NotNil(t, transport.DialContext)
	}
}

// TestBuildTLSHTTPClientBlocksLinkLocal proves the dial-time guard still
// refuses link-local destinations (including the 169.254.169.254 cloud
// metadata endpoint) - the one class of target with no legitimate MCP use
// case under any deployment topology, authenticated or not.
func TestBuildTLSHTTPClientBlocksLinkLocal(t *testing.T) {
	httpClient, err := (&MCPManager{logger: defaultLogger}).buildTLSHTTPClient(nil)
	require.NoError(t, err)
	transport := httpClient.Transport.(*http.Transport)

	_, err = transport.DialContext(context.Background(), "tcp", "169.254.169.254:80")
	require.Error(t, err)
	require.Contains(t, err.Error(), "blocked connection to link-local address")
}

// TestBuildTLSHTTPClientAllowsLoopback proves loopback MCP servers keep
// working: connecting to a local MCP tool server on the same host is the
// documented primary HTTP-client use case
// (docs/mcp/connecting-to-servers.mdx), so the dial-time guard here must not
// reject it. The unauthenticated-caller case is refused earlier, at the HTTP
// handler layer (rejectPrivateMCPTargetIfAuthBypassed), not here.
func TestBuildTLSHTTPClientAllowsLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			conn.Close()
		}
	}()

	httpClient, err := (&MCPManager{logger: defaultLogger}).buildTLSHTTPClient(nil)
	require.NoError(t, err)
	transport := httpClient.Transport.(*http.Transport)

	conn, err := transport.DialContext(context.Background(), "tcp", ln.Addr().String())
	require.NoError(t, err)
	conn.Close()
}

func TestCreateSTDIOConnectionAllowsInlineEnvAssignments(t *testing.T) {
	t.Parallel()

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"TEST_STDIO_ENV_ASSIGNMENT=inline-value"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.NoError(t, err)
}

func TestCreateSTDIOConnectionAllowsSetReferencedEnvVars(t *testing.T) {
	t.Setenv("TEST_STDIO_ENV_REFERENCE_SET", "set-value")

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"TEST_STDIO_ENV_REFERENCE_SET"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.NoError(t, err)
}

func TestCreateSTDIOConnectionRequiresReferencedEnvVars(t *testing.T) {
	t.Setenv("TEST_STDIO_ENV_REFERENCE_MISSING", "")

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"TEST_STDIO_ENV_REFERENCE_MISSING"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "environment variable TEST_STDIO_ENV_REFERENCE_MISSING is not set")
}

func TestCreateSTDIOConnectionRejectsEmptyEnvAssignmentName(t *testing.T) {
	t.Parallel()

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"=inline-value"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "environment variable name is empty")
}
