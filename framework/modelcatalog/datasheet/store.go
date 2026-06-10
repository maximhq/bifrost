package datasheet

import (
	"slices"
	"strings"
	"sync"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// Defaults for sync configuration and timeouts. Exposed so the composer can
// fall back to these when the framework Config leaves fields nil.
const (
	DefaultURL                    = "https://getbifrost.ai/datasheet"
	DefaultModelParametersURL     = "https://getbifrost.ai/datasheet/model-parameters"
	DefaultSyncInterval           = 24 * time.Hour
	DefaultPricingTimeout         = 45 * time.Second
	DefaultModelParametersTimeout = 45 * time.Second
)

// Config groups the values the composer hands to New / UpdateSyncConfig.
// Zero values fall back to the Default* constants.
type Config struct {
	URL                string
	ModelParametersURL string
	SyncInterval       time.Duration
}

func (c Config) resolved() Config {
	if c.URL == "" {
		c.URL = DefaultURL
	}
	if c.ModelParametersURL == "" {
		c.ModelParametersURL = DefaultModelParametersURL
	}
	if c.SyncInterval <= 0 {
		c.SyncInterval = DefaultSyncInterval
	}
	return c
}

// Store owns the pricing catalog (canonical rows, base-model index, derived
// datasheet view, supported request types/parameters) and pricing overrides.
//
// All I/O is driven by the composer — Store does not own a ticker or the
// distributed lock; it exposes SyncFromURL / LoadFromDB / UpdateSyncConfig as
// the surface the composer calls.
type Store struct {
	configStore configstore.ConfigStore
	logger      schemas.Logger

	// Canonical pricing state, protected by mu. Read paths take RLock and
	// return defensive copies of any slice/map they expose.
	mu                     sync.RWMutex
	pricingData            map[string]configstoreTables.TableModelPricing // model|provider|mode → row
	baseModelIndex         map[string]string                              // model → canonical base name
	supportedResponseTypes map[string][]string                            // model → [chat_completion, responses, …]
	supportedParams        map[string][]string                            // model → [temperature, top_p, …]
	datasheetByProvider    map[schemas.ModelProvider][]string             // rebuilt every reload

	// Overrides under their own mutex: writes here don't block pricing reads
	// (the hot CalculateCost path takes mu.RLock and overridesMu.RLock
	// independently and the orderings never invert).
	overridesMu   sync.RWMutex
	rawOverrides  []Override
	customPricing *customPricingData

	// Sync configuration owned here so UpdateSyncConfig is atomic w.r.t. the
	// URL accessors in sync.go. The composer's ticker reads SyncInterval()
	// and LastSyncedAt() to schedule.
	syncCfgMu          sync.RWMutex
	url                string
	modelParametersURL string
	syncInterval       time.Duration
	lastSyncedAt       time.Time
}

// New constructs a Store with the given config. The store is empty; callers
// (composer) drive bootstrap via LoadFromDB or LoadFromURLIntoMemory.
func New(configStore configstore.ConfigStore, logger schemas.Logger, cfg Config) *Store {
	cfg = cfg.resolved()
	return &Store{
		configStore:            configStore,
		logger:                 logger,
		pricingData:            make(map[string]configstoreTables.TableModelPricing),
		baseModelIndex:         make(map[string]string),
		supportedResponseTypes: make(map[string][]string),
		supportedParams:        make(map[string][]string),
		datasheetByProvider:    make(map[schemas.ModelProvider][]string),
		url:                    cfg.URL,
		modelParametersURL:     cfg.ModelParametersURL,
		syncInterval:           cfg.SyncInterval,
	}
}

// UpdateSyncConfig replaces URL / params URL / interval atomically. The
// composer is responsible for triggering a fresh sync after this returns.
func (s *Store) UpdateSyncConfig(cfg Config) {
	cfg = cfg.resolved()
	s.syncCfgMu.Lock()
	s.url = cfg.URL
	s.modelParametersURL = cfg.ModelParametersURL
	s.syncInterval = cfg.SyncInterval
	s.syncCfgMu.Unlock()
}

// URL returns a snapshot of the pricing URL.
func (s *Store) URL() string {
	s.syncCfgMu.RLock()
	defer s.syncCfgMu.RUnlock()
	return s.url
}

// ModelParametersURL returns a snapshot of the model-parameters URL.
func (s *Store) ModelParametersURL() string {
	s.syncCfgMu.RLock()
	defer s.syncCfgMu.RUnlock()
	return s.modelParametersURL
}

// SyncInterval returns the minimum elapsed time between background syncs.
func (s *Store) SyncInterval() time.Duration {
	s.syncCfgMu.RLock()
	defer s.syncCfgMu.RUnlock()
	return s.syncInterval
}

// LastSyncedAt returns the last successful URL→DB sync timestamp; zero
// before any sync has completed.
func (s *Store) LastSyncedAt() time.Time {
	s.syncCfgMu.RLock()
	defer s.syncCfgMu.RUnlock()
	return s.lastSyncedAt
}

// MarkSynced records the timestamp of a successful sync — called by the
// composer's ticker after a successful tick.
func (s *Store) MarkSynced(t time.Time) {
	s.syncCfgMu.Lock()
	s.lastSyncedAt = t
	s.syncCfgMu.Unlock()
}

// --- Reads ---

// Get returns the raw pricing row for (model, provider, requestType) or nil.
// Useful for callers that need exact pricing without override resolution.
func (s *Store) Get(model string, provider schemas.ModelProvider, requestType schemas.RequestType) *configstoreTables.TableModelPricing {
	key := makeKey(model, string(provider), normalizeRequestType(requestType))
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, ok := s.pricingData[key]
	if !ok {
		return nil
	}
	return &row
}

// GetPricingEntryForModel returns the first pricing entry found across known
// modes. Preserved for callers (inference handler) that want any pricing row
// for the model without specifying a request type.
func (s *Store) GetPricingEntryForModel(model string, provider schemas.ModelProvider) *Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, mode := range []schemas.RequestType{
		schemas.TextCompletionRequest,
		schemas.ChatCompletionRequest,
		schemas.ResponsesRequest,
		schemas.EmbeddingRequest,
		schemas.RerankRequest,
		schemas.SpeechRequest,
		schemas.TranscriptionRequest,
		schemas.ImageGenerationRequest,
		schemas.ImageEditRequest,
		schemas.ImageVariationRequest,
		schemas.VideoGenerationRequest,
		schemas.OCRRequest,
	} {
		key := makeKey(model, string(provider), normalizeRequestType(mode))
		if pricing, ok := s.pricingData[key]; ok {
			return convertTablePricingToEntry(&pricing)
		}
	}
	return nil
}

