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
