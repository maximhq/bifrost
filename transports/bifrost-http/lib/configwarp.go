package lib

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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

	vectorStore        *vectorstore.Config // warp.vector_store: Warp's own vector store, nil for the shared one
	vectorStoreRaw     json.RawMessage     // warp.vector_store as declared, for MarshalJSON
	vectorStoreInvalid error               // why warp.vector_store could not be parsed
}

// UnmarshalJSON keeps the section compacted and checks it decodes as a Warp config write. A
// malformed one is recorded rather than returned, so it is skipped with a warning when applied,
// like one the settings page would refuse, instead of failing the load of the whole file.
//
// vector_store is taken out first. It is boot-time infrastructure, not a setting the page saves:
// it never reaches SaveConfig and is not part of the sync hash, so editing it does not re-apply
// the rest of the section over dashboard edits. A section without it keeps its bytes as written.
func (w *WarpFileConfig) UnmarshalJSON(data []byte) error {
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		w.raw, w.invalid = nil, err
		return nil
	}
	w.raw, w.invalid = compact.Bytes(), nil
	w.vectorStore, w.vectorStoreRaw, w.vectorStoreInvalid = nil, nil, nil
	if section := gjson.GetBytes(w.raw, "vector_store"); section.Exists() {
		w.vectorStoreRaw = json.RawMessage(section.Raw)
		var store vectorstore.Config
		if err := json.Unmarshal([]byte(section.Raw), &store); err != nil {
			w.vectorStoreInvalid = err
		} else {
			w.vectorStore = &store
		}
		stripped, err := sjson.DeleteBytes(w.raw, "vector_store")
		if err != nil {
			w.raw, w.invalid = nil, err
			return nil
		}
		w.raw = stripped
		// A section that only gives Warp its store declares no settings. Applying
		// the empty remainder would save enabled=false and switch Warp off.
		if gjson.ParseBytes(stripped).IsObject() && len(gjson.ParseBytes(stripped).Map()) == 0 {
			w.raw = nil
			return nil
		}
	}
	var probe warp.ConfigInput
	if err := json.Unmarshal(w.raw, &probe); err != nil {
		w.invalid = err
	}
	return nil
}

// MarshalJSON writes the section back as declared.
func (w WarpFileConfig) MarshalJSON() ([]byte, error) {
	raw := w.raw
	if len(w.vectorStoreRaw) > 0 {
		if len(raw) == 0 {
			raw = []byte("{}")
		}
		return sjson.SetRawBytes(raw, "vector_store", w.vectorStoreRaw)
	}
	if len(raw) == 0 {
		return []byte("null"), nil
	}
	return raw, nil
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

// initWarpVectorStore connects Warp's own vector store when config.json's warp section declares
// one. It never fails the boot: a store that is invalid or cannot be reached leaves Warp
// unavailable, with the cause in the log, and the rest of Bifrost runs. Warp does not fall back
// to the shared store, which would put the log index where the admin chose to keep it out of.
//
// A declaration that is disabled, or that points at the shared store's own location, is not a
// separate store: Warp keeps using the shared one, whose data it already indexed.
//
// The store's ID for backfill checkpoints (warp.VectorStoreSource.ID) is a hash of its location,
// so a rotated credential is the same store and job metadata holds no endpoint. A memory-only store is empty at every start, and one with
// no location cannot be told from another, so each gets an ID of its own at every start: a
// checkpoint from before a restart is never resumed there.
func initWarpVectorStore(ctx context.Context, config *Config, configData *ConfigData) {
	if config == nil || configData == nil || configData.Warp == nil {
		return
	}
	section := configData.Warp
	if section.vectorStoreInvalid != nil {
		config.WarpVectorStoreSource = warp.VectorStoreSource{Dedicated: true, Failed: true}
		logger.Error("warp.vector_store in config.json is invalid, so Warp is unavailable until it is fixed and Bifrost restarts (the rest of Bifrost is running): %v", section.vectorStoreInvalid)
		return
	}
	declared := section.vectorStore
	if declared == nil {
		return
	}
	if !declared.Enabled {
		logger.Info("warp.vector_store in config.json is disabled; Warp uses the shared vector store")
		return
	}
	location := declared.Location()
	if shared := configData.VectorStoreConfig; shared != nil && shared.Enabled && location != "" && shared.Location() == location {
		logger.Warn("warp.vector_store in config.json points at the same %s store as vector_store; Warp uses the shared vector store", declared.Type)
		return
	}
	source := warp.VectorStoreSource{Dedicated: true, Type: string(declared.Type), ID: warpVectorStoreID(declared)}
	logger.Info("connecting to Warp's vector store (%s)", declared.Type)
	connectCtx, cancel := context.WithTimeout(ctx, warpVectorStoreConnectTimeout)
	defer cancel()
	store, err := vectorstore.NewVectorStore(connectCtx, declared, logger)
	if err != nil {
		source.Failed = true
		config.WarpVectorStoreSource = source
		logger.Error("Warp's vector store (warp.vector_store in config.json, %s) could not be reached, so Warp is unavailable until it is fixed and Bifrost restarts (the rest of Bifrost is running): %v", declared.Type, err)
		return
	}
	config.WarpVectorStore = store
	config.WarpVectorStoreSource = source
}

// warpVectorStoreConnectTimeout bounds connecting Warp's own store at startup. A
// store that accepts the connection and then never answers would otherwise
// hold startup forever; past it, Warp is unavailable and Bifrost starts.
var warpVectorStoreConnectTimeout = 30 * time.Second

// warpVectorStoreID is a dedicated store's ID for backfill checkpoints; see initWarpVectorStore.
func warpVectorStoreID(declared *vectorstore.Config) string {
	location := declared.Location()
	if location == "" {
		return "unknown:" + uuid.NewString()
	}
	sum := sha256.Sum256([]byte(location))
	id := hex.EncodeToString(sum[:16])
	if declared.InMemory() {
		return id + "#" + uuid.NewString()
	}
	return id
}
