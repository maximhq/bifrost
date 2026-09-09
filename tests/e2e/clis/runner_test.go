package clis

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// killedCommands records the subprocesses the SIGINT handler actually reaped,
// so a cell can distinguish "the harness killed my subprocess" from "my
// subprocess failed on its own". The process-global `interrupted` flag cannot
// make that distinction: a Ctrl-C landing after this cell's subprocess already
// exited would otherwise relabel this cell's genuine failure as "interrupted"
// and clear its error, hiding a real regression behind the very status that
// exists to keep interruptions distinguishable from regressions.
var killedCommands sync.Map // *exec.Cmd -> struct{}

// errSubprocessKilled marks an error caused by the harness reaping this cell's
// own subprocess.
var errSubprocessKilled = errors.New("harness killed this cell's subprocess")

// markKilled records that the signal handler killed cmd.
func markKilled(cmd *exec.Cmd) { killedCommands.Store(cmd, struct{}{}) }

// annotateIfKilled tags err with errSubprocessKilled when cmd is one the signal
// handler reaped, leaving unrelated failures untouched.
func annotateIfKilled(cmd *exec.Cmd, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := killedCommands.Load(cmd); !ok {
		return err
	}
	return fmt.Errorf("%w: %w", errSubprocessKilled, err)
}

// cellWasInterrupted reports whether runErr is this cell's own subprocess being
// reaped on Ctrl-C, rather than a genuine failure that merely coincided with one.
//
// Identity is the only evidence accepted: the tag annotateIfKilled attaches from
// the specific *exec.Cmd the signal handler reaped. Matching "signal: killed" in
// the message would be wrong twice over -- the errors here embed tailStr of the
// CLI's own output, so a CLI that merely printed those words would mask its
// failure, and exec.CommandContext reports the same text when the per-turn
// context deadline kills the process, which is a real timeout. Either would
// clear runErr and hide a regression, the exact failure this exists to prevent.
// Drivers whose kill surfaces through a pipe or scanner instead of cmd.Wait must
// therefore route their errors through annotateIfKilled themselves.
func cellWasInterrupted(runErr error) bool {
	return errors.Is(runErr, errSubprocessKilled)
}

// activeCommands tracks every running CLI subprocess so the SIGINT handler
// in TestMain can reap them. Sequential cells means there's at most one at
// a time today, but the sync.Map keeps us future-proof.
var activeCommands sync.Map // *exec.Cmd -> *trackedCmd

// trackedCmd carries the lifecycle state the signal handler needs before it may
// signal a command.
//
// The pgid is captured at Start rather than looked up at kill time. A lookup
// takes the pid of a command that may already have been reaped, and the kernel
// is free to have recycled that pid; the value captured while the child is
// certainly still ours cannot drift.
type trackedCmd struct {
	pgid int

	mu     sync.Mutex
	reaped bool
}

// trackCmd registers cmd as signallable. Call it immediately after cmd.Start.
func trackCmd(cmd *exec.Cmd) {
	activeCommands.Store(cmd, &trackedCmd{pgid: processGroupID(cmd)})
}

// untrackCmd marks cmd reaped and unregisters it. Call it after cmd.Wait has
// returned, at which point the pid is no longer ours to signal.
func untrackCmd(cmd *exec.Cmd) {
	if v, ok := activeCommands.Load(cmd); ok {
		if tc, ok := v.(*trackedCmd); ok {
			tc.mu.Lock()
			tc.reaped = true
			tc.mu.Unlock()
		}
	}
	activeCommands.Delete(cmd)
}

// killIfLive terminates cmd's process group on behalf of the signal handler,
// unless cmd has already been reaped.
//
// The reaped check is the point of this function. sync.Map.Range may hand the
// handler an entry whose deferred Delete has not run yet, and cmd.Wait has by
// then returned the pid to the kernel's pool. Signalling it means aiming
// SIGKILL at a process group that may now belong to something else entirely -
// and syscall.Kill on a negative pid carries none of os.Process.Kill's
// ErrProcessDone protection, so nothing downstream would catch the mistake.
//
// Holding the mutex across the check and the kill is what makes the pair
// atomic against untrackCmd. It cannot shrink the window between wait4
// returning inside cmd.Wait and untrackCmd taking the lock - no userspace code
// can, since pid recycling is the kernel's to schedule - but it does mean the
// handler and the reaper can never interleave halfway through.
func killIfLive(cmd *exec.Cmd, tc *trackedCmd) {
	if cmd.Process == nil || tc == nil {
		return
	}
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.reaped {
		return
	}
	// Record before killing: the cell reads this to prove the kill was ours
	// rather than a coincident genuine failure.
	markKilled(cmd)
	// Group kill, not Process.Kill: these CLIs spawn servers and helpers of
	// their own, and orphaning those on Ctrl-C leaves them billing tokens
	// against the user's keys long after the harness has exited.
	_ = killCmdGroup(cmd, tc.pgid)
}

