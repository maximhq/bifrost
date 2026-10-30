package datasheet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/tidwall/gjson"
)

// Datasheet entry overrides are the user's edits to the synced datasheet. The
// sync keeps writing pristine upstream rows to governance_model_pricing and
// governance_model_parameters; every load then merges the overrides over those
// rows in memory, so an edit survives any number of upstream refreshes and
// "reset to upstream" is just deleting the patched key.
//
// Both patches are RFC 7386 JSON merge patches. The pricing patch is applied to
// the TableModelPricing JSON shape (whose keys are the datasheet's own keys),
// the params patch to the model-parameters blob. All merging happens at load
// time; the request path only sees the already-merged rows.

// EntryBaseline is the upstream value of every patched key at save time,
// compared against the current upstream value to report drift.
type EntryBaseline struct {
	Pricing map[string]any `json:"pricing,omitempty"`
	Params  map[string]any `json:"params,omitempty"`
}

// EntryOverride is a parsed, validated datasheet override.
type EntryOverride struct {
	ID           string
	Provider     string
	Model        string
	Mode         string // empty applies to every mode of the model
	IsCustom     bool   // entry absent from the upstream datasheet
	PricingPatch map[string]any
	ParamsPatch  map[string]any
	Baseline     EntryBaseline
}

// paramsPatch is the composed params patch for one (provider, model) target.
type paramsPatch struct {
	provider string
	model    string
	patch    map[string]any
}

// Drift field prefixes, so a client can tell which patch a drifted key lives in.
const (
	DriftPricingPrefix = "pricing."
	DriftParamsPrefix  = "params."
)

// pricingPatchExcludedKeys are row identity and editorial columns. Identity
// is the override target, and additional_attributes has its own endpoint.
var pricingPatchExcludedKeys = map[string]struct{}{
	"id":                    {},
	"model":                 {},
	"provider":              {},
	"mode":                  {},
	"additional_attributes": {},
}

// pricingPatchKeys returns the JSON keys a pricing patch may set: every
// TableModelPricing field except identity and editorial columns.
var pricingPatchKeys = sync.OnceValue(func() map[string]struct{} {
	keys := make(map[string]struct{})
	t := reflect.TypeOf(configstoreTables.TableModelPricing{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		if _, excluded := pricingPatchExcludedKeys[name]; excluded {
			continue
		}
		keys[name] = struct{}{}
	}
	return keys
})

// IsPricingPatchKey reports whether key is a datasheet pricing field a
// pricing patch may set.
func IsPricingPatchKey(key string) bool {
	_, ok := pricingPatchKeys()[key]
	return ok
}

// ParseEntryOverride validates a persisted or incoming override row. It is the
// single validation path for the management API, config.json and DB loads.
func ParseEntryOverride(row *configstoreTables.TableDatasheetOverride) (EntryOverride, error) {
	o := EntryOverride{
		ID:       row.ID,
		Provider: strings.TrimSpace(row.Provider),
		Model:    strings.TrimSpace(row.Model),
		Mode:     strings.TrimSpace(row.Mode),
		IsCustom: row.IsCustom,
	}
	var err error
	if o.PricingPatch, err = decodePatch(row.PricingPatch, "pricing_patch"); err != nil {
		return o, err
	}
	if o.ParamsPatch, err = decodePatch(row.ParamsPatch, "params_patch"); err != nil {
		return o, err
	}
	if len(row.Baseline) > 0 && string(row.Baseline) != "null" {
		if err := sonic.Unmarshal(row.Baseline, &o.Baseline); err != nil {
			return o, fmt.Errorf("baseline must be a JSON object: %w", err)
		}
	}
	return o, o.validate()
}

func decodePatch(raw json.RawMessage, field string) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var patch map[string]any
	if err := sonic.Unmarshal(raw, &patch); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object: %w", field, err)
	}
	if len(patch) == 0 {
		return nil, nil
	}
	return patch, nil
}

