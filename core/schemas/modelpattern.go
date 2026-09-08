package schemas

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// ModelRegexPrefix marks an allow/block list entry as a regular expression.
//
// An entry of the form "regex:<pattern>" is compiled as RE2 and matched
// case-insensitively as a full match against the bare model name and, when the
// caller knows the provider, against "<provider>/<model>". Any other entry is an
// exact, case-insensitive model name. The prefix itself is matched literally and
// is case-sensitive.
const ModelRegexPrefix = "regex:"

// modelPatternCache holds compiled patterns keyed by the raw list entry (prefix
// included). Entries are user data evaluated on every request, so compiling
// once per distinct entry matters. Only successful compiles are cached; invalid
// entries are rejected at write time by Validate.
var modelPatternCache sync.Map // map[string]*regexp.Regexp

// IsRegexEntry reports whether entry carries the regex marker.
func IsRegexEntry(entry string) bool {
	return strings.HasPrefix(entry, ModelRegexPrefix)
}

// RegexEntryPattern returns the raw pattern of a regex entry with the marker
// stripped, or "" when entry is not a regex entry.
func RegexEntryPattern(entry string) string {
	if !IsRegexEntry(entry) {
		return ""
	}
	return strings.TrimPrefix(entry, ModelRegexPrefix)
}

// CompileModelPattern compiles a regex entry into an anchored, case-insensitive
// RE2 expression and caches it. It returns an error when entry is not a regex
// entry, when the pattern is empty, or when RE2 rejects it.
func CompileModelPattern(entry string) (*regexp.Regexp, error) {
	if v, ok := modelPatternCache.Load(entry); ok {
		return v.(*regexp.Regexp), nil
	}
	if !IsRegexEntry(entry) {
		return nil, fmt.Errorf("entry %q is not a regex entry (expected %q prefix)", entry, ModelRegexPrefix)
	}
	pattern := RegexEntryPattern(entry)
	if strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("regex entry has an empty pattern")
	}
	re, err := regexp.Compile("(?i)^(?:" + pattern + ")$")
	if err != nil {
		return nil, err
	}
	actual, _ := modelPatternCache.LoadOrStore(entry, re)
	return actual.(*regexp.Regexp), nil
}

// ValidateModelEntry checks that a list entry is well-formed. Non-regex entries
// are always valid here; regex entries must compile.
func ValidateModelEntry(entry string) error {
	if !IsRegexEntry(entry) {
		return nil
	}
	_, err := CompileModelPattern(entry)
	return err
}

// MatchesEntry reports whether one list entry matches model.
//
// The "*" wildcard is not handled here; callers decide that through
// IsUnrestricted / IsBlockAll before consulting individual entries. A plain
// entry matches by case-insensitive equality. A regex entry matches when the
// compiled pattern fully matches model, or "<provider>/<model>" when provider is
// non-empty. An entry that fails to compile never matches.
func MatchesEntry(entry, model, provider string) bool {
	if !IsRegexEntry(entry) {
		return strings.EqualFold(entry, model)
	}
	re, err := CompileModelPattern(entry)
	if err != nil {
		return false
	}
	if re.MatchString(model) {
		return true
	}
	if provider != "" && re.MatchString(provider+"/"+model) {
		return true
	}
	return false
}
