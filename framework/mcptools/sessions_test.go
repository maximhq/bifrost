package mcptools

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

func TestGetSessionReportsUnavailableWithoutLogs(t *testing.T) {
	result := runToolViaHandler(t, "get_session", &Deps{}, map[string]any{"session_id": "s-1"})
	require.True(t, result.IsError)
	require.Contains(t, result.Content[0].(mcp.TextContent).Text, "logging is not enabled")
}

func TestGetSessionListsOldestFirst(t *testing.T) {
	fake := &fakeLogReader{
		sessionLogs: &logstore.SessionDetailResult{
			SessionID: "s-1",
			Count:     2,
			HasMore:   false,
			Logs: []logstore.Log{
				{ID: "r-1", Timestamp: time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC), Provider: "openai", Model: "gpt-4o", Status: "success"},
				{ID: "r-2", Timestamp: time.Date(2026, 9, 18, 10, 1, 0, 0, time.UTC), Provider: "openai", Model: "gpt-4o", Status: "success"},
			},
		},
	}
	type scopeKey struct{}
	ctx := context.WithValue(context.Background(), scopeKey{}, "caller-scope")
	result, err := runToolCtx(t, ctx, "get_session", &Deps{LogManager: fake}, map[string]any{"session_id": "s-1"})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, "s-1", out["session_id"])
	require.EqualValues(t, 2, out["count"])
	require.Equal(t, false, out["has_more"])
	rows := out["rows"].([]LogRow)
	require.Equal(t, "r-1", rows[0].ID)
	require.Equal(t, "s-1", fake.sessionIDSeen)
	require.Equal(t, "caller-scope", fake.sawContext.Value(scopeKey{}))
}

func TestGetSessionSummary(t *testing.T) {
	fake := &fakeLogReader{
		sessionSummary: &logstore.SessionSummaryResult{SessionID: "s-1", Count: 3, TotalCost: 0.12, TotalTokens: 400},
	}
	result, err := runTool(t, "get_session_summary", &Deps{LogManager: fake}, map[string]any{"session_id": "s-1"})
	require.NoError(t, err)
	summary := result.(*logstore.SessionSummaryResult)
	require.Equal(t, int64(3), summary.Count)
	require.InDelta(t, 0.12, summary.TotalCost, 0.0001)
}

func TestGetDroppedRequests(t *testing.T) {
	fake := &fakeLogReader{droppedRequests: 7}
	result, err := runTool(t, "get_dropped_requests", &Deps{LogManager: fake}, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, int64(7), result.(map[string]any)["dropped_requests"])
}

// sessionNotFoundReader answers session lookups the way a store that reports a
// missing session as ErrNotFound would.
type sessionNotFoundReader struct{ *fakeLogReader }

func (sessionNotFoundReader) GetSessionLogs(context.Context, string, *logstore.PaginationOptions) (*logstore.SessionDetailResult, error) {
	return nil, fmt.Errorf("session: %w", logstore.ErrNotFound)
}

func (sessionNotFoundReader) GetSessionSummary(context.Context, string) (*logstore.SessionSummaryResult, error) {
	return nil, fmt.Errorf("session: %w", logstore.ErrNotFound)
}

// A session counts only the requests the caller may see, so one wholly outside
// their view loads as empty, exactly like an unknown id. The refusal says what
// was searched rather than calling the id wrong, for an empty load and for
// ErrNotFound alike.
func TestSessionToolsNotFoundIsVisibilityAware(t *testing.T) {
	restricted := WithCallerView(context.Background(), CallerView{DefaultUserID: "u-1", Visibility: "their teams' traffic"})
	for _, name := range []string{"get_session", "get_session_summary"} {
		for reader, deps := range map[string]*Deps{
			"empty":     {LogManager: &fakeLogReader{}},
			"not found": {LogManager: sessionNotFoundReader{&fakeLogReader{}}},
		} {
			_, err := runTool(t, name, deps, map[string]any{"session_id": "s-9"})
			require.EqualError(t, err, `no session found with id "s-9"`, "%s, %s", name, reader)

			_, err = runToolCtx(t, restricted, name, deps, map[string]any{"session_id": "s-9"})
			require.ErrorContains(t, err, "among the requests the person asking may see (their teams' traffic)", "%s, %s", name, reader)
			require.ErrorContains(t, err, "rather than that the id does not exist or is mistyped", "%s, %s", name, reader)
		}
	}
}
