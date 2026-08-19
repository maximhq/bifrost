package clis

import (
	"os"
	"path/filepath"
	"testing"
)

// These tests exercise report-directory resolution only -- no CLI subprocesses,
// no Bifrost, no provider traffic -- so they run anywhere:
//
//	GOWORK=off go test ./tests/e2e/clis/ -run 'TestReportsDir|TestLoadReportResultsNested'

// The CI runner gives each filter its own reports subdirectory so one suite's
// cells cannot be mistaken for another's, and so an empty subdirectory proves
// that filter matched nothing. `go test -run` exits 0 when its regex matches no
// subtests, so without that per-filter evidence a drifted CLAUDE_CASES /
// CODEX_CASES regex would pass the release gate having run zero cells.
//
// The aggregate report still has to cover every subdirectory, which is why
// loadReportResults walks instead of globbing one level.
func TestLoadReportResultsNestedDirectories(t *testing.T) {
	t.Chdir(t.TempDir())

	for dir, stem := range map[string]string{
		filepath.Join("reports", "claude"): "claude__anthropic__sonnet__simple-chat",
		filepath.Join("reports", "codex"):  "codex__openai__gpt__simple-chat",
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("seed %s: %v", dir, err)
		}
		seedReportSummary(t, dir, stem)
	}

	// A summary written directly at the root must still be picked up, so an
	// existing local run (or the SIGINT handler) keeps working unchanged.
	seedReportSummary(t, "reports", "opencode__bedrock__nova__file-read")

	results, err := loadReportResults("reports")
	if err != nil {
		t.Fatalf("loadReportResults: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("got %d cells, want 3 (one per subdirectory plus the root one); results: %+v",
			len(results), results)
	}

	seen := map[string]bool{}
	for _, r := range results {
		seen[r.CLI] = true
	}
	for _, cli := range []string{"claude", "codex", "opencode"} {
		if !seen[cli] {
			t.Errorf("cell for %q missing from the aggregate report", cli)
		}
	}
}

// index.html lives at the reports root and must never be mistaken for a cell
// summary now that the loader walks the tree.
func TestLoadReportResultsIgnoresNonSummaries(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := os.MkdirAll(filepath.Join("reports", "claude"), 0o755); err != nil {
		t.Fatalf("seed reports dir: %v", err)
	}
	seedReportSummary(t, filepath.Join("reports", "claude"), "claude__anthropic__sonnet__simple-chat")

	if err := os.WriteFile(filepath.Join("reports", "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("seed index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join("reports", "claude", "x.transcript.log"), []byte("noise"), 0o644); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}

	results, err := loadReportResults("reports")
	if err != nil {
		t.Fatalf("loadReportResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d cells, want exactly 1 (index.html and transcripts are not summaries); results: %+v",
			len(results), results)
	}
}

// reportsDir is what lets the CI runner point one `go test` invocation at its
// own subdirectory. Defaulting to "reports" keeps every local invocation and
// the existing report tests working with no environment set.
func TestReportsDirHonoursEnvOverride(t *testing.T) {
	t.Setenv(reportsDirEnv, "")
	if got := reportsDir(); got != "reports" {
		t.Errorf("with no override, reportsDir() = %q, want \"reports\"", got)
	}

	t.Setenv(reportsDirEnv, filepath.Join("reports", "claude"))
	if got, want := reportsDir(), filepath.Join("reports", "claude"); got != want {
		t.Errorf("reportsDir() = %q, want %q", got, want)
	}
}
