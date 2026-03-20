package tunnel

import (
	"context"
	"fmt"
	"log"
	"net"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	// channelSize is the buffer size for the channel endpoint between TUN and netstack.
	channelSize = 512

	// nicID is the network interface ID within the gVisor stack.
	nicID = 1
)

// NetStack wraps a gVisor userspace TCP/IP stack connected to a TUN device.
// It reads raw IP packets from the TUN, feeds them into the stack, and makes
// accepted TCP connections available via AcceptTCP.
type NetStack struct {
	tun      *TUNDevice
	stack    *stack.Stack
	ep       *channel.Endpoint
	tcpCh    chan net.Conn
	cancel   context.CancelFunc
	pktCount uint64
}

// NewNetStack creates a userspace TCP/IP stack attached to the given TUN device.
// It sets up TCP and UDP protocol support and begins reading packets from the TUN.
func NewNetStack(tun *TUNDevice) (*NetStack, error) {
	// Create the gVisor stack with IPv4 + IPv6 + TCP + UDP
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})

	// Create a channel endpoint to bridge TUN ↔ netstack
	ep := channel.New(channelSize, uint32(tun.MTU), "")

	// Create a NIC (network interface) backed by the channel endpoint
	if err := s.CreateNIC(nicID, ep); err != nil {
		return nil, fmt.Errorf("create NIC: %s", err)
	}

	// Add default routes for both IPv4 and IPv6 so the stack handles all traffic
	s.SetRouteTable([]tcpip.Route{
		{
			Destination: header.IPv4EmptySubnet,
			NIC:         nicID,
		},
		{
			Destination: header.IPv6EmptySubnet,
			NIC:         nicID,
		},
	})

	// Enable promiscuous mode so the stack accepts packets for any destination IP
	// (since we're routing traffic for multiple fake IPs through this interface)
	if err := s.SetPromiscuousMode(nicID, true); err != nil {
		return nil, fmt.Errorf("set promiscuous mode: %s", err)
	}

	// Enable spoofing so we can send responses from any source IP
	if err := s.SetSpoofing(nicID, true); err != nil {
		return nil, fmt.Errorf("set spoofing: %s", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	ns := &NetStack{
		tun:    tun,
		stack:  s,
		ep:     ep,
		tcpCh:  make(chan net.Conn, 64),
		cancel: cancel,
	}

	// Start the TUN → netstack packet pump
	go ns.readFromTUN(ctx)

	// Start the netstack → TUN packet pump
	go ns.writeToTUN(ctx)

	return ns, nil
}

// ListenTCP sets up a TCP forwarder that captures all incoming TCP connections
// on the given port (typically 443 for HTTPS) and delivers them as net.Conn
// via AcceptTCP.
func (ns *NetStack) ListenTCP(port uint16) error {
	// Use a TCP forwarder to intercept all SYN packets on this port
	fwd := tcp.NewForwarder(ns.stack, 0, 1024, func(r *tcp.ForwarderRequest) {
		var wq waiter.Queue
		ep, err := r.CreateEndpoint(&wq)
		if err != nil {
			r.Complete(true) // send RST
			return
		}
		r.Complete(false)

		conn := gonet.NewTCPConn(&wq, ep)
		ns.tcpCh <- conn
	})

	ns.stack.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)
	return nil
}

// AcceptTCP returns the next intercepted TCP connection.
// It blocks until a connection is available or the context is cancelled.
func (ns *NetStack) AcceptTCP(ctx context.Context) (net.Conn, error) {
	select {
	case conn := <-ns.tcpCh:
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close shuts down the netstack and releases resources.
func (ns *NetStack) Close() error {
	ns.cancel()
	ns.stack.Close()
	return ns.tun.Close()
}

// readFromTUN continuously reads raw IP packets from the TUN device and
// injects them into the gVisor netstack for processing.
func (ns *NetStack) readFromTUN(ctx context.Context) {
	buf := make([]byte, ns.tun.MTU+64) // extra room for headers
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := ns.tun.Read(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("TUN read error: %v", err)
			continue
		}
		if n == 0 {
			continue
		}

		// Log first packet and periodic stats for debugging
		ns.pktCount++
		if ns.pktCount == 1 || ns.pktCount%100 == 0 {
			log.Printf("TUN: received packet #%d (%d bytes)", ns.pktCount, n)
		}

		// Make a copy for the stack (the buffer is reused)
		pkt := make([]byte, n)
		copy(pkt, buf[:n])

		// Detect IP version from the first nibble and inject with correct protocol
		proto := header.IPv4ProtocolNumber
		if len(pkt) > 0 && (pkt[0]>>4) == 6 {
			proto = header.IPv6ProtocolNumber
		}

		pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(pkt),
		})
		ns.ep.InjectInbound(proto, pkb)
		pkb.DecRef()
	}
}

// writeToTUN continuously reads packets from the gVisor netstack (outbound
// responses) and writes them to the TUN device so they reach the original
// application.
func (ns *NetStack) writeToTUN(ctx context.Context) {
	for {
		pkb := ns.ep.ReadContext(ctx)
		if pkb == nil {
			return // context cancelled
		}

		// Serialize the packet into bytes for the TUN device
		view := pkb.ToView()
		data := view.AsSlice()
		if _, err := ns.tun.Write(data); err != nil {
			if ctx.Err() != nil {
				pkb.DecRef()
				return
			}
			log.Printf("TUN write error: %v", err)
		}
		pkb.DecRef()
	}
}