func (o *EntryOverride) validate() error {
	switch {
	case o.Provider == "":
		return errors.New("provider is required")
	case o.Model == "":
		return errors.New("model is required")
	case strings.Contains(o.Provider, "|") || strings.Contains(o.Model, "|") || strings.Contains(o.Mode, "|"):
		return errors.New("provider, model and mode must not contain '|'")
	case o.IsCustom && o.Mode == "":
		return errors.New("mode is required for a custom entry")
	case !o.IsCustom && o.PricingPatch == nil && o.ParamsPatch == nil:
		return errors.New("at least one of pricing_patch or params_patch is required")
	}
	for key, value := range o.PricingPatch {
		if !IsPricingPatchKey(key) {
			return fmt.Errorf("pricing_patch: unknown field %q", key)
		}
		if f, ok := value.(float64); ok && f < 0 {
			return fmt.Errorf("pricing_patch: %s must not be negative", key)
		}
	}
	if _, err := patchPricingRow(configstoreTables.TableModelPricing{}, o.PricingPatch); err != nil {
		return fmt.Errorf("pricing_patch: %w", err)
	}
	for key, value := range o.ParamsPatch {
		if key == "provider" {
			return errors.New("params_patch: provider is the override target and cannot be patched")
		}
		if f, ok := value.(float64); ok && f < 0 {
			return fmt.Errorf("params_patch: %s must not be negative", key)
		}
	}
	if o.ParamsPatch != nil {
		blob, err := patchParamsBlob(nil, o.Provider, o.ParamsPatch)
		if err != nil {
			return fmt.Errorf("params_patch: %w", err)
		}
		var caps schemas.ModelCapabilities
		if err := sonic.Unmarshal(blob, &caps); err != nil {
			return fmt.Errorf("params_patch: %w", err)
		}
	}
	return nil
}

// mergePatch applies an RFC 7386 merge patch to target and returns it. A null
// patch value deletes the key; nested objects merge recursively.
func mergePatch(target, patch map[string]any) map[string]any {
	if target == nil {
		target = make(map[string]any, len(patch))
	}
	for key, value := range patch {
		if value == nil {
			delete(target, key)
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			existing, _ := target[key].(map[string]any)
			target[key] = mergePatch(existing, nested)
			continue
		}
		target[key] = value
	}
	return target
}

// composePatch folds src into dst so applying dst equals applying the two
// patches in order. Unlike mergePatch it keeps nulls, which still mean delete.
func composePatch(dst, src map[string]any) map[string]any {
	if dst == nil {
		dst = make(map[string]any, len(src))
	}
	for key, value := range src {
		nested, srcIsObject := value.(map[string]any)
		existing, dstIsObject := dst[key].(map[string]any)
		if srcIsObject && dstIsObject {
			dst[key] = composePatch(maps.Clone(existing), nested)
			continue
		}
		dst[key] = value
	}
	return dst
}

