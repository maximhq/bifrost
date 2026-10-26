package mcp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/cli/internal/harness"
)

// fakeClaudeScript stands in for the claude CLI: it appends each argument on its own line to
// $FAKE_CLAUDE_ARGS, then either sleeps for $FAKE_CLAUDE_SLEEP or exits with $FAKE_CLAUDE_EXIT.
const fakeClaudeScript = `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a" >> "$FAKE_CLAUDE_ARGS"; done
if [ -n "$FAKE_CLAUDE_SLEEP" ]; then exec sleep "$FAKE_CLAUDE_SLEEP"; fi
exit "${FAKE_CLAUDE_EXIT:-0}"
`

// TestAttachBestEffort runs each case of registering the Bifrost MCP server with an agent CLI.
func TestAttachBestEffort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude CLI is a shell script")
	}
	t.Run("CLI-01 attach without a VK", testAttachWithoutVK)
	t.Run("CLI-02 attach with a VK", testAttachWithVK)
	t.Run("CLI-03 VK whitespace", testAttachVKWhitespace)
	t.Run("CLI-04 claude fails, no VK", testAttachFailsWithoutVK)
	t.Run("CLI-05 claude fails, with VK", testAttachFailsWithVK)
	t.Run("CLI-06 claude hangs", testAttachHangs)
	t.Run("CLI-07 agent without MCP auto-attach", testAttachUnsupportedAgents)
	t.Run("CLI-08 URL building", testAttachURLBuilding)
}

// fakeClaude puts a fake claude first on PATH for the test, exiting with exitCode, or sleeping for
// sleep when it is not empty, and returns the file its arguments are recorded in.
func fakeClaude(t *testing.T, exitCode string, sleep string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(fakeClaudeScript), 0o755); err != nil {
		t.Fatalf("write the fake claude: %v", err)
	}
	argsFile := filepath.Join(dir, "args")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_CLAUDE_ARGS", argsFile)
	t.Setenv("FAKE_CLAUDE_EXIT", exitCode)
	t.Setenv("FAKE_CLAUDE_SLEEP", sleep)
	return argsFile
}

