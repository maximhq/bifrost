package tables

import (
	"strings"
)

const (
	// MaxVirtualKeyMetadataEntries caps how many metadata entries one virtual key can carry.
	MaxVirtualKeyMetadataEntries = MaxMetadataEntries
	// MaxVirtualKeyMetadataKeyLength caps the length of a metadata key. It matches the log store's
	// metadata key limit, so every key a virtual key carries can also be filtered on in the logs.
	MaxVirtualKeyMetadataKeyLength = MaxMetadataKeyLength
	// MaxVirtualKeyMetadataValueLength caps the length of a metadata value, in characters.
	MaxVirtualKeyMetadataValueLength = MaxMetadataValueLength
)

// virtualKeyMetadataReservedKeys are log metadata keys the system writes itself. A virtual key
// may not claim them: its metadata is merged into the log row's metadata.
var virtualKeyMetadataReservedKeys = map[string]struct{}{
	"isAsyncRequest": {},
}

// virtualKeyMetadataLoadBalancerPrefix is the log metadata key prefix the enterprise load balancer
// writes under (schemas.LoadBalancerMetadataPrefix in core, which the log store also treats as
// system metadata). It is duplicated here rather than referenced so this module keeps building
// against the published core release it pins; the two values must stay identical.
const virtualKeyMetadataLoadBalancerPrefix = "bifrost_alb_"

// isVirtualKeyMetadataReservedKey reports whether key is a system log metadata key
// (isAsyncRequest or anything under the load balancer's metadata prefix).
func isVirtualKeyMetadataReservedKey(key string) bool {
	_, reserved := virtualKeyMetadataReservedKeys[key]
	return reserved || strings.HasPrefix(key, virtualKeyMetadataLoadBalancerPrefix)
}

// IsValidVirtualKeyMetadataKey reports whether key is acceptable as a virtual key metadata key.
func IsValidVirtualKeyMetadataKey(key string) bool {
	return IsValidMetadataKey(key)
}

// ValidateVirtualKeyMetadata checks a virtual key's metadata map with ValidateMetadata and also
// refuses keys reserved by the system, because the metadata is merged into the log row's
// metadata. A nil or empty map is valid. Errors name the offending key so API callers can fix it.
func ValidateVirtualKeyMetadata(metadata map[string]string) error {
	return ValidateMetadata(metadata, isVirtualKeyMetadataReservedKey)
}
