package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/mcptools"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cross-tenant isolation for Bifrost's MCP server: the scope admit() derives from the
// caller's governance identity must reach the log store on the same ctx the tool handler
// runs under, so a team-A key never sees team-B rows. This drives the real chain -
// admit -> stampQueryScope -> mcp-go tools/call -> mcptools handler -> RDBLogStore.ScopedDB -
// against real rows in sqlite, rather than checking that a filter closure was built.

// tenantLogReader adapts the raw store to warp.LogReader for the three methods query_logs
// and count_logs reach. Every other method is left on the nil embedded interface: reaching
// one would panic, which is the right outcome for a test that claims to exercise only these.
type tenantLogReader struct {
	warp.LogReader
	store logstore.LogStore
}

func (r tenantLogReader) Search(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error) {
	return r.store.SearchLogs(ctx, *filters, *pagination)
}

func (r tenantLogReader) GetLog(ctx context.Context, id string) (*logstore.Log, error) {
	return r.store.FindByID(ctx, id)
}

func (r tenantLogReader) GetStats(ctx context.Context, filters *logstore.SearchFilters) (*logstore.SearchStats, error) {
	return r.store.GetStats(ctx, *filters)
}

// seedTenantLogs writes two rows for team A (under customer X), two for team B (under
// customer Y) and one with no tenant at all, and returns the ids grouped by owner.
func seedTenantLogs(t *testing.T, store logstore.LogStore) map[string][]string {
	t.Helper()
	now := time.Now().UTC()
	owners := map[string][]string{}
	seed := func(id string, team, customer *string) {
		entry := &logstore.Log{
			ID:         id,
			Timestamp:  now.Add(-time.Minute),
			Object:     "chat.completion",
			Provider:   "openai",
			Model:      "gpt-4o",
			Status:     "success",
			TeamID:     team,
			CustomerID: customer,
		}
		require.NoError(t, store.Create(context.Background(), entry))
		switch {
		case team != nil:
			owners[*team] = append(owners[*team], id)
		default:
			owners["none"] = append(owners["none"], id)
		}
		if customer != nil {
			owners[*customer] = append(owners[*customer], id)
		}
	}
	teamA, teamB := "team-a", "team-b"
	custX, custY := "cust-x", "cust-y"
	seed("log-a1", &teamA, &custX)
	seed("log-a2", &teamA, &custX)
	seed("log-b1", &teamB, &custY)
	seed("log-b2", &teamB, &custY)
	seed("log-none", nil, nil)
	return owners
}

func newTenantIsolationHandler(t *testing.T, store logstore.LogStore) *MCPServerHandler {
	t.Helper()
	key, _ := newTestSigningKey(t)
	h := &MCPServerHandler{config: newTestOAuth2Config(&mockOAuth2Store{signingKey: key}, configtables.MCPServerAuthModeHeaders, false)}
	h.mcpServer.Store(mcptools.NewServer(mcptools.StaticDeps(&mcptools.Deps{LogManager: tenantLogReader{store: store}})))
	return h
}

// identityAdmitter is governance as the scoping chain sees it: it admits every request and
// stamps the resolved team/customer onto ctx, which is exactly what StampVirtualKeyScope does
// for a real virtual key.
type identityAdmitter struct {
	teamID, customerID string
}

func (a identityAdmitter) AdmitMCPGatewayRequest(ctx *schemas.BifrostContext) (schemas.Access, *schemas.BifrostError) {
	if a.teamID != "" {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamID, a.teamID)
	}
	if a.customerID != "" {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, a.customerID)
	}
	return nil, nil
}

