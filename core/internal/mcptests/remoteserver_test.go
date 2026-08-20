package mcptests

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestMain boots examples/mcps/remote-test-server and points the remote-transport
// tests at it, so MCP_HTTP_URL / MCP_SSE_URL need no external server.
//
// Why this exists: the HTTP and SSE cases here (connection_test.go,
// health_monitoring_test.go, codemode_files_test.go, client_management_test.go,
// tool_conflicts_test.go) t.Skip when their URL is empty, so for a long time
// they were simply not running. When the URLs WERE supplied and the host had
// gone away, they did something worse than skip: each one executed and burned
// five connect retries before failing, so a dead dependency read as ~30 product
// defects. Owning the server removes both failure modes.
//
// STDIO fixtures are unaffected - those spawn their server per client from
// MCPClientConfig.StdioConfig, so building the binary is enough. HTTP and SSE
// need something already listening, which is what this provides.
func TestMain(m *testing.M) {
	stop, err := startRemoteTestServer()
	if err != nil {
		// Not fatal: without the server the remote-transport tests skip exactly
		// as they did before, and every STDIO and mocker-backed test still runs.
		// Failing here would turn a missing optional fixture into a red suite.
		log.Printf("mcptests: remote-test-server unavailable, remote-transport tests will skip: %v", err)
	}

	code := m.Run()

	if stop != nil {
		stop()
	}
	os.Exit(code)
}

// startRemoteTestServer launches the local HTTP+SSE MCP server and exports its
// URLs. Returns a stop func, or an error if the fixture could not be started.
func startRemoteTestServer() (func(), error) {
	// MCP_USE_REMOTE=1 hands control back to whatever the environment supplies,
	// for deliberately testing against a hosted MCP server. Off by default: an
	// inherited-but-stale URL (a shell profile, a secrets manager entry for a
	// decommissioned host) is the exact failure this function exists to prevent,
	// so the local server has to win unless someone opts out explicitly.
	if os.Getenv("MCP_USE_REMOTE") == "1" {
		return nil, fmt.Errorf("MCP_USE_REMOTE=1, using environment-supplied URLs")
	}

	// Cleared HERE, not after a successful launch. Every return below this point is a failure
	// path - no binary, no free port, the server never coming up - and each one leaves the
	// remote-transport tests to fall back on whatever the environment already held. That is the
	// stale-URL case this function exists to prevent: the tests would quietly exercise a
	// decommissioned host instead of skipping. Past the MCP_USE_REMOTE opt-out above, the only
	// URLs that may be in play are the ones this function sets itself.
	for _, k := range []string{EnvMCPHTTPServerURL, EnvMCPSSEServerURL, EnvMCPHTTPHeaders, EnvMCPSSEHeaders} {
		if err := os.Unsetenv(k); err != nil {
			return nil, fmt.Errorf("clear inherited %s: %w", k, err)
		}
	}

	// One cancellable context spans the whole fixture: port allocation, the server process, and
	// the readiness dials. stop() cancels it, which is also how the child is torn down -
	// exec.CommandContext kills the process when the context ends.
	ctx, cancel := context.WithCancel(context.Background())
	// Ownership passes to stop() only on the success path; until then every early return has to
	// release the context itself, or a failed fixture leaks it for the rest of the test binary.
	started := false
	defer func() {
		if !started {
			cancel()
		}
	}()

	root, err := bifrostRootDir()
	if err != nil {
		return nil, err
	}
	bin := filepath.Join(root, "..", "examples", "mcps", "remote-test-server", "bin", "remote-test-server")
	if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("binary not built (run `make setup-mcp-tests`): %w", err)
	}

	// Ports are chosen at run time rather than fixed: the suite runs in parallel
	// with whatever else is on the machine, and a hardcoded port turns an
	// unrelated stray process into a confusing bind failure.
	httpPort, err := freePort(ctx)
	if err != nil {
		return nil, fmt.Errorf("allocate http port: %w", err)
	}
	ssePort, err := freePort(ctx)
	if err != nil {
		return nil, fmt.Errorf("allocate sse port: %w", err)
	}

	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("MCP_HTTP_PORT=%d", httpPort),
		fmt.Sprintf("MCP_SSE_PORT=%d", ssePort),
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start remote-test-server: %w", err)
	}

	stop := func() {
		cancel()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}

	for _, p := range []int{httpPort, ssePort} {
		if err := waitForPort(ctx, p, 10*time.Second); err != nil {
			stop()
			return nil, fmt.Errorf("remote-test-server port %d never came up: %w", p, err)
		}
	}

	// Paths match the constants the server pins (see its main.go).
	if err := os.Setenv(EnvMCPHTTPServerURL, fmt.Sprintf("http://localhost:%d/mcp", httpPort)); err != nil {
		stop()
		return nil, err
	}
	if err := os.Setenv(EnvMCPSSEServerURL, fmt.Sprintf("http://localhost:%d/sse", ssePort)); err != nil {
		stop()
		return nil, err
	}
	// Headers were already cleared at the top of this function, alongside the URLs: the local
	// server takes no auth, and an inherited header set would be sent to it verbatim.

	started = true
	log.Printf("mcptests: remote-test-server up (http :%d, sse :%d)", httpPort, ssePort)
	return stop, nil
}

// bifrostRootDir mirrors GetBifrostRoot for callers with no *testing.T.
func bifrostRootDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find bifrost root (go.mod not found)")
		}
		dir = parent
	}
}

// freePort asks the kernel for an unused port and immediately releases it. The
// gap between release and the child's bind is a race in principle; in practice
// the kernel does not hand the same port out again that quickly, and this is
// the standard approach for handing a port to a child process.
func freePort(ctx context.Context) (int, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "localhost:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitForPort(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("localhost:%d", port)
	var d net.Dialer
	for time.Now().Before(deadline) {
		// Per-attempt deadline on top of the caller's context, so one hung dial cannot eat the
		// whole readiness budget while the loop still has time left to retry.
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, 500*time.Millisecond)
		conn, err := d.DialContext(attemptCtx, "tcp", addr)
		cancelAttempt()
		if err == nil {
			_ = conn.Close()
			return nil
		}
		// A cancelled fixture context means stop() ran: give up rather than spending the
		// remaining budget on dials that cannot succeed.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s", timeout)
}
