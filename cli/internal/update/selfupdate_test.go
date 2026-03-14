package update

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveManagedBinaryTargetFallsBackToCLIInstallLocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	binaryName := "bifrost"
	if runtime.GOOS == "windows" {
		binaryName = "bifrost.exe"
	}

	got, err := bifrostCLIManagedBinaryPath(binaryName)
	if err != nil {
		t.Fatalf("bifrostCLIManagedBinaryPath() error = %v", err)
	}

	want := filepath.Join(home, ".bifrost", "bin", binaryName)
	if got != want {
		t.Fatalf("bifrostCLIManagedBinaryPath() = %q, want %q", got, want)
	}
}

func TestIsWrapperManagedBinaryPathMatchesCLIInstallLocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	binaryName := "bifrost"
	if runtime.GOOS == "windows" {
		binaryName = "bifrost.exe"
	}

	path := filepath.Join(home, ".bifrost", "bin", binaryName)
	if !isWrapperManagedBinaryPath(path, binaryName) {
		t.Fatalf("expected %q to be recognized as wrapper-managed", path)
	}
}

func TestIsWrapperManagedBinaryPathMatchesCacheInstallLocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		localAppData := t.TempDir()
		t.Setenv("LOCALAPPDATA", localAppData)
		path := filepath.Join(localAppData, "bifrost", "v1.2.3", "bin", "bifrost.exe-0")
		if !isWrapperManagedBinaryPath(path, "bifrost.exe") {
			t.Fatalf("expected %q to be recognized as wrapper-managed cache path", path)
		}
		return
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "linux" {
		xdg := filepath.Join(home, ".custom-cache")
		t.Setenv("XDG_CACHE_HOME", xdg)
		path := filepath.Join(xdg, "bifrost", "v1.2.3", "bin", "bifrost-0")
		if !isWrapperManagedBinaryPath(path, "bifrost") {
			t.Fatalf("expected %q to be recognized as wrapper-managed cache path", path)
		}
		return
	}

	path := filepath.Join(home, "Library", "Caches", "bifrost", "v1.2.3", "bin", "bifrost-0")
	if !isWrapperManagedBinaryPath(path, "bifrost") {
		t.Fatalf("expected %q to be recognized as wrapper-managed cache path", path)
	}
}

func TestStageUpdateBinaryCopiesIntoTargetDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	targetDir := filepath.Join(root, ".bifrost", "bin")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}

	downloadPath := filepath.Join(t.TempDir(), "downloaded-bifrost")
	if err := os.WriteFile(downloadPath, []byte("new-binary"), 0o600); err != nil {
		t.Fatalf("write download: %v", err)
	}

	targetPath := filepath.Join(targetDir, "bifrost")
	stagePath, err := stageUpdateBinary(downloadPath, targetPath, 0o755)
	if err != nil {
		t.Fatalf("stageUpdateBinary() error = %v", err)
	}

	if filepath.Dir(stagePath) != targetDir {
		t.Fatalf("stage path dir = %q, want %q", filepath.Dir(stagePath), targetDir)
	}
	if got, err := os.ReadFile(stagePath); err != nil {
		t.Fatalf("read staged file: %v", err)
	} else if string(got) != "new-binary" {
		t.Fatalf("staged file contents = %q, want %q", string(got), "new-binary")
	}
}
