package mcptools

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

func TestQueryMCPLogsReportsUnavailableWithoutLogs(t *testing.T) {
	result := runToolViaHandler(t, "query_mcp_logs", &Deps{}, map[string]any{"filters": map[string]any{}})
	require.True(t, result.IsError)
	require.Contains(t, result.Content[0].(mcp.TextContent).Text, "logging is not enabled")
}

func TestQueryMCPLogsRejectsUnknownFilters(t *testing.T) {
	_, err := runTool(t, "query_mcp_logs", &Deps{LogManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{"providers": []any{"openai"}},
	})
	require.ErrorContains(t, err, "unknown filter fields")
}

// A null or non-string content_search must fail, not run the query unfiltered
// and pass the broader result off as the answer to the narrow question.
func TestParseMCPFiltersRejectsNonStringContentSearch(t *testing.T) {
	for _, value := range []any{nil, 42.0, map[string]any{}} {
		_, err := parseMCPFilters(map[string]any{"content_search": value}, Now())
		require.ErrorContains(t, err, "content_search must be a string")
	}
	_, err := parseMCPFilters(map[string]any{"content_search": "   "}, Now())
	require.ErrorContains(t, err, "content_search must not be empty")
	_, err = parseMCPFilters(map[string]any{"content_search": strings.Repeat("é", MaxContentSearchChars+1)}, Now())
	require.ErrorContains(t, err, "at most")
	for _, key := range []string{"start_time", "end_time"} {
		_, err = parseMCPFilters(map[string]any{key: nil}, Now())
		require.ErrorContains(t, err, key+" must be a string, got null")
	}
	filters, err := parseMCPFilters(map[string]any{"content_search": "timeout"}, Now())
	require.NoError(t, err)
	require.Equal(t, "timeout", filters.ContentSearch)
}

// has_more follows the total, not the page size: exactly limit matching rows
// is the whole result, and a short page of a larger total has more.
func TestQueryMCPLogsHasMoreFollowsTotal(t *testing.T) {
	rows := []logstore.MCPToolLog{{ID: "a"}, {ID: "b"}}
	for _, tc := range []struct {
		total int64
		want  bool
	}{{2, false}, {3, true}} {
		fake := &fakeLogReader{mcpSearchResult: &logstore.MCPToolLogSearchResult{
			Logs: rows, HasLogs: true, Pagination: logstore.PaginationOptions{TotalCount: tc.total},
		}}
		result, err := runTool(t, "query_mcp_logs", &Deps{LogManager: fake}, map[string]any{"filters": map[string]any{}, "limit": float64(2)})
		require.NoError(t, err)
		require.Equal(t, tc.want, result.(map[string]any)["has_more"], "total %d", tc.total)
	}
}

func TestParseMCPFiltersRejectsUnknownStatus(t *testing.T) {
	_, err := parseMCPFilters(map[string]any{"status": []any{"success", "failed"}}, Now())
	require.ErrorContains(t, err, `status[1] is "failed"; supported statuses are success, error, processing`)
	filters, err := parseMCPFilters(map[string]any{"status": []any{"error", "processing"}}, Now())
	require.NoError(t, err)
	require.Equal(t, []string{"error", "processing"}, filters.Status)
}

func TestQueryMCPLogsProjectsCompactRows(t *testing.T) {
	latency := 12.5
	name := "prod"
	secret := "sk-should-never-appear"
	fake := &fakeLogReader{
		mcpSearchResult: &logstore.MCPToolLogSearchResult{Logs: []logstore.MCPToolLog{
			{
				ID:             "mcp-1",
				Timestamp:      time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
				ToolName:       "github.search",
				ServerLabel:    "github",
				Status:         "success",
				Latency:        &latency,
				VirtualKeyName: &name,
				Arguments:      `{"query":"` + secret + `"}`,
				Result:         `{"hits":1}`,
				VirtualKey:     &tables.TableVirtualKey{Value: schemas.SecretVar{Val: secret}},
			},
		}},
	}
	result, err := runTool(t, "query_mcp_logs", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{"start_time": "-1h"},
	})
	require.NoError(t, err)
	out := result.(map[string]any)
	rows := out["rows"].([]map[string]any)
	require.Len(t, rows, 1)
	require.Equal(t, "mcp-1", rows[0]["id"])
	require.Equal(t, "github.search", rows[0]["tool_name"])
	require.Equal(t, "/workspace/mcp-logs?selected_log=mcp-1", rows[0]["link"])
	require.Contains(t, out, "window")
	require.NotContains(t, rows[0], "arguments")
	require.NotContains(t, rows[0], "result")
	serialized := boundToolResult(result)
	require.NotContains(t, serialized, secret)
	require.NotNil(t, fake.mcpSearchFilters)
}