// cmdWaitDelay bounds how long cmd.Wait may keep blocking after the process is
// gone or its context has been cancelled.
//
// Without it a cell can hang unboundedly, and the timeouts already in this file
// are powerless to stop it. The drivers hand exec an ordinary io.Writer for
// Stdout/Stderr rather than an *os.File, so exec allocates an os.Pipe and a
// copying goroutine, and Wait blocks until that goroutine sees EOF -- which
// needs *every* holder of the write end to close it. A CLI that spawned a
// server child (opencode always does) leaves that grandchild holding the fd, so
// killing the direct child on deadline achieves nothing: Wait never returns,
// the cell never records a result, and because the model subtest is serial
// while only its scenarios are parallel, the entire remaining matrix stalls
// behind it. isolateProcessGroup removes the usual cause; WaitDelay is the
// backstop for whatever the group kill misses, converting a wedged suite into
// an ordinary reported failure.
//
// 15s is generous relative to pipe drain (milliseconds) and short relative to
// any turn timeout, so it never truncates a healthy cell.
const cmdWaitDelay = 15 * time.Second

// waitErr normalizes the error cmd.Wait returns, clearing the one case that is
// not a failure of the turn.
//
// cmdWaitDelay exists because a CLI's background server child keeps the
// inherited stdout pipe open after the CLI itself exits, so exec's copying
// goroutine never sees EOF. That is the normal shutdown of a healthy turn, not
// a fault: the process exited 0 and its output was fully captured, and only the
// orphaned writer is late. Left unhandled, Wait's ErrWaitDelay turns every such
// turn into a reported failure - most often on opencode, which always spawns
// that child.
//
// os/exec returns ErrWaitDelay "instead of nil", so an ExitError outranks it and
// a genuinely failing turn (including one killed by a signal) still surfaces as
// its exit status. The ExitCode check is therefore belt-and-braces rather than
// load-bearing, kept so the suppression cannot widen if that ordering ever
// changes. TestWaitErrPreservesNonZeroExitUnderWaitDelay pins the ordering.
func waitErr(cmd *exec.Cmd, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 0 {
		return nil
	}
	return err
}

