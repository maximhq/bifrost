//go:build !tinygo && !wasm

package mcp

import (
	"context"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
)

// discoveryStore is the built-in schemas.MCPDiscoveryStore: one discovery
// per client, in memory, the same for every caller — ctx is ignored. It is the
// storage the MCP manager uses when no store is injected, and the fallback a
// caller-aware store (e.g. one keeping per-identity discoveries) delegates to for
// clients it holds nothing special for.
//
// Discoveries are replaced wholesale and never mutated in place, so one handed
// out by a read stays valid (if possibly stale) for as long as the caller holds
// it, without cloning.
type discoveryStore struct {
	mu          sync.RWMutex
	discoveries map[string]*schemas.MCPDiscovery
}

// NewDiscoveryStore returns the built-in in-memory discovery store.
func NewDiscoveryStore() schemas.MCPDiscoveryStore {
	return &discoveryStore{discoveries: make(map[string]*schemas.MCPDiscovery)}
}

func (s *discoveryStore) GetDiscovery(_ context.Context, clientID string) *schemas.MCPDiscovery {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.discoveries[clientID]
}

func (s *discoveryStore) GetClientForTool(_ context.Context, toolName string) (string, *schemas.MCPDiscovery) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for clientID, discovery := range s.discoveries {
		if _, exists := discovery.Tools[toolName]; exists {
			return clientID, discovery
		}
	}
	return "", nil
}

func (s *discoveryStore) SetDiscovery(_ context.Context, clientID string, discovery *schemas.MCPDiscovery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if discovery == nil {
		delete(s.discoveries, clientID)
		return
	}
	s.discoveries[clientID] = discovery
}
