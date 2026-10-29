package datasheet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/network"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

const (
	urlFetchMaxRetries = 3                // retries after first attempt (4 attempts total)
	urlFetchMaxBackoff = 10 * time.Second // cap for exponential backoff (steps start at 1s)
)

// Total budget for fetching a datasheet across all retries; vars so tests can shorten them.
var (
	pricingFetchTimeout = DefaultPricingTimeout
	paramsFetchTimeout  = DefaultModelParametersTimeout
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
	// Only the fetch is bounded so a hung URL cannot starve the DB fallback below.
	fetchCtx, cancel := context.WithTimeout(ctx, pricingFetchTimeout)
	pricingData, err := withRetries(fetchCtx, urlFetchMaxRetries, urlFetchMaxBackoff, func() (map[string]Entry, error) {
		return s.loadPricingFromURL(fetchCtx)
	})
	cancel()
	if err != nil {
		// URL failed — fall back to existing DB records when we have them.
		if s.configStore != nil {
			records, dbErr := s.configStore.GetModelPrices(ctx)
			if dbErr != nil {
				return fmt.Errorf("failed to load pricing data from URL (%v) and failed to get pricing records: %w", err, dbErr)
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
		// One row per (model, provider, mode) before the write: the batched upsert
		// below issues multi-row ON CONFLICT statements, and PostgreSQL rejects a
		// statement whose VALUES list hits the same conflict target twice.
		records := s.resolvePricingRows(pricingData)
		if err := s.configStore.UpsertModelPricesBatch(ctx, records); err != nil {
			return fmt.Errorf("failed to sync pricing data to database: %w", err)
		}

		if err := s.LoadFromDB(ctx); err != nil {
			return fmt.Errorf("failed to reload pricing cache: %w", err)
		}
	} else {
		// No config store — apply the parsed data directly to in-memory state.
		s.applyPricingData(pricingData)
	}

	if s.logger != nil {
		s.logger.Debug("successfully synced %d pricing records", len(pricingData))
	}
	return nil
}

// pricingCandidate is one datasheet row competing for a stored key. Only the datasheet key is
// kept: the full row is converted once, for the winner, so a sync does not copy every large row.
type pricingCandidate struct {
	modelKey string
	exact    bool // the row's own provider is already the stored provider, nothing was folded
}

// beats reports whether c should replace other as the stored row for their shared key. The answer
// depends only on the two rows, never on the order they are visited in.
func (c pricingCandidate) beats(other pricingCandidate) bool {
	if c.exact != other.exact {
		return c.exact
	}
	return c.modelKey < other.modelKey
}

// samePricing reports whether two rows carry identical prices on every cost field, not just the
// base token rates. Options is all pointer fields, so == would compare addresses; its encoding
// compares the values instead (struct fields encode in declaration order, nil ones are omitted), and
// new cost fields are covered without maintenance here. Only rows that fold pay for this.
func samePricing(a, b Entry) bool {
	ea, errA := sonic.Marshal(a.Options)
	eb, errB := sonic.Marshal(b.Options)
	return errA == nil && errB == nil && bytes.Equal(ea, eb)
}

// resolvePricingRows turns the parsed datasheet into one stored row per (model, provider, mode).
// Several datasheet rows can fold onto one key: extractModelName drops the "bedrock_mantle/" prefix
// and normalizeProvider maps both providers to "bedrock". The survivor is chosen by rule so every
// sync of the same datasheet stores the same price: a row whose own provider already is the stored
// provider beats a folded-in one, then the smaller datasheet key wins. Rows come back in key order.
//
// A fold that drops a row with different pricing is logged. When the exact-provider rule picked the
// survivor it is the intended outcome and goes to Debug; when only key order separated the rows the
// choice is arbitrary, so it is a Warn worth looking at.
func (s *Store) resolvePricingRows(pricingData map[string]Entry) []configstoreTables.TableModelPricing {
	winners := make(map[string]pricingCandidate, len(pricingData))
	var losers map[string][]pricingCandidate // stored key -> rows that lost it; empty unless rows fold
	for modelKey, entry := range pricingData {
		// Same identity convertEntryToTablePricing stores the row under.
		provider := normalizeProvider(entry.Provider)
		key := makeKey(extractModelName(modelKey), provider, entry.Mode)
		cand := pricingCandidate{modelKey: modelKey, exact: entry.Provider == provider}
		cur, taken := winners[key]
		lost := cand
		switch {
		case !taken:
			winners[key] = cand
			continue
		case cand.beats(cur):
			winners[key] = cand
			lost = cur
		}
		if losers == nil {
			losers = make(map[string][]pricingCandidate)
		}
		losers[key] = append(losers[key], lost)
	}

	keys := make([]string, 0, len(winners))
	for key := range winners {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	records := make([]configstoreTables.TableModelPricing, 0, len(keys))
	var byKeyOrder, byRule []string
	for _, key := range keys {
		win := winners[key]
		records = append(records, convertEntryToTablePricing(win.modelKey, pricingData[win.modelKey]))
		var dropped []string
		arbitrary := false
		for _, lost := range losers[key] {
			if samePricing(pricingData[win.modelKey], pricingData[lost.modelKey]) {
				continue
			}
			dropped = append(dropped, lost.modelKey)
			arbitrary = arbitrary || lost.exact == win.exact
		}
		if len(dropped) == 0 {
			continue
		}
		sort.Strings(dropped)
		conflict := fmt.Sprintf("%s kept over %s", win.modelKey, strings.Join(dropped, ", "))
		if arbitrary {
			byKeyOrder = append(byKeyOrder, conflict)
		} else {
			byRule = append(byRule, conflict)
		}
	}
	if s.logger != nil {
		if len(byKeyOrder) > 0 {
			s.logger.Warn("pricing datasheet has %d stored keys where rows with different prices fold onto one key and only datasheet key order chose the kept row (e.g. %s)", len(byKeyOrder), foldExamples(byKeyOrder))
		}
		if len(byRule) > 0 {
			s.logger.Debug("pricing datasheet has %d stored keys where a folded-in row with different prices lost to the row whose provider matches (e.g. %s)", len(byRule), foldExamples(byRule))
		}
	}
	return records
}

// foldExamples joins the first few fold conflicts for a log line.
func foldExamples(conflicts []string) string {
	const shown = 5
	if len(conflicts) > shown {
		conflicts = conflicts[:shown]
	}
	return strings.Join(conflicts, "; ")
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
	return nil
}

// applyPricingData replaces the in-memory pricing cache + datasheet view
// from a freshly-parsed URL payload. Used by the LoadFromURLIntoMemory
// path and the no-configstore SyncFromURL fallback.
func (s *Store) applyPricingData(pricingData map[string]Entry) {
	records := s.resolvePricingRows(pricingData)
	s.mu.Lock()
	s.pricingData = make(map[string]configstoreTables.TableModelPricing, len(records))
	for _, pricing := range records {
		s.pricingData[makeKey(pricing.Model, pricing.Provider, pricing.Mode)] = pricing
	}
	s.rebuildDatasheetViewUnsafe()
	s.mu.Unlock()
}

// FilePathFromURL resolves a parsed file:// URL to a filesystem path,
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
//
// Exported because every air-gapped source in the catalog resolves file:// URLs
// the same way: the pricing datasheet, the model parameters datasheet, and the
// MCP library catalog (modelcatalog.fetchMCPLibrary).
func FilePathFromURL(parsed *url.URL) string {
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
		data, err = os.ReadFile(FilePathFromURL(parsed))
		if err != nil {
			return nil, fmt.Errorf("failed to read pricing file: %w", err)
		}
	} else {
		if err := bifrost.ValidateExternalURL(rawURL, true); err != nil {
			return nil, fmt.Errorf("pricing URL validation failed: %w", err)
		}
		// ValidateExternalURL screens the configured host once; the guarded
		// client holds every connection and redirect hop to the same policy.
		client := network.NewPrivateNetworkHTTPClient(DefaultPricingTimeout)
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
