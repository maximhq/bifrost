package schemas

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// RegexEntryPrefix marks a list entry as an RE2 pattern instead of an exact value.
// "regex:^gpt-4.*" allows or blocks every model whose name starts with gpt-4.
const RegexEntryPrefix = "regex:"

// regexEntryCache holds compiled patterns keyed by the raw entry text.
var regexEntryCache sync.Map

// IsRegexEntry reports whether entry carries the regex: prefix.
func IsRegexEntry(entry string) bool {
	return strings.HasPrefix(entry, RegexEntryPrefix)
}

// compileRegexEntry compiles the pattern behind the prefix as a full, case-insensitive match.
func compileRegexEntry(entry string) (*regexp.Regexp, error) {
	if cached, ok := regexEntryCache.Load(entry); ok {
		return cached.(*regexp.Regexp), nil
	}
	pattern := strings.TrimPrefix(entry, RegexEntryPrefix)
	if pattern == "" {
		return nil, fmt.Errorf("regex entry %q has no pattern", entry)
	}
	if pattern == "*" {
		return nil, fmt.Errorf("regex entry %q is not a pattern; use a bare \"*\" to allow or block everything", entry)
	}
	re, err := regexp.Compile("(?i)^(?:" + pattern + ")$")
	if err != nil {
		return nil, fmt.Errorf("regex entry %q does not compile: %v", entry, err)
	}
	regexEntryCache.Store(entry, re)
	return re, nil
}

// MatchEntry reports whether a list entry matches value: an exact, case-insensitive
// comparison for plain entries, a full RE2 match for regex: entries. An entry that
// does not compile never matches.
func MatchEntry(entry, value string) bool {
	if !IsRegexEntry(entry) {
		return strings.EqualFold(entry, value)
	}
	re, err := compileRegexEntry(entry)
	if err != nil {
		return false
	}
	return re.MatchString(value)
}

// ValidateRegexEntries returns the first regex: entry in entries that is empty,
// the bare wildcard, or does not compile. Plain entries are not inspected.
func ValidateRegexEntries(entries []string) error {
	for _, entry := range entries {
		if !IsRegexEntry(entry) {
			continue
		}
		if _, err := compileRegexEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

// ExpandRegexEntry matches value against a regex: entry and expands template with its capture groups ($1, ${name}).
func ExpandRegexEntry(entry, value, template string) (string, bool) {
	if !IsRegexEntry(entry) {
		return "", false
	}
	re, err := compileRegexEntry(entry)
	if err != nil {
		return "", false
	}
	match := re.FindStringSubmatchIndex(value)
	if match == nil {
		return "", false
	}
	return string(re.ExpandString(nil, template, value, match)), true
}

// ValidateRegexTemplate rejects a template that references a capture group the regex: entry does not define.
func ValidateRegexTemplate(entry, template string) error {
	re, err := compileRegexEntry(entry)
	if err != nil {
		return err
	}
	for _, ref := range templateGroupRefs(template) {
		if n, ok := templateGroupNumber(ref); ok {
			if n > re.NumSubexp() {
				return fmt.Errorf("template %q references group $%d but %q has %d capture group(s)", template, n, entry, re.NumSubexp())
			}
			continue
		}
		if re.SubexpIndex(ref) < 0 {
			return fmt.Errorf("template %q references group ${%s} that %q does not define", template, ref, entry)
		}
	}
	return nil
}

// templateGroupNumber reports the group number ref names by regexp.Expand's rule: digits only, no leading zero, at most nine of them.
func templateGroupNumber(ref string) (int, bool) {
	if ref == "" || len(ref) > 9 || (len(ref) > 1 && ref[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(ref); i++ {
		if ref[i] < '0' || ref[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(ref)
	return n, err == nil
}

// templateGroupRefs returns the group names and numbers a regexp.Expand template references, skipping escaped $$.
func templateGroupRefs(template string) []string {
	var refs []string
	for i := 0; i < len(template); i++ {
		if template[i] != '$' || i+1 >= len(template) {
			continue
		}
		next := template[i+1]
		if next == '$' {
			i++
			continue
		}
		if next == '{' {
			end := strings.IndexByte(template[i+2:], '}')
			if end < 0 {
				continue
			}
			refs = append(refs, template[i+2:i+2+end])
			i += 2 + end
			continue
		}
		j := i + 1
		for j < len(template) {
			r, size := utf8.DecodeRuneInString(template[j:])
			if !isTemplateNameRune(r) {
				break
			}
			j += size
		}
		if j > i+1 {
			refs = append(refs, template[i+1:j])
			i = j - 1
		}
	}
	return refs
}

// isTemplateNameRune reports whether r continues a $name reference, by the rule regexp.Expand uses.
func isTemplateNameRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
