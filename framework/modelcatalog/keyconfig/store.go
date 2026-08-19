// Package keyconfig caches per-key configuration (allowed/blacklisted
// models, aliases) for every configured provider. It is a pure
// transformation: callers push raw schemas.Key slices in via SetProvider /
// Replace, and the store exposes derived views (aggregated allow/block
// lists, alias-owner index, per-key entries) for routing-time queries.
//
// The store performs no I/O — it does not know about configstore, the
// network, or persistence. The composer (ModelCatalog) owns fetching keys
// from the config store and pushing them in.
//
// Aggregation semantics — provider-level blacklist as the intersection across
// enabled keys, last-enabled-key-wins on alias collisions — are ported from
// bifrost-enterprise/core/loadbalancing/plugin.go and extended with per-key
// alias retention so routing can resolve (provider, model) → (keyID, AliasConfig).
package keyconfig

import (
	"slices"
	"strings"
	"sync"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// KeyEntry is the per-key configuration snapshot the store maintains. Slice
// and map fields are owned by the store; callers must not mutate.
type KeyEntry struct {
	KeyID       string
	Enabled     bool
	Allowed     schemas.WhiteList
	Blacklisted schemas.BlackList
	Aliases     schemas.KeyAliases
}

// AliasOwner identifies which key owns an alias and carries its AliasConfig.
// Routing uses KeyID to pick credentials; Config carries the deployment /
// region overrides that must be applied alongside.
type AliasOwner struct {
	KeyID  string
	Config schemas.AliasConfig
}

// providerState is the immutable snapshot stored per provider. Writers
// build a fresh providerState off-lock and slot it into the entries map
// under the write lock, so readers always see a consistent set of derived
// views — never a torn mix of pre- and post-refresh state.
type providerState struct {
	entries     []KeyEntry
	allowed     schemas.WhiteList
	blacklisted schemas.BlackList
	aliasIndex  map[string]AliasOwner
}

type Store struct {
	// mu serializes writers (Replace / SetProvider / RemoveProvider) and
	// gates concurrent readers. Replace holds the write lock for its full
	// build-and-swap so readers see either the full old snapshot or the
	// full new one — never an interleaving.
	mu      sync.RWMutex
	entries map[schemas.ModelProvider]*providerState
	logger  schemas.Logger
}

// New constructs an empty Store.
func New(logger schemas.Logger) *Store {
	if logger == nil {
		logger = bifrost.NewNoOpLogger()
	}
	return &Store{
		entries: make(map[schemas.ModelProvider]*providerState),
		logger:  logger,
	}
}

// Replace resets the store to reflect the snapshot. Providers present in the
// previous state but absent from snapshot are dropped. The new map is
// swapped in atomically under the write lock: readers see either the full
// old snapshot or the full new one, never an interleaving. Use on initial
// load and on full cross-pod resyncs.
func (s *Store) Replace(snapshot map[schemas.ModelProvider][]schemas.Key) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[schemas.ModelProvider]*providerState, len(snapshot))
	for p, keys := range snapshot {
		if st := s.buildState(p, keys); st != nil {
			next[p] = st
		}
	}
	s.entries = next
}

// SetProvider replaces the cached state for one provider. Call after a
// successful key add / update / delete for that provider.
func (s *Store) SetProvider(provider schemas.ModelProvider, keys []schemas.Key) {
	st := s.buildState(provider, keys)
	s.mu.Lock()
	defer s.mu.Unlock()
	if st == nil {
		delete(s.entries, provider)
		return
	}
	s.entries[provider] = st
}

// RemoveProvider drops all state for the provider. Call on provider delete.
func (s *Store) RemoveProvider(provider schemas.ModelProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, provider)
}

// EntriesFor returns all per-key entries for the provider (including
// individually-disabled keys), or nil when the provider has no routable
// keys at all and was dropped from the store. Returns a defensive slice
// copy; KeyEntry fields share underlying memory with the store and must
// not be mutated.
func (s *Store) EntriesFor(provider schemas.ModelProvider) []KeyEntry {
	st := s.load(provider)
	if st == nil {
		return nil
	}
	out := make([]KeyEntry, len(st.entries))
	copy(out, st.entries)
	return out
}

