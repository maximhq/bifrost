package openapimcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// ErrSpecTooLarge is returned when a spec exceeds MaxSpecBytes.
var ErrSpecTooLarge = fmt.Errorf("openapi spec exceeds %d bytes", MaxSpecBytes)

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
// (the config directory) and must not escape it.
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
		rel, err := filepath.Rel(filepath.Clean(baseDir), full)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("spec_file %q resolves outside the config directory", path)
		}
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("spec_file %q: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("spec_file %q is a directory", path)
	}
	if info.Size() > MaxSpecBytes {
		return nil, ErrSpecTooLarge
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("spec_file %q: %w", path, err)
	}
	return LoadBytes(data)
}

// LoadURL fetches a spec over HTTP(S) with the supplied client. The caller picks
// the client, and with it the network policy (SSRF guard, proxy, TLS). Bodies are
// read through a hard cap so a hostile server cannot exhaust memory.
func LoadURL(ctx context.Context, client *http.Client, specURL string) ([]byte, error) {
	specURL = strings.TrimSpace(specURL)
	if specURL == "" {
		return nil, errors.New("spec_url is empty")
	}
	if !strings.HasPrefix(specURL, "http://") && !strings.HasPrefix(specURL, "https://") {
		return nil, fmt.Errorf("spec_url %q must be an http(s) URL", specURL)
	}
	if client == nil {
		client = &http.Client{Timeout: defaultFetchTimeout}
	}
	if client.Timeout == 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultFetchTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, specURL, nil)
	if err != nil {
		return nil, fmt.Errorf("spec_url: %w", err)
	}
	req.Header.Set("Accept", "application/json, application/yaml, application/x-yaml, text/yaml, text/plain;q=0.9, */*;q=0.1")
	req.Header.Set("User-Agent", "Bifrost-OpenAPI-MCP/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching spec_url: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetching spec_url: upstream returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(MaxSpecBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("reading spec_url body: %w", err)
	}
	if len(data) > MaxSpecBytes {
		return nil, ErrSpecTooLarge
	}
	return LoadBytes(data)
}

// ResolveSpec picks the spec source from an openapi_config in priority order:
// inline spec, spec_file (relative to baseDir), spec_url (fetched with client).
// It returns the bytes and which source supplied them ("inline", "file", "url").
func ResolveSpec(ctx context.Context, cfg *schemas.MCPOpenAPIConfig, client *http.Client, baseDir string) ([]byte, string, error) {
	if cfg == nil {
		return nil, "", errors.New("openapi_config is nil")
	}
	if strings.TrimSpace(cfg.Spec) != "" {
		data, err := LoadBytes([]byte(cfg.Spec))
		return data, "inline", err
	}
	if cfg.SpecFile != nil && strings.TrimSpace(*cfg.SpecFile) != "" {
		data, err := LoadFile(*cfg.SpecFile, baseDir)
		return data, "file", err
	}
	if cfg.SpecURL != nil && strings.TrimSpace(*cfg.SpecURL) != "" {
		data, err := LoadURL(ctx, client, *cfg.SpecURL)
		return data, "url", err
	}
	return nil, "", errors.New("openapi_config has no spec, spec_file or spec_url")
}