func TestGetMCPLogDetailIncludesTruncatedPayload(t *testing.T) {
	payload := strings.Repeat("a", DetailContentChars+50)
	fake := &fakeLogReader{
		mcpLog: &logstore.MCPToolLog{
			ID:        "mcp-1",
			Timestamp: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
			ToolName:  "echo",
			Status:    "success",
			Arguments: payload,
			Result:    "ok",
		},
	}
	result, err := runTool(t, "get_mcp_log_detail", &Deps{LogManager: fake}, map[string]any{"log_id": "mcp-1"})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, "ok", out["result"])
	args, ok := out["arguments"].(string)
	require.True(t, ok)
	require.True(t, strings.HasSuffix(args, "... [truncated]"))
	require.LessOrEqual(t, len(args), DetailContentChars+len("... [truncated]"))
}

func TestGetMCPLogDetailPassesCallerContext(t *testing.T) {
	type scopeKey struct{}
	fake := &fakeLogReader{
		mcpLog: &logstore.MCPToolLog{ID: "mcp-1", Timestamp: time.Now(), ToolName: "echo", Status: "success"},
	}
	ctx := context.WithValue(context.Background(), scopeKey{}, "caller-scope")
	_, err := runToolCtx(t, ctx, "get_mcp_log_detail", &Deps{LogManager: fake}, map[string]any{"log_id": "mcp-1"})
	require.NoError(t, err)
	require.Equal(t, "caller-scope", fake.sawContext.Value(scopeKey{}))
}

func TestCountMCPLogsGuidesOnEmpty(t *testing.T) {
	fake := &fakeLogReader{mcpStats: &logstore.MCPToolLogStats{}}
	result, err := runTool(t, "count_mcp_logs", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{},
	})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.EqualValues(t, 0, out["total_executions"])
	require.Contains(t, out["guidance"], "Nothing matched")
	require.Contains(t, out, "window")
}

func TestCountMCPLogsSaysWhenTooManyToList(t *testing.T) {
	fake := &fakeLogReader{mcpStats: &logstore.MCPToolLogStats{TotalExecutions: LargeResultThreshold + 1}}
	result, err := runTool(t, "count_mcp_logs", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{},
	})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, true, out["too_many_to_list"])
	require.Contains(t, out["guidance"], "too many to list")
}

func TestQueryMCPMetricsSummary(t *testing.T) {
	fake := &fakeLogReader{mcpStats: &logstore.MCPToolLogStats{TotalExecutions: 4, SuccessRate: 75}}
	result, err := runTool(t, "query_mcp_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{"start_time": "-1h"},
		"metrics": []any{"summary"},
	})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, int64(4), out["summary"].(*logstore.MCPToolLogStats).TotalExecutions)
	require.NotContains(t, out, "volume", "only the metrics asked for are fetched")
	require.Contains(t, out, "window")
}

func TestQueryMCPMetricsReturnsVolumeAndCost(t *testing.T) {
	fake := &fakeLogReader{
		mcpHistogram:     &logstore.MCPHistogramResult{BucketSizeSeconds: 60},
		mcpCostHistogram: &logstore.MCPCostHistogramResult{BucketSizeSeconds: 60},
	}
	result, err := runTool(t, "query_mcp_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{"start_time": "-1h"},
		"metrics": []any{"volume", "cost"},
	})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Same(t, fake.mcpHistogram, out["volume"])
	require.Same(t, fake.mcpCostHistogram, out["cost"])
	require.NotContains(t, out, "summary")
}

