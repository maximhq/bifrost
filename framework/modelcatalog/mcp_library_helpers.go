// Package-local helpers and constants used by mcp_library_sync.go. These
// were referenced unqualified from the synced file but never defined in
// the modelcatalog package — analogues live in framework/modelcatalog/datasheet
// as unexported symbols and aren't importable. Defining package-local copies
// keeps mcp_library_sync.go compilable from the canonical upstream Dockerfile
// (transports/Dockerfile) instead of requiring Dockerfile.local + go workspace.
//
// Filed in response to a clean-Docker build failure on upstream/dev tip
// 9908b7c5 — see the corresponding issue/PR for context.

package modelcatalog

import (
	"context"
	"regexp"
	"strings"
	"time"
)

const (
	// urlFetchMaxRetries is the number of retries (after the first attempt)
	// for transient errors fetching the MCP library catalog. 1 initial +
	// 3 retries = 4 attempts total. Mirrors datasheet's value for consistency.
	urlFetchMaxRetries = 3

	// urlFetchMaxBackoff caps the exponential backoff between retries. Steps
	// start at retryBackoffMin (1s) and double until they hit this ceiling.
	urlFetchMaxBackoff = 10 * time.Second

	// retryBackoffMin is the first-step backoff between MCP library fetch
	// retries. Doubles each attempt up to urlFetchMaxBackoff.
	retryBackoffMin = 1 * time.Second

	// maxMCPLibraryBodyBytes is the upper bound on the JSON envelope size
	// the remote MCP library catalog endpoint may return. Catalogs are
	// typically <100 KB; 10 MB is comfortably above any reasonable payload
	// while still bounding memory on a hostile/misconfigured endpoint.
	maxMCPLibraryBodyBytes = 10 * 1024 * 1024
)

// slugRE matches one-or-more characters that are NOT lowercase letters or
// digits. Used by Slugify to collapse separators into a single "-".
var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify converts a free-form name into a URL-safe slug suitable for use
// as a primary key on TableMCPLibrary. The transformation is:
//  1. lowercase + trim whitespace
//  2. collapse any run of non-alphanumeric characters into a single "-"
//  3. trim leading/trailing "-"
//
// Examples:
//
//	"GitHub Enterprise"     → "github-enterprise"
//	"  My-Server (v2)  "   → "my-server-v2"
//	"acme.SERVER/path?q=1" → "acme-server-path-q-1"
func Slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = slugRE.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// WithRetries runs op until it succeeds or maxRetries retries are exhausted
// (1 initial attempt + maxRetries retries). After each failure it waits with
// exponential backoff starting at retryBackoffMin, capped at maxBackoff when
// > 0. If maxBackoff is zero, the delay grows unbounded.
//
// Exported (capitalized) to match the call site in mcp_library_sync.go.
// Mirrors the unexported `withRetries` in framework/modelcatalog/datasheet —
// keeping a package-local copy avoids cross-subpackage coupling.
func WithRetries[T any](ctx context.Context, maxRetries int, maxBackoff time.Duration, op func() (T, error)) (T, error) {
	var zero T
	if maxRetries < 0 {
		maxRetries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		default:
		}

		if attempt > 0 {
			backoff := retryBackoffMin * time.Duration(1<<uint(attempt-1))
			if maxBackoff > 0 && backoff > maxBackoff {
				backoff = maxBackoff
			}
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-time.After(backoff):
			}
		}
		v, err := op()
		if err == nil {
			return v, nil
		}
		lastErr = err
	}
	return zero, lastErr
}
