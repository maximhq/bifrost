package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// skipWarnLogger counts the "Skipping MCP tool" warnings, which every sync used to repeat.
type skipWarnLogger struct {
	MockLogger
	mu    sync.Mutex
	warns []string
}

func (l *skipWarnLogger) Warn(msg string, args ...any) {
	line := fmt.Sprintf(msg, args...)
	if !strings.Contains(line, "Skipping MCP tool") {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, line)
}

func (l *skipWarnLogger) skipWarnCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.warns)
}

// buildInvalidToolMCPServer serves a valid "echo" tool and a REST-path tool name that
// Bifrost cannot expose, like the ones an OpenAPI-derived MCP server lists.
func buildInvalidToolMCPServer(t *testing.T) *httptest.Server {
	t.Helper()

	s := server.NewMCPServer("invalid-tool", "1.0.0", server.WithToolCapabilities(true))
	for _, name := range []string{"echo", "PUT_/v1/cloud/project/{serviceName}/storage/{containerId}"} {
		s.AddTool(mcpgo.NewTool(name), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("ok"), nil
		})
	}

	streamable := server.NewStreamableHTTPServer(s)
	ts := httptest.NewServer(http.HandlerFunc(streamable.ServeHTTP))
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})
	return ts
}

func TestToolSync_WarnsOncePerSkippedTool(t *testing.T) {
	ts := buildInvalidToolMCPServer(t)
	logger := &skipWarnLogger{}

	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, logger, nil)
	t.Cleanup(func() { _ = m.Cleanup() })

	config := newRefreshConfig("skipwarn", "skipwarn", ts.URL, false)
	require.NoError(t, m.AddClient(context.Background(), config))
	require.Equal(t, 1, logger.skipWarnCount(), "the invalid tool is warned about when the client connects")

	for i := 0; i < 2; i++ {
		_, err := m.RefreshClientTools(context.Background(), config.ID)
		require.NoError(t, err)
	}
	require.Equal(t, 1, logger.skipWarnCount(), "later syncs of the same invalid tool must not warn again")

	// A client removed and added back under the same name starts fresh.
	require.NoError(t, m.RemoveClient(config.ID))
	require.NoError(t, m.AddClient(context.Background(), config))
	require.Equal(t, 2, logger.skipWarnCount(), "a re-added client warns about its invalid tool again")
}
