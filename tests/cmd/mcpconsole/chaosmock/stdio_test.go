package chaosmock_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

// The stdio bridge is thin by design, but "thin" is a claim about state, not
// about framing: it still has to speak newline-delimited JSON-RPC over pipes
// correctly, and a client has to be able to complete a handshake over it. That
// is checked here against the real client, wired to the bridge through
// in-memory pipes so no process spawn is needed.
//
// The kill path is checked separately with a real subprocess, because killing
// a process is the one fault this transport has that cannot be simulated.

// bridgeToClient wires a running bridge to an mcp-go client over pipes and
// returns the client. The bridge stops when the test's context ends.
func bridgeToClient(t *testing.T, fleet *chaosmock.Fleet, server string) (*client.Client, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	// clientWrites carries the client's stdin to the bridge; bridgeWrites
	// carries the bridge's stdout back to the client.
	clientWritesR, clientWritesW := io.Pipe()
	bridgeWritesR, bridgeWritesW := io.Pipe()

	done := make(chan error, 1)
	go func() {
		done <- chaosmock.RunStdioBridge(ctx, chaosmock.StdioBridgeConfig{
			FleetURL:   fleet.URL(),
			ServerName: server,
			In:         clientWritesR,
			Out:        bridgeWritesW,
		})
	}()
	t.Cleanup(func() {
		_ = clientWritesW.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})

	tr := transport.NewIO(bridgeWritesR, clientWritesW, io.NopCloser(strings.NewReader("")))
	c := client.NewClient(tr)
	if err := c.Start(ctx); err != nil {
		t.Fatalf("failed to start stdio client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	var req mcp.InitializeRequest
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "chaosmock-stdio", Version: "1.0.0"}
	if _, err := c.Initialize(ctx, req); err != nil {
		t.Fatalf("initialize over stdio failed: %v", err)
	}
	return c, ctx
}

func TestStdioClientCompletesHandshakeAndListsTools(t *testing.T) {
	fleet := chaosmock.NewFleet()
	srv := fleet.Add("alpha")
	srv.Tools().Set([]chaosmock.Tool{{Name: "alpha_one"}, {Name: "alpha_two"}})
	fleet.Start()
	t.Cleanup(fleet.Stop)

	c, ctx := bridgeToClient(t, fleet, "alpha")

	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list over stdio failed: %v", err)
	}
	if len(res.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(res.Tools))
	}
	if got := srv.Counters().Snapshot().Initializes; got != 1 {
		t.Errorf("expected 1 initialize, got %d", got)
	}
}

func TestStdioToolCallAndMutationReachTheFleet(t *testing.T) {
	fleet := chaosmock.NewFleet()
	srv := fleet.Add("alpha")
	srv.Tools().Set([]chaosmock.Tool{{Name: "echo_tool"}})
	fleet.Start()
	t.Cleanup(fleet.Stop)

	c, ctx := bridgeToClient(t, fleet, "alpha")

	var call mcp.CallToolRequest
	call.Params.Name = "echo_tool"
	call.Params.Arguments = map[string]any{"v": 1}
	if _, err := c.CallTool(ctx, call); err != nil {
		t.Fatalf("tools/call over stdio failed: %v", err)
	}
	if got := srv.Counters().Snapshot().Calls["echo_tool"]; got != 1 {
		t.Errorf("expected the call to reach the fleet, got %d", got)
	}

	// The bridge holds no tool state, so a mutation on the fleet is visible
	// through it immediately. That is the whole point of it being thin.
	srv.Tools().Add(chaosmock.Tool{Name: "added_later"})
	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("tools/list after mutation failed: %v", err)
	}
	if len(res.Tools) != 2 {
		t.Fatalf("expected the mutation to be visible over stdio, got %d tools", len(res.Tools))
	}
}

