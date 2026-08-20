//go:build unix

// The build constraint lives in the tag above rather than in the filename so
// this file can keep the repo's no-underscores-in-Go-filenames convention (see
// AGENTS.md, Code Style); the `_test.go` suffix is the sole exception.

package clis

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startOrphanHolder runs a shell that exits with `code` immediately while a
// backgrounded child keeps the inherited stdout pipe open.
//
// This is the exact shape the harness hits in production: the drivers hand exec
// an ordinary io.Writer, so exec allocates an os.Pipe and a copying goroutine,
// and every CLI under test re-execs itself as a background server (see
// cmdWaitDelay's comment). Wait cannot return until every holder of the write
// end closes it, so WaitDelay elapses and Wait reports ErrWaitDelay - whatever
// the process's own exit status was.
func startOrphanHolder(t *testing.T, code string) (*exec.Cmd, int, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "sleep 30 & exit "+code)
	cmd.WaitDelay = 500 * time.Millisecond
	// Own process group, so the cleanup below reaps the backgrounded sleep
	// rather than leaving it behind for the rest of the suite.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Start(); err != nil {
		t.Fatalf("start holder: %v", err)
	}

	// Read the group NOW, not in the cleanup. cmd.Wait below reaps the shell
	// before the test body even runs, and a reaped pid is gone: Getpgid would
	// return ESRCH, the kill would be skipped, and the backgrounded sleep would
	// outlive the suite - the exact opposite of what this cleanup is for. The
	// group id stays valid for the survivors after its leader is reaped.
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("read process group: %v", err)
	}
	t.Cleanup(func() {
		if pgid > 1 {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	})
	return cmd, pgid, cmd.Wait()
}

// A turn whose process exited cleanly must not be reported as a failed turn
// just because a grandchild outlived it. Suppressing ErrWaitDelay is only safe
// when the exit status itself was clean, so the negative case matters just as
// much as the positive one.
func TestWaitErrSuppressesWaitDelayOnlyOnCleanExit(t *testing.T) {
	cmd, _, raw := startOrphanHolder(t, "0")

	// Preconditions. If either of these stops holding, this test is no longer
	// exercising the reported condition and would pass for the wrong reason.
	if !errors.Is(raw, exec.ErrWaitDelay) {
		t.Fatalf("precondition: expected Wait to report ErrWaitDelay, got %v", raw)
	}
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("precondition: expected a clean exit status, got %v", cmd.ProcessState)
	}

	if err := waitErr(cmd, raw); err != nil {
		t.Errorf("a turn that exited 0 was reported as failed: %v", err)
	}
}

// The same orphan-holder shape, but the process exits non-zero. os/exec
// documents ErrWaitDelay as returned "instead of nil", so an ExitError always
// wins: a failing turn surfaces as its exit status even though WaitDelay also
// elapsed. That ordering is what keeps the suppression above from ever
// swallowing a real failure, so it is pinned here rather than assumed.
func TestWaitErrPreservesNonZeroExitUnderWaitDelay(t *testing.T) {
	cmd, _, raw := startOrphanHolder(t, "3")

	if raw == nil {
		t.Fatal("a genuinely failing turn reported no error at all")
	}
	if errors.Is(raw, exec.ErrWaitDelay) {
		t.Fatalf("ErrWaitDelay outranked the exit status; suppressing it would now hide real failures: %v", raw)
	}
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 3 {
		t.Fatalf("precondition: expected exit status 3, got %v", cmd.ProcessState)
	}

	err := waitErr(cmd, raw)
	if err == nil {
		t.Fatal("a genuinely failing turn was suppressed as success")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("error lost the exit status that identifies the failure: %v", err)
	}
}