func TestQueryMCPMetricsRequiresMetrics(t *testing.T) {
	_, err := runTool(t, "query_mcp_metrics", &Deps{LogManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{"start_time": "-1h"},
	})
	require.Error(t, err)
}

func TestQueryMCPUsageByFlattensTools(t *testing.T) {
	fake := &fakeLogReader{
		mcpTopTools: &logstore.MCPTopToolsResult{
			Tools: []logstore.MCPTopToolResult{{ToolName: "echo", Count: 3, Cost: 0.01}},
		},
	}
	result, err := runTool(t, "query_mcp_usage_by", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{"start_time": "-1h"},
	})
	require.NoError(t, err)
	tools := result.(map[string]any)["tools"].([]linkedMCPTool)
	require.Len(t, tools, 1)
	require.Equal(t, "echo", tools[0].ToolName)
}

// mcpNotFoundReader answers every MCP log lookup the way the store does for an
// id that is missing or outside the caller's queryscope.
type mcpNotFoundReader struct{ *fakeLogReader }

func (mcpNotFoundReader) GetMCPToolLog(context.Context, string) (*logstore.MCPToolLog, error) {
	return nil, fmt.Errorf("find mcp log: %w", logstore.ErrNotFound)
}

// The MCP tools resolve times against the turn's pinned clock in the asker's
// zone, read end_time as inclusive, and hand the store a UTC window - the same
// rules as the LLM tools, so "yesterday" means the same day on both tables.
func TestMCPFiltersReadTheTurnClock(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	ctx := WithCallerView(context.Background(), CallerView{Clock: time.Date(2026, 9, 18, 15, 0, 0, 0, newYork)})
	fake := &fakeLogReader{}
	result, err := runToolCtx(t, ctx, "query_mcp_logs", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{"start_time": "yesterday", "end_time": "yesterday"},
	})
	require.NoError(t, err)
	wantStart := time.Date(2026, 9, 17, 4, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 9, 18, 3, 59, 59, 999999000, time.UTC)
	require.Equal(t, wantStart, *fake.mcpSearchFilters.StartTime)
	require.Equal(t, wantEnd, *fake.mcpSearchFilters.EndTime)
	require.Equal(t, time.UTC, fake.mcpSearchFilters.StartTime.Location())
	require.Equal(t, time.UTC, fake.mcpSearchFilters.EndTime.Location())
	require.Equal(t, formatWindow(wantStart, wantEnd), result.(map[string]any)["window"])

	// A relative offset counts back from the pinned clock, not the wall clock.
	_, err = runToolCtx(t, ctx, "query_mcp_logs", &Deps{LogManager: fake}, map[string]any{"filters": map[string]any{"start_time": "-1h"}})
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 9, 18, 19, 0, 0, 0, time.UTC), *fake.mcpSearchFilters.EndTime)
	require.Equal(t, time.Date(2026, 9, 18, 19, 0, 0, 0, time.UTC).Add(-time.Hour), *fake.mcpSearchFilters.StartTime)
}

// An argument the tool takes beside filters, sent inside them, is named as
// misplaced rather than listed among unsupported filter fields.
func TestMCPUnknownFilterFieldNamesMisplacedArgument(t *testing.T) {
	result := runToolViaHandler(t, "query_mcp_logs", &Deps{LogManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{"limit": float64(5)},
	})
	require.True(t, result.IsError)
	text := result.Content[0].(mcp.TextContent).Text
	require.Contains(t, text, "unknown filter fields: limit")
	require.Contains(t, text, "limit is an argument of query_mcp_logs, not a filter field")
}