// recordedArgs returns the arguments the fake claude was run with, or nil if it never ran.
func recordedArgs(t *testing.T, argsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read the recorded arguments: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// attach runs AttachBestEffort for the harness called id and returns what it wrote to stdout and stderr.
func attach(t *testing.T, id, baseURL, vk string) (string, string) {
	t.Helper()
	h, ok := harness.Get(id)
	if !ok {
		t.Fatalf("no harness %q", id)
	}
	var stdout, stderr bytes.Buffer
	AttachBestEffort(context.Background(), &stdout, &stderr, h, baseURL, vk)
	return stdout.String(), stderr.String()
}

// addJSONPayload checks the fake claude ran "mcp add-json bifrost <payload>" and returns the payload
// decoded.
func addJSONPayload(t *testing.T, args []string) map[string]any {
	t.Helper()
	if len(args) != 4 || args[0] != "mcp" || args[1] != "add-json" || args[2] != "bifrost" {
		t.Fatalf("claude ran with %q, want mcp add-json bifrost <payload>", args)
	}
	var payload map[string]any
	if err := sonic.Unmarshal([]byte(args[3]), &payload); err != nil {
		t.Fatalf("payload %q does not parse: %v", args[3], err)
	}
	return payload
}

// testAttachWithoutVK: with no virtual key, claude is asked to add the HTTP server by URL, and the
// success line is printed.
func testAttachWithoutVK(t *testing.T) {
	argsFile := fakeClaude(t, "0", "")
	stdout, stderr := attach(t, "claude", "http://h:8080", "")
	want := []string{"mcp", "add", "--transport", "http", "bifrost", "http://h:8080/mcp"}
	if got := recordedArgs(t, argsFile); !reflect.DeepEqual(got, want) {
		t.Fatalf("claude ran with %q, want %q", got, want)
	}
	if stdout != "MCP: attached Claude MCP server 'bifrost'.\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

// testAttachWithVK: with a virtual key, claude gets an add-json payload carrying it as a Bearer
// header.
func testAttachWithVK(t *testing.T) {
	argsFile := fakeClaude(t, "0", "")
	stdout, stderr := attach(t, "claude", "http://h:8080", "sk-bf-x")
	payload := addJSONPayload(t, recordedArgs(t, argsFile))
	want := map[string]any{"type": "http", "url": "http://h:8080/mcp", "headers": map[string]any{"Authorization": "Bearer sk-bf-x"}}
	if !reflect.DeepEqual(payload, want) {
		t.Fatalf("payload = %v, want %v", payload, want)
	}
	if !strings.HasSuffix(stdout, "with virtual key.\n") {
		t.Fatalf("stdout = %q, want it to end \"with virtual key.\"", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
}

// testAttachVKWhitespace: a virtual key is trimmed before it goes into the header, and one that is
// only whitespace counts as no key.
func testAttachVKWhitespace(t *testing.T) {
	argsFile := fakeClaude(t, "0", "")
	attach(t, "claude", "http://h:8080", "  sk-bf-x  ")
	headers, _ := addJSONPayload(t, recordedArgs(t, argsFile))["headers"].(map[string]any)
	if got := headers["Authorization"]; got != "Bearer sk-bf-x" {
		t.Fatalf("Authorization = %q, want \"Bearer sk-bf-x\"", got)
	}

	argsFile = fakeClaude(t, "0", "")
	attach(t, "claude", "http://h:8080", "   ")
	want := []string{"mcp", "add", "--transport", "http", "bifrost", "http://h:8080/mcp"}
	if got := recordedArgs(t, argsFile); !reflect.DeepEqual(got, want) {
		t.Fatalf("a blank VK ran claude with %q, want the mcp add path %q", got, want)
	}
}

// testAttachFailsWithoutVK: when claude fails, the warning gives the command to run by hand and no
// success line is printed.
func testAttachFailsWithoutVK(t *testing.T) {
	fakeClaude(t, "1", "")
	stdout, stderr := attach(t, "claude", "http://h:8080", "")
	if stderr != "warning: auto MCP add failed. Run: claude mcp add --transport http bifrost http://h:8080/mcp\n" {
		t.Fatalf("stderr = %q", stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want no success line", stdout)
	}
}

// testAttachFailsWithVK: when claude fails with a virtual key, the warning names the add-json
// command without ever printing the key.
func testAttachFailsWithVK(t *testing.T) {
	fakeClaude(t, "1", "")
	stdout, stderr := attach(t, "claude", "http://h:8080", "sk-bf-x")
	if stderr != "warning: auto MCP add failed. Run: claude mcp add-json bifrost '<payload>' (with your Authorization header)\n" {
		t.Fatalf("stderr = %q", stderr)
	}
	if strings.Contains(stderr, "sk-bf-x") {
		t.Fatalf("stderr contains the virtual key: %q", stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want no success line", stdout)
	}
}

// testAttachHangs: a claude that never returns is given up on after about 10s, with the failure
// warning.
func testAttachHangs(t *testing.T) {
	fakeClaude(t, "0", "30")
	start := time.Now()
	stdout, stderr := attach(t, "claude", "http://h:8080", "")
	took := time.Since(start)
	if took < 9*time.Second || took > 15*time.Second {
		t.Fatalf("returned after %s, want about 10s", took)
	}
	if !strings.HasPrefix(stderr, "warning: auto MCP add failed.") {
		t.Fatalf("stderr = %q, want the failure warning", stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want no success line", stdout)
	}
}

// testAttachUnsupportedAgents: agents with no MCP auto-attach get the server URL to add themselves,
// and claude is never run.
func testAttachUnsupportedAgents(t *testing.T) {
	for _, id := range []string{"codex", "gemini", "opencode"} {
		argsFile := fakeClaude(t, "0", "")
		h, _ := harness.Get(id)
		stdout, stderr := attach(t, id, "http://h:8080", "sk-bf-x")
		if want := "MCP: " + h.Label + " has no native auto-attach yet. Use server URL: http://h:8080/mcp\n"; stdout != want {
			t.Fatalf("%s: stdout = %q, want %q", id, stdout, want)
		}
		if stderr != "" {
			t.Fatalf("%s: stderr = %q, want empty", id, stderr)
		}
		if args := recordedArgs(t, argsFile); args != nil {
			t.Fatalf("%s: claude ran with %q, want it never run", id, args)
		}
	}
}

// testAttachURLBuilding: a base URL that cannot be parsed is reported without running claude, and a
// base path is kept in the MCP URL.
func testAttachURLBuilding(t *testing.T) {
	argsFile := fakeClaude(t, "0", "")
	stdout, stderr := attach(t, "claude", "not-a-url", "")
	if stderr != "warning: invalid MCP URL: invalid base url \"not-a-url\"\n" {
		t.Fatalf("stderr = %q", stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if args := recordedArgs(t, argsFile); args != nil {
		t.Fatalf("claude ran with %q, want it never run", args)
	}

	argsFile = fakeClaude(t, "0", "")
	attach(t, "claude", "http://h:8080/gw/", "")
	if args := recordedArgs(t, argsFile); len(args) == 0 || args[len(args)-1] != "http://h:8080/gw/mcp" {
		t.Fatalf("claude ran with %q, want the URL http://h:8080/gw/mcp", args)
	}
}
