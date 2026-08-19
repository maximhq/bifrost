package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

const (
	urlFetchMaxRetries     = 3                // retries after the first attempt (4 attempts total)
	urlFetchMaxBackoff     = 10 * time.Second // cap for exponential backoff (steps start at 1s)
	retryBackoffMin        = time.Second      // initial wait before the first retry
	maxMCPLibraryBodyBytes = 50 << 20         // 50 MiB — hard cap on the catalog payload to prevent OOM
)

// withRetries runs op up to maxRetries+1 times, waiting with exponential
// backoff (starting at retryBackoffMin, capped at maxBackoff) between attempts.
// It returns the first successful result or the last error. The context is
// honored during both the operation and the backoff waits.
func withRetries[T any](ctx context.Context, maxRetries int, maxBackoff time.Duration, op func() (T, error)) (T, error) {
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

// MCPLibraryEntry is the JSON shape a single server has in the remote MCP
// library catalog (the payload fetched from DefaultMCPLibraryURL / custom URL).
// The catalog carries no slug; it is derived from Name at sync time via
// Slugify. The remaining fields map onto TableMCPLibrary minus the DB-managed
// fields (ID, Slug, CreatedAt, UpdatedAt).
type MCPLibraryEntry struct {
	Name               string                    `json:"name"`
	Description        string                    `json:"description,omitempty"`
	Category           string                    `json:"category,omitempty"`
	ConnectionType     schemas.MCPConnectionType `json:"connection_type"`
	ConnectionURL      string                    `json:"connection_url,omitempty"`
	StdioConfig        *schemas.MCPStdioConfig   `json:"stdio_config,omitempty"`
	AuthType           schemas.MCPAuthType       `json:"auth_type,omitempty"`
	RequiredHeaderKeys []string                  `json:"required_header_keys,omitempty"`
	IconURL            string                    `json:"icon_url,omitempty"`
	DocsURL            string                    `json:"docs_url,omitempty"`
	Publisher          string                    `json:"publisher,omitempty"`
	Tags               []string                  `json:"tags,omitempty"`
	Metadata           map[string]any            `json:"metadata,omitempty"`
}

// MCPLibraryPayload is the top-level JSON envelope returned by the remote
// MCP library catalog endpoint.
type MCPLibraryPayload struct {
	Servers       []MCPLibraryEntry `json:"servers"`
	LastUpdatedAt string            `json:"lastUpdatedAt,omitempty"`
}

// SyncMCPLibrary fetches the MCP server catalog from url, parses the JSON
// payload, and upserts each row into the mcp_library table keyed by slug.
// Returns the number of rows upserted.
//
// The function is intentionally stateless and operates directly on the
// ConfigStore so it can be called from both the force-sync handler and the
// background worker without needing a dedicated manager struct.
func SyncMCPLibrary(ctx context.Context, url string, store configstore.ConfigStore) (int, error) {
	if url == "" {
		url = DefaultMCPLibraryURL
	}

	entries, err := withRetries(ctx, urlFetchMaxRetries, urlFetchMaxBackoff, func() ([]MCPLibraryEntry, error) {
		return fetchMCPLibrary(ctx, url)
	})
	if err != nil {
		return 0, fmt.Errorf("failed to fetch MCP library from %s: %w", url, err)
	}

	if len(entries) == 0 {
		return 0, nil
	}

	// Load the slugs the sync must not touch: org-internal ("custom") rows and
	// soft-deleted ("tombstoned") rows. A remote payload entry whose slug is in
	// this set is skipped silently so the rest of the payload still seeds.
	protected, err := store.GetProtectedMCPLibrarySlugs(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to load protected MCP library slugs: %w", err)
	}
	protectedSet := make(map[string]bool, len(protected))
	for _, slug := range protected {
		protectedSet[slug] = true
	}

	// Upsert all entries in a single transaction.
	count := 0
	err = store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		seen := make(map[string]bool, len(entries))
		for i := range entries {
			e := &entries[i]
			if e.Name == "" {
				continue // skip malformed entries
			}
			// The catalog payload carries no slug; derive it from the name,
			// matching the slug generation used for custom library entries.
			slug := Slugify(e.Name)
			if slug == "" {
				continue // name had no slug-able content
			}
			if protectedSet[slug] {
				continue // never overwrite custom or tombstoned rows
			}
			if seen[slug] {
				continue // deduplicate within the payload
			}
			seen[slug] = true

			now := time.Now()
			row := &configstoreTables.TableMCPLibrary{
				Slug:               slug,
				Name:               e.Name,
				Description:        e.Description,
				Category:           e.Category,
				ConnectionType:     e.ConnectionType,
				ConnectionURL:      e.ConnectionURL,
				StdioConfig:        e.StdioConfig,
				AuthType:           e.AuthType,
				RequiredHeaderKeys: e.RequiredHeaderKeys,
				IconURL:            e.IconURL,
				DocsURL:            e.DocsURL,
				Publisher:          e.Publisher,
				Tags:               e.Tags,
				Metadata:           e.Metadata,
				Source:             "remote",
				CreatedAt:          now,
				UpdatedAt:          now,
			}
			if err := store.UpsertMCPLibraryEntry(ctx, row, tx); err != nil {
				return fmt.Errorf("failed to upsert MCP library entry %q: %w", slug, err)
			}
			count++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("failed to sync MCP library to database: %w", err)
	}

	return count, nil
}

// syncMCPLibrary is the ModelCatalog method called by the background sync worker
// and ForceReloadPricing. It delegates to the stateless SyncMCPLibrary function
// using the catalog's configured URL and config store.
func (mc *ModelCatalog) syncMCPLibrary(ctx context.Context) error {
	if mc.shouldSyncGate != nil && !mc.shouldSyncGate(ctx) {
		mc.logger.Debug("MCP library sync cancelled by custom gate")
		return nil
	}
	_, err := mc.syncMCPLibraryNow(ctx)
	return err
}

// ForceReloadMCPLibrary triggers an immediate MCP library sync from the current
// configured source and advances the MCP library sync timer on success.
func (mc *ModelCatalog) ForceReloadMCPLibrary(ctx context.Context) (int, error) {
	if mc.shouldSyncGate != nil && !mc.shouldSyncGate(ctx) {
		mc.logger.Debug("MCP library sync cancelled by custom gate")
		return 0, nil
	}
	count, err := mc.syncMCPLibraryNow(ctx)
	if err != nil {
		return 0, err
	}
	mc.syncMu.Lock()
	mc.lastMCPLibrarySyncedAt = time.Now()
	mc.syncMu.Unlock()
	return count, nil
}

func (mc *ModelCatalog) syncMCPLibraryNow(ctx context.Context) (int, error) {
	if mc.configStore == nil {
		return 0, nil
	}
	url := mc.getMCPLibraryURL()
	count, err := SyncMCPLibrary(ctx, url, mc.configStore)
	if err != nil {
		return 0, err
	}
	mc.logger.Info("MCP library sync completed: %d entries synced from %s", count, url)
	return count, nil
}

// getMCPLibraryURL returns a copy of the MCP library URL under mutex protection.
func (mc *ModelCatalog) getMCPLibraryURL() string {
	mc.syncMu.RLock()
	defer mc.syncMu.RUnlock()
	return mc.mcpLibraryURL
}

// fetchMCPLibrary downloads and parses the MCP library JSON from the given URL.
func fetchMCPLibrary(ctx context.Context, rawURL string) ([]MCPLibraryEntry, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse MCP library URL: %w", err)
	}

	var data []byte
	if parsed.Scheme == "file" {
		f, err := os.Open(parsed.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to open MCP library file: %w", err)
		}
		defer f.Close()
		data, err = io.ReadAll(io.LimitReader(f, maxMCPLibraryBodyBytes+1))
		if err != nil {
			return nil, fmt.Errorf("failed to read MCP library file: %w", err)
		}
		if int64(len(data)) > maxMCPLibraryBodyBytes {
			return nil, fmt.Errorf("MCP library file exceeds %d bytes", maxMCPLibraryBodyBytes)
		}
	} else {
		if err := bifrost.ValidateExternalURL(rawURL, true); err != nil {
			return nil, fmt.Errorf("MCP library URL validation failed: %w", err)
		}
		client := &http.Client{Timeout: DefaultMCPLibraryTimeout}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to download MCP library data: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("failed to download MCP library data: HTTP %d", resp.StatusCode)
		}

		data, err = io.ReadAll(io.LimitReader(resp.Body, maxMCPLibraryBodyBytes+1))
		if err != nil {
			return nil, fmt.Errorf("failed to read MCP library response: %w", err)
		}
		if int64(len(data)) > maxMCPLibraryBodyBytes {
			return nil, fmt.Errorf("MCP library response exceeds %d bytes", maxMCPLibraryBodyBytes)
		}
	}

	var payload MCPLibraryPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal MCP library data: %w", err)
	}

	return payload.Servers, nil
}

// Slugify derives a URL/identifier-safe slug from a display name: lowercase,
// non-alphanumeric runs collapsed to a single "-", and leading/trailing "-"
// trimmed. Used to key custom library entries off their name so the existing
// unique slug index detects duplicates. Returns "" for names with no
// alphanumeric content (the caller rejects an empty slug).
//
// NOTE: only ASCII letters and digits are retained — accented/unicode characters
// (e.g. "données") are stripped, which may produce unexpected slugs for non-ASCII
// names. This is intentional to keep slugs URL-safe without a transliteration
// dependency; callers should be aware of this limitation.
func Slugify(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
