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
		activeCommands.Range(func(k, v any) bool {
			cmd, ok := k.(*exec.Cmd)
			if !ok {
				return true
			}
			tc, _ := v.(*trackedCmd)
			killIfLive(cmd, tc)
			return true
		})
		time.Sleep(200 * time.Millisecond)
		reportHTMLBestEffort()
		// os.Exit below bypasses t.Cleanup, so the ticker's own final table
		// never prints. Emit one here for the same reason the HTML report is
		// rebuilt above: an interrupted sweep should still say how far it got.
		printProgress()
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
	// The stop function prints one final table. Parallel subtests are all
	// joined before the parent's cleanups run, so by then every cell that was
	// going to finish has recorded itself.
	t.Cleanup(startProgressTicker())

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

	// Deliberately after the health check and provider discovery: an
	// unreachable gateway skips out above without destroying the previous run's
	// report. Deliberately before the CLI loop: every cell calls t.Parallel()
	// before runCell, so parked subtests cannot write until this returns.
	maybeClearReportsDir(t)

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
										progress.register(cli.ID)
										t.Parallel()
										runCell(t, cli, prov, model, sc, "", baseURL, apiKey)
										return
									}
									for _, effort := range sc.Efforts {
										effort := effort
										t.Run(effort, func(t *testing.T) {
											progress.register(cli.ID)
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

	// Every registered cell must also record an outcome, or the progress table
	// would show a permanent gap between TOTAL and DONE that looks identical to
	// a hung cell. The paths below that leave via t.Fatalf/t.Skipf never reach
	// the explicit record after writeReport, so catch them here -- t.Fatalf and
	// t.Skipf both unwind through runtime.Goexit, which runs deferred calls.
	recorded := false
	defer func() {
		if recorded {
			return
		}
		switch {
		case t.Skipped():
			progress.record(cli.ID, "skip")
		default:
			progress.record(cli.ID, "fail")
		}
	}()

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

	logCellStart(cli.ID, modelRef, scenarioLabel, len(sc.Turns))

	started := time.Now()
	transcripts := make([]string, 0, len(sc.Turns))
	// conversation is the structured record the HTML report renders: what was
	// sent and what came back, per turn. The raw transcript is kept alongside it
	// (writeReport still writes the .transcript.log), but raw stream-JSON is not
	// something anyone can read a conversation out of.
	conversation := make([]turnRecord, 0, len(sc.Turns))
	var runErr error
	var assertionErr error

	// recordTurn captures one exchange. out is the CLI's raw output for the
	// turn; the rendered response is the assistant text extracted from it, which
	// for the JSON-event CLIs is the only part that is prose.
	recordTurn := func(i int, turn Turn, out string, turnStart time.Time, turnErr error) {
		rec := turnRecord{
			Index:      i + 1,
			Prompt:     turn.Send,
			Response:   strings.TrimSpace(assertionOutput(cli.ID, out)),
			DurationMs: time.Since(turnStart).Milliseconds(),
		}
		if turn.AttachImage != "" {
			rec.Attachment = turn.AttachImage
		}
		if turnErr != nil {
			rec.Error = summarizeFailure(turnErr.Error())
		}
		conversation = append(conversation, rec)
	}

	// One overall budget per cell; individual turns still enforce their own
	// timeouts inside the runner.
	//
	// Scaled to the scenario rather than fixed. A flat 15 minutes was fine for
	// three turns, but reasoning-replay's five 180s turns sum to exactly 15
	// minutes -- so the cell budget would fire first and report a deadline
	// instead of whichever turn actually stalled, turning a diagnosable turn
	// timeout into an opaque one.
	cellCtx, cellCancel := context.WithTimeout(context.Background(), cellBudget(sc))
	defer cellCancel()

	if len(sc.Turns) <= 1 {
		// Pattern A: single-turn one-shot.
		var turn Turn
		if len(sc.Turns) == 1 {
			turn = sc.Turns[0]
		}
		turnStart := time.Now()
		out, err := runSingleTurnWithRetry(cellCtx, t, cli, modelRef, turn, env, mirror, effort)
		transcripts = append(transcripts, out)
		runErr = err
		if err == nil {
			if err := assertTurn(turn, assertionOutput(cli.ID, out)); err != nil {
				assertionErr = err
				runErr = err
			}
		}
		recordTurn(0, turn, out, turnStart, runErr)
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
			turnStart := time.Now()
			out, nextDriver, err := sendMultiTurnWithRetry(cellCtx, t, cli, modelRef, env, mirror, driver, sc.Turns, i)
			driver = nextDriver
			transcripts = append(transcripts, out)
			if err != nil {
				runErr = fmt.Errorf("turn %d: %w", i+1, err)
				recordTurn(i, turn, out, turnStart, runErr)
				break
			}
			if err := assertTurn(turn, assertionOutput(cli.ID, out)); err != nil {
				assertionErr = fmt.Errorf("turn %d: %w", i+1, err)
				runErr = assertionErr
				recordTurn(i, turn, out, turnStart, runErr)
				break
			}
			recordTurn(i, turn, out, turnStart, nil)
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
	writeReport(t, cli.ID, prov.ID, safeName(model.ID), scenarioLabel, modelRef, status, runErr, softReason, dur, combined, conversation)
	logCellResult(cli.ID, modelRef, scenarioLabel, status, len(sc.Turns), dur, runErr, softReason)
	progress.record(cli.ID, status)
	recorded = true

	if mirror != nil {
		_, _ = fmt.Fprintf(os.Stdout, "\033[0m\n\033[1;36m<<< %s × %s × %s  (%s)\033[0m\n",
			cli.ID, prov.ID, scenarioLabel, dur.Round(time.Millisecond))
	}

	if runErr != nil {
		t.Fatal(runErr)
	}
}

// cellBudget is the overall deadline for one cell: the sum of its turns' own
// timeouts plus headroom for process startup, with a 15 minute floor so short
// scenarios keep the budget they have always had.
//
// The headroom matters as much as the sum. Each turn of a resume-driver CLI
// (codex, opencode) spawns a fresh process that re-reads its config and, for
// opencode, rebuilds an empty SQLite store and re-fetches the models.dev
// catalogue -- seconds per turn that belong to no turn's own timeout.
func cellBudget(sc scenario) time.Duration {
	const (
		floor          = 15 * time.Minute
		perTurnStartup = 45 * time.Second
		defaultTurn    = 120 * time.Second // runner's fallback when Timeout is 0
	)
	total := time.Duration(0)
	for _, turn := range sc.Turns {
		if turn.Timeout > 0 {
			total += turn.Timeout
		} else {
			total += defaultTurn
		}
		total += perTurnStartup
	}
	if total < floor {
		return floor
	}
	return total
}

const maxRateLimitRetries = 3

var rateLimitWaitRE = regexp.MustCompile(`(?i)(?:please\s+wait|try\s+again\s+in|retry\s+after)\s+(\d+)\s*(?:seconds?|secs?|s)\b`)
// rateLimitSignalRE requires a failure word alongside the phrase, not the phrase
// alone.
//
// The bare form matched the model's own writing. A cell burned three 60-second
// retries and then failed outright because the assistant had read Bifrost's
// architecture docs and quoted back "PreLLMHook Pipeline (auth, rate-limit,
// cache check - registration order)". Discussing rate limits is not being rate
// limited, and any scenario where a model reads this repo can quote any marker,
// so widening ErrorIgnore per scenario would be endless whack-a-mole.
//
// 429 and "too many requests" stay unqualified: they are unambiguous on their
// own, and both are what a CLI prints when it really is throttled.
var rateLimitSignalRE = regexp.MustCompile(
	`(?i)\brate[ _-]?limits?\b[^\n]{0,40}\b(?:exceed|reach|hit|error|throttl|retry|wait)` +
		// "error" belongs in the reverse-order branch too: "API Error: rate
		// limit" is standard CLI wording, and it puts the failure word ahead of
		// the phrase where the forward branch cannot see it. Without it a real
		// throttle skipped the retry path and failed the cell outright.
		`|(?i)\b(?:exceeded|hit|reached|throttled|error)\b[^\n]{0,40}\brate[ _-]?limit` +
		`|\b429\b` +
		`|(?i)too many requests` +
		`|(?i)rate_limit_error`)

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

// logCellResult prints one line per finished cell: the scenario and how it
// went, first, since that is what a reader is scanning for. The cell's identity
// and duration follow, and a failure reason is appended when there is one.
//
// Deliberately one line: cells finish concurrently, so anything multi-line
// would interleave into nonsense. Nothing parses this format - it exists to be
// read by a human and to be the record in CI's artifact log.
// logCellStart announces a cell as it begins. Cells run in parallel and a
// single cell can take minutes, so without this the console sits silent with no
// indication of what is in flight - which reads as a hang.
func logCellStart(cliID, model, scenarioID string, turns int) {
	_, _ = fmt.Fprintf(os.Stdout, "%-11s %-28s %-8s %-9s %-28s\n",
		"RUNNING", scenarioID, turnsLabel(turns), cliID, model)
}

// turnsLabel renders a scenario's conversation length for the console. Turn
// count is the difference between a one-shot probe and a multi-turn
// conversation, which is most of what these scenarios are actually testing, so
// it belongs next to the scenario name rather than only in the report.
func turnsLabel(turns int) string {
	if turns == 1 {
		return "1 turn"
	}
	return fmt.Sprintf("%d turns", turns)
}

// model is the provider-prefixed ref ("openai/gpt-5.5"), so the provider needs
// no column of its own.
func logCellResult(cliID, model, scenarioID, status string, turns int, dur time.Duration, runErr error, softReason string) {
	reason := ""
	if runErr != nil {
		reason = "  " + tableCell(summarizeFailure(runErr.Error()), 120)
	} else if softReason != "" {
		reason = "  " + tableCell(softReason, 120)
	}
	// Widths fit the longest of each: "conversation-role-stability" (27) and
	// the status labels ("interrupted", 11).
	_, _ = fmt.Fprintf(os.Stdout, "%-11s %-28s %-8s %-9s %-28s %8s%s\n",
		strings.ToUpper(status),
		scenarioID,
		turnsLabel(turns),
		cliID,
		model,
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
	case "codex", "opencode", "opencode-responses", "opencode-anthropic":
		text, sawJSON := extractJSONAssistantText(raw)
		if sawJSON {
			return text
		}
	}
	return raw
}

// mirrorWriter returns os.Stdout when the live mirror is requested, nil
// otherwise.
//
// Off by default, deliberately. The mirror streams each CLI subprocess's raw
// stdout, which for a JSON-event CLI (OpenCode, Codex) is thousands of lines
// per turn - and with cells running in parallel, several interleaved streams at
// once. That buries the per-cell result lines and the progress table, i.e. the
// output people actually read, so it is opt-in for the case it was built for:
// debugging one specific cell's wire traffic.
//
// BIFROST_E2E_CLIS_QUIET is still honoured, and still wins, so existing
// invocations (and CI) keep working unchanged.
func mirrorWriter() io.Writer {
	if os.Getenv("BIFROST_E2E_CLIS_QUIET") != "" {
		return nil
	}
	if os.Getenv("BIFROST_E2E_CLIS_MIRROR") != "" {
		return os.Stdout
	}
	return nil
}

func tailStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

var reportMu sync.Mutex

// turnRecord is one user→assistant exchange, as the report renders it. It is
// deliberately the *extracted* conversation rather than the raw stream: the raw
// transcript is kept in the sibling .transcript.log for debugging, but nobody
// can read a conversation out of thousands of stream-JSON events.
type turnRecord struct {
	Index      int    `json:"index"`
	Prompt     string `json:"prompt"`
	Response   string `json:"response"`
	Attachment string `json:"attachment,omitempty"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
}

type cellResult struct {
	CLI            string
	Provider       string
	Model          string
	Scenario       string
	Effort         string
	Status         string
	Reason         string
	DurationMs     int64
	Turns          []turnRecord
	TranscriptPath string
	MetaPath       string
}

// reportsDirEnv lets one `go test` invocation write its cells into its own
// directory. The CI runner uses it to give each CLI filter an isolated
// subdirectory; see .github/workflows/scripts/test-cli-harness.sh.
const reportsDirEnv = "BIFROST_E2E_CLIS_REPORTS_DIR"

// keepReportsEnv preserves the reports directory across a run. See
// maybeClearReportsDir.
const keepReportsEnv = "BIFROST_E2E_CLIS_KEEP_REPORTS"

var clearReportsOnce sync.Once

// maybeClearReportsDir empties the reports directory once per run, so
// index.html shows THIS run rather than every cell ever executed locally
// (the aggregate had grown past 300 stale summaries, with no timestamp to tell
// runs apart).
//
// Two cases must NOT clear, and both are load-bearing:
//
//   - BIFROST_E2E_CLIS_REPORTS_DIR set. CI owns the directory lifecycle there:
//     test-cli-harness.sh rm -rf's each per-label dir up front, and then its
//     retry loop re-invokes `go test` against that SAME dir to re-run only the
//     failed cells. Clearing on that second invocation would delete the passing
//     cells' summaries, breaking count_failed_cells, the aggregate report and
//     the "cells produced: 0" guard.
//   - BIFROST_E2E_CLIS_KEEP_REPORTS set, for accumulating across several
//     filtered local runs on purpose.
//
// TestRenderReport / `make cli-harness-report` never reach this: they re-render
// what is already on disk, which is the whole point of them.
func maybeClearReportsDir(t *testing.T) {
	t.Helper()
	if os.Getenv(reportsDirEnv) != "" || os.Getenv(keepReportsEnv) != "" {
		return
	}
	clearReportsOnce.Do(func() {
		reportMu.Lock()
		defer reportMu.Unlock()
		if err := clearReportsDir(reportsDir()); err != nil {
			t.Logf("could not clear reports dir: %v", err)
		}
	})
}

// clearReportsDir removes previous results from dir, recursively so stale
// per-label subdirectories go too (loadReportResults walks the same tree).
//
// Only the harness's own artefacts are deleted, rather than os.RemoveAll on the
// directory: anything else a developer has parked in reports/ survives, and a
// caller who points reportsDir somewhere unwise cannot lose unrelated data. A
// missing directory is success, not an error.
func clearReportsDir(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		// Surface traversal failures rather than skipping them. A directory
		// that cannot be read still holds the previous run's reports, and
		// swallowing the error here would let maybeClearReportsDir report a
		// successful clear - after which the run renders its report over stale
		// cells from an earlier invocation.
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if filepath.Ext(name) == ".json" || strings.HasSuffix(name, ".transcript.log") || name == "index.html" {
			if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
				return rmErr
			}
		}
		return nil
	})
}

// reportsDir is where this invocation writes per-cell summaries. It defaults to
// "reports" so every local run and the standalone renderer behave as before.
func reportsDir() string {
	if dir := strings.TrimSpace(os.Getenv(reportsDirEnv)); dir != "" {
		return dir
	}
	return "reports"
}

func writeReport(t *testing.T, cli, provider, modelStem, scenarioID, model, status string, runErr error, softReason string, dur time.Duration, transcript string, turns []turnRecord) {
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
		"turns":      turns,
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
			CLI        string       `json:"cli"`
			Provider   string       `json:"provider"`
			Scenario   string       `json:"scenario"`
			Model      string       `json:"model"`
			Status     string       `json:"status"`
			Error      string       `json:"error"`
			DurationMs int64        `json:"durationMs"`
			Turns      []turnRecord `json:"turns"`
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
			Turns:          meta.Turns,
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
	body.WriteString(reportHead)
	body.WriteString(`<header><div><h1>Bifrost CLI Harness Report</h1>` +
		`<span class="meta">Generated ` + html.EscapeString(time.Now().Format(time.RFC3339)) + `</span></div></header>`)

	body.WriteString(`<div class="summary">`)
	fmt.Fprintf(&body, `<span class="pill total">cells: %d</span>`, len(results))
	for _, status := range []string{"pass", "soft_pass", "fail", "interrupted"} {
		// pass/fail always render (a zero there is itself information);
		// the qualified states only render when they actually occurred.
		if counts[status] == 0 && status != "pass" && status != "fail" {
			continue
		}
		fmt.Fprintf(&body, `<span class="pill %s">%s: %d</span>`, pillClass(status), html.EscapeString(status), counts[status])
	}
	fmt.Fprintf(&body, `<span class="pill total">turns: %d</span>`, totalTurns(results))
	body.WriteString(`</div><main>`)
	for _, result := range results {
		body.WriteString(renderCellCard(result))
	}
	body.WriteString(`</main></body></html>`)

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

// reportHead is the report's document head. The palette, spacing and card
// layout deliberately mirror the provider harness's viewer
// (tests/e2e/api/runners/harness-viewer.mjs) so the two harnesses' reports read
// as one family rather than two unrelated tools.
//
// Unlike that viewer, this report is a static file with no JavaScript: it is
// uploaded as a CI artifact and published to R2, where it has to work as a
// plain file open from disk. Expansion is therefore native <details>/<summary>
// with the disclosure marker suppressed, not a click handler.
const reportHead = `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Bifrost CLI Harness Report</title><style>
:root{color-scheme:dark}
*{box-sizing:border-box}
body{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;margin:0;background:#0d1117;color:#e6edf3}
header{display:flex;align-items:center;justify-content:space-between;padding:12px 24px;border-bottom:1px solid #30363d;background:#161b22;position:sticky;top:0;z-index:10}
header h1{font-size:16px;margin:0;font-weight:600;display:inline}
header .meta{font-size:12px;color:#8b949e;margin-left:12px}
.summary{padding:12px 24px;display:flex;gap:12px;font-size:13px;border-bottom:1px solid #30363d;flex-wrap:wrap}
.pill{padding:4px 10px;border-radius:999px}
.pill.pass{background:#1a4731;color:#4ade80}
.pill.fail{background:#5b1a1a;color:#f87171}
.pill.warn{background:#3a3a1a;color:#facc15}
.pill.total{background:#1f2933;color:#93c5fd}
main{padding:16px 24px 64px}
.cell{border:1px solid #30363d;border-radius:8px;margin-bottom:12px;overflow:hidden}
.cell-head{padding:10px 14px;display:grid;grid-template-columns:92px 1fr auto auto auto;gap:12px;align-items:center;cursor:pointer;list-style:none}
.cell-head::-webkit-details-marker{display:none}
.cell-head:hover{background:#161b22}
.cell.failed .cell-head{background:#2d0f12}
.cell.failed .cell-head:hover{background:#3a1418}
.status{font-family:ui-monospace,monospace;font-weight:700;font-size:11px;padding:3px 8px;border-radius:4px;text-align:center}
.status.ok{background:#0d3a2a;color:#4ade80}
.status.err{background:#5b1a1a;color:#f87171}
.status.warn{background:#3a3a1a;color:#facc15}
.name{font-weight:600;font-size:14px}
.name .folder{font-weight:400;color:#8b949e;font-size:12px;margin-left:8px;font-family:ui-monospace,monospace}
.badge{font-family:ui-monospace,monospace;font-size:11px;color:#93c5fd;background:#1f2933;padding:3px 8px;border-radius:4px;white-space:nowrap}
.dur{font-family:ui-monospace,monospace;font-size:12px;color:#8b949e;min-width:64px;text-align:right}
.cell-body{padding:14px;border-top:1px solid #30363d;background:#0d1117}
.panel{margin-bottom:14px}
.panel h3{font-size:12px;color:#8b949e;text-transform:uppercase;letter-spacing:.5px;margin:0 0 6px}
pre{background:#161b22;border:1px solid #30363d;padding:10px;border-radius:6px;overflow-x:auto;font-size:12px;line-height:1.5;max-height:420px;white-space:pre-wrap;word-break:break-word;margin:0}
a{color:#79c0ff;text-decoration:none}a:hover{text-decoration:underline}
.reason{color:#f87171;font-size:13px}
pre.convo{max-height:none;font-family:ui-monospace,monospace}
/* The role marker is the only styled element in the transcript: colour is what
   keeps it findable when a long multi-line message runs past it. */
.role{font-weight:700}
.role.user{color:#79c0ff}
.role.assistant{color:#4ade80}
.role.attachment{color:#8b949e}
.role.error{color:#f87171}
details.raw summary{cursor:pointer;color:#8b949e;font-size:12px;padding:4px 0}
</style></head><body>`

func pillClass(status string) string {
	switch status {
	case "pass":
		return "pass"
	case "fail":
		return "fail"
	default:
		return "warn"
	}
}

// statusClass maps a cell status onto the viewer's badge colours: green for a
// clean pass, red for a regression, amber for the qualified states that are
// neither.
func statusClass(status string) string {
	switch status {
	case "pass":
		return "ok"
	case "fail":
		return "err"
	default:
		return "warn"
	}
}

func totalTurns(results []cellResult) int {
	n := 0
	for _, result := range results {
		n += len(result.Turns)
	}
	return n
}

// renderCellCard renders one cell as an expandable card. Anything that did not
// cleanly pass is expanded on load - the conversation is the first thing wanted
// on a failure, and making someone click every red row to find it defeats the
// purpose of recording it.
func renderCellCard(result cellResult) string {
	var b strings.Builder
	cls := "cell"
	open := ""
	if result.Status != "pass" {
		cls += " failed"
		open = " open"
	}
	fmt.Fprintf(&b, `<details class="%s"%s><summary class="cell-head">`, cls, open)
	fmt.Fprintf(&b, `<span class="status %s">%s</span>`,
		statusClass(result.Status), html.EscapeString(strings.ToUpper(result.Status)))

	scenario := result.Scenario
	if result.Effort != "" {
		scenario += " @" + result.Effort
	}
	fmt.Fprintf(&b, `<span class="name">%s<span class="folder">%s · %s</span></span>`,
		html.EscapeString(scenario), html.EscapeString(result.CLI), html.EscapeString(result.Model))
	fmt.Fprintf(&b, `<span class="badge">%s</span>`, html.EscapeString(turnsLabel(len(result.Turns))))
	fmt.Fprintf(&b, `<span class="badge">%s</span>`, html.EscapeString(result.Provider))
	fmt.Fprintf(&b, `<span class="dur">%s</span>`,
		html.EscapeString((time.Duration(result.DurationMs)*time.Millisecond).Round(time.Millisecond).String()))
	b.WriteString(`</summary><div class="cell-body">`)

	if result.Reason != "" {
		fmt.Fprintf(&b, `<div class="panel"><h3>reason</h3><div class="reason">%s</div></div>`,
			html.EscapeString(result.Reason))
	}
	b.WriteString(conversationDetails(result))
	b.WriteString(transcriptDetails(result))
	b.WriteString(`</div></details>`)
	return b.String()
}

func transcriptDetails(result cellResult) string {
	transcript, _ := os.ReadFile(result.TranscriptPath)
	relTranscript := html.EscapeString(filepath.Base(result.TranscriptPath))
	relMeta := html.EscapeString(filepath.Base(result.MetaPath))

	var b strings.Builder
	b.WriteString(`<div class="panel"><h3>artifacts</h3>`)
	fmt.Fprintf(&b, `<a href="%s">summary json</a> · <a href="%s">transcript</a>`, relMeta, relTranscript)
	// The raw stream stays available but stays collapsed even on a failure: it
	// is the debugging artefact, whereas the conversation above is the record
	// anyone reads first.
	fmt.Fprintf(&b, `<details class="raw"><summary>raw stream (%d bytes)</summary><pre>%s</pre></details>`,
		len(transcript), html.EscapeString(string(transcript)))
	b.WriteString(`</div>`)
	return b.String()
}

// conversationDetails renders the cell's turns as a plain transcript - one
// `<role>: <content>` line per message, in order, in a single block.
//
// Deliberately unstyled beyond role colouring: a conversation is a linear
// thing, and wrapping each message in its own card makes a three-turn exchange
// scroll like a dashboard. This is the same shape you would read in a chat log.
// It is always expanded, since the enclosing cell card is what collapses.
func conversationDetails(result cellResult) string {
	if len(result.Turns) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<div class="panel"><h3>conversation · %s</h3><pre class="convo">`,
		html.EscapeString(turnsLabel(len(result.Turns))))
	for i, turn := range result.Turns {
		if i > 0 {
			b.WriteString("\n")
		}
		writeConvoLine(&b, "user", turn.Prompt)
		if turn.Attachment != "" {
			writeConvoLine(&b, "attachment", turn.Attachment)
		}
		response := turn.Response
		if strings.TrimSpace(response) == "" {
			// An empty response is itself the finding on a failed turn -- say so
			// rather than emitting a blank line that reads as a rendering bug.
			response = "(no assistant text extracted from this turn)"
		}
		writeConvoLine(&b, "assistant", response)
		if turn.Error != "" {
			writeConvoLine(&b, "error", turn.Error)
		}
	}
	b.WriteString(`</pre></div>`)
	return b.String()
}

// writeConvoLine emits one `role: content` line. The role is wrapped in a span
// so it can be coloured, which is the only concession to styling - it keeps a
// long multi-line message from visually swallowing the next role marker.
func writeConvoLine(b *strings.Builder, role, content string) {
	fmt.Fprintf(b, `<span class="role %s">%s:</span> %s`+"\n",
		role, role, html.EscapeString(strings.TrimRight(content, "\n")))
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

// envValue looks up key in a "K=V" slice of the kind runCell builds. It scans
// backwards because os/exec resolves duplicate keys to the last occurrence, so
// the last entry is the one the child process will actually see.
func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix), true
		}
	}
	return "", false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
