package clis

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Progress reporting for CI runs.
//
// The harness's live mirror streams every CLI subprocess's raw JSON to the
// terminal, which is right for local debugging and useless in an append-only
// Actions log -- tens of thousands of lines with no way to see how far along
// the sweep is. QUIET=1 already suppresses the mirror, but that leaves CI with
// no signal at all for the ~45 minutes a suite takes.
//
// This replaces it with a periodic table, one row per harness (CLI) plus a
// total: how many cells the run will execute, how many have finished, and how
// they split pass/fail.
//
// Output goes to stderr, deliberately. Everything else the run emits -- go
// test's own output, per-cell result lines, rate-limit notices -- goes to
// stdout, so CI can redirect stdout to an artifact log and keep only this
// table on the console. See .github/workflows/scripts/test-cli-harness.sh.

const (
	// progressIntervalEnv overrides the tick interval (any time.ParseDuration
	// value, e.g. "30s"). "0" or "off" disables the ticker entirely.
	progressIntervalEnv = "BIFROST_E2E_CLIS_PROGRESS_INTERVAL"

	// progressEnv forces the ticker on ("1") or off ("0") regardless of quiet
	// mode. Unset means "on when the live mirror is off", since the two are
	// alternative views of the same run and interleaving them is unreadable.
	progressEnv = "BIFROST_E2E_CLIS_PROGRESS"

	defaultProgressInterval = 10 * time.Second
)

// progressRow is one harness's tally. total is fixed once the matrix has been
// enumerated; the rest climb as cells finish.
type progressRow struct {
	total int
	done  int
	pass  int
	fail  int
	other int // soft_pass, interrupted, skip -- neither a clean pass nor a regression
}

type progressTracker struct {
	mu    sync.Mutex
	rows  map[string]*progressRow
	start time.Time
}

var progress = &progressTracker{rows: map[string]*progressRow{}, start: time.Now()}

// register counts one cell that the -run filter selected and that will
// therefore execute.
//
// Called from the scenario subtest body *before* t.Parallel(). That ordering
// is what makes the total trustworthy: Go runs each subtest body up to its
// t.Parallel() call, parks it, and only resumes the parked bodies once the
// enclosing test has finished enumerating. So every selected cell registers
// during the (fast, serial) enumeration pass, before any of them actually
// starts work -- the table shows a true denominator within a second or two of
// launch rather than a total that creeps up alongside the count of finished
// cells.
//
// It also means the total already reflects `-run` filtering and every
// Supports() gate, neither of which this package could otherwise predict.
func (p *progressTracker) register(cliID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.row(cliID).total++
}

// record tallies a finished cell under one of runCell's statuses.
func (p *progressTracker) record(cliID, status string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	row := p.row(cliID)
	row.done++
	switch status {
	case "pass":
		row.pass++
	case "fail":
		row.fail++
	default:
		row.other++
	}
}

// row returns cliID's tally, creating it on first use. Callers hold p.mu.
func (p *progressTracker) row(cliID string) *progressRow {
	row, ok := p.rows[cliID]
	if !ok {
		row = &progressRow{}
		p.rows[cliID] = row
	}
	return row
}

// snapshot returns the rows sorted by harness name plus a synthetic total,
// and whether anything has been registered yet.
func (p *progressTracker) snapshot() (names []string, rows []progressRow, total progressRow) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for name := range p.rows {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		row := *p.rows[name]
		rows = append(rows, row)
		total.total += row.total
		total.done += row.done
		total.pass += row.pass
		total.fail += row.fail
		total.other += row.other
	}
	return names, rows, total
}

