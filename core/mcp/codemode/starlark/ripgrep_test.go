//go:build !tinygo && !wasm

package starlark

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
