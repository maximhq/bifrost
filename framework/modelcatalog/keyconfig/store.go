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

// providerState is the immutable snapshot stored per provider. RefreshProvider
// atomically swaps a fresh one in, so readers always see a consistent set of
// derived views — never a torn mix of pre- and post-refresh state.
type providerState struct {
	entries     []KeyEntry
	allowed     schemas.WhiteList
	blacklisted schemas.BlackList
	aliasIndex  map[string]AliasOwner
}

type Store struct {
	state  sync.Map // schemas.ModelProvider → *providerState
	logger schemas.Logger
}

// New constructs an empty Store.
func New(logger schemas.Logger) *Store {
	return &Store{logger: logger}
}

// Replace atomically resets the store to reflect the snapshot. Providers
// present in the previous state but absent from snapshot are dropped. Use on
// initial load and on full cross-pod resyncs.
func (s *Store) Replace(snapshot map[schemas.ModelProvider][]schemas.Key) {
	seen := make(map[schemas.ModelProvider]struct{}, len(snapshot))
	for p, keys := range snapshot {
		seen[p] = struct{}{}
		s.storeOrDelete(p, keys)
	}
	s.state.Range(func(k, _ any) bool {
		p := k.(schemas.ModelProvider)
		if _, ok := seen[p]; !ok {
			s.state.Delete(p)
		}
		return true
	})
}

// SetProvider replaces the cached state for one provider. Call after a
// successful key add / update / delete for that provider.
func (s *Store) SetProvider(provider schemas.ModelProvider, keys []schemas.Key) {
	s.storeOrDelete(provider, keys)
}

// RemoveProvider drops all state for the provider. Call on provider delete.
func (s *Store) RemoveProvider(provider schemas.ModelProvider) {
	s.state.Delete(provider)
}

// storeOrDelete writes the derived state, or deletes the entry when the
// provider has nothing useful to store (no enabled keys and not a keyless
// non-standard provider).
func (s *Store) storeOrDelete(provider schemas.ModelProvider, keys []schemas.Key) {
	st := s.buildState(provider, keys)
	if st == nil {
		s.state.Delete(provider)
		return
	}
	s.state.Store(provider, st)
}

// buildState applies the aggregation rules: skip disabled keys and full
// blacklists, union allowed minus per-key blacklisted, intersect blacklists
// across enabled keys for the provider-level set, last-enabled-key-wins on
// alias collisions with a warning. Returns nil when nothing should be stored.
func (s *Store) buildState(provider schemas.ModelProvider, keys []schemas.Key) *providerState {
	var (
		allModelsAllowed bool
		enabledKeysCount int
		allowed          schemas.WhiteList
		blacklistCounts  = make(map[string]int)
		aliasIndex       = make(map[string]AliasOwner)
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
			blacklistCounts[m]++
		}

		if key.Models.IsUnrestricted() {
			allModelsAllowed = true
		} else {
			for _, m := range key.Models {
				if key.BlacklistedModels.IsBlocked(m) {
					continue
				}
				allowed = append(allowed, m)
			}
		}

		for aliasName, cfg := range key.Aliases {
			if prev, exists := aliasIndex[aliasName]; exists && prev.KeyID != key.ID && s.logger != nil {
				s.logger.Warn("keyconfig: alias %q on provider %s defined by both key %s and key %s; last enabled key wins",
					aliasName, provider, prev.KeyID, key.ID)
			}
			aliasIndex[aliasName] = AliasOwner{KeyID: key.ID, Config: cfg}
		}
	}

	if enabledKeysCount == 0 && !allModelsAllowed {
		return nil
	}

	if allModelsAllowed {
		allowed = schemas.WhiteList{"*"}
	}

	var blacklisted schemas.BlackList
	for m, count := range blacklistCounts {
		if count == enabledKeysCount {
			blacklisted = append(blacklisted, m)
		}
	}

	return &providerState{
		entries:     entries,
		allowed:     allowed,
		blacklisted: blacklisted,
		aliasIndex:  aliasIndex,
	}
}

func (s *Store) load(provider schemas.ModelProvider) *providerState {
	v, ok := s.state.Load(provider)
	if !ok {
		return nil
	}
	return v.(*providerState)
}

// EntriesFor returns all per-key entries for the provider (including disabled
// ones). Returns a defensive slice copy; KeyEntry fields share underlying
// memory with the store and must not be mutated.
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
// no key can serve it.
func (s *Store) BlacklistedFor(provider schemas.ModelProvider) schemas.BlackList {
	st := s.load(provider)
	if st == nil {
		return nil
	}
	return slices.Clone(st.blacklisted)
}

// IsAllowed composes the aggregated allow/block without copying. Returns
// false for unknown providers (no state ⇒ no allowance).
func (s *Store) IsAllowed(provider schemas.ModelProvider, model string) bool {
	st := s.load(provider)
	if st == nil {
		return false
	}
	if st.blacklisted.IsBlocked(model) {
		return false
	}
	return st.allowed.IsAllowed(model)
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
	owner, ok := st.aliasIndex[model]
	return owner, ok
}

// Providers returns every provider with cached state. Used by callers
// (notably the load balancer) that need to enumerate the routing-eligible
// provider set.
func (s *Store) Providers() []schemas.ModelProvider {
	out := make([]schemas.ModelProvider, 0)
	s.state.Range(func(k, _ any) bool {
		out = append(out, k.(schemas.ModelProvider))
		return true
	})
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
