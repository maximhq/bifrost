package schemas

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// ModelPatternList holds RE2 patterns that admit or block models by shape
// rather than by name. Each pattern is compiled as a case-insensitive full
// match ("(?i)^(?:pattern)$") and tried against the bare model name and, when
// the caller knows the provider, against "<provider>/<model>".
//
// Patterns live next to the exact lists (WhiteList / BlackList) and never
// inside them: an exact list holds names and the "*" wildcard, a pattern list
// holds only patterns. The "*" wildcard is not a valid pattern.
type ModelPatternList []string

// modelPatternCache holds compiled patterns keyed by the raw pattern. Patterns
// are user data evaluated on every request, so compiling once per distinct
// pattern matters. Only successful compiles are cached; invalid patterns are
// rejected at write time by Validate.
var modelPatternCache sync.Map // map[string]*regexp.Regexp

// CompileModelPattern compiles pattern into an anchored, case-insensitive RE2
// expression and caches it. It returns an error when the pattern is blank or
// when RE2 rejects it.
func CompileModelPattern(pattern string) (*regexp.Regexp, error) {
	if v, ok := modelPatternCache.Load(pattern); ok {
		return v.(*regexp.Regexp), nil
	}
	if strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("model pattern is empty")
	}
	re, err := regexp.Compile("(?i)^(?:" + pattern + ")$")
	if err != nil {
		return nil, err
	}
	actual, _ := modelPatternCache.LoadOrStore(pattern, re)
	return actual.(*regexp.Regexp), nil
}

// IsEmpty reports whether the list holds no patterns.
func (pl ModelPatternList) IsEmpty() bool {
	return len(pl) == 0
}

// Validate checks that every pattern is non-blank, is not the "*" wildcard,
// compiles as RE2, and appears only once.
func (pl ModelPatternList) Validate() error {
	seen := make(map[string]struct{}, len(pl))
	for _, p := range pl {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("model pattern is empty")
		}
		if p == "*" {
			return fmt.Errorf("wildcard '*' is not a pattern; use the exact model list instead")
		}
		if _, ok := seen[p]; ok {
			return fmt.Errorf("duplicate pattern '%s'", p)
		}
		seen[p] = struct{}{}
		if _, err := CompileModelPattern(p); err != nil {
			return fmt.Errorf("invalid pattern '%s': %w", p, err)
		}
	}
	return nil
}

// Matches reports whether any pattern fully matches model, or
// "<provider>/<model>" when provider is non-empty. A pattern that fails to
// compile never matches.
func (pl ModelPatternList) Matches(provider, model string) bool {
	if len(pl) == 0 {
		return false
	}
	var qualified string
	if provider != "" {
		qualified = provider + "/" + model
	}
	for _, p := range pl {
		re, err := CompileModelPattern(p)
		if err != nil {
			continue
		}
		if re.MatchString(model) {
			return true
		}
		if qualified != "" && re.MatchString(qualified) {
			return true
		}
	}
	return false
}

// ModelAccessRule is the composition of one exact allow list, one exact block
// list and their pattern twins. Every site that decides whether a model may be
// served evaluates through it, so allow and block semantics live in one place:
//
//   - a model is admitted when the allow list is "*", names it exactly, or an
//     allow pattern matches it;
//   - a model is blocked when the block list is "*", names it exactly, or a
//     block pattern matches it;
//   - block wins over allow;
//   - an empty allow list with no allow patterns admits nothing.
type ModelAccessRule struct {
	Allowed         WhiteList
	Blocked         BlackList
	AllowedPatterns ModelPatternList
	BlockedPatterns ModelPatternList
}

// Blocks reports whether model is blocked by the exact block list or a block
// pattern.
func (r ModelAccessRule) Blocks(provider, model string) bool {
	return r.Blocked.IsBlocked(model) || r.BlockedPatterns.Matches(provider, model)
}

// Admits reports whether model is admitted by the exact allow list or an allow
// pattern. It ignores the block side; see Allows.
func (r ModelAccessRule) Admits(provider, model string) bool {
	return r.Allowed.IsAllowed(model) || r.AllowedPatterns.Matches(provider, model)
}

// Allows reports whether model may be served: admitted and not blocked.
func (r ModelAccessRule) Allows(provider, model string) bool {
	return !r.Blocks(provider, model) && r.Admits(provider, model)
}

// IsRestricted reports whether the allow side names specific models, either
// exactly or by pattern, rather than admitting everything.
func (r ModelAccessRule) IsRestricted() bool {
	return r.Allowed.IsRestricted()
}

// DeniesAll reports whether no model can pass: everything is blocked, or
// nothing is admitted.
func (r ModelAccessRule) DeniesAll() bool {
	return r.Blocked.IsBlockAll() || (r.Allowed.IsEmpty() && r.AllowedPatterns.IsEmpty())
}

// Validate checks all four lists. The error names the offending side using
// the given field names so handlers can surface it verbatim.
func (r ModelAccessRule) Validate() error {
	if err := r.Allowed.Validate(); err != nil {
		return fmt.Errorf("allowed models: %w", err)
	}
	if err := r.Blocked.Validate(); err != nil {
		return fmt.Errorf("blocked models: %w", err)
	}
	if err := r.AllowedPatterns.Validate(); err != nil {
		return fmt.Errorf("allowed model patterns: %w", err)
	}
	if err := r.BlockedPatterns.Validate(); err != nil {
		return fmt.Errorf("blocked model patterns: %w", err)
	}
	return nil
}
