package schemas

import (
	"fmt"
	"time"
)

// KeySelectionStrategy names how Bifrost picks which of a provider's keys serves a request.
type KeySelectionStrategy string

const (
	// KeySelectionWeightedRandom picks a key at random, proportionally to Key.Weight. This is
	// Bifrost's historical behaviour and the default when no strategy is configured.
	KeySelectionWeightedRandom KeySelectionStrategy = "weighted_random"
	// KeySelectionRoundRobin cycles through the keys in order (smooth weighted round robin:
	// a key with weight 2 serves twice as often as a key with weight 1).
	KeySelectionRoundRobin KeySelectionStrategy = "round_robin"
	// KeySelectionLeastUsed picks the key with the fewest requests in flight, then the one
	// that has served the fewest requests so far.
	KeySelectionLeastUsed KeySelectionStrategy = "least_used"
	// KeySelectionFillFirst always uses the first eligible key in the configured order and
	// only moves on when it fails or is cooling down (drain one account before the next).
	KeySelectionFillFirst KeySelectionStrategy = "fill_first"
)

// KeySelectionStrategies lists every accepted KeySelectionStrategy value.
var KeySelectionStrategies = []KeySelectionStrategy{
	KeySelectionWeightedRandom,
	KeySelectionRoundRobin,
	KeySelectionLeastUsed,
	KeySelectionFillFirst,
}

// DefaultKeyCooldownSeconds is how long a key that hit a rate limit or quota stays out of
// rotation when the upstream gave no Retry-After and KeySelectionConfig.CooldownSeconds is unset.
const DefaultKeyCooldownSeconds = 60

// KeySelectionConfig configures key/account rotation for one provider.
type KeySelectionConfig struct {
	// Strategy picks the rotation rule. Empty means weighted_random.
	Strategy KeySelectionStrategy `json:"strategy"`
	// StickyLimit (round_robin only) is how many consecutive requests a key serves before
	// rotation advances. 0 or 1 means advance on every request.
	StickyLimit int `json:"sticky_limit,omitempty"`
	// CooldownSeconds is how long a key that failed with a rate limit, exhausted quota or a
	// rejected credential is skipped by later requests when the upstream sent no Retry-After.
	// nil = DefaultKeyCooldownSeconds; 0 disables cross-request cooldown.
	CooldownSeconds *int `json:"cooldown_seconds,omitempty"`
}

// IsValidKeySelectionStrategy reports whether s is a known strategy (empty counts as valid: default).
func IsValidKeySelectionStrategy(s KeySelectionStrategy) bool {
	if s == "" {
		return true
	}
	for _, known := range KeySelectionStrategies {
		if s == known {
			return true
		}
	}
	return false
}

// Validate checks the strategy name and numeric bounds.
func (c *KeySelectionConfig) Validate() error {
	if c == nil {
		return nil
	}
	if !IsValidKeySelectionStrategy(c.Strategy) {
		return fmt.Errorf("invalid key_selection.strategy %q: must be one of weighted_random, round_robin, least_used, fill_first", c.Strategy)
	}
	if c.StickyLimit < 0 || c.StickyLimit > 1000 {
		return fmt.Errorf("invalid key_selection.sticky_limit %d: must be between 0 and 1000", c.StickyLimit)
	}
	if c.CooldownSeconds != nil && (*c.CooldownSeconds < 0 || *c.CooldownSeconds > 86400) {
		return fmt.Errorf("invalid key_selection.cooldown_seconds %d: must be between 0 and 86400", *c.CooldownSeconds)
	}
	return nil
}

// Cooldown returns the configured fallback cooldown (DefaultKeyCooldownSeconds when unset).
func (c *KeySelectionConfig) Cooldown() time.Duration {
	if c == nil || c.CooldownSeconds == nil {
		return DefaultKeyCooldownSeconds * time.Second
	}
	return time.Duration(*c.CooldownSeconds) * time.Second
}
