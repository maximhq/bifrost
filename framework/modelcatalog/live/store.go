// Package live caches the response of provider /v1/models calls per
// (provider, keyID, unfiltered). Filtered entries are pre-gated by the
// provider's ListModelsPipeline against the key's allowed/blacklisted/aliases;
// callers reading filtered entries MUST NOT reapply that gate elsewhere or
// alias-backfill rows will be dropped.
//
// The store is passive — it never calls the network. Callers (the HTTP server
// after key add/update, or a future background refresher) decide when to
// fetch and push results in via Upsert.
package live

import (
	"slices"
	"sync"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// Key identifies one cached response. KeyID is "" for keyless providers
// (Vertex workload identity, Bedrock IAM, etc).
type Key struct {
	Provider   schemas.ModelProvider
	KeyID      string
	Unfiltered bool
}

// Entry is a single cached response.
type Entry struct {
	Models []string
}

type Store struct {
	mu      sync.RWMutex
	entries map[Key]Entry
	logger  schemas.Logger
}

func New(logger schemas.Logger) *Store {
	if logger == nil {
		logger = bifrost.NewNoOpLogger()
	}
	return &Store{entries: make(map[Key]Entry), logger: logger}
}

// Upsert stores a successful fetch.
func (s *Store) Upsert(provider schemas.ModelProvider, keyID string, unfiltered bool, models []string) {
	cp := make([]string, len(models))
	copy(cp, models)
	k := Key{Provider: provider, KeyID: keyID, Unfiltered: unfiltered}
	s.mu.Lock()
	s.entries[k] = Entry{Models: cp}
	s.mu.Unlock()
}

// Invalidate drops both filtered and unfiltered entries for one key. Called
// when the key's credential value changes (cached models were computed
// against the old credential) or when the key is deleted.
func (s *Store) Invalidate(provider schemas.ModelProvider, keyID string) {
	s.mu.Lock()
	delete(s.entries, Key{Provider: provider, KeyID: keyID, Unfiltered: false})
	delete(s.entries, Key{Provider: provider, KeyID: keyID, Unfiltered: true})
	s.mu.Unlock()
}

// InvalidateProvider drops every entry for the provider across all keys and
// modes. Called on provider delete.
func (s *Store) InvalidateProvider(provider schemas.ModelProvider) {
	s.mu.Lock()
	for k := range s.entries {
		if k.Provider == provider {
			delete(s.entries, k)
		}
	}
	s.mu.Unlock()
}

// ModelsForProvider returns the union of filtered entries for the provider,
// sorted. Filtered entries are pre-gated so this is the effective allowed set
// across the provider's keys.
func (s *Store) ModelsForProvider(provider schemas.ModelProvider) []string {
	return s.unionForProvider(provider, false)
}

// UnfilteredModelsForProvider returns the union of unfiltered entries — the
// raw provider catalog with no key-level gating applied.
func (s *Store) UnfilteredModelsForProvider(provider schemas.ModelProvider) []string {
	return s.unionForProvider(provider, true)
}

// Snapshot returns a defensive copy of every entry for diagnostics. Slices
// are copied; the returned map is independent of store state.
func (s *Store) Snapshot() map[Key]Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[Key]Entry, len(s.entries))
	for k, e := range s.entries {
		cp := make([]string, len(e.Models))
		copy(cp, e.Models)
		out[k] = Entry{Models: cp}
	}
	return out
}

// unionForProvider returns the sorted, deduplicated set of models across all
// entries matching the given provider and unfiltered flag.
func (s *Store) unionForProvider(provider schemas.ModelProvider, unfiltered bool) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]struct{})
	for k, e := range s.entries {
		if k.Provider != provider || k.Unfiltered != unfiltered {
			continue
		}
		for _, m := range e.Models {
			seen[m] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}
