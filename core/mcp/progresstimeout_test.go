package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func enableProgressReset(t *testing.T, maxTotal time.Duration) {
	t.Helper()
	progressSettingsOnce.Do(func() {})
	oldEnabled, oldMax := progressResetEnabled, progressMaxTotal
	progressResetEnabled, progressMaxTotal = true, maxTotal
	t.Cleanup(func() { progressResetEnabled, progressMaxTotal = oldEnabled, oldMax })
}

func progress(token any) mcp.JSONRPCNotification {
	n := mcp.JSONRPCNotification{JSONRPC: "2.0"}
	n.Method = string(mcp.MethodNotificationProgress)
	n.Params.AdditionalFields = map[string]any{"progressToken": token}
	return n
}

func TestWithToolTimeoutResetsOnProgress(t *testing.T) {
	enableProgressReset(t, 5*time.Second)
	ctx, req, cancel := withToolTimeout(context.Background(), 150*time.Millisecond, mcp.CallToolRequest{})
	defer cancel()
	if req.Params.Meta == nil || req.Params.Meta.ProgressToken == nil {
		t.Fatal("request carries no progress token")
	}
	token := req.Params.Meta.ProgressToken
	// Keep reporting progress for well past the idle timeout: the call stays alive.
	for i := 0; i < 6; i++ {
		time.Sleep(80 * time.Millisecond)
		routeProgressNotification(progress(token))
	}
	if ctx.Err() != nil {
		t.Fatalf("call ended despite progress: %v", ctx.Err())
	}
	// Then silence: it times out, reported as a deadline like a plain timeout.
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("idle timeout did not fire")
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("err = %v, want DeadlineExceeded", ctx.Err())
	}
}

func TestWithToolTimeoutMaxTotal(t *testing.T) {
	enableProgressReset(t, 300*time.Millisecond)
	ctx, req, cancel := withToolTimeout(context.Background(), 200*time.Millisecond, mcp.CallToolRequest{})
	defer cancel()
	token := req.Params.Meta.ProgressToken
	start := time.Now()
	for ctx.Err() == nil && time.Since(start) < 2*time.Second {
		routeProgressNotification(progress(token))
		time.Sleep(50 * time.Millisecond)
	}
	if elapsed := time.Since(start); elapsed > time.Second || ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("max total not enforced: elapsed %v, err %v", elapsed, ctx.Err())
	}
}

func TestWithToolTimeoutDisabledIsPlainTimeout(t *testing.T) {
	progressSettingsOnce.Do(func() {})
	old := progressResetEnabled
	progressResetEnabled = false
	t.Cleanup(func() { progressResetEnabled = old })
	ctx, req, cancel := withToolTimeout(context.Background(), 50*time.Millisecond, mcp.CallToolRequest{})
	defer cancel()
	if req.Params.Meta != nil {
		t.Fatal("no progress token expected when the feature is off")
	}
	<-ctx.Done()
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("err = %v", ctx.Err())
	}
}