// EntryFor returns the entry for one (provider, keyID), or false if absent.
func (s *Store) EntryFor(provider schemas.ModelProvider, keyID string) (KeyEntry, bool) {
	st := s.load(provider)
	if st == nil {
		return KeyEntry{}, false
	}
	for _, e := range st.entries {
		if e.KeyID == keyID {
			return e, true
		}
	}
	return KeyEntry{}, false
}

// AllowedFor returns the aggregated whitelist: union of enabled keys' Models
// minus per-key Blacklisted, or ["*"] when any enabled key is unrestricted or
// the provider is keyless non-standard.
func (s *Store) AllowedFor(provider schemas.ModelProvider) schemas.WhiteList {
	st := s.load(provider)
	if st == nil {
		return nil
	}
	return slices.Clone(st.allowed)
}

// BlacklistedFor returns the intersection of enabled keys' BlacklistedModels.
// A model is provider-wide blocked only when *every* enabled key blacklists
// it — matching the LB semantics that a model is only fully unavailable when
// no key can serve it. Entries preserve the original casing of the first key
// that blacklisted the model (the intersection itself is case-insensitive),
// consistent with AllowedFor.
func (s *Store) BlacklistedFor(provider schemas.ModelProvider) schemas.BlackList {
	st := s.load(provider)
	if st == nil {
		return nil
	}
	return slices.Clone(st.blacklisted)
}

// IsAllowed reports whether at least one enabled key can actually serve the
// model on this provider — a key whose allow-list permits it and whose
// blacklist does not block it. Returns false for unknown providers (no state ⇒
// no allowance). This mirrors KeysAllowingModel (true ⇔ KeysAllowingModel
// returns a non-empty set), so it is safe to use as a routing pre-filter: a
// true result guarantees a routable key exists. Keyless unrestricted providers
// (custom providers configured without keys) allow everything, since there is
// no per-key allow-list to route through.
func (s *Store) IsAllowed(provider schemas.ModelProvider, model string) bool {
	st := s.load(provider)
	if st == nil {
		return false
	}
	// Keyless unrestricted provider: no per-key entries to gate on, but the
	// aggregated allow-list ("*") governs and ambient/IAM auth routes without a key.
	if len(st.entries) == 0 {
		return st.allowed.IsAllowed(model) && !st.blacklisted.IsBlocked(model)
	}
	return anyKeyAllows(st, model)
}

// anyKeyAllows reports whether any enabled key in the snapshot can serve the
// model (allowed and not blacklisted). Shares the per-key gating predicate with
// KeysAllowingModel.
func anyKeyAllows(st *providerState, model string) bool {
	for _, e := range st.entries {
		if !e.Enabled || e.Blacklisted.IsBlockAll() || e.Blacklisted.IsBlocked(model) {
			continue
		}
		if e.Allowed.IsAllowed(model) {
			return true
		}
	}
	return false
}

// ResolveAlias returns which key owns the alias on this provider and its
// AliasConfig. AliasConfig is a value copy — safe to mutate without
// affecting store state (though inner pointer fields like Region remain
// shared; treat the returned Config as read-only).
func (s *Store) ResolveAlias(provider schemas.ModelProvider, model string) (AliasOwner, bool) {
	st := s.load(provider)
	if st == nil {
		return AliasOwner{}, false
	}
	// aliasIndex is keyed lowercase; match the case-insensitive contract
	// of schemas.KeyAliases.ResolveConfig.
	owner, ok := st.aliasIndex[strings.ToLower(model)]
	return owner, ok
}

// Providers returns every provider with cached state. Used by callers
// (notably the load balancer) that need to enumerate the routing-eligible
// provider set.
func (s *Store) Providers() []schemas.ModelProvider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]schemas.ModelProvider, 0, len(s.entries))
	for p := range s.entries {
		out = append(out, p)
	}
	return out
}

// KeysAllowingModel returns the IDs of enabled keys whose Allowed list
// includes the model and whose Blacklisted list does not block it. Used by
// routing to skip keys that cannot serve the request without scanning each
// key's Models slice itself.
func (s *Store) KeysAllowingModel(provider schemas.ModelProvider, model string) []string {
	st := s.load(provider)
	if st == nil {
		return nil
	}
	var out []string
	for _, e := range st.entries {
		if !e.Enabled || e.Blacklisted.IsBlockAll() || e.Blacklisted.IsBlocked(model) {
			continue
		}
		if e.Allowed.IsAllowed(model) {
			out = append(out, e.KeyID)
		}
	}
	return out
}

