package network

import (
	"net"
	"net/netip"
)

// IsLocalhost reports whether hostname is localhost or a loopback literal.
func IsLocalhost(hostname string) bool {
	return hostname == "localhost" ||
		hostname == "127.0.0.1" ||
		hostname == "::1" ||
		hostname == "0.0.0.0" ||
		hostname == "::"
}

var privateSubnets []*net.IPNet
var linkLocalSubnet *net.IPNet

func init() {
	for _, cidr := range []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16", // link-local / AWS metadata
		"127.0.0.0/8",    // loopback
	} {
		_, subnet, _ := net.ParseCIDR(cidr)
		privateSubnets = append(privateSubnets, subnet)
	}
	_, linkLocalSubnet, _ = net.ParseCIDR("169.254.0.0/16")
}

// IsLinkLocal reports whether ip is a link-local address.
// These are always blocked regardless of AllowPrivateNetwork — they include
// cloud instance metadata endpoints (169.254.169.254, fe80::) that must
// never be reachable even in private-network deployments.
//
// IPv6 forms that embed an IPv4 address (IPv4-mapped, 6to4, NAT64) are judged
// by the embedded IPv4 as well, as IsPublicIP does, so 169.254.169.254 is
// still link-local when written as 2002:a9fe:a9fe:: or 64:ff9b::a9fe:a9fe.
// The Teredo prefix is reported as link-local outright: its embedded IPv4 is
// obfuscated and it has no legitimate server-to-server use (see the teredo
// var in ssrf.go), so every gate built on this predicate refuses it.
func IsLinkLocal(ip net.IP) bool {
	if ip.To4() != nil {
		return linkLocalSubnet.Contains(ip)
	}
	if ip.IsLinkLocalUnicast() {
		return true
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if teredo.Contains(addr) {
		return true
	}
	if embedded, ok := embeddedIPv4(addr); ok {
		return embedded.IsLinkLocalUnicast()
	}
	return false
}

// IsPrivateIP reports whether ip falls in a private, loopback, or link-local range.
func IsPrivateIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() {
		return true
	}
	for _, subnet := range privateSubnets {
		if subnet.Contains(ip) {
			return true
		}
	}
	// IPv6: loopback, link-local, unique-local (fc00::/7)
	if ip.To4() == nil {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return true
		}
		if len(ip) == 16 && (ip[0]&0xfe) == 0xfc {
			return true
		}
	}
	return false
}
