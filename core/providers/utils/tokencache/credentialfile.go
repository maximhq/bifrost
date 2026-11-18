package tokencache

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// CredentialDirsEnv lists, comma separated, the directories a workload credential file (an
// identity or subject token) may be read from, replacing DefaultCredentialDirs. It is read from
// the environment only, never from a key, so whoever can edit a key cannot have Bifrost read an
// arbitrary local file and send it out.
const CredentialDirsEnv = "BIFROST_OIDC_ALLOWED_CREDENTIAL_DIRS"

// DefaultCredentialDirs are the standard container credential mounts, where Kubernetes projects
// service account tokens.
var DefaultCredentialDirs = []string{"/var/run/secrets", "/run/secrets"}

// ErrCredentialPathNotAllowed is returned for a credential file outside the allowed directories.
var ErrCredentialPathNotAllowed = errors.New("outside the allowed credential directories")

// allowedRoots returns the allowed credential directories, absolute and cleaned.
func allowedRoots() []string {
	dirs := DefaultCredentialDirs
	if raw := os.Getenv(CredentialDirsEnv); strings.TrimSpace(raw) != "" {
		dirs = strings.Split(raw, ",")
	}
	roots := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if dir = strings.TrimSpace(dir); filepath.IsAbs(dir) {
			roots = append(roots, filepath.Clean(dir))
		}
	}
	return roots
}

func notAllowed(path string) error {
	return fmt.Errorf("%s is %w; set %s to the directories to read from, comma separated (default %s)",
		path, ErrCredentialPathNotAllowed, CredentialDirsEnv, strings.Join(DefaultCredentialDirs, ","))
}

// rootFor returns the allowed directory path sits under, as written, and path relative to it.
func rootFor(path string) (string, string, bool) {
	clean := filepath.Clean(path)
	for _, root := range allowedRoots() {
		if rel, err := filepath.Rel(root, clean); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return root, rel, true
		}
	}
	return "", "", false
}

// AllowedCredentialPath returns the real path of a credential file once it is known to sit
// under an allowed credential directory. The path is checked before it is resolved, so a
// refusal says nothing about whether a file exists elsewhere, and again after, so a symlink
// cannot lead out of the directory; the symlinks Kubernetes rotates a projected token through
// stay inside its mount. A refusal wraps ErrCredentialPathNotAllowed. Readers should use
// ReadCredentialFile, which also opens the file without a check-then-open race.
func AllowedCredentialPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s must be an absolute path", path)
	}
	root, _, ok := rootFor(path)
	if !ok {
		return "", notAllowed(path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	// The root as written and with its own symlinks resolved (/var/run is a link to /run on
	// most distributions).
	realRoot := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		realRoot = r
	}
	if rel, err := filepath.Rel(realRoot, resolved); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", notAllowed(path)
	}
	return resolved, nil
}

// ReadCredentialFile reads a credential file from an allowed directory, trimmed, refusing an
// empty file and one larger than maxBytes. The file is opened through an os.Root on the
// allowed directory, so no path component, symlink included, can lead out of it even if the
// directory changes between the check and the open. The open does not block on a FIFO, and
// the opened descriptor must be a regular file.
func ReadCredentialFile(path string, maxBytes int) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s must be an absolute path", path)
	}
	rootDir, rel, ok := rootFor(path)
	if !ok {
		return "", notAllowed(path)
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if strings.Contains(err.Error(), "path escapes from parent") {
			return "", notAllowed(path)
		}
		return "", err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return "", err
	} else if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxBytes {
		return "", fmt.Errorf("%s exceeds the %d-byte limit", path, maxBytes)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return token, nil
}
