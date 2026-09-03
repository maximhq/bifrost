package server

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// The sweeper skips the plugin pipeline, so nothing upstream resolves a key for it.
// Pinning to the key that created the job is what stops core from polling with an
// arbitrary one — which for a video is not a preference but a correctness matter:
// OpenAI scopes a video id to its creating key.
func TestInternalJobContextPinsTheCreatingKey(t *testing.T) {
	ctx := internalJobContext(context.Background(), "key-abc")

	keyID, ok := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string)
	if !ok || keyID != "key-abc" {
		t.Fatalf("api key id = %q (ok=%v), want key-abc", keyID, ok)
	}
}

// BifrostContextKeyAPIKeyID is a reserved key: writes to it are dropped while the
// plugin pipeline holds restricted writes. This context never enters that phase, so
// the write must land — if that ever changes, the pin silently stops working and
// the sweeper goes back to polling with whatever key core picks.
func TestInternalJobContextReservedWriteIsNotDropped(t *testing.T) {
	ctx := internalJobContext(context.Background(), "key-reserved")
	if _, ok := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); !ok {
		t.Fatal("a reserved-key write on a fresh sweeper context must not be dropped")
	}
	// Same reasoning as the skip flags this context has always set.
	if skip, ok := ctx.Value(schemas.BifrostContextKeySkipBudgetAndRateLimits).(bool); !ok || !skip {
		t.Fatal("expected the sweeper context to keep skipping budgets")
	}
}

// A job row written before the key was recorded, or by a path that never had one,
// must still poll rather than pin to the empty string and match nothing.
func TestInternalJobContextWithoutKeyLeavesSelectionOpen(t *testing.T) {
	for _, selectedKeyID := range []string{"", "   "} {
		ctx := internalJobContext(context.Background(), selectedKeyID)
		if _, ok := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); ok {
			t.Fatalf("selectedKeyID %q must leave key selection unpinned", selectedKeyID)
		}
	}
}

// The sweeper's own invariants must survive the pin.
func TestInternalJobContextKeepsSweeperInvariants(t *testing.T) {
	ctx := internalJobContext(context.Background(), "key-abc")

	if skip, ok := ctx.Value(schemas.BifrostContextKeySkipPluginPipeline).(bool); !ok || !skip {
		t.Fatal("the sweeper must not re-enter the plugin pipeline")
	}
	if requestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string); !ok || requestID == "" {
		t.Fatal("every sweeper call needs its own request id")
	}
}

// Both job kinds poll through the same pinned context. An earlier revision gave
// batch an unpinned retry on failure, on the theory that a batch id is org-scoped
// so any key could rescue it. That fires on every transient error — an outage, a
// rate limit — doubling load on an upstream already failing, to cover only a key
// deleted mid-flight; and that case is already answered by the job parking as
// unpriceable and by /v1/batches/{id}/results settling inline with the caller's own
// key. One code path, no retry.
func TestInternalJobContextIsTheOnlySweeperContext(t *testing.T) {
	batchCtx := internalJobContext(context.Background(), "key-batch")
	videoCtx := internalJobContext(context.Background(), "key-video")

	for name, ctx := range map[string]*schemas.BifrostContext{"batch": batchCtx, "video": videoCtx} {
		if _, ok := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); !ok {
			t.Fatalf("%s: both kinds must poll pinned to their creating key", name)
		}
	}
}
