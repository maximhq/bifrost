package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// requireSentinel polls until path exists, failing with msg if it never
// does. Used to detect whether a subprocess finished writing stderr (i.e.
// wasn't left blocked on a full pipe).
func requireSentinel(t *testing.T, path, msg string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := os.Stat(path)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, msg)
}

// lineCollector is a concurrency-safe StderrHandler sink for assertions.
type lineCollector struct {
	mu    sync.Mutex
	lines []string
}

func (c *lineCollector) handle(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, line)
}

func (c *lineCollector) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.lines)
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

		requireSentinel(t, sentinel, "subprocess did not finish writing stderr; pipe likely not drained")
	})
}

func TestDrainStderrForwardsLinesToHandler(t *testing.T) {
	t.Parallel()

	cli := startedStdioClient(t, "sh", "-c", `echo first >&2; echo second >&2`)

	var collector lineCollector
	config := &schemas.MCPClientConfig{
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig:    &schemas.MCPStdioConfig{StderrHandler: collector.handle},
	}
	drainStderr(cli, config)

	require.Eventually(t, func() bool {
		return len(collector.snapshot()) == 2
	}, 5*time.Second, 10*time.Millisecond)

	require.Equal(t, []string{"first", "second"}, collector.snapshot())
}

func TestDrainStderrIgnoresNonSTDIOConfig(t *testing.T) {
	t.Parallel()

	cli := startedStdioClient(t, "sh", "-c", `echo unread >&2`)

	// Simulates an HTTP/SSE config, whose StdioConfig is always nil. Must not
	// panic despite the stdio transport actually being present here.
	config := &schemas.MCPClientConfig{ConnectionType: schemas.MCPConnectionTypeHTTP}
	require.NotPanics(t, func() { drainStderr(cli, config) })
}

func TestDrainStderrTruncatesOversizedLine(t *testing.T) {
	t.Parallel()
	sentinel := filepath.Join(t.TempDir(), "done")

	// One unbroken 200KB write with no newline, well past maxStderrLineBytes,
	// followed by a trailing newline and a normal line.
	cmd := fmt.Sprintf(`head -c 200000 /dev/zero | tr '\0' 'a' >&2; echo >&2; echo second >&2; touch %q`, sentinel)
	cli := startedStdioClient(t, "sh", "-c", cmd)

	var collector lineCollector
	config := &schemas.MCPClientConfig{
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig:    &schemas.MCPStdioConfig{StderrHandler: collector.handle},
	}
	drainStderr(cli, config)

	requireSentinel(t, sentinel, "subprocess did not finish writing stderr; oversized line likely blocked the reader")

	require.Eventually(t, func() bool {
		return len(collector.snapshot()) == 2
	}, 5*time.Second, 10*time.Millisecond)

	lines := collector.snapshot()
	require.Len(t, lines[0], maxStderrLineBytes, "oversized line should be truncated to maxStderrLineBytes")
	require.Equal(t, "second", lines[1])
}

func TestDrainStderrHandlerBlockDoesNotStallReading(t *testing.T) {
	t.Parallel()
	sentinel := filepath.Join(t.TempDir(), "done")

	// Enough lines to overflow stderrQueueSize even though the handler is
	// stuck on the first one; the subprocess must still finish writing.
	cmd := fmt.Sprintf(`for i in $(seq 1 %d); do echo "line-$i" >&2; done; touch %q`, stderrQueueSize*4, sentinel)
	cli := startedStdioClient(t, "sh", "-c", cmd)

	block := make(chan struct{})
	config := &schemas.MCPClientConfig{
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig: &schemas.MCPStdioConfig{
			StderrHandler: func(line string) {
				<-block // blocks on every call, including the first
			},
		},
	}
	drainStderr(cli, config)
	t.Cleanup(func() { close(block) })

	requireSentinel(t, sentinel, "subprocess did not finish writing stderr; blocked handler stalled the reader")
}
