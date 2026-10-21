package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture creates all supported platforms using harmless synthetic binary bytes.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, platform := range platforms {
		name := "bifrost"
		if strings.HasPrefix(platform, "windows/") {
			name += ".exe"
		}
		path := filepath.Join(root, platform, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		body := []byte("synthetic test binary")
		if err := os.WriteFile(path, body, 0644); err != nil {
			t.Fatal(err)
		}
		checksum := fmt.Sprintf("%x  %s\n", sha256.Sum256(body), name)
		if err := os.WriteFile(path+".sha256", []byte(checksum), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestInjectionAndSourceSubstitution preserves the original source and byte checks.
func TestInjectionAndSourceSubstitution(t *testing.T) {
	root := fixture(t)
	sha := strings.Repeat("a", 40)
	for _, mode := range []string{"create", "verify"} {
		if err := process(mode, root, "cli", "1.2.3", sha); err != nil {
			t.Fatal(err)
		}
	}
	if err := process("verify", root, "cli", "1.2.3", strings.Repeat("b", 40)); err == nil {
		t.Fatal("different source accepted")
	}
	extra := filepath.Join(root, "cook.pth")
	if err := os.WriteFile(extra, []byte("harmless fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := process("verify", root, "cli", "1.2.3", sha); err == nil {
		t.Fatal("injection accepted")
	}
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "linux/amd64/bifrost"), []byte("substituted"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := process("verify", root, "cli", "1.2.3", sha); err == nil {
		t.Fatal("binary substitution accepted")
	}
}

// TestNestedManifestsAndSymlinks rejects hidden extras and metadata writes through links.
func TestNestedManifestsAndSymlinks(t *testing.T) {
	for _, name := range []string{"linux/release.json", "version.txt", "release.json"} {
		t.Run(name, func(t *testing.T) {
			root := fixture(t)
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("untouched"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, name)); err != nil {
				t.Fatal(err)
			}
			if err := process("create", root, "cli", "1.2.3", strings.Repeat("a", 40)); err == nil {
				t.Fatal("symlink accepted")
			}
			data, err := os.ReadFile(outside)
			if err != nil || string(data) != "untouched" {
				t.Fatal("symlink target changed")
			}
		})
	}
}
