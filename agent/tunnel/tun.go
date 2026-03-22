// Package tunnel manages the TUN device and userspace network stack for
// intercepting AI API traffic.
package tunnel

import (
	"fmt"
	"io"
	"net"
	"os/exec"
	"runtime"
)

// TUNDevice wraps a platform-specific TUN device.
type TUNDevice struct {
	// Name is the OS interface name (e.g. "utun4" on macOS, "bifrost0" on Linux).
	Name string

	// ReadWriter provides raw packet I/O on the TUN device.
	io.ReadWriteCloser

	// MTU is the maximum transmission unit for the device.
	MTU int
}

// CreateTUN creates a new TUN device. Requires elevated privileges.
// Returns the device with its assigned OS interface name.
func CreateTUN() (*TUNDevice, error) {
	return createTUN()
}

// AddRoute adds an OS route directing traffic for the given IP through the TUN device.
func (t *TUNDevice) AddRoute(ip net.IP) error {
	return addRoute(t.Name, ip)
}

// RemoveRoute removes a previously added route.
func (t *TUNDevice) RemoveRoute(ip net.IP) error {
	return removeRoute(t.Name, ip)
}

// ConfigureInterface sets up the TUN interface with an IP address and brings it up.
// The TUN interface uses 198.18.0.0/15 for fake IPs, so we assign 198.18.0.1/15 to it.
func (t *TUNDevice) ConfigureInterface() error {
	return configureInterface(t.Name)
}

// runCommand is a helper to run shell commands for route/interface management.
func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v failed: %w: %s", name, args, err, string(out))
	}
	return nil
}

// Supported reports whether TUN devices are supported on the current platform.
func Supported() bool {
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		return true
	default:
		return false
	}
}
