package clis

import (
	"regexp"
	"strings"
)

// errorPatterns matches strings that signal a transport, upstream, or
// model-execution failure that the CLI may have rendered into stdout/stderr
// without setting a non-zero exit. We scan the combined transcript in
// addition to checking the exit code.
//
// Patterns are deliberately narrow: matching JSON keys like "error":
// would false-positive against every successful structured-output run, since
// both Claude stream-json and `codex --json` always include is_error / error
// fields in their result events.
var errorPatterns = []*regexp.Regexp{
	// Plain-prose error markers that real CLIs print on failure.
	regexp.MustCompile(`(?i)API Error`),
	regexp.MustCompile(`(?i)Request failed`),
	regexp.MustCompile(`(?i)\brate[_ -]?limit\b`),
	regexp.MustCompile(`\b(?:401|403|429|5\d{2})\b\s+(?i)(?:error|status)`),
	regexp.MustCompile(`(?i)authentication failed`),
	regexp.MustCompile(`(?i)invalid[_ ]api[_ ]key`),
	regexp.MustCompile(`(?i)upstream error`),
	regexp.MustCompile(`(?i)connection refused`),
	regexp.MustCompile(`context deadline exceeded`),
	regexp.MustCompile(`panic:`),
	regexp.MustCompile(`Unhandled exception`),
	// "Error: <something>" at line start or after whitespace - matches CLI
	// stderr formatting but skips JSON keys (which are preceded by a quote).
	// Go regexp doesn't have lookbehind, so we anchor with (?:^|[^"\w]).
	regexp.MustCompile(`(?m)(?:^|[^"\w])Error:\s+\S`),
	// Explicit structured-output failure signals from Claude / Codex.
	regexp.MustCompile(`"is_error"\s*:\s*true`),
	regexp.MustCompile(`"type"\s*:\s*"error"`),
}

// detectError returns the first matching pattern + a snippet, or empty if
// nothing suspicious is in the transcript. Substrings in `ignore` are removed
// before scanning so a scenario can whitelist its own sentinel tokens.
func detectError(clean string, ignore []string) (pattern, snippet string, ok bool) {
	haystack := clean
	for _, skip := range ignore {
		haystack = strings.ReplaceAll(haystack, skip, "")
	}
	for _, re := range errorPatterns {
		loc := re.FindStringIndex(haystack)
		if loc == nil {
			continue
		}
		start := loc[0] - 80
		if start < 0 {
			start = 0
		}
		end := loc[1] + 160
		if end > len(haystack) {
			end = len(haystack)
		}
		return re.String(), haystack[start:end], true
	}
	return "", "", false
}
