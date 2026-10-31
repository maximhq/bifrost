package handlers

import (
	"context"
	"net"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// recordingRedactedKeys records the id list of every redacted lookup. The real
// store treats an empty or nil list as "every row", so a per-request path that
// hands it one reads the whole keys, virtual keys or routing rules table.
type recordingRedactedKeys struct {
	keys, virtualKeys, routingRules [][]string
}

// GetAllRedactedKeys records the lookup and returns nothing.
func (r *recordingRedactedKeys) GetAllRedactedKeys(_ context.Context, ids []string) []schemas.Key {
	r.keys = append(r.keys, ids)
	return nil
}

// GetAllRedactedVirtualKeys records the lookup and returns nothing.
func (r *recordingRedactedKeys) GetAllRedactedVirtualKeys(_ context.Context, ids []string) []tables.TableVirtualKey {
	r.virtualKeys = append(r.virtualKeys, ids)
	return nil
}

// GetAllRedactedRoutingRules records the lookup and returns nothing.
func (r *recordingRedactedKeys) GetAllRedactedRoutingRules(_ context.Context, ids []string) []tables.TableRoutingRule {
	r.routingRules = append(r.routingRules, ids)
	return nil
}

// requireNoEmptyLookup fails when any recorded lookup carried an empty id list.
func (r *recordingRedactedKeys) requireNoEmptyLookup(t *testing.T) {
	t.Helper()
	for name, calls := range map[string][][]string{"keys": r.keys, "virtual keys": r.virtualKeys, "routing rules": r.routingRules} {
		for _, ids := range calls {
			require.NotEmpty(t, ids, "%s lookup with an empty id list reads every row", name)
		}
	}
}

// keylessLogManager serves one page of logs and one MCP page that name no
// selected key, virtual key or routing rule, and no MCP virtual keys.
type keylessLogManager struct {
	logging.LogManager
}

// Search returns one log with no key, virtual key or routing rule.
func (keylessLogManager) Search(context.Context, *logstore.SearchFilters, *logstore.PaginationOptions) (*logstore.SearchResult, error) {
	return &logstore.SearchResult{Logs: []logstore.Log{{ID: "log-1"}}}, nil
}

// GetSessionLogs returns one session log with no key, virtual key or routing rule.
func (keylessLogManager) GetSessionLogs(_ context.Context, sessionID string, _ *logstore.PaginationOptions) (*logstore.SessionDetailResult, error) {
	return &logstore.SessionDetailResult{SessionID: sessionID, Logs: []logstore.Log{{ID: "log-1"}}}, nil
}

// SearchMCPToolLogs returns one MCP log with no virtual key.
func (keylessLogManager) SearchMCPToolLogs(context.Context, *logstore.MCPToolLogSearchFilters, *logstore.PaginationOptions) (*logstore.MCPToolLogSearchResult, error) {
	return &logstore.MCPToolLogSearchResult{Logs: []logstore.MCPToolLog{{ID: "mcp-1"}}}, nil
}

// GetAvailableMCPVirtualKeys returns no virtual keys, as when no MCP log has one.
func (keylessLogManager) GetAvailableMCPVirtualKeys(context.Context, int, string) ([]logging.KeyPair, error) {
	return nil, nil
}

// TestFollowupsLogPagesSkipEmptyRedactedLookups pins that the logs list, the
// session detail, the MCP logs list and the MCP virtual-key filter never ask
// for redacted names with an empty id list when the page names none, since
// the store reads every row for an empty list.
func TestFollowupsLogPagesSkipEmptyRedactedLookups(t *testing.T) {
	SetLogger(&mockLogger{})
	routes := []struct {
		name string
		uri  string
		call func(h *LoggingHandler, ctx *fasthttp.RequestCtx)
		prep func(ctx *fasthttp.RequestCtx)
	}{
		{name: "logs", uri: "/api/logs", call: (*LoggingHandler).getLogs},
		{name: "session", uri: "/api/logs/sessions/s-1", call: (*LoggingHandler).getLogSessionByID,
			prep: func(ctx *fasthttp.RequestCtx) { ctx.SetUserValue("session_id", "s-1") }},
		{name: "mcp logs", uri: "/api/mcp-logs", call: (*LoggingHandler).getMCPLogs},
		// A search query bypasses the filter-data cache, which a bare handler lacks.
		{name: "mcp filterdata", uri: "/api/mcp-logs/filterdata?dimensions=virtual_keys&q=x", call: (*LoggingHandler).getMCPLogsFilterData},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			redacted := &recordingRedactedKeys{}
			h := &LoggingHandler{logManager: keylessLogManager{}, redactedKeysManager: redacted}
			var req fasthttp.Request
			req.SetRequestURI(route.uri)
			ctx := &fasthttp.RequestCtx{}
			ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)
			if route.prep != nil {
				route.prep(ctx)
			}

			route.call(h, ctx)

			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			redacted.requireNoEmptyLookup(t)
		})
	}
}
