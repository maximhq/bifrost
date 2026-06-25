package datasheet

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
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

const (
	urlFetchMaxRetries = 3                // retries after first attempt (4 attempts total)
	urlFetchMaxBackoff = 10 * time.Second // cap for exponential backoff (steps start at 1s)
)

// SyncFromURL fetches the upstream pricing datasheet, persists it to the DB
// (when configStore != nil), and refreshes the in-memory cache + derived
// datasheet view. On URL failure it falls back to existing DB records when
// any exist, otherwise propagates the error.
//
// The composer owns the distributed lock, the ticker, and the after-sync
// gossip hook — none of that lives here. SyncFromURL is the pure
// "URL → DB → memory" step.
func (s *Store) SyncFromURL(ctx context.Context) error {
	pricingData, err := withRetries(ctx, urlFetchMaxRetries, urlFetchMaxBackoff, func() (map[string]Entry, error) {
		return s.loadPricingFromURL(ctx)
	})
	if err != nil {
		// URL failed — fall back to existing DB records when we have them.
		if s.configStore != nil {
			records, dbErr := s.configStore.GetModelPrices(ctx)
			if dbErr != nil {
				return fmt.Errorf("failed to get pricing records: %w", dbErr)
			}
			if len(records) > 0 {
				if s.logger != nil {
					s.logger.Warn("failed to fetch pricing from URL, falling back to existing database records: %v", err)
				}
				return nil
			}
		}
		return fmt.Errorf("failed to load pricing data from URL and no existing data available: %w", err)
	}

	if s.configStore != nil {
		err = s.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			seen := make(map[string]struct{})
			for modelKey, entry := range pricingData {
				pricing := convertEntryToTablePricing(modelKey, entry)
				key := makeKey(pricing.Model, pricing.Provider, pricing.Mode)
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				if err := s.configStore.UpsertModelPrices(ctx, &pricing, tx); err != nil {
					return fmt.Errorf("failed to create pricing record for model %s: %w", pricing.Model, err)
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("failed to sync pricing data to database: %w", err)
		}

		if err := s.LoadFromDB(ctx); err != nil {
			return fmt.Errorf("failed to reload pricing cache: %w", err)
		}
	} else {
		// No config store — apply the parsed data directly to in-memory state.
		s.applyPricingData(pricingData)
	}

	// Populate provider-utils model params cache from any max_output_tokens
	// fields in the pricing entries so providers can read those without a
	// separate model-parameters sync round-trip.
	s.populateModelParamsFromPricing(pricingData)

	if s.logger != nil {
		s.logger.Debug("successfully synced %d pricing records", len(pricingData))
	}
	return nil
}

// LoadFromDB reloads the in-memory pricing cache + datasheet view from the
// config store. Used by the composer at bootstrap and as the gossip
// ReloadFromDB handler on non-leader pods.
func (s *Store) LoadFromDB(ctx context.Context) error {
	if s.configStore == nil {
		return nil
	}
	records, err := s.configStore.GetModelPrices(ctx)
	if err != nil {
		return fmt.Errorf("failed to load pricing from database: %w", err)
	}

	s.mu.Lock()
	s.pricingData = make(map[string]configstoreTables.TableModelPricing, len(records))
	for _, pricing := range records {
		key := makeKey(pricing.Model, pricing.Provider, pricing.Mode)
		s.pricingData[key] = pricing
	}
	s.rebuildDatasheetViewUnsafe()
	s.mu.Unlock()

	if s.logger != nil {
		s.logger.Debug("loaded %d pricing records from database into memory", len(records))
	}
	return nil
}

// LoadFromURLIntoMemory loads pricing from the URL directly into memory
// (no DB). Used when the composer was built without a config store.
func (s *Store) LoadFromURLIntoMemory(ctx context.Context) error {
	pricingData, err := withRetries(ctx, urlFetchMaxRetries, urlFetchMaxBackoff, func() (map[string]Entry, error) {
		return s.loadPricingFromURL(ctx)
	})
	if err != nil {
		return fmt.Errorf("failed to load pricing data from URL: %w", err)
	}
	s.applyPricingData(pricingData)
	s.populateModelParamsFromPricing(pricingData)
	return nil
}

// applyPricingData replaces the in-memory pricing cache + datasheet view
// from a freshly-parsed URL payload. Used by the LoadFromURLIntoMemory
// path and the no-configstore SyncFromURL fallback.
func (s *Store) applyPricingData(pricingData map[string]Entry) {
	s.mu.Lock()
	s.pricingData = make(map[string]configstoreTables.TableModelPricing, len(pricingData))
	for modelKey, entry := range pricingData {
		pricing := convertEntryToTablePricing(modelKey, entry)
		key := makeKey(pricing.Model, pricing.Provider, pricing.Mode)
		s.pricingData[key] = pricing
	}
	s.rebuildDatasheetViewUnsafe()
	s.mu.Unlock()
}

// filePathFromURL resolves a parsed file:// URL to a filesystem path,
// supporting both absolute and relative references. Go's url.Parse scatters a
// relative path across different fields depending on its form, so we reassemble
// it here:
//   - file:./x.json or file:x.json -> Opaque ("./x.json")
//   - file://./x.json              -> Host (".") + Path ("/x.json") = "./x.json"
//   - file:///abs/x.json           -> Path ("/abs/x.json")
//   - file://localhost/abs/x.json  -> Path ("/abs/x.json")
//
// Relative paths resolve against the process working directory, matching how the
// sqlite config store treats a relative "path" value.
func filePathFromURL(parsed *url.URL) string {
	if parsed.Opaque != "" {
		return parsed.Opaque
	}
	if parsed.Host != "" && parsed.Host != "localhost" {
		return parsed.Host + parsed.Path
	}
	return parsed.Path
}

// loadPricingFromURL fetches and parses the pricing datasheet at the
// configured URL. Honors ctx for cancellation.
func (s *Store) loadPricingFromURL(ctx context.Context) (map[string]Entry, error) {
	s.syncCfgMu.RLock()
	rawURL := s.url
	s.syncCfgMu.RUnlock()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse pricing URL: %w", err)
	}

	var data []byte

	if parsed.Scheme == "file" {
		data, err = os.ReadFile(filePathFromURL(parsed))
		if err != nil {
			return nil, fmt.Errorf("failed to read pricing file: %w", err)
		}
	} else {
		if err := bifrost.ValidateExternalURL(rawURL, true); err != nil {
			return nil, fmt.Errorf("pricing URL validation failed: %w", err)
		}
		client := &http.Client{Timeout: DefaultPricingTimeout}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL(), nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to download pricing data: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("failed to download pricing data: HTTP %d", resp.StatusCode)
		}
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read pricing data response: %w", err)
		}
	}
	var pricingData map[string]Entry
	if err := json.Unmarshal(data, &pricingData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pricing data: %w", err)
	}
	if s.logger != nil {
		s.logger.Debug("successfully downloaded and parsed %d pricing records", len(pricingData))
	}

	return pricingData, nil
}

// populateModelParamsFromPricing extracts max_output_tokens from pricing
// entries and seeds the provider-utils model params cache so providers can
// look up max output tokens without a separate model-parameters sync.
func (s *Store) populateModelParamsFromPricing(pricingData map[string]Entry) {
	modelParamsEntries := make(map[string]providerUtils.ModelParams)
	for modelKey, entry := range pricingData {
		if entry.MaxOutputTokens != nil {
			modelName := extractModelName(modelKey)
			modelParamsEntries[modelName] = providerUtils.ModelParams{
				MaxOutputTokens: entry.MaxOutputTokens,
			}
		}
	}
	if len(modelParamsEntries) > 0 {
		providerUtils.BulkSetModelParams(modelParamsEntries)
		if s.logger != nil {
			s.logger.Debug("populated %d model params entries from pricing datasheet", len(modelParamsEntries))
		}
	}
}