// A Ctrl-C landing in the window between cmd.Wait returning and the deferred
// untrack running must not signal the command. Wait has already reaped it, so
// its pid is back in the kernel's pool: syscall.Kill(-pgid) would be aimed at
// whatever now holds that group, and unlike os.Process.Kill it carries no
// ErrProcessDone guard to stop it. The reaped flag is what closes that window.
//
// markKilled is the observable: killIfLive records there before it signals, so
// an entry in killedCommands proves a signal was attempted. It doubles as a
// correctness check on the cell bookkeeping - a reaped cell wrongly tagged as
// killed would have its genuine failure relabelled as an interruption.
func TestReapedCommandIsNotSignalled(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	trackCmd(cmd)

	v, ok := activeCommands.Load(cmd)
	if !ok {
		t.Fatal("precondition: command was not tracked")
	}
	tc := v.(*trackedCmd)
	if tc.pgid <= 1 {
		t.Fatalf("precondition: expected a real process group captured at Start, got %d", tc.pgid)
	}

	// Reap it, exactly as a driver's Send does, then take the signal handler's
	// path against the entry the handler would still have seen.
	_ = cmd.Wait()
	untrackCmd(cmd)

	killIfLive(cmd, tc)

	if _, killed := killedCommands.Load(cmd); killed {
		killedCommands.Delete(cmd)
		t.Error("signalled a command that had already been reaped; its pid may belong to an unrelated process")
	}
}

// The guard must not shade into never killing anything: a command still running
// is exactly what the handler exists to reap.
func TestLiveCommandIsSignalled(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	trackCmd(cmd)
	t.Cleanup(func() { killedCommands.Delete(cmd) })

	v, _ := activeCommands.Load(cmd)
	tc := v.(*trackedCmd)

	killIfLive(cmd, tc)

	if _, killed := killedCommands.Load(cmd); !killed {
		t.Error("a running command was left un-signalled")
	}
	if err := cmd.Wait(); err == nil {
		t.Error("expected the killed command to report a signal exit")
	}
	untrackCmd(cmd)
}

// A cleanup that could not read the tree must say so. Swallowing the traversal
// error leaves the previous run's reports/*.json in place while the caller logs
// success, and the next run then renders a report over stale cells - the one
// outcome clearing the directory exists to prevent.
//
// Unix-only: it needs a directory the process genuinely cannot read, and
// chmod 000 does not stop root, so this is skipped when running as root.
func TestClearReportsDirReportsTraversalFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny traversal")
	}
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "cell.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	if err := clearReportsDir(dir); err == nil {
		t.Error("an unreadable reports directory was reported as successfully cleared")
	}
}

// An ordinary failure carries no ErrWaitDelay and must pass through untouched.
func TestWaitErrPassesThroughOrdinaryFailures(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "exit 4")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	raw := cmd.Wait()
	if raw == nil {
		t.Fatal("precondition: expected a non-nil error for exit 4")
	}
	if got := waitErr(cmd, raw); !errors.Is(got, raw) {
		t.Errorf("ordinary failure was altered: got %v, want %v", got, raw)
	}
}

// The cleanup has to reap the backgrounded child, and it only can if it knows
// the group before cmd.Wait reaps the leader. Looking the group up afterwards
// gets ESRCH, the kill is skipped, and `sleep 30` outlives the suite - a leak
// that no assertion in the tests above would ever notice.
func TestOrphanHolderCleanupReapsBackgroundChild(t *testing.T) {
	var pgid int
	t.Run("holder", func(t *testing.T) {
		_, p, _ := startOrphanHolder(t, "0")
		pgid = p
		if pgid <= 1 {
			t.Fatalf("precondition: expected a real process group, got %d", pgid)
		}
		// The backgrounded sleep is still running at this point, so the group
		// must still be signalable - otherwise the check after the subtest
		// would pass for the wrong reason.
		if err := syscall.Kill(-pgid, 0); err != nil {
			t.Fatalf("precondition: group already gone before cleanup: %v", err)
		}
	})

	// The subtest has returned, so its t.Cleanup has run.
	if err := syscall.Kill(-pgid, 0); err == nil {
		t.Error("the backgrounded child survived cleanup; it will outlive the suite")
	}
}
