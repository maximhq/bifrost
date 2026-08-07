package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestCreateSTDIOConnectionAllowsInlineEnvAssignments(t *testing.T) {
	t.Parallel()

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"TEST_STDIO_ENV_ASSIGNMENT=inline-value"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.NoError(t, err)
}

func TestCreateSTDIOConnectionAllowsSetReferencedEnvVars(t *testing.T) {
	t.Setenv("TEST_STDIO_ENV_REFERENCE_SET", "set-value")

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"TEST_STDIO_ENV_REFERENCE_SET"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.NoError(t, err)
}

func TestCreateSTDIOConnectionRequiresReferencedEnvVars(t *testing.T) {
	t.Setenv("TEST_STDIO_ENV_REFERENCE_MISSING", "")

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"TEST_STDIO_ENV_REFERENCE_MISSING"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "environment variable TEST_STDIO_ENV_REFERENCE_MISSING is not set")
}

func TestCreateSTDIOConnectionRejectsEmptyEnvAssignmentName(t *testing.T) {
	t.Parallel()

	config := &schemas.MCPClientConfig{
		Name:           "test-stdio-client",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			Command: "echo",
			Envs:    []string{"=inline-value"},
		},
	}

	_, _, err := (&MCPManager{}).createSTDIOConnection(context.Background(), config, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "environment variable name is empty")
}

// startedStdioClient spawns command/args via transport.NewStdio and starts
// it, returning a ready-to-close *client.Client. Mirrors what
// connectToMCPClient does around createSTDIOConnection, minus the retry loop.
func startedStdioClient(t *testing.T, command string, args ...string) *client.Client {
	t.Helper()

	stdioTransport := transport.NewStdio(command, nil, args...)
	cli := client.NewClient(stdioTransport)
	require.NoError(t, cli.Start(context.Background()))
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func TestDrainStderrPreventsBlockingOnFullPipeBuffer(t *testing.T) {
	t.Parallel()

	t.Run("without_drain_blocks", func(t *testing.T) {
		t.Parallel()
		sentinel := filepath.Join(t.TempDir(), "done")

		// Write 200KB to stderr (past typical 64KB pipe buffer), then sleep
		// for 3 seconds before touching sentinel. If the pipe is not drained,
		// the subprocess blocks on the write and never reaches sleep/sentinel.
		cmd := fmt.Sprintf(`yes boom | head -c 200000 >&2; sleep 2; touch %q`, sentinel)
		cli := startedStdioClient(t, "sh", "-c", cmd)
		// Intentionally do NOT call drainStderr here.
		_ = cli

		// Give time for the pipe to fill and the subprocess to block.
		time.Sleep(500 * time.Millisecond)

		// The sentinel should not exist because the subprocess is still
		// blocked on the stderr write. If we reach this point without
		// finding the sentinel, we've proven the blocking behavior.
		_, err := os.Stat(sentinel)
		require.Error(t, err, "expected sentinel to NOT exist (subprocess should be blocked), but it was found")
	})

	t.Run("with_drain_unblocks", func(t *testing.T) {
		t.Parallel()
		sentinel := filepath.Join(t.TempDir(), "done")
		cmd := fmt.Sprintf(`yes boom | head -c 200000 >&2; touch %q`, sentinel)

		cli := startedStdioClient(t, "sh", "-c", cmd)
		drainStderr(cli, &schemas.MCPClientConfig{
			ConnectionType: schemas.MCPConnectionTypeSTDIO,
			StdioConfig:    &schemas.MCPStdioConfig{}})

		require.Eventually(t, func() bool {
			_, err := os.Stat(sentinel)
			return err == nil
		}, 5*time.Second, 10*time.Millisecond,
			"subprocess did not finish writing stderr; pipe likely not drained")
	})
}

func TestDrainStderrForwardsLinesToHandler(t *testing.T) {
	t.Parallel()

	cli := startedStdioClient(t, "sh", "-c", `echo first >&2; echo second >&2`)

	var (
		mu    sync.Mutex
		lines []string
	)
	config := &schemas.MCPClientConfig{
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			StderrHandler: func(line string) {
				mu.Lock()
				defer mu.Unlock()
				lines = append(lines, line)
			},
		},
	}
	drainStderr(cli, config)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(lines) == 2
	}, 5*time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"first", "second"}, lines)
}

func TestDrainStderrIgnoresNonSTDIOConfig(t *testing.T) {
	t.Parallel()

	cli := startedStdioClient(t, "sh", "-c", `echo unread >&2`)

	// Simulates an HTTP/SSE config, whose StdioConfig is always nil. Must not
	// panic despite the stdio transport actually being present here.
	config := &schemas.MCPClientConfig{ConnectionType: schemas.MCPConnectionTypeHTTP}
	require.NotPanics(t, func() { drainStderr(cli, config) })
}
