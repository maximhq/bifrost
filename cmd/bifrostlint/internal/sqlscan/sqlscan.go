// Package sqlscan provides helpers for extracting SQL text from Go source.
//
// The analyzers look at every string literal in the package and ask whether
// the literal contains a SQL keyword of interest. Matching is regex-based so
// we don't false-positive on prose ("create index connection") or on GORM
// struct tags ("gorm:\"index;not null\" json:\"created_at\"") that happen to
// contain the same words.
package sqlscan

import (
	"go/ast"
	"go/token"
	"regexp"
	"strconv"
	"strings"
)

// LiteralValue returns the unquoted string value of lit, or "" if lit is not
// a STRING literal.
func LiteralValue(lit *ast.BasicLit) string {
	if lit == nil || lit.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return s
}

// reCreateIndex matches a Postgres CREATE INDEX DDL statement: must have
// CREATE INDEX, an ON keyword, and an opening paren for the column list. The
// paren requirement disambiguates real DDL from English prose like
// "failed to create index on foo: %w".
var reCreateIndex = regexp.MustCompile(`(?is)\bCREATE\s+(UNIQUE\s+)?INDEX\b[^;]*\bON\b[^;()]*\(`)

// reMatViewCreate matches a CREATE MATERIALIZED VIEW DDL.
var reMatViewCreate = regexp.MustCompile(`(?is)\bCREATE\s+MATERIALIZED\s+VIEW\b`)

// reMatViewRefresh matches a REFRESH MATERIALIZED VIEW statement.
var reMatViewRefresh = regexp.MustCompile(`(?is)\bREFRESH\s+MATERIALIZED\s+VIEW\b`)

// reConcurrently matches the CONCURRENTLY keyword as a SQL token.
var reConcurrently = regexp.MustCompile(`(?i)\bCONCURRENTLY\b`)

// IsCreateIndex reports whether s contains a CREATE INDEX DDL statement.
func IsCreateIndex(s string) bool { return reCreateIndex.MatchString(s) }

// IsMaterializedViewOp reports whether s contains a CREATE/REFRESH
// MATERIALIZED VIEW statement.
func IsMaterializedViewOp(s string) bool {
	return reMatViewCreate.MatchString(s) || reMatViewRefresh.MatchString(s)
}

// HasConcurrently reports whether s contains the CONCURRENTLY keyword.
func HasConcurrently(s string) bool { return reConcurrently.MatchString(s) }

// ContainsKeyword reports whether s contains the given keyword as a SQL
// token (case-insensitive, word-bounded).
func ContainsKeyword(s, keyword string) bool {
	// build a word-bounded matcher; cache via a small map per token would help
	// but the analyzer pass count is small.
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(keyword) + `\b`)
	return re.MatchString(s)
}

// ContainsAnyKeyword reports whether s contains any of needles as SQL tokens.
func ContainsAnyKeyword(s string, needles ...string) bool {
	for _, n := range needles {
		if ContainsKeyword(s, n) {
			return true
		}
	}
	return false
}

// ContainsPhrase reports whether s contains the given multi-word phrase as a
// SQL token sequence (case-insensitive, word-bounded, whitespace-flexible).
func ContainsPhrase(s string, words ...string) bool {
	if len(words) == 0 {
		return false
	}
	parts := make([]string, 0, len(words))
	for _, w := range words {
		parts = append(parts, regexp.QuoteMeta(w))
	}
	pattern := `(?i)\b` + strings.Join(parts, `\s+`) + `\b`
	re := regexp.MustCompile(pattern)
	return re.MatchString(s)
}