// GetCapabilityEntry returns capability metadata (context length, supported
// modes, etc.) for a (model, provider) pair. Prefers chat → responses →
// text-completion entries; falls back to the lexicographically first mode if
// none of the preferred modes match. Tries the exact model first, then the
// canonical base model.
func (s *Store) GetCapabilityEntry(model string, provider schemas.ModelProvider) *Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if entry := s.capabilityEntryForExactUnsafe(model, provider); entry != nil {
		return entry
	}

	baseModel := s.baseModelNameUnsafe(model)
	if baseModel != model {
		if entry := s.capabilityEntryForExactUnsafe(baseModel, provider); entry != nil {
			return entry
		}
	}

	if entry := s.capabilityEntryForFamilyUnsafe(baseModel, provider); entry != nil {
		return entry
	}
	return nil
}

// BaseModelName returns the canonical base model name. Uses the pre-computed
// base_model from the pricing catalog when present, falling back to
// algorithmic date/version stripping for unknown models.
//
//	"gpt-4o"               → "gpt-4o"
//	"openai/gpt-4o"        → "gpt-4o"
//	"gpt-4o-2024-08-06"    → "gpt-4o"  (algorithmic fallback)
func (s *Store) BaseModelName(model string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.baseModelNameUnsafe(model)
}

// baseModelNameUnsafe — caller MUST hold s.mu. Used by GetCapabilityEntry
// and other hot paths that already hold the lock.
func (s *Store) baseModelNameUnsafe(model string) string {
	if base, ok := s.baseModelIndex[model]; ok {
		return base
	}
	_, baseName := schemas.ParseModelString(model, "")
	if baseName != model {
		if base, ok := s.baseModelIndex[baseName]; ok {
			return base
		}
	}
	return schemas.BaseModelName(baseName)
}

// IsSameModel reports whether two model strings refer to the same underlying
// model after normalization.
func (s *Store) IsSameModel(model1, model2 string) bool {
	if model1 == model2 {
		return true
	}
	return s.BaseModelName(model1) == s.BaseModelName(model2)
}

// DistinctBaseModelNames returns every unique base name from the catalog.
// Used by governance for cross-provider model selection.
func (s *Store) DistinctBaseModelNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]struct{})
	for _, baseName := range s.baseModelIndex {
		seen[baseName] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}

// DatasheetModelsForProvider returns the per-provider model slice derived
// from pricing data on the last load/sync. Composer unions this with
// live.ModelsForProvider on read.
func (s *Store) DatasheetModelsForProvider(provider schemas.ModelProvider) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	models, ok := s.datasheetByProvider[provider]
	if !ok {
		return nil
	}
	out := make([]string, len(models))
	copy(out, models)
	return out
}

// DatasheetProviders returns every provider that has at least one pricing
// row in the datasheet view. Composer unions this with live + keyconfig to
// enumerate "all known providers" for GetProvidersForModel.
func (s *Store) DatasheetProviders() []schemas.ModelProvider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]schemas.ModelProvider, 0, len(s.datasheetByProvider))
	for p := range s.datasheetByProvider {
		out = append(out, p)
	}
	return out
}

// IsRequestTypeSupported checks whether a model declares support for the
// given request type via the model-parameters datasheet.
func (s *Store) IsRequestTypeSupported(model string, requestType schemas.RequestType) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	outputs, ok := s.supportedResponseTypes[model]
	return ok && slices.Contains(outputs, string(requestType))
}