// prepareCmd applies the two guards every CLI subprocess in this harness needs.
// Call it between exec.CommandContext and cmd.Start.
func prepareCmd(cmd *exec.Cmd) *exec.Cmd {
	cmd.WaitDelay = cmdWaitDelay
	isolateProcessGroup(cmd)
	return cmd
}

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
	Send string

	// AttachImage, if set, is an absolute path to a local image file to
	// attach to this turn via the CLI's own attachment mechanism (see
	// CLI.AttachImageArgs). Only meaningful for single-turn scenarios
	// (Pattern A / runSingleTurn) -- CLIs with no attachment flag (Claude)
	// ignore this at the args level; reference the path in Send instead so
	// their own file-read tool can open it.
	AttachImage string

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
func runSingleTurn(ctx context.Context, t *testing.T, cli CLI, model string, turn Turn, env []string, mirror io.Writer, effort string) (string, error) {
	t.Helper()
	timeout := turn.Timeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var extra []string
	if turn.AttachImage != "" && cli.AttachImageArgs != nil {
		extra = append(extra, cli.AttachImageArgs(turn.AttachImage)...)
	}
	if effort != "" && cli.EffortArgs != nil {
		extra = append(extra, cli.EffortArgs(effort)...)
	}

	args := cli.SingleTurnArgs(model, turn.Send, extra)
	cmd := prepareCmd(exec.CommandContext(cctx, cli.Binary, args...))
	cmd.Env = append(os.Environ(), env...)

	var combined bytes.Buffer
	cmd.Stdout = teeWriter(&combined, mirror)
	cmd.Stderr = teeWriter(&combined, mirror)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start %s: %w", cli.Binary, err)
	}
	trackCmd(cmd)
	defer untrackCmd(cmd)

	if err := waitErr(cmd, cmd.Wait()); err != nil {
		// Treat non-zero exit as a soft failure so the assertion path can
		// still inspect what we got. Include the buffer tail in the error
		// message so a t.Fatal report shows what the CLI actually printed,
		// not just "exit status 1".
		out := combined.String()
		return out, annotateIfKilled(cmd, fmt.Errorf("%s exit: %w; output tail:\n%s",
			cli.Binary, err, tailStr(out, 600)))
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

	cmd := prepareCmd(exec.CommandContext(cctx, cli.Binary, args...))
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
	trackCmd(cmd)

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
		return "", annotateIfKilled(d.cmd, fmt.Errorf("write user msg: %w", err))
	}

	deadline := time.After(timeout)
	type result struct {
		text string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		var assistantText strings.Builder
		var toolMarkers strings.Builder
		for d.stdout.Scan() {
			raw := d.stdout.Bytes()
			if d.mirror != nil {
				fmt.Fprintf(d.mirror, "%s\n", raw)
			}
			var ev map[string]any
			if err := json.Unmarshal(raw, &ev); err != nil {
				continue
			}
			extractAssistantText(ev, &assistantText, &toolMarkers)
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
				// accumulated assistant content blocks if it's absent. Tool-use
				// markers are tracked separately and always appended, since
				// result.result only ever contains genuine model text.
				text := assistantText.String()
				if r, _ := ev["result"].(string); r != "" {
					text = r
				}
				text += toolMarkers.String()
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

	// A reaped subprocess surfaces here as a scanner error or a truncated
	// stream, never as cmd.Wait -- so this is where the kill has to be tagged
	// for cellWasInterrupted to see it.
	select {
	case r := <-ch:
		return r.text, annotateIfKilled(d.cmd, r.err)
	case <-deadline:
		return "", annotateIfKilled(d.cmd, fmt.Errorf("turn timed out after %s", timeout))
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
		untrackCmd(d.cmd)
	}
}

// agentToolUseMarker is appended to a turn's asserted text whenever the
// CLI's JSONL/stream-JSON output shows the model actually invoking a
// subagent-delegation tool — Claude's built-in `Agent` tool_use block, or
// Codex's `collab_tool_call` item with tool == "spawn_agent" (see
// codex-rs/exec/src/exec_events.rs,
// https://github.com/openai/codex/blob/rust-v0.145.0/codex-rs/exec/src/exec_events.rs).
// The subagent-delegation scenario asserts on this marker instead of
// natural-language prose, since neither CLI's tool-call payload is prose.
const agentToolUseMarker = "\n[BIFROST_E2E_AGENT_TOOL_USE]\n"

// claudeToolUseMarkers maps a Claude tool_use block's name to the marker to
// emit. The subagent tool was renamed Task -> Agent in Claude Code v2.1.63;
// both names are mapped defensively in case a run is pinned to an older CLI.
var claudeToolUseMarkers = map[string]string{"Agent": agentToolUseMarker, "Task": agentToolUseMarker}

// extractAssistantText walks a Claude stream-json event, appending assistant
// text to out and a synthetic marker to markers for any tool_use block whose
// name we care about (see claudeToolUseMarkers). markers is tracked
// separately from out because Send() prefers the stream's canonical
// result.result field over the accumulated text once present, and that field
// only ever contains real model text — a marker folded into out would be
// silently dropped.
func extractAssistantText(ev map[string]any, out, markers *strings.Builder) {
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
		switch block["type"] {
		case "text":
			if s, _ := block["text"].(string); s != "" {
				out.WriteString(s)
			}
		case "tool_use":
			if name, _ := block["name"].(string); name != "" {
				if marker, ok := claudeToolUseMarkers[name]; ok {
					markers.WriteString(marker)
				}
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
		appendAgentToolUseMarker(ev, &out)
	}
	return out.String(), sawJSON
}

// appendAgentToolUseMarker walks a decoded event looking for a Codex
// collab_tool_call item whose "tool" is "spawn_agent" and appends the shared
// agentToolUseMarker. Runs independently of isAssistantEvent: a
// collab_tool_call item is correctly not assistant text (see
// codex-rs/exec/src/exec_events.rs,
// https://github.com/openai/codex/blob/rust-v0.145.0/codex-rs/exec/src/exec_events.rs),
// so it would never reach appendJSONText otherwise. Mirrors
// isAssistantEvent's item/message/delta wrapper-key descent so it needs no
// new event shapes to be taught.
func appendAgentToolUseMarker(v any, out *strings.Builder) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if typ, _ := m["type"].(string); typ == "collab_tool_call" {
		if tool, _ := m["tool"].(string); tool == "spawn_agent" {
			out.WriteString(agentToolUseMarker)
		}
	}
	for _, key := range []string{"item", "message", "delta"} {
		if child, ok := m[key]; ok {
			appendAgentToolUseMarker(child, out)
		}
	}
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

// ---- Codex: chained `exec` + `exec resume --last` ----
//
// Codex doesn't expose a bidirectional stream-json mode, so we drive multi-
// turn by spawning one process per turn: the first turn uses `codex exec`,
// subsequent turns use `codex exec resume --last`. To isolate "last" from the
// user's actual codex history we redirect CODEX_HOME to a per-cell temp dir.
//
// `exec resume`, not the top-level `codex resume`: the latter is the
// interactive TUI ("Resume a previous interactive session (picker by default
// ...)" per `codex resume --help`), which aborts with "Error: stdin is not a
// terminal" when its stdin is a pipe rather than a tty - as it always is here.
// The resume path takes the same --json/--model/--skip-git-repo-check flags as
// `exec` itself (confirmed via `codex exec resume --help`, codex-cli 0.147.0),
// so every turn emits the same JSONL the turn-1 parser already expects.

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
	// codexPreLaunch already minted a per-cell CODEX_HOME and wrote the Bifrost
	// provider config into it; reuse that directory so `resume --last` reads the
	// same config and session store. Minting a second one here would shadow the
	// config (os/exec keeps the last occurrence of a duplicated env key), sending
	// codex back to api.openai.com. The fallback branch only runs if a caller
	// wires this driver up without the prelaunch.
	tempHome, ok := envValue(env, "CODEX_HOME")
	if !ok {
		var err error
		tempHome, err = os.MkdirTemp("", "bifrost-clis-codex-home-*")
		if err != nil {
			return fmt.Errorf("create temp codex home: %w", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(tempHome) })
		env = append(env, "CODEX_HOME="+tempHome)
	}

	d.cli = cli
	d.model = model
	d.envBase = env
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
		args = []string{"exec", "resume", "--last", "--json", "--skip-git-repo-check"}
		if d.model != "" {
			args = append(args, "--model", d.model)
		}
		args = append(args, prompt)
	}
	d.turnIndex++

	cmd := prepareCmd(exec.CommandContext(cctx, d.cli.Binary, args...))
	cmd.Env = append(os.Environ(), d.envBase...)

	var stdout bytes.Buffer
	cmd.Stdout = teeWriter(&stdout, d.mirror)
	cmd.Stderr = teeWriter(&stdout, d.mirror)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start codex turn %d: %w", d.turnIndex, err)
	}
	trackCmd(cmd)
	defer untrackCmd(cmd)

	if err := waitErr(cmd, cmd.Wait()); err != nil {
		out := stdout.String()
		return out, annotateIfKilled(cmd, fmt.Errorf("codex turn %d exit: %w; output tail:\n%s",
			d.turnIndex, err, tailStr(out, 600)))
	}
	return stdout.String(), nil
}

