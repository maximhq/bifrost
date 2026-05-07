package clis

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestMain installs a signal handler that kills every running CLI subprocess
// on Ctrl-C / SIGTERM, then exits. Without it, killing `go test` would orphan
// any spawned `claude` / `codex` processes.
func TestMain(m *testing.M) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		s := <-sigs
		fmt.Fprintf(os.Stderr, "\n\033[1;31m[harness] received %s, killing %d active subprocesses\033[0m\n",
			s, countActive())
		activeCommands.Range(func(k, _ any) bool {
			if cmd, ok := k.(*exec.Cmd); ok && cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			return true
		})
		time.Sleep(200 * time.Millisecond)
		os.Exit(130)
	}()
	os.Exit(m.Run())
}

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
						t.Run(safeName(model.ID), func(t *testing.T) {
							for _, sc := range scenarios {
								sc := sc
								t.Run(sc.ID, func(t *testing.T) {
									if !sc.Supports(cli.ID, prov.ID, model) {
										t.Skipf("scenario %q unsupported for %s/%s/%s", sc.ID, cli.ID, prov.ID, model.ID)
									}
									runCell(t, cli, prov, model, sc, baseURL, apiKey)
								})
							}
						})
					}
				})
			}
		})
	}
}

// safeName converts a model ID into something usable as a t.Run subtest name.
// Go's test runner splits on `/` for filtering, so any slashes in model IDs
// (none today, but bedrock-style IDs sometimes have them) get replaced.
func safeName(id string) string {
	return strings.NewReplacer("/", "_", " ", "_").Replace(id)
}

func runCell(t *testing.T, cli CLI, prov Provider, model ModelInfo, sc scenario, baseURL, apiKey string) {
	modelRef := bifrostModelRef(prov.ID, model.ID)

	env := []string{
		cli.BaseURLEnv + "=" + baseURL + cli.BasePath,
		cli.APIKeyEnv + "=" + apiKey,
	}
	for k, v := range cli.ExtraEnv {
		env = append(env, k+"="+v)
	}

	mirror := mirrorWriter()
	if mirror != nil {
		fmt.Fprintf(os.Stdout,
			"\n\033[1;36m>>> %s × %s × %s  (model=%s, turns=%d)\033[0m\n",
			cli.ID, prov.ID, sc.ID, modelRef, len(sc.Turns),
		)
	}

	started := time.Now()
	transcripts := make([]string, 0, len(sc.Turns))
	var runErr error

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
		out, err := runSingleTurn(cellCtx, t, cli, modelRef, turn, env, mirror)
		transcripts = append(transcripts, out)
		runErr = err
		if err == nil {
			runErr = assertTurn(turn, out)
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
		t.Cleanup(driver.Close)
		for i, turn := range sc.Turns {
			out, err := driver.Send(t, turn.Send, turn.Timeout)
			transcripts = append(transcripts, out)
			if err != nil {
				runErr = fmt.Errorf("turn %d: %w", i+1, err)
				break
			}
			if err := assertTurn(turn, out); err != nil {
				runErr = fmt.Errorf("turn %d: %w", i+1, err)
				break
			}
		}
	}

	dur := time.Since(started)
	combined := strings.Join(transcripts, "\n--- next turn ---\n")
	writeReport(t, cli.ID, prov.ID, safeName(model.ID), sc.ID, modelRef, runErr, dur, combined)

	if mirror != nil {
		fmt.Fprintf(os.Stdout, "\033[0m\n\033[1;36m<<< %s × %s × %s  (%s)\033[0m\n",
			cli.ID, prov.ID, sc.ID, dur.Round(time.Millisecond))
	}

	if runErr != nil {
		t.Fatal(runErr)
	}
	if pat, snip, ok := detectError(combined, sc.ErrorIgnore); ok {
		t.Fatalf("error marker %q in transcript:\n%s", pat, snip)
	}
}

// assertTurn checks that the turn's required substrings appear in the output
// and that any forbidden substrings (refusal markers, etc.) do not.
func assertTurn(turn Turn, output string) error {
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
	return nil
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

func writeReport(t *testing.T, cli, provider, modelStem, scenarioID, model string, runErr error, dur time.Duration, transcript string) {
	t.Helper()
	reportMu.Lock()
	defer reportMu.Unlock()

	dir := "reports"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("mkdir reports: %v", err)
		return
	}
	stem := fmt.Sprintf("%s__%s__%s__%s", cli, provider, modelStem, scenarioID)
	status := "pass"
	errStr := ""
	if runErr != nil || t.Failed() {
		status = "fail"
		if runErr != nil {
			errStr = runErr.Error()
		}
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
	_ = os.WriteFile(filepath.Join(dir, stem+".json"), b, 0o644)
	_ = os.WriteFile(filepath.Join(dir, stem+".transcript.log"), []byte(transcript), 0o644)
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
