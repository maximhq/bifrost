package schemas

import (
	"context"
	"fmt"
	"testing"
)

// Benchmarks AppendToContextList, which backs BifrostContextKeyRoutingEnginesUsed,
// BifrostContextKeyMCPAddedTools and (via AppendRoutingEngineLog) the routing engine
// log. Each append does a slices.Contains scan plus a full slice realloc, so building
// a list of n entries is O(n^2).
func benchAppendDistinct(b *testing.B, n int) {
	vals := make([]string, n)
	for i := range vals {
		vals[i] = fmt.Sprintf("mcp-client-tool-%d", i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := NewBifrostContext(context.Background(), NoDeadline)
		for _, v := range vals {
			AppendToContextList(ctx, BifrostContextKeyMCPAddedTools, v)
		}
	}
}

func BenchmarkAppendToContextList_10(b *testing.B)  { benchAppendDistinct(b, 10) }
func BenchmarkAppendToContextList_50(b *testing.B)  { benchAppendDistinct(b, 50) }
func BenchmarkAppendToContextList_200(b *testing.B) { benchAppendDistinct(b, 200) }
func BenchmarkAppendToContextList_500(b *testing.B) { benchAppendDistinct(b, 500) }

// Routing engine logs are the highest-volume caller: 53 call sites, each building a
// message with fmt.Sprintf before appending.
func BenchmarkAppendRoutingEngineLog_50(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ctx := NewBifrostContext(context.Background(), NoDeadline)
		for j := 0; j < 50; j++ {
			ctx.AppendRoutingEngineLog(RoutingEngineGovernance, LogLevelInfo,
				fmt.Sprintf("Load balancing model claude-sonnet-%d across 4 providers", j))
		}
	}
}

// Exercises the guarded write path (blockRestrictedWrites set), which is where the
// reserved-key check actually runs on every write.
func BenchmarkSetValue_RestrictedWritesBlocked(b *testing.B) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	ctx.BlockRestrictedWrites()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx.SetValue(BifrostContextKeyUserID, "u-1")
	}
}

// Reserved keys are silently dropped; confirms the map lookup preserves that.
func TestReservedKeyWritesStillBlocked(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	ctx.SetValue(BifrostContextKeyRequestID, "before-block")
	ctx.BlockRestrictedWrites()
	ctx.SetValue(BifrostContextKeyRequestID, "after-block")
	if got, _ := ctx.Value(BifrostContextKeyRequestID).(string); got != "before-block" {
		t.Fatalf("reserved key write not blocked: got %q, want %q", got, "before-block")
	}
	// Non-reserved keys must still be writable while blocked.
	ctx.SetValue(BifrostContextKeyUserID, "u-1")
	if got, _ := ctx.Value(BifrostContextKeyUserID).(string); got != "u-1" {
		t.Fatalf("non-reserved key write dropped: got %q", got)
	}
}

// isReservedKey must report membership of reservedKeys, and nothing else.
func TestIsReservedKey(t *testing.T) {
	for k := range reservedKeys {
		if !isReservedKey(k) {
			t.Fatalf("key %v in reservedKeys but isReservedKey returned false", k)
		}
	}
	if isReservedKey(BifrostContextKeyUserID) {
		t.Fatal("non-reserved key reported as reserved")
	}
}

// Routing engine logs must stay in append order and keep duplicates (they carry
// distinct timestamps, so they are not set-semantics data).
func TestRoutingEngineLogOrderAndDuplicates(t *testing.T) {
	ctx := NewBifrostContext(context.Background(), NoDeadline)
	ctx.AppendRoutingEngineLog("governance", LogLevelInfo, "first")
	ctx.AppendRoutingEngineLog("governance", LogLevelInfo, "second")
	ctx.AppendRoutingEngineLog("governance", LogLevelInfo, "first")
	logs := ctx.GetRoutingEngineLogs()
	if len(logs) != 3 {
		t.Fatalf("got %d entries, want 3 (duplicates preserved)", len(logs))
	}
	if logs[0].Message != "first" || logs[1].Message != "second" || logs[2].Message != "first" {
		t.Fatalf("append order not preserved: %q, %q, %q",
			logs[0].Message, logs[1].Message, logs[2].Message)
	}
}

// SetValue takes `key any`, so callers can pass a non-comparable key. Indexing
// reservedKeys with one of those would panic; isReservedKey must reject them.
// Regression test for the map-backed lookup introduced alongside these benchmarks.
func TestNonComparableKeyDoesNotPanic(t *testing.T) {
	nonComparable := []any{
		[]string{"slice-key"},
		map[string]string{"map": "key"},
		func() {},
	}
	for _, key := range nonComparable {
		if isReservedKey(key) {
			t.Fatalf("non-comparable key %T reported as reserved", key)
		}
	}

	// Note: SetValue still panics on a non-comparable key when it reaches
	// `bc.userValues[key] = value`, but that predates this change and happens
	// whether or not restricted writes are blocked. The guarantee asserted here
	// is narrower: the reserved-key check itself must not be what panics.
}
