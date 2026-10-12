package lib

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
)

// LoadLogStoreForMaintenance loads only the effective logs-store configuration
// and constructs that store. Unlike LoadConfig, it does not run gateway startup
// reconciliation or clear restart-required state in the shared config store.
func LoadLogStoreForMaintenance(ctx context.Context, configDirPath string, logger schemas.Logger) (*logstore.Config, logstore.LogStore, error) {
	configFilePath := filepath.Join(configDirPath, "config.json")
	configDBPath := filepath.Join(configDirPath, "config.db")
	logsDBPath := filepath.Join(configDirPath, "logs.db")

	var configData ConfigData
	data, err := os.ReadFile(configFilePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("failed to read config file: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &configData); err != nil {
			return nil, nil, fmt.Errorf("failed to unmarshal config: %w", err)
		}
	}
	if err := initEncryption(&configData); err != nil {
		return nil, nil, err
	}
	initVault(&configData)

	logsConfig := configData.LogsStoreConfig
	if logsConfig == nil {
		configStoreConfig := configData.ConfigStoreConfig
		if configStoreConfig == nil {
			configStoreConfig = &configstore.Config{
				Enabled: true,
				Type:    configstore.ConfigStoreTypeSQLite,
				Config: &configstore.SQLiteConfig{
					Path: configDBPath,
				},
			}
		}
		if configStoreConfig.Enabled {
			store, err := configstore.NewConfigStore(ctx, configStoreConfig, logger)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to initialize config store: %w", err)
			}
			defer store.Close(ctx)
			logsConfig, err = store.GetLogsStoreConfig(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to get logs store config: %w", err)
			}
		}
	}
	if logsConfig == nil {
		logsConfig = &logstore.Config{
			Enabled: true,
			Type:    logstore.LogStoreTypeSQLite,
			Config: &logstore.SQLiteConfig{
				Path: logsDBPath,
			},
		}
	}
	if !logsConfig.Enabled {
		return logsConfig, nil, nil
	}

	store, err := logstore.NewLogStore(ctx, logsConfig, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize logs store: %w", err)
	}
	return logsConfig, store, nil
}
