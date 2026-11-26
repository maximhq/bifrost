package mcpdiscovery

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/livestore"
)

// sharedWriteTimeout bounds one write of a client's tools to its row.
const sharedWriteTimeout = 10 * time.Second

// sharedDiscoveriesTable names the live store's state table of shared discoveries, one per MCP
// client, deleted with the client.
const sharedDiscoveriesTable = "mcp_shared_discoveries"

var _ schemas.MCPDiscoveryStore = (*SharedStore)(nil)

// SharedStore keeps the discoveries of MCP clients without
// per_user_discovery: one per client, in a state table of the live store,
// served to every caller from memory, which is the source of truth, and written
// to the client's row so a restart and the admin API find them. The client's
// record in the live store carries no tools; this table is their one copy.
//
// The manager calls it while holding its own lock, so nothing here waits on
// the database. SetDiscovery records the discovery and hands the write to a
// background writer, one per client, which writes the latest discovery it has
// been handed, skips one whose content the row already holds, and retries a
// failed write. A discovery
// with no DiscoveredAt was not produced by an upstream tools/list (the
// manager restored it from the row), so it is taken as what the row holds:
// it is not written, and a write handed over before it is dropped.
//
// All methods are safe on a nil receiver: reads miss and writes are refused.
type SharedStore struct {
	configStore configstore.ConfigStore
	logger      schemas.Logger

	mu          sync.RWMutex
	discoveries *livestore.Table[string, *schemas.MCPDiscovery] // by client ID
	writes      map[string]*sharedWrite                         // by client ID; the writes in flight

	// writeGate, when set, is asked before every write; see SetWriteGate.
	writeGate atomic.Pointer[func() bool]
}

// sharedWrite is what the store knows about one client's row. seq orders the
// client's SetDiscovery calls, so a slow write never records itself over a
// newer restore.
type sharedWrite struct {
	seq         uint64
	pending     *schemas.MCPDiscovery // the latest discovery not yet written; nil when none
	pendingSeq  uint64
	running     bool                  // a writer is draining pending
	written     *schemas.MCPDiscovery // what the row holds: the last discovery written or restored
	writtenSeq  uint64
	writtenHash string // hash of written; "" until computed
}

// NewSharedStore returns a SharedStore keeping its discoveries in a state table it registers on
// live, and writing through configStore. A nil configStore keeps discoveries in memory only.
func NewSharedStore(configStore configstore.ConfigStore, live *livestore.Store, logger schemas.Logger) (*SharedStore, error) {
	if logger == nil {
		logger = bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	}
	if live == nil {
		return nil, fmt.Errorf("shared discovery store: %w", livestore.ErrNoLiveStore)
	}
	discoveries, err := live.NewStateTable[schemas.MCPDiscovery](context.Background(), sharedDiscoveriesTable, livestore.KindMCPClient)
	if err != nil {
		return nil, fmt.Errorf("shared discovery store: %w", err)
	}
	return &SharedStore{
		configStore: configStore,
		logger:      logger,
		discoveries: discoveries,
		writes:      make(map[string]*sharedWrite),
	}, nil
}

// Load fills the store from the discoveries the clients' rows hold, so a restart serves them
// before any client is dialed. They are taken as what the rows hold and not written back. A client
// with per_user_discovery on keeps no shared discovery and is left out, as is one with nothing
// discovered. No-op without a config store.
func (s *SharedStore) Load(ctx context.Context) error {
	if s == nil || s.configStore == nil {
		return nil
	}
	clients, err := s.configStore.GetMCPClientConfigs(ctx)
	if err != nil {
		return fmt.Errorf("failed to read the MCP clients' discoveries: %w", err)
	}
	for _, client := range clients {
		s.restore(client)
	}
	return nil
}

