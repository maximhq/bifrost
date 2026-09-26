package routing

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
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
	widths map[string]int
}

func newPersistentEmbeddingDimensionStore(ctx context.Context, store configstore.ConfigStore, logger schemas.Logger) *persistentEmbeddingDimensionStore {
	if store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &persistentEmbeddingDimensionStore{ctx: ctx, store: store, logger: logger, widths: map[string]int{}}
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
	dimension, ok := s.widths[identity]
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
	if existing, ok := s.widths[identity]; ok && existing == dimension {
		s.mu.Unlock()
		return
	}
	if _, replacing := s.widths[identity]; !replacing && len(s.widths) >= maxRememberedEmbeddingDimensions {
		s.mu.Unlock()
		return
	}
	s.widths[identity] = dimension
	snapshot := make(map[string]int, len(s.widths))
	for key, value := range s.widths {
		snapshot[key] = value
	}
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
			s.widths[identity] = dimension
		}
	}
}
