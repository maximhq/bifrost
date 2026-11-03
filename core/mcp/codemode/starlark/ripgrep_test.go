//go:build !tinygo && !wasm

package starlark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// ripgrepTestMode creates an isolated search root and a Code Mode instance backed by the real rg executable.
func ripgrepTestMode(t *testing.T) (*StarlarkCodeMode, string) {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is required")
	}
	root := t.TempDir()
	mode := NewStarlarkCodeMode(&mcp.CodeModeConfig{RipgrepRoot: &root}, nil)
	mode.SetDependencies(&mcp.CodeModeDependencies{ClientManager: &testClientManager{}})
	return mode, root
}

// writeRipgrepFile creates a private text or binary fixture for a search test.
func writeRipgrepFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// runRipgrep executes a script with the same entry point used by executeToolCode.
func runRipgrep(mode *StarlarkCodeMode, code string) ExecutionResult {
	return mode.executeCode(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), code)
}

// TestRipgrep checks structured results, regex behavior, argument validation, and call limits.
func TestRipgrep(t *testing.T) {
	mode, root := ripgrepTestMode(t)
	writeRipgrepFile(t, filepath.Join(root, "a.txt"), "first\nTimeout α\ntimeout β\n--version\n$(touch should-not-exist)\n")
	if err := os.Mkdir(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	writeRipgrepFile(t, filepath.Join(root, "nested", "b.txt"), "timeout γ\n")
	for _, tc := range []struct {
		name, code string
		want       interface{}
	}{
		{"regex", `r = ripgrep("Timeout.*α", path="a.txt")
result = [len(r["matches"]), r["matches"][0]["line"], r["matches"][0]["text"], r["truncated"]]`, []interface{}{int64(1), int64(2), "Timeout α", false}},
		{"recursive case insensitive", `r = ripgrep("timeout", ignore_case=True)
result = [len(r["matches"]), r["matches"][0]["path"], r["matches"][2]["path"]]`, []interface{}{int64(3), "a.txt", "nested/b.txt"}},
		{"no matches", `r = ripgrep("absent")
result = [len(r["matches"]), r["truncated"]]`, []interface{}{int64(0), false}},
		{"truncated", `r = ripgrep("timeout", max_results=1)
result = [len(r["matches"]), r["truncated"]]`, []interface{}{int64(1), true}},
		{"exact limit", `r = ripgrep("Timeout", max_results=1)
result = [len(r["matches"]), r["truncated"]]`, []interface{}{int64(1), false}},
		{"option injection", `result = ripgrep("--version", path="a.txt")["matches"][0]["text"]`, "--version"},
		{"shell pattern", `result = ripgrep("\\$\\(touch", path="a.txt")["matches"][0]["line"]`, int64(5)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runRipgrep(mode, tc.code)
			if r.Errors != nil {
				t.Fatal(r.Errors.Message)
			}
			if !reflect.DeepEqual(r.Result, tc.want) {
				t.Fatalf("got %#v want %#v", r.Result, tc.want)
			}
		})
	}
	for _, tc := range []struct{ code, want string }{
		{`result = ripgrep("x", path="../secret")`, "relative"},
		{`result = ripgrep("x", path="/etc/passwd")`, "relative"},
		{`result = ripgrep("x", path="a/../../secret")`, "relative"},
		{`result = ripgrep("x", path="..\\secret")`, "relative"},
		{`result = ripgrep("[")`, "ripgrep failed"},
		{`result = ripgrep("")`, "pattern"},
		{`result = ripgrep("x", max_results=0)`, "max_results"},
		{`result = ripgrep("x", max_results=101)`, "max_results"},
		{`result = ripgrep("x", path="missing")`, "no such file"},
		{`for i in range(5):
    ripgrep("absent")`, "call limit"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			r := runRipgrep(mode, tc.code)
			if r.Errors == nil || !strings.Contains(r.Errors.Message, tc.want) {
				t.Fatalf("want %q got %#v", tc.want, r.Errors)
			}
		})
	}
}

