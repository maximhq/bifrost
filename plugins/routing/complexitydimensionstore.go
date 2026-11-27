package routing

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
)

// maxRememberedEmbeddingDimensions bounds the persisted map. Entries are keyed
// by provider and model, so the set only grows when an operator moves the
// classifier to a different embedding model — rare, and never unbounded in
// practice. The cap exists so a pathological config-editing loop cannot grow one
// config row without limit; the oldest entries are simply not written back.
const maxRememberedEmbeddingDimensions = 32

// persistentEmbeddingDimensionStore remembers, across restarts, the vector width
// each provider/model pair returns.
//
// It reads through an in-memory copy and writes through to a config row of its
// own. The row is deliberately not part of the semantic config an operator
// edits: this is measured runtime state, and a saved edit that did not echo it
// back would silently discard it.
type persistentEmbeddingDimensionStore struct {
	ctx    context.Context
	store  configstore.ConfigStore
	logger schemas.Logger

	mu     sync.RWMutex
	loaded bool
	// widths is the in-memory copy, a live-store table read and written under mu.
	widths *livestore.Table[string, int]
	live   *livestore.Store
}

// embeddingWidthsTable is the live-store table of remembered embedding widths. Routing is a fixed
// built-in plugin, so one instance runs per process and the name is fixed.
const embeddingWidthsTable = "routing_embedding_widths"

// newPersistentEmbeddingDimensionStore returns the store over store, its in-memory copy in a table
// of live, or nil without a config store, which leaves nothing to remember to.
func newPersistentEmbeddingDimensionStore(ctx context.Context, store configstore.ConfigStore, live *livestore.Store, logger schemas.Logger) (*persistentEmbeddingDimensionStore, error) {
	if store == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	widths, err := live.NewTable[string, int](ctx, embeddingWidthsTable)
	if err != nil {
		return nil, err
	}
	return &persistentEmbeddingDimensionStore{ctx: ctx, store: store, logger: logger, widths: widths, live: live}, nil
}

// close drops the in-memory copy's table, for a plugin being cleaned up.
func (s *persistentEmbeddingDimensionStore) close() {
	if s == nil {
		return
	}
	_ = s.live.DropTable(context.Background(), embeddingWidthsTable)
}

// Dimension returns the remembered width for an embedding identity. A read that
// cannot reach the config store answers "not remembered", which costs a
// measurement rather than a failure.
func (s *persistentEmbeddingDimensionStore) Dimension(identity string) (int, bool) {
	if s == nil || identity == "" {
		return 0, false
	}
	s.load()

	s.mu.RLock()
	defer s.mu.RUnlock()
	dimension, ok := s.widths.Get(identity)
	if !ok || dimension < 2 {
		return 0, false
	}
	return dimension, true
}

// Remember records a measured width, persisting it only when it is new or has
// changed. A model re-versioned under the same name overwrites the old value
// rather than being rejected: what the provider just returned is the truth, and
// the stale entry would otherwise keep failing its adoption check forever.
func (s *persistentEmbeddingDimensionStore) Remember(identity string, dimension int) {
	if s == nil || identity == "" || dimension < 2 {
		return
	}
	s.load()

	s.mu.Lock()
	if existing, ok := s.widths.Get(identity); ok && existing == dimension {
		s.mu.Unlock()
		return
	}
	snapshot := make(map[string]int, maxRememberedEmbeddingDimensions)
	s.widths.Range(func(key string, value int) bool {
		snapshot[key] = value
		return true
	})
	if _, replacing := snapshot[identity]; !replacing && len(snapshot) >= maxRememberedEmbeddingDimensions {
		s.mu.Unlock()
		return
	}
	s.widths.Put(identity, dimension)
	snapshot[identity] = dimension
	// The write stays under the lock so concurrent callers persist in the same
	// order they mutated: releasing first lets two writers race and leave the
	// row holding whichever snapshot happened to reach the store last, which
	// can be the older one. Remember only writes when a width actually changed,
	// so this serializes a rare call rather than a hot path.
	defer s.mu.Unlock()

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	if err := s.store.UpdateConfig(s.ctx, &tables.TableGovernanceConfig{
		Key:   tables.ConfigComplexitySemanticDimensionsKey,
		Value: string(encoded),
	}); err != nil && s.logger != nil {
		// Losing this costs one batch of embeddings on the next boot, not
		// correctness, so it is reported and not escalated.
		s.logger.Debug("[Governance] Failed to persist semantic embedding dimensions: %v", err)
	}
}

// load reads the persisted row once. A missing row is the normal first-boot
// state, not an error.
func (s *persistentEmbeddingDimensionStore) load() {
	s.mu.RLock()
	loaded := s.loaded
	s.mu.RUnlock()
	if loaded {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return
	}

	entry, err := s.store.GetConfig(s.ctx, tables.ConfigComplexitySemanticDimensionsKey)
	if err != nil && !errors.Is(err, configstore.ErrNotFound) {
		// Deliberately not marked loaded: a transient read failure would
		// otherwise disable the memory for the life of the process, making every
		// later save re-measure a width that is sitting in the row unread.
		if s.logger != nil {
			s.logger.Debug("[Governance] Could not read semantic embedding dimensions, will retry: %v", err)
		}
		return
	}
	s.loaded = true
	if entry == nil || entry.Value == "" {
		return
	}
	var stored map[string]int
	if err := json.Unmarshal([]byte(entry.Value), &stored); err != nil {
		// A row this process cannot parse is treated as absent: it will be
		// replaced by the next measurement rather than blocking one.
		if s.logger != nil {
			s.logger.Debug("[Governance] Ignoring unreadable semantic embedding dimensions row: %v", err)
		}
		return
	}
	for identity, dimension := range stored {
		if dimension >= 2 {
			s.widths.Put(identity, dimension)
		}
	}
}
