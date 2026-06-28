package clis

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCodexToolSearchViaLocalBifrostConfig is a focused live e2e for the
// native Codex -> Bifrost /v1 Responses path configured in ~/.codex/config.toml.
// Codex's JSONL output does not expose low-level tool_search_call frames, so we
// verify the observable behavior instead: Codex must use tool search to
// discover the shell tool and report the discovered tool name with a sentinel.
func TestCodexToolSearchViaLocalBifrostConfig(t *testing.T) {
	if os.Getenv("BIFROST_E2E_CLIS") == "skip" {
		t.Skip("BIFROST_E2E_CLIS=skip")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skipf("codex not installed: %v", err)
	}

	configPath := filepath.Join(os.Getenv("HOME"), ".codex", "config.toml")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Skipf("read %s: %v", configPath, err)
	}
	configText := string(configBytes)
	if !strings.Contains(configText, `base_url = "http://bifrost.localdev.com/v1"`) {
		t.Skipf("local codex config is not pointed at bifrost.localdev.com/v1: %s", configPath)
	}
	if !strings.Contains(configText, `wire_api = "responses"`) {
		t.Skipf("local codex config is not using Responses wire API: %s", configPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	prompt := "Use tool search to find the shell execution tool available in this environment. " +
		"You must actually use tool search, not prior knowledge. " +
		"Then reply with exactly two lines: FOUND_TOOL=<tool name> and SEARCHOK."

	cmd := exec.CommandContext(ctx,
		"codex", "exec",
		"--json",
		"--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox",
		prompt,
	)
	cmd.Dir = "/home/siraj/Desktop/codebases/oss/bifrost/bifrost_dev"
	cmd.Env = append(os.Environ(), "CODEX_DISABLE_TELEMETRY=1")

	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	if err := cmd.Run(); err != nil {
		t.Fatalf("codex exec failed: %v\noutput tail:\n%s", err, tailStr(combined.String(), 2000))
	}

	raw := combined.String()
	if pat, snip, ok := detectError(raw, []string{"SEARCHOK"}); ok {
		t.Fatalf("error marker %q in codex transcript:\n%s", pat, snip)
	}

	output := assertionOutput("codex", raw)
	if err := assertTurnRaw(Turn{
		AssertText: []string{"FOUND_TOOL=", "SEARCHOK"},
		Validate: func(output string) error {
			if strings.Contains(output, "FOUND_TOOL=<tool name>") {
				return assertionError{err: errPlaceholder("model returned placeholder tool name")}
			}
			return nil
		},
	}, output); err != nil {
		t.Fatalf("tool-search assertion failed: %v\nraw transcript tail:\n%s", err, tailStr(raw, 2000))
	}
}

func errPlaceholder(msg string) error {
	return &placeholderError{msg: msg}
}

type placeholderError struct {
	msg string
}

func (e *placeholderError) Error() string { return e.msg }