// toJSONDoc round-trips v into a generic JSON object.
func toJSONDoc(v any) (map[string]any, error) {
	raw, err := sonic.Marshal(v)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := sonic.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// patchPricingRow merges patch into row. Row identity and the editorial
// attributes are never touched.
func patchPricingRow(row configstoreTables.TableModelPricing, patch map[string]any) (configstoreTables.TableModelPricing, error) {
	if len(patch) == 0 {
		return row, nil
	}
	doc, err := toJSONDoc(row)
	if err != nil {
		return row, err
	}
	merged, err := sonic.Marshal(mergePatch(doc, patch))
	if err != nil {
		return row, err
	}
	var patched configstoreTables.TableModelPricing
	if err := sonic.Unmarshal(merged, &patched); err != nil {
		return row, err
	}
	patched.ID = row.ID
	patched.Model = row.Model
	patched.Provider = row.Provider
	patched.Mode = row.Mode
	patched.AdditionalAttributesJSON = row.AdditionalAttributesJSON
	patched.AdditionalAttributes = row.AdditionalAttributes
	return patched, nil
}

// patchParamsBlob merges patch into a model-parameters blob. A nil blob
// synthesizes one for a model the params datasheet does not list.
func patchParamsBlob(raw []byte, provider string, patch map[string]any) (json.RawMessage, error) {
	doc := make(map[string]any)
	if len(raw) > 0 {
		if err := sonic.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
	}
	doc = mergePatch(doc, patch)
	if _, ok := doc["provider"]; !ok {
		doc["provider"] = provider
	}
	return providerUtils.MarshalSorted(doc)
}

// paramsRowMatchesProvider mirrors LoadModelCapabilities: exact first, since
// normalizeProvider folds every bedrock_* row onto "bedrock".
func paramsRowMatchesProvider(rowProvider, provider string) bool {
	return rowProvider == provider || normalizeProvider(rowProvider) == provider
}

func paramsPatchKey(provider, model string) string {
	return provider + "|" + model
}

// orderEntryOverrides puts mode-wide overrides first so a mode-specific
// override for the same model is applied last and wins.
func orderEntryOverrides(overrides []EntryOverride) {
	slices.SortStableFunc(overrides, func(a, b EntryOverride) int {
		if (a.Mode == "") != (b.Mode == "") {
			if a.Mode == "" {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Mode, b.Mode)
	})
}

func buildParamsPatches(overrides []EntryOverride) map[string]paramsPatch {
	var patches map[string]paramsPatch
	for i := range overrides {
		o := &overrides[i]
		if o.ParamsPatch == nil {
			continue
		}
		if patches == nil {
			patches = make(map[string]paramsPatch)
		}
		key := paramsPatchKey(o.Provider, o.Model)
		existing := patches[key]
		patches[key] = paramsPatch{provider: o.Provider, model: o.Model, patch: composePatch(maps.Clone(existing.patch), o.ParamsPatch)}
	}
	return patches
}

// installEntryOverrides swaps the override set. Callers re-apply it with
// refreshEntryOverrides or a full LoadFromDB / LoadModelParamsFromDB.
func (s *Store) installEntryOverrides(rows []configstoreTables.TableDatasheetOverride, overrides []EntryOverride) {
	orderEntryOverrides(overrides)
	patches := buildParamsPatches(overrides)
	s.entryOverridesMu.Lock()
	s.entryOverrideRows = rows
	s.entryOverrides = overrides
	s.paramsPatches = patches
	s.entryOverridesMu.Unlock()
}

// parseEntryOverrideRows parses rows, skipping (and logging) invalid ones so a
// single bad row never blocks boot or a reload.
func (s *Store) parseEntryOverrideRows(rows []configstoreTables.TableDatasheetOverride) ([]configstoreTables.TableDatasheetOverride, []EntryOverride) {
	keptRows := make([]configstoreTables.TableDatasheetOverride, 0, len(rows))
	overrides := make([]EntryOverride, 0, len(rows))
	for i := range rows {
		o, err := ParseEntryOverride(&rows[i])
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("datasheet override %s (%s/%s) skipped: %v", rows[i].ID, rows[i].Provider, rows[i].Model, err)
			}
			continue
		}
		keptRows = append(keptRows, rows[i])
		overrides = append(overrides, o)
	}
	return keptRows, overrides
}

// LoadEntryOverridesFromStore reads the datasheet overrides into memory
// without re-applying them; the LoadFromDB / LoadModelParamsFromDB that follow
// do that. ReloadEntryOverrides is the variant that also re-applies.
func (s *Store) LoadEntryOverridesFromStore(ctx context.Context) error {
	if s.configStore == nil {
		return nil
	}
	rows, err := s.configStore.GetDatasheetOverrides(ctx)
	if err != nil {
		return fmt.Errorf("failed to load datasheet overrides: %w", err)
	}
	s.installEntryOverrides(s.parseEntryOverrideRows(rows))
	return nil
}

// ReloadEntryOverrides reads the overrides from the store and re-applies them
// over the loaded pricing and model-parameters data.
func (s *Store) ReloadEntryOverrides(ctx context.Context) error {
	if err := s.LoadEntryOverridesFromStore(ctx); err != nil {
		return err
	}
	s.refreshEntryOverrides(ctx)
	return nil
}

// SetEntryOverrides replaces the full override set (config.json load path) and
// re-applies it.
func (s *Store) SetEntryOverrides(ctx context.Context, rows []configstoreTables.TableDatasheetOverride) {
	s.installEntryOverrides(s.parseEntryOverrideRows(slices.Clone(rows)))
	s.refreshEntryOverrides(ctx)
}

