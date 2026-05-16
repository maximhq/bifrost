package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// RunSelfUpdate downloads and replaces the current binary with the latest version.
func RunSelfUpdate(currentVersion string) error {
	fmt.Println("Checking latest bifrost version...")
	latest, err := fetchLatestVersion()
	if err != nil {
		return fmt.Errorf("check latest version: %w", err)
	}

	if !isNewer(latest, currentVersion) {
		fmt.Printf("Already up to date (%s).\n", currentVersion)
		return nil
	}

	fmt.Printf("Updating bifrost from %s to %s...\n", currentVersion, latest)

	binaryName := "bifrost"
	if runtime.GOOS == "windows" {
		binaryName = "bifrost.exe"
	}
	downloadURL := fmt.Sprintf("%s/bifrost-cli/%s/%s/%s/%s", baseURL, latest, runtime.GOOS, runtime.GOARCH, binaryName)
	checksumURL := downloadURL + ".sha256"
	fmt.Printf("Resolved update artifact: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	targetPath, err := resolveManagedBinaryTarget(binaryName)
	if err != nil {
		return fmt.Errorf("resolve managed binary path: %w", err)
	}
	fmt.Printf("Managed binary target: %s\n", targetPath)

	// Download to the OS temp directory first so partial downloads never touch
	// the managed install location.
	tmpFile, err := os.CreateTemp("", ".bifrost-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	fmt.Printf("Temporary download path: %s\n", tmpPath)

	// Download binary (use a generous timeout for large binaries)
	downloadClient := &http.Client{Timeout: 5 * time.Minute}
	fmt.Printf("Downloading update from %s\n", downloadURL)
	resp, err := downloadClient.Get(downloadURL)
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("download binary: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		tmpFile.Close()
		return fmt.Errorf("download binary: status %d", resp.StatusCode)
	}

	hasher := sha256.New()
	progress := newProgressLogger(resp.ContentLength)
	defer progress.Finish()
	writer := io.MultiWriter(tmpFile, hasher, progress)

	if _, err := io.Copy(writer, resp.Body); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write binary: %w", err)
	}
	progress.Finish()
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("sync binary: %w", err)
	}
	tmpFile.Close()

	actualHash := hex.EncodeToString(hasher.Sum(nil))

	// Verify checksum (mandatory — refuse to install unverified binaries)
	fmt.Printf("Fetching checksum from %s\n", checksumURL)
	expectedHash, err := fetchChecksum(checksumURL)
	if err != nil {
		return fmt.Errorf("checksum verification failed: %w", err)
	}
	if actualHash != expectedHash {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actualHash)
	}
	fmt.Println("Checksum verified.")

	// Preserve permissions from old binary
	fmt.Println("Preserving executable permissions...")
	info, err := os.Stat(targetPath)
	if err != nil {
		return fmt.Errorf("stat target binary: %w", err)
	}
	stagePath, err := stageUpdateBinary(tmpPath, targetPath, info.Mode())
	if err != nil {
		return fmt.Errorf("stage binary: %w", err)
	}
	defer os.Remove(stagePath)
	if err := os.Chmod(stagePath, info.Mode()); err != nil {
		return fmt.Errorf("set permissions: %w", err)
	}

	// Atomic replace: rename new over old
	fmt.Printf("Replacing binary at %s\n", targetPath)
	if err := atomicReplace(targetPath, stagePath); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}

	fmt.Printf("Updated bifrost from %s to %s. Please restart.\n", currentVersion, latest)
	return nil
}

type progressLogger struct {
	total      int64
	written    int64
	lastReport time.Time
	finished   bool
	mu         sync.Mutex
}

func newProgressLogger(total int64) *progressLogger {
	return &progressLogger{
		total:      total,
		lastReport: time.Now(),
	}
}

