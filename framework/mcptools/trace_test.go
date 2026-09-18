package mcptools

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

func TestGetRequestTraceRequiresID(t *testing.T) {
	_, err := runTool(t, "get_request_trace", &Deps{LogManager: &fakeLogReader{}}, map[string]any{})
	require.ErrorContains(t, err, "log_id is required")
}

func TestGetRequestTraceNotFound(t *testing.T) {
	_, err := runTool(t, "get_request_trace", &Deps{LogManager: &fakeLogReader{}}, map[string]any{"log_id": "missing"})
	require.ErrorContains(t, err, "missing")
}

// A request with no parent and no fallback children is a chain of one: itself.
// Every optional causal field (retries, error, cache, guardrails, plugin and
// routing logs, overhead) is exercised here so a nil-guard regression on any
// one of them fails this test rather than surfacing as a panic on real traffic.
func TestGetRequestTraceSingleNodeProjectsEveryField(t *testing.T) {
	ts := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	failReason := "rate_limit_error"
	stopReason := "stop"

	entry := &logstore.Log{
		ID:              "req-root",
		Timestamp:       ts,
		Provider:        "openai",
		Model:           "gpt-4o",
		Status:          "error",
		StopReason:      &stopReason,
		Latency:         new(1500.0),
		UpstreamLatency: new(1200.0),
		OverheadLatency: new(300.0),
		NumberOfRetries: 1,
		AttemptTrailParsed: []schemas.KeyAttemptRecord{
			{Attempt: 1, KeyID: "key-1", KeyName: "primary", FailReason: &failReason, TriggeredRotation: true},
			{Attempt: 2, KeyID: "key-2", KeyName: "secondary", TriggeredRotation: false},
		},
		ErrorDetailsParsed: &schemas.BifrostError{
			StatusCode: new(429),
			Error: &schemas.ErrorField{
				Type:    new("rate_limit_error"),
				Code:    new("rate_limited"),
				Message: "rate limit exceeded",
			},
		},
		CacheDebugParsed: &schemas.BifrostCacheMetadata{
			CacheHit:   true,
			HitType:    new("semantic"),
			Similarity: new(0.92),
			Threshold:  new(0.85),
		},
		GuardrailDebugParsed: &schemas.BifrostGuardrailMetadata{
			JudgeCalls: []schemas.BifrostGuardrailJudgeCall{
				{Phase: "pre", RuleName: "pii-check", GuardrailName: "pii-guard", Action: "block", Reason: "detected SSN"},
			},
		},
		PluginLogs:        `{"governance":[{"plugin_name":"governance","level":"info","message":"budget check passed","timestamp":1700000000000}]}`,
		RoutingEngineLogs: "[1700000000000] [governance] [info] - budget check passed\n[1700000000100] [routing-rule] [info] - matched rule prod-fallback\n",
		OverheadBreakdownParsed: []logstore.OverheadBucket{
			{Name: "key.selection", Kind: "core", DurationUs: 1500},
			{Name: "plugin.governance", Kind: "plugin", DurationUs: 2500},
		},
	}

	fake := &fakeLogReader{
		getLogFunc: func(_ context.Context, id string) (*logstore.Log, error) {
			require.Equal(t, "req-root", id)
			return entry, nil
		},
		searchResult: &logstore.SearchResult{Logs: nil, Pagination: logstore.PaginationOptions{TotalCount: 0}},
	}

	result, err := runTool(t, "get_request_trace", &Deps{LogManager: fake}, map[string]any{"log_id": "req-root"})
	require.NoError(t, err)

	out, ok := result.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "req-root", out["root_id"])
	require.Equal(t, 1, out["chain_length"])
	require.Equal(t, "error", out["final_status"])
	require.Equal(t, false, out["truncated_chain"])
	require.NotEmpty(t, out["link"])

	nodes, ok := out["nodes"].([]traceNode)
	require.True(t, ok)
	require.Len(t, nodes, 1)
	node := nodes[0]

	require.Equal(t, "req-root", node.ID)
	require.Equal(t, 0, node.FallbackIndex)
	require.Equal(t, "openai", node.Provider)
	require.Equal(t, "gpt-4o", node.Model)
	require.Equal(t, "stop", node.StopReason)
	require.Equal(t, 1500.0, node.LatencyMs)
	require.Equal(t, 1200.0, node.UpstreamLatencyMs)
	require.Equal(t, 300.0, node.OverheadLatencyMs)

	require.Len(t, node.Attempts, 2)
	require.Equal(t, "rate_limit_error", node.Attempts[0].FailReason)
	require.True(t, node.Attempts[0].TriggeredRotation)
	require.False(t, node.Attempts[1].TriggeredRotation)

	require.NotNil(t, node.Error)
	require.Equal(t, 429, node.Error.StatusCode)
	require.Equal(t, "rate_limit_error", node.Error.Type)
	require.Equal(t, "rate_limited", node.Error.Code)
	require.Equal(t, "rate limit exceeded", node.Error.Message)

	require.NotNil(t, node.Cache)
	require.True(t, node.Cache.Hit)
	require.Equal(t, "semantic", node.Cache.HitType)
	require.InDelta(t, 0.92, *node.Cache.Similarity, 0.0001)

	require.Len(t, node.Guardrails, 1)
	require.Equal(t, "block", node.Guardrails[0].Action)
	require.Equal(t, "detected SSN", node.Guardrails[0].Reason)

	require.Len(t, node.PluginLogs, 1)
	require.Equal(t, "governance", node.PluginLogs[0].Plugin)
	require.Equal(t, "info", node.PluginLogs[0].Level)
	require.Equal(t, "budget check passed", node.PluginLogs[0].Message)
	require.Equal(t, time.UnixMilli(1700000000000).UTC().Format(time.RFC3339Nano), node.PluginLogs[0].Timestamp)

	require.Len(t, node.RoutingEngineLogs, 2)
	require.Contains(t, node.RoutingEngineLogs[0], "budget check passed")

	require.Len(t, node.OverheadBreakdown, 2)
	require.Equal(t, "key.selection", node.OverheadBreakdown[0].Name)
	require.InDelta(t, 1.5, node.OverheadBreakdown[0].DurationMs, 0.0001)
}

