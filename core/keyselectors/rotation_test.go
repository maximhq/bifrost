package keyselectors

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

const testProvider = schemas.ModelProvider("antigravity")

func testKeys(weights ...float64) []schemas.Key {
	keys := make([]schemas.Key, len(weights))
	for i, w := range weights {
		id := string(rune('a' + i))
		keys[i] = schemas.Key{ID: id, Name: id, Weight: w}
	}
	return keys
}

func newTestRotator() (*Rotator, *time.Time) {
	r := NewRotator()
	now := time.Unix(1_700_000_000, 0)
	r.now = func() time.Time { return now }
	return r, &now
}

func pick(t *testing.T, r *Rotator, cfg *schemas.KeySelectionConfig, keys []schemas.Key, model string) string {
	t.Helper()
	key, err := r.Select(nil, testProvider, model, cfg, keys, nil)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	return key.ID
}

func sequence(t *testing.T, r *Rotator, cfg *schemas.KeySelectionConfig, keys []schemas.Key, n int) string {
	t.Helper()
	out := ""
	for range n {
		out += pick(t, r, cfg, keys, "m")
	}
	return out
}

func TestRoundRobinCyclesInOrder(t *testing.T) {
	r, _ := newTestRotator()
	cfg := &schemas.KeySelectionConfig{Strategy: schemas.KeySelectionRoundRobin}
	if got := sequence(t, r, cfg, testKeys(1, 1, 1), 6); got != "abcabc" {
		t.Fatalf("got %q, want abcabc", got)
	}
}

func TestRoundRobinHonoursWeightsSmoothly(t *testing.T) {
	r, _ := newTestRotator()
	cfg := &schemas.KeySelectionConfig{Strategy: schemas.KeySelectionRoundRobin}
	// Smooth WRR with 2:1 interleaves instead of bursting: a, b, a per cycle.
	if got := sequence(t, r, cfg, testKeys(2, 1), 6); got != "abaaba" {
		t.Fatalf("got %q, want abaaba", got)
	}
	// Zero-weight keys are skipped while others carry weight.
	r2, _ := newTestRotator()
	if got := sequence(t, r2, cfg, testKeys(1, 0, 1), 4); got != "acac" {
		t.Fatalf("got %q, want acac", got)
	}
}

func TestRoundRobinStickyLimit(t *testing.T) {
	r, _ := newTestRotator()
	cfg := &schemas.KeySelectionConfig{Strategy: schemas.KeySelectionRoundRobin, StickyLimit: 3}
	if got := sequence(t, r, cfg, testKeys(1, 1), 7); got != "aaabbba" {
		t.Fatalf("got %q, want aaabbba", got)
	}
}

func TestRoundRobinAdvancesPastFailedStickyKey(t *testing.T) {
	r, _ := newTestRotator()
	cfg := &schemas.KeySelectionConfig{Strategy: schemas.KeySelectionRoundRobin, StickyLimit: 5}
	keys := testKeys(1, 1)
	if got := pick(t, r, cfg, keys, "m"); got != "a" {
		t.Fatalf("first pick %q, want a", got)
	}
	r.ReportFailure(testProvider, "a", "m", schemas.FailureClassRateLimit, 0, cfg)
	if got := pick(t, r, cfg, keys, "m"); got != "b" {
		t.Fatalf("after a rate-limited, got %q, want b", got)
	}
}

func TestFillFirstDrainsFirstKeyThenFailsOver(t *testing.T) {
	r, now := newTestRotator()
	cfg := &schemas.KeySelectionConfig{Strategy: schemas.KeySelectionFillFirst}
	keys := testKeys(1, 1, 1)
	if got := sequence(t, r, cfg, keys, 3); got != "aaa" {
		t.Fatalf("got %q, want aaa", got)
	}
	r.ReportFailure(testProvider, "a", "m", schemas.FailureClassQuota, 0, cfg)
	if got := sequence(t, r, cfg, keys, 2); got != "bb" {
		t.Fatalf("while a cools down got %q, want bb", got)
	}
	// The cooldown is per model: another model still fills the first key.
	if got := pick(t, r, cfg, keys, "other"); got != "a" {
		t.Fatalf("other model got %q, want a", got)
	}
	*now = now.Add(schemas.DefaultKeyCooldownSeconds*time.Second + time.Second)
	if got := pick(t, r, cfg, keys, "m"); got != "a" {
		t.Fatalf("after cooldown got %q, want a", got)
	}
}

func TestCooldownHonoursRetryAfterAndConfig(t *testing.T) {
	r, now := newTestRotator()
	ten := 10
	cfg := &schemas.KeySelectionConfig{Strategy: schemas.KeySelectionFillFirst, CooldownSeconds: &ten}
	keys := testKeys(1, 1)

	r.ReportFailure(testProvider, "a", "m", schemas.FailureClassRateLimit, 0, cfg)
	*now = now.Add(9 * time.Second)
	if got := pick(t, r, cfg, keys, "m"); got != "b" {
		t.Fatalf("within configured cooldown got %q, want b", got)
	}
	*now = now.Add(2 * time.Second)
	if got := pick(t, r, cfg, keys, "m"); got != "a" {
		t.Fatalf("after configured cooldown got %q, want a", got)
	}

	r.ReportFailure(testProvider, "a", "m", schemas.FailureClassRateLimit, 30*time.Second, cfg)
	*now = now.Add(20 * time.Second)
	if got := pick(t, r, cfg, keys, "m"); got != "b" {
		t.Fatalf("Retry-After must override the configured cooldown, got %q", got)
	}
}

