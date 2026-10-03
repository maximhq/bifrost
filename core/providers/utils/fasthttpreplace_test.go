package utils

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Keep every module on upstream fasthttp v1.74.0 without a replacement.
const upstreamFasthttp = "github.com/valyala/fasthttp"
const requiredFasthttpVersion = "v1.74.0"

// parseFasthttpDirectives reports the required version and any upstream replacement.
// go.mod is parsed with `go mod edit -json` rather than pattern-matched: require
// and replace each have a single-line and a parenthesized-block form, and a
// hand-rolled matcher that misses one silently skips the module - which in a
// drift guard means reporting success on exactly the module it was added to
// catch.
func parseFasthttpDirectives(ctx context.Context, modPath string) (version string, replaced bool, err error) {
	out, err := exec.CommandContext(ctx, "go", "mod", "edit", "-json", modPath).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", false, fmt.Errorf("go mod edit -json %s: %w: %s", modPath, err, exitErr.Stderr)
		}
		return "", false, fmt.Errorf("go mod edit -json %s: %w", modPath, err)
	}

	var mod struct {
		Require []struct{ Path, Version string }
		Replace []struct {
			Old struct{ Path, Version string }
			New struct{ Path, Version string }
		}
	}
	if err := json.Unmarshal(out, &mod); err != nil {
		return "", false, fmt.Errorf("parsing %s: %w", modPath, err)
	}

	for _, r := range mod.Require {
		if r.Path == upstreamFasthttp {
			version = r.Version
			break
		}
	}
	for _, r := range mod.Replace {
		if r.Old.Path == upstreamFasthttp {
			replaced = true
			break
		}
	}
	return version, replaced, nil
}

// TestParseFasthttpDirectivesCoversEveryGoModForm pins the parser against all four
// shapes go.mod allows. require and replace each have a single-line and a
// parenthesized-block form, and a parser that misses one silently skips the
// module - which in a drift guard means reporting success.
func TestParseFasthttpDirectivesCoversEveryGoModForm(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantVersion  string
		wantReplaced bool
	}{
		{
			name: "single-line require, single-line replace",
			body: "module example.com/a\ngo 1.27.0\n\n" +
				"require github.com/valyala/fasthttp v1.71.0\n\n" +
				"replace github.com/valyala/fasthttp => github.com/maximhq/fasthttp v1.73.1\n",
			wantVersion:  "v1.71.0",
			wantReplaced: true,
		},
		{
			name: "require block, single-line replace",
			body: "module example.com/b\ngo 1.27.0\n\n" +
				"require (\n\tgithub.com/valyala/fasthttp v1.71.0\n)\n\n" +
				"replace github.com/valyala/fasthttp => github.com/maximhq/fasthttp v1.73.1\n",
			wantVersion:  "v1.71.0",
			wantReplaced: true,
		},
		{
			name: "require block, replace block",
			body: "module example.com/c\ngo 1.27.0\n\n" +
				"require (\n\tgithub.com/valyala/fasthttp v1.71.0\n)\n\n" +
				"replace (\n\tgithub.com/valyala/fasthttp => github.com/maximhq/fasthttp v1.73.1\n)\n",
			wantVersion:  "v1.71.0",
			wantReplaced: true,
		},
		{
			// A replace qualified with the same version as the require applies.
			name: "replace qualified with the required upstream version",
			body: "module example.com/f\ngo 1.27.0\n\n" +
				"require github.com/valyala/fasthttp v1.71.0\n\n" +
				"replace github.com/valyala/fasthttp v1.71.0 => github.com/maximhq/fasthttp v1.73.1\n",
			wantVersion:  "v1.71.0",
			wantReplaced: true,
		},
		{
			// Stale version-qualified replacements must also be removed.
			name: "replace qualified with a different upstream version is still detected",
			body: "module example.com/g\ngo 1.27.0\n\n" +
				"require github.com/valyala/fasthttp v1.71.0\n\n" +
				"replace github.com/valyala/fasthttp v1.70.0 => github.com/maximhq/fasthttp v1.73.1\n",
			wantVersion:  "v1.71.0",
			wantReplaced: true,
		},
		{
			// Upstream v1.74.0 without a replacement is the supported configuration.
			name: "single-line require, no replace",
			body: "module example.com/d\ngo 1.27.0\n\n" +
				"require github.com/valyala/fasthttp v1.74.0\n",
			wantVersion:  "v1.74.0",
			wantReplaced: false,
		},
		{
			name:         "does not require fasthttp at all",
			body:         "module example.com/e\ngo 1.27.0\n",
			wantVersion:  "",
			wantReplaced: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			modPath := filepath.Join(t.TempDir(), "go.mod")
			if err := os.WriteFile(modPath, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("writing fixture: %v", err)
			}
			version, replaced, err := parseFasthttpDirectives(t.Context(), modPath)
			if err != nil {
				t.Fatalf("parsing fixture: %v", err)
			}
			if version != tc.wantVersion {
				t.Errorf("version: got %v, want %v", version, tc.wantVersion)
			}
			if replaced != tc.wantReplaced {
				t.Errorf("replaced: got %v, want %v", replaced, tc.wantReplaced)
			}
		})
	}
}

// TestParseFasthttpDirectivesHonoursContext pins that the go mod edit subprocess
// is bound to the caller's context, so a hung go toolchain cannot wedge the test
// binary past its deadline.
func TestParseFasthttpDirectivesHonoursContext(t *testing.T) {
	modPath := filepath.Join(t.TempDir(), "go.mod")
	if err := os.WriteFile(modPath, []byte("module example.com/h\ngo 1.27.0\n"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := parseFasthttpDirectives(ctx, modPath); err == nil {
		t.Fatal("expected an error from an already-cancelled context, got nil")
	}
}

// TestFasthttpVersionIsConsistentAcrossModules prevents a module from keeping
// an older version or reintroducing a replacement hidden by the local workspace.
func TestFasthttpVersionIsConsistentAcrossModules(t *testing.T) {
	root := repoRoot(t)
	var checked int
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "go.mod" {
			return nil
		}
		version, hasReplace, err := parseFasthttpDirectives(t.Context(), path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if hasReplace {
			t.Errorf("%s replaces %s; use upstream without a replacement", rel, upstreamFasthttp)
		}
		if version != "" {
			checked++
			if version != requiredFasthttpVersion {
				t.Errorf("%s requires %s %s, want %s", rel, upstreamFasthttp, version, requiredFasthttpVersion)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking modules: %v", err)
	}
	if checked == 0 {
		t.Fatalf("no go.mod requiring %s found under %s", upstreamFasthttp, root)
	}
}

func TestRepoRootWithoutGoWorkspace(t *testing.T) {
	root := t.TempDir()
	for _, module := range []string{"core", "framework", "transports"} {
		dir := filepath.Join(root, module)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/"+module+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(filepath.Join(root, "core"))
	got, err := filepath.EvalSymlinks(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("repoRoot = %q, want %q", got, want)
	}
}

// repoRoot finds the repository by its tracked module files. go.work is
// gitignored and need not exist in a fresh checkout or a module-only CI job.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		isRoot := true
		for _, module := range []string{"core", "framework", "transports"} {
			info, statErr := os.Stat(filepath.Join(dir, module, "go.mod"))
			if statErr != nil || info.IsDir() {
				isRoot = false
				break
			}
		}
		if isRoot {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root with core, framework, and transports modules not found")
		}
		dir = parent
	}
}
