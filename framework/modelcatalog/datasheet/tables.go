package datasheet

import (
	"context"
	"encoding/json"
	"iter"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
)

// slotKey is the one key a slot's table holds.
const slotKey = "value"

// slot holds one value in a live-store table, under one key: a field the store replaces whole and
// readers take as a snapshot. The zero slot holds the zero value.
type slot[V any] struct {
	t *livestore.Table[string, V]
}

// The datasheet's live-store table names. A process has one datasheet, so the names are fixed.
const (
	tablePricing            = "model_catalog_datasheet_pricing"
	tableBaseModels         = "model_catalog_datasheet_base_models"
	tablePricingUpstream    = "model_catalog_datasheet_pricing_upstream"
	tableCustomPricingKeys  = "model_catalog_datasheet_custom_pricing_keys"
	tableParamsOverrideOnly = "model_catalog_datasheet_params_override_only"
	tableIgnoreProviderCost = "model_catalog_datasheet_ignore_provider_cost"
	tableResponseTypes      = "model_catalog_datasheet_response_types"
	tableParams             = "model_catalog_datasheet_params"
	tableProviderModels     = "model_catalog_datasheet_provider_models"
	tableDeprecatedModels   = "model_catalog_datasheet_deprecated_models"
	tableParameterKeys      = "model_catalog_datasheet_parameter_keys"
	tableOverrides          = "model_catalog_datasheet_overrides"
	tableOverrideIndex      = "model_catalog_datasheet_override_index"
	tableEntryOverrides     = "model_catalog_datasheet_entry_overrides"
	tableParamsPatches      = "model_catalog_datasheet_params_patches"
	tableParamsUpstream     = "model_catalog_datasheet_params_upstream"
	tableUpstreamRows       = "model_catalog_datasheet_upstream_rows"
	tableParamsSheet        = "model_catalog_datasheet_params_sheet"
)

// newSlot creates a slot's table in live under name.
func newSlot[V any](live *livestore.Store, name string) (slot[V], error) {
	t, err := live.NewTable[string, V](context.Background(), name)
	return slot[V]{t: t}, err
}

// load returns the slot's value, the zero value before the first store.
func (sl slot[V]) load() V {
	v, _ := sl.t.Get(slotKey)
	return v
}

// store replaces the slot's value.
func (sl slot[V]) store(v V) {
	sl.t.Put(slotKey, v)
}

// lookup returns t's value under k, or the zero value when it holds none.
func lookup[K comparable, V any](t *livestore.Table[K, V], k K) V {
	v, _ := t.Get(k)
	return v
}

// has reports whether t holds a value under k.
func has[K comparable, V any](t *livestore.Table[K, V], k K) bool {
	_, ok := t.Get(k)
	return ok
}

// fillTable replaces t's entries with m's.
func fillTable[K comparable, V any](t *livestore.Table[K, V], m map[K]V) {
	t.Flush()
	for k, v := range m {
		t.Put(k, v)
	}
}

// createTables creates the store's tables in live. A process runs one model catalog, so the names
// are fixed.
func (s *Store) createTables(live *livestore.Store) error {
	var err error
	if s.pricingData, err = live.NewTable[string, configstoreTables.TableModelPricing](context.Background(), tablePricing); err != nil {
		return err
	}
	if s.baseModelIndex, err = live.NewTable[string, string](context.Background(), tableBaseModels); err != nil {
		return err
	}
	if s.supportedResponseTypes, err = live.NewTable[string, []string](context.Background(), tableResponseTypes); err != nil {
		return err
	}
	if s.supportedParams, err = live.NewTable[string, []string](context.Background(), tableParams); err != nil {
		return err
	}
	if s.modelParameterKeys, err = newSlot[[]string](live, tableParameterKeys); err != nil {
		return err
	}
	if s.datasheetByProvider, err = live.NewTable[schemas.ModelProvider, []string](context.Background(), tableProviderModels); err != nil {
		return err
	}
	if s.deprecatedByProvider, err = live.NewTable[schemas.ModelProvider, []string](context.Background(), tableDeprecatedModels); err != nil {
		return err
	}
	if s.rawOverrides, err = newSlot[[]Override](live, tableOverrides); err != nil {
		return err
	}
	if s.customPricing, err = newSlot[*customPricingData](live, tableOverrideIndex); err != nil {
		return err
	}
	if s.entryOverrides, err = newSlot[[]EntryOverride](live, tableEntryOverrides); err != nil {
		return err
	}
	if s.paramsPatches, err = newSlot[map[string]paramsPatch](live, tableParamsPatches); err != nil {
		return err
	}
	if s.paramsUpstream, err = newSlot[map[string]json.RawMessage](live, tableParamsUpstream); err != nil {
		return err
	}
	if s.pricingUpstream, err = live.NewTable[string, configstoreTables.TableModelPricing](context.Background(), tablePricingUpstream); err != nil {
		return err
	}
	if s.customPricingKeys, err = live.NewTable[string, struct{}](context.Background(), tableCustomPricingKeys); err != nil {
		return err
	}
	if s.pricingRows, err = newSlot[[]configstoreTables.TableModelPricing](live, tableUpstreamRows); err != nil {
		return err
	}
	if s.paramsSheet, err = newSlot[map[string]json.RawMessage](live, tableParamsSheet); err != nil {
		return err
	}
	if s.paramsOverrideOnly, err = live.NewTable[string, struct{}](context.Background(), tableParamsOverrideOnly); err != nil {
		return err
	}
	s.ignoreProviderCost, err = live.NewTable[schemas.ModelProvider, struct{}](context.Background(), tableIgnoreProviderCost)
	return err
}

// tableEntries ranges over t's entries, so a loop over a table reads as one over a map. Each shard is
// copied before the loop body runs, so the body may write the table.
func tableEntries[K comparable, V any](t *livestore.Table[K, V]) iter.Seq2[K, V] {
	return func(yield func(K, V) bool) { t.Range(yield) }
}
