package tunnel

import (
	"context"
	"log"
	"net"
	"sync"
	"time"

	"github.com/maximhq/bifrost/agent/config"
)

// DomainResolver resolves real IPs for AI provider domains and manages OS routes
// to direct that traffic through the TUN device. No DNS changes are needed —
// apps resolve domains normally via system DNS; we just route the resulting IPs
// through our TUN.
type DomainResolver struct {
	tun     *TUNDevice
	runtime *config.RuntimeConfig

	mu       sync.RWMutex
	// realIPMap maps hostname → resolved real IPs
	realIPMap map[string][]net.IP
	// ipToHost maps real IP string → hostname (reverse lookup for SNI fallback)
	ipToHost map[string]string
	// routedIPs tracks all IPs we've added routes for (for cleanup)
	routedIPs map[string]bool
}

// NewDomainResolver creates a resolver that manages routes for AI domain IPs.
func NewDomainResolver(tun *TUNDevice, runtime *config.RuntimeConfig) *DomainResolver {
	return &DomainResolver{
		tun:       tun,
		runtime:   runtime,
		realIPMap: make(map[string][]net.IP),
		ipToHost:  make(map[string]string),
		routedIPs: make(map[string]bool),
	}
}

// ResolveAndRoute resolves all configured AI domains and adds OS routes for
// their IPs through the TUN device. This is the initial setup call.
func (r *DomainResolver) ResolveAndRoute() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for hostname := range r.runtime.DomainMap {
		r.resolveAndRouteOne(hostname)
	}
	return nil
}

// resolveAndRouteOne resolves a single hostname and adds routes for both
// IPv4 and IPv6 addresses. Must be called with mu held.
func (r *DomainResolver) resolveAndRouteOne(hostname string) {
	ips, err := net.LookupIP(hostname)
	if err != nil {
		log.Printf("resolver: failed to resolve %s: %v", hostname, err)
		return
	}

	if len(ips) == 0 {
		log.Printf("resolver: no addresses for %s", hostname)
		return
	}

	// Keep all IPs — both IPv4 and IPv6
	r.realIPMap[hostname] = ips

	for _, ip := range ips {
		ipStr := ip.String()
		r.ipToHost[ipStr] = hostname

		if !r.routedIPs[ipStr] {
			if err := r.tun.AddRoute(ip); err != nil {
				log.Printf("resolver: failed to add route for %s (%s): %v", hostname, ipStr, err)
			} else {
				r.routedIPs[ipStr] = true
				log.Printf("resolver: route %s → %s via %s", hostname, ipStr, r.tun.Name)
			}
		}
	}
}

// StartPeriodicRefresh re-resolves all domains periodically to catch DNS changes
// (CDN rotation, round-robin, IP changes). Blocks until ctx is cancelled.
func (r *DomainResolver) StartPeriodicRefresh(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.refresh()
		}
	}
}

// refresh re-resolves all domains and updates routes.
func (r *DomainResolver) refresh() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for hostname := range r.runtime.DomainMap {
		r.resolveAndRouteOne(hostname)
	}
}

// UpdateDomains handles a config update with a new domain list from the server.
// It adds routes for new domains and removes routes for domains no longer in the list.
func (r *DomainResolver) UpdateDomains(newDomains []config.DomainRule) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Build set of new hostnames
	newSet := make(map[string]bool, len(newDomains))
	for _, d := range newDomains {
		newSet[d.Hostname] = true
	}

	// Remove routes for domains no longer in the list
	for hostname, ips := range r.realIPMap {
		if !newSet[hostname] {
			for _, ip := range ips {
				ipStr := ip.String()
				if r.routedIPs[ipStr] {
					if err := r.tun.RemoveRoute(ip); err != nil {
						log.Printf("resolver: failed to remove route for %s (%s): %v", hostname, ipStr, err)
					} else {
						log.Printf("resolver: removed route %s → %s", hostname, ipStr)
					}
					delete(r.routedIPs, ipStr)
				}
				delete(r.ipToHost, ipStr)
			}
			delete(r.realIPMap, hostname)
		}
	}

	// Resolve and route any new domains
	for _, d := range newDomains {
		if _, exists := r.realIPMap[d.Hostname]; !exists {
			r.resolveAndRouteOne(d.Hostname)
		}
	}
}

// GetHostnameForIP returns the hostname associated with a real IP address.
// Used as a fallback when SNI extraction fails.
func (r *DomainResolver) GetHostnameForIP(ip string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	hostname, ok := r.ipToHost[ip]
	return hostname, ok
}

// Cleanup removes all routes that were added by the resolver.
func (r *DomainResolver) Cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for ipStr := range r.routedIPs {
		ip := net.ParseIP(ipStr)
		if ip != nil {
			if err := r.tun.RemoveRoute(ip); err != nil {
				log.Printf("resolver: cleanup failed for %s: %v", ipStr, err)
			}
		}
	}
	r.routedIPs = make(map[string]bool)
	r.ipToHost = make(map[string]string)
	r.realIPMap = make(map[string][]net.IP)
}

// ResolvedCount returns the number of domains with at least one resolved IP.
func (r *DomainResolver) ResolvedCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.realIPMap)
}

// RouteCount returns the number of active IP routes.
func (r *DomainResolver) RouteCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.routedIPs)
}
