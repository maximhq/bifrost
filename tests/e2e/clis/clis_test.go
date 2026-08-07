package clis

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestMain installs a signal handler that kills every running CLI subprocess
// on Ctrl-C / SIGTERM, then exits. Without it, killing `go test` would orphan
// any spawned `claude` / `codex` processes.
//
// os.Exit() in the signal handler bypasses Go's normal t.Cleanup mechanism,
// so an interrupted run would otherwise lose its HTML report even though
// every completed cell already wrote its own reports/*.json summary to disk.
// writeHTMLReport rebuilds reports/index.html from those files (see
// loadReportResults), so calling it here before exiting is enough to recover
// the report for everything that finished before the interrupt.
func TestMain(m *testing.M) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		s := <-sigs
		// Set this BEFORE killing so any cell that reaches writeReport in the
		// grace window below records itself as interrupted rather than failed.
		interrupted.Store(true)
		_, _ = fmt.Fprintf(os.Stderr, "\n\033[1;31m[harness] received %s, killing %d active subprocesses\033[0m\n",
			s, countActive())
		activeCommands.Range(func(k, _ any) bool {
			if cmd, ok := k.(*exec.Cmd); ok && cmd.Process != nil {
				// Record before killing: the cell reads this to prove the kill
				// was ours rather than a coincident genuine failure.
				markKilled(cmd)
				_ = cmd.Process.Kill()
			}
			return true
		})
		time.Sleep(200 * time.Millisecond)
		reportHTMLBestEffort()
		os.Exit(130)
	}()
	os.Exit(m.Run())
}

// interrupted is set once a SIGINT/SIGTERM has been received. Cells whose
// subprocess we deliberately killed would otherwise be recorded as genuine
// failures ("claude exit: signal: killed"), which is misleading: an
// interrupted sweep produced a report showing 4 red cells that had simply
// been in flight at Ctrl-C, indistinguishable from real regressions.
var interrupted atomic.Bool

func countActive() int {
	n := 0
	activeCommands.Range(func(_, _ any) bool { n++; return true })
	return n
}

// TestCLIs is the matrix entry point. Run a single cell:
//
//	go test -run 'TestCLIs/claude/anthropic/simple-chat' -v
func TestCLIs(t *testing.T) {
	if os.Getenv("BIFROST_E2E_CLIS") == "skip" {
		t.Skip("BIFROST_E2E_CLIS=skip")
	}
	t.Cleanup(reportHTMLBestEffort)

	baseURL := envDefault("BIFROST_BASE_URL", "http://localhost:8080")
	apiKey := envDefault("BIFROST_API_KEY", "dummy")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bf := newBifrostClient(baseURL)
	if err := bf.Health(ctx); err != nil {
		t.Skipf("bifrost not reachable at %s: %v", baseURL, err)
	}
	configured, err := bf.ConfiguredProviders(ctx)
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}

	scenarios := allScenarios()
	modelFilter := os.Getenv("MODEL") // optional substring/exact filter on model ID

	for _, cliID := range sortedKeys(clis) {
		cli := clis[cliID]
		t.Run(cliID, func(t *testing.T) {
			for _, provID := range sortedKeys(providers) {
				prov := providers[provID]
				t.Run(provID, func(t *testing.T) {
					if !configured[provID] {
						t.Skipf("provider %q not configured in bifrost", provID)
					}
					for _, model := range prov.Models {
						model := model
						if modelFilter != "" && !strings.Contains(model.ID, modelFilter) {
							continue
						}
						if !supportsCLIProviderModel(cli.ID, prov.ID, model) {
							continue
						}
						t.Run(safeName(model.ID), func(t *testing.T) {
							for _, sc := range scenarios {
								sc := sc
								t.Run(sc.ID, func(t *testing.T) {
									if !sc.Supports(cli.ID, prov.ID, model) {
										t.Skipf("scenario %q unsupported for %s/%s/%s", sc.ID, cli.ID, prov.ID, model.ID)
									}
									// No effort sweep declared, or no effort surface on
									// this cell: run the scenario once at the CLI's
									// default effort. The Efforts sweep multiplies a
									// scenario's runs, it never gates whether it runs --
									// image-qa/pdf-qa are gated on the CLI, not on
									// adaptive thinking, so skipping here would silently
									// drop them on non-adaptive models.
									if len(sc.Efforts) == 0 || !model.AdaptiveThinking || cli.EffortArgs == nil {
										t.Parallel()
										runCell(t, cli, prov, model, sc, "", baseURL, apiKey)
										return
									}
									for _, effort := range sc.Efforts {
										effort := effort
										t.Run(effort, func(t *testing.T) {
											t.Parallel()
											runCell(t, cli, prov, model, sc, effort, baseURL, apiKey)
										})
									}
								})
							}
						})
					}
				})
			}
		})
	}
}

