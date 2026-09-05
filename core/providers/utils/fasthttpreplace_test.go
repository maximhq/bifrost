package utils

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The fork carries the pooled-resource synchronization fix that streaming
// cancellation depends on (see SetupStreamCancellation and
// TestStreamCloseUnderActiveReaderIsSafe). A module that requires upstream
// fasthttp without the replace builds against v1.71.0-v1.73.0, where closing a
// stream under an active reader double-releases the pooled requestStream.
const (
	upstreamFasthttp = "github.com/valyala/fasthttp"
	forkedFasthttp   = "github.com/maximhq/fasthttp"
)

// parseFasthttpDirectives reports whether the module at modPath requires upstream
// fasthttp, and whether it replaces it with the fork.
//
// A replace only counts when Go would apply it to the required version: an
// unversioned old side applies to every version, a version-qualified old side
// applies to that exact version only (https://go.dev/ref/mod#go-mod-file-replace).
// A replace pinned to a version the module does not require leaves the build on
// upstream, which is exactly the drift this guard exists to catch.
//
// go.mod is parsed with `go mod edit -json` rather than pattern-matched: require
// and replace each have a single-line and a parenthesized-block form, and a
// hand-rolled matcher that misses one silently skips the module - which in a
// drift guard means reporting success on exactly the module it was added to
// catch.
func parseFasthttpDirectives(ctx context.Context, modPath string) (requires, replaced bool, err error) {
	out, err := exec.CommandContext(ctx, "go", "mod", "edit", "-json", modPath).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, false, fmt.Errorf("go mod edit -json %s: %w: %s", modPath, err, exitErr.Stderr)
		}
		return false, false, fmt.Errorf("go mod edit -json %s: %w", modPath, err)
	}

	var mod struct {
		Require []struct{ Path, Version string }
		Replace []struct {
			Old struct{ Path, Version string }
			New struct{ Path, Version string }
		}
	}
	if err := json.Unmarshal(out, &mod); err != nil {
		return false, false, fmt.Errorf("parsing %s: %w", modPath, err)
	}

	var requiredVersion string
	for _, r := range mod.Require {
		if r.Path == upstreamFasthttp {
			requires = true
			requiredVersion = r.Version
			break
		}
	}
	for _, r := range mod.Replace {
		if r.Old.Path != upstreamFasthttp || r.New.Path != forkedFasthttp {
			continue
		}
		if r.Old.Version == "" || r.Old.Version == requiredVersion {
			replaced = true
			break
		}
	}
	return requires, replaced, nil
}

// TestParseFasthttpDirectivesCoversEveryGoModForm pins the parser against all four
// shapes go.mod allows. require and replace each have a single-line and a
// parenthesized-block form, and a parser that misses one silently skips the
// module - which in a drift guard means reporting success.
func TestParseFasthttpDirectivesCoversEveryGoModForm(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantRequires bool
		wantReplaced bool
	}{
		{
			name: "single-line require, single-line replace",
			body: "module example.com/a\ngo 1.27.0\n\n" +
				"require github.com/valyala/fasthttp v1.71.0\n\n" +
				"replace github.com/valyala/fasthttp => github.com/maximhq/fasthttp v1.73.1\n",
			wantRequires: true,
			wantReplaced: true,
		},
		{
			name: "require block, single-line replace",
			body: "module example.com/b\ngo 1.27.0\n\n" +
				"require (\n\tgithub.com/valyala/fasthttp v1.71.0\n)\n\n" +
				"replace github.com/valyala/fasthttp => github.com/maximhq/fasthttp v1.73.1\n",
			wantRequires: true,
			wantReplaced: true,
		},
		{
			name: "require block, replace block",
			body: "module example.com/c\ngo 1.27.0\n\n" +
				"require (\n\tgithub.com/valyala/fasthttp v1.71.0\n)\n\n" +
				"replace (\n\tgithub.com/valyala/fasthttp => github.com/maximhq/fasthttp v1.73.1\n)\n",
			wantRequires: true,
			wantReplaced: true,
		},
		{
			// A replace qualified with the same version as the require applies.
			name: "replace qualified with the required upstream version",
			body: "module example.com/f\ngo 1.27.0\n\n" +
				"require github.com/valyala/fasthttp v1.71.0\n\n" +
				"replace github.com/valyala/fasthttp v1.71.0 => github.com/maximhq/fasthttp v1.73.1\n",
			wantRequires: true,
			wantReplaced: true,
		},
		{
			// Go applies a version-qualified replace only to that exact version
			// (https://go.dev/ref/mod#go-mod-file-replace), so a replace pinned to a
			// version the module does not require leaves the build on upstream.
			name: "replace qualified with a different upstream version is not applied",
			body: "module example.com/g\ngo 1.27.0\n\n" +
				"require github.com/valyala/fasthttp v1.71.0\n\n" +
				"replace github.com/valyala/fasthttp v1.70.0 => github.com/maximhq/fasthttp v1.73.1\n",
			wantRequires: true,
			wantReplaced: false,
		},
		{
			// The case the guard exists for: requires the upstream, replaces nothing.
			name: "single-line require, no replace",
			body: "module example.com/d\ngo 1.27.0\n\n" +
				"require github.com/valyala/fasthttp v1.71.0\n",
			wantRequires: true,
			wantReplaced: false,
		},
		{
			name:         "does not require fasthttp at all",
			body:         "module example.com/e\ngo 1.27.0\n",
			wantRequires: false,
			wantReplaced: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			modPath := filepath.Join(t.TempDir(), "go.mod")
			if err := os.WriteFile(modPath, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("writing fixture: %v", err)
			}
			requires, replaced, err := parseFasthttpDirectives(t.Context(), modPath)
			if err != nil {
				t.Fatalf("parsing fixture: %v", err)
			}
			if requires != tc.wantRequires {
				t.Errorf("requires: got %v, want %v", requires, tc.wantRequires)
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

// TestFasthttpReplaceIsConsistentAcrossModules guards the invariant the comment
// above core/go.mod's replace directive documents: Go ignores replace directives
// from non-main modules, so every module in this workspace that requires
// upstream fasthttp has to carry the replace itself. A new module added without
// one compiles fine and fails only under load, so pin it here instead.
//
// Only the presence of the replace is asserted. Disagreeing fork versions are
// already fatal at load time ("conflicting replacements for
// github.com/valyala/fasthttp"), so checking them here would be dead code, and
// pinning the version would just make every deliberate bump edit this file.
func TestFasthttpReplaceIsConsistentAcrossModules(t *testing.T) {
	root := repoRoot(t)

	var replaced, missing []string
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
		requires, hasReplace, parseErr := parseFasthttpDirectives(t.Context(), path)
		if parseErr != nil {
			return parseErr
		}
		if !requires {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if !hasReplace {
			missing = append(missing, rel)
			return nil
		}
		replaced = append(replaced, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walking modules: %v", err)
	}

	if len(replaced) == 0 && len(missing) == 0 {
		t.Fatalf("no go.mod requiring %s found under %s - the guard is not looking where it thinks", upstreamFasthttp, root)
	}
	if len(missing) > 0 {
		t.Errorf("these modules require %s without replacing it with %s, so they build against\n"+
			"upstream fasthttp and lose the pooled-resource fix:\n  %s",
			upstreamFasthttp, forkedFasthttp, strings.Join(missing, "\n  "))
	}
}

// repoRoot walks up from this package to the directory holding go.work, which is
// the workspace root every module in this repo lives under.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.work")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.work found above %q", dir)
		}
		dir = parent
	}
}
