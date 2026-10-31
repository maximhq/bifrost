package lib

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// DatasheetOverrideFileConfig is one datasheet_overrides entry in config.json:
// a user edit of the synced datasheet for a (provider, model, mode) target.
type DatasheetOverrideFileConfig struct {
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	Mode         string          `json:"mode,omitempty"`
	IsCustom     bool            `json:"is_custom,omitempty"`
	PricingPatch json.RawMessage `json:"pricing_patch,omitempty"`
	ParamsPatch  json.RawMessage `json:"params_patch,omitempty"`
}

func (c DatasheetOverrideFileConfig) row() tables.TableDatasheetOverride {
	return tables.TableDatasheetOverride{
		Provider:     strings.TrimSpace(c.Provider),
		Model:        strings.TrimSpace(c.Model),
		Mode:         strings.TrimSpace(c.Mode),
		IsCustom:     c.IsCustom,
		PricingPatch: c.PricingPatch,
		ParamsPatch:  c.ParamsPatch,
	}
}

func datasheetOverrideTargetKey(row tables.TableDatasheetOverride) string {
	return row.Provider + "|" + row.Model + "|" + row.Mode
}

// generateDatasheetOverrideHash fingerprints a config.json entry. Patches are
// re-encoded with sorted keys so reformatting the file is not a change.
func generateDatasheetOverrideHash(row tables.TableDatasheetOverride) (string, error) {
	canonical := func(raw json.RawMessage) (string, error) {
		if len(raw) == 0 {
			return "", nil
		}
		var v any
		if err := sonic.Unmarshal(raw, &v); err != nil {
			return "", err
		}
		out, err := providerUtils.MarshalSorted(v)
		return string(out), err
	}
	pricing, err := canonical(row.PricingPatch)
	if err != nil {
		return "", err
	}
	params, err := canonical(row.ParamsPatch)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	for _, part := range []string{datasheetOverrideTargetKey(row), strconv.FormatBool(row.IsCustom), pricing, params} {
		hash.Write([]byte(part))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// loadDatasheetOverridesFromFile applies config.json datasheet_overrides once
// the model catalog is up. With a config store a file entry is written only
// when it changed since the last load (by hash), so a UI edit of the same
// target stands until the file entry itself changes; in config.json
// source-of-truth mode, stored overrides the file no longer lists are removed.
// Without a config store the entries apply in memory only.
func loadDatasheetOverridesFromFile(ctx context.Context, config *Config, configData *ConfigData) {
	if config == nil || config.ModelCatalog == nil || configData == nil {
		return
	}
	prune := configData.isConfigJSONSourceOfTruth() && configData.sectionPresent("datasheet_overrides")
	if len(configData.DatasheetOverrides) == 0 && !prune {
		return
	}

	rows := make([]tables.TableDatasheetOverride, 0, len(configData.DatasheetOverrides))
	for i, entry := range configData.DatasheetOverrides {
		row := entry.row()
		if err := modelcatalog.ValidateDatasheetOverride(&row); err != nil {
			logger.Warn("datasheet_overrides[%d] (%s/%s) skipped: %v", i, row.Provider, row.Model, err)
			continue
		}
		hash, err := generateDatasheetOverrideHash(row)
		if err != nil {
			logger.Warn("datasheet_overrides[%d] (%s/%s) skipped: %v", i, row.Provider, row.Model, err)
			continue
		}
		row.ConfigHash = hash
		rows = append(rows, row)
	}

	if config.ConfigStore == nil {
		config.ModelCatalog.SetDatasheetOverrides(ctx, rows)
		return
	}
	stored, err := config.ConfigStore.GetDatasheetOverrides(ctx)
	if err != nil {
		logger.Warn("failed to read datasheet overrides, config.json entries not applied: %v", err)
		return
	}
	storedByTarget := make(map[string]tables.TableDatasheetOverride, len(stored))
	for _, row := range stored {
		storedByTarget[datasheetOverrideTargetKey(row)] = row
	}
	inFile := make(map[string]bool, len(rows))
	for i := range rows {
		key := datasheetOverrideTargetKey(rows[i])
		inFile[key] = true
		if existing, ok := storedByTarget[key]; ok && existing.ConfigHash == rows[i].ConfigHash {
			continue
		}
		if err := config.ModelCatalog.SaveDatasheetOverride(ctx, &rows[i]); err != nil {
			logger.Warn("failed to apply datasheet override %s from config.json: %v", key, err)
		}
	}
	if !prune {
		return
	}
	for key, row := range storedByTarget {
		if inFile[key] {
			continue
		}
		if err := config.ModelCatalog.DeleteDatasheetOverride(ctx, row.ID); err != nil {
			logger.Warn("failed to remove datasheet override %s absent from config.json: %v", key, err)
		}
	}
}