// TestRenderReport regenerates reports/index.html from whatever
// reports/*.json summaries already exist on disk, without driving any CLI
// or hitting Bifrost/a real provider -- free and instant. Useful after an
// interrupted TestCLIs run (which now also writes the report itself via
// TestMain's signal handler, but this is a safety net / manual re-render),
// or any time you just want to view the current state of local results:
//
//	go test -run TestRenderReport -v ./...
func TestRenderReport(t *testing.T) {
	if err := writeHTMLReport(); err != nil {
		t.Fatalf("render harness report: %v", err)
	}
}

// safeName converts a model ID into something usable as a t.Run subtest name.
// Go's test runner splits on `/` for filtering, so any slashes in model IDs
// (none today, but bedrock-style IDs sometimes have them) get replaced.
func safeName(id string) string {
	return strings.NewReplacer("/", "_", " ", "_").Replace(id)
}

func runCell(t *testing.T, cli CLI, prov Provider, model ModelInfo, sc scenario, effort string, baseURL, apiKey string) {
	if effort != "" && len(sc.Turns) > 1 {
		t.Fatalf("scenario %q: effort-level testing is only wired for single-turn scenarios", sc.ID)
	}
	scenarioLabel := sc.ID
	if effort != "" {
		scenarioLabel = sc.ID + "@" + effort
	}

	modelRef := bifrostModelRef(prov.ID, model.ID)
	cliBaseURL := baseURL + cli.BasePath

	env := []string{
		cli.BaseURLEnv + "=" + cliBaseURL,
		cli.APIKeyEnv + "=" + apiKey,
	}
	for k, v := range cli.ExtraEnv {
		env = append(env, k+"="+v)
	}
	for k, v := range model.Env {
		env = append(env, k+"="+v)
	}
	if cli.PreLaunch != nil {
		extraEnv, cleanup, err := cli.PreLaunch(cliBaseURL, apiKey, modelRef)
		if err != nil {
			t.Fatalf("cli prelaunch: %v", err)
		}
		if cleanup != nil {
			t.Cleanup(cleanup)
		}
		env = append(env, extraEnv...)
	}

	mirror := mirrorWriter()
	if mirror != nil {
		_, _ = fmt.Fprintf(os.Stdout,
			"\n\033[1;36m>>> %s × %s × %s  (model=%s, turns=%d)\033[0m\n",
			cli.ID, prov.ID, scenarioLabel, modelRef, len(sc.Turns),
		)
	}

	started := time.Now()
	transcripts := make([]string, 0, len(sc.Turns))
	var runErr error
	var assertionErr error

	// One overall budget per cell; individual turns still enforce their own
	// timeouts inside the runner. 15 minutes is enough for a 3-turn reasoning
	// scenario on a slow provider; less than the Make recipe's 30m default.
	cellCtx, cellCancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cellCancel()

	if len(sc.Turns) <= 1 {
		// Pattern A: single-turn one-shot.
		var turn Turn
		if len(sc.Turns) == 1 {
			turn = sc.Turns[0]
		}
		out, err := runSingleTurnWithRetry(cellCtx, t, cli, modelRef, turn, env, mirror, effort)
		transcripts = append(transcripts, out)
		runErr = err
		if err == nil {
			if err := assertTurn(turn, assertionOutput(cli.ID, out)); err != nil {
				assertionErr = err
				runErr = err
			}
		}
	} else {
		// Pattern C (Claude) or Pattern B (Codex): multi-turn driver.
		if cli.MultiTurnDriver == nil {
			t.Skipf("multi-turn unsupported for cli %q", cli.ID)
		}
		driver := cli.MultiTurnDriver()
		if err := driver.Start(cellCtx, t, cli, modelRef, env, mirror); err != nil {
			t.Fatalf("driver start: %v", err)
		}
		t.Cleanup(func() {
			if driver != nil {
				driver.Close()
			}
		})
		for i, turn := range sc.Turns {
			out, nextDriver, err := sendMultiTurnWithRetry(cellCtx, t, cli, modelRef, env, mirror, driver, sc.Turns, i)
			driver = nextDriver
			transcripts = append(transcripts, out)
			if err != nil {
				runErr = fmt.Errorf("turn %d: %w", i+1, err)
				break
			}
			if err := assertTurn(turn, assertionOutput(cli.ID, out)); err != nil {
				assertionErr = fmt.Errorf("turn %d: %w", i+1, err)
				runErr = assertionErr
				break
			}
		}
	}

	dur := time.Since(started)
	combined := strings.Join(transcripts, "\n--- next turn ---\n")
	if pat, snip, ok := detectError(combined, sc.ErrorIgnore); ok {
		runErr = fmt.Errorf("error marker %q in transcript:\n%s", pat, snip)
	}
	status := "pass"
	softReason := ""
	switch {
	case cellWasInterrupted(runErr):
		// We killed this cell's subprocess ourselves on Ctrl-C. Report it as
		// interrupted so it's visibly distinct from a real regression, and
		// don't fail the test for it.
		status = "interrupted"
		softReason = summarizeFailure(runErr.Error())
		runErr = nil
	case runErr != nil && assertionErr != nil && isSoftPassCandidate(combined, cli.ID):
		status = "soft_pass"
		softReason = summarizeFailure(assertionErr.Error())
		runErr = nil
	case runErr != nil:
		status = "fail"
	}
	writeReport(t, cli.ID, prov.ID, safeName(model.ID), scenarioLabel, modelRef, status, runErr, softReason, dur, combined)
	logCellResult(cli.ID, prov.ID, modelRef, scenarioLabel, status, dur, runErr, softReason)

	if mirror != nil {
		_, _ = fmt.Fprintf(os.Stdout, "\033[0m\n\033[1;36m<<< %s × %s × %s  (%s)\033[0m\n",
			cli.ID, prov.ID, scenarioLabel, dur.Round(time.Millisecond))
	}

	if runErr != nil {
		t.Fatal(runErr)
	}
}