// UpsertEntryOverride adds or replaces one override (matched by ID, then by
// target) and re-applies the set.
func (s *Store) UpsertEntryOverride(ctx context.Context, row configstoreTables.TableDatasheetOverride) error {
	parsed, err := ParseEntryOverride(&row)
	if err != nil {
		return err
	}
	s.entryOverridesMu.RLock()
	rows := make([]configstoreTables.TableDatasheetOverride, 0, len(s.entryOverrideRows)+1)
	overrides := make([]EntryOverride, 0, len(s.entryOverrides)+1)
	for i, existing := range s.entryOverrides {
		if existing.ID == parsed.ID || (existing.Provider == parsed.Provider && existing.Model == parsed.Model && existing.Mode == parsed.Mode) {
			continue
		}
		rows = append(rows, s.entryOverrideRows[i])
		overrides = append(overrides, existing)
	}
	s.entryOverridesMu.RUnlock()
	s.installEntryOverrides(append(rows, row), append(overrides, parsed))
	s.refreshEntryOverrides(ctx)
	return nil
}

// DeleteEntryOverride removes an override by ID and re-applies the set, which
// restores the upstream values it had replaced.
func (s *Store) DeleteEntryOverride(ctx context.Context, id string) {
	s.entryOverridesMu.RLock()
	rows := make([]configstoreTables.TableDatasheetOverride, 0, len(s.entryOverrideRows))
	overrides := make([]EntryOverride, 0, len(s.entryOverrides))
	for i, existing := range s.entryOverrides {
		if existing.ID != id {
			rows = append(rows, s.entryOverrideRows[i])
			overrides = append(overrides, existing)
		}
	}
	s.entryOverridesMu.RUnlock()
	s.installEntryOverrides(rows, overrides)
	s.refreshEntryOverrides(ctx)
}

// refreshEntryOverrides re-applies the current override set: pricing in
// memory, model parameters by reloading the rows they patch, and finally the
// capability cache so per-model records are re-resolved.
func (s *Store) refreshEntryOverrides(ctx context.Context) {
	s.mu.Lock()
	for key, row := range s.pricingUpstream {
		s.pricingData[key] = row
	}
	for key := range s.customPricingKeys {
		delete(s.pricingData, key)
	}
	s.applyPricingEntryOverridesUnsafe()
	s.rebuildDatasheetViewUnsafe()
	s.mu.Unlock()

	if s.configStore != nil {
		if _, err := s.LoadModelParamsFromDB(ctx); err != nil && s.logger != nil {
			s.logger.Warn("datasheet overrides: failed to reload model parameters: %v", err)
		}
	}
	if s.onModelParametersApplied != nil {
		s.onModelParametersApplied()
	}
}

// applyPricingEntryOverridesUnsafe merges the overrides into pricingData,
// which must hold pristine upstream rows. It records each replaced upstream row
// and each synthesized custom row so the next refresh can undo them. Caller
// holds s.mu write-lock; entryOverridesMu is taken inside (lock order mu →
// entryOverridesMu), which keeps a concurrent sync from applying a stale set.
func (s *Store) applyPricingEntryOverridesUnsafe() {
	s.entryOverridesMu.RLock()
	overrides := s.entryOverrides
	s.entryOverridesMu.RUnlock()

	s.pricingUpstream = nil
	s.customPricingKeys = nil
	if len(overrides) == 0 {
		return
	}
	s.pricingUpstream = make(map[string]configstoreTables.TableModelPricing)
	s.customPricingKeys = make(map[string]struct{})

	apply := func(key string, row configstoreTables.TableModelPricing, o *EntryOverride) {
		if _, recorded := s.pricingUpstream[key]; !recorded {
			if _, custom := s.customPricingKeys[key]; !custom {
				s.pricingUpstream[key] = row
			}
		}
		patched, err := patchPricingRow(row, o.PricingPatch)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("datasheet override %s: failed to patch %s: %v", o.ID, key, err)
			}
			return
		}
		s.pricingData[key] = patched
	}

	for i := range overrides {
		o := &overrides[i]
		provider := normalizeProvider(o.Provider)
		if o.Mode != "" {
			key := makeKey(o.Model, provider, o.Mode)
			row, ok := s.pricingData[key]
			if !ok {
				if !o.IsCustom {
					continue
				}
				row = configstoreTables.TableModelPricing{Model: o.Model, Provider: provider, Mode: o.Mode}
				s.customPricingKeys[key] = struct{}{}
			}
			apply(key, row, o)
			continue
		}
		if o.PricingPatch == nil {
			continue
		}
		prefix := o.Model + "|" + provider + "|"
		for key, row := range s.pricingData {
			if strings.HasPrefix(key, prefix) {
				apply(key, row, o)
			}
		}
	}
}

