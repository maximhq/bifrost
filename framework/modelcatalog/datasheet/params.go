package datasheet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/tidwall/gjson"
)

// LoadModelParamsFromDB bulk-loads model parameters from the DB into the
// provider-utils cache and the in-memory supportedResponseTypes /
// supportedParams indexes. Returns the row count so the composer can decide
// whether to background-sync from URL afterwards.
//
// The provider-utils cache-miss handler in the composer still loads one row
// at a time when an unknown model is queried; both paths use the same JSON
// shape stored in the table.
func (s *Store) LoadModelParamsFromDB(ctx context.Context) (int, error) {
	if s.configStore == nil {
		return 0, nil
	}
	rows, err := s.configStore.GetModelParameters(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to load model parameters from database: %w", err)
	}
	if len(rows) == 0 {
		if s.logger != nil {
			s.logger.Debug("no model parameters rows in database")
		}
		return 0, nil
	}
	paramsData := make(map[string]json.RawMessage, len(rows))
	for _, row := range rows {
		paramsData[row.Model] = json.RawMessage(row.Data)
	}
	applied := s.applyModelParameters(paramsData)
	if s.logger != nil {
		s.logger.Debug("loaded %d model parameters records from database into cache (%d rows scanned)", applied, len(rows))
	}
	return applied, nil
}

// SyncModelParamsFromURL fetches model parameters from the configured URL,
// persists to DB (when configStore != nil), and refreshes the in-memory
// indexes. On URL failure it falls back to DB records when any exist.
func (s *Store) SyncModelParamsFromURL(ctx context.Context) error {
	if s.logger != nil {
		s.logger.Debug("starting model parameters synchronization")
	}

	paramsData, err := withRetries(ctx, urlFetchMaxRetries, urlFetchMaxBackoff, func() (map[string]json.RawMessage, error) {
		return s.loadModelParametersFromURL(ctx)
	})
	if err != nil {
		if s.configStore != nil {
			rows, dbErr := s.configStore.GetModelParameters(ctx)
			if dbErr == nil && len(rows) > 0 {
				if s.logger != nil {
					s.logger.Error("failed to load model parameters from URL, falling back to existing database records: %v", err)
				}
				return nil
			}
		}
		return fmt.Errorf("failed to load model parameters from URL and no existing data in database: %w", err)
	}

	if s.configStore != nil {
		records := make([]configstoreTables.TableModelParameters, 0, len(paramsData))
		for model, data := range paramsData {
			records = append(records, configstoreTables.TableModelParameters{
				Model: model,
				Data:  string(data),
			})
		}
		if err := s.configStore.UpsertModelParametersBatch(ctx, records); err != nil {
			return fmt.Errorf("failed to sync model parameters to database: %w", err)
		}
	}

	s.applyModelParameters(paramsData)
	if s.logger != nil {
		s.logger.Info("successfully synced %d model parameters records", len(paramsData))
	}
	return nil
}

// LoadModelParamsFromURLIntoMemory fetches model parameters from the URL and
// applies them in-memory only. Used when there's no config store.
func (s *Store) LoadModelParamsFromURLIntoMemory(ctx context.Context) error {
	paramsData, err := withRetries(ctx, urlFetchMaxRetries, urlFetchMaxBackoff, func() (map[string]json.RawMessage, error) {
		return s.loadModelParametersFromURL(ctx)
	})
	if err != nil {
		return fmt.Errorf("failed to load model parameters from URL: %w", err)
	}
	s.applyModelParameters(paramsData)
	return nil
}

// loadModelParametersFromURL fetches and parses the model parameters
// datasheet at the configured URL.
func (s *Store) loadModelParametersFromURL(ctx context.Context) (map[string]json.RawMessage, error) {
	s.syncCfgMu.RLock()
	rawURL := s.modelParametersURL
	s.syncCfgMu.RUnlock()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse model parameters URL: %w", err)
	}

	var data []byte

	if parsed.Scheme == "file" {
		data, err = os.ReadFile(filePathFromURL(parsed))
		if err != nil {
			return nil, fmt.Errorf("failed to read model parameters file: %w", err)
		}
	} else {
		if err := bifrost.ValidateExternalURL(rawURL, true); err != nil {
			return nil, fmt.Errorf("model parameters URL validation failed: %w", err)
		}
		client := &http.Client{Timeout: DefaultModelParametersTimeout}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to download model parameters data: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("failed to download model parameters data: HTTP %d", resp.StatusCode)
		}
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read model parameters response: %w", err)
		}
	}
	var paramsData map[string]json.RawMessage
	if err := json.Unmarshal(data, &paramsData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal model parameters data: %w", err)
	}
	if s.logger != nil {
		s.logger.Debug("successfully downloaded and parsed %d model parameters records", len(paramsData))
	}
	return paramsData, nil
}