const maxRateLimitRetries = 3

var rateLimitWaitRE = regexp.MustCompile(`(?i)(?:please\s+wait|try\s+again\s+in|retry\s+after)\s+(\d+)\s*(?:seconds?|secs?|s)\b`)
var rateLimitSignalRE = regexp.MustCompile(`(?i)\brate[_ -]?limit\b|\b429\b|too many requests`)

func runSingleTurnWithRetry(ctx context.Context, t *testing.T, cli CLI, modelRef string, turn Turn, env []string, mirror io.Writer, effort string) (string, error) {
	t.Helper()
	var lastOut string
	for attempt := 0; attempt <= maxRateLimitRetries; attempt++ {
		out, err := runSingleTurn(ctx, t, cli, modelRef, turn, env, mirror, effort)
		lastOut = out
		if wait, ok := rateLimitDelay(out, err); ok {
			if err := sleepForRateLimit(ctx, t, cli.ID, modelRef, wait, attempt+1); err != nil {
				return out, err
			}
			continue
		}
		return out, err
	}
	return lastOut, fmt.Errorf("rate limit persisted after %d retries", maxRateLimitRetries)
}

func sendMultiTurnWithRetry(ctx context.Context, t *testing.T, cli CLI, modelRef string, env []string, mirror io.Writer, driver multiTurnDriver, turns []Turn, turnIndex int) (string, multiTurnDriver, error) {
	t.Helper()
	var lastOut string
	for attempt := 0; attempt <= maxRateLimitRetries; attempt++ {
		out, err := driver.Send(t, turns[turnIndex].Send, turns[turnIndex].Timeout)
		lastOut = out
		if wait, ok := rateLimitDelay(out, err); ok {
			if err := sleepForRateLimit(ctx, t, cli.ID, modelRef, wait, attempt+1); err != nil {
				return out, driver, err
			}
			driver.Close()
			driver = cli.MultiTurnDriver()
			if err := driver.Start(ctx, t, cli, modelRef, env, mirror); err != nil {
				return out, driver, fmt.Errorf("restart driver after rate limit: %w", err)
			}
			// Replay turns can themselves be rate-limited under heavy throttling;
			// wait+retry inline on the same driver rather than recursing into
			// another restart-and-replay cycle.
			for replayIndex := 0; replayIndex < turnIndex; replayIndex++ {
				var replayErr error
				var replayOut string
				var throttled bool
				for replayAttempt := 0; replayAttempt <= maxRateLimitRetries; replayAttempt++ {
					replayOut, replayErr = driver.Send(t, turns[replayIndex].Send, turns[replayIndex].Timeout)
					wait, isThrottle := rateLimitDelay(replayOut, replayErr)
					throttled = isThrottle
					if !throttled {
						break
					}
					if waitErr := sleepForRateLimit(ctx, t, cli.ID, modelRef, wait, replayAttempt+1); waitErr != nil {
						return replayOut, driver, fmt.Errorf("replay turn %d wait after rate limit: %w", replayIndex+1, waitErr)
					}
				}
				if throttled {
					return replayOut, driver, fmt.Errorf("replay turn %d rate-limited after %d retries", replayIndex+1, maxRateLimitRetries)
				}
				if replayErr != nil {
					return replayOut, driver, fmt.Errorf("replay turn %d after rate limit: %w", replayIndex+1, replayErr)
				}
			}
			continue
		}
		return out, driver, err
	}
	return lastOut, driver, fmt.Errorf("rate limit persisted after %d retries", maxRateLimitRetries)
}