// TestRipgrepConfinementAndLimits rejects root escapes and bounds scanned bytes, files, and output.
func TestRipgrepConfinementAndLimits(t *testing.T) {
	t.Run("symlink escape", func(t *testing.T) {
		mode, root := ripgrepTestMode(t)
		outside := t.TempDir()
		writeRipgrepFile(t, filepath.Join(outside, "secret"), "secret")
		if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
			t.Skip(err)
		}
		if r := runRipgrep(mode, `result = ripgrep("secret", path="escape/secret")`); r.Errors == nil {
			t.Fatal("escape allowed")
		}
		r := runRipgrep(mode, `result = len(ripgrep("secret")["matches"])`)
		if r.Errors != nil || r.Result != int64(0) {
			t.Fatalf("leaked: %#v", r)
		}
	})
	for _, tc := range []struct {
		name        string
		size, count int
		want        string
	}{
		{"large file", ripgrepMaxFileBytes + 1, 1, "scan byte"},
		{"many files", 1, ripgrepMaxFiles + 1, "file limit"},
		{"total scan", ripgrepMaxFileBytes, 9, "scan byte"},
		{"output", 300000, 1, "output byte"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode, root := ripgrepTestMode(t)
			for i := 0; i < tc.count; i++ {
				writeRipgrepFile(t, filepath.Join(root, fmt.Sprintf("%03d", i)), strings.Repeat("x", tc.size))
			}
			pattern := "absent"
			if tc.name == "output" {
				pattern = "x"
			}
			r := runRipgrep(mode, fmt.Sprintf("result = ripgrep(%q)", pattern))
			if r.Errors == nil || !strings.Contains(r.Errors.Message, tc.want) {
				t.Fatalf("want %q got %#v", tc.want, r.Errors)
			}
		})
	}
	t.Run("binary and invalid UTF8", func(t *testing.T) {
		mode, root := ripgrepTestMode(t)
		writeRipgrepFile(t, filepath.Join(root, "binary"), "secret\x00")
		writeRipgrepFile(t, filepath.Join(root, "invalid"), "secret\xff")
		r := runRipgrep(mode, `result = len(ripgrep("secret")["matches"])`)
		if r.Errors != nil || r.Result != int64(0) {
			t.Fatalf("got result %#v, error %+v", r.Result, r.Errors)
		}
	})
}

// TestRipgrepConfiguration verifies opt-in configuration, updates, and missing-executable errors.
func TestRipgrepConfiguration(t *testing.T) {
	mode, root := ripgrepTestMode(t)
	empty := ""
	mode.UpdateConfig(&mcp.CodeModeConfig{RipgrepRoot: &empty})
	r := runRipgrep(mode, `result = ripgrep("x")`)
	if r.Errors == nil || !strings.Contains(r.Errors.Message, "undefined") {
		t.Fatal("enabled without root")
	}
	mode.UpdateConfig(&mcp.CodeModeConfig{RipgrepRoot: &root})
	mode.UpdateConfig(&mcp.CodeModeConfig{Limits: &schemas.MCPCodeModeLimits{}})
	if r := runRipgrep(mode, `result = len(ripgrep("x")["matches"])`); r.Errors != nil {
		t.Fatal(r.Errors.Message)
	}
	relative := "relative"
	mode.UpdateConfig(&mcp.CodeModeConfig{RipgrepRoot: &relative})
	if *mode.ripgrepRoot.Load() != root {
		t.Fatal("relative root accepted")
	}
	if len(NewStarlarkCodeMode(nil, nil).ripgrepBuiltins()) != 0 {
		t.Fatal("enabled by default")
	}
	t.Setenv("PATH", t.TempDir())
	r = runRipgrep(mode, `result = ripgrep("x")`)
	if r.Errors == nil || !strings.Contains(r.Errors.Message, "requires rg") {
		t.Fatalf("got result %#v, error %+v", r.Result, r.Errors)
	}
}

