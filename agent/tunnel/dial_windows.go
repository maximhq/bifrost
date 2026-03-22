package tunnel

import (
	"fmt"
	"net"
	"time"
)

// dialBypassTUN connects to hostname:port on Windows.
// TODO: Implement interface binding via WFP or similar mechanism.
func DialBypassTUN(hostname string, port int) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
	}
	addr := fmt.Sprintf("%s:%d", hostname, port)
	return dialer.Dial("tcp", addr)
}