func rateLimitDelay(output string, runErr error) (time.Duration, bool) {
	haystack := output
	if runErr != nil {
		haystack += "\n" + runErr.Error()
	}
	unescaped := strings.ReplaceAll(haystack, `\n`, "\n")
	unescaped = strings.ReplaceAll(unescaped, `\"`, `"`)
	if !rateLimitSignalRE.MatchString(unescaped) {
		return 0, false
	}
	matches := rateLimitWaitRE.FindStringSubmatch(unescaped)
	if len(matches) < 2 {
		return 60 * time.Second, true
	}
	seconds, err := strconv.Atoi(matches[1])
	if err != nil || seconds <= 0 {
		return 60 * time.Second, true
	}
	return time.Duration(seconds+1) * time.Second, true
}

func isSoftPassCandidate(transcript, cliID string) bool {
	if strings.TrimSpace(transcript) == "" {
		return false
	}
	if _, _, ok := detectError(transcript, nil); ok {
		return false
	}
	text := strings.TrimSpace(assertionOutput(cliID, transcript))
	if text == "" {
		return false
	}
	if containsFold(text, "is_bifrost_error") || containsFold(text, `"error":`) {
		return false
	}
	return true
}

func sleepForRateLimit(ctx context.Context, t *testing.T, cliID, modelRef string, wait time.Duration, attempt int) error {
	t.Helper()
	_, _ = fmt.Fprintf(os.Stdout, "harness cli=%s model=%s status=rate-limit-retry attempt=%d wait=%s\n",
		cliID, modelRef, attempt, wait.Round(time.Second))
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("rate limit retry wait aborted: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

func logCellResult(cliID, providerID, model, scenarioID, status string, dur time.Duration, runErr error, softReason string) {
	reason := ""
	if runErr != nil {
		reason = " reason=" + tableCell(summarizeFailure(runErr.Error()), 90)
	} else if softReason != "" {
		reason = " reason=" + tableCell(softReason, 90)
	}
	_, _ = fmt.Fprintf(os.Stdout, "harness cli=%s provider=%s model=%s scenario=%s status=%s duration=%s%s\n",
		cliID,
		providerID,
		model,
		scenarioID,
		status,
		dur.Round(time.Millisecond),
		reason,
	)
}

// assertTurn checks that the turn's required substrings appear in the output
// and that any forbidden substrings (refusal markers, etc.) do not.
func assertTurn(turn Turn, output string) error {
	if err := assertTurnRaw(turn, output); err != nil {
		return assertionError{err: err}
	}
	return nil
}

func assertTurnRaw(turn Turn, output string) error {
	// Negative assertions first - they catch refusal-style responses that
	// would otherwise satisfy the positive sentinel assertions.
	for _, s := range turn.AssertNotText {
		if s == "" {
			continue
		}
		if strings.Contains(output, s) {
			return fmt.Errorf("forbidden substring %q present in output (likely a model refusal); tail:\n%s",
				s, tailStr(output, 600))
		}
	}
	for _, s := range turn.AssertText {
		if s == "" {
			continue
		}
		if !strings.Contains(output, s) {
			return fmt.Errorf("expected %q in output, got tail:\n%s", s, tailStr(output, 600))
		}
	}
	for _, s := range turn.AssertTextFold {
		if s == "" {
			continue
		}
		if !containsFold(output, s) {
			return fmt.Errorf("expected %q in output, got tail:\n%s", s, tailStr(output, 600))
		}
	}
	if len(turn.AssertTextAny) > 0 {
		hit := false
		for _, s := range turn.AssertTextAny {
			if strings.Contains(output, s) {
				hit = true
				break
			}
		}
		if !hit {
			return fmt.Errorf("expected one of %v in output, got tail:\n%s",
				turn.AssertTextAny, tailStr(output, 600))
		}
	}
	if len(turn.AssertTextAnyFold) > 0 {
		hit := false
		for _, s := range turn.AssertTextAnyFold {
			if containsFold(output, s) {
				hit = true
				break
			}
		}
		if !hit {
			return fmt.Errorf("expected one of %v in output, got tail:\n%s",
				turn.AssertTextAnyFold, tailStr(output, 600))
		}
	}
	if turn.Validate != nil {
		if err := turn.Validate(output); err != nil {
			return err
		}
	}
	return nil
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func assertionOutput(cliID, raw string) string {
	switch cliID {
	case "codex", "opencode":
		text, sawJSON := extractJSONAssistantText(raw)
		if sawJSON {
			return text
		}
	}
	return raw
}

// mirrorWriter returns os.Stdout when live output is requested, nil otherwise.
func mirrorWriter() io.Writer {
	if os.Getenv("BIFROST_E2E_CLIS_QUIET") != "" {
		return nil
	}
	return os.Stdout
}

func tailStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

var reportMu sync.Mutex

type cellResult struct {
	CLI            string
	Provider       string
	Model          string
	Scenario       string
	Effort         string
	Status         string
	Reason         string
	DurationMs     int64
	TranscriptPath string
	MetaPath       string
}

// reportsDirEnv lets one `go test` invocation write its cells into its own
// directory. The CI runner uses it to give each CLI filter an isolated
// subdirectory; see .github/workflows/scripts/test-cli-harness.sh.
const reportsDirEnv = "BIFROST_E2E_CLIS_REPORTS_DIR"

// reportsDir is where this invocation writes per-cell summaries. It defaults to
// "reports" so every local run and the standalone renderer behave as before.
func reportsDir() string {
	if dir := strings.TrimSpace(os.Getenv(reportsDirEnv)); dir != "" {
		return dir
	}
	return "reports"
}

func writeReport(t *testing.T, cli, provider, modelStem, scenarioID, model, status string, runErr error, softReason string, dur time.Duration, transcript string) {
	t.Helper()
	reportMu.Lock()
	defer reportMu.Unlock()

	dir := reportsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("mkdir %s: %v", dir, err)
		return
	}
	stem := fmt.Sprintf("%s__%s__%s__%s", cli, provider, modelStem, scenarioID)
	metaPath := filepath.Join(dir, stem+".json")
	transcriptPath := filepath.Join(dir, stem+".transcript.log")
	errStr := ""
	if runErr != nil || t.Failed() {
		if runErr != nil {
			errStr = runErr.Error()
		}
	} else if softReason != "" {
		errStr = softReason
	}
	meta := map[string]any{
		"cli":        cli,
		"provider":   provider,
		"scenario":   scenarioID,
		"model":      model,
		"status":     status,
		"error":      errStr,
		"durationMs": dur.Milliseconds(),
	}
	b, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(metaPath, b, 0o644)
	_ = os.WriteFile(transcriptPath, []byte(transcript), 0o644)
}

// loadReportResults rebuilds the report table from every reports/*.json
// summary on disk, rather than from this process's in-memory state. Each
// summary is written synchronously by writeReport before the owning test
// even asserts pass/fail, so this reflects every cell that has EVER
// completed locally -- including ones from an earlier, interrupted run, or
// runs from a completely separate `go test` invocation -- not just cells
// this process happened to run. That's what makes both the SIGINT-handler
// report write and the standalone TestRenderReport (see below) correct.
// The walk is recursive, not a one-level glob: the CI runner gives each CLI
// filter its own subdirectory (see reportsDirEnv) so an empty one proves that
// filter matched no cells, and the aggregate report still has to span all of
// them. Summaries written straight into the root -- every local run, and the
// SIGINT handler -- are found at depth 0 exactly as before.
func loadReportResults(dir string) ([]cellResult, error) {
	var metaPaths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A missing reports dir is a normal "nothing ran yet" state, and an
			// unreadable subdirectory should not sink the whole report.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		metaPaths = append(metaPaths, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(metaPaths)

	results := make([]cellResult, 0, len(metaPaths))
	for _, metaPath := range metaPaths {
		b, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var meta struct {
			CLI        string `json:"cli"`
			Provider   string `json:"provider"`
			Scenario   string `json:"scenario"`
			Model      string `json:"model"`
			Status     string `json:"status"`
			Error      string `json:"error"`
			DurationMs int64  `json:"durationMs"`
		}
		if err := json.Unmarshal(b, &meta); err != nil {
			continue
		}

		scenario, effort := meta.Scenario, ""
		if i := strings.LastIndex(meta.Scenario, "@"); i >= 0 {
			scenario, effort = meta.Scenario[:i], meta.Scenario[i+1:]
		}
		reason := ""
		if meta.Error != "" {
			reason = summarizeFailure(meta.Error)
		}

		results = append(results, cellResult{
			CLI:            meta.CLI,
			Provider:       meta.Provider,
			Model:          meta.Model,
			Scenario:       scenario,
			Effort:         effort,
			Status:         meta.Status,
			Reason:         reason,
			DurationMs:     meta.DurationMs,
			TranscriptPath: strings.TrimSuffix(metaPath, ".json") + ".transcript.log",
			MetaPath:       metaPath,
		})
	}
	return results, nil
}

// writeHTMLReport renders reports/index.html from the per-cell summaries on
// disk. It returns the first error that prevented a report from being written
// so TestRenderReport (and `make cli-harness-report`) fail loudly instead of
// exiting 0 with a missing or stale report. Best-effort callers -- t.Cleanup
// and the signal handler -- log the error and carry on, since neither can fail
// a test.
//
// Loading runs under reportMu, the same lock writeReport holds. Without it the
// signal handler could render while parallel cells were mid-write, read a
// partially written summary, fail to unmarshal it, and silently drop that cell
// from the report (loadReportResults skips unparseable files by design, so the
// loss leaves no trace).
func writeHTMLReport() error {
	dir := reportsDir()
	reportMu.Lock()
	results, err := loadReportResults(dir)
	reportMu.Unlock()
	if err != nil {
		return fmt.Errorf("load harness reports: %w", err)
	}
	if len(results) == 0 {
		return nil
	}

	counts := map[string]int{}
	for _, result := range results {
		counts[result.Status]++
	}

	var body strings.Builder
	body.WriteString(`<!doctype html><html><head><meta charset="utf-8"><title>Bifrost CLI Harness Report</title><style>
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:24px;background:#f7f8fa;color:#151922}
h1{margin:0 0 8px;font-size:24px}.muted{color:#667085}.summary{display:flex;gap:12px;margin:18px 0;flex-wrap:wrap}
.pill{border-radius:999px;padding:6px 12px;background:white;border:1px solid #d0d5dd;font-weight:600}
.pass{color:#067647}.soft_pass{color:#b54708}.fail{color:#b42318}.skip{color:#475467}.interrupted{color:#475467}
table{width:100%;border-collapse:collapse;background:white;border:1px solid #d0d5dd}
th,td{padding:10px 12px;border-bottom:1px solid #eaecf0;text-align:left;vertical-align:top;font-size:13px}
th{position:sticky;top:0;background:#f2f4f7;z-index:1}.model{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px}
details{max-width:720px}summary{cursor:pointer;color:#344054}pre{white-space:pre-wrap;background:#101828;color:#f9fafb;border-radius:6px;padding:12px;max-height:360px;overflow:auto;font-size:12px}
a{color:#175cd3;text-decoration:none}a:hover{text-decoration:underline}
</style></head><body>`)
	body.WriteString("<h1>Bifrost CLI Harness Report</h1>")
	body.WriteString(`<div class="muted">Generated ` + html.EscapeString(time.Now().Format(time.RFC3339)) + `</div>`)
	body.WriteString(`<div class="summary">`)
	for _, status := range []string{"pass", "soft_pass", "fail", "interrupted"} {
		// pass/fail always render (a zero there is itself information);
		// the qualified states only render when they actually occurred.
		if counts[status] == 0 && status != "pass" && status != "fail" {
			continue
		}
		fmt.Fprintf(&body, `<span class="pill %s">%s: %d</span>`, status, html.EscapeString(status), counts[status])
	}
	body.WriteString(`</div><table><thead><tr><th>Status</th><th>CLI</th><th>Provider</th><th>Model</th><th>Scenario</th><th>Effort</th><th>Duration</th><th>Reason</th><th>Logs</th></tr></thead><tbody>`)
	for _, result := range results {
		body.WriteString("<tr>")
		fmt.Fprintf(&body, `<td class="%s">%s</td>`, html.EscapeString(result.Status), html.EscapeString(result.Status))
		body.WriteString("<td>" + html.EscapeString(result.CLI) + "</td>")
		body.WriteString("<td>" + html.EscapeString(result.Provider) + "</td>")
		body.WriteString(`<td class="model">` + html.EscapeString(result.Model) + "</td>")
		body.WriteString("<td>" + html.EscapeString(result.Scenario) + "</td>")
		body.WriteString("<td>" + html.EscapeString(result.Effort) + "</td>")
		body.WriteString("<td>" + html.EscapeString((time.Duration(result.DurationMs) * time.Millisecond).Round(time.Millisecond).String()) + "</td>")
		body.WriteString("<td>" + html.EscapeString(result.Reason) + "</td>")
		body.WriteString("<td>" + transcriptDetails(result) + "</td>")
		body.WriteString("</tr>")
	}
	body.WriteString("</tbody></table></body></html>")

	path := filepath.Join(dir, "index.html")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create reports dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	abs, _ := filepath.Abs(path)
	_, _ = fmt.Fprintf(os.Stdout, "\nCLI harness HTML report: %s\n", abs)
	return nil
}

// reportHTMLBestEffort renders the report for callers that cannot fail a test --
// t.Cleanup and the SIGINT handler -- logging any error rather than dropping it.
func reportHTMLBestEffort() {
	if err := writeHTMLReport(); err != nil {
		_, _ = fmt.Fprintf(os.Stdout, "harness report error: %v\n", err)
	}
}

func transcriptDetails(result cellResult) string {
	transcript, _ := os.ReadFile(result.TranscriptPath)
	relTranscript := html.EscapeString(filepath.Base(result.TranscriptPath))
	relMeta := html.EscapeString(filepath.Base(result.MetaPath))
	return fmt.Sprintf(`<a href="%s">json</a> · <a href="%s">transcript</a><details><summary>inline log</summary><pre>%s</pre></details>`,
		relMeta,
		relTranscript,
		html.EscapeString(string(transcript)),
	)
}

func summarizeFailure(errStr string) string {
	errStr = strings.TrimSpace(errStr)
	if errStr == "" {
		return "test failed without a captured error"
	}
	for _, marker := range []string{"API Error:", `"message":"`, "expected ", "forbidden substring ", "error marker "} {
		if idx := strings.Index(errStr, marker); idx >= 0 {
			errStr = errStr[idx:]
			break
		}
	}
	errStr = strings.ReplaceAll(errStr, "\n", " ")
	errStr = strings.Join(strings.Fields(errStr), " ")
	if len(errStr) > 220 {
		errStr = errStr[:217] + "..."
	}
	return errStr
}

func tableCell(s string, width int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > width {
		if width <= 1 {
			return s[:width]
		}
		return s[:width-1] + "…"
	}
	return s
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
