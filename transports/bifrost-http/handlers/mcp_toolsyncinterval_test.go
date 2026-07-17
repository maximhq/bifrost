package handlers

import (
	"testing"
	"time"
)

// outOfToolSyncIntervalRange mirrors the bounds check applied to
// req.ToolSyncInterval in updateMCPClient.
func outOfToolSyncIntervalRange(minutes int64) bool {
	return minutes > maxToolSyncIntervalMinutes || minutes < minToolSyncIntervalMinutes
}

// TestToolSyncIntervalBoundsRejectNanosecondValues covers issue #5026: GET returns
// tool_sync_interval in nanoseconds while PUT reads minutes, so a client echoing the
// GET value back overflows the minutes->Duration multiply and persists garbage. The
// bounds must reject every such value, including the ones that overflow to a positive
// interval — those pass a naive "must be >= 0" check while silently parking tool sync
// centuries into the future.
func TestToolSyncIntervalBoundsRejectNanosecondValues(t *testing.T) {
	for _, override := range []time.Duration{time.Minute, 10 * time.Minute, time.Hour, -time.Minute} {
		nanos := int64(override) // what a GET response carries for this override
		if !outOfToolSyncIntervalRange(nanos) {
			t.Errorf("tool_sync_interval=%d (nanoseconds for a %v override) should be rejected, but was accepted; it would persist %v",
				nanos, override, time.Duration(nanos)*time.Minute)
		}
	}
}

// TestToolSyncIntervalBoundsAcceptRealisticValues guards against the bounds being
// tightened into legitimate configuration. -1 disables sync and 0 selects the global
// default, so both must survive.
func TestToolSyncIntervalBoundsAcceptRealisticValues(t *testing.T) {
	for _, minutes := range []int64{-1, 0, 1, 10, 60, 1440, 525600} {
		if outOfToolSyncIntervalRange(minutes) {
			t.Errorf("tool_sync_interval=%d minutes is a realistic value but was rejected", minutes)
		}
	}
}

// TestToolSyncIntervalBoundsSurvivePersistence asserts that an accepted value also
// survives the second conversion, minutes -> int seconds, which is how the interval is
// stored. The bounds are derived from the minutes -> Duration multiply, so this pins
// the assumption that clearing that hurdle is sufficient — it would start failing if
// the persisted column ever narrowed to a fixed 32-bit type.
func TestToolSyncIntervalBoundsSurvivePersistence(t *testing.T) {
	for _, minutes := range []int64{minToolSyncIntervalMinutes, -1, 0, 1, 10, maxToolSyncIntervalMinutes} {
		seconds := int64(time.Duration(minutes)*time.Minute) / int64(time.Second)
		if int64(int(seconds)) != seconds {
			t.Errorf("accepted tool_sync_interval=%d minutes wraps on persistence: %d seconds does not round-trip through int (got %d)",
				minutes, seconds, int(seconds))
		}
	}
}

// TestToolSyncIntervalBoundsPreventOverflow asserts the bounds are actually the
// overflow edge: any accepted value must survive the multiply with its sign intact.
func TestToolSyncIntervalBoundsPreventOverflow(t *testing.T) {
	for _, minutes := range []int64{minToolSyncIntervalMinutes, -1, 0, 1, maxToolSyncIntervalMinutes} {
		got := time.Duration(minutes) * time.Minute
		if minutes > 0 && got < 0 {
			t.Errorf("accepted tool_sync_interval=%d minutes overflowed to %v", minutes, got)
		}
		if minutes < 0 && got > 0 {
			t.Errorf("accepted tool_sync_interval=%d minutes underflowed to %v", minutes, got)
		}
	}
	// One minute past the ceiling must overflow — proving the bound is not arbitrary.
	// Kept in a var so the multiply happens at runtime; as a constant expression it
	// is a compile error, which is its own proof that the bound is the exact edge.
	past := maxToolSyncIntervalMinutes + 1
	if got := time.Duration(past) * time.Minute; got > 0 {
		t.Errorf("expected overflow one minute past the ceiling, got %v", got)
	}
}
