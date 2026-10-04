package tables

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// MaxVirtualKeyMetadataEntries caps how many metadata entries one virtual key can carry.
	MaxVirtualKeyMetadataEntries = 50
	// MaxVirtualKeyMetadataKeyLength caps the length of a metadata key. It matches the log store's
	// metadata key limit, so every key a virtual key carries can also be filtered on in the logs.
	MaxVirtualKeyMetadataKeyLength = 256
	// MaxVirtualKeyMetadataValueLength caps the length of a metadata value, in characters.
	MaxVirtualKeyMetadataValueLength = 512
)

// virtualKeyMetadataKeyRegex is the log store's metadata key rule (alphanumerics, '.', '_' and
// '-'). Keys are stamped onto log rows and used as JSON paths in metadata filters, so a key that
// the log store would refuse to filter on is refused here up front.
var virtualKeyMetadataKeyRegex = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// virtualKeyMetadataReservedKeys are log metadata keys the system writes itself. A virtual key
// may not claim them: its metadata is merged into the log row's metadata.
var virtualKeyMetadataReservedKeys = map[string]struct{}{
	"isAsyncRequest": {},
}

// IsValidVirtualKeyMetadataKey reports whether key is acceptable as a virtual key metadata key.
func IsValidVirtualKeyMetadataKey(key string) bool {
	return key != "" && len(key) <= MaxVirtualKeyMetadataKeyLength && virtualKeyMetadataKeyRegex.MatchString(key)
}

// ValidateVirtualKeyMetadata checks a virtual key's metadata map: at most
// MaxVirtualKeyMetadataEntries entries, keys matching the log store's metadata key rule and not
// reserved by the system, and values of at most MaxVirtualKeyMetadataValueLength characters.
// A nil or empty map is valid. Errors name the offending key so API callers can fix it.
func ValidateVirtualKeyMetadata(metadata map[string]string) error {
	if len(metadata) > MaxVirtualKeyMetadataEntries {
		return fmt.Errorf("metadata can have at most %d entries, got %d", MaxVirtualKeyMetadataEntries, len(metadata))
	}
	// Sorted so the error for a map with several bad keys is deterministic.
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !IsValidVirtualKeyMetadataKey(key) {
			return fmt.Errorf("invalid metadata key %q: keys must be 1-%d characters of letters, digits, '.', '_' or '-'", key, MaxVirtualKeyMetadataKeyLength)
		}
		if _, reserved := virtualKeyMetadataReservedKeys[key]; reserved || strings.HasPrefix(key, schemas.LoadBalancerMetadataPrefix) {
			return fmt.Errorf("invalid metadata key %q: the key is reserved for system metadata", key)
		}
		if n := utf8.RuneCountInString(metadata[key]); n > MaxVirtualKeyMetadataValueLength {
			return fmt.Errorf("invalid metadata value for key %q: values can be at most %d characters, got %d", key, MaxVirtualKeyMetadataValueLength, n)
		}
	}
	return nil
}
