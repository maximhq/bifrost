package handlers

import (
	"testing"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestValidateModelRateLimitRules(t *testing.T) {
	valid := []ModelRateLimitRuleRequest{
		{Metric: configstoreTables.ModelRateLimitMetricRequests, MaxLimit: 15, ResetDuration: "1m"},
		{Metric: configstoreTables.ModelRateLimitMetricRequests, MaxLimit: 1500, ResetDuration: "1d"},
		{Metric: configstoreTables.ModelRateLimitMetricTokens, MaxLimit: 1000, ResetDuration: "1m"},
		{Metric: configstoreTables.ModelRateLimitMetricTokens, MaxLimit: 100000, ResetDuration: "1d"},
	}
	if err := validateModelRateLimitRules(valid, false); err != nil {
		t.Fatalf("valid multi-rule model limit rejected: %v", err)
	}

	withID := []ModelRateLimitRuleRequest{{
		ID:            "existing-model-rate-limit",
		Metric:        configstoreTables.ModelRateLimitMetricRequests,
		MaxLimit:      15,
		ResetDuration: "1m",
	}}
	if err := validateModelRateLimitRules(withID, false); err == nil {
		t.Fatal("expected create validation to reject an existing rate-limit ID")
	}
	if err := validateModelRateLimitRules(withID, true); err != nil {
		t.Fatalf("update validation rejected an existing rate-limit ID: %v", err)
	}

	tests := []struct {
		name  string
		rules []ModelRateLimitRuleRequest
	}{
		{
			name: "duplicate duration aliases",
			rules: []ModelRateLimitRuleRequest{
				{Metric: configstoreTables.ModelRateLimitMetricRequests, MaxLimit: 15, ResetDuration: "60s"},
				{Metric: configstoreTables.ModelRateLimitMetricRequests, MaxLimit: 15, ResetDuration: "1m"},
			},
		},
		{
			name:  "unknown metric",
			rules: []ModelRateLimitRuleRequest{{Metric: "rpm", MaxLimit: 15, ResetDuration: "1m"}},
		},
		{
			name:  "zero limit",
			rules: []ModelRateLimitRuleRequest{{Metric: configstoreTables.ModelRateLimitMetricRequests, MaxLimit: 0, ResetDuration: "1m"}},
		},
		{
			name:  "invalid duration",
			rules: []ModelRateLimitRuleRequest{{Metric: configstoreTables.ModelRateLimitMetricTokens, MaxLimit: 100, ResetDuration: "not-a-duration"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateModelRateLimitRules(tt.rules, false); err == nil {
				t.Fatal("expected invalid model rate-limit rules to be rejected")
			}
		})
	}
}

func TestDeleteModelConfigRateLimitIfUnreferencedProtectsOwnedRule(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&configstoreTables.TableModelConfig{},
		&configstoreTables.TableRateLimit{},
		&configstoreTables.TableProvider{},
		&configstoreTables.TableVirtualKey{},
		&configstoreTables.TableVirtualKeyProviderConfig{},
		&configstoreTables.TableTeam{},
		&configstoreTables.TableCustomer{},
	))

	modelConfigID := "model-config"
	maxLimit := int64(15)
	resetDuration := "1m"
	require.NoError(t, db.Create(&configstoreTables.TableRateLimit{
		ID:                   "owned-model-rule",
		ModelConfigID:        &modelConfigID,
		Metric:               configstoreTables.ModelRateLimitMetricRequests,
		RequestMaxLimit:      &maxLimit,
		RequestResetDuration: &resetDuration,
	}).Error)

	require.NoError(t, deleteModelConfigRateLimitIfUnreferenced(db, "owned-model-rule"))
	var retained configstoreTables.TableRateLimit
	require.NoError(t, db.First(&retained, "id = ?", "owned-model-rule").Error)

	require.NoError(t, db.Model(&configstoreTables.TableRateLimit{}).
		Where("id = ?", "owned-model-rule").Update("model_config_id", nil).Error)
	require.NoError(t, deleteModelConfigRateLimitIfUnreferenced(db, "owned-model-rule"))
	require.Error(t, db.First(&configstoreTables.TableRateLimit{}, "id = ?", "owned-model-rule").Error)
}
