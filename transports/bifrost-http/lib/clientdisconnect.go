package lib

import (
	"context"
	"crypto/tls"
	"net"
	"syscall"
	"time"
)

// DefaultClientDisconnectPollInterval is how often an in-flight request's
// client socket is peeked for a close while the handler is still waiting on
// core (header wait on the upstream, retry backoff). Requests that finish
// sooner than this never pay for a peek.
const DefaultClientDisconnectPollInterval = 500 * time.Millisecond

// peekResult is what one non-consuming look at the client socket found.
type peekResult int

const (
	peekAlive        peekResult = iota // nothing to read, socket open
	peekClosed                         // peer sent FIN or RST: the client is gone
	peekUndetermined                   // bytes pending (a pipelined request) or an unsupported socket
)

// startClientDisconnectWatcher cancels the request context when the client
// closes its socket before Bifrost has written anything back.
//
// fasthttp's RequestCtx.Done fires only on server shutdown and the reactive
// cancel in the streaming handlers fires only when an SSE write fails, so a
// client that leaves while core is still waiting on a silent upstream or
// sleeping between retries was invisible (issue #7035). The watcher peeks at
// the socket with MSG_PEEK, which consumes nothing, so it cannot interfere
// with fasthttp's own reads once the handler returns. With a TLS edge a
// graceful close sends close_notify bytes first, which reads as pending data;
// only resets are then detected. Behind a reverse proxy detection depends on
// the proxy closing its upstream connection on client abort.
//
// It is a no-op for sockets that cannot be peeked (fasthttputil.PipeConns,
// net.Pipe, non-unix platforms) and stops as soon as the context ends.
func startClientDisconnectWatcher(conn net.Conn, ctx context.Context, cancel context.CancelFunc) {
	raw := rawConnOf(conn)
	if raw == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(DefaultClientDisconnectPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			switch peekClientSocket(raw) {
			case peekAlive:
			case peekClosed:
				cancel()
				return
			default:
				return
			}
		}
	}()
}

// rawConnOf unwraps TLS and returns the socket's raw handle, or nil when the
// connection cannot be peeked.
func rawConnOf(conn net.Conn) syscall.RawConn {
	if conn == nil {
		return nil
	}
	if tlsConn, ok := conn.(*tls.Conn); ok {
		conn = tlsConn.NetConn()
	}
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return nil
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return nil
	}
	return raw
}
