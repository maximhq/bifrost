package tunnel

import (
	"context"
	"io"
	"log"
	"net"
	"sync"

	"github.com/maximhq/bifrost/agent/config"
	"github.com/maximhq/bifrost/agent/internal/sni"
)

// ConnHandler is a function that handles an intercepted TCP connection.
// It receives the connection, the SNI hostname, and the corresponding domain rule.
type ConnHandler func(conn net.Conn, hostname string, rule *config.DomainRule)

// Router accepts connections from the netstack, peeks at the TLS ClientHello
// to extract the SNI, looks up the domain rule, and dispatches to the handler.
type Router struct {
	stack      *NetStack
	runtime    *config.RuntimeConfig
	handler    ConnHandler
	loggedOnce sync.Map // tracks domains we've already logged passthrough for
}

// NewRouter creates a connection router.
func NewRouter(stack *NetStack, runtime *config.RuntimeConfig, handler ConnHandler) *Router {
	return &Router{
		stack:   stack,
		runtime: runtime,
		handler: handler,
	}
}

// Run starts accepting and routing connections. Blocks until ctx is cancelled.
func (r *Router) Run(ctx context.Context) error {
	for {
		conn, err := r.stack.AcceptTCP(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("accept error: %v", err)
			continue
		}

		go r.handleConn(conn)
	}
}

// handleConn peeks at the first bytes to extract SNI, then dispatches.
func (r *Router) handleConn(conn net.Conn) {
	// Read enough data to parse the TLS ClientHello and extract SNI.
	// TCP (especially via gVisor netstack) may deliver data in small segments,
	// so we read in a loop until we have a complete TLS record.
	peekBuf, err := readTLSClientHello(conn)
	if err != nil {
		log.Printf("failed to read ClientHello: %v", err)
		conn.Close()
		return
	}

	hostname, err := sni.Extract(peekBuf)
	if err != nil {
		log.Printf("SNI extraction failed (%d bytes read): %v", len(peekBuf), err)
		conn.Close()
		return
	}

	// Wrap the connection so subsequent reads see the peeked bytes first
	buffered := &prefixConn{
		Conn:   conn,
		prefix: peekBuf,
	}

	rule := r.runtime.GetDomainRule(hostname)
	if rule == nil {
		// Domain shares an IP with an intercepted domain but isn't one we care about.
		// Pass through directly — connect to the real server and splice bytes.
		log.Printf("passthrough: %s (not intercepted, shared IP)", hostname)
		r.directRelay(buffered, hostname)
		return
	}

	log.Printf("intercepted connection to %s (integration: %s)", hostname, rule.IntegrationPrefix)
	r.handler(buffered, hostname, rule)
}

// directRelay connects to the real server and splices raw TCP bytes in both
// directions. Used for domains that share IPs with intercepted domains but
// shouldn't be proxied (e.g., content.googleapis.com sharing IPs with
// generativelanguage.googleapis.com).
//
// We use dialBypassTUN (platform-specific) to connect via the physical NIC,
// bypassing our TUN routes that would otherwise cause an infinite loop.
func (r *Router) directRelay(clientConn net.Conn, hostname string) {
	defer clientConn.Close()

	serverConn, err := DialBypassTUN(hostname, 443)
	if err != nil {
		// Only log once per domain to avoid spam
		if _, loaded := r.loggedOnce.LoadOrStore(hostname, true); !loaded {
			log.Printf("passthrough: cannot relay %s (shared IP routing conflict): %v", hostname, err)
		}
		return
	}
	defer serverConn.Close()

	// Splice: client ↔ server (raw bytes, TLS passes through untouched)
	done := make(chan struct{}, 1)
	go func() {
		io.Copy(serverConn, clientConn)
		done <- struct{}{}
	}()
	io.Copy(clientConn, serverConn)
	<-done
}

// readTLSClientHello reads from a connection until we have a complete TLS record
// containing the ClientHello. TLS record header is 5 bytes:
// content_type(1) + version(2) + length(2). We read the header first to know
// the full record length, then read until we have the complete record.
func readTLSClientHello(conn net.Conn) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)

	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if len(buf) > 0 {
				return buf, nil // return what we have
			}
			return nil, err
		}

		// Need at least 5 bytes for the TLS record header
		if len(buf) < 5 {
			continue
		}

		// Check if this looks like a TLS handshake record
		if buf[0] != 0x16 {
			// Not a TLS handshake — return what we have (will fail SNI extraction)
			return buf, nil
		}

		// Parse record length from bytes 3-4
		recordLen := int(buf[3])<<8 | int(buf[4])
		totalNeeded := 5 + recordLen

		// Cap at 16KB (max TLS record size) to prevent abuse
		if totalNeeded > 16384 {
			return buf, nil
		}

		// Do we have the full record?
		if len(buf) >= totalNeeded {
			return buf[:totalNeeded], nil
		}
		// Keep reading
	}
}

// prefixConn wraps a net.Conn and prepends buffered data (the peeked TLS ClientHello)
// before reading from the underlying connection.
type prefixConn struct {
	net.Conn
	prefix []byte
	offset int
}

func (p *prefixConn) Read(b []byte) (int, error) {
	if p.offset < len(p.prefix) {
		n := copy(b, p.prefix[p.offset:])
		p.offset += n
		return n, nil
	}
	return p.Conn.Read(b)
}

func (p *prefixConn) WriteTo(w io.Writer) (int64, error) {
	var total int64
	if p.offset < len(p.prefix) {
		n, err := w.Write(p.prefix[p.offset:])
		p.offset += n
		total += int64(n)
		if err != nil {
			return total, err
		}
	}
	if wt, ok := p.Conn.(io.WriterTo); ok {
		n, err := wt.WriteTo(w)
		return total + n, err
	}
	n, err := io.Copy(w, p.Conn)
	return total + n, err
}