// The MCP tools default to the caller's own executions under the same rules as
// the LLM tools, and say which scope the answer covers.
func TestMCPToolsApplyTheDefaultScope(t *testing.T) {
	caller := CallerView{DefaultUserID: "u-asker"}
	cases := map[string]struct {
		view      *CallerView
		filters   map[string]any
		wantUsers []string
		wantScope string
	}{
		"identified caller, nothing named": {&caller, map[string]any{}, []string{"u-asker"}, "self"},
		"identified caller, scope all":     {&caller, map[string]any{"scope": "all"}, nil, "all"},
		"identified caller, team named":    {&caller, map[string]any{"team_ids": []any{"team-1"}}, nil, "named"},
		"identified caller, project named": {&caller, map[string]any{"project_ids": []any{"proj-1"}}, nil, "named"},
		"identified caller, device named":  {&caller, map[string]any{"device_ids": []any{"dev-1"}}, nil, "named"},
		"identified caller, key named":     {&caller, map[string]any{"virtual_key_ids": []any{"vk-1"}}, nil, "named"},
		"identified caller, tool named":    {&caller, map[string]any{"tool_names": []any{"search"}}, []string{"u-asker"}, "self"},
		"unrestricted caller":              {&CallerView{DefaultUserID: "u-asker", Unrestricted: true}, map[string]any{}, nil, "deployment"},
		"nobody identified":                {nil, map[string]any{}, nil, "all"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if tc.view != nil {
				ctx = WithCallerView(ctx, *tc.view)
			}
			fake := &fakeLogReader{}
			result, err := runToolCtx(t, ctx, "query_mcp_logs", &Deps{LogManager: fake}, map[string]any{"filters": tc.filters})
			require.NoError(t, err)
			require.Equal(t, tc.wantUsers, fake.mcpSearchFilters.UserIDs)
			require.Equal(t, tc.wantScope, result.(map[string]any)["scope"])
		})
	}
	_, err := runTool(t, "query_mcp_logs", &Deps{LogManager: &fakeLogReader{}}, map[string]any{"filters": map[string]any{"scope": "mine"}})
	require.ErrorContains(t, err, `scope must be "all"`)

	// Every MCP aggregate carries the tag, so caller_can_see reaches them too.
	ctx := WithCallerView(context.Background(), CallerView{DefaultUserID: "u-asker", Visibility: "their teams' traffic"})
	for name, args := range map[string]map[string]any{
		"query_mcp_logs":     {},
		"count_mcp_logs":     {},
		"query_mcp_metrics":  {"metrics": []any{"summary"}},
		"query_mcp_usage_by": {},
	} {
		args["filters"] = map[string]any{"scope": "all"}
		result := runToolViaHandlerCtx(t, ctx, name, &Deps{LogManager: &fakeLogReader{}}, args)
		require.False(t, result.IsError, "%s: %v", name, result.Content)
		var out map[string]any
		require.NoError(t, sonic.UnmarshalString(result.Content[0].(mcp.TextContent).Text, &out))
		require.Equal(t, "all", out["scope"], name)
		require.Equal(t, "their teams' traffic", out["caller_can_see"], name)
		require.Contains(t, out, "window", name)
	}
}

// A ranking of tools is a ranking of what the traffic was, so it keeps the
// caller's default rather than widening the way a ranking of people does.
func TestQueryMCPUsageByKeepsTheCallerDefault(t *testing.T) {
	ctx := WithCallerView(context.Background(), CallerView{DefaultUserID: "u-asker"})
	result, err := runToolCtx(t, ctx, "query_mcp_usage_by", &Deps{LogManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{"start_time": matrixStart, "end_time": matrixEnd},
	})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, "self", out["scope"])
	requireMCPLogsLink(t, out["logs_link"], matrixQuery(map[string]string{"user_ids": "u-asker"}), "the link carries the default")
}

// Every filter the MCP logstore applies is offered to the model: a dimension
// the store supports and the schema omits is a question the tools cannot ask.
func TestMCPFilterSchemaCoversTheStoreFilters(t *testing.T) {
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	require.NoError(t, sonic.UnmarshalString(MCPFilterSchema, &schema))
	fields := reflect.TypeOf(logstore.MCPToolLogSearchFilters{})
	for i := 0; i < fields.NumField(); i++ {
		tag := strings.Split(fields.Field(i).Tag.Get("json"), ",")[0]
		require.Contains(t, schema.Properties, tag, "MCPToolLogSearchFilters.%s has no MCPFilterSchema field", fields.Field(i).Name)
	}
	// And every schema field parses: an advertised field the parser refuses is
	// a call that cannot succeed.
	for name := range schema.Properties {
		_, err := parseMCPFilters(map[string]any{name: map[string]any{}}, Now())
		if err != nil {
			require.NotContains(t, err.Error(), "unknown filter fields", "%s is advertised and refused", name)
		}
	}
}

