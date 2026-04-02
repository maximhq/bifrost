package certinstall

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type windowsInstaller struct{}

func newPlatformInstaller() Installer {
	return &windowsInstaller{}
}

// Install adds the CA certificate to the Windows Root certificate store.
// Triggers a UAC elevation dialog.
func (w *windowsInstaller) Install(certPEM []byte) error {
	path, err := WriteTempCert(certPEM)
	if err != nil {
		return err
	}
	defer os.Remove(path)

	cmd := exec.Command("certutil", "-addstore", "-f", "Root", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("certutil -addstore failed: %w: %s", err, string(out))
	}
	return nil
}

// IsInstalled checks if the CA certificate is in the Windows Root store.
func (w *windowsInstaller) IsInstalled(certPEM []byte) (bool, error) {
	fingerprint, err := CertFingerprint(certPEM)
	if err != nil {
		return false, err
	}

	// List certs in the Root store and search for our fingerprint
	cmd := exec.Command("certutil", "-store", "Root")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, nil
	}

	// certutil outputs the hash with spaces, so normalize
	normalized := strings.ReplaceAll(fingerprint, " ", "")
	return strings.Contains(strings.ReplaceAll(string(out), " ", ""), normalized), nil
}

// Uninstall removes the CA certificate from the Windows Root store.
func (w *windowsInstaller) Uninstall(certPEM []byte) error {
	fingerprint, err := CertFingerprint(certPEM)
	if err != nil {
		return err
	}

	cmd := exec.Command("certutil", "-delstore", "Root", fingerprint)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("certutil -delstore failed: %w: %s", err, string(out))
	}
	return nil
}
