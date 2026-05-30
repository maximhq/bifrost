package clis

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// activeCommands tracks every running CLI subprocess so the SIGINT handler
// in TestMain can reap them. Sequential cells means there's at most one at
// a time today, but the sync.Map keeps us future-proof.
var activeCommands sync.Map // *exec.Cmd -> struct{}

// Turn is one user→model exchange in a scenario.
//
//	AssertText:        every substring must appear in the response (case-sensitive)
//	AssertTextFold:    every substring must appear in the response (case-insensitive)
//	AssertTextAny:     at least one substring must appear (case-sensitive)
//	AssertTextAnyFold: at least one substring must appear (case-insensitive)
//	AssertNotText: none of these substrings may appear (catches refusals like
//	               "I don't have access to web search" that would otherwise
//	               pass a positive-only assertion)
type Turn struct {
	Send              string
	AssertText        []string
	AssertTextFold    []string
	AssertTextAny     []string
	AssertTextAnyFold []string
	AssertNotText     []string
	Validate          func(output string) error
	Timeout           time.Duration
}

type assertionError struct {
	err error
}

func (e assertionError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e assertionError) Unwrap() error {
	return e.err
}

// runSingleTurn executes a one-shot prompt: spawn binary, read combined
// stdout+stderr, return what we got. Any assertion is performed by the
// scenario after this returns.
func runSingleTurn(ctx context.Context, t *testing.T, cli CLI, model string, turn Turn, env []string, mirror io.Writer) (string, error) {
	t.Helper()
	timeout := turn.Timeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := cli.SingleTurnArgs(model, turn.Send)
	cmd := exec.CommandContext(cctx, cli.Binary, args...)
	cmd.Env = append(os.Environ(), env...)

	var combined bytes.Buffer
	cmd.Stdout = teeWriter(&combined, mirror)
	cmd.Stderr = teeWriter(&combined, mirror)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start %s: %w", cli.Binary, err)
	}
	activeCommands.Store(cmd, struct{}{})
	defer activeCommands.Delete(cmd)

	if err := cmd.Wait(); err != nil {
		// Treat non-zero exit as a soft failure so the assertion path can
		// still inspect what we got. Include the buffer tail in the error
		// message so a t.Fatal report shows what the CLI actually printed,
		// not just "exit status 1".
		out := combined.String()
		return out, fmt.Errorf("%s exit: %w; output tail:\n%s",
			cli.Binary, err, tailStr(out, 600))
	}
	return combined.String(), nil
}

// multiTurnDriver abstracts the per-CLI mechanism for sending N turns into
// the same conversation. Each call to Send returns the *full* assistant
// text for that turn (concatenated content blocks, ignoring tool_use noise).
type multiTurnDriver interface {
	Start(ctx context.Context, t *testing.T, cli CLI, model string, env []string, mirror io.Writer) error
	Send(t *testing.T, prompt string, timeout time.Duration) (string, error)
	Close()
}

// ---- Claude: bidirectional stream-JSON over stdin/stdout ----

type claudeStreamJSON struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	mirror io.Writer
	cancel context.CancelFunc
	stderr bytes.Buffer
}

func claudeStreamJSONDriver() multiTurnDriver { return &claudeStreamJSON{} }

func (d *claudeStreamJSON) Start(ctx context.Context, t *testing.T, cli CLI, model string, env []string, mirror io.Writer) error {
	t.Helper()
	cctx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	d.mirror = mirror

	args := []string{
		"-p",
		"--dangerously-skip-permissions",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose", // stream-json output requires --verbose per the SDK docs
	}
	if model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.CommandContext(cctx, cli.Binary, args...)
	cmd.Env = append(os.Environ(), env...)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = teeWriter(&d.stderr, mirror)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", cli.Binary, err)
	}
	activeCommands.Store(cmd, struct{}{})

	d.cmd = cmd
	d.stdin = stdinPipe
	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024) // tolerate ~4MB lines
	d.stdout = scanner
	return nil
}

