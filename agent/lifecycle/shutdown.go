package lifecycle

import (
	"log"
	"sync"
)

var shutdownOnce sync.Once

// Shutdown performs a graceful shutdown of all agent components.
// Safe to call multiple times — only the first call executes.
//
//  1. Cancel context (stops config sync, router, periodic resolver)
//  2. Remove all routes added by the resolver
//  3. Close netstack (closes active connections and TUN)
//  4. Save config to disk
func (a *Agent) Shutdown() {
	shutdownOnce.Do(func() {
		log.Printf("shutting down agent...")

		// Cancel context to stop background goroutines
		if a.Cancel != nil {
			a.Cancel()
		}

		// Remove all routes BEFORE closing TUN — routes reference the interface
		if a.Resolver != nil {
			a.Resolver.Cleanup()
		}

		// Close netstack (which closes active connections and the TUN device)
		if a.NetStack != nil {
			a.NetStack.Close()
		}

		// Save config
		if a.Store != nil && a.Config != nil {
			if err := a.Store.Save(a.Config); err != nil {
				log.Printf("warning: failed to save config: %v", err)
			}
		}

		log.Printf("shutdown complete")
	})
}
