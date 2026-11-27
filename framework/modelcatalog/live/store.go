// Package live caches the response of provider /v1/models calls per
// (provider, keyID, unfiltered). Filtered entries are pre-gated by the
// provider's ListModelsPipeline against the key's allowed/blacklisted/aliases;
// callers reading filtered entries MUST NOT reapply that gate elsewhere or
// alias-backfill rows will be dropped.
//
// The store is passive: it never calls the network. Callers decide when to
// fetch and push results in via Upsert. Today those are the HTTP server's
// bootstrap seed, its key add/update handlers, the on-demand refresh
// endpoints, and BifrostHTTPServer.startLiveModelRefresher, which re-fetches
// every provider on the configured live_models_sync_interval unless that
// interval is 0, which disables the refresher.
//
// Entries are per process and are never persisted, so every node refreshes its
// own copy rather than electing one refresher.
package live

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/livestore"
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
	Models  []string
	Created map[string]int64 // provider-reported creation time in Unix seconds; untimed models are absent
}

// Store keeps the cached /v1/models responses and each provider's invalidation generation in
// live-store tables. Writers hold mu and readers hold it shared, so a read sees an invalidation of
// several entries whole, and UpsertIfCurrent compares the generation under the same lock the bump
// takes.
type Store struct {
	mu      sync.RWMutex // writers lock, readers read-lock
	entries *livestore.Table[Key, Entry]
	// gen counts invalidations per provider so a fetch that was already in
	// flight when its key was deleted or disabled, or its provider removed,
	// cannot re-add the entry the invalidation just dropped. Fetchers capture
	// Generation before issuing the upstream call and commit through
	// UpsertIfCurrent, which compares under the same lock the bump takes.
	//
	// Never pruned. Dropping a provider's counter on delete would reset it to
	// 0, and a fetch still holding a pre-delete generation of 0 would then
	// look current again after the provider was re-added.
	gen    *livestore.Table[schemas.ModelProvider, uint64]
	logger schemas.Logger

	// writeGen counts entry mutations across all providers; the composer's
	// model→provider memo stamps it to detect staleness. Distinct from gen,
	// which is per-provider and guards in-flight fetch races.
	writeGen atomic.Uint64
}

// Names of the store's live-store tables. A process runs one model catalog, so
// the names are fixed.
const (
	entriesTable     = "model_catalog_live_entries"
	generationsTable = "model_catalog_live_generations"
)

// New constructs an empty Store keeping its entries and generations in live,
// which is required.
func New(live *livestore.Store, logger schemas.Logger) (*Store, error) {
	if live == nil {
		return nil, fmt.Errorf("live models: %w", livestore.ErrNoLiveStore)
	}
	if logger == nil {
		logger = bifrost.NewNoOpLogger()
	}
	entries, err := live.NewTable[Key, Entry](context.Background(), entriesTable)
	if err != nil {
		return nil, err
	}
	gen, err := live.NewTable[schemas.ModelProvider, uint64](context.Background(), generationsTable)
	if err != nil {
		_ = live.DropTable(context.Background(), entriesTable)
		return nil, err
	}
	return &Store{entries: entries, gen: gen, logger: logger}, nil
}

// keysOf returns the keys of the provider's entries. Callers hold mu.
func (s *Store) keysOf(provider schemas.ModelProvider) []Key {
	var out []Key
	s.entries.Range(func(k Key, _ Entry) bool {
		if k.Provider == provider {
			out = append(out, k)
		}
		return true
	})
	return out
}

// Upsert stores a successful fetch unconditionally. Use it for writes with no
// in-flight window to lose a race in (seeding, tests); anything that fetches
// from an upstream first should go through Generation + UpsertIfCurrent.
// models is copied; the store takes ownership of created, so callers must not
// modify it after the call.
func (s *Store) Upsert(provider schemas.ModelProvider, keyID string, unfiltered bool, models []string, created map[string]int64) {
	cp := make([]string, len(models))
	copy(cp, models)
	k := Key{Provider: provider, KeyID: keyID, Unfiltered: unfiltered}
	s.mu.Lock()
	s.entries.Put(k, Entry{Models: cp, Created: created})
	s.mu.Unlock()
	s.writeGen.Add(1)
}

// WriteGen returns the monotonic count of entry mutations across all providers.
func (s *Store) WriteGen() uint64 {
	return s.writeGen.Load()
}

// Generation returns the provider's current invalidation counter. Read it
// before starting a fetch and hand it back to UpsertIfCurrent.
func (s *Store) Generation(provider schemas.ModelProvider) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	gen, _ := s.gen.Get(provider)
	return gen
}