func TestCooldownDisabledAndIgnoredClasses(t *testing.T) {
	r, _ := newTestRotator()
	zero := 0
	disabled := &schemas.KeySelectionConfig{Strategy: schemas.KeySelectionFillFirst, CooldownSeconds: &zero}
	keys := testKeys(1, 1)
	r.ReportFailure(testProvider, "a", "m", schemas.FailureClassRateLimit, time.Minute, disabled)
	if got := pick(t, r, disabled, keys, "m"); got != "a" {
		t.Fatalf("cooldown_seconds=0 must disable cooldowns, got %q", got)
	}
	cfg := &schemas.KeySelectionConfig{Strategy: schemas.KeySelectionFillFirst}
	for _, class := range []schemas.FailureClass{schemas.FailureClassTransient, schemas.FailureClassCallerFault, schemas.FailureClassModelAccess, schemas.FailureClassUnknown} {
		r.ReportFailure(testProvider, "a", "m", class, 0, cfg)
		if got := pick(t, r, cfg, keys, "m"); got != "a" {
			t.Fatalf("%s must not cool the key down, got %q", class, got)
		}
	}
}

func TestCredentialFailureCoolsWholeKeyUntilSuccess(t *testing.T) {
	r, _ := newTestRotator()
	cfg := &schemas.KeySelectionConfig{Strategy: schemas.KeySelectionFillFirst}
	keys := testKeys(1, 1)
	r.ReportFailure(testProvider, "a", "m", schemas.FailureClassCredential, 0, cfg)
	if got := pick(t, r, cfg, keys, "another-model"); got != "b" {
		t.Fatalf("credential cooldown must cover every model, got %q", got)
	}
	r.ReportSuccess(testProvider, "a", "m")
	if got := pick(t, r, cfg, keys, "another-model"); got != "a" {
		t.Fatalf("success must clear the cooldown, got %q", got)
	}
}

func TestAllKeysCoolingUsesWholePool(t *testing.T) {
	r, _ := newTestRotator()
	cfg := &schemas.KeySelectionConfig{Strategy: schemas.KeySelectionFillFirst}
	keys := testKeys(1, 1)
	r.ReportFailure(testProvider, "a", "m", schemas.FailureClassRateLimit, 0, cfg)
	r.ReportFailure(testProvider, "b", "m", schemas.FailureClassRateLimit, 0, cfg)
	if got := pick(t, r, cfg, keys, "m"); got != "a" {
		t.Fatalf("with every key cooling the strategy must run over the full pool, got %q", got)
	}
}

func TestLeastUsedPrefersFewestInFlightThenFewestServed(t *testing.T) {
	r, _ := newTestRotator()
	cfg := &schemas.KeySelectionConfig{Strategy: schemas.KeySelectionLeastUsed}
	keys := testKeys(1, 1, 1)
	// Served counts spread evenly when nothing stays in flight.
	if got := sequence(t, r, cfg, keys, 6); got != "abcabc" {
		t.Fatalf("got %q, want abcabc", got)
	}
	releaseA := r.Begin(testProvider, "a")
	releaseB := r.Begin(testProvider, "b")
	if got := pick(t, r, cfg, keys, "m"); got != "c" {
		t.Fatalf("got %q, want c (only key idle)", got)
	}
	releaseC := r.Begin(testProvider, "c")
	releaseB()
	releaseB() // idempotent: must not drive the counter below the real value
	if got := pick(t, r, cfg, keys, "m"); got != "b" {
		t.Fatalf("got %q, want b (released)", got)
	}
	releaseA()
	releaseC()
}

func TestWeightedRandomStrategyDelegatesToFallbackWithoutCoolingKeys(t *testing.T) {
	r, _ := newTestRotator()
	cfg := &schemas.KeySelectionConfig{Strategy: schemas.KeySelectionWeightedRandom}
	keys := testKeys(1, 1, 1)
	r.ReportFailure(testProvider, "b", "m", schemas.FailureClassRateLimit, 0, cfg)
	var seen []string
	fallback := func(_ *schemas.BifrostContext, pool []schemas.Key, _ schemas.ModelProvider, _ string) (schemas.Key, error) {
		for _, k := range pool {
			seen = append(seen, k.ID)
		}
		return pool[len(pool)-1], nil
	}
	key, err := r.Select(nil, testProvider, "m", cfg, keys, fallback)
	if err != nil || key.ID != "c" {
		t.Fatalf("got %v %v, want c from the fallback", key.ID, err)
	}
	if len(seen) != 2 || seen[0] != "a" || seen[1] != "c" {
		t.Fatalf("fallback saw %v, want [a c]", seen)
	}
}

func TestStateIsPerProvider(t *testing.T) {
	r, _ := newTestRotator()
	cfg := &schemas.KeySelectionConfig{Strategy: schemas.KeySelectionRoundRobin}
	keys := testKeys(1, 1)
	if got := pick(t, r, cfg, keys, "m"); got != "a" {
		t.Fatalf("got %q", got)
	}
	other, err := r.Select(nil, "kiro", "m", cfg, keys, nil)
	if err != nil || other.ID != "a" {
		t.Fatalf("another provider must start its own cycle, got %q", other.ID)
	}
}