func (d *claudeStreamJSON) Send(t *testing.T, prompt string, timeout time.Duration) (string, error) {
	t.Helper()
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	msg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": prompt,
		},
	}
	line, err := json.Marshal(msg)
	if err != nil {
		return "", err
	}
	if _, err := d.stdin.Write(append(line, '\n')); err != nil {
		return "", fmt.Errorf("write user msg: %w", err)
	}

	deadline := time.After(timeout)
	type result struct {
		text string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		var assistantText strings.Builder
		for d.stdout.Scan() {
			raw := d.stdout.Bytes()
			if d.mirror != nil {
				fmt.Fprintf(d.mirror, "%s\n", raw)
			}
			var ev map[string]any
			if err := json.Unmarshal(raw, &ev); err != nil {
				continue
			}
			extractAssistantText(ev, &assistantText)
			if evType, _ := ev["type"].(string); evType == "result" {
				// Authoritative success/failure signal per the Agent SDK docs.
				if isErr, _ := ev["is_error"].(bool); isErr {
					sub, _ := ev["subtype"].(string)
					msg, _ := ev["error"].(string)
					if msg == "" {
						msg, _ = ev["result"].(string)
					}
					if msg == "" {
						msg = "no error message in result event"
					}
					ch <- result{err: fmt.Errorf("claude result error (subtype=%s): %s", sub, msg)}
					return
				}
				// Prefer the canonical result.result field; fall back to the
				// accumulated assistant content blocks if it's absent.
				text := assistantText.String()
				if r, _ := ev["result"].(string); r != "" {
					text = r
				}
				ch <- result{text: text}
				return
			}
		}
		if err := d.stdout.Err(); err != nil {
			ch <- result{err: fmt.Errorf("read stream: %w", err)}
			return
		}
		ch <- result{err: fmt.Errorf("stream closed without result event; stderr=%q",
			tailStr(d.stderr.String(), 400))}
	}()

	select {
	case r := <-ch:
		return r.text, r.err
	case <-deadline:
		return "", fmt.Errorf("turn timed out after %s", timeout)
	}
}

func (d *claudeStreamJSON) Close() {
	if d.stdin != nil {
		_ = d.stdin.Close()
	}
	if d.cancel != nil {
		d.cancel()
	}
	if d.cmd != nil {
		_ = d.cmd.Wait()
		activeCommands.Delete(d.cmd)
	}
}

// extractAssistantText walks a Claude stream-json event and appends any text
// the assistant produced. We deliberately ignore tool_use / tool_result blocks
// because tests assert on the natural-language portion of responses.
func extractAssistantText(ev map[string]any, out *strings.Builder) {
	t, _ := ev["type"].(string)
	if t != "assistant" {
		return
	}
	msg, _ := ev["message"].(map[string]any)
	if msg == nil {
		return
	}
	content, _ := msg["content"].([]any)
	for _, c := range content {
		block, _ := c.(map[string]any)
		if block == nil {
			continue
		}
		if bt, _ := block["type"].(string); bt == "text" {
			if s, _ := block["text"].(string); s != "" {
				out.WriteString(s)
			}
		}
	}
}

func extractJSONAssistantText(raw string) (string, bool) {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var out strings.Builder
	sawJSON := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var ev any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		sawJSON = true
		if isAssistantEvent(ev) {
			appendJSONText(ev, &out)
		}
	}
	return out.String(), sawJSON
}

func isAssistantEvent(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	if role, _ := m["role"].(string); role == "assistant" {
		return true
	}
	if typ, _ := m["type"].(string); isAssistantType(typ) {
		return true
	}
	if typ, _ := m["type"].(string); typ == "text" {
		if part, _ := m["part"].(map[string]any); part != nil {
			if partType, _ := part["type"].(string); partType == "text" {
				return true
			}
		}
	}
	if typ, _ := m["item_type"].(string); isAssistantType(typ) {
		return true
	}
	// Descend only into known wrapper keys; mirrors appendJSONText so a
	// user envelope's sibling fields can't be misclassified as assistant.
	for _, key := range []string{"item", "message", "delta"} {
		if child, ok := m[key]; ok {
			if isAssistantEvent(child) {
				return true
			}
		}
	}
	return false
}

func isAssistantType(typ string) bool {
	typ = strings.ToLower(typ)
	return strings.Contains(typ, "assistant") ||
		strings.Contains(typ, "agent_message") ||
		strings.Contains(typ, "output_text")
}

func appendJSONText(v any, out *strings.Builder) {
	switch x := v.(type) {
	case map[string]any:
		if s, _ := x["text"].(string); s != "" {
			out.WriteString(s)
			out.WriteByte('\n')
		}
		if s, _ := x["output_text"].(string); s != "" {
			out.WriteString(s)
			out.WriteByte('\n')
		}
		for _, key := range []string{"content", "message", "item", "delta", "part", "parts"} {
			if child, ok := x[key]; ok {
				appendJSONText(child, out)
			}
		}
	case []any:
		for _, child := range x {
			appendJSONText(child, out)
		}
	case string:
		out.WriteString(x)
		out.WriteByte('\n')
	}
}

// ---- Codex: chained `exec` + `resume --last` ----
//
// Codex doesn't expose a bidirectional stream-json mode, so we drive multi-
// turn by spawning one process per turn: the first turn uses `codex exec`,
// subsequent turns use `codex resume --last`. To isolate "last" from the
// user's actual codex history we redirect CODEX_HOME to a per-cell temp dir.

