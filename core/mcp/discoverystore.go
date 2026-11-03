//go:build !tinygo && !wasm

package mcp

import (
	"context"
	"errors"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
)

// errNoPerUserDiscoveryStore refuses a write for a client with
// per_user_discovery on when no store keeps per-caller discoveries.
var errNoPerUserDiscoveryStore = errors.New("no per-user discovery store is configured")

// discoveryStore is the manager's schemas.MCPDiscoveryStore: it hands a
// shared client to shared and a client with per_user_discovery on to perUser,
// which keeps one discovery per caller; with no perUser such a client has no
// tools for anyone.
type discoveryStore struct {
	shared  schemas.MCPDiscoveryStore
	perUser schemas.MCPDiscoveryStore
}

// NewDiscoveryStore returns the manager's discovery store. It takes the two
// stores as a pair: shared clients are handed to shared only when perUser is
// given too, and are kept in memory when either is nil. Clients with
// per_user_discovery on are handed to perUser; nil leaves them with no tools.
func NewDiscoveryStore(shared, perUser schemas.MCPDiscoveryStore) schemas.MCPDiscoveryStore {
	if shared == nil || perUser == nil {
		shared = &memoryDiscoveryStore{discoveries: make(map[string]*schemas.MCPDiscovery)}
	}
	return &discoveryStore{shared: shared, perUser: perUser}
}

func (s *discoveryStore) GetDiscovery(ctx context.Context, client *schemas.MCPClientConfig) *schemas.MCPDiscovery {
	if client == nil {
		return nil
	}
	if perUserDiscoveryOn(client) {
		if s.perUser == nil {
			return nil
		}
		return s.perUser.GetDiscovery(ctx, client)
	}
	return s.shared.GetDiscovery(ctx, client)
}

func (s *discoveryStore) SetDiscovery(ctx context.Context, client *schemas.MCPClientConfig, discovery *schemas.MCPDiscovery) error {
	if client == nil || discovery == nil {
		return errors.New("a discovery write requires a client and a discovery")
	}
	if perUserDiscoveryOn(client) {
		if s.perUser == nil {
			return errNoPerUserDiscoveryStore
		}
		return s.perUser.SetDiscovery(ctx, client, discovery)
	}
	return s.shared.SetDiscovery(ctx, client, discovery)
}

// DeleteDiscovery drops the shared entry whatever the flag says, so one left
// from before the client turned per_user_discovery on does not outlive it,
// then deletes the caller's own for a client with the flag on.
func (s *discoveryStore) DeleteDiscovery(ctx context.Context, client *schemas.MCPClientConfig) error {
	if client == nil {
		return nil
	}
	if err := s.shared.DeleteDiscovery(ctx, client); err != nil {
		return err
	}
	if perUserDiscoveryOn(client) && s.perUser != nil {
		return s.perUser.DeleteDiscovery(ctx, client)
	}
	return nil
}

// memoryDiscoveryStore is the default shared store: one discovery per client,
// in memory, served to every caller; ctx is ignored.
//
// Discoveries are replaced wholesale and never mutated in place, so one handed
// out by a read stays valid (if possibly stale) for as long as the caller holds
// it, without cloning.
type memoryDiscoveryStore struct {
	mu          sync.RWMutex
	discoveries map[string]*schemas.MCPDiscovery
}

func (s *memoryDiscoveryStore) GetDiscovery(_ context.Context, client *schemas.MCPClientConfig) *schemas.MCPDiscovery {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.discoveries[client.ID]
}

func (s *memoryDiscoveryStore) SetDiscovery(_ context.Context, client *schemas.MCPClientConfig, discovery *schemas.MCPDiscovery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discoveries[client.ID] = discovery
	return nil
}

func (s *memoryDiscoveryStore) DeleteDiscovery(_ context.Context, client *schemas.MCPClientConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.discoveries, client.ID)
	return nil
}

// perUserDiscoveryOn reports whether config has per_user_discovery on.
func perUserDiscoveryOn(config *schemas.MCPClientConfig) bool {
	return config != nil && config.PerUserDiscovery != nil && *config.PerUserDiscovery
}

// discoveryForID returns the caller's discovery for the client with this ID,
// or nil when there is no such client. Takes m.mu, so the caller must not hold
// it; the store is read outside it, since a per-user store may read through to
// a database.
func (m *MCPManager) discoveryForID(ctx context.Context, clientID string) *schemas.MCPDiscovery {
	m.mu.RLock()
	var config *schemas.MCPClientConfig
	if client, ok := m.clientMap[clientID]; ok {
		config = client.ExecutionConfig
	}
	m.mu.RUnlock()
	if config == nil {
		return nil
	}
	return m.discoveryStore.GetDiscovery(ctx, config)
}
