package lib

import (
	"fmt"
	"io/fs"
	"strings"
	"sync/atomic"
)

// basePath holds the resolved server.base_path, set once in Bootstrap before
// the server starts serving.
var basePath atomic.Pointer[string]

// SetBasePath records the resolved (normalized) base path for BasePath.
func SetBasePath(path string) {
	basePath.Store(&path)
}

// BasePath returns the resolved server.base_path, or "" at the domain root.
func BasePath() string {
	if p := basePath.Load(); p != nil {
		return *p
	}
	return ""
}

// WithBasePath prefixes a root-relative dashboard or API path ("/workspace/...")
// with the base path, for Location headers and links sent to browsers.
func WithBasePath(path string) string {
	return BasePath() + path
}

// NormalizeBasePath validates the URL path prefix Bifrost is mounted under
// behind a reverse proxy (e.g. "/bifrost" for https://abc.com/bifrost) and
// returns it in canonical form: a leading slash, no trailing slash, and "" for
// the domain root. Accepted segments are limited to unreserved URL characters
// so the value can be spliced into HTML, cookies, and Location headers as-is.
// Surrounding whitespace is rejected rather than trimmed, matching the
// server.base_path pattern in config.schema.json.
func NormalizeBasePath(raw string) (string, error) {
	value := raw
	if value == "" || value == "/" {
		return "", nil
	}
	if strings.TrimSpace(value) != value {
		return "", fmt.Errorf("base path %q must not have surrounding whitespace", raw)
	}
	if !strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("base path %q must start with /", raw)
	}
	value = strings.TrimSuffix(value, "/")
	for _, segment := range strings.Split(value[1:], "/") {
		if segment == "" {
			return "", fmt.Errorf("base path %q must not contain empty segments", raw)
		}
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("base path %q must not contain . or .. segments", raw)
		}
		for _, r := range segment {
			if !isBasePathRune(r) {
				return "", fmt.Errorf("base path %q contains invalid character %q", raw, r)
			}
		}
	}
	return value, nil
}

// ResolveBasePath picks the base path from the -base-path flag (or the
// BIFROST_BASE_PATH env it defaults from) and falls back to server.base_path
// from config.json, then normalizes it. Only an empty flag value counts as
// unset; a whitespace-only one is passed on and rejected by NormalizeBasePath.
func ResolveBasePath(cliValue string, serverConfig *ServerConfig) (string, error) {
	value := cliValue
	if value == "" && serverConfig != nil {
		value = serverConfig.BasePath
	}
	return NormalizeBasePath(value)
}

// CheckBasePathCollision rejects a base path whose first segment is also the
// first segment of a registered route. Requests are accepted with or without
// the prefix, so such a prefix would make "/<segment>/..." ambiguous between a
// prefixed request and a direct hit on the route. The top-level entries of the
// embedded UI (uiContent's "ui" directory, e.g. "assets") are reserved too:
// they are served by the "/{filepath:*}" catch-all, so they never appear as
// router paths. uiContent may be nil or empty.
func CheckBasePathCollision(basePath string, routes map[string][]string, uiContent fs.FS) error {
	if basePath == "" {
		return nil
	}
	first := firstPathSegment(basePath)
	for _, paths := range routes {
		for _, path := range paths {
			if firstPathSegment(path) == first {
				return fmt.Errorf("base path %q collides with route %q; choose a prefix whose first segment is not used by Bifrost", basePath, path)
			}
		}
	}
	if uiContent != nil {
		// A missing "ui" directory (no embedded UI) reserves nothing.
		entries, _ := fs.ReadDir(uiContent, "ui")
		for _, entry := range entries {
			if entry.Name() == first {
				return fmt.Errorf("base path %q collides with UI path %q; choose a prefix whose first segment is not used by Bifrost", basePath, "/"+entry.Name())
			}
		}
	}
	return nil
}

// firstPathSegment returns the first segment of a path, without slashes
// ("/api/providers" -> "api").
func firstPathSegment(path string) string {
	path = strings.TrimPrefix(path, "/")
	if i := strings.IndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return path
}

// isBasePathRune reports whether r is an unreserved URL character (RFC 3986)
// allowed in a base path segment.
func isBasePathRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
		r == '-' || r == '_' || r == '.' || r == '~'
}
