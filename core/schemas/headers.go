package schemas

import "strings"

// MatchHeaderPattern reports whether a lowercased header name matches a pattern.
// Supports exact match, "*" (all), and a single trailing-wildcard prefix (e.g. "x-custom-*").
// The pattern is trimmed and lowercased before comparison; the header name is expected to
// already be lowercased by the caller.
func MatchHeaderPattern(headerName, pattern string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(headerName, prefix)
	}
	return headerName == pattern
}

// RedactSensitiveHeaders replaces the value of every credential-bearing header
// (per IsSensitiveHeader) with RedactedAttrValue, keeping the key so header
// presence is still visible in telemetry. Mutates and returns the given map;
// safe on nil. Call this before exporting captured request headers to any
// observability backend, since FilterHeaders itself does no redaction.
func RedactSensitiveHeaders(headers map[string]string) map[string]string {
	for name := range headers {
		if IsSensitiveHeader(name) {
			headers[name] = RedactedAttrValue
		}
	}
	return headers
}

// FilterHeaders returns the subset of headers whose (lowercased) keys match any of the
// given patterns (exact name or wildcard like "x-custom-*" or "*"). Header keys are
// expected to already be lowercased by the capture layer. Returns nil when nothing matches.
func FilterHeaders(headers map[string]string, patterns []string) map[string]string {
	if len(headers) == 0 || len(patterns) == 0 {
		return nil
	}
	out := make(map[string]string)
	for name, value := range headers {
		for _, pattern := range patterns {
			if MatchHeaderPattern(name, pattern) {
				out[name] = value
				break
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// credentialQueryParams are credential query parameter names that IsSensitiveHeader does not name.
var credentialQueryParams = map[string]struct{}{
	"key": {}, "apikey": {}, "token": {}, "password": {}, "pwd": {}, "sig": {}, "signature": {},
	"x-amz-signature": {}, "x-amz-credential": {},
}

// IsCredentialQueryParam reports whether a query parameter carries a credential: a name the header
// rules flag once "_" is read as "-", or a well-known credential parameter. Relay job validation and
// Relay Logs redaction share it, so a name refused on save is also masked in logs.
func IsCredentialQueryParam(name string) bool {
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "_", "-")
	if _, ok := credentialQueryParams[normalized]; ok {
		return true
	}
	if _, ok := credentialQueryParams[strings.ReplaceAll(normalized, "-", "")]; ok {
		return true
	}
	return IsSensitiveHeader(normalized)
}