// UpsertIfCurrent stores a fetch only if nothing invalidated the provider
// since gen was read, and reports whether it wrote. Like Upsert, it copies
// models and takes ownership of created.
//
// This is what keeps a deleted or disabled key's models from being resurrected
// by a fetch that outlived it. The background refresher works from a key
// snapshot taken at the top of a pass; by the time list-models answers, the key
// may be gone. Its Invalidate already ran, and no later pass re-fetches or
// prunes a key that is no longer configured, so an unguarded commit would
// advertise that key's models until the process restarted.
func (s *Store) UpsertIfCurrent(provider schemas.ModelProvider, keyID string, unfiltered bool, models []string, created map[string]int64, gen uint64) bool {
	cp := make([]string, len(models))
	copy(cp, models)
	k := Key{Provider: provider, KeyID: keyID, Unfiltered: unfiltered}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, _ := s.gen.Get(provider); current != gen {
		return false
	}
	s.entries.Put(k, Entry{Models: cp, Created: created})
	s.writeGen.Add(1)
	return true
}

// bumpLocked invalidates every generation handed out for the provider so far.
// Callers must hold mu for writing.
//
// Bumped unconditionally, even when the invalidation deleted no entry: the
// write a pending fetch is about to make is exactly the case where there is
// nothing cached yet, and that is the write this has to stop.
func (s *Store) bumpLocked(provider schemas.ModelProvider) {
	gen, _ := s.gen.Get(provider)
	s.gen.Put(provider, gen+1)
	s.writeGen.Add(1)
}

// Invalidate drops both filtered and unfiltered entries for one key. Called
// when the key's credential value changes (cached models were computed
// against the old credential) or when the key is deleted.
func (s *Store) Invalidate(provider schemas.ModelProvider, keyID string) {
	s.mu.Lock()
	s.entries.Delete(Key{Provider: provider, KeyID: keyID, Unfiltered: false})
	s.entries.Delete(Key{Provider: provider, KeyID: keyID, Unfiltered: true})
	s.bumpLocked(provider)
	s.mu.Unlock()
}

// InvalidateProvider drops every entry for the provider across all keys and
// modes. Called on provider delete.
func (s *Store) InvalidateProvider(provider schemas.ModelProvider) {
	s.mu.Lock()
	for _, k := range s.keysOf(provider) {
		s.entries.Delete(k)
	}
	s.bumpLocked(provider)
	s.mu.Unlock()
}

// RetainKeys drops the provider's entries whose KeyID is absent from keep,
// in both filtered and unfiltered modes, and leaves the rest untouched. It is
// the narrow counterpart to InvalidateProvider: callers about to re-fetch use
// it to prune keys that were removed or disabled while letting the surviving
// keys' last-known-good entries stand until a successful Upsert replaces them.
// A key named in keep that has no entry is a no-op, so callers can pass their
// whole configured key set without first checking what is cached.
//
// keep is read-only and is not retained by the store. A nil or empty keep is
// equivalent to InvalidateProvider.
//
// Bumps the provider's generation like the other invalidations, which costs the
// caller nothing: every caller re-fetches straight afterwards and so captures
// the new generation. A concurrent background pass loses that provider's
// results for one cycle, which is the correct outcome — the caller's own
// re-fetch replaces them, and the pass was working from the pre-prune key set.
func (s *Store) RetainKeys(provider schemas.ModelProvider, keep map[string]struct{}) {
	s.mu.Lock()
	for _, k := range s.keysOf(provider) {
		if _, ok := keep[k.KeyID]; !ok {
			s.entries.Delete(k)
		}
	}
	s.bumpLocked(provider)
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
	out := make(map[Key]Entry)
	s.entries.Range(func(k Key, e Entry) bool {
		cp := make([]string, len(e.Models))
		copy(cp, e.Models)
		out[k] = Entry{Models: cp}
		return true
	})
	return out
}

// CreatedForProvider returns the provider creation time of each model across
// the provider's entries in the given mode, in Unix seconds. When keys report
// different times for one model, the latest wins. Models without a reported
// time are absent. The returned map is owned by the caller.
func (s *Store) CreatedForProvider(provider schemas.ModelProvider, unfiltered bool) map[string]int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]int64)
	s.entries.Range(func(k Key, e Entry) bool {
		if k.Provider != provider || k.Unfiltered != unfiltered {
			return true
		}
		for model, created := range e.Created {
			if created > out[model] {
				out[model] = created
			}
		}
		return true
	})
	return out
}

// unionForProvider returns the sorted, deduplicated set of models across all
// entries matching the given provider and unfiltered flag.
func (s *Store) unionForProvider(provider schemas.ModelProvider, unfiltered bool) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]struct{})
	s.entries.Range(func(k Key, e Entry) bool {
		if k.Provider != provider || k.Unfiltered != unfiltered {
			return true
		}
		for _, m := range e.Models {
			seen[m] = struct{}{}
		}
		return true
	})
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}