// applyModelParameters parses the raw model-parameters JSON and updates:
//   - supportedResponseTypes (per-model normalized output types)
//   - supportedParams (per-model accepted request parameter names)
//   - the provider-utils ModelParams cache (max_output_tokens,
//     vertex_multi_region_only)
//
// Models with no useful info contribute nothing — the indexes are wholly
// replaced under mu.Lock so readers see either the pre- or post-sync state,
// never a partial mix. Returns the count of successfully parsed entries so
// bootstrap callers can distinguish "DB had rows but all malformed" from
// "DB had usable rows".
func (s *Store) applyModelParameters(paramsData map[string]json.RawMessage) int {
	modelParamsEntries := make(map[string]providerUtils.ModelParams, len(paramsData))
	newResponseTypes := make(map[string][]string, len(paramsData))
	newParamsIndex := make(map[string][]string, len(paramsData))
	applied := 0

	for model, rawData := range paramsData {
		var parsed modelParametersParseResult
		if err := json.Unmarshal(rawData, &parsed); err != nil {
			if s.logger != nil {
				s.logger.Warn("model-parameters-sync: skipping malformed parameters for model %s: %v", model, err)
			}
			continue
		}
		applied++

		outputs := make([]string, 0, len(parsed.SupportedEndpoints))
		for _, endpoint := range parsed.SupportedEndpoints {
			if normalized := normalizeEndpointToOutputType(endpoint); normalized != "" && !slices.Contains(outputs, normalized) {
				outputs = append(outputs, normalized)
			}
		}
		if parsed.Mode != nil {
			if normalized := normalizeModeToOutputType(*parsed.Mode); normalized != "" && !slices.Contains(outputs, normalized) {
				outputs = append(outputs, normalized)
			}
		}

		// Backfill text_completion when the pricing catalog has a row for it
		// even though supported_endpoints didn't mention /completions.
		if !slices.Contains(outputs, "text_completion") {
			if provider := gjson.GetBytes(rawData, "provider"); provider.Exists() {
				key := makeKey(model, normalizeProvider(provider.String()), normalizeRequestType(schemas.TextCompletionRequest))
				s.mu.RLock()
				_, ok := s.pricingData[key]
				s.mu.RUnlock()
				if ok {
					outputs = append(outputs, "text_completion")
				}
			}
		}

		if len(outputs) > 0 {
			newResponseTypes[model] = outputs
		}

		if supported := extractSupportedParams(&parsed); len(supported) > 0 {
			newParamsIndex[model] = supported
		}

		var p struct {
			MaxOutputTokens *int `json:"max_output_tokens"`
		}
		if err := json.Unmarshal(rawData, &p); err == nil && (p.MaxOutputTokens != nil || parsed.VertexMultiRegionOnly != nil) {
			modelParamsEntries[model] = providerUtils.ModelParams{
				MaxOutputTokens:         p.MaxOutputTokens,
				IsVertexMultiRegionOnly: parsed.VertexMultiRegionOnly,
			}
		}
	}

	s.mu.Lock()
	s.supportedResponseTypes = newResponseTypes
	s.supportedParams = newParamsIndex
	s.mu.Unlock()

	if len(modelParamsEntries) > 0 {
		providerUtils.BulkSetModelParams(modelParamsEntries)
	}
	return applied
}

// GetModelParametersByModel reads a single model-parameter row from the DB.
// Used by the composer's cache-miss handler — installed via
// providerUtils.SetCacheMissHandler in the composer's Init.
func (s *Store) GetModelParametersByModel(ctx context.Context, model string) (*configstoreTables.TableModelParameters, error) {
	if s.configStore == nil {
		return nil, nil
	}
	return s.configStore.GetModelParametersByModel(ctx, model)
}

// ResolveModelParameters looks up a model-parameter row for model, falling
// back through equivalent identifiers when the exact key has no row. The
// model-parameters datasheet keys models inconsistently — some bare
// ("gpt-5.5"), some provider-qualified ("openrouter/moonshotai/kimi-k2.5") —
// so clients querying with the IDs returned by /v1/models need this
// resolution to land on the stored key. Mirrors GetCapabilityEntry's
// exact → stripped → base-model fallback chain.
func (s *Store) ResolveModelParameters(ctx context.Context, model string) (*configstoreTables.TableModelParameters, error) {
	if s.configStore == nil {
		return nil, nil
	}
	var firstErr error
	for _, candidate := range s.modelParameterCandidates(model) {
		params, err := s.configStore.GetModelParametersByModel(ctx, candidate)
		if err == nil {
			return params, nil
		}
		if !errors.Is(err, configstore.ErrNotFound) {
			return nil, err
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// modelParameterCandidates returns the ordered lookup keys tried by
// ResolveModelParameters: the exact model, each progressively
// provider-stripped form ("openrouter/openai/gpt-5.5" → "openai/gpt-5.5" →
// "gpt-5.5"), the canonical base name, and finally any datasheet keys that
// qualify the bare name with a provider path ("kimi-k2.5" →
// "openrouter/moonshotai/kimi-k2.5"). Suffix matches are sorted so
// resolution is deterministic when multiple qualified keys exist.
func (s *Store) modelParameterCandidates(model string) []string {
	candidates := []string{model}
	add := func(c string) {
		if c != "" && !slices.Contains(candidates, c) {
			candidates = append(candidates, c)
		}
	}

	bare := model
	for {
		_, stripped := schemas.ParseModelString(bare, "")
		if stripped == bare {
			break
		}
		add(stripped)
		bare = stripped
	}

	add(s.BaseModelName(model))

	suffix := "/" + bare
	var qualified []string
	s.mu.RLock()
	for key := range s.supportedParams {
		if strings.HasSuffix(key, suffix) {
			qualified = append(qualified, key)
		}
	}
	s.mu.RUnlock()
	slices.Sort(qualified)
	for _, key := range qualified {
		add(key)
	}

	return candidates
}
