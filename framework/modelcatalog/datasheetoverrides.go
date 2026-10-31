package modelcatalog

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

type (
	DatasheetEntry       = datasheet.DatasheetEntry
	DatasheetEntryDetail = datasheet.DatasheetEntryDetail
	DatasheetEntryQuery  = datasheet.DatasheetEntryQuery
)

// ListDatasheetEntries returns the datasheet as the editor shows it: upstream
// and effective values per (provider, model, mode), with the applied overrides.
func (mc *ModelCatalog) ListDatasheetEntries(q DatasheetEntryQuery) ([]DatasheetEntry, int) {
	return mc.datasheet.ListDatasheetEntries(q)
}

// GetDatasheetEntry returns one datasheet entry including its model-parameters
// blob before and after overrides, or nil when nothing exists for the target.
func (mc *ModelCatalog) GetDatasheetEntry(ctx context.Context, provider, model, mode string) (*DatasheetEntryDetail, error) {
	return mc.datasheet.GetDatasheetEntry(ctx, provider, model, mode)
}

// FindDatasheetOverride returns the override saved for an exact
// (provider, model, mode) target, or nil.
func (mc *ModelCatalog) FindDatasheetOverride(provider, model, mode string) *configstoreTables.TableDatasheetOverride {
	return mc.datasheet.FindEntryOverride(provider, model, mode)
}

// ValidateDatasheetOverride checks an override without persisting it.
func ValidateDatasheetOverride(row *configstoreTables.TableDatasheetOverride) error {
	_, err := datasheet.ParseEntryOverride(row)
	return err
}

// SaveDatasheetOverride validates the override, baselines the upstream values
// it replaces, persists it by (provider, model, mode) target and applies it.
// Peers reload through the after-sync hook.
func (mc *ModelCatalog) SaveDatasheetOverride(ctx context.Context, row *configstoreTables.TableDatasheetOverride) error {
	if mc.configStore == nil {
		return fmt.Errorf("datasheet overrides require a config store")
	}
	parsed, err := datasheet.ParseEntryOverride(row)
	if err != nil {
		return err
	}
	row.Provider, row.Model, row.Mode = parsed.Provider, parsed.Model, parsed.Mode
	baseline, err := mc.datasheet.BuildEntryBaseline(ctx, &parsed)
	if err != nil {
		return fmt.Errorf("failed to snapshot upstream values: %w", err)
	}
	if row.Baseline, err = providerUtils.MarshalSorted(baseline); err != nil {
		return err
	}
	if row.ID == "" {
		row.ID = uuid.NewString()
	}
	if err := mc.configStore.UpsertDatasheetOverride(ctx, row); err != nil {
		return err
	}
	if err := mc.datasheet.UpsertEntryOverride(ctx, *row); err != nil {
		return err
	}
	mc.notifyDatasheetChanged(ctx)
	return nil
}

// DeleteDatasheetOverride removes an override, restoring the upstream values.
func (mc *ModelCatalog) DeleteDatasheetOverride(ctx context.Context, id string) error {
	if mc.configStore == nil {
		return fmt.Errorf("datasheet overrides require a config store")
	}
	if err := mc.configStore.DeleteDatasheetOverride(ctx, id); err != nil {
		return err
	}
	mc.datasheet.DeleteEntryOverride(ctx, id)
	mc.notifyDatasheetChanged(ctx)
	return nil
}

// SetDatasheetOverrides replaces the in-memory override set, for the
// config.json load path; persistence is the caller's.
func (mc *ModelCatalog) SetDatasheetOverrides(ctx context.Context, rows []configstoreTables.TableDatasheetOverride) {
	mc.datasheet.SetEntryOverrides(ctx, rows)
}

// ReloadDatasheetOverrides re-reads the overrides from the config store and
// re-applies them.
func (mc *ModelCatalog) ReloadDatasheetOverrides(ctx context.Context) error {
	return mc.datasheet.ReloadEntryOverrides(ctx)
}

// notifyDatasheetChanged drops capability records and asks peers to reload.
func (mc *ModelCatalog) notifyDatasheetChanged(ctx context.Context) {
	if mc.capabilities != nil {
		mc.capabilities.Flush()
	}
	if mc.afterSyncHook != nil {
		mc.afterSyncHook(ctx)
	}
}
