package lib

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/warp"
)

// WarpFileConfig is config.json's warp section: the Warp settings page, models, behaviour, log
// search and topics, in the body PUT /api/warp/config takes.
//
// It keeps the declared bytes rather than a decoded warp.ConfigInput. ConfigInput records which
// keys a write carried, so an omitted embedding or topic field keeps its stored value the way it
// does for a dashboard save, and SaveConfig normalizes the input in place; each apply therefore
// decodes a fresh one. The bytes are also what the sync hash is taken over.
type WarpFileConfig struct {
	raw     json.RawMessage
	invalid error // why the section cannot be a Warp config write; ApplyWarpFileConfig skips it
}

// UnmarshalJSON keeps the section compacted and checks it decodes as a Warp config write. A
// malformed one is recorded rather than returned, so it is skipped with a warning when applied,
// like one the settings page would refuse, instead of failing the load of the whole file.
func (w *WarpFileConfig) UnmarshalJSON(data []byte) error {
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		w.raw, w.invalid = nil, err
		return nil
	}
	w.raw, w.invalid = compact.Bytes(), nil
	var probe warp.ConfigInput
	if err := json.Unmarshal(data, &probe); err != nil {
		w.invalid = err
	}
	return nil
}

// MarshalJSON writes the section back as declared.
func (w WarpFileConfig) MarshalJSON() ([]byte, error) {
	if len(w.raw) == 0 {
		return []byte("null"), nil
	}
	return w.raw, nil
}

// WarpConfigSaver is the Warp service's write path, the one the settings page saves through.
type WarpConfigSaver interface {
	SaveConfig(ctx context.Context, input *warp.ConfigInput) (warp.ConfigView, error)
}

// ApplyWarpFileConfig applies config.json's warp section through the Warp service, the way the
// other file sections are reconciled:
//
//   - split (the default): the declaration is applied when it is new or changed since the hash
//     recorded at the last sync, and a dashboard edit to an unchanged declaration is kept
//   - config.json source of truth: the declaration is applied on every boot
//
// It goes through SaveConfig rather than writing the row, because a save does more than store:
// it validates against the same rules as the settings page, provisions the log vector namespace,
// retires the old one when the embedding space changes, and refuses a change while a backfill
// runs. That is also why it runs from the server once the service exists, not from LoadConfig.
//
// The hash lives in its own row (ConfigWarpHashKey) because the settings page saves the Warp row
// whole. An absent or malformed warp section leaves the stored settings alone.
func (c *Config) ApplyWarpFileConfig(ctx context.Context, saver WarpConfigSaver) {
	if c == nil || c.warpFileConfig == nil || saver == nil {
		return
	}
	if c.warpFileConfig.invalid != nil {
		logger.Warn("invalid warp config in config.json, not applied: %v", c.warpFileConfig.invalid)
		return
	}
	if len(c.warpFileConfig.raw) == 0 {
		return
	}
	if c.ConfigStore == nil {
		logger.Warn("warp in config.json needs a config store, not applied")
		return
	}
	fileHash := fmt.Sprintf("%x", sha256.Sum256(c.warpFileConfig.raw))
	if !c.warpFileIsSourceOfTruth {
		storedHash, err := c.ConfigStore.GetConfig(ctx, configstoreTables.ConfigWarpHashKey)
		if err != nil && !errors.Is(err, configstore.ErrNotFound) {
			logger.Warn("failed to read the warp config hash: %v", err)
			return
		}
		if storedHash != nil && storedHash.Value == fileHash {
			logger.Debug("warp config hash matches, keeping DB config")
			return
		}
	}
	var input warp.ConfigInput
	if err := json.Unmarshal(c.warpFileConfig.raw, &input); err != nil {
		logger.Warn("invalid warp config in config.json, not applied: %v", err)
		return
	}
	// Cleared before the save and set after it, so a save that fails leaves no hash rather than
	// the previous declaration's, and the next boot tries the file again.
	if err := c.ConfigStore.UpdateConfig(ctx, &configstoreTables.TableGovernanceConfig{
		Key: configstoreTables.ConfigWarpHashKey,
	}); err != nil {
		logger.Warn("failed to clear the warp config hash, warp from config.json not applied: %v", err)
		return
	}
	if _, err := saver.SaveConfig(ctx, &input); err != nil {
		logger.Warn("warp config from config.json not applied: %v", err)
		return
	}
	if err := c.ConfigStore.UpdateConfig(ctx, &configstoreTables.TableGovernanceConfig{
		Key:   configstoreTables.ConfigWarpHashKey,
		Value: fileHash,
	}); err != nil {
		logger.Warn("failed to record the warp config hash: %v", err)
	}
	logger.Info("warp config synced from config.json (enabled=%t)", input.Enabled)
}
