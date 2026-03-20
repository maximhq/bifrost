package tunnel

import (
	"fmt"
	"net"
)

// createTUN creates a TUN device on Windows using the Wintun driver.
// TODO: Implement using golang.zx2c4.com/wintun or wireguard-go/tun for Windows.
func createTUN() (*TUNDevice, error) {
	return nil, fmt.Errorf("TUN device creation not yet implemented on Windows")
}

// configureInterface sets up the TUN interface on Windows.
func configureInterface(ifname string) error {
	// Windows uses netsh to configure interfaces:
	// netsh interface ip set address "Bifrost" static 198.18.0.1 255.254.0.0
	return runCommand("netsh", "interface", "ip", "set", "address", ifname, "static", "198.18.0.1", "255.254.0.0")
}

// addRoute adds a host route on Windows.
func addRoute(ifname string, ip net.IP) error {
	return runCommand("route", "ADD", ip.String(), "MASK", "255.255.255.255", "198.18.0.1")
}

// removeRoute removes a host route on Windows.
func removeRoute(ifname string, ip net.IP) error {
	return runCommand("route", "DELETE", ip.String())
}