// callTool admits the request as the given identity, then dispatches a tools/call on the
// very same ctx - the property under test is that the scope stamped by the first step is
// what the second step's store reads see.
func callTool(t *testing.T, h *MCPServerHandler, admitter MCPGatewayAdmitter, tool string, args map[string]any) map[string]any {
	t.Helper()
	h.admitter = admitter
	ctx, bifrostCtx := newRequestCtx()
	require.Nil(t, h.admit(ctx, bifrostCtx))

	req := map[string]any{
		"jsonrpc": mcp.JSONRPC_VERSION,
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": tool, "arguments": args},
	}
	raw, err := json.Marshal(req)
	require.NoError(t, err)

	resp := h.mcpServer.Load().HandleMessage(bifrostCtx, raw)
	encoded, err := json.Marshal(resp)
	require.NoError(t, err)

	var envelope struct {
		Result mcp.CallToolResult `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(encoded, &envelope))
	require.Nil(t, envelope.Error, "tools/call %s failed at the JSON-RPC layer", tool)
	require.False(t, envelope.Result.IsError, "tools/call %s returned a tool error: %s", tool, toolText(t, envelope.Result))

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(toolText(t, envelope.Result)), &payload))
	return payload
}

func toolText(t *testing.T, result mcp.CallToolResult) string {
	t.Helper()
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(mcp.TextContent)
	require.True(t, ok, "expected text content, got %T", result.Content[0])
	return text.Text
}

func rowIDs(t *testing.T, payload map[string]any) []string {
	t.Helper()
	rows, _ := payload["rows"].([]any)
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		m, _ := row.(map[string]any)
		id, _ := m["id"].(string)
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestBifrostMCP_TenantIsolation(t *testing.T) {
	SetLogger(&mockLogger{})
	store, err := logstore.NewLogStore(context.Background(), &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeSQLite,
		Config:  &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "tenant.db")},
	}, &mockLogger{})
	require.NoError(t, err)
	owners := seedTenantLogs(t, store)
	h := newTenantIsolationHandler(t, store)

	// A wide window and a limit above the seeded count, so the only thing narrowing the
	// result is the scope.
	queryArgs := map[string]any{
		"filters": map[string]any{"start_time": "-7d"},
		"limit":   25,
	}
	countArgs := map[string]any{"filters": map[string]any{"start_time": "-7d"}}

	// query_logs and count_logs must agree under every scope: a count that leaks the other
	// tenant's size is still a leak.
	assertScope := func(t *testing.T, admitter MCPGatewayAdmitter, want []string) {
		t.Helper()
		logs := callTool(t, h, admitter, "query_logs", queryArgs)
		assert.Equal(t, sorted(want), rowIDs(t, logs), "query_logs rows")
		assert.EqualValues(t, len(want), logs["total_matching"], "query_logs total_matching")

		count := callTool(t, h, admitter, "count_logs", countArgs)
		assert.EqualValues(t, len(want), count["total_requests"], "count_logs total_requests")
	}

	t.Run("team A sees only team A", func(t *testing.T) {
		assertScope(t, identityAdmitter{teamID: "team-a"}, owners["team-a"])
	})

	t.Run("team B sees only team B", func(t *testing.T) {
		assertScope(t, identityAdmitter{teamID: "team-b"}, owners["team-b"])
	})

	t.Run("customer scope filters by customer_id", func(t *testing.T) {
		assertScope(t, identityAdmitter{customerID: "cust-x"}, owners["cust-x"])
		assertScope(t, identityAdmitter{customerID: "cust-y"}, owners["cust-y"])
	})

	t.Run("team wins over customer when both are stamped", func(t *testing.T) {
		// A team key also carries a customer; scoping by the customer would widen it to every
		// team under that customer. The customer here deliberately differs from team A's own so
		// the two scopes return disjoint rows and the assertion can tell them apart.
		assertScope(t, identityAdmitter{teamID: "team-a", customerID: "cust-y"}, owners["team-a"])
	})

	t.Run("a key with no team or customer is unscoped by design", func(t *testing.T) {
		// Intentional: an admin-style key names no tenant and sees the whole deployment,
		// including the row that belongs to nobody. If this ever tightens, this test is what
		// documents that the behaviour changed.
		all := append(append(append([]string{}, owners["team-a"]...), owners["team-b"]...), owners["none"]...)
		assertScope(t, identityAdmitter{}, all)
	})

	t.Run("get_log_detail refuses a row outside the scope", func(t *testing.T) {
		h.admitter = identityAdmitter{teamID: "team-a"}
		ctx, bifrostCtx := newRequestCtx()
		require.Nil(t, h.admit(ctx, bifrostCtx))

		raw, err := json.Marshal(map[string]any{
			"jsonrpc": mcp.JSONRPC_VERSION, "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "get_log_detail", "arguments": map[string]any{"log_id": owners["team-b"][0]}},
		})
		require.NoError(t, err)
		encoded, err := json.Marshal(h.mcpServer.Load().HandleMessage(bifrostCtx, raw))
		require.NoError(t, err)
		var envelope struct {
			Result mcp.CallToolResult `json:"result"`
		}
		require.NoError(t, json.Unmarshal(encoded, &envelope))
		assert.True(t, envelope.Result.IsError, "a team-A key must not be able to fetch a team-B row by id; got %s", toolText(t, envelope.Result))
		assert.Contains(t, toolText(t, envelope.Result), fmt.Sprintf("could not load log %s", owners["team-b"][0]))
	})
}
