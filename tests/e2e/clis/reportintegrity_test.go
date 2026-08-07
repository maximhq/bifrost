package clis

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests exercise the report pipeline only -- no CLI subprocesses, no
// Bifrost, no provider traffic -- so they run anywhere:
//
//	GOWORK=off go test ./tests/e2e/clis/ -run 'TestWriteHTMLReport|TestCellWasInterrupted'

// TestWriteHTMLReportReportsFailure pins that report generation surfaces its
// errors instead of printing them and returning. writeHTMLReport used to be
// func() and swallowed every filesystem failure, so `make cli-harness-report`
// exited 0 with a missing or stale reports/index.html and nobody noticed.
func TestWriteHTMLReportReportsFailure(t *testing.T) {
	t.Chdir(t.TempDir())

	// One valid summary, so the run gets past the "no results" early return
	// and actually attempts to write index.html.
	if err := os.MkdirAll("reports", 0o755); err != nil {
		t.Fatalf("seed reports dir: %v", err)
	}
	seedReportSummary(t, "reports", "claude__anthropic__opus__simple-chat")

	// Make index.html impossible to write by putting a directory in its place.
	if err := os.MkdirAll(filepath.Join("reports", "index.html"), 0o755); err != nil {
		t.Fatalf("seed index.html collision: %v", err)
	}

	if err := writeHTMLReport(); err == nil {
		t.Fatal("writeHTMLReport must return an error when index.html cannot be written")
	}
}

