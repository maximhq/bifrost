package lib

import (
	"strings"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// headerMatchesPattern returns true if headerName matches the pattern.
// Patterns support trailing wildcard: "anthropic-*" matches "anthropic-beta".
// A bare "*" matches everything. All comparisons are case-insensitive.
func headerMatchesPattern(pattern, headerName string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	headerName = strings.ToLower(strings.TrimSpace(headerName))
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(headerName, pattern[:len(pattern)-1])
	}
	return pattern == headerName
}

// HeaderMatcher holds precomputed header filter data for O(1) exact-match lookups
// and fast prefix matching. Compiled once on config change, safe for concurrent reads.
type HeaderMatcher struct {
	allowExact    map[string]bool
	allowPrefixes []string // lowercased prefixes (without trailing *)
	allowAll      bool
	hasAllowlist  bool
	denyExact     map[string]bool
	denyPrefixes  []string
	denyAll       bool
	hasDenylist   bool
}

// NewHeaderMatcher compiles a GlobalHeaderFilterConfig into an optimized HeaderMatcher.
// Returns nil if config is nil (callers should treat nil as "allow all").
func NewHeaderMatcher(config *configstoreTables.GlobalHeaderFilterConfig) *HeaderMatcher {
	if config == nil {
		return nil
	}
	m := &HeaderMatcher{
		hasAllowlist: len(config.Allowlist) > 0,
		hasDenylist:  len(config.Denylist) > 0,
	}
	if m.hasAllowlist {
		m.allowExact = make(map[string]bool, len(config.Allowlist))
		for _, p := range config.Allowlist {
			lp := strings.ToLower(strings.TrimSpace(p))
			if lp == "*" {
				m.allowAll = true
			} else if strings.HasSuffix(lp, "*") {
				m.allowPrefixes = append(m.allowPrefixes, lp[:len(lp)-1])
			} else {
				m.allowExact[lp] = true
			}
		}
	}
	if m.hasDenylist {
		m.denyExact = make(map[string]bool, len(config.Denylist))
		for _, p := range config.Denylist {
			lp := strings.ToLower(strings.TrimSpace(p))
			if lp == "*" {
				m.denyAll = true
			} else if strings.HasSuffix(lp, "*") {
				m.denyPrefixes = append(m.denyPrefixes, lp[:len(lp)-1])
			} else {
				m.denyExact[lp] = true
			}
		}
	}
	return m
}

// HasAllowlist returns true if the matcher has a non-empty allowlist.
func (m *HeaderMatcher) HasAllowlist() bool {
	if m == nil {
		return false
	}
	return m.hasAllowlist
}

// MatchesAllow returns true if headerName matches any allowlist entry.
// headerName must be lowercased by the caller.
func (m *HeaderMatcher) MatchesAllow(headerName string) bool {
	if m.allowAll {
		return true
	}
	if m.allowExact[headerName] {
		return true
	}
	for _, prefix := range m.allowPrefixes {
		if strings.HasPrefix(headerName, prefix) {
			return true
		}
	}
	return false
}

// MatchesDeny returns true if headerName matches any denylist entry.
// headerName must be lowercased by the caller.
func (m *HeaderMatcher) MatchesDeny(headerName string) bool {
	if m.denyAll {
		return true
	}
	if m.denyExact[headerName] {
		return true
	}
	for _, prefix := range m.denyPrefixes {
		if strings.HasPrefix(headerName, prefix) {
			return true
		}
	}
	return false
}

// ShouldAllow determines if a header should be forwarded based on the
// configurable header filter config (separate from the security denylist).
// Returns true if the header passes both allowlist and denylist checks.
// headerName is lowercased internally for case-insensitive matching.
func (m *HeaderMatcher) ShouldAllow(headerName string) bool {
	if m == nil {
		return true
	}
	headerName = strings.ToLower(headerName)
	if m.hasAllowlist && !m.MatchesAllow(headerName) {
		return false
	}
	if m.hasDenylist && m.MatchesDeny(headerName) {
		return false
	}
	return true
}
