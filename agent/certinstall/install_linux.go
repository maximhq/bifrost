package certinstall

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type linuxInstaller struct{}

func newPlatformInstaller() Installer {
	return &linuxInstaller{}
}

const (
	// Debian/Ubuntu path
	debianCertDir = "/usr/local/share/ca-certificates"
	debianUpdate  = "update-ca-certificates"

	// RHEL/Fedora path
	rhelCertDir = "/etc/pki/ca-trust/source/anchors"
	rhelUpdate  = "update-ca-trust"

	certFileName = "bifrost-agent-ca.crt"
)

// Install adds the CA certificate to the Linux system trust store.
// Supports Debian/Ubuntu (ca-certificates) and RHEL/Fedora (ca-trust).
func (l *linuxInstaller) Install(certPEM []byte) error {
	certDir, updateCmd := l.detectDistro()

	// Write cert to the system cert directory
	certPath := filepath.Join(certDir, certFileName)
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return fmt.Errorf("write cert to %s: %w", certPath, err)
	}

	// Update the system trust store
	cmd := exec.Command(updateCmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %w: %s", updateCmd, err, string(out))
	}
	return nil
}

// IsInstalled checks if the CA certificate exists in the system cert directory.
func (l *linuxInstaller) IsInstalled(certPEM []byte) (bool, error) {
	certDir, _ := l.detectDistro()
	certPath := filepath.Join(certDir, certFileName)

	existing, err := os.ReadFile(certPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	// Compare content
	return string(existing) == string(certPEM), nil
}

// Uninstall removes the CA certificate from the Linux system trust store.
func (l *linuxInstaller) Uninstall(certPEM []byte) error {
	certDir, updateCmd := l.detectDistro()
	certPath := filepath.Join(certDir, certFileName)

	if err := os.Remove(certPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove cert: %w", err)
	}

	cmd := exec.Command(updateCmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %w: %s", updateCmd, err, string(out))
	}
	return nil
}

// detectDistro returns the cert directory and update command for the current distro.
func (l *linuxInstaller) detectDistro() (certDir, updateCmd string) {
	// Check for RHEL/Fedora
	if _, err := os.Stat(rhelCertDir); err == nil {
		return rhelCertDir, rhelUpdate
	}

	// Check /etc/os-release for additional hints
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		content := string(data)
		if strings.Contains(content, "fedora") || strings.Contains(content, "rhel") ||
			strings.Contains(content, "centos") || strings.Contains(content, "rocky") {
			return rhelCertDir, rhelUpdate
		}
	}

	// Default to Debian/Ubuntu style
	return debianCertDir, debianUpdate
}
