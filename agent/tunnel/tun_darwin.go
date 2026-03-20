package tunnel

import (
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const (
	tunDefaultMTU = 1500
	// macOS utun control socket constants
	utunControl    = "com.apple.net.utun_control"
	utunOptIfname  = 2
	ctliocGInfo    = 0xC0644E03
	maxIfNameSize  = 16
)

// ctlInfo matches struct ctl_info from <sys/kern_control.h>
type ctlInfo struct {
	id   uint32
	name [96]byte
}

// createTUN creates a utun device on macOS using the kernel control socket API.
// This avoids any external dependencies — pure syscall-based implementation.
func createTUN() (*TUNDevice, error) {
	fd, err := syscall.Socket(syscall.AF_SYSTEM, syscall.SOCK_DGRAM, 2) // SYSPROTO_CONTROL
	if err != nil {
		return nil, fmt.Errorf("socket(AF_SYSTEM): %w", err)
	}

	var info ctlInfo
	copy(info.name[:], utunControl)

	// CTLIOCGINFO ioctl to get the control ID
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), ctliocGInfo, uintptr(unsafe.Pointer(&info)))
	if errno != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("ioctl(CTLIOCGINFO): %w", errno)
	}

	// Connect with sc_unit = 0 to let the kernel assign the next available utun number
	addr := &sockaddrCtl{
		scLen:    uint8(unsafe.Sizeof(sockaddrCtl{})),
		scFamily: syscall.AF_SYSTEM,
		ssSysaddr: 2, // AF_SYS_CONTROL
		scID:     info.id,
		scUnit:   0, // 0 = auto-assign
	}

	_, _, errno = syscall.Syscall(syscall.SYS_CONNECT, uintptr(fd), uintptr(unsafe.Pointer(addr)), unsafe.Sizeof(*addr))
	if errno != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("connect(utun): %w", errno)
	}

	// Get the assigned interface name
	ifname := make([]byte, maxIfNameSize)
	ifnameLen := uint32(len(ifname))
	_, _, errno = syscall.Syscall6(
		syscall.SYS_GETSOCKOPT,
		uintptr(fd),
		2,                    // SYSPROTO_CONTROL
		utunOptIfname,
		uintptr(unsafe.Pointer(&ifname[0])),
		uintptr(unsafe.Pointer(&ifnameLen)),
		0,
	)
	if errno != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("getsockopt(UTUN_OPT_IFNAME): %w", errno)
	}
	name := strings.TrimRight(string(ifname[:ifnameLen]), "\x00")

	// Keep the fd in blocking mode. Go's runtime poller handles scheduling
	// via os.NewFile, and non-blocking mode can cause issues with the
	// utun read/write path (EAGAIN errors that os.File doesn't retry).

	return &TUNDevice{
		Name:            name,
		ReadWriteCloser: &utunReadWriter{fd: fd, file: os.NewFile(uintptr(fd), "utun")},
		MTU:             tunDefaultMTU,
	}, nil
}

// sockaddrCtl matches struct sockaddr_ctl
type sockaddrCtl struct {
	scLen     uint8
	scFamily  uint8
	ssSysaddr uint16
	scID      uint32
	scUnit    uint32
	scReserved [5]uint32
}

// utunReadWriter wraps the utun file descriptor.
// macOS utun prepends a 4-byte protocol header (AF_INET = 0x00000002) to each packet.
type utunReadWriter struct {
	fd   int
	file *os.File
}

func (u *utunReadWriter) Read(p []byte) (int, error) {
	// Read into a buffer with space for the 4-byte header
	buf := make([]byte, len(p)+4)
	n, err := u.file.Read(buf)
	if err != nil {
		return 0, err
	}
	if n < 4 {
		return 0, fmt.Errorf("utun read: packet too short (%d bytes)", n)
	}
	// Strip the 4-byte protocol header
	copy(p, buf[4:n])
	return n - 4, nil
}

func (u *utunReadWriter) Write(p []byte) (int, error) {
	// Determine protocol from IP version
	var proto uint32
	if len(p) > 0 && (p[0]>>4) == 4 {
		proto = syscall.AF_INET
	} else {
		proto = syscall.AF_INET6
	}

	// Prepend 4-byte protocol header
	buf := make([]byte, 4+len(p))
	buf[0] = byte(proto >> 24)
	buf[1] = byte(proto >> 16)
	buf[2] = byte(proto >> 8)
	buf[3] = byte(proto)
	copy(buf[4:], p)

	n, err := u.file.Write(buf)
	if err != nil {
		return 0, err
	}
	if n < 4 {
		return 0, fmt.Errorf("utun write: short write")
	}
	return n - 4, nil
}

func (u *utunReadWriter) Close() error {
	return u.file.Close()
}

// configureInterface sets up the utun interface on macOS with both IPv4 and IPv6.
func configureInterface(ifname string) error {
	// Assign IPv4 address (point-to-point link)
	if err := runCommand("ifconfig", ifname, "10.255.255.1", "10.255.255.2", "up"); err != nil {
		return fmt.Errorf("configure interface IPv4: %w", err)
	}
	// Assign IPv6 address for IPv6 route support
	if err := runCommand("ifconfig", ifname, "inet6", "fd00:bf::1/128"); err != nil {
		// IPv6 config failure is non-fatal — log and continue with IPv4 only
		log.Printf("warning: IPv6 configuration failed (IPv4 only): %v", err)
	}
	// Set MTU
	if err := runCommand("ifconfig", ifname, "mtu", strconv.Itoa(tunDefaultMTU)); err != nil {
		return fmt.Errorf("set MTU: %w", err)
	}
	return nil
}

// addRoute adds a host route for the given IP (IPv4 or IPv6) through the TUN interface on macOS.
func addRoute(ifname string, ip net.IP) error {
	if ip.To4() != nil {
		return runCommand("route", "add", "-host", ip.String(), "-interface", ifname)
	}
	// IPv6 route
	return runCommand("route", "add", "-inet6", "-host", ip.String(), "-interface", ifname)
}

// removeRoute removes a host route on macOS.
func removeRoute(ifname string, ip net.IP) error {
	if ip.To4() != nil {
		return runCommand("route", "delete", "-host", ip.String(), "-interface", ifname)
	}
	return runCommand("route", "delete", "-inet6", "-host", ip.String(), "-interface", ifname)
}
