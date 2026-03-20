package tunnel

import (
	"fmt"
	"net"
	"syscall"
	"time"
)

// dialBypassTUN connects to hostname:port via the physical network interface,
// bypassing TUN routes. On Linux, we use SO_BINDTODEVICE to bind to the
// default physical interface.
func DialBypassTUN(hostname string, port int) (net.Conn, error) {
	ifName, err := getDefaultInterfaceName()
	if err != nil {
		return nil, fmt.Errorf("get default interface: %w", err)
	}

	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			var sErr error
			c.Control(func(fd uintptr) {
				sErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, ifName)
			})
			return sErr
		},
	}

	addr := fmt.Sprintf("%s:%d", hostname, port)
	return dialer.Dial("tcp", addr)
}

// getDefaultInterfaceName returns the name of the default route's interface.
func getDefaultInterfaceName() (string, error) {
	// Find the default route interface by checking which interface has the
	// default gateway.
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		// Skip TUN/TAP interfaces
		if len(iface.Name) >= 3 && (iface.Name[:3] == "tun" || iface.Name[:3] == "tap") {
			continue
		}
		if iface.Name == "bifrost0" {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil || len(addrs) == 0 {
			continue
		}
		return iface.Name, nil
	}
	return "", fmt.Errorf("no suitable physical interface found")
}
