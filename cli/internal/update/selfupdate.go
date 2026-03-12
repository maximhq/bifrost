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
	"time"
)

// RunSelfUpdate downloads and replaces the current binary with the latest version.
func RunSelfUpdate(currentVersion string) error {
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

	// Download new binary to temp file next to current executable
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(execPath), ".bifrost-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	// Download binary (use a generous timeout for large binaries)
	downloadClient := &http.Client{Timeout: 5 * time.Minute}
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
	writer := io.MultiWriter(tmpFile, hasher)

	if _, err := io.Copy(writer, resp.Body); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write binary: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("sync binary: %w", err)
	}
	tmpFile.Close()

	actualHash := hex.EncodeToString(hasher.Sum(nil))

	// Verify checksum (mandatory — refuse to install unverified binaries)
	expectedHash, err := fetchChecksum(checksumURL)
	if err != nil {
		return fmt.Errorf("checksum verification failed: %w", err)
	}
	if actualHash != expectedHash {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actualHash)
	}
	fmt.Println("Checksum verified.")

	// Preserve permissions from old binary
	info, err := os.Stat(execPath)
	if err != nil {
		return fmt.Errorf("stat current binary: %w", err)
	}
	if err := os.Chmod(tmpPath, info.Mode()); err != nil {
		return fmt.Errorf("set permissions: %w", err)
	}

	// Atomic replace: rename new over old
	if err := atomicReplace(execPath, tmpPath); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}

	fmt.Printf("Updated bifrost from %s to %s. Please restart.\n", currentVersion, latest)
	return nil
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
