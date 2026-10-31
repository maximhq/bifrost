package openapimcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// ErrSpecTooLarge is returned when a spec exceeds MaxSpecBytes.
var ErrSpecTooLarge = fmt.Errorf("openapi spec exceeds %d bytes", MaxSpecBytes)

// ErrSpecURLInvalid marks a spec_url that cannot be fetched no matter how often
// it is retried (wrong scheme, malformed URL).
var ErrSpecURLInvalid = errors.New("invalid spec_url")

// defaultFetchTimeout bounds a spec fetch when the supplied client has no timeout.
const defaultFetchTimeout = 15 * time.Second

// LoadBytes accepts a spec supplied as text (paste, upload, inline config).
func LoadBytes(data []byte) ([]byte, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, errors.New("openapi spec is empty")
	}
	if len(data) > MaxSpecBytes {
		return nil, ErrSpecTooLarge
	}
	return data, nil
}

// LoadFile reads a spec from disk. A relative path is resolved against baseDir
// (the config directory) and must not escape it, judged by the file actually
// read: symlinks are resolved before the check. Only regular files are read,
// and the read itself is capped, so a special file or one that grows after the
// size check cannot block or exhaust memory.
func LoadFile(path, baseDir string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("spec_file is empty")
	}
	full := path
	if !filepath.IsAbs(full) {
		if baseDir == "" {
			return nil, fmt.Errorf("spec_file %q is relative but no config directory is known", path)
		}
		full = filepath.Join(baseDir, path)
	}
	full = filepath.Clean(full)
	if baseDir != "" {
		if !withinDir(filepath.Clean(baseDir), full) {
			return nil, fmt.Errorf("spec_file %q resolves outside the config directory", path)
		}
		realBase, err := filepath.EvalSymlinks(baseDir)
		if err != nil {
			return nil, fmt.Errorf("config directory: %w", err)
		}
		realFull, err := filepath.EvalSymlinks(full)
		if err != nil {
			return nil, fmt.Errorf("spec_file %q: %w", path, err)
		}
		if !withinDir(realBase, realFull) {
			return nil, fmt.Errorf("spec_file %q resolves outside the config directory", path)
		}
		full = realFull
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("spec_file %q: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("spec_file %q is a directory", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("spec_file %q is not a regular file", path)
	}
	if info.Size() > MaxSpecBytes {
		return nil, ErrSpecTooLarge
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, fmt.Errorf("spec_file %q: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(MaxSpecBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("spec_file %q: %w", path, err)
	}
	if len(data) > MaxSpecBytes {
		return nil, ErrSpecTooLarge
	}
	return LoadBytes(data)
}

// withinDir reports whether path is dir or lies under it.
func withinDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// LoadURL fetches a spec over HTTP(S) with the supplied client. The caller picks
// the client, and with it the network policy (SSRF guard, proxy, TLS). Bodies are
// read through a hard cap so a hostile server cannot exhaust memory. It also
// returns the document's final URL after redirects, which is what relative
// servers resolve against.
func LoadURL(ctx context.Context, client *http.Client, specURL string) ([]byte, string, error) {
	specURL = strings.TrimSpace(specURL)
	if specURL == "" {
		return nil, "", errors.New("spec_url is empty")
	}
	if !strings.HasPrefix(specURL, "http://") && !strings.HasPrefix(specURL, "https://") {
		return nil, "", fmt.Errorf("%w: %q must be an http(s) URL", ErrSpecURLInvalid, specURL)
	}
	if _, err := url.ParseRequestURI(specURL); err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrSpecURLInvalid, err)
	}
	if client == nil {
		// The client carries the network policy (SSRF guard, proxy, TLS); there
		// is no safe default to fall back to.
		return nil, "", fmt.Errorf("%w: fetching it requires an HTTP client", ErrSpecURLInvalid)
	}
	if client.Timeout == 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultFetchTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, specURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrSpecURLInvalid, err)
	}
	req.Header.Set("Accept", "application/json, application/yaml, application/x-yaml, text/yaml, text/plain;q=0.9, */*;q=0.1")
	req.Header.Set("User-Agent", "Bifrost-OpenAPI-MCP/1.0")
	resp, err := client.Do(req)
	if err != nil {
		// The query string may carry a token, and this error reaches logs.
		return nil, "", fmt.Errorf("fetching spec_url: %w", sanitizeURLError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("fetching spec_url: upstream returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(MaxSpecBytes)+1))
	if err != nil {
		return nil, "", fmt.Errorf("reading spec_url body: %w", err)
	}
	if len(data) > MaxSpecBytes {
		return nil, "", ErrSpecTooLarge
	}
	finalURL := specURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	data, err = LoadBytes(data)
	return data, finalURL, err
}

// ResolveSpec picks the spec source from an openapi_config in priority order:
// inline spec, spec_file (relative to baseDir), spec_url (fetched with client).
// It returns the bytes, which source supplied them ("inline", "file", "url"),
// and the URL relative servers resolve against: the fetched document's final
// URL, or spec_url as given for the other sources.
func ResolveSpec(ctx context.Context, cfg *schemas.MCPOpenAPIConfig, client *http.Client, baseDir string) ([]byte, string, string, error) {
	if cfg == nil {
		return nil, "", "", errors.New("openapi_config is nil")
	}
	specURL := ""
	if cfg.SpecURL != nil {
		specURL = strings.TrimSpace(*cfg.SpecURL)
	}
	if strings.TrimSpace(cfg.Spec) != "" {
		data, err := LoadBytes([]byte(cfg.Spec))
		return data, "inline", specURL, err
	}
	if cfg.SpecFile != nil && strings.TrimSpace(*cfg.SpecFile) != "" {
		data, err := LoadFile(*cfg.SpecFile, baseDir)
		return data, "file", specURL, err
	}
	if specURL != "" {
		data, finalURL, err := LoadURL(ctx, client, specURL)
		return data, "url", finalURL, err
	}
	return nil, "", "", errors.New("openapi_config has no spec, spec_file or spec_url")
}
