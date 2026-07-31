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
//
// Entries carry each provider's models in full, not just their identifiers,
// because the store also backs schemas.ModelDirectory: when a refresher is
// keeping it current, GET /v1/models is answered from here rather than by
// querying every provider per request, and that answer has to match what a
// live call would have returned.
package live

import (
	"slices"
	"strings"
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
	// Models is the provider's list-models response for this key, already
	// filtered to this provider and deduplicated. Kept in full rather than
	// reduced to identifiers so a cache hit can serve /v1/models with the same
	// fields a live call would return (alias, owned_by, context_length,
	// pricing, ...).
	Models []schemas.Model
	// IDs is the routing view of the same entry: the identifiers callers match
	// against when resolving a model to a provider.
	//
	// Deliberately supplied by the caller rather than derived from Models. The
	// two are not the same projection: routing strips the owning provider's own
	// prefix ("openai/gpt-4o" cached under openai routes as "gpt-4o") while
	// leaving foreign prefixes intact, because cross-provider resolution keys
	// off them ("openai/gpt-oss-120b" listed by groq must stay prefixed).
	// Models keeps every identifier exactly as the provider reported it, since
	// that is what a cache hit has to hand back to /v1/models.
	IDs []string
}

type Store struct {
	mu      sync.RWMutex
	entries map[Key]Entry
	// gen counts invalidations per provider so a fetch that was already in
	// flight when its key was deleted or disabled, or its provider removed,
	// cannot re-add the entry the invalidation just dropped. Fetchers capture
	// Generation before issuing the upstream call and commit through
	// UpsertIfCurrent, which compares under the same lock the bump takes.
	//
	// Never pruned. Dropping a provider's counter on delete would reset it to
	// 0, and a fetch still holding a pre-delete generation of 0 would then
	// look current again after the provider was re-added.
	gen    map[schemas.ModelProvider]uint64
	logger schemas.Logger
}

func New(logger schemas.Logger) *Store {
	if logger == nil {
		logger = bifrost.NewNoOpLogger()
	}
	return &Store{
		entries: make(map[Key]Entry),
		gen:     make(map[schemas.ModelProvider]uint64),
		logger:  logger,
	}
}

// Upsert stores a successful fetch.
//
// models is the provider's response, already narrowed to this provider and
// deduplicated, with identifiers left as reported. ids is the routing
// projection of the same fetch; see Entry.IDs for why it is passed in rather
// than derived. The two need not be the same length.
func (s *Store) Upsert(provider schemas.ModelProvider, keyID string, unfiltered bool, models []schemas.Model, ids []string) {
	modelsCopy := make([]schemas.Model, len(models))
	copy(modelsCopy, models)
	idsCopy := make([]string, len(ids))
	copy(idsCopy, ids)
	k := Key{Provider: provider, KeyID: keyID, Unfiltered: unfiltered}
	s.mu.Lock()
	s.entries[k] = Entry{Models: modelsCopy, IDs: idsCopy}
	s.mu.Unlock()
}

// Generation returns the provider's current invalidation counter. Read it
// before starting a fetch and hand it back to UpsertIfCurrent.
func (s *Store) Generation(provider schemas.ModelProvider) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gen[provider]
}

// UpsertIfCurrent stores a fetch only if nothing invalidated the provider
// since gen was read, and reports whether it wrote.
//
// models and ids carry the same two projections Upsert takes, and for the same
// reason: an entry written with only one of them would answer either /v1/models
// or routing with an empty set. See Entry for why the two are not derivable
// from each other.
//
// This is what keeps a deleted or disabled key's models from being resurrected
// by a fetch that outlived it. The background refresher works from a key
// snapshot taken at the top of a pass; by the time list-models answers, the key
// may be gone. Its Invalidate already ran, and no later pass re-fetches or
// prunes a key that is no longer configured, so an unguarded commit would
// advertise that key's models until the process restarted.
func (s *Store) UpsertIfCurrent(provider schemas.ModelProvider, keyID string, unfiltered bool, models []schemas.Model, ids []string, gen uint64) bool {
	modelsCopy := make([]schemas.Model, len(models))
	copy(modelsCopy, models)
	idsCopy := make([]string, len(ids))
	copy(idsCopy, ids)
	k := Key{Provider: provider, KeyID: keyID, Unfiltered: unfiltered}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen[provider] != gen {
		return false
	}
	s.entries[k] = Entry{Models: modelsCopy, IDs: idsCopy}
	return true
}

// bumpLocked invalidates every generation handed out for the provider so far.
// Callers must hold mu for writing.
//
// Bumped unconditionally, even when the invalidation deleted no entry: the
// write a pending fetch is about to make is exactly the case where there is
// nothing cached yet, and that is the write this has to stop.
func (s *Store) bumpLocked(provider schemas.ModelProvider) {
	s.gen[provider]++
}

// Invalidate drops both filtered and unfiltered entries for one key. Called
// when the key's credential value changes (cached models were computed
// against the old credential) or when the key is deleted.
func (s *Store) Invalidate(provider schemas.ModelProvider, keyID string) {
	s.mu.Lock()
	delete(s.entries, Key{Provider: provider, KeyID: keyID, Unfiltered: false})
	delete(s.entries, Key{Provider: provider, KeyID: keyID, Unfiltered: true})
	s.bumpLocked(provider)
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
	for k := range s.entries {
		if k.Provider != provider {
			continue
		}
		if _, ok := keep[k.KeyID]; ok {
			continue
		}
		delete(s.entries, k)
	}
	s.bumpLocked(provider)
	s.mu.Unlock()
}

