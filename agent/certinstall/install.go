// Package certinstall handles installing the Bifrost CA certificate into
// the operating system's trust store so that TLS MITM certificates are
// trusted by applications.
package certinstall

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Installer provides platform-specific CA certificate trust store operations.
type Installer interface {
	// Install adds the CA certificate to the OS trust store.
	// May require elevated privileges.
	Install(certPEM []byte) error

	// IsInstalled checks if the CA certificate is already in the OS trust store.
	IsInstalled(certPEM []byte) (bool, error)

	// Uninstall removes the CA certificate from the OS trust store.
	Uninstall(certPEM []byte) error
}

// NewInstaller returns a platform-appropriate Installer.
func NewInstaller() Installer {
	return newPlatformInstaller()
}

// CertFingerprint returns the SHA-256 fingerprint of a PEM-encoded certificate.
// Used as a stable identifier for checking if a cert is already installed.
func CertFingerprint(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse certificate: %w", err)
	}
	hash := sha256.Sum256(cert.Raw)
	return fmt.Sprintf("%X", hash), nil
}

// WriteTempCert writes the PEM certificate to a temporary file and returns the path.
// The caller should remove the file when done.
func WriteTempCert(certPEM []byte) (string, error) {
	dir := os.TempDir()
	path := filepath.Join(dir, "bifrost-agent-ca.pem")
	if err := os.WriteFile(path, certPEM, 0644); err != nil {
		return "", fmt.Errorf("write temp cert: %w", err)
	}
	return path, nil
}

// NeedsElevation returns true if CA installation requires elevated privileges
// on the current platform.
func NeedsElevation() bool {
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		return true
	default:
		return false
	}
}
