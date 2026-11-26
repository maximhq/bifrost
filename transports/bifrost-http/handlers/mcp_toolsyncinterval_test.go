package handlers

import (
	"testing"
	"time"
)

// outOfToolSyncIntervalRange asks the real request field whether a bare
// number of minutes would be accepted, rather than restating the rule. It
// decodes the value the way a handler does and then validates it, so these
// cases keep testing the shipped path as that path changes.
func outOfToolSyncIntervalRange(minutes int64) bool {
	var field MCPSyncInterval
	if err := field.UnmarshalJSON([]byte(jsonInt(minutes))); err != nil {
		return true
	}
	return field.Validate() != nil
}

// TestToolSyncIntervalBoundsRejectNanosecondValues covers issue #5026: GET returns
// tool_sync_interval in nanoseconds while PUT reads minutes, so a client echoing the
// GET value back overflows the minutes->Duration multiply and persists garbage. The
// bounds must reject every such value, including the ones that overflow to a positive
// interval — those pass a naive "must be >= 0" check while silently parking tool sync
// centuries into the future.
func TestToolSyncIntervalBoundsRejectNanosecondValues(t *testing.T) {
	for _, override := range []time.Duration{time.Minute, 10 * time.Minute, time.Hour} {
		nanos := int64(override) // what a GET response carries for this override
		if !outOfToolSyncIntervalRange(nanos) {
			t.Errorf("tool_sync_interval=%d (nanoseconds for a %v override) should be rejected, but was accepted; it would persist %v",
				nanos, override, time.Duration(nanos)*time.Minute)
		}
	}
}

// TestToolSyncIntervalBoundsRejectNegatives pins that negative values are an
// error on the API: there is no "disabled" state (the checker also drives
// liveness), and 0 already means "use the global setting".
func TestToolSyncIntervalBoundsRejectNegatives(t *testing.T) {
	for _, minutes := range []int64{-1, -10, int64(-time.Minute)} {
		if !outOfToolSyncIntervalRange(minutes) {
			t.Errorf("tool_sync_interval=%d minutes is negative and must be rejected", minutes)
		}
	}
}

// TestToolSyncIntervalBoundsAcceptRealisticValues guards against the bounds being
// tightened into legitimate configuration. 0 selects the global setting and must
// survive alongside ordinary positive minute counts.
func TestToolSyncIntervalBoundsAcceptRealisticValues(t *testing.T) {
	for _, minutes := range []int64{0, 1, 10, 60, 1440, 525600} {
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
	for _, minutes := range []int64{0, 1, 10, maxToolSyncIntervalMinutes} {
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
	for _, minutes := range []int64{0, 1, maxToolSyncIntervalMinutes} {
		if got := time.Duration(minutes) * time.Minute; got < 0 {
			t.Errorf("accepted tool_sync_interval=%d minutes overflowed to %v", minutes, got)
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
