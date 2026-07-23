//go:build !race

package governance

// raceEnabled reports whether the race detector is on, so timing-sensitive
// tests can scale their wall-clock budgets (the detector slows execution
// 2-20x per https://go.dev/doc/articles/race_detector).
const raceEnabled = false
