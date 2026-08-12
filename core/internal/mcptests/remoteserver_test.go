package mcptests

import (
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
	httpPort, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("allocate http port: %w", err)
	}
	ssePort, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("allocate sse port: %w", err)
	}

	cmd := exec.Command(bin)
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
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}

	for _, p := range []int{httpPort, ssePort} {
		if err := waitForPort(p, 10*time.Second); err != nil {
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
	// The local server takes no auth, and a stale header set inherited from the
	// environment would be sent to it verbatim. Clear both so the fixture is
	// self-consistent.
	_ = os.Unsetenv(EnvMCPHTTPHeaders)
	_ = os.Unsetenv(EnvMCPSSEHeaders)

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
func freePort() (int, error) {
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitForPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("localhost:%d", port)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s", timeout)
}