// GetSupportedParameters returns the list of OpenAI-compatible parameter
// names a model accepts (e.g. temperature, top_p, tools). nil for unknown.
func (s *Store) GetSupportedParameters(model string) []string {
	s.mu.RLock()
	params, ok := s.supportedParams[model]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	out := make([]string, len(params))
	copy(out, params)
	return out
}

// IsTextCompletionSupported checks whether a model has a text_completion
// pricing entry — used by litellmcompat to decide whether to convert text
// completion requests into chat completion requests.
func (s *Store) IsTextCompletionSupported(model string, provider schemas.ModelProvider) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := makeKey(model, normalizeProvider(string(provider)), normalizeRequestType(schemas.TextCompletionRequest))
	_, ok := s.pricingData[key]
	return ok
}

// --- Private capability helpers (caller holds mu) ---

func (s *Store) capabilityEntryForExactUnsafe(model string, provider schemas.ModelProvider) *Entry {
	preferredModes := []schemas.RequestType{
		schemas.ChatCompletionRequest,
		schemas.ResponsesRequest,
		schemas.TextCompletionRequest,
	}
	for _, mode := range preferredModes {
		key := makeKey(model, string(provider), normalizeRequestType(mode))
		if pricing, ok := s.pricingData[key]; ok {
			return convertTablePricingToEntry(&pricing)
		}
	}

	prefix := model + "|" + string(provider) + "|"
	var matchingKeys []string
	for key := range s.pricingData {
		if strings.HasPrefix(key, prefix) {
			matchingKeys = append(matchingKeys, key)
		}
	}
	return s.selectCapabilityEntryFromKeysUnsafe(matchingKeys)
}

func (s *Store) capabilityEntryForFamilyUnsafe(baseModel string, provider schemas.ModelProvider) *Entry {
	if baseModel == "" {
		return nil
	}
	var matchingKeys []string
	for key, pricing := range s.pricingData {
		if normalizeProvider(pricing.Provider) != string(provider) {
			continue
		}
		if s.baseModelNameUnsafe(pricing.Model) != baseModel {
			continue
		}
		matchingKeys = append(matchingKeys, key)
	}
	return s.selectCapabilityEntryFromKeysUnsafe(matchingKeys)
}

func (s *Store) selectCapabilityEntryFromKeysUnsafe(matchingKeys []string) *Entry {
	if len(matchingKeys) == 0 {
		return nil
	}
	preferredModes := []string{
		normalizeRequestType(schemas.ChatCompletionRequest),
		normalizeRequestType(schemas.ResponsesRequest),
		normalizeRequestType(schemas.TextCompletionRequest),
	}
	for _, mode := range preferredModes {
		var modeMatches []string
		for _, key := range matchingKeys {
			parts := strings.SplitN(key, "|", 3)
			if len(parts) != 3 || parts[2] != mode {
				continue
			}
			modeMatches = append(modeMatches, key)
		}
		if len(modeMatches) == 0 {
			continue
		}
		slices.Sort(modeMatches)
		pricing := s.pricingData[modeMatches[0]]
		return convertTablePricingToEntry(&pricing)
	}
	slices.Sort(matchingKeys)
	pricing := s.pricingData[matchingKeys[0]]
	return convertTablePricingToEntry(&pricing)
}

// NewTestStore constructs a minimal Store for unit tests without I/O.
// Optionally seed baseModelIndex so BaseModelName lookups resolve. A no-op
// logger is wired so cost / pricing paths (which assume Store.logger is
// non-nil) don't panic from external test code.
func NewTestStore(baseModelIndex map[string]string) *Store {
	if baseModelIndex == nil {
		baseModelIndex = make(map[string]string)
	}
	return &Store{
		logger:                 bifrost.NewNoOpLogger(),
		pricingData:            make(map[string]configstoreTables.TableModelPricing),
		baseModelIndex:         baseModelIndex,
		supportedResponseTypes: make(map[string][]string),
		supportedParams:        make(map[string][]string),
		datasheetByProvider:    make(map[schemas.ModelProvider][]string),
	}
}

// --- Internal: rebuild the datasheet view from current pricingData ---

// rebuildDatasheetViewUnsafe regenerates baseModelIndex and datasheetByProvider
// from pricingData. Caller MUST hold s.mu write-lock. Called after every
// pricingData mutation in sync.go / params.go.
func (s *Store) rebuildDatasheetViewUnsafe() {
	s.baseModelIndex = make(map[string]string)
	providerModels := make(map[schemas.ModelProvider]map[string]struct{})

	for _, pricing := range s.pricingData {
		normalized := schemas.ModelProvider(normalizeProvider(pricing.Provider))
		if providerModels[normalized] == nil {
			providerModels[normalized] = make(map[string]struct{})
		}
		providerModels[normalized][pricing.Model] = struct{}{}

		if pricing.BaseModel != "" {
			s.baseModelIndex[pricing.Model] = pricing.BaseModel
		}
	}

	s.datasheetByProvider = make(map[schemas.ModelProvider][]string, len(providerModels))
	for provider, modelSet := range providerModels {
		models := make([]string, 0, len(modelSet))
		for m := range modelSet {
			models = append(models, m)
		}
		slices.Sort(models)
		s.datasheetByProvider[provider] = models
	}
}
