package tables

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Shared limits for operator-defined labels: key/value metadata (virtual keys, providers) and
// tags (providers, models).
const (
	// MaxMetadataEntries caps how many metadata entries one entity can carry.
	MaxMetadataEntries = 50
	// MaxMetadataKeyLength caps the length of a metadata key. It matches the log store's metadata
	// key limit, so a key is always usable as a metadata_<key> filter.
	MaxMetadataKeyLength = 256
	// MaxMetadataValueLength caps the length of a metadata value, in characters.
	MaxMetadataValueLength = 512
	// MaxTags caps how many tags one entity can carry.
	MaxTags = 50
	// MaxTagLength caps the length of a tag. Tags are short labels ("prod", "approved-for-pii",
	// "eu-west-1") sent back as a comma-separated tags= filter, so they are kept to the size of a
	// Kubernetes label value rather than the size of a metadata value.
	MaxTagLength = 64
)

// metadataKeyRegex is the log store's metadata key rule (alphanumerics, '.', '_' and '-'), so a
// metadata key can be used as a JSON path in a metadata filter.
var metadataKeyRegex = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// tagRegex allows the same characters as metadata keys. ',' is excluded, which keeps the
// comma-separated tags= list filter unambiguous.
var tagRegex = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// IsValidMetadataKey reports whether key is acceptable as a metadata key.
func IsValidMetadataKey(key string) bool {
	return key != "" && len(key) <= MaxMetadataKeyLength && metadataKeyRegex.MatchString(key)
}

// ValidateMetadata checks a metadata map: at most MaxMetadataEntries entries, keys matching
// IsValidMetadataKey and values of at most MaxMetadataValueLength characters. isReserved, when
// non-nil, refuses keys the caller keeps for system use. A nil or empty map is valid. Keys are
// checked in sorted order so the error for a map with several bad keys is deterministic, and
// errors name the offending key so API callers can fix it.
func ValidateMetadata(metadata map[string]string, isReserved func(key string) bool) error {
	if len(metadata) > MaxMetadataEntries {
		return fmt.Errorf("metadata can have at most %d entries, got %d", MaxMetadataEntries, len(metadata))
	}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !IsValidMetadataKey(key) {
			return fmt.Errorf("invalid metadata key %q: keys must be 1-%d characters of letters, digits, '.', '_' or '-'", key, MaxMetadataKeyLength)
		}
		if isReserved != nil && isReserved(key) {
			return fmt.Errorf("invalid metadata key %q: the key is reserved for system metadata", key)
		}
		if n := utf8.RuneCountInString(metadata[key]); n > MaxMetadataValueLength {
			return fmt.Errorf("invalid metadata value for key %q: values can be at most %d characters, got %d", key, MaxMetadataValueLength, n)
		}
	}
	return nil
}

// IsValidTag reports whether tag is acceptable as a tag (after trimming).
func IsValidTag(tag string) bool {
	return tag != "" && len(tag) <= MaxTagLength && tagRegex.MatchString(tag)
}

// NormalizeTags trims each tag, validates it, removes duplicates and sorts the result, so a tag
// set has one stored representation and one config hash regardless of input order. Tags are
// case-sensitive. A nil or empty input (or one that is empty after normalization) returns nil.
// The MaxTags cap applies to the input list as sent, duplicates included: config.schema.json
// caps the config-file array with maxItems, so the API accepts exactly what the file accepts,
// and the work done on an untrusted list stays bounded.
func NormalizeTags(tags []string) ([]string, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	if len(tags) > MaxTags {
		return nil, fmt.Errorf("tags can have at most %d entries, got %d", MaxTags, len(tags))
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, raw := range tags {
		tag := strings.TrimSpace(raw)
		if !IsValidTag(tag) {
			return nil, fmt.Errorf("invalid tag %q: tags must be 1-%d characters of letters, digits, '.', '_' or '-'", raw, MaxTagLength)
		}
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	sort.Strings(out)
	return out, nil
}

// HasAllTags reports whether have contains every tag in want. An empty want matches everything.
func HasAllTags(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}