// TestRipgrepCancellation verifies cancellation before execution and while a child process is running.
func TestRipgrepCancellation(t *testing.T) {
	mode, root := ripgrepTestMode(t)
	writeRipgrepFile(t, filepath.Join(root, "a"), "x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := mode.executeCode(schemas.NewBifrostContext(ctx, schemas.NoDeadline), `result = ripgrep("x")`)
	if r.Errors == nil || !strings.Contains(r.Errors.Message, "cancel") {
		t.Fatalf("got result %#v, error %+v", r.Result, r.Errors)
	}
	if os.PathSeparator == '\\' {
		t.Skip("helper requires POSIX shell")
	}
	bin := t.TempDir()
	writeRipgrepFile(t, filepath.Join(bin, "rg"), "#!/bin/sh\nwhile :; do :; done\n")
	if err := os.Chmod(filepath.Join(bin, "rg"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	parent, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	r = mode.executeCode(schemas.NewBifrostContext(parent, schemas.NoDeadline), `result = ripgrep("x")`)
	if r.Errors == nil || !strings.Contains(r.Errors.Message, "deadline") {
		t.Fatalf("got result %#v, error %+v", r.Result, r.Errors)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("child not promptly reaped")
	}
}

// TestRipgrepEntryAndValueBudgets exercises directory, result, and interpreter budgets and binding precedence.
func TestRipgrepEntryAndValueBudgets(t *testing.T) {
	t.Run("directory entries", func(t *testing.T) {
		mode, root := ripgrepTestMode(t)
		for i := 0; i < ripgrepMaxEntries+1; i++ {
			if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("%03d", i)), 0700); err != nil {
				t.Fatal(err)
			}
		}
		r := runRipgrep(mode, `result = ripgrep("x")`)
		if r.Errors == nil || !strings.Contains(r.Errors.Message, "entry limit") {
			t.Fatalf("got result %#v, error %+v", r.Result, r.Errors)
		}
	})
	t.Run("result bytes", func(t *testing.T) {
		mode, root := ripgrepTestMode(t)
		writeRipgrepFile(t, filepath.Join(root, "a"), strings.Repeat("x", 70<<10))
		r := runRipgrep(mode, `result = ripgrep("^x")`)
		if r.Errors == nil || !strings.Contains(r.Errors.Message, "result byte") {
			t.Fatalf("got result %#v, error %+v", r.Result, r.Errors)
		}
	})
	t.Run("interpreter memory", func(t *testing.T) {
		mode, root := ripgrepTestMode(t)
		writeRipgrepFile(t, filepath.Join(root, "a"), "x")
		mode.UpdateConfig(&mcp.CodeModeConfig{Limits: &schemas.MCPCodeModeLimits{MaxMemoryBytes: 64 << 10}})
		r := runRipgrep(mode, `result = ripgrep("x")`)
		if r.Errors == nil {
			t.Fatal("search bypassed interpreter memory accounting")
		}
	})
	t.Run("server name collision", func(t *testing.T) {
		mode, _ := ripgrepTestMode(t)
		r := runSandbox(context.Background(), schemas.MCPCodeModeLimits{}.WithDefaults(), `result = type(ripgrep)`, map[string][]string{"ripgrep": {"echo"}}, nil, mode.ripgrepBuiltins())
		if r.Errors != nil || r.Result != "struct" {
			t.Fatalf("server binding overwritten: %#v", r)
		}
	})
	t.Run("ignore user rg config", func(t *testing.T) {
		mode, root := ripgrepTestMode(t)
		writeRipgrepFile(t, filepath.Join(root, "a"), "x")
		config := filepath.Join(t.TempDir(), "rgconfig")
		writeRipgrepFile(t, config, "--invalid-rg-option\n")
		t.Setenv("RIPGREP_CONFIG_PATH", config)
		r := runRipgrep(mode, `result = len(ripgrep("x")["matches"])`)
		if r.Errors != nil || r.Result != int64(1) {
			t.Fatalf("rg config affected search: %#v", r)
		}
	})
}

// BenchmarkRipgrep measures complete Starlark search execution, including process startup and result conversion.
func BenchmarkRipgrep(b *testing.B) {
	if _, err := exec.LookPath("rg"); err != nil {
		b.Skip("rg required")
	}
	root := b.TempDir()
	if err := os.WriteFile(filepath.Join(root, "log.txt"), []byte(strings.Repeat("all good\n", 100)+"connection timeout\n"), 0600); err != nil {
		b.Fatal(err)
	}
	mode := NewStarlarkCodeMode(&mcp.CodeModeConfig{RipgrepRoot: &root}, nil)
	mode.SetDependencies(&mcp.CodeModeDependencies{ClientManager: &testClientManager{}})
	b.ReportAllocs()
	for b.Loop() {
		r := runRipgrep(mode, `result = ripgrep("timeout")`)
		if r.Errors != nil {
			b.Fatal(r.Errors.Message)
		}
	}
}

// TestRipgrepFilesystemWorkerProtocol pins the isolated filesystem boundary used by real searches.
func TestRipgrepFilesystemWorkerProtocol(t *testing.T) {
	root := t.TempDir()
	writeRipgrepFile(t, filepath.Join(root, "log.txt"), "timeout\n")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "--bifrost-ripgrep-files-v1")
	request, _ := json.Marshal(map[string]interface{}{"root": root, "path": ".", "max_bytes": 8 << 20})
	cmd.Stdin = bytes.NewReader(request)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("filesystem helper must return a bounded snapshot: %v: %s", err, output)
	}
	var response struct {
		Files []struct {
			Path    string
			Content []byte
		}
		Error string
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != "" || len(response.Files) != 1 || response.Files[0].Path != "log.txt" || string(response.Files[0].Content) != "timeout\n" {
		t.Fatalf("unexpected snapshot: %+v", response)
	}
}