// TestStdioInjectedStatusBecomesAProtocolError pins the translation this
// transport needs. An HTTP status has no equivalent on a pipe, so it is
// reported as a JSON-RPC error rather than by killing the process: a client
// should see a failed call, not a dead server.
func TestStdioInjectedStatusBecomesAProtocolError(t *testing.T) {
	fleet := chaosmock.NewFleet()
	srv := fleet.Add("alpha")
	srv.Tools().Set([]chaosmock.Tool{{Name: "some_tool"}})
	fleet.Start()
	t.Cleanup(fleet.Stop)

	c, ctx := bridgeToClient(t, fleet, "alpha")

	srv.SetFaults(chaosmock.Faults{Status: 503})
	if _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); err == nil {
		t.Fatalf("expected tools/list to fail while a 503 is injected")
	}

	srv.ClearFaults()
	if _, err := c.ListTools(ctx, mcp.ListToolsRequest{}); err != nil {
		t.Fatalf("the same client must recover once the fault clears: %v", err)
	}
}

func TestStdioBridgeSharesOnlyPrefixedEnvironment(t *testing.T) {
	// The bridge in bridgeToClient runs in this process, so this process's
	// environment is the one it registers.
	t.Setenv("CHAOS_TAG", "t1")
	t.Setenv("MCPCHAOS_SECRET", "must-not-leave")

	fleet := chaosmock.NewFleet()
	srv := fleet.Add("alpha")
	srv.Tools().Set([]chaosmock.Tool{
		{Name: "env_tool", ReplyEnv: "CHAOS_TAG"},
		{Name: "unset_tool", ReplyEnv: "CHAOS_ABSENT"},
	})
	fleet.Start()
	t.Cleanup(fleet.Stop)

	c, ctx := bridgeToClient(t, fleet, "alpha")

	env := srv.StdioEnv()
	if env["CHAOS_TAG"] != "t1" {
		t.Errorf("expected CHAOS_TAG to be registered, got %v", env)
	}
	if _, leaked := env["MCPCHAOS_SECRET"]; leaked {
		t.Errorf("a variable without the prefix must not leave the process, got %v", env)
	}

	call := func(name string) *mcp.CallToolResult {
		t.Helper()
		var req mcp.CallToolRequest
		req.Params.Name = name
		res, err := c.CallTool(ctx, req)
		if err != nil {
			t.Fatalf("tools/call %s failed: %v", name, err)
		}
		return res
	}
	if text, ok := mcp.AsTextContent(call("env_tool").Content[0]); !ok || text.Text != "t1" {
		t.Errorf("expected env_tool to answer t1, got %+v", text)
	}
	if res := call("unset_tool"); !res.IsError {
		t.Errorf("expected an unset variable to answer as a tool error")
	}
}

// TestStdioSubprocessRegistersAndCanBeKilled uses a real process, because the
// fault it covers is the process dying and there is no way to simulate that
// honestly. It is skipped when the binary cannot be built, so a machine
// without a working toolchain does not fail the suite for the wrong reason.
func TestStdioSubprocessRegistersAndCanBeKilled(t *testing.T) {
	bin := buildChaosBinary(t)

	fleet := chaosmock.NewFleet()
	srv := fleet.Add("alpha")
	srv.Tools().Set([]chaosmock.Tool{{Name: "some_tool"}})
	fleet.Start()
	t.Cleanup(fleet.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "-stdio", "-fleet", fleet.URL(), "-server", "alpha")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if _, err := cmd.StdoutPipe(); err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start the bridge process: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// Registration happens at startup, before any protocol traffic, so a
	// scenario can kill a subprocess that has not been spoken to yet.
	if !waitFor(t, 10*time.Second, func() bool { return srv.StdioPID() != 0 }) {
		t.Fatalf("expected the subprocess to register its pid")
	}
	if srv.StdioPID() != cmd.Process.Pid {
		t.Errorf("registered pid %d does not match the spawned process %d", srv.StdioPID(), cmd.Process.Pid)
	}

	if err := srv.KillStdio(); err != nil {
		t.Fatalf("kill failed: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Errorf("expected the killed subprocess to exit with an error")
	}
	// The registration clears on kill, so a later respawn is distinguishable
	// from one that never happened.
	if got := srv.StdioPID(); got != 0 {
		t.Errorf("expected the registration to clear on kill, got %d", got)
	}
	if err := srv.KillStdio(); err == nil {
		t.Errorf("expected a second kill to report that nothing is registered")
	}
}

func buildChaosBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mcpchaos")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/mcpchaos")
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not build the mcpchaos binary, skipping the subprocess test: %v\n%s", err, out)
	}
	return bin
}