// withParamsEntryOverrides returns paramsData with every params patch merged
// in, adding a synthesized row for a patched model the sheet does not list.
// It also records the upstream blob of each patched row for drift reporting.
// With no params patches it returns paramsData untouched.
func (s *Store) withParamsEntryOverrides(paramsData map[string]json.RawMessage) map[string]json.RawMessage {
	s.entryOverridesMu.RLock()
	patches := s.paramsPatches
	s.entryOverridesMu.RUnlock()
	if len(patches) == 0 {
		s.entryOverridesMu.Lock()
		s.paramsUpstream = nil
		s.entryOverridesMu.Unlock()
		return paramsData
	}

	out := maps.Clone(paramsData)
	if out == nil {
		out = make(map[string]json.RawMessage, len(patches))
	}
	upstream := make(map[string]json.RawMessage, len(patches))
	for key, p := range patches {
		raw, exists := out[p.model]
		if exists {
			// The sheet keys rows by model alone; a row owned by another provider
			// stays as is and LoadModelCapabilities serves this provider's patch.
			if !paramsRowMatchesProvider(gjson.GetBytes(raw, "provider").String(), p.provider) {
				continue
			}
			upstream[key] = raw
		}
		patched, err := patchParamsBlob(raw, p.provider, p.patch)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("datasheet override: failed to patch model parameters for %s/%s: %v", p.provider, p.model, err)
			}
			continue
		}
		out[p.model] = patched
	}
	s.entryOverridesMu.Lock()
	s.paramsUpstream = upstream
	s.entryOverridesMu.Unlock()
	return out
}

func (s *Store) hasParamsPatches() bool {
	s.entryOverridesMu.RLock()
	defer s.entryOverridesMu.RUnlock()
	return len(s.paramsPatches) > 0
}

// paramsPatchFor returns the params patch for provider and the first of names
// that has one. The common case (no overrides) is a single nil-map check.
func (s *Store) paramsPatchFor(provider string, names ...string) (paramsPatch, bool) {
	s.entryOverridesMu.RLock()
	defer s.entryOverridesMu.RUnlock()
	if len(s.paramsPatches) == 0 {
		return paramsPatch{}, false
	}
	for _, name := range names {
		if p, ok := s.paramsPatches[paramsPatchKey(provider, name)]; ok {
			return p, true
		}
	}
	return paramsPatch{}, false
}

// patchedCapabilities applies the provider's params patch, if any, to a stored
// blob and decodes it. A nil blob decodes the patch alone, for models the
// params datasheet does not list.
func (s *Store) patchedCapabilities(raw []byte, provider string, names ...string) (*schemas.ModelCapabilities, bool) {
	if p, ok := s.paramsPatchFor(provider, names...); ok {
		patched, err := patchParamsBlob(raw, provider, p.patch)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("datasheet override: failed to patch capabilities for %s/%s: %v", provider, names[0], err)
			}
		} else {
			raw = patched
		}
	}
	if len(raw) == 0 {
		return nil, false
	}
	// encoding/json, as before overrides existed: this is the capability
	// cache-miss path and must keep its allocation profile.
	var caps schemas.ModelCapabilities
	if err := json.Unmarshal(raw, &caps); err != nil || IsEmptyModelCapabilities(&caps) {
		return nil, false
	}
	return &caps, true
}

