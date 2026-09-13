package logging

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// persistenceFaultStore wraps real SQLite persistence. A negative failure count
// fails permanently; otherwise the first N attempts fail before reaching SQLite.
type persistenceFaultStore struct {
	logstore.LogStore
	failures, attempts map[string]int
}

func (s *persistenceFaultStore) fail(ids []string) error {
	failed := false
	for _, id := range ids {
		s.attempts[id]++
		n := s.failures[id]
		if n < 0 || s.attempts[id] <= n {
			failed = true
		}
	}
	if failed {
		return errors.New("injected persistence failure")
	}
	return nil
}
func (s *persistenceFaultStore) BatchCreateIfNotExists(ctx context.Context, rows []*logstore.Log) error {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	if err := s.fail(ids); err != nil {
		return err
	}
	return s.LogStore.BatchCreateIfNotExists(ctx, rows)
}
func (s *persistenceFaultStore) BatchCreateMCPToolLogsIfNotExists(ctx context.Context, rows []*logstore.MCPToolLog) error {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	if err := s.fail(ids); err != nil {
		return err
	}
	return s.LogStore.BatchCreateMCPToolLogsIfNotExists(ctx, rows)
}
func TestWriterPersistence(t *testing.T) {
	type rowCase struct {
		id                  string
		mcp                 bool
		failures, attempts  int
		dropped, badPayload bool
	}
	for _, tc := range []struct {
		name string
		rows []rowCase
	}{
		{"batch_success", []rowCase{{id: "llm", attempts: 1}}},
		{"individual_recovery", []rowCase{{id: "llm", failures: 1, attempts: 2}}},
		{"payload_stripped_success", []rowCase{{id: "llm", failures: 2, attempts: 3, badPayload: true}}},
		{"real_serialization_recovery", []rowCase{{id: "llm", attempts: 3, badPayload: true}}},
		{"final_failure", []rowCase{{id: "llm", failures: -1, attempts: 3, dropped: true}}},
		{"mcp_batch_success", []rowCase{{id: "mcp", mcp: true, attempts: 1}}},
		{"mcp_individual_recovery", []rowCase{{id: "mcp", mcp: true, failures: 1, attempts: 2}}},
		{"mcp_final_failure", []rowCase{{id: "mcp", mcp: true, failures: -1, attempts: 2, dropped: true}}},
		{"mixed_batch", []rowCase{
			{id: "llm-good", attempts: 2},
			{id: "llm-stripped", failures: 2, attempts: 3, badPayload: true},
			{id: "llm-failed", failures: -1, attempts: 3, dropped: true},
			{id: "mcp-good", mcp: true, attempts: 2},
			{id: "mcp-failed", mcp: true, failures: -1, attempts: 2, dropped: true},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			real := newTestStore(t)
			t.Cleanup(func() { require.NoError(t, real.Close(ctx)) })
			store := &persistenceFaultStore{LogStore: real, failures: map[string]int{}, attempts: map[string]int{}}
			p := &LoggerPlugin{ctx: ctx, store: store, logger: testLogger{}}
			type event struct {
				id       string
				readable bool
				err      error
			}
			events := make(chan event, len(tc.rows)+2)
			llmCB := func(row *logstore.Log) {
				saved, err := real.FindByID(ctx, row.ID)
				events <- event{row.ID, saved != nil && saved.ID == row.ID, err}
			}
			mcpCB := func(row *logstore.MCPToolLog) {
				saved, err := real.FindMCPToolLog(ctx, row.ID)
				events <- event{row.ID, saved != nil && saved.ID == row.ID, err}
			}
			var batch []*writeQueueEntry
			expected := map[string]bool{"barrier": true}
			var dropped int64
			for _, r := range tc.rows {
				store.failures[r.id] = r.failures
				if r.dropped {
					dropped++
				} else {
					expected[r.id] = true
				}
				if r.mcp {
					batch = append(batch, &writeQueueEntry{mcpLog: &logstore.MCPToolLog{ID: r.id, Timestamp: time.Now(), Status: "success", ToolName: "test-tool"}, mcpCallback: mcpCB})
				} else {
					params := map[string]any{"good": "preserved"}
					if r.badPayload {
						params["bad"] = make(chan int)
					}
					batch = append(batch, &writeQueueEntry{log: &logstore.Log{ID: r.id, Timestamp: time.Now(), Status: "success", Model: "test-model", ParamsParsed: params}, callback: llmCB})
				}
			}
			// The last successful MCP callback is a positive completion barrier: all
			// LLM callbacks and earlier MCP callbacks have run when it is received.
			batch = append(batch, &writeQueueEntry{mcpLog: &logstore.MCPToolLog{ID: "barrier", Timestamp: time.Now(), Status: "success", ToolName: "barrier"}, mcpCallback: mcpCB})
			p.processBatch(batch)
			require.Equal(t, dropped, p.droppedRequests.Load())
			seen := map[string]int{}
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
		wait:
			for {
				select {
				case e := <-events:
					seen[e.id]++
					if !expected[e.id] {
						t.Errorf("callback fired for unpersisted row %s", e.id)
					}
					if e.err != nil || !e.readable {
						t.Errorf("row unreadable at callback %s: %v", e.id, e.err)
					}
					if e.id == "barrier" {
						break wait
					}
				case <-timer.C:
					t.Fatal("successful MCP completion barrier did not fire")
				}
			}
			select {
			case e := <-events:
				t.Errorf("unexpected callback after barrier: %s", e.id)
			case <-time.After(25 * time.Millisecond):
			}
			for id := range expected {
				require.Equal(t, 1, seen[id], "callback count for %s", id)
			}
			for _, r := range tc.rows {
				require.Equal(t, r.attempts, store.attempts[r.id], "attempt count for %s", r.id)
				if r.mcp {
					saved, err := real.FindMCPToolLog(ctx, r.id)
					if r.dropped {
						require.ErrorIs(t, err, logstore.ErrNotFound)
						require.Zero(t, seen[r.id])
						continue
					}
					require.NoError(t, err)
					require.Equal(t, "test-tool", saved.ToolName)
				} else {
					saved, err := real.FindByID(ctx, r.id)
					if r.dropped {
						require.ErrorIs(t, err, logstore.ErrNotFound)
						require.Zero(t, seen[r.id])
						continue
					}
					require.NoError(t, err)
					require.Equal(t, "test-model", saved.Model)
					require.Equal(t, "success", saved.Status)
					params, ok := saved.ParamsParsed.(map[string]interface{})
					require.True(t, ok, "persisted params must be hydrated")
					require.Equal(t, "preserved", params["good"])
					if r.badPayload {
						require.Nil(t, params["bad"])
					}
				}
			}
		})
	}
}
