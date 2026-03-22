package tunnel

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

const (
	tunDefaultMTULinux = 1500
	tunDevice          = "/dev/net/tun"
	ifNameSize         = 16
	ioctlTUNSETIFF     = 0x400454CA
	iffTUN             = 0x0001
	iffNOPI            = 0x1000
)

// ifReq matches struct ifreq for TUNSETIFF ioctl.
type ifReq struct {
	name  [ifNameSize]byte
	flags uint16
	_     [22]byte // padding
}

// createTUN creates a TUN device on Linux via /dev/net/tun.
func createTUN() (*TUNDevice, error) {
	fd, err := syscall.Open(tunDevice, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", tunDevice, err)
	}

	var req ifReq
	copy(req.name[:], "bifrost0")
	req.flags = iffTUN | iffNOPI

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ioctlTUNSETIFF, uintptr(unsafe.Pointer(&req)))
	if errno != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("ioctl(TUNSETIFF): %w", errno)
	}

	name := string(req.name[:])
	// Trim null bytes
	for i, b := range name {
		if b == 0 {
			name = name[:i]
			break
		}
	}

	file := os.NewFile(uintptr(fd), tunDevice)

	return &TUNDevice{
		Name:            name,
		ReadWriteCloser: file,
		MTU:             tunDefaultMTULinux,
	}, nil
}

// configureInterface sets up the TUN interface on Linux using ip commands.
func configureInterface(ifname string) error {
	if err := runCommand("ip", "addr", "add", "10.255.255.1/32", "dev", ifname); err != nil {
		return fmt.Errorf("configure interface: %w", err)
	}
	if err := runCommand("ip", "link", "set", ifname, "mtu", strconv.Itoa(tunDefaultMTULinux)); err != nil {
		return fmt.Errorf("set MTU: %w", err)
	}
	if err := runCommand("ip", "link", "set", ifname, "up"); err != nil {
		return fmt.Errorf("bring up interface: %w", err)
	}
	return nil
}

// addRoute adds a host route for the given IP (IPv4 or IPv6) through the TUN device on Linux.
func addRoute(ifname string, ip net.IP) error {
	if ip.To4() != nil {
		return runCommand("ip", "route", "add", ip.String()+"/32", "dev", ifname)
	}
	return runCommand("ip", "-6", "route", "add", ip.String()+"/128", "dev", ifname)
}

// removeRoute removes a host route on Linux.
func removeRoute(ifname string, ip net.IP) error {
	if ip.To4() != nil {
		return runCommand("ip", "route", "del", ip.String()+"/32", "dev", ifname)
	}
	return runCommand("ip", "-6", "route", "del", ip.String()+"/128", "dev", ifname)
}
