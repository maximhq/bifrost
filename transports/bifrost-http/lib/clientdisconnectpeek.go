//go:build unix

package lib

import (
	"errors"
	"syscall"
)

// clientDisconnectPeekSupported reports whether this platform can peek the
// client socket, so that a client disconnect before the first byte cancels the
// request. Tests assert the documented behaviour on both kinds of platform.
const clientDisconnectPeekSupported = true

// peekClientSocket looks at the socket without consuming anything. n == 0 with
// no error is the peer's FIN; ECONNRESET is its RST; EAGAIN means the socket is
// open and idle; pending bytes mean a pipelined request is queued, which the
// watcher must leave alone.
func peekClientSocket(raw syscall.RawConn) peekResult {
	result := peekUndetermined
	var buf [1]byte
	ctrlErr := raw.Control(func(fd uintptr) {
		n, _, err := syscall.Recvfrom(int(fd), buf[:], syscall.MSG_PEEK|syscall.MSG_DONTWAIT)
		switch {
		case err == nil && n == 0:
			result = peekClosed
		case err == nil:
			result = peekUndetermined
		case errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR):
			result = peekAlive
		case errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE):
			result = peekClosed
		}
	})
	if ctrlErr != nil {
		return peekUndetermined
	}
	return result
}