// TestRipgrepToolsManagerConfiguration checks observable searches through manager updates,
// including omitted roots on both a no-op update and an update of another Code Mode field.
func TestRipgrepToolsManagerConfiguration(t *testing.T) {
	mode, original := ripgrepTestMode(t)
	writeRipgrepFile(t, filepath.Join(original, "log"), "original\n")
	replacement := t.TempDir()
	writeRipgrepFile(t, filepath.Join(replacement, "log"), "replacement\nreplacement\n")
	manager := mcp.NewToolsManagerWithCodeMode(nil, &testClientManager{}, nil, mode, nil, defaultLogger)
	empty := ""
	for _, tc := range []struct {
		name     string
		config   *schemas.MCPToolManagerConfig
		want     int64
		disabled bool
	}{
		{"enable", &schemas.MCPToolManagerConfig{CodeModeRipgrepRoot: &original}, 1, false},
		{"omitted root on no-op", &schemas.MCPToolManagerConfig{}, 1, false},
		{"omitted root with binding update", &schemas.MCPToolManagerConfig{CodeModeBindingLevel: schemas.CodeModeBindingLevelTool}, 1, false},
		{"replace root", &schemas.MCPToolManagerConfig{CodeModeRipgrepRoot: &replacement}, 2, false},
		{"disable", &schemas.MCPToolManagerConfig{CodeModeRipgrepRoot: &empty}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager.UpdateConfig(tc.config)
			result := runRipgrep(mode, `result = len(ripgrep("original|replacement")["matches"])`)
			if tc.disabled {
				if result.Errors == nil || !strings.Contains(result.Errors.Message, "undefined") {
					t.Fatalf("not disabled: %+v", result.Errors)
				}
			} else if result.Errors != nil || result.Result != tc.want {
				t.Fatalf("got result %v, error %+v", result.Result, result.Errors)
			}
		})
	}
}

