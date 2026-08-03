package configstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestModelConfigRateLimitsPreloadAndDeleteOwnership(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	now := time.Now()
	max := int64(15)
	minute := "1m"
	day := "1d"
	modelID := "model-multi"
	sharedLegacyID := "legacy-shared"
	ownedRequest := tables.TableRateLimit{
		ID:                   "model-rpm",
		ModelConfigID:        &modelID,
		Metric:               tables.ModelRateLimitMetricRequests,
		RequestMaxLimit:      &max,
		RequestResetDuration: &minute,
		RequestLastReset:     now,
	}
	ownedToken := tables.TableRateLimit{
		ID:                 "model-tpm",
		ModelConfigID:      &modelID,
		Metric:             tables.ModelRateLimitMetricTokens,
		TokenMaxLimit:      &max,
		TokenResetDuration: &day,
		TokenLastReset:     now,
	}
	legacyMax := int64(100)
	legacyDuration := "1h"
	sharedLegacy := tables.TableRateLimit{
		ID:                   sharedLegacyID,
		RequestMaxLimit:      &legacyMax,
		RequestResetDuration: &legacyDuration,
		RequestLastReset:     now,
	}

	model := &tables.TableModelConfig{
		ID:          modelID,
		ModelName:   "gpt-test",
		Scope:       tables.ModelConfigScopeGlobal,
		RateLimitID: &sharedLegacyID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	require.NoError(t, store.CreateModelConfig(ctx, model))
	require.NoError(t, store.DB().Create(&ownedRequest).Error)
	require.NoError(t, store.DB().Create(&ownedToken).Error)
	require.NoError(t, store.DB().Create(&sharedLegacy).Error)
	require.NoError(t, store.DB().Create(&tables.TableProvider{Name: "shared-provider", RateLimitID: &sharedLegacyID, CreatedAt: now, UpdatedAt: now}).Error)

	loaded, err := store.GetModelConfigByID(ctx, modelID)
	require.NoError(t, err)
	require.Len(t, loaded.RateLimits, 2)

	require.NoError(t, store.DeleteModelConfig(ctx, modelID))
	var remaining []tables.TableRateLimit
	require.NoError(t, store.DB().Find(&remaining).Error)
	require.Len(t, remaining, 1)
	require.Equal(t, sharedLegacyID, remaining[0].ID)
}

func TestMigrationAddsModelRateLimitOwnershipAndCleansOrphans(t *testing.T) {
	store := setupRDBTestStore(t)
	db := store.DB()

	if db.Migrator().HasConstraint(&tables.TableModelConfig{}, "RateLimits") {
		require.NoError(t, db.Migrator().DropConstraint(&tables.TableModelConfig{}, "RateLimits"))
	}
	orphanModelID := "missing-model-config"
	max := int64(15)
	duration := "1m"
	orphan := tables.TableRateLimit{
		ID:                   "orphan-model-rule",
		ModelConfigID:        &orphanModelID,
		Metric:               tables.ModelRateLimitMetricRequests,
		RequestMaxLimit:      &max,
		RequestResetDuration: &duration,
		RequestLastReset:     time.Now(),
	}
	require.NoError(t, db.Create(&orphan).Error)

	require.NoError(t, migrationAddModelConfigRateLimits(context.Background(), db, testMigrationLogger))
	require.True(t, db.Migrator().HasColumn(&tables.TableRateLimit{}, "model_config_id"))
	require.True(t, db.Migrator().HasColumn(&tables.TableRateLimit{}, "metric"))
	require.True(t, db.Migrator().HasConstraint(&tables.TableModelConfig{}, "RateLimits"))

	var remaining tables.TableRateLimit
	err := db.First(&remaining, "id = ?", orphan.ID).Error
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
