package network

import "net"

// IsLocalhost reports whether hostname is localhost or a loopback literal.
func IsLocalhost(hostname string) bool {
	return hostname == "localhost" ||
		hostname == "127.0.0.1" ||
		hostname == "::1" ||
		hostname == "0.0.0.0" ||
		hostname == "::"
}

var privateSubnets []*net.IPNet

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