// LoadClient fills the store with the discovery one client's row holds, as Load does for every
// client at boot, when the store holds none for the client: a client connected while Bifrost runs,
// by this process or by another over the same database, is served the tools its row was written
// with instead of discovering them again. A discovery the store already holds is as new as the row
// or newer, so it is kept and the row is not read. A client with no row is left out, as is one Load
// would leave out. No-op without a config store.
func (s *SharedStore) LoadClient(ctx context.Context, id string) error {
	if s == nil || s.configStore == nil {
		return nil
	}
	if _, held := s.discoveries.Get(id); held {
		return nil
	}
	client, err := s.configStore.GetMCPClientConfigByID(ctx, id)
	if errors.Is(err, configstore.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read MCP client %s: %w", id, err)
	}
	s.restore(client)
	return nil
}

// restore records the discovery client's row holds as what the row holds, so it is not written
// back, unless the store already holds a discovery for the client, which is as new as the row or
// newer. A client with per_user_discovery on keeps no shared discovery and is left out, as is one
// with nothing discovered.
func (s *SharedStore) restore(client *schemas.MCPClientConfig) {
	if client.PerUserDiscovery != nil && *client.PerUserDiscovery {
		return
	}
	if len(client.DiscoveredTools) == 0 && client.DiscoveredInstructions == "" && len(client.DiscoveredResources) == 0 &&
		len(client.DiscoveredResourceTemplates) == 0 && len(client.DiscoveredPrompts) == 0 {
		return
	}
	for name, tool := range client.DiscoveredTools {
		_ = tool.EnsureSerialized() // cache serialized JSON at the source
		client.DiscoveredTools[name] = tool
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, held := s.discoveries.Get(client.ID); held {
		return
	}
	// Zero DiscoveredAt: restored from the row, so it is recorded as what the row holds.
	s.setLocked(client.ID, &schemas.MCPDiscovery{
		Tools:             client.DiscoveredTools,
		Instructions:      client.DiscoveredInstructions,
		Resources:         client.DiscoveredResources,
		ResourceTemplates: client.DiscoveredResourceTemplates,
		Prompts:           client.DiscoveredPrompts,
	})
}

// GetDiscovery returns client's discovery; ctx is ignored.
func (s *SharedStore) GetDiscovery(_ context.Context, client *schemas.MCPClientConfig) *schemas.MCPDiscovery {
	if s == nil || client == nil {
		return nil
	}
	discovery, _ := s.discoveries.Get(client.ID)
	return discovery
}

// SetDiscovery replaces client's discovery and, unless it was restored,
// queues it to be written to the client's row; ctx is ignored. It never waits
// on the database.
func (s *SharedStore) SetDiscovery(_ context.Context, client *schemas.MCPClientConfig, discovery *schemas.MCPDiscovery) error {
	if s == nil {
		return fmt.Errorf("shared discovery store is not configured")
	}
	if client == nil || discovery == nil {
		return fmt.Errorf("shared discovery requires a client and a discovery")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setLocked(client.ID, discovery)
	return nil
}

// setLocked is SetDiscovery for the client with this ID; the caller holds s.mu.
func (s *SharedStore) setLocked(clientID string, discovery *schemas.MCPDiscovery) {
	s.discoveries.Put(clientID, discovery)
	if s.configStore == nil {
		return
	}
	w := s.writes[clientID]
	if w == nil {
		w = &sharedWrite{}
		s.writes[clientID] = w
	}
	w.seq++
	if discovery.DiscoveredAt.IsZero() {
		w.written, w.writtenSeq, w.writtenHash = discovery, w.seq, ""
		return
	}
	w.pending, w.pendingSeq = discovery, w.seq
	if !w.running {
		w.running = true
		go s.drain(clientID, w)
	}
}

// DeleteDiscovery forgets client's discovery; ctx is ignored. The row is left
// as it is: a disabled client is restored from it when re-enabled, and a
// removed client's row goes with the client. A write already handed to the
// writer still lands, but is not retried once it fails.
func (s *SharedStore) DeleteDiscovery(_ context.Context, client *schemas.MCPClientConfig) error {
	if s == nil || client == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discoveries.Delete(client.ID)
	if w := s.writes[client.ID]; w != nil && !w.running {
		delete(s.writes, client.ID)
	}
	return nil
}

// SetWriteGate registers allow, asked before every write to a client's row; a
// write it refuses is skipped, and the client's next discovery tries again.
// Every process discovers the same tools for a shared client, so a deployment
// running several processes over one database can leave the writes to one of
// them. nil clears it.
func (s *SharedStore) SetWriteGate(allow func() bool) {
	if s == nil {
		return
	}
	if allow == nil {
		s.writeGate.Store(nil)
		return
	}
	s.writeGate.Store(&allow)
}

// sharedWriteRetryBackoff is the wait before retrying a failed write: 1s,
// doubling to a minute.
func sharedWriteRetryBackoff(attempt int) time.Duration {
	return min(time.Second<<min(attempt-1, 6), time.Minute)
}

// drain writes clientID's pending discovery until there is none, retrying a
// failed write until it lands, a newer discovery replaces it or the client is
// deleted. Hashing and the write itself run without s.mu, so neither holds up
// the manager.
func (s *SharedStore) drain(clientID string, w *sharedWrite) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("panic while persisting the discovery of MCP client %s: %v", clientID, r)
			s.mu.Lock()
			w.running = false
			s.mu.Unlock()
		}
	}()
	for attempt := 0; ; {
		s.mu.Lock()
		discovery, seq := w.pending, w.pendingSeq
		w.pending = nil
		written, writtenHash := w.written, w.writtenHash
		if _, present := s.discoveries.Get(clientID); attempt > 0 && !present {
			discovery = nil // deleted while its write kept failing: nothing is left to save
		}
		if discovery == nil {
			w.running = false
			if _, ok := s.discoveries.Get(clientID); !ok && s.writes[clientID] == w {
				delete(s.writes, clientID)
			}
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		hash, err := discoveryHash(discovery)
		if err != nil {
			s.logger.Warn("failed to hash the discovery of MCP client %s: %v", clientID, err)
			continue
		}
		if writtenHash == "" && written != nil {
			if writtenHash, err = discoveryHash(written); err == nil {
				s.mu.Lock()
				if w.written == written {
					w.writtenHash = writtenHash
				}
				s.mu.Unlock()
			}
		}
		if hash == writtenHash || !s.mayWrite() {
			attempt = 0
			continue
		}
		s.mu.Lock()
		superseded := w.writtenSeq > seq // restored since it was handed over
		s.mu.Unlock()
		if superseded {
			attempt = 0
			continue
		}
		if err := s.write(clientID, discovery); err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				attempt = 0 // no row to write to: nothing to retry
				continue
			}
			// Retried rather than left for the client's next sync, which may be many minutes
			// away. A newer discovery handed over meanwhile is written instead.
			attempt++
			s.mu.Lock()
			if w.pending == nil {
				w.pending, w.pendingSeq = discovery, seq
			}
			s.mu.Unlock()
			time.Sleep(sharedWriteRetryBackoff(attempt))
			continue
		}
		attempt = 0
		s.mu.Lock()
		if w.writtenSeq < seq {
			w.written, w.writtenSeq, w.writtenHash = discovery, seq, hash
		}
		s.mu.Unlock()
	}
}

// mayWrite asks the write gate, if one is set.
func (s *SharedStore) mayWrite() bool {
	allow := s.writeGate.Load()
	return allow == nil || (*allow)()
}

// write stores discovery on clientID's row. A client with no row (one that is
// not persisted, or was just removed) is not an error worth more than a debug
// line.
func (s *SharedStore) write(clientID string, discovery *schemas.MCPDiscovery) error {
	ctx, cancel := context.WithTimeout(context.Background(), sharedWriteTimeout)
	defer cancel()
	err := s.configStore.UpdateMCPClientDiscovery(ctx, clientID, discovery)
	switch {
	case errors.Is(err, configstore.ErrNotFound):
		s.logger.Debug("MCP client %s has no row to persist its discovery to", clientID)
	case err != nil:
		s.logger.Error("failed to persist the discovery of MCP client %s: %v", clientID, err)
	}
	return err
}