type codexResume struct {
	cli       CLI
	model     string
	envBase   []string
	mirror    io.Writer
	tempHome  string
	turnIndex int
	ctx       context.Context
}

func codexResumeDriver() multiTurnDriver { return &codexResume{} }

func (d *codexResume) Start(ctx context.Context, t *testing.T, cli CLI, model string, env []string, mirror io.Writer) error {
	t.Helper()
	tempHome, err := os.MkdirTemp("", "bifrost-clis-codex-home-*")
	if err != nil {
		return fmt.Errorf("create temp codex home: %w", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempHome) })

	d.cli = cli
	d.model = model
	d.envBase = append(env, "CODEX_HOME="+tempHome)
	d.mirror = mirror
	d.tempHome = tempHome
	d.ctx = ctx
	return nil
}

func (d *codexResume) Send(t *testing.T, prompt string, timeout time.Duration) (string, error) {
	t.Helper()
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	cctx, cancel := context.WithTimeout(d.ctx, timeout)
	defer cancel()

	var args []string
	if d.turnIndex == 0 {
		args = []string{"exec", "--json", "--skip-git-repo-check"}
		if d.model != "" {
			args = append(args, "--model", d.model)
		}
		args = append(args, prompt)
	} else {
		args = []string{"resume", "--last", prompt}
	}
	d.turnIndex++

	cmd := exec.CommandContext(cctx, d.cli.Binary, args...)
	cmd.Env = append(os.Environ(), d.envBase...)

	var stdout bytes.Buffer
	cmd.Stdout = teeWriter(&stdout, d.mirror)
	cmd.Stderr = teeWriter(&stdout, d.mirror)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start codex turn %d: %w", d.turnIndex, err)
	}
	activeCommands.Store(cmd, struct{}{})
	defer activeCommands.Delete(cmd)

	if err := cmd.Wait(); err != nil {
		out := stdout.String()
		return out, fmt.Errorf("codex turn %d exit: %w; output tail:\n%s",
			d.turnIndex, err, tailStr(out, 600))
	}
	return stdout.String(), nil
}

func (d *codexResume) Close() {
	// CODEX_HOME cleanup is registered via t.Cleanup in Start.
}

// ---- OpenCode: chained `run` + `run --continue` ----

type opencodeResume struct {
	cli       CLI
	model     string
	envBase   []string
	mirror    io.Writer
	tempHome  string
	turnIndex int
	ctx       context.Context
}

func opencodeResumeDriver() multiTurnDriver { return &opencodeResume{} }

func (d *opencodeResume) Start(ctx context.Context, t *testing.T, cli CLI, model string, env []string, mirror io.Writer) error {
	t.Helper()
	tempHome, err := os.MkdirTemp("", "bifrost-clis-opencode-home-*")
	if err != nil {
		return fmt.Errorf("create temp opencode home: %w", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempHome) })

	d.cli = cli
	d.model = model
	d.envBase = append(env,
		"XDG_CONFIG_HOME="+tempHome+"/config",
		"XDG_DATA_HOME="+tempHome+"/data",
		"XDG_CACHE_HOME="+tempHome+"/cache",
	)
	d.mirror = mirror
	d.tempHome = tempHome
	d.ctx = ctx
	return nil
}

func (d *opencodeResume) Send(t *testing.T, prompt string, timeout time.Duration) (string, error) {
	t.Helper()
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	cctx, cancel := context.WithTimeout(d.ctx, timeout)
	defer cancel()

	args := []string{"run", "--dangerously-skip-permissions", "--format", "json"}
	if d.turnIndex > 0 {
		args = append(args, "--continue")
	}
	if d.model != "" {
		args = append(args, "--model", opencodeModelRef(d.model))
	}
	args = append(args, prompt)
	d.turnIndex++

	cmd := exec.CommandContext(cctx, d.cli.Binary, args...)
	cmd.Env = append(os.Environ(), d.envBase...)

	var stdout bytes.Buffer
	cmd.Stdout = teeWriter(&stdout, d.mirror)
	cmd.Stderr = teeWriter(&stdout, d.mirror)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start opencode turn %d: %w", d.turnIndex, err)
	}
	activeCommands.Store(cmd, struct{}{})
	defer activeCommands.Delete(cmd)

	if err := cmd.Wait(); err != nil {
		out := stdout.String()
		return out, fmt.Errorf("opencode turn %d exit: %w; output tail:\n%s",
			d.turnIndex, err, tailStr(out, 600))
	}
	return stdout.String(), nil
}

func (d *opencodeResume) Close() {
	// XDG temp dirs are cleaned via t.Cleanup in Start.
}

// ---- shared helpers ----

func teeWriter(target io.Writer, mirror io.Writer) io.Writer {
	if mirror == nil {
		return target
	}
	return io.MultiWriter(target, mirror)
}