func (p *progressLogger) Write(data []byte) (int, error) {
	n := len(data)

	p.mu.Lock()
	defer p.mu.Unlock()

	p.written += int64(n)
	now := time.Now()
	if now.Sub(p.lastReport) >= time.Second {
		fmt.Println(p.statusLine())
		p.lastReport = now
	}

	return n, nil
}

func (p *progressLogger) Finish() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.finished {
		return
	}
	p.finished = true
	fmt.Println(p.statusLine())
}

func (p *progressLogger) statusLine() string {
	if p.total > 0 {
		pct := float64(p.written) * 100 / float64(p.total)
		return fmt.Sprintf("Download progress: %.1f%% (%s/%s)", pct, formatBytes(p.written), formatBytes(p.total))
	}
	return fmt.Sprintf("Download progress: %s", formatBytes(p.written))
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for value := n / unit; value >= unit; value /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func resolveManagedBinaryTarget(binaryName string) (string, error) {
	execPath, err := os.Executable()
	if err == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(execPath); resolveErr == nil {
			execPath = resolved
		}
		if isWrapperManagedBinaryPath(execPath, binaryName) {
			return execPath, nil
		}
		// If the running binary has the expected name, update it in place
		// even if it's not under a wrapper-managed path.
		if filepath.Base(execPath) == binaryName {
			return execPath, nil
		}
	}

	return bifrostCLIManagedBinaryPath(binaryName)
}

func bifrostCLIManagedBinaryPath(binaryName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".bifrost", "bin", binaryName), nil
}

func isWrapperManagedBinaryPath(path, binaryName string) bool {
	if path == "" {
		return false
	}

	cleanPath := filepath.Clean(path)
	homePath, err := bifrostCLIManagedBinaryPath(binaryName)
	if err == nil && cleanPath == filepath.Clean(homePath) {
		return true
	}

	cacheRoot, err := wrapperCacheRoot()
	if err != nil {
		return false
	}
	cachePrefix := filepath.Clean(filepath.Join(cacheRoot, "bifrost")) + string(os.PathSeparator)
	if !strings.HasPrefix(cleanPath, cachePrefix) {
		return false
	}

	base := filepath.Base(cleanPath)
	if base == binaryName {
		return true
	}
	return strings.HasPrefix(base, binaryName+"-")
}

func wrapperCacheRoot() (string, error) {
	switch runtime.GOOS {
	case "linux":
		if xdg := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); xdg != "" {
			return xdg, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".cache"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Caches"), nil
	case "windows":
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			return localAppData, nil
		}
		userProfile := strings.TrimSpace(os.Getenv("USERPROFILE"))
		if userProfile == "" {
			return "", fmt.Errorf("userprofile is not set")
		}
		return filepath.Join(userProfile, "AppData", "Local"), nil
	default:
		return "", fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
}

func stageUpdateBinary(downloadPath, targetPath string, mode os.FileMode) (string, error) {
	stageFile, err := os.CreateTemp(filepath.Dir(targetPath), ".bifrost-stage-*")
	if err != nil {
		return "", err
	}
	stagePath := stageFile.Name()

	src, err := os.Open(downloadPath)
	if err != nil {
		stageFile.Close()
		os.Remove(stagePath)
		return "", err
	}
	defer src.Close()

	if _, err := io.Copy(stageFile, src); err != nil {
		stageFile.Close()
		os.Remove(stagePath)
		return "", err
	}
	if err := stageFile.Sync(); err != nil {
		stageFile.Close()
		os.Remove(stagePath)
		return "", err
	}
	if err := stageFile.Close(); err != nil {
		os.Remove(stagePath)
		return "", err
	}
	if err := os.Chmod(stagePath, mode); err != nil {
		os.Remove(stagePath)
		return "", err
	}
	return stagePath, nil
}

func fetchChecksum(url string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", err
	}

	hash := strings.TrimSpace(strings.Split(string(body), " ")[0])
	if hash == "" {
		return "", fmt.Errorf("empty checksum")
	}
	return hash, nil
}