func (d *codexResume) Close() {
	// CODEX_HOME cleanup belongs to whoever created the directory: codexPreLaunch
	// (via runCell's t.Cleanup) normally, or Start's fallback branch otherwise.
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

	// See the SingleTurnArgs comment in matrix_test.go for why no
	// permission-bypass flag is passed here.
	args := []string{"run", "--format", "json"}
	if d.turnIndex > 0 {
		args = append(args, "--continue")
	}
	if d.model != "" {
		args = append(args, "--model", opencodeModelRef(d.model))
	}
	args = append(args, prompt)
	d.turnIndex++

	cmd := prepareCmd(exec.CommandContext(cctx, d.cli.Binary, args...))
	cmd.Env = append(os.Environ(), d.envBase...)

	var stdout bytes.Buffer
	cmd.Stdout = teeWriter(&stdout, d.mirror)
	cmd.Stderr = teeWriter(&stdout, d.mirror)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start opencode turn %d: %w", d.turnIndex, err)
	}
	trackCmd(cmd)
	defer untrackCmd(cmd)

	if err := waitErr(cmd, cmd.Wait()); err != nil {
		out := stdout.String()
		return out, annotateIfKilled(cmd, fmt.Errorf("opencode turn %d exit: %w; output tail:\n%s",
			d.turnIndex, err, tailStr(out, 600)))
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
