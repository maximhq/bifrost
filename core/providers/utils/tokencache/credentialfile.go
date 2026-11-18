package tokencache

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// AllowedCredentialPath returns the real path of a credential file once it is known to sit
// under an allowed credential directory. The path is checked before it is resolved, so a
// refusal says nothing about whether a file exists elsewhere, and again after, so a symlink
// cannot lead out of the directory; the symlinks Kubernetes rotates a projected token through
// stay inside its mount. A refusal wraps ErrCredentialPathNotAllowed.
func AllowedCredentialPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s must be an absolute path", path)
	}
	dirs := DefaultCredentialDirs
	if raw := os.Getenv(CredentialDirsEnv); strings.TrimSpace(raw) != "" {
		dirs = strings.Split(raw, ",")
	}
	// Each directory as written and with its symlinks resolved, so both the configured path and
	// the real path of a file under it match (/var/run is a link to /run on most distributions).
	roots := make([]string, 0, 2*len(dirs))
	for _, dir := range dirs {
		if dir = strings.TrimSpace(dir); !filepath.IsAbs(dir) {
			continue
		}
		roots = append(roots, filepath.Clean(dir))
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			roots = append(roots, real)
		}
	}
	within := func(p string) bool {
		for _, root := range roots {
			if rel, err := filepath.Rel(root, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	refuse := fmt.Errorf("%s is %w; set %s to the directories to read from, comma separated (default %s)",
		path, ErrCredentialPathNotAllowed, CredentialDirsEnv, strings.Join(DefaultCredentialDirs, ","))
	if !within(filepath.Clean(path)) {
		return "", refuse
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if !within(resolved) {
		return "", refuse
	}
	return resolved, nil
}

// ReadCredentialFile reads a credential file from an allowed directory (AllowedCredentialPath),
// trimmed, refusing an empty file and one larger than maxBytes.
func ReadCredentialFile(path string, maxBytes int) (string, error) {
	resolved, err := AllowedCredentialPath(path)
	if err != nil {
		return "", err
	}
	// Only a regular file: opening a FIFO or a device could block or read without end.
	if fi, err := os.Stat(resolved); err != nil {
		return "", err
	} else if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(resolved)
	if err != nil {
		return "", err
	}
	defer f.Close()
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
