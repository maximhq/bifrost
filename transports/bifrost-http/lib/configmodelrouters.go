package lib

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/maximhq/bifrost/framework/configstore"
)

// fileModelRouters validates config.json's model_routers entries and returns
// the valid ones, keeping the first valid declaration of an id and skipping,
// and logging, every later one. An entry that fails validation is dropped
// before its id is counted, so it cannot shadow a valid declaration of the
// same id.
func fileModelRouters(configData *ConfigData) []declaredModelRouter {
	seen := make(map[string]bool, len(configData.ModelRouters))
	declared := make([]declaredModelRouter, 0, len(configData.ModelRouters))
	for _, entry := range configData.ModelRouters {
		router, asWritten, ok := fileModelRouter(entry)
		if !ok {
			continue
		}
		if seen[router.ID] {
			logger.Error("skipping model_routers entry: id %q is declared more than once", router.ID)
			continue
		}
		seen[router.ID] = true
		declared = append(declared, declaredModelRouter{router: router, asWritten: asWritten})
	}
	return declared
}

// declaredModelRouter is one valid config.json router, normalized, with the
// config as written for the split-mode merge.
type declaredModelRouter struct {
	router    configstore.ModelRouter
	asWritten *configstore.ComplexityAnalyzerConfig
}

// fileModelRouter validates one config.json router and returns it normalized,
// with its section hashes and its identity hash, plus the config as written
// for the split-mode merge.
func fileModelRouter(entry configstore.ModelRouter) (router configstore.ModelRouter, asWritten *configstore.ComplexityAnalyzerConfig, ok bool) {
	if err := configstore.ValidateModelRouterIdentity(&entry); err != nil {
		logger.Error("invalid model_routers entry in config file: %v", err)
		return configstore.ModelRouter{}, nil, false
	}
	raw := entry.ComplexityAnalyzerConfig
	normalized, asWritten, _, ok := complexityAnalyzerConfigFromFile(fmt.Sprintf("model router %q", entry.ID), &raw)
	if !ok {
		return configstore.ModelRouter{}, nil, false
	}
	return configstore.ModelRouter{
		ID:                       entry.ID,
		Name:                     entry.Name,
		Description:              entry.Description,
		ComplexityAnalyzerConfig: *normalized,
		ConfigHash:               configstore.GenerateModelRouterHash(entry),
	}, asWritten, true
}

// syncModelRoutersFromFile reconciles config.json's model routers with the
// store when the file has a model_routers section. Routers are independent of
// governance.complexity_analyzer_config, which mergeGovernanceConfig applies.
//
// In source-of-truth mode the file's config is written whenever it differs
// from the stored one, and stored routers the file does not declare are
// deleted. In split mode only the sections whose hash changed are applied,
// merged into what the store holds, so dashboard edits to the rest survive a
// reboot, and a router new to the store is created from the file as written.
// Name and description follow their own hash the same way. Each router is
// written on its own, so one bad entry is logged and skipped without holding
// back the others. Without a config store there is nowhere to keep them, so
// the section is skipped with a warning. A null section decodes to a nil list
// and is treated as omitted; only an explicit `[]` prunes every router.
func syncModelRoutersFromFile(ctx context.Context, config *Config, configData *ConfigData) {
	if config == nil || configData == nil || !configData.sectionPresent("model_routers") || configData.ModelRouters == nil {
		return
	}
	if config.ConfigStore == nil {
		logger.Warn("config file declares model_routers, but there is no config store to keep them; skipping")
		return
	}
	declared := fileModelRouters(configData)

	stored, err := config.ConfigStore.GetModelRouters(ctx)
	if err != nil {
		logger.Error("failed to read model routers for config file sync: %v", err)
		return
	}
	storedByID := make(map[string]configstore.ModelRouter, len(stored))
	for _, router := range stored {
		storedByID[router.ID] = router
	}
	sourceOfTruth := configData.isConfigJSONSourceOfTruth()

	// Undeclared routers go first, so a declared router can take the name of
	// one being removed, as routing rules are pruned before their inserts.
	if sourceOfTruth {
		keep := map[string]bool{}
		for _, entry := range configData.ModelRouters {
			// Every declared id is kept, valid or not: a typo in an entry must
			// not delete the router it meant to configure.
			keep[strings.TrimSpace(entry.ID)] = true
		}
		for _, router := range stored {
			if keep[router.ID] {
				continue
			}
			if err := config.ConfigStore.DeleteModelRouter(ctx, router.ID); err != nil && !errors.Is(err, configstore.ErrNotFound) {
				logger.Error("failed to delete model router %q absent from config file: %v", router.ID, err)
			}
		}
	}

	for _, d := range declared {
		current, exists := storedByID[d.router.ID]
		next := d.router
		if exists && !sourceOfTruth {
			identityChanged := current.ConfigHash != d.router.ConfigHash
			configChanged := !current.ConfigHashes.Equal(d.router.ConfigHashes)
			if !identityChanged && !configChanged {
				continue
			}
			next = current
			if configChanged {
				merged, err := configstore.MergeComplexityAnalyzerConfigByHashes(&current.ComplexityAnalyzerConfig, d.asWritten)
				if err != nil {
					logger.Error("failed to merge model router %q from config file: %v", d.router.ID, err)
					continue
				}
				next.ComplexityAnalyzerConfig = *merged
			}
			if identityChanged {
				next.Name, next.Description, next.ConfigHash = d.router.Name, d.router.Description, d.router.ConfigHash
			}
		}
		if exists {
			if reflect.DeepEqual(current, next) {
				continue
			}
			err = config.ConfigStore.UpdateModelRouter(ctx, &next)
		} else {
			err = config.ConfigStore.CreateModelRouter(ctx, &next)
			if errors.Is(err, configstore.ErrAlreadyExists) {
				// A stored row this version cannot read is left out of the list
				// above, and the file's declaration replaces it. Any other clash
				// is the name, held by another router.
				if _, getErr := config.ConfigStore.GetModelRouter(ctx, next.ID); errors.Is(getErr, configstore.ErrConfigUnreadable) {
					err = config.ConfigStore.UpdateModelRouter(ctx, &next)
				} else {
					err = fmt.Errorf("the name %q is already used by another model router", next.Name)
				}
			}
		}
		if err != nil {
			logger.Error("failed to sync model router %q from config file: %v", d.router.ID, err)
		}
	}
}
