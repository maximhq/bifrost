package tunnel

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// dialBypassTUN connects to hostname:port via the physical network interface,
// bypassing TUN routes. On macOS, we find the default interface index and use
// IP_BOUND_IF to bind the socket to it.
func DialBypassTUN(hostname string, port int) (net.Conn, error) {
	ifIndex, err := getDefaultInterfaceIndex()
	if err != nil {
		return nil, fmt.Errorf("get default interface: %w", err)
	}

	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			var sErr error
			c.Control(func(fd uintptr) {
				// IP_BOUND_IF (25) binds the socket to a specific interface index,
				// bypassing any TUN routes in the routing table.
				sErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, 25, ifIndex)
			})
			return sErr
		},
	}

	addr := fmt.Sprintf("%s:%d", hostname, port)
	return dialer.Dial("tcp", addr)
}

// getDefaultInterfaceIndex returns the interface index of the default route's
// network interface (typically en0 for WiFi or en1 for Ethernet on macOS).
func getDefaultInterfaceIndex() (int, error) {
	// Use "route get default" to find the default interface
	out, err := exec.Command("route", "-n", "get", "default").CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("route get default: %w", err)
	}

	// Parse "interface: en0" from the output
	var ifName string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "interface:") {
			ifName = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
			break
		}
	}
	if ifName == "" {
		return 0, fmt.Errorf("no default interface found")
	}

	// Get the interface index
	iface, err := net.InterfaceByName(ifName)
	if err != nil {
		return 0, fmt.Errorf("interface %s: %w", ifName, err)
	}

	return iface.Index, nil
}