// FindEntryOverride returns the persisted override for an exact target.
func (s *Store) FindEntryOverride(provider, model, mode string) *configstoreTables.TableDatasheetOverride {
	s.entryOverridesMu.RLock()
	defer s.entryOverridesMu.RUnlock()
	for i, o := range s.entryOverrides {
		if o.Provider == provider && o.Model == model && o.Mode == mode {
			row := s.entryOverrideRows[i]
			return &row
		}
	}
	return nil
}

// --- Management API views ---

// DatasheetEntryQuery filters ListDatasheetEntries.
type DatasheetEntryQuery struct {
	Search         string
	Provider       string
	OverriddenOnly bool
	Limit          int
	Offset         int
}

// DatasheetEntry is one row of the datasheet editor: the upstream row, the
// effective row after overrides, and the overrides that apply to it.
type DatasheetEntry struct {
	Provider      string                                     `json:"provider"`
	Model         string                                     `json:"model"`
	Mode          string                                     `json:"mode"`
	IsCustom      bool                                       `json:"is_custom"`
	Upstream      *configstoreTables.TableModelPricing       `json:"upstream"`
	Effective     *configstoreTables.TableModelPricing       `json:"effective"`
	Overrides     []configstoreTables.TableDatasheetOverride `json:"overrides,omitempty"`
	DriftedFields []string                                   `json:"drifted_fields,omitempty"`
}

// DatasheetEntryDetail adds the model-parameters blob, before and after
// overrides, to a DatasheetEntry.
type DatasheetEntryDetail struct {
	DatasheetEntry
	UpstreamParams  json.RawMessage `json:"upstream_params,omitempty"`
	EffectiveParams json.RawMessage `json:"effective_params,omitempty"`
}

// entryOverridesSnapshot returns the persisted rows paired with their parsed form.
func (s *Store) entryOverridesSnapshot() ([]configstoreTables.TableDatasheetOverride, []EntryOverride, map[string]json.RawMessage) {
	s.entryOverridesMu.RLock()
	defer s.entryOverridesMu.RUnlock()
	return s.entryOverrideRows, s.entryOverrides, s.paramsUpstream
}

// ListDatasheetEntries returns one entry per pricing row (upstream and
// custom), plus one per override that matches no row, sorted by provider,
// model and mode, with the total before pagination.
func (s *Store) ListDatasheetEntries(q DatasheetEntryQuery) ([]DatasheetEntry, int) {
	rows, overrides, paramsUpstream := s.entryOverridesSnapshot()
	search := strings.ToLower(strings.TrimSpace(q.Search))
	providerFilter := ""
	if q.Provider != "" {
		providerFilter = normalizeProvider(q.Provider)
	}
	matchesFilter := func(provider, model string) bool {
		if providerFilter != "" && provider != providerFilter {
			return false
		}
		return search == "" || strings.Contains(strings.ToLower(model), search)
	}

	matched := make([]bool, len(overrides))
	var entries []DatasheetEntry
	s.mu.RLock()
	for key, effective := range s.pricingData {
		if !matchesFilter(effective.Provider, effective.Model) {
			continue
		}
		entry := DatasheetEntry{Provider: effective.Provider, Model: effective.Model, Mode: effective.Mode}
		for i := range overrides {
			if overrideTargetsRow(&overrides[i], &effective) {
				matched[i] = true
				entry.Overrides = append(entry.Overrides, rows[i])
			}
		}
		if q.OverriddenOnly && len(entry.Overrides) == 0 {
			continue
		}
		effectiveCopy := effective
		entry.Effective = &effectiveCopy
		_, entry.IsCustom = s.customPricingKeys[key]
		if upstream, replaced := s.pricingUpstream[key]; replaced {
			entry.Upstream = &upstream
		} else if !entry.IsCustom {
			entry.Upstream = &effectiveCopy
		}
		entries = append(entries, entry)
	}
	s.mu.RUnlock()

	// An override whose model is not (or no longer) in the datasheet stays
	// listed so it can be found and deleted.
	for i := range overrides {
		o := &overrides[i]
		if matched[i] || !matchesFilter(normalizeProvider(o.Provider), o.Model) {
			continue
		}
		entries = append(entries, DatasheetEntry{
			Provider:  normalizeProvider(o.Provider),
			Model:     o.Model,
			Mode:      o.Mode,
			IsCustom:  o.IsCustom,
			Overrides: []configstoreTables.TableDatasheetOverride{rows[i]},
		})
	}

	for i := range entries {
		entries[i].DriftedFields = s.entryDrift(&entries[i], overrides, rows, paramsUpstream)
	}
	slices.SortFunc(entries, func(a, b DatasheetEntry) int {
		if c := strings.Compare(a.Provider, b.Provider); c != 0 {
			return c
		}
		if c := strings.Compare(a.Model, b.Model); c != 0 {
			return c
		}
		return strings.Compare(a.Mode, b.Mode)
	})

	total := len(entries)
	start := min(max(q.Offset, 0), total)
	end := total
	if q.Limit > 0 {
		end = min(start+q.Limit, total)
	}
	return entries[start:end], total
}