// load returns the providerState for one provider under an RLock. Returns
// nil when the provider isn't in the store. The returned *providerState is
// immutable once published — safe to read without holding the lock.
func (s *Store) load(provider schemas.ModelProvider) *providerState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.entries[provider]
}

// buildState applies the aggregation rules: skip disabled keys and full
// blacklists, union allowed minus per-key blacklisted, intersect blacklists
// across enabled keys for the provider-level set, last-enabled-key-wins on
// alias collisions with a warning. Returns nil when the provider has no
// routable keys (all disabled or fully block-all) and isn't a keyless
// non-standard provider — such providers are dropped from the store
// entirely, since this cache exists for routing-time queries, not
// inspection. The configstore remains the source of truth for the full
// configured-key set.
//
// Safe to call without holding s.mu — it only reads its parameters and
// s.logger, and writes only to local variables.
func (s *Store) buildState(provider schemas.ModelProvider, keys []schemas.Key) *providerState {
	var (
		allModelsAllowed bool
		enabledKeysCount int
		allowed schemas.WhiteList
		// blacklistAgg accumulates the cross-key blacklist intersection. Keyed by
		// lowercased model for case-insensitive counting; name holds the original
		// casing of the first key that blacklisted it, so the emitted blacklist
		// preserves casing like allowed does.
		blacklistAgg = make(map[string]struct {
			count int
			name  string
		})
		aliasIndex = make(map[string]AliasOwner)
		entries          = make([]KeyEntry, 0, len(keys))
	)

	// Keyless non-standard providers (custom providers configured without keys)
	// are unrestricted — there's no allow-list to derive from.
	if len(keys) == 0 && !bifrost.IsStandardProvider(provider) {
		allModelsAllowed = true
	}

	for _, key := range keys {
		enabled := key.Enabled == nil || *key.Enabled
		entries = append(entries, KeyEntry{
			KeyID:       key.ID,
			Enabled:     enabled,
			Allowed:     key.Models,
			Blacklisted: key.BlacklistedModels,
			Aliases:     key.Aliases,
		})

		if !enabled || key.BlacklistedModels.IsBlockAll() {
			continue
		}
		enabledKeysCount++

		for _, m := range key.BlacklistedModels {
			lower := strings.ToLower(m)
			agg := blacklistAgg[lower]
			if agg.count == 0 {
				agg.name = m
			}
			agg.count++
			blacklistAgg[lower] = agg
		}

		if key.Models.IsUnrestricted() {
			allModelsAllowed = true
		} else {
			for _, m := range key.Models {
				if key.BlacklistedModels.IsBlocked(m) {
					continue
				}
				if !allowed.Contains(m) {
					allowed = append(allowed, m)
				}
			}
		}

		for aliasName, cfg := range key.Aliases {
			// Normalize to lowercase so the cross-key alias index matches the
			// case-insensitive semantic of schemas.KeyAliases.ResolveConfig
			// and schemas.KeyAliases.Validate's intra-key uniqueness check.
			// Without this, two keys that differ only in alias casing would
			// silently both land in the index and the collision warning
			// would never fire.
			normalizedAlias := strings.ToLower(aliasName)
			if prev, exists := aliasIndex[normalizedAlias]; exists && prev.KeyID != key.ID {
				s.logger.Debug("keyconfig: alias %q on provider %s defined by both key %s and key %s; last enabled key wins",
					aliasName, provider, prev.KeyID, key.ID)
			}
			aliasIndex[normalizedAlias] = AliasOwner{KeyID: key.ID, Config: cfg}
		}
	}

	if enabledKeysCount == 0 && !allModelsAllowed {
		return nil
	}

	if allModelsAllowed {
		allowed = schemas.WhiteList{"*"}
	}

	var blacklisted schemas.BlackList
	for _, agg := range blacklistAgg {
		if agg.count == enabledKeysCount {
			blacklisted = append(blacklisted, agg.name)
		}
	}

	return &providerState{
		entries:     entries,
		allowed:     allowed,
		blacklisted: blacklisted,
		aliasIndex:  aliasIndex,
	}
}
