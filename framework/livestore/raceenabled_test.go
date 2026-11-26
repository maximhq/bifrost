//go:build race

package livestore

// raceEnabled reports whether the race detector is on: its instrumentation adds allocations,
// so allocation counts are only checked without it.
const raceEnabled = true