// overrideTargetsRow reports whether o applies to the pricing row.
func overrideTargetsRow(o *EntryOverride, row *configstoreTables.TableModelPricing) bool {
	return o.Model == row.Model && normalizeProvider(o.Provider) == row.Provider && (o.Mode == "" || o.Mode == row.Mode)
}

// entryDrift lists the patched keys whose upstream value changed since the
// override was saved. paramsUpstream maps provider|model to the upstream blob.
func (s *Store) entryDrift(entry *DatasheetEntry, overrides []EntryOverride, rows []configstoreTables.TableDatasheetOverride, paramsUpstream map[string]json.RawMessage) []string {
	if len(entry.Overrides) == 0 {
		return nil
	}
	var upstreamPricing map[string]any
	if entry.Upstream != nil {
		upstreamPricing, _ = toJSONDoc(entry.Upstream)
	}
	var drifted []string
	for _, applied := range entry.Overrides {
		idx := slices.IndexFunc(rows, func(r configstoreTables.TableDatasheetOverride) bool { return r.ID == applied.ID })
		if idx < 0 {
			continue
		}
		o := &overrides[idx]
		drifted = appendDrift(drifted, DriftPricingPrefix, o.Baseline.Pricing, upstreamPricing)
		if len(o.Baseline.Params) > 0 {
			var upstreamParams map[string]any
			if raw, ok := paramsUpstream[paramsPatchKey(o.Provider, o.Model)]; ok {
				_ = sonic.Unmarshal(raw, &upstreamParams)
			}
			drifted = appendDrift(drifted, DriftParamsPrefix, o.Baseline.Params, upstreamParams)
		}
	}
	slices.Sort(drifted)
	return slices.Compact(drifted)
}

func appendDrift(drifted []string, prefix string, baseline, upstream map[string]any) []string {
	for key, saved := range baseline {
		if !reflect.DeepEqual(saved, upstream[key]) {
			drifted = append(drifted, prefix+key)
		}
	}
	return drifted
}

