package configstore

import (
	"context"
	"errors"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// GetLocalCacheConfig retrieves the local-cache configuration from the database.
// Returns (nil, nil) when no row exists, so callers can distinguish "not yet
// configured" from a hard error and apply their own defaults.
func (s *RDBConfigStore) GetLocalCacheConfig(ctx context.Context) (*LocalCacheConfig, error) {
	var dbConfig tables.TableLocalCacheConfig
	if err := s.DB().WithContext(ctx).First(&dbConfig).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &LocalCacheConfig{
		Provider:                     schemas.ModelProvider(dbConfig.Provider),
		EmbeddingModel:               dbConfig.EmbeddingModel,
		CleanUpOnShutdown:            dbConfig.CleanUpOnShutdown,
		TTL:                          time.Duration(dbConfig.TTLSeconds) * time.Second,
		Threshold:                    dbConfig.Threshold,
		VectorStoreNamespace:         dbConfig.VectorStoreNamespace,
		Dimension:                    dbConfig.Dimension,
		DefaultCacheKey:              dbConfig.DefaultCacheKey,
		ConversationHistoryThreshold: dbConfig.ConversationHistoryThreshold,
		CacheByModel:                 dbConfig.CacheByModel,
		CacheByProvider:              dbConfig.CacheByProvider,
		ExcludeSystemPrompt:          dbConfig.ExcludeSystemPrompt,
		ConfigHash:                   dbConfig.ConfigHash,
	}, nil
}

// UpdateLocalCacheConfig persists the local-cache configuration. The table is
// single-row: existing rows are deleted before insert so callers always
// observe exactly one row.
func (s *RDBConfigStore) UpdateLocalCacheConfig(ctx context.Context, config *LocalCacheConfig) error {
	if config == nil {
		return nil
	}
	dbConfig := tables.TableLocalCacheConfig{
		Provider:                     string(config.Provider),
		EmbeddingModel:               config.EmbeddingModel,
		CleanUpOnShutdown:            config.CleanUpOnShutdown,
		TTLSeconds:                   int64(config.TTL / time.Second),
		Threshold:                    config.Threshold,
		VectorStoreNamespace:         config.VectorStoreNamespace,
		Dimension:                    config.Dimension,
		DefaultCacheKey:              config.DefaultCacheKey,
		ConversationHistoryThreshold: config.ConversationHistoryThreshold,
		CacheByModel:                 config.CacheByModel,
		CacheByProvider:              config.CacheByProvider,
		ExcludeSystemPrompt:          config.ExcludeSystemPrompt,
		ConfigHash:                   config.ConfigHash,
	}
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&tables.TableLocalCacheConfig{}).Error; err != nil {
			return err
		}
		return tx.Create(&dbConfig).Error
	})
}
