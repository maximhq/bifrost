package logstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// traceReaderStub serves fixed rows by id and a fixed fallback chain, and
// records what the chain was looked up by.
type traceReaderStub struct {
	rows     map[string]*Log
	children []Log
	total    int64
	parentOf string
}

func (r *traceReaderStub) GetLog(_ context.Context, id string) (*Log, error) {
	if row, ok := r.rows[id]; ok {
		return row, nil
	}
	return nil, ErrNotFound
}

func (r *traceReaderStub) Search(_ context.Context, filters *SearchFilters, _ *PaginationOptions) (*SearchResult, error) {
	r.parentOf = filters.ParentRequestID
	total := r.total
	if total == 0 {
		total = int64(len(r.children))
	}
	return &SearchResult{Logs: r.children, Pagination: PaginationOptions{TotalCount: total}}, nil
}

// Any hop resolves the whole chain, ordered by fallback index, and the chain is
// looked up by the root rather than by the id asked for.
func TestBuildRequestTraceResolvesTheChainFromAnyHop(t *testing.T) {
	ts := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	reader := &traceReaderStub{
		rows: map[string]*Log{
			"root": {ID: "root", Timestamp: ts, Status: "error"},
			"hop":  {ID: "hop", Timestamp: ts, Status: "success", FallbackIndex: 1, ParentRequestID: new("root")},
		},
		children: []Log{{ID: "hop", Timestamp: ts, Status: "success", FallbackIndex: 1, ParentRequestID: new("root")}},
	}
	trace, err := BuildRequestTrace(context.Background(), reader, "hop")
	require.NoError(t, err)
	require.Equal(t, "root", trace.RootID)
	require.Equal(t, "root", reader.parentOf)
	require.Equal(t, 2, trace.ChainLength)
	require.Equal(t, []string{"root", "hop"}, []string{trace.Nodes[0].ID, trace.Nodes[1].ID})
	require.Equal(t, "success", trace.FinalStatus)
	require.False(t, trace.TruncatedChain)
	require.Empty(t, trace.Nodes[0].Link, "dashboard links are Warp's to add")
}

// A missing row, or a missing root, is ErrNotFound naming the row that was
// looked up; a chain longer than the cap reports itself truncated.
func TestBuildRequestTraceNotFoundAndTruncation(t *testing.T) {
	_, err := BuildRequestTrace(context.Background(), &traceReaderStub{}, "nope")
	require.True(t, errors.Is(err, ErrNotFound))
	var missing *TraceNotFoundError
	require.True(t, errors.As(err, &missing))
	require.Equal(t, "nope", missing.ID)

	orphan := &traceReaderStub{rows: map[string]*Log{"hop": {ID: "hop", ParentRequestID: new("gone")}}}
	_, err = BuildRequestTrace(context.Background(), orphan, "hop")
	require.True(t, errors.As(err, &missing))
	require.Equal(t, "gone", missing.ID)

	long := &traceReaderStub{
		rows:     map[string]*Log{"root": {ID: "root", Status: "error"}},
		children: []Log{{ID: "hop", FallbackIndex: 1}},
		total:    15,
	}
	trace, err := BuildRequestTrace(context.Background(), long, "root")
	require.NoError(t, err)
	require.True(t, trace.TruncatedChain)
	require.Empty(t, trace.FinalStatus)
}

// Storage groups plugin logs by plugin name, which erases the order in which
// two different plugins actually logged relative to each other - and even
// within one node, capping each plugin's own group separately could return
// more than MaxTraceLogLines lines in total. TraceFromLog must flatten across
// plugins, sort by when each line actually happened, and only then cap once
// for the whole node.
func TestTraceFromLogOrdersPluginLogsAcrossPluginsAndCapsNodeWide(t *testing.T) {
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

	node := TraceFromLog(&Log{ID: "req-plugin-logs", Timestamp: time.Now(), PluginLogs: string(raw)})

	require.Len(t, node.PluginLogs, MaxTraceLogLines,
		"the cap applies once across the whole node, not once per plugin, even though neither plugin alone exceeds it")
	for i := 0; i < MaxTraceLogLines/2; i++ {
		require.Equal(t, "alpha", node.PluginLogs[2*i].Plugin)
		require.Equal(t, fmt.Sprintf("alpha-%d", i), node.PluginLogs[2*i].Message)
		require.Equal(t, "beta", node.PluginLogs[2*i+1].Plugin)
		require.Equal(t, fmt.Sprintf("beta-%d", i), node.PluginLogs[2*i+1].Message)
	}
}