// GetDatasheetEntry returns one entry with its model-parameters blob before
// and after overrides. A nil result means neither a pricing row nor an
// override exists for the target.
func (s *Store) GetDatasheetEntry(ctx context.Context, provider, model, mode string) (*DatasheetEntryDetail, error) {
	entries, _ := s.ListDatasheetEntries(DatasheetEntryQuery{Provider: provider})
	var detail *DatasheetEntryDetail
	for i := range entries {
		if entries[i].Model == model && entries[i].Mode == mode {
			detail = &DatasheetEntryDetail{DatasheetEntry: entries[i]}
			break
		}
	}
	if detail == nil {
		return nil, nil
	}

	upstreamRow, err := s.upstreamParamsRow(ctx, provider, model)
	if err != nil {
		return nil, err
	}
	var upstreamBlob []byte
	if upstreamRow != nil {
		upstreamBlob = []byte(upstreamRow.Data)
		detail.UpstreamParams = json.RawMessage(upstreamRow.Data)
	}
	detail.EffectiveParams = detail.UpstreamParams
	if p, ok := s.paramsPatchFor(provider, model); ok {
		if patched, err := patchParamsBlob(upstreamBlob, provider, p.patch); err == nil {
			detail.EffectiveParams = patched
		}
	}

	// Recompute params drift against the stored row: the list view only knows
	// the rows the in-memory index patched.
	_, overrides, _ := s.entryOverridesSnapshot()
	var upstreamParams map[string]any
	if upstreamBlob != nil {
		_ = sonic.Unmarshal(upstreamBlob, &upstreamParams)
	}
	drifted := slices.DeleteFunc(slices.Clone(detail.DriftedFields), func(f string) bool { return strings.HasPrefix(f, DriftParamsPrefix) })
	for _, applied := range detail.Overrides {
		idx := slices.IndexFunc(overrides, func(o EntryOverride) bool { return o.ID == applied.ID })
		if idx >= 0 {
			drifted = appendDrift(drifted, DriftParamsPrefix, overrides[idx].Baseline.Params, upstreamParams)
		}
	}
	slices.Sort(drifted)
	detail.DriftedFields = slices.Compact(drifted)
	return detail, nil
}

// upstreamParamsRow returns the stored (unpatched) model-parameters row that
// LoadModelCapabilities would serve for provider/model, or nil.
func (s *Store) upstreamParamsRow(ctx context.Context, provider, model string) (*configstoreTables.TableModelParameters, error) {
	if s.configStore == nil {
		return nil, nil
	}
	for _, candidate := range s.modelParameterCandidates(model) {
		row, err := s.configStore.GetModelParametersByModel(ctx, candidate)
		if err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				continue
			}
			return nil, err
		}
		if row != nil && paramsRowMatchesProvider(gjson.Get(row.Data, "provider").String(), provider) {
			return row, nil
		}
	}
	return nil, nil
}

// BuildEntryBaseline records the current upstream value of every key the
// override patches. Saving again re-baselines, which is how a client
// acknowledges drift.
func (s *Store) BuildEntryBaseline(ctx context.Context, o *EntryOverride) (EntryBaseline, error) {
	var baseline EntryBaseline
	if len(o.PricingPatch) > 0 {
		var upstream map[string]any
		if row := s.upstreamPricingRow(o); row != nil {
			var err error
			if upstream, err = toJSONDoc(row); err != nil {
				return baseline, err
			}
		}
		baseline.Pricing = make(map[string]any, len(o.PricingPatch))
		for key := range o.PricingPatch {
			baseline.Pricing[key] = upstream[key]
		}
	}
	if len(o.ParamsPatch) > 0 {
		var upstream map[string]any
		row, err := s.upstreamParamsRow(ctx, o.Provider, o.Model)
		if err != nil {
			return baseline, err
		}
		if row != nil {
			if err := sonic.Unmarshal([]byte(row.Data), &upstream); err != nil {
				return baseline, err
			}
		}
		baseline.Params = make(map[string]any, len(o.ParamsPatch))
		for key := range o.ParamsPatch {
			baseline.Params[key] = upstream[key]
		}
	}
	return baseline, nil
}

// upstreamPricingRow returns the upstream row the override patches; for a
// mode-wide override, the first mode in sort order. Nil for a custom entry.
func (s *Store) upstreamPricingRow(o *EntryOverride) *configstoreTables.TableModelPricing {
	provider := normalizeProvider(o.Provider)
	s.mu.RLock()
	defer s.mu.RUnlock()
	lookup := func(key string) *configstoreTables.TableModelPricing {
		if row, ok := s.pricingUpstream[key]; ok {
			return &row
		}
		if _, custom := s.customPricingKeys[key]; custom {
			return nil
		}
		if row, ok := s.pricingData[key]; ok {
			return &row
		}
		return nil
	}
	if o.Mode != "" {
		return lookup(makeKey(o.Model, provider, o.Mode))
	}
	prefix := o.Model + "|" + provider + "|"
	var keys []string
	for key := range s.pricingData {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		if row := lookup(key); row != nil {
			return row
		}
	}
	return nil
}