// Storage groups plugin logs by plugin name, which erases the order in which
// two different plugins actually logged relative to each other - and even
// within one node, capping each plugin's own group separately could return
// more than MaxTraceLogLines lines in total. traceFromLog must flatten across
// plugins, sort by when each line actually happened, and only then cap once
// for the whole node.
func TestGetRequestTracePluginLogsOrderedAcrossPluginsAndCappedNodeWide(t *testing.T) {
	base := int64(1700000000000)
	grouped := map[string][]schemas.PluginLogEntry{}
	// Two plugins, 15 lines each (30 total, comfortably over MaxTraceLogLines),
	// interleaved one millisecond apart so the merged chronological order
	// alternates between them rather than grouping one plugin's lines first.
	for i := range 15 {
		grouped["alpha"] = append(grouped["alpha"], schemas.PluginLogEntry{
			PluginName: "alpha", Level: schemas.LogLevelInfo, Message: fmt.Sprintf("alpha-%d", i),
			Timestamp: base + int64(i*2),
		})
		grouped["beta"] = append(grouped["beta"], schemas.PluginLogEntry{
			PluginName: "beta", Level: schemas.LogLevelInfo, Message: fmt.Sprintf("beta-%d", i),
			Timestamp: base + int64(i*2) + 1,
		})
	}
	raw, err := sonic.Marshal(grouped)
	require.NoError(t, err)

	node := traceFromLog(&logstore.Log{ID: "req-plugin-logs", Timestamp: time.Now(), PluginLogs: string(raw)})

	require.Len(t, node.PluginLogs, MaxTraceLogLines,
		"the cap applies once across the whole node, not once per plugin, even though neither plugin alone exceeds it")
	for i := 0; i < MaxTraceLogLines/2; i++ {
		require.Equal(t, "alpha", node.PluginLogs[2*i].Plugin)
		require.Equal(t, fmt.Sprintf("alpha-%d", i), node.PluginLogs[2*i].Message)
		require.Equal(t, "beta", node.PluginLogs[2*i+1].Plugin)
		require.Equal(t, fmt.Sprintf("beta-%d", i), node.PluginLogs[2*i+1].Message)
	}
}

// Passing the id of a fallback hop, not the root, must still resolve and
// return the whole chain - the model has no way to know in advance which one
// a pasted or previously-seen id refers to.
func TestGetRequestTraceResolvesFullChainFromAnyHop(t *testing.T) {
	ts := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	root := &logstore.Log{ID: "req-root", Timestamp: ts, Provider: "openai", Model: "gpt-4o", Status: "error", FallbackIndex: 0}
	fallback := &logstore.Log{
		ID: "req-fallback-1", Timestamp: ts.Add(2 * time.Second), Provider: "anthropic", Model: "claude-sonnet-5",
		Status: "success", FallbackIndex: 1, ParentRequestID: new("req-root"),
	}

	logsByID := map[string]*logstore.Log{"req-root": root, "req-fallback-1": fallback}
	fake := &fakeLogReader{
		getLogFunc: func(_ context.Context, id string) (*logstore.Log, error) {
			entry, ok := logsByID[id]
			if !ok {
				return nil, nil
			}
			return entry, nil
		},
		searchResult: &logstore.SearchResult{
			Logs:       []logstore.Log{*fallback},
			Pagination: logstore.PaginationOptions{TotalCount: 1},
		},
	}

	// Ask about the fallback hop, not the root.
	result, err := runTool(t, "get_request_trace", &Deps{LogManager: fake}, map[string]any{"log_id": "req-fallback-1"})
	require.NoError(t, err)

	out := result.(map[string]any)
	require.Equal(t, "req-root", out["root_id"])
	require.Equal(t, 2, out["chain_length"])
	require.Equal(t, "success", out["final_status"], "the chain's last hop succeeded, and final_status should reflect that, not the root's own status")

	nodes := out["nodes"].([]traceNode)
	require.Len(t, nodes, 2)
	require.Equal(t, "req-root", nodes[0].ID)
	require.Equal(t, "req-fallback-1", nodes[1].ID)

	require.Equal(t, "req-root", fake.searchFilters.ParentRequestID, "the fallback chain must be looked up by the resolved root, not the id the model passed")
}

// A chain longer than the tool's cap should be reported as such, so the model
// does not read a capped list as the complete fallback history.
func TestGetRequestTraceReportsTruncatedChain(t *testing.T) {
	ts := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	root := &logstore.Log{ID: "req-root", Timestamp: ts, Provider: "openai", Model: "gpt-4o", Status: "error"}

	fake := &fakeLogReader{
		getLogFunc: func(_ context.Context, id string) (*logstore.Log, error) { return root, nil },
		searchResult: &logstore.SearchResult{
			Logs:       []logstore.Log{{ID: "req-fallback-1", FallbackIndex: 1, ParentRequestID: new("req-root")}},
			Pagination: logstore.PaginationOptions{TotalCount: 15},
		},
	}

	result, err := runTool(t, "get_request_trace", &Deps{LogManager: fake}, map[string]any{"log_id": "req-root"})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, true, out["truncated_chain"])
	require.NotContains(t, out, "final_status", "a truncated chain's last fetched node isn't necessarily the terminal hop, so final_status must not be inferred from it")
}
