package datasheet

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// SetIgnoreProviderCost records whether a provider's self-reported usage.cost
// is ignored in favour of catalog pricing.
func (s *Store) SetIgnoreProviderCost(provider schemas.ModelProvider, ignore bool) {
	s.ignoreProviderCostMu.Lock()
	defer s.ignoreProviderCostMu.Unlock()
	if ignore {
		if s.ignoreProviderCost == nil {
			s.ignoreProviderCost = make(map[schemas.ModelProvider]struct{})
		}
		s.ignoreProviderCost[provider] = struct{}{}
	} else {
		delete(s.ignoreProviderCost, provider)
	}
}

// ReplaceIgnoreProviderCost resets the set of providers whose reported cost is ignored.
func (s *Store) ReplaceIgnoreProviderCost(providers []schemas.ModelProvider) {
	next := make(map[schemas.ModelProvider]struct{}, len(providers))
	for _, p := range providers {
		next[p] = struct{}{}
	}
	s.ignoreProviderCostMu.Lock()
	s.ignoreProviderCost = next
	s.ignoreProviderCostMu.Unlock()
}

// IsProviderCostIgnored reports whether the provider's self-reported cost should be ignored.
func (s *Store) IsProviderCostIgnored(provider schemas.ModelProvider) bool {
	if s == nil || provider == "" {
		return false
	}
	s.ignoreProviderCostMu.RLock()
	defer s.ignoreProviderCostMu.RUnlock()
	_, ok := s.ignoreProviderCost[provider]
	return ok
}

// providerCostUsable reports whether a provider-reported cost should short-circuit catalog pricing.
func (s *Store) providerCostUsable(cost *schemas.BifrostCost, provider schemas.ModelProvider) bool {
	return cost != nil && cost.TotalCost > 0 && !s.IsProviderCostIgnored(provider)
}
