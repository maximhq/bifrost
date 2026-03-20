// Package privilege handles platform-specific privilege escalation for
// operations that require root/admin access (TUN creation, CA installation).
//
// The agent binary itself does not run as root. Instead, it re-executes itself
// with elevated privileges when needed, passing a --privileged-helper flag
// along with the specific operation to perform.
package privilege

import (
	"fmt"
	"os"
	"runtime"
)

// Operation identifies a privileged operation to perform.
type Operation string

const (
	// OpCreateTUN requests TUN device creation.
	OpCreateTUN Operation = "create-tun"

	// OpInstallCert requests CA certificate installation.
	OpInstallCert Operation = "install-cert"

	// OpUninstallCert requests CA certificate removal.
	OpUninstallCert Operation = "uninstall-cert"

	// OpConfigureInterface requests TUN interface configuration.
	OpConfigureInterface Operation = "configure-interface"
)

// IsElevated returns true if the current process has elevated privileges.
func IsElevated() bool {
	return os.Geteuid() == 0
}

// Elevate re-executes the current binary with elevated privileges.
// The args are passed to the new process.
func Elevate(args ...string) error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	return elevate(execPath, args...)
}

// ElevateWithOp re-executes the binary with the --privileged-helper flag
// and the specified operation.
func ElevateWithOp(op Operation, extraArgs ...string) error {
	args := append([]string{"--privileged-helper", "--operation", string(op)}, extraArgs...)
	return Elevate(args...)
}

// Supported returns true if privilege escalation is supported on this platform.
func Supported() bool {
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		return true
	default:
		return false
	}
}
