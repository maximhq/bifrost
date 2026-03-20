package certinstall

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type darwinInstaller struct{}

func newPlatformInstaller() Installer {
	return &darwinInstaller{}
}

// Install adds the CA certificate to the macOS System Keychain and marks it
// as trusted for SSL. Requires admin privileges.
func (d *darwinInstaller) Install(certPEM []byte) error {
	path, err := WriteTempCert(certPEM)
	if err != nil {
		return err
	}
	defer os.Remove(path)

	// Step 1: Add the certificate to the System Keychain
	cmd := exec.Command("security", "add-certificates",
		"-k", "/Library/Keychains/System.keychain",
		path,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Ignore "already exists" errors
		if !strings.Contains(string(out), "already exists") {
			fmt.Printf("add-certificates output: %s\n", string(out))
		}
	}

	// Step 2: Set trust to "trustRoot" for SSL policy
	cmd = exec.Command("security", "add-trusted-cert",
		"-d",              // admin cert store
		"-r", "trustRoot", // trust as root CA
		"-p", "ssl",       // for SSL/TLS policy
		"-k", "/Library/Keychains/System.keychain",
		path,
	)
	out, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("security add-trusted-cert failed: %w: %s", err, string(out))
	}
	return nil
}

// IsInstalled checks if the CA certificate is in the macOS System Keychain.
func (d *darwinInstaller) IsInstalled(certPEM []byte) (bool, error) {
	fingerprint, err := CertFingerprint(certPEM)
	if err != nil {
		return false, err
	}

	// Search for certs with "Bifrost" in the name in the System Keychain
	cmd := exec.Command("security", "find-certificate",
		"-a", "-c", "Bifrost",
		"-Z", // show SHA-256 hash
		"/Library/Keychains/System.keychain",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// If no matching cert found, security returns an error
		return false, nil
	}

	return strings.Contains(string(out), fingerprint), nil
}

// Uninstall removes the CA certificate from the macOS System Keychain.
func (d *darwinInstaller) Uninstall(certPEM []byte) error {
	path, err := WriteTempCert(certPEM)
	if err != nil {
		return err
	}
	defer os.Remove(path)

	cmd := exec.Command("security", "remove-trusted-cert",
		"-d", // remove from admin cert store
		path,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("security remove-trusted-cert failed: %w: %s", err, string(out))
	}
	return nil
}
