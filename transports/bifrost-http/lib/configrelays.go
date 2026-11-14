package lib

import (
	"context"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// loadRelaysConfig reconciles the top-level "relays" section against the
// config store, the way loadAgentsConfig reconciles agents: declarations are validated with a
// warn-and-skip policy, matched by name using the config hash for change detection, and pruned only
// when config.json is the source of truth and the section is present. Writes go through the
// configstore CRUD, so the vault callbacks and encryption hooks see every secret. The runtime manager
// is built later from the reconciled rows.
func loadRelaysConfig(ctx context.Context, config *Config, configData *ConfigData) {
	declared := make(map[string]bool)
	counts := make(map[string]int)
	for _, cfg := range configData.Relays {
		if cfg == nil {
			continue
		}
		// Matched on the exact name: that is what the store keys on, and the name pattern rejects
		// whitespace, so a padded declaration must not shadow or protect the real one.
		if strings.TrimSpace(cfg.Name) != "" {
			declared[cfg.Name] = true
			counts[cfg.Name]++
		}
	}
	var valid []*schemas.RelayConfig
	for _, cfg := range configData.Relays {
		if cfg == nil {
			continue
		}
		if counts[cfg.Name] > 1 {
			logger.Warn("skipping duplicate relay %q from config file", cfg.Name)
			continue
		}
		if err := cfg.ValidateEndpoint(); err != nil {
			logger.Warn("skipping relay %q from config file: %v", cfg.Name, err)
			continue
		}
		valid = append(valid, cfg)
	}
	if config.ConfigStore == nil {
		if len(valid) > 0 {
			logger.Warn("config store is disabled - relays from config file will not be available")
		}
		return
	}
	hashes, err := config.ConfigStore.GetRelayConfigHashes(ctx)
	if err != nil {
		logger.Warn("failed to load relays: %v", err)
		return
	}
	keep := make(map[string]bool, len(valid))
	for _, cfg := range valid {
		storedHash, exists := hashes[cfg.Name]
		if exists {
			keep[cfg.Name] = true
		}
		fileHash, err := configstore.GenerateRelayHash(cfg)
		if err != nil {
			logger.Warn("failed to hash relay %q: %v", cfg.Name, err)
			continue
		}
		if exists {
			// An API edit keeps the file's hash on the row, so a matching hash does not mean the
			// stored settings match the file. With config.json as the source of truth the file wins
			// and every declared relay is written; in split mode the hash says the file is unchanged.
			if storedHash == fileHash && !configData.isConfigJSONSourceOfTruth() {
				continue
			}
			if err := config.ConfigStore.UpdateRelay(ctx, cfg, fileHash); err != nil {
				logger.Warn("failed to update relay %q: %v", cfg.Name, err)
			}
			continue
		}
		if err := config.ConfigStore.CreateRelay(ctx, cfg, fileHash); err != nil {
			logger.Warn("failed to create relay %q: %v", cfg.Name, err)
			continue
		}
		keep[cfg.Name] = true
	}
	if !configData.isConfigJSONSourceOfTruth() || !configData.sectionPresent("relays") {
		return
	}
	// The prune fails safe: a stored endpoint survives when its name is declared at all, so a
	// declaration that failed validation keeps its working row instead of deleting it.
	for name := range hashes {
		if keep[name] || declared[name] {
			continue
		}
		if err := config.ConfigStore.DeleteRelay(ctx, name); err != nil {
			logger.Warn("failed to delete relay %q: %v", name, err)
		}
	}
}
