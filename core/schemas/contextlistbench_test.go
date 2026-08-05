package schemas

import (
	"context"
	"fmt"
	"testing"
)

// Benchmarks AppendToContextList, which backs BifrostContextKeyRoutingEnginesUsed
// and BifrostContextKeyMCPAddedTools. Its slices.Contains scan grows with the
// existing list on every append, so building a distinct list is O(n^2).
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

// Reserved keys are silently preserved while non-reserved operations still run.
func TestReservedKeyWritesStillBlocked(t *testing.T) {
	tests := []struct {
		name                string
		apply               func(*BifrostContext, BifrostContextKey) any
		wantNonReserved     any
		wantReservedReturn  any
		checkReservedReturn bool
	}{
		{
			name: "SetValue",
			apply: func(ctx *BifrostContext, key BifrostContextKey) any {
				ctx.SetValue(key, "after-block")
				return nil
			},
			wantNonReserved: "after-block",
		},
		{
			name: "ClearValue",
			apply: func(ctx *BifrostContext, key BifrostContextKey) any {
				ctx.ClearValue(key)
				return nil
			},
			wantNonReserved: nil,
		},
		{
			name: "GetAndSetValue",
			apply: func(ctx *BifrostContext, key BifrostContextKey) any {
				return ctx.GetAndSetValue(key, "after-block")
			},
			wantNonReserved:     "after-block",
			wantReservedReturn:  "before-block",
			checkReservedReturn: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := NewBifrostContext(context.Background(), NoDeadline)
			ctx.SetValue(BifrostContextKeyRequestID, "before-block")
			ctx.SetValue(BifrostContextKeyUserID, "before-block")
			ctx.BlockRestrictedWrites()

			reservedReturn := tt.apply(ctx, BifrostContextKeyRequestID)
			if tt.checkReservedReturn && reservedReturn != tt.wantReservedReturn {
				t.Fatalf("reserved operation returned %v, want %v", reservedReturn, tt.wantReservedReturn)
			}
			if got := ctx.Value(BifrostContextKeyRequestID); got != "before-block" {
				t.Fatalf("reserved value changed: got %v, want before-block", got)
			}

			tt.apply(ctx, BifrostContextKeyUserID)
			if got := ctx.Value(BifrostContextKeyUserID); got != tt.wantNonReserved {
				t.Fatalf("non-reserved operation got %v, want %v", got, tt.wantNonReserved)
			}
		})
	}
}

// isReservedKey must report the canonical reserved-key contract, not merely
// agree with the implementation map under test.
func TestIsReservedKey(t *testing.T) {
	canonicalReservedKeys := []BifrostContextKey{
		BifrostContextKeyVirtualKey,
		BifrostContextKeyAPIKeyName,
		BifrostContextKeyAPIKeyID,
		BifrostContextKeyDirectKey,
		BifrostContextKeyRequestID,
		BifrostContextKeyFallbackRequestID,
		BifrostContextKeySelectedKeyID,
		BifrostContextKeySelectedKeyName,
		BifrostContextKeyNumberOfRetries,
		BifrostContextKeyFallbackIndex,
		BifrostContextKeySkipKeySelection,
		BifrostContextKeyPassthroughHeaders,
		BifrostContextKeySkipBudgetAndRateLimits,
		BifrostContextKeyURLPath,
		BifrostContextKeyDeferTraceCompletion,
		BifrostContextKeyAttemptTrail,
		BifrostContextKeyStreamGated,
		BifrostContextKeyMCPHealthCheckRequest,
		BifrostContextKeyUpstreamLatency,
		BifrostContextKeyRoutingInfo,
	}
	if len(reservedKeys) != len(canonicalReservedKeys) {
		t.Fatalf("reserved key count = %d, want %d", len(reservedKeys), len(canonicalReservedKeys))
	}
	for _, key := range canonicalReservedKeys {
		if !isReservedKey(key) {
			t.Errorf("canonical key %v is not reserved", key)
		}
	}
	if isReservedKey(BifrostContextKeyUserID) {
		t.Fatal("non-reserved key reported as reserved")
	}
}

// Routing engine logs must stay in append order and preserve repeated messages;
// they are event data, not a set.
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
