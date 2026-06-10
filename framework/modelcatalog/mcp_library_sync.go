package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

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

	entries, err := WithRetries(ctx, urlFetchMaxRetries, urlFetchMaxBackoff, func() ([]MCPLibraryEntry, error) {
		return fetchMCPLibrary(ctx, url)
	})
	if err != nil {
		return 0, fmt.Errorf("failed to fetch MCP library from %s: %w", url, err)
	}

	if len(entries) == 0 {
		return 0, nil
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