func TestParseMCPFiltersReadsEveryStoreDimension(t *testing.T) {
	filters, err := parseMCPFilters(map[string]any{
		"business_unit_ids": []any{"bu-1"}, "project_ids": []any{"proj-1"}, "device_ids": []any{"dev-1"},
		"user_agents": []any{"curl/8.0"}, "agent_names": []any{"triage"}, "llm_request_ids": []any{"req-1"},
		"session_id": "sess-1", "agent_correlation_id": "run-1",
	}, Now())
	require.NoError(t, err)
	require.Equal(t, []string{"bu-1"}, filters.BusinessUnitIDs)
	require.Equal(t, []string{"proj-1"}, filters.ProjectIDs)
	require.Equal(t, []string{"dev-1"}, filters.DeviceIDs)
	require.Equal(t, []string{"curl/8.0"}, filters.UserAgents)
	require.Equal(t, []string{"triage"}, filters.AgentNames)
	require.Equal(t, []string{"req-1"}, filters.LLMRequestIDs)
	require.Equal(t, "sess-1", filters.SessionID)
	require.Equal(t, "run-1", filters.AgentCorrelationID)

	// The same validating readers as the LLM filters: null, empty and blank are
	// refused rather than read as "no filter".
	for _, bad := range []map[string]any{
		{"agent_names": nil}, {"llm_request_ids": []any{}}, {"project_ids": []any{" "}},
		{"session_id": "  "}, {"agent_correlation_id": 7.0},
	} {
		_, err := parseMCPFilters(bad, Now())
		require.Error(t, err, "%v", bad)
	}
}

// A row names the LLM request and session that made the call, so the caller
// can follow it to get_log_detail or get_session.
func TestMCPLogRowsCarryTheirLLMRequestAndSession(t *testing.T) {
	llm, session, run := "req-llm-1", "sess-1", "run-1"
	fake := &fakeLogReader{mcpSearchResult: &logstore.MCPToolLogSearchResult{Logs: []logstore.MCPToolLog{
		{ID: "mcp-1", ToolName: "search", LLMRequestID: &llm, SessionID: &session, AgentCorrelationID: &run},
		{ID: "mcp-2", ToolName: "search"},
	}}}
	result, err := runTool(t, "query_mcp_logs", &Deps{LogManager: fake}, map[string]any{"filters": map[string]any{}})
	require.NoError(t, err)
	rows := result.(map[string]any)["rows"].([]map[string]any)
	require.Equal(t, "req-llm-1", rows[0]["llm_request_id"])
	require.Equal(t, "sess-1", rows[0]["session_id"])
	require.Equal(t, "run-1", rows[0]["agent_correlation_id"])
	require.NotContains(t, rows[1], "llm_request_id")
	require.NotContains(t, rows[1], "session_id")
}

// A missing MCP log says what was searched: for a restricted caller an id
// outside their view looks exactly like one that does not exist.
func TestGetMCPLogDetailNotFoundIsVisibilityAware(t *testing.T) {
	deps := &Deps{LogManager: mcpNotFoundReader{&fakeLogReader{}}}
	_, err := runTool(t, "get_mcp_log_detail", deps, map[string]any{"log_id": "mcp-9"})
	require.EqualError(t, err, "no mcp log found with id mcp-9")

	ctx := WithCallerView(context.Background(), CallerView{DefaultUserID: "u-1", Visibility: "their teams' traffic"})
	_, err = runToolCtx(t, ctx, "get_mcp_log_detail", deps, map[string]any{"log_id": "mcp-9"})
	require.ErrorContains(t, err, "among the rows the person asking may see (their teams' traffic)")
	require.ErrorContains(t, err, "rather than that the id does not exist or is mistyped")
}
