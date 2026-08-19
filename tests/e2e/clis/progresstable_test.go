package clis

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func newTestProgress() *progressTracker {
	return &progressTracker{rows: map[string]*progressRow{}, start: time.Now()}
}

// The table is the only signal a CI run emits for ~45 minutes, so a
// miscounted column is invisible until someone trusts a wrong number.
func TestProgressTableCounts(t *testing.T) {
	p := newTestProgress()
	for range 5 {
		p.register("claude")
	}
	for range 3 {
		p.register("codex")
	}
	p.record("claude", "pass")
	p.record("claude", "pass")
	p.record("claude", "fail")
	p.record("claude", "soft_pass")
	p.record("codex", "interrupted")
	p.record("codex", "skip")

	names, rows, total := p.snapshot()
	if got, want := strings.Join(names, ","), "claude,codex"; got != want {
		t.Fatalf("harness rows = %q, want %q (sorted)", got, want)
	}

	claude := rows[0]
	if claude.total != 5 || claude.done != 4 || claude.pass != 2 || claude.fail != 1 || claude.other != 1 {
		t.Errorf("claude row = %+v, want {total:5 done:4 pass:2 fail:1 other:1}", claude)
	}
	// Both non-pass, non-fail statuses land in OTHER: an interrupted cell is
	// not a regression, and neither is a skip.
	codex := rows[1]
	if codex.total != 3 || codex.done != 2 || codex.pass != 0 || codex.fail != 0 || codex.other != 2 {
		t.Errorf("codex row = %+v, want {total:3 done:2 pass:0 fail:0 other:2}", codex)
	}
	if total.total != 8 || total.done != 6 || total.pass != 2 || total.fail != 1 || total.other != 3 {
		t.Errorf("total row = %+v, want {total:8 done:6 pass:2 fail:1 other:3}", total)
	}
}

// Every rendered line must have the same width, or the table shears in the
// Actions log the moment a count gains a digit.
func TestProgressTableAlignment(t *testing.T) {
	p := newTestProgress()
	p.register("claude")
	for range 1234 {
		p.register("a-very-long-harness-name")
	}
	p.record("claude", "pass")

	table := p.table()
	lines := strings.Split(strings.TrimRight(table, "\n"), "\n")
	// [0] blank, [1] the "Bifrost CLI Harness  Elapsed ..." header, then the
	// boxed table itself.
	if len(lines) < 8 {
		t.Fatalf("expected at least 8 lines, got %d:\n%s", len(lines), table)
	}
	// Rune counts, not bytes: every box-drawing character is 3 bytes in UTF-8,
	// so a byte-width comparison would pass on a genuinely ragged table.
	width := utf8.RuneCountInString(lines[2])
	for i, line := range lines[2:] {
		if got := utf8.RuneCountInString(line); got != width {
			t.Errorf("line %d width = %d runes, want %d (misaligned):\n%q", i+2, got, width, line)
		}
	}
	if !strings.Contains(table, "TOTAL") {
		t.Error("table is missing its TOTAL row")
	}
}

// The Other column is noise on a clean run and essential on a dirty one:
// without it Completed silently exceeds Pass + Fail with nothing to explain
// the gap.
func TestProgressTableOtherColumnAppearsOnlyWhenUsed(t *testing.T) {
	clean := newTestProgress()
	clean.register("claude")
	clean.record("claude", "pass")
	if strings.Contains(clean.table(), "Other") {
		t.Errorf("clean run should not render an Other column:\n%s", clean.table())
	}

	soft := newTestProgress()
	soft.register("claude")
	soft.record("claude", "soft_pass")
	if !strings.Contains(soft.table(), "Other") {
		t.Errorf("a soft_pass must be visible in an Other column:\n%s", soft.table())
	}
}

func TestProgressTableEmpty(t *testing.T) {
	// Nothing registered yet (e.g. the filter matched no cells): print nothing
	// rather than a header with no rows, which reads as a rendering bug.
	if got := newTestProgress().table(); got != "" {
		t.Errorf("empty tracker table = %q, want empty", got)
	}
}

func TestProgressInterval(t *testing.T) {
	for _, tc := range []struct {
		name     string
		env      string
		want     time.Duration
		wantTick bool
	}{
		{name: "default", env: "", want: defaultProgressInterval, wantTick: true},
		{name: "explicit", env: "30s", want: 30 * time.Second, wantTick: true},
		{name: "disabled zero", env: "0", wantTick: false},
		{name: "disabled off", env: "off", wantTick: false},
		// A typo must not silently disable progress: a run with no ticker is
		// indistinguishable from a hung one.
		{name: "invalid falls back", env: "10 secondz", want: defaultProgressInterval, wantTick: true},
		{name: "negative falls back", env: "-5s", want: defaultProgressInterval, wantTick: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(progressIntervalEnv, tc.env)
			got, tick := progressInterval()
			if tick != tc.wantTick {
				t.Fatalf("progressInterval() tick = %v, want %v", tick, tc.wantTick)
			}
			if tick && got != tc.want {
				t.Errorf("progressInterval() = %s, want %s", got, tc.want)
			}
		})
	}
}

// The table and the live mirror are alternative views of the same run;
// interleaving them buries the table in a firehose of JSON. The mirror is
// opt-in, so the table is what a default run shows.
func TestProgressEnabledTracksMirror(t *testing.T) {
	t.Setenv(progressEnv, "")
	t.Setenv("BIFROST_E2E_CLIS_QUIET", "")
	t.Setenv("BIFROST_E2E_CLIS_MIRROR", "")

	if !progressEnabled() {
		t.Error("progress should be on by default, since the mirror is off by default")
	}
	t.Setenv("BIFROST_E2E_CLIS_MIRROR", "1")
	if progressEnabled() {
		t.Error("progress should turn off when the live mirror is explicitly enabled")
	}
	// QUIET wins over MIRROR, so the table comes back.
	t.Setenv("BIFROST_E2E_CLIS_QUIET", "1")
	if !progressEnabled() {
		t.Error("QUIET should override MIRROR, leaving the table on")
	}

	// An explicit setting wins over the mirror in both directions.
	t.Setenv("BIFROST_E2E_CLIS_QUIET", "")
	t.Setenv("BIFROST_E2E_CLIS_MIRROR", "1")
	t.Setenv(progressEnv, "1")
	if !progressEnabled() {
		t.Errorf("%s=1 should force progress on even with the mirror up", progressEnv)
	}
	t.Setenv("BIFROST_E2E_CLIS_MIRROR", "")
	t.Setenv(progressEnv, "0")
	if progressEnabled() {
		t.Errorf("%s=0 should force progress off", progressEnv)
	}
}

// The mirror is the thing that floods a terminal with raw CLI JSON. It must
// stay off unless explicitly asked for.
func TestMirrorIsOptIn(t *testing.T) {
	t.Setenv("BIFROST_E2E_CLIS_QUIET", "")
	t.Setenv("BIFROST_E2E_CLIS_MIRROR", "")
	if mirrorWriter() != nil {
		t.Error("mirror must be off by default")
	}

	t.Setenv("BIFROST_E2E_CLIS_MIRROR", "1")
	if mirrorWriter() == nil {
		t.Error("BIFROST_E2E_CLIS_MIRROR=1 should enable the mirror")
	}

	// QUIET is the pre-existing off switch and still wins, so CI and any
	// existing invocation passing it keep behaving exactly as before.
	t.Setenv("BIFROST_E2E_CLIS_QUIET", "1")
	if mirrorWriter() != nil {
		t.Error("BIFROST_E2E_CLIS_QUIET must override MIRROR")
	}
}