// blockingRipgrepDirectory simulates a ReadDir syscall stalled in a remote filesystem.
type blockingRipgrepDirectory struct{ reader *os.File }

// ReadDir deliberately waits in a real pipe read; only terminating the helper can release it.
func (d blockingRipgrepDirectory) ReadDir(int) ([]os.DirEntry, error) {
	var data [1]byte
	_, err := d.reader.Read(data[:])
	return nil, err
}

// TestRipgrepBlockingFilesystemHelper runs only in a dedicated test child. Blocking
// calls ignore context intentionally, reproducing the limitation of network/FUSE I/O.
func TestRipgrepBlockingFilesystemHelper(t *testing.T) {
	operation := os.Getenv("BIFROST_TEST_BLOCKED_FS")
	if operation == "" {
		return
	}
	if operation == "output" {
		fmt.Print(strings.Repeat("x", 1<<20))
		return
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if err := os.WriteFile(os.Getenv("BIFROST_TEST_FS_READY"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	if operation == "read" {
		_, err = io.ReadFull(reader, make([]byte, 1))
	} else {
		_, err = (blockingRipgrepDirectory{reader}).ReadDir(32)
	}
	runtime.KeepAlive(writer)
	t.Fatalf("blocking filesystem unexpectedly returned: %v", err)
}

// TestRipgrepFilesystemCancellation proves that read and directory stalls cannot
// hold the gateway's execution beyond cancellation and that children are reaped.
func TestRipgrepFilesystemCancellation(t *testing.T) {
	for _, operation := range []string{"read", "readdir"} {
		t.Run(operation, func(t *testing.T) {
			ready := filepath.Join(t.TempDir(), "ready")
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(executable, "-test.run=^TestRipgrepBlockingFilesystemHelper$")
			cmd.Env = append(os.Environ(), "BIFROST_TEST_BLOCKED_FS="+operation, "BIFROST_TEST_FS_READY="+ready)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := runRipgrepFilesystemWorker(ctx, cmd, 1024); done <- err }()
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("helper did not enter blocking operation: %v", err)
				case <-time.After(5 * time.Millisecond):
				}
			}
			started := time.Now()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("got %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("filesystem cancellation blocked the caller")
			}
			if time.Since(started) > time.Second {
				t.Fatal("cancellation was not prompt")
			}
			deadline := time.Now().Add(3 * time.Second)
			for len(ripgrepFilesystemSlots) != 0 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if len(ripgrepFilesystemSlots) != 0 {
				t.Fatal("helper was not reaped; worker slot leaked")
			}
		})
	}
}

// TestRipgrepFilesystemWorkerSaturation checks that unreaped children cannot grow without bound.
func TestRipgrepFilesystemWorkerSaturation(t *testing.T) {
	for i := 0; i < cap(ripgrepFilesystemSlots); i++ {
		ripgrepFilesystemSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(ripgrepFilesystemSlots); i++ {
			<-ripgrepFilesystemSlots
		}
	}()
	cmd := exec.Command("must-not-be-started")
	_, err := runRipgrepFilesystemWorker(context.Background(), cmd, 1024)
	if err == nil || !strings.Contains(err.Error(), "workers busy") || cmd.Process != nil {
		t.Fatalf("saturation was not refused: %v", err)
	}
}

// TestRipgrepFilesystemWorkerOutputLimit checks the pipe limit before snapshot decoding.
func TestRipgrepFilesystemWorkerOutputLimit(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestRipgrepBlockingFilesystemHelper$")
	cmd.Env = append(os.Environ(), "BIFROST_TEST_BLOCKED_FS=output")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = runRipgrepFilesystemWorker(ctx, cmd, 1024)
	if err == nil || !strings.Contains(err.Error(), "output byte limit") {
		t.Fatalf("got %v", err)
	}
}