// table renders the current tallies as a boxed table, deliberately shaped like
// the provider harness's monitor (tests/e2e/api/runners/harness-monitor.mjs) so
// the two harnesses read the same way in a job log.
//
// The Other column carries the statuses that are neither a clean pass nor a
// regression - soft_pass, interrupted, skip - and is elided when they never
// occur, which is the common case. Without it Completed would silently exceed
// Pass + Fail with no indication of where the difference went.
//
// Column widths are computed from the content, so a long harness name or a
// five-digit count cannot shear the alignment.
func (p *progressTracker) table() string {
	names, rows, total := p.snapshot()
	if len(names) == 0 {
		return ""
	}
	showOther := total.other > 0

	header := []string{"Harness", "Total", "Completed", "Pass", "Fail"}
	if showOther {
		header = append(header, "Other")
	}
	cells := [][]string{header}
	for i, name := range names {
		cells = append(cells, progressCells(name, rows[i], showOther))
	}
	totalRow := progressCells("TOTAL", total, showOther)

	widths := make([]int, len(header))
	for _, row := range append(cells, totalRow) {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	// sep builds a horizontal rule with the given box-drawing corners.
	sep := func(left, mid, right string) string {
		var line strings.Builder
		line.WriteString(left)
		for i, w := range widths {
			line.WriteString(strings.Repeat("─", w+2))
			if i == len(widths)-1 {
				line.WriteString(right)
			} else {
				line.WriteString(mid)
			}
		}
		return line.String()
	}

	var b strings.Builder
	writeRow := func(row []string) {
		b.WriteString("│")
		for i, cell := range row {
			if i == 0 {
				fmt.Fprintf(&b, " %-*s ", widths[i], cell) // harness name, left-aligned
			} else {
				fmt.Fprintf(&b, " %*s ", widths[i], cell) // counts, right-aligned
			}
			b.WriteString("│")
		}
		b.WriteByte('\n')
	}

	fmt.Fprintf(&b, "\nBifrost CLI Harness   Elapsed %s%s\n",
		fmtProgressDuration(time.Since(p.start)), progressETA(p.start, total))
	b.WriteString(sep("┌", "┬", "┐") + "\n")
	writeRow(cells[0])
	b.WriteString(sep("├", "┼", "┤") + "\n")
	for _, row := range cells[1:] {
		writeRow(row)
	}
	b.WriteString(sep("├", "┼", "┤") + "\n")
	writeRow(totalRow)
	b.WriteString(sep("└", "┴", "┘") + "\n")
	return b.String()
}

func progressCells(name string, row progressRow, showOther bool) []string {
	cells := []string{
		name,
		strconv.Itoa(row.total),
		strconv.Itoa(row.done),
		strconv.Itoa(row.pass),
		strconv.Itoa(row.fail),
	}
	if showOther {
		cells = append(cells, strconv.Itoa(row.other))
	}
	return cells
}

// progressETA extrapolates the remaining wall-clock from the completion rate so
// far. It returns "" until a cell has finished, since a rate derived from zero
// completions is not an estimate.
func progressETA(start time.Time, total progressRow) string {
	if total.done == 0 || total.done >= total.total {
		return ""
	}
	perCell := time.Since(start) / time.Duration(total.done)
	return "   ETA " + fmtProgressDuration(perCell*time.Duration(total.total-total.done))
}

func fmtProgressDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// progressInterval resolves the tick interval, returning ok=false when the
// ticker is disabled.
func progressInterval() (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv(progressIntervalEnv))
	if raw == "" {
		return defaultProgressInterval, true
	}
	if raw == "0" || strings.EqualFold(raw, "off") {
		return 0, false
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		// A typo'd interval should not silently mean "no progress at all" --
		// that reads identically to a hung run.
		fmt.Fprintf(os.Stderr, "[harness] ignoring invalid %s=%q, using %s\n",
			progressIntervalEnv, raw, defaultProgressInterval)
		return defaultProgressInterval, true
	}
	return d, true
}

// progressEnabled reports whether to run the ticker. It defaults to the
// inverse of the live mirror: the mirror is the local view, the table is the
// CI view, and printing both interleaves a summary table into a firehose of
// JSON where nobody will see it.
func progressEnabled() bool {
	switch strings.TrimSpace(os.Getenv(progressEnv)) {
	case "1", "true", "on":
		return true
	case "0", "false", "off":
		return false
	}
	return mirrorWriter() == nil
}

// startProgressTicker prints the table every interval until the returned stop
// function is called, which also prints one final table. The final table is
// printed even when the ticker is disabled, so a run always ends with a
// summary.
func startProgressTicker() (stop func()) {
	interval, tick := progressInterval()
	if !progressEnabled() {
		return func() {}
	}
	if !tick {
		return func() { printProgress() }
	}

	done := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				printProgress()
			}
		}
	}()
	return func() {
		once.Do(func() { close(done) })
		printProgress()
	}
}

func printProgress() {
	if table := progress.table(); table != "" {
		_, _ = fmt.Fprint(os.Stderr, table)
	}
}