// ModelsForProvider returns the union of filtered entries for the provider,
// sorted. Filtered entries are pre-gated so this is the effective allowed set
// across the provider's keys.
func (s *Store) ModelsForProvider(provider schemas.ModelProvider) []string {
	return s.unionForProvider(provider, nil, false)
}

// UnfilteredModelsForProvider returns the union of unfiltered entries — the
// raw provider catalog with no key-level gating applied.
func (s *Store) UnfilteredModelsForProvider(provider schemas.ModelProvider) []string {
	return s.unionForProvider(provider, nil, true)
}

// IDsForKeys is ModelsForProvider narrowed to a subset of the provider's keys:
// the routing view of just those keys' entries, sorted and deduplicated.
//
// Needed because the provider-wide union answers "what can this gateway route
// to the provider", which is the wrong question for a call already scoped to
// one key. Reconciling a key-scoped list-models response against the union
// would append models only some *other* key is allowed to serve, and — since
// that reconciled response is what the refresher caches under the scoped key —
// write the broadened set back over the very allowlist that excluded them.
//
// keyIDs is read-only. An empty keyIDs looks up the "" sentinel that keyless
// providers cache under, matching FullModelsFor rather than falling back to the
// provider-wide union.
func (s *Store) IDsForKeys(provider schemas.ModelProvider, keyIDs []string, unfiltered bool) []string {
	lookup := keyIDs
	if len(lookup) == 0 {
		lookup = []string{""}
	}
	keep := make(map[string]struct{}, len(lookup))
	for _, id := range lookup {
		keep[id] = struct{}{}
	}
	return s.unionForProvider(provider, keep, unfiltered)
}

// FullModelsFor returns the models cached for the provider across the given
// keys, deduplicated by identifier and sorted, plus whether any of those keys
// had an entry at all.
//
// The bool is the cache-hit signal and is deliberately distinct from an empty
// slice: a provider that legitimately serves no models is a hit with zero
// results, whereas a key that has never been fetched is a miss that must fall
// through to the provider.
//
// keyIDs is read-only. An empty keyIDs looks up the "" sentinel that keyless
// providers cache under.
func (s *Store) FullModelsFor(provider schemas.ModelProvider, keyIDs []string, unfiltered bool) ([]schemas.Model, bool) {
	lookup := keyIDs
	if len(lookup) == 0 {
		lookup = []string{""}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	hit := false
	seen := make(map[string]struct{})
	out := make([]schemas.Model, 0)
	for _, keyID := range lookup {
		e, ok := s.entries[Key{Provider: provider, KeyID: keyID, Unfiltered: unfiltered}]
		if !ok {
			continue
		}
		hit = true
		for _, m := range e.Models {
			if _, dup := seen[m.ID]; dup {
				continue
			}
			seen[m.ID] = struct{}{}
			out = append(out, m)
		}
	}
	if !hit {
		return nil, false
	}
	slices.SortFunc(out, func(a, b schemas.Model) int { return strings.Compare(a.ID, b.ID) })
	return out, true
}

// FindModel returns the provider's record for one model, across every key and
// both filtered and unfiltered entries.
//
// Deliberately unscoped by key: this answers "what is this model", not "may
// this key reach it". A model's context length and pricing do not depend on
// which credential fetched the listing, so narrowing to a key here would only
// turn a describable model into an unknown one.
//
// Matched against Models rather than IDs. The two are different projections
// and need not even be the same length (see Entry.IDs), so index parity cannot
// be assumed; only Models carries the metadata a caller wants back. Comparison
// drops the owning provider's own prefix on both sides, leaving foreign
// prefixes intact, which is how routing reads these identifiers.
//
// Deep-copied on the way out, like Snapshot: entries are replaced wholesale by
// the refresher, so a caller holding the stored value would be reading a map
// the next Upsert may be rewriting.
func (s *Store) FindModel(provider schemas.ModelProvider, model string) (schemas.Model, bool) {
	if model == "" {
		return schemas.Model{}, false
	}
	want := strings.TrimPrefix(model, string(provider)+"/")

	s.mu.RLock()
	defer s.mu.RUnlock()
	for k, e := range s.entries {
		if k.Provider != provider {
			continue
		}
		for _, m := range e.Models {
			if strings.TrimPrefix(m.ID, string(provider)+"/") == want {
				return m.DeepCopy(), true
			}
		}
	}
	return schemas.Model{}, false
}

// Snapshot returns a defensive copy of every entry for diagnostics. Slices
// are copied; the returned map is independent of store state.
func (s *Store) Snapshot() map[Key]Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[Key]Entry, len(s.entries))
	for k, e := range s.entries {
		models := make([]schemas.Model, len(e.Models))
		copy(models, e.Models)
		ids := make([]string, len(e.IDs))
		copy(ids, e.IDs)
		out[k] = Entry{Models: models, IDs: ids}
	}
	return out
}

// unionForProvider returns the sorted, deduplicated set of models across all
// entries matching the given provider and unfiltered flag. A nil keep set means
// every key; a non-nil one narrows to exactly its members (an empty non-nil set
// therefore matches nothing, which is the honest answer for "these keys" when
// the caller named none).
func (s *Store) unionForProvider(provider schemas.ModelProvider, keep map[string]struct{}, unfiltered bool) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]struct{})
	for k, e := range s.entries {
		if k.Provider != provider || k.Unfiltered != unfiltered {
			continue
		}
		if keep != nil {
			if _, ok := keep[k.KeyID]; !ok {
				continue
			}
		}
		for _, id := range e.IDs {
			seen[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}