// TestWriteHTMLReportSucceedsAndRendersEveryCell is the positive counterpart:
// with a writable directory it reports no error and every seeded cell reaches
// the HTML. Without the per-cell assertion a report that silently dropped rows
// would still look healthy.
func TestWriteHTMLReportSucceedsAndRendersEveryCell(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("reports", 0o755); err != nil {
		t.Fatalf("seed reports dir: %v", err)
	}
	stems := []string{
		"claude__anthropic__opus__simple-chat",
		"codex__openai__gpt__image-qa",
		"opencode__bedrock__nova__conversation-memory",
	}
	for _, stem := range stems {
		seedReportSummary(t, "reports", stem)
	}

	if err := writeHTMLReport(); err != nil {
		t.Fatalf("writeHTMLReport: %v", err)
	}

	html, err := os.ReadFile(filepath.Join("reports", "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	for _, stem := range stems {
		scenario := stem[strings.LastIndex(stem, "__")+2:]
		if !strings.Contains(string(html), scenario) {
			t.Errorf("report is missing cell %q", scenario)
		}
	}
}

// TestWriteHTMLReportIsConsistentUnderConcurrentWrites covers the interrupt
// path: the SIGINT handler calls writeHTMLReport while parallel cells are still
// calling writeReport. Loading used to run outside reportMu, so a half-written
// summary could be read mid-write, fail json.Unmarshal, and be skipped silently
// -- producing a plausible-looking report with cells missing. Run with -race.
func TestWriteHTMLReportIsConsistentUnderConcurrentWrites(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("reports", 0o755); err != nil {
		t.Fatalf("seed reports dir: %v", err)
	}

	// A settled cell from an earlier run, plus one whose write is deliberately
	// caught mid-flight.
	seedReportSummary(t, "reports", "settled__prov__model__scenarioSETTLED")
	const inFlight = "inflight__prov__model__scenarioINFLIGHT"

	truncated := make(chan struct{})
	released := make(chan struct{})
	go func() {
		defer close(released)
		// Exactly what writeReport does: hold reportMu across a non-atomic write.
		// os.WriteFile truncates before writing, so between these two steps the
		// file exists and is empty. The sleep widens that real window to make the
		// hazard deterministic rather than a one-in-a-thousand flake.
		reportMu.Lock()
		defer reportMu.Unlock()

		f, err := os.Create(filepath.Join("reports", inFlight+".json"))
		if err != nil {
			t.Errorf("create in-flight summary: %v", err)
			close(truncated)
			return
		}
		close(truncated)
		time.Sleep(150 * time.Millisecond)
		if _, err := f.Write(reportSummaryJSON(t, inFlight)); err != nil {
			t.Errorf("write in-flight summary: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Errorf("close in-flight summary: %v", err)
		}
	}()

	// Render while that write is mid-flight, exactly as the SIGINT handler would.
	// Taking reportMu makes this block until the write finishes, so the cell is
	// present. Loading outside the lock reads the truncated file, fails to
	// unmarshal it, and drops the cell without a trace.
	<-truncated
	if err := writeHTMLReport(); err != nil {
		t.Fatalf("writeHTMLReport during an in-flight write: %v", err)
	}
	<-released

	html, err := os.ReadFile(filepath.Join("reports", "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(html), "scenarioSETTLED") {
		t.Error("report dropped an already-settled cell")
	}
	if !strings.Contains(string(html), "scenarioINFLIGHT") {
		t.Error("report dropped a cell whose write was in flight: rendering must serialize with writeReport via reportMu")
	}
}

// TestCellWasInterruptedRequiresThisCellsKill pins that interruption is judged
// per cell. `interrupted` is process-global, so a SIGINT that lands after this
// cell's subprocess already exited must not relabel this cell's genuine failure
// as "interrupted" -- that silently clears runErr and hides a real regression,
// which is the opposite of what the interrupted status exists for.
func TestCellWasInterruptedRequiresThisCellsKill(t *testing.T) {
	// The global flag is set for the whole of this test: that is precisely the
	// condition under which the old code relabelled everything.
	interrupted.Store(true)
	t.Cleanup(func() { interrupted.Store(false) })

	killed := &exec.Cmd{}
	untouched := &exec.Cmd{}
	markKilled(killed)
	t.Cleanup(func() { killedCommands.Delete(killed) })

	genuine := fmt.Errorf("claude exit: exit status 1; output tail:\nassertion failed")
	if cellWasInterrupted(annotateIfKilled(untouched, genuine)) {
		t.Error("a cell whose own subprocess was never killed must not be relabelled as interrupted")
	}
	if !cellWasInterrupted(annotateIfKilled(killed, genuine)) {
		t.Error("a cell whose own subprocess the harness killed must be reported as interrupted")
	}

	// Classification is by command identity, never by message text. The error a
	// cell reports embeds tailStr(out, 600) -- raw CLI output -- so matching on
	// the signal name would let a CLI that merely printed those words mask its
	// own failure.
	printedByCLI := fmt.Errorf("claude exit: exit status 1; output tail:\n"+
		"the agent reported: child process died with signal: killed\n%s", "assertion failed")
	if cellWasInterrupted(annotateIfKilled(untouched, printedByCLI)) {
		t.Error("CLI output containing the signal name must not be read as this cell being interrupted")
	}

	// exec.CommandContext kills the process when the per-turn context deadline
	// expires, so a genuine timeout produces the same signal text. That is a real
	// failure and must stay one.
	timedOut := fmt.Errorf("claude exit: signal: killed; output tail:\n(no output before the 90s turn timeout)")
	if cellWasInterrupted(annotateIfKilled(untouched, timedOut)) {
		t.Error("a context timeout must not be reclassified as an interruption")
	}
}

// seedReportSummary writes one reports/<stem>.json in the shape
// loadReportResults expects.
func seedReportSummary(t *testing.T, dir, stem string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, stem+".json"), reportSummaryJSON(t, stem), 0o644); err != nil {
		t.Fatalf("write summary: %v", err)
	}
}

// reportSummaryJSON builds one summary payload in the shape loadReportResults
// expects, from a cli__provider__model__scenario stem.
func reportSummaryJSON(t *testing.T, stem string) []byte {
	t.Helper()
	parts := strings.Split(stem, "__")
	b, err := json.Marshal(map[string]any{
		"cli": parts[0], "provider": parts[1], "model": parts[2],
		"scenario": parts[3], "status": "pass", "error": "", "durationMs": 1234,
	})
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	return b
}
