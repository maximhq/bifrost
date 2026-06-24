package configstore

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupRDBTestStore creates an in-memory SQLite database and returns an RDBConfigStore for testing
func setupRDBTestStore(t *testing.T) *RDBConfigStore {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "Failed to create test database")

	// Run migrations for all tables
	err = db.AutoMigrate(
		&tables.TableProvider{},
		&tables.TableKey{},
		&tables.TableBudget{},
		&tables.TableRateLimit{},
		&tables.TableModelConfig{},
		&tables.TableRoutingRule{},
		&tables.TableRoutingTarget{},
		&tables.TablePricingOverride{},
		&tables.TableVirtualKey{},
		&tables.TableVirtualKeyProviderConfig{},
		&tables.TableVirtualKeyProviderConfigKey{},
		&tables.TableModelConfig{},
		&tables.TableCustomer{},
		&tables.TableTeam{},
		&tables.TableClientConfig{},
		&tables.TableGovernanceConfig{},
		&tables.TablePlugin{},
		&tables.TableMCPClient{},
		&tables.TableMCPLibrary{},
		&tables.TableVirtualKeyMCPConfig{},
		&tables.TableFolder{},
		&tables.TablePrompt{},
		&tables.TablePromptVersion{},
		&tables.TablePromptVersionMessage{},
		&tables.TablePromptSession{},
		&tables.TablePromptSessionMessage{},
		&tables.TableOauthUserSession{},
		&tables.TableOauthUserToken{},
		&tables.TableMCPPerUserHeaderCredential{},
		&tables.TableMCPPerUserHeaderFlow{},
	)
	require.NoError(t, err, "Failed to migrate test database")

	// Setup join table
	err = db.SetupJoinTable(&tables.TableVirtualKeyProviderConfig{}, "Keys", &tables.TableVirtualKeyProviderConfigKey{})
	require.NoError(t, err, "Failed to setup join table")

	s := &RDBConfigStore{logger: nil}
	s.db.Store(db)
	s.migrateOnFreshFn = func(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
		return fn(ctx, s.DB())
	}
	s.refreshPoolFn = func(ctx context.Context) error { return nil }
	return s
}

func testComplexityAnalyzerConfig() *ComplexityAnalyzerConfig {
	return &ComplexityAnalyzerConfig{
		TierBoundaries: ComplexityTierBoundaries{
			SimpleMedium:     0.10,
			MediumComplex:    0.30,
			ComplexReasoning: 0.70,
		},
		Keywords: ComplexityEditableKeywordConfig{
			CodeKeywords:      []string{" Function ", "api", "API"},
			ReasoningKeywords: []string{"tradeoffs"},
			TechnicalKeywords: []string{"latency"},
			SimpleKeywords:    []string{"hello"},
		},
	}
}

func TestRDBConfigStore_ComplexityAnalyzerConfigRoundTrip(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	cfg := testComplexityAnalyzerConfig()
	cfg.ConfigHashes = ComplexityAnalyzerConfigHashes{
		TierBoundaries:    "tier-hash-1",
		CodeKeywords:      "code-hash-1",
		ReasoningKeywords: "reason-hash-1",
		TechnicalKeywords: "tech-hash-1",
		SimpleKeywords:    "simple-hash-1",
	}
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, cfg))

	got, err := store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, ComplexityTierBoundaries{
		SimpleMedium:     0.10,
		MediumComplex:    0.30,
		ComplexReasoning: 0.70,
	}, got.TierBoundaries)
	assert.Equal(t, []string{"api", "function"}, got.Keywords.CodeKeywords)
	assert.Equal(t, cfg.ConfigHashes, got.ConfigHashes)
}

func TestRDBConfigStore_GetComplexityAnalyzerConfigMissingReturnsNil(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	got, err := store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestRDBConfigStore_UpdateComplexityAnalyzerConfigPreservesExistingHashesOnRuntimeUpdate(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	fileConfig := testComplexityAnalyzerConfig()
	fileConfig.ConfigHashes = ComplexityAnalyzerConfigHashes{
		TierBoundaries:    "tier-hash-1",
		CodeKeywords:      "code-hash-1",
		ReasoningKeywords: "reason-hash-1",
		TechnicalKeywords: "tech-hash-1",
		SimpleKeywords:    "simple-hash-1",
	}
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, fileConfig))

	runtimeConfig := testComplexityAnalyzerConfig()
	runtimeConfig.TierBoundaries.SimpleMedium = 0.12
	runtimeConfig.ConfigHashes = ComplexityAnalyzerConfigHashes{}
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, runtimeConfig))

	got, err := store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 0.12, got.TierBoundaries.SimpleMedium)
	assert.Equal(t, fileConfig.ConfigHashes, got.ConfigHashes)
}

func TestGenerateComplexityAnalyzerConfigHashesCanonicalizesKeywords(t *testing.T) {
	left := testComplexityAnalyzerConfig()
	right := testComplexityAnalyzerConfig()
	right.Keywords.CodeKeywords = []string{"api", "function"}
	left.ConfigHashes = ComplexityAnalyzerConfigHashes{CodeKeywords: "stored-code-hash-a"}
	right.ConfigHashes = ComplexityAnalyzerConfigHashes{CodeKeywords: "stored-code-hash-b"}

	leftHashes, err := GenerateComplexityAnalyzerConfigHashes(left)
	require.NoError(t, err)
	rightHashes, err := GenerateComplexityAnalyzerConfigHashes(right)
	require.NoError(t, err)

	assert.Equal(t, leftHashes, rightHashes)
}

func TestMergeComplexityAnalyzerConfigAddsKeywordsAndOverlaysBoundaries(t *testing.T) {
	base := testComplexityAnalyzerConfig()
	file := testComplexityAnalyzerConfig()
	file.TierBoundaries = ComplexityTierBoundaries{
		SimpleMedium:     0.20,
		MediumComplex:    0.40,
		ComplexReasoning: 0.80,
	}
	file.Keywords.CodeKeywords = []string{"GraphQL", "api"}
	file.Keywords.ReasoningKeywords = []string{"tradeoffs", "step by step"}
	file.Keywords.TechnicalKeywords = []string{"latency", "kubernetes"}
	file.Keywords.SimpleKeywords = []string{"hello", "thanks"}

	merged, err := MergeComplexityAnalyzerConfig(base, file)
	require.NoError(t, err)
	require.NotNil(t, merged)

	assert.Equal(t, file.TierBoundaries, merged.TierBoundaries)
	assert.Equal(t, []string{"api", "function", "graphql"}, merged.Keywords.CodeKeywords)
	assert.Equal(t, []string{"step by step", "tradeoffs"}, merged.Keywords.ReasoningKeywords)
	assert.Equal(t, []string{"kubernetes", "latency"}, merged.Keywords.TechnicalKeywords)
	assert.Equal(t, []string{"hello", "thanks"}, merged.Keywords.SimpleKeywords)
}

func TestMergeComplexityAnalyzerConfigByHashesOnlyAppliesChangedSections(t *testing.T) {
	base := testComplexityAnalyzerConfig()
	base.ConfigHashes = ComplexityAnalyzerConfigHashes{
		TierBoundaries:    "tier-hash-1",
		CodeKeywords:      "code-hash-1",
		ReasoningKeywords: "reason-hash-1",
		TechnicalKeywords: "tech-hash-1",
		SimpleKeywords:    "simple-hash-1",
	}
	base.TierBoundaries.SimpleMedium = 0.12
	base.Keywords.CodeKeywords = []string{"ui-code"}
	base.Keywords.ReasoningKeywords = []string{"ui-reason"}

	file := testComplexityAnalyzerConfig()
	file.ConfigHashes = base.ConfigHashes
	file.ConfigHashes.CodeKeywords = "code-hash-2"
	file.TierBoundaries.SimpleMedium = 0.20
	file.Keywords.CodeKeywords = []string{"file-code"}
	file.Keywords.ReasoningKeywords = []string{"file-reason"}

	merged, err := MergeComplexityAnalyzerConfigByHashes(base, file)
	require.NoError(t, err)
	require.NotNil(t, merged)

	assert.Equal(t, 0.12, merged.TierBoundaries.SimpleMedium)
	assert.Equal(t, []string{"file-code", "ui-code"}, merged.Keywords.CodeKeywords)
	assert.Equal(t, []string{"ui-reason"}, merged.Keywords.ReasoningKeywords)
	assert.Equal(t, "code-hash-2", merged.ConfigHashes.CodeKeywords)
	assert.Equal(t, "reason-hash-1", merged.ConfigHashes.ReasoningKeywords)
}

func TestRDBConfigStore_GetGovernanceConfigIncludesComplexityAnalyzerConfig(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	cfg := testComplexityAnalyzerConfig()
	cfg.ConfigHashes = ComplexityAnalyzerConfigHashes{
		TierBoundaries:    "tier-hash-2",
		CodeKeywords:      "code-hash-2",
		ReasoningKeywords: "reason-hash-2",
		TechnicalKeywords: "tech-hash-2",
		SimpleKeywords:    "simple-hash-2",
	}
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, cfg))

	governanceConfig, err := store.GetGovernanceConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, governanceConfig)
	require.NotNil(t, governanceConfig.ComplexityAnalyzerConfig)
	assert.Equal(t, 0.70, governanceConfig.ComplexityAnalyzerConfig.TierBoundaries.ComplexReasoning)
	assert.Equal(t, cfg.ConfigHashes, governanceConfig.ComplexityAnalyzerConfig.ConfigHashes)
}

func TestRDBConfigStore_UpdateComplexityAnalyzerConfigRejectsInvalidConfig(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	tests := []struct {
		name   string
		mutate func(*ComplexityAnalyzerConfig)
	}{
		{
			name: "simple medium below minimum",
			mutate: func(cfg *ComplexityAnalyzerConfig) {
				cfg.TierBoundaries.SimpleMedium = -0.1
			},
		},
		{
			name: "medium complex at minimum",
			mutate: func(cfg *ComplexityAnalyzerConfig) {
				cfg.TierBoundaries.MediumComplex = 0
			},
		},
		{
			name: "complex reasoning at maximum",
			mutate: func(cfg *ComplexityAnalyzerConfig) {
				cfg.TierBoundaries.ComplexReasoning = 1.0
			},
		},
		{
			name: "boundaries out of order",
			mutate: func(cfg *ComplexityAnalyzerConfig) {
				cfg.TierBoundaries.ComplexReasoning = cfg.TierBoundaries.MediumComplex - 0.1
			},
		},
		{
			name: "empty code keywords",
			mutate: func(cfg *ComplexityAnalyzerConfig) {
				cfg.Keywords.CodeKeywords = nil
			},
		},
		{
			name: "empty reasoning keywords",
			mutate: func(cfg *ComplexityAnalyzerConfig) {
				cfg.Keywords.ReasoningKeywords = nil
			},
		},
		{
			name: "empty technical keywords",
			mutate: func(cfg *ComplexityAnalyzerConfig) {
				cfg.Keywords.TechnicalKeywords = nil
			},
		},
		{
			name: "empty simple keywords",
			mutate: func(cfg *ComplexityAnalyzerConfig) {
				cfg.Keywords.SimpleKeywords = nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invalid := testComplexityAnalyzerConfig()
			tt.mutate(invalid)

			err := store.UpdateComplexityAnalyzerConfig(ctx, invalid)
			require.Error(t, err)
		})
	}
}

func TestUpsertMCPLibraryEntry(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	entry := &tables.TableMCPLibrary{
		Slug:           "filesystem",
		Name:           "Filesystem",
		Description:    "original",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		AuthType:       schemas.MCPAuthTypeNone,
		Source:         "remote",
	}
	require.NoError(t, store.UpsertMCPLibraryEntry(ctx, entry))

	entry.Description = "updated"
	require.NoError(t, store.UpsertMCPLibraryEntry(ctx, entry))

	entries, totalCount, err := store.GetMCPLibraryPaginated(ctx, MCPLibraryQueryParams{Limit: 1})
	require.NoError(t, err)
	require.Equal(t, int64(1), totalCount)
	require.Len(t, entries, 1)
	require.Equal(t, "updated", entries[0].Description)
}

func TestValidateSkillVersionIncrementRequiresGreaterVersion(t *testing.T) {
	tests := []struct {
		name    string
		latest  string
		next    string
		wantErr bool
	}{
		{name: "rejects lower prerelease core", latest: "1.0.3", next: "1.0.2-1", wantErr: true},
		{name: "accepts same core with suffix after release", latest: "1.0.3", next: "1.0.3-1", wantErr: false},
		{name: "accepts release after same core suffix", latest: "1.0.3-beta1", next: "1.0.3", wantErr: false},
		{name: "accepts higher patch", latest: "1.0.3", next: "1.0.4", wantErr: false},
		{name: "accepts higher minor", latest: "1.0.3", next: "1.1.0", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSkillVersionIncrement(tt.latest, tt.next)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestLatestCreatedSkillVersionUsesCreationOrder(t *testing.T) {
	store := setupRDBTestStore(t)
	err := store.DB().AutoMigrate(
		&tables.TableSkill{},
		&tables.TableSkillVersion{},
		&tables.TableSkillFile{},
		&tables.TableSkillFileBlob{},
	)
	require.NoError(t, err)
	ctx := context.Background()
	baseTime := time.Now()

	skillID := "skill-latest-created"
	err = store.DB().Create(&tables.TableSkill{
		ID:            skillID,
		Name:          "latest-created",
		Description:   "Latest created version test",
		SkillMDBody:   "body",
		LatestVersion: "1.0.3",
		CreatedAt:     baseTime,
		UpdatedAt:     baseTime,
	}).Error
	require.NoError(t, err)
	err = store.DB().Create(&tables.TableSkillVersion{
		ID:                  "skill-version-old",
		SkillID:             skillID,
		Version:             "1.0.3",
		SkillMDBody:         "body",
		FrontmatterSnapshot: tables.SkillJSONMap{"name": "latest-created", "description": "Latest created version test"},
		CreatedAt:           baseTime,
	}).Error
	require.NoError(t, err)
	err = store.DB().Create(&tables.TableSkillVersion{
		ID:                  "skill-version-new",
		SkillID:             skillID,
		Version:             "1.0.2-1",
		SkillMDBody:         "body",
		FrontmatterSnapshot: tables.SkillJSONMap{"name": "latest-created", "description": "Latest created version test"},
		CreatedAt:           baseTime.Add(time.Minute),
	}).Error
	require.NoError(t, err)

	latest, err := latestCreatedSkillVersion(store.DB(), skillID)
	require.NoError(t, err)
	assert.Equal(t, "1.0.2-1", latest)

	skill, err := store.GetSkillLean(ctx, skillID)
	require.NoError(t, err)
	assert.Equal(t, "1.0.2-1", skill.HighestVersion)
}

// =============================================================================
// Provider and Key Tests
// =============================================================================

func TestUpdateProvidersConfig_CreateNew(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	providers := map[schemas.ModelProvider]ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{
					ID:     "key-uuid-1",
					Name:   "openai-primary",
					Value:  *schemas.NewSecretVar("sk-test-key"),
					Weight: 1.0,
				},
			},
		},
	}

	err := store.UpdateProvidersConfig(ctx, providers)
	require.NoError(t, err)

	// Verify provider was created
	result, err := store.GetProvidersConfig(ctx)
	require.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Contains(t, result, schemas.ModelProvider("openai"))
	assert.Len(t, result["openai"].Keys, 1)
	assert.Equal(t, "openai-primary", result["openai"].Keys[0].Name)
}

func TestUpdateProvidersConfig_UpdateExistingByKeyID(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	// Create initial provider with key
	providers := map[schemas.ModelProvider]ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{
					ID:     "key-uuid-1",
					Name:   "openai-primary",
					Value:  *schemas.NewSecretVar("sk-test-key-v1"),
					Weight: 1.0,
				},
			},
		},
	}
	err := store.UpdateProvidersConfig(ctx, providers)
	require.NoError(t, err)

	// Update with same KeyID but different value
	providers["openai"] = ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "key-uuid-1", // Same KeyID
				Name:   "openai-primary",
				Value:  *schemas.NewSecretVar("sk-test-key-v2"), // Updated value
				Weight: 2.0,
			},
		},
	}
	err = store.UpdateProvidersConfig(ctx, providers)
	require.NoError(t, err)

	// Verify key was updated, not duplicated
	result, err := store.GetProvidersConfig(ctx)
	require.NoError(t, err)
	assert.Len(t, result["openai"].Keys, 1)
	assert.Equal(t, "sk-test-key-v2", result["openai"].Keys[0].Value.Val)
}

func TestUpdateProvidersConfig_UpdateExistingByName_FallbackFix(t *testing.T) {
	// This test verifies the fix for the unique constraint violation issue
	// when a new UUID is generated for a key that already exists by name
	store := setupRDBTestStore(t)
	ctx := context.Background()

	// Create initial provider with key
	providers := map[schemas.ModelProvider]ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{
					ID:     "original-uuid",
					Name:   "openai-primary",
					Value:  *schemas.NewSecretVar("sk-test-key-v1"),
					Weight: 1.0,
				},
			},
		},
	}
	err := store.UpdateProvidersConfig(ctx, providers)
	require.NoError(t, err)

	// Simulate config reload with NEW UUID (as happens when loading from config file)
	providers["openai"] = ProviderConfig{
		Keys: []schemas.Key{
			{
				ID:     "new-uuid-from-config-reload", // Different UUID!
				Name:   "openai-primary",              // Same name
				Value:  *schemas.NewSecretVar("sk-test-key-v2"),
				Weight: 1.5,
			},
		},
	}
	err = store.UpdateProvidersConfig(ctx, providers)
	require.NoError(t, err, "Should not fail with unique constraint violation")

	// Verify key was updated (not duplicated) and original KeyID preserved
	result, err := store.GetProvidersConfig(ctx)
	require.NoError(t, err)
	assert.Len(t, result["openai"].Keys, 1, "Should have exactly one key, not duplicated")
	assert.Equal(t, "sk-test-key-v2", result["openai"].Keys[0].Value.Val, "Value should be updated")
	assert.Equal(t, "original-uuid", result["openai"].Keys[0].ID, "Original KeyID should be preserved")
}

func TestUpdateProvidersConfig_MultipleKeys(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	providers := map[schemas.ModelProvider]ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: "key-1", Name: "openai-primary", Value: *schemas.NewSecretVar("sk-key-1"), Weight: 1.0},
				{ID: "key-2", Name: "openai-secondary", Value: *schemas.NewSecretVar("sk-key-2"), Weight: 0.5},
			},
		},
		"anthropic": {
			Keys: []schemas.Key{
				{ID: "key-3", Name: "anthropic-main", Value: *schemas.NewSecretVar("sk-key-3"), Weight: 1.0},
			},
		},
	}

	err := store.UpdateProvidersConfig(ctx, providers)
	require.NoError(t, err)

	result, err := store.GetProvidersConfig(ctx)
	require.NoError(t, err)
	assert.Len(t, result, 2)
	assert.Len(t, result["openai"].Keys, 2)
	assert.Len(t, result["anthropic"].Keys, 1)
}

func TestProviderKeyCRUD(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	err := store.UpdateProvidersConfig(ctx, map[schemas.ModelProvider]ProviderConfig{
		"openai": {},
	})
	require.NoError(t, err)

	keys, err := store.GetProviderKeys(ctx, "openai")
	require.NoError(t, err)
	assert.Empty(t, keys)

	key := schemas.Key{
		ID:     "key-uuid-1",
		Name:   "openai-primary",
		Value:  *schemas.NewSecretVar("sk-test-key-v1"),
		Weight: 1.0,
	}

	err = store.CreateProviderKey(ctx, "openai", key)
	require.NoError(t, err)

	keys, err = store.GetProviderKeys(ctx, "openai")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "openai-primary", keys[0].Name)

	storedKey, err := store.GetProviderKey(ctx, "openai", key.ID)
	require.NoError(t, err)
	require.NotNil(t, storedKey)
	assert.Equal(t, "sk-test-key-v1", storedKey.Value.Val)

	key.Value = *schemas.NewSecretVar("sk-test-key-v2")
	key.Weight = 2.0

	err = store.UpdateProviderKey(ctx, "openai", key.ID, key)
	require.NoError(t, err)

	storedKey, err = store.GetProviderKey(ctx, "openai", key.ID)
	require.NoError(t, err)
	require.NotNil(t, storedKey)
	assert.Equal(t, "sk-test-key-v2", storedKey.Value.Val)
	assert.Equal(t, 2.0, storedKey.Weight)

	err = store.DeleteProviderKey(ctx, "openai", key.ID)
	require.NoError(t, err)

	keys, err = store.GetProviderKeys(ctx, "openai")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestProviderKeyCRUD_ProviderMustExist(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	key := schemas.Key{
		ID:     "key-uuid-1",
		Name:   "openai-primary",
		Value:  *schemas.NewSecretVar("sk-test-key-v1"),
		Weight: 1.0,
	}

	err := store.CreateProviderKey(ctx, "openai", key)
	require.ErrorIs(t, err, ErrNotFound)

	_, err = store.GetProviderKeys(ctx, "openai")
	require.ErrorIs(t, err, ErrNotFound)

	_, err = store.GetProviderKey(ctx, "openai", key.ID)
	require.ErrorIs(t, err, ErrNotFound)

	err = store.UpdateProviderKey(ctx, "openai", key.ID, key)
	require.ErrorIs(t, err, ErrNotFound)

	err = store.DeleteProviderKey(ctx, "openai", key.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

// =============================================================================
// Budget Tests
// =============================================================================

func TestCreateBudget(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	budget := &tables.TableBudget{
		ID:            "budget-test",
		MaxLimit:      100.0,
		ResetDuration: "1M",
	}

	err := store.CreateBudget(ctx, budget)
	require.NoError(t, err)

	// Verify budget was created
	result, err := store.GetBudget(ctx, "budget-test")
	require.NoError(t, err)
	assert.Equal(t, "budget-test", result.ID)
	assert.Equal(t, 100.0, result.MaxLimit)
	assert.Equal(t, "1M", result.ResetDuration)
}

func TestCreateBudget_InvalidDuration(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	budget := &tables.TableBudget{
		ID:            "budget-invalid",
		MaxLimit:      100.0,
		ResetDuration: "invalid",
	}

	err := store.CreateBudget(ctx, budget)
	assert.Error(t, err, "Should fail with invalid duration")
}

func TestCreateBudget_NegativeLimit(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	budget := &tables.TableBudget{
		ID:            "budget-negative",
		MaxLimit:      -50.0,
		ResetDuration: "1h",
	}

	err := store.CreateBudget(ctx, budget)
	assert.Error(t, err, "Should fail with negative max limit")
}

func TestUpdateBudget(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	// Create budget
	budget := &tables.TableBudget{
		ID:            "budget-update",
		MaxLimit:      100.0,
		ResetDuration: "1h",
	}
	err := store.CreateBudget(ctx, budget)
	require.NoError(t, err)

	// Update budget
	budget.MaxLimit = 200.0
	err = store.UpdateBudget(ctx, budget)
	require.NoError(t, err)

	// Verify update
	result, err := store.GetBudget(ctx, "budget-update")
	require.NoError(t, err)
	assert.Equal(t, 200.0, result.MaxLimit)
}

func TestGetBudgets(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	// Create multiple budgets
	budgets := []*tables.TableBudget{
		{ID: "budget-1", MaxLimit: 100.0, ResetDuration: "1h"},
		{ID: "budget-2", MaxLimit: 200.0, ResetDuration: "1d"},
		{ID: "budget-3", MaxLimit: 300.0, ResetDuration: "1M"},
	}

	for _, b := range budgets {
		err := store.CreateBudget(ctx, b)
		require.NoError(t, err)
	}

	result, err := store.GetBudgets(ctx)
	require.NoError(t, err)
	assert.Len(t, result, 3)
}

// =============================================================================
// Rate Limit Tests
// =============================================================================

func TestCreateRateLimit(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	tokenMax := int64(100000)
	requestMax := int64(1000)
	tokenDuration := "1h"
	requestDuration := "1h"

	rateLimit := &tables.TableRateLimit{
		ID:                   "rate-limit-test",
		TokenMaxLimit:        &tokenMax,
		TokenResetDuration:   &tokenDuration,
		RequestMaxLimit:      &requestMax,
		RequestResetDuration: &requestDuration,
	}

	err := store.CreateRateLimit(ctx, rateLimit)
	require.NoError(t, err)

	result, err := store.GetRateLimit(ctx, "rate-limit-test")
	require.NoError(t, err)
	assert.Equal(t, "rate-limit-test", result.ID)
	assert.Equal(t, int64(100000), *result.TokenMaxLimit)
	assert.Equal(t, int64(1000), *result.RequestMaxLimit)
}

func TestCreateRateLimit_InvalidDuration(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	tokenMax := int64(100000)
	invalidDuration := "invalid"

	rateLimit := &tables.TableRateLimit{
		ID:                 "rate-limit-invalid",
		TokenMaxLimit:      &tokenMax,
		TokenResetDuration: &invalidDuration,
	}

	err := store.CreateRateLimit(ctx, rateLimit)
	assert.Error(t, err, "Should fail with invalid duration")
}

func TestCreateRateLimit_MissingDuration(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	tokenMax := int64(100000)

	rateLimit := &tables.TableRateLimit{
		ID:            "rate-limit-missing",
		TokenMaxLimit: &tokenMax,
		// Missing TokenResetDuration
	}

	err := store.CreateRateLimit(ctx, rateLimit)
	assert.Error(t, err, "Should fail when max limit set without duration")
}

func TestUpdateRateLimit(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	tokenMax := int64(100000)
	tokenDuration := "1h"

	rateLimit := &tables.TableRateLimit{
		ID:                 "rate-limit-update",
		TokenMaxLimit:      &tokenMax,
		TokenResetDuration: &tokenDuration,
	}
	err := store.CreateRateLimit(ctx, rateLimit)
	require.NoError(t, err)

	// Update
	newMax := int64(200000)
	rateLimit.TokenMaxLimit = &newMax
	err = store.UpdateRateLimit(ctx, rateLimit)
	require.NoError(t, err)

	result, err := store.GetRateLimit(ctx, "rate-limit-update")
	require.NoError(t, err)
	assert.Equal(t, int64(200000), *result.TokenMaxLimit)
}

// =============================================================================
// Virtual Key Tests
// =============================================================================

func TestCreateVirtualKey(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	vk := &tables.TableVirtualKey{
		ID:       "vk-test",
		Name:     "Test Virtual Key",
		Value:    "vk-test-value-123",
		IsActive: schemas.Ptr(true),
	}

	err := store.CreateVirtualKey(ctx, vk)
	require.NoError(t, err)

	result, err := store.GetVirtualKey(ctx, "vk-test")
	require.NoError(t, err)
	assert.Equal(t, "vk-test", result.ID)
	assert.Equal(t, "Test Virtual Key", result.Name)
	assert.Equal(t, "vk-test-value-123", result.Value)
	assert.True(t, result.IsActiveValue())
}

func TestCreateVirtualKey_WithBudgetAndRateLimit(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	// Create budget first
	budget := &tables.TableBudget{
		ID:            "budget-for-vk",
		MaxLimit:      100.0,
		ResetDuration: "1M",
	}
	err := store.CreateBudget(ctx, budget)
	require.NoError(t, err)

	// Create rate limit
	tokenMax := int64(100000)
	tokenDuration := "1h"
	rateLimit := &tables.TableRateLimit{
		ID:                 "rate-limit-for-vk",
		TokenMaxLimit:      &tokenMax,
		TokenResetDuration: &tokenDuration,
	}
	err = store.CreateRateLimit(ctx, rateLimit)
	require.NoError(t, err)

	// Create virtual key with references
	rateLimitID := "rate-limit-for-vk"
	vkID := "vk-with-refs"
	vk := &tables.TableVirtualKey{
		ID:          vkID,
		Name:        "VK With References",
		Value:       "vk-refs-value",
		IsActive:    schemas.Ptr(true),
		RateLimitID: &rateLimitID,
	}

	err = store.CreateVirtualKey(ctx, vk)
	require.NoError(t, err)

	// Link the existing budget to the VK via FK
	budget.VirtualKeyID = &vkID
	err = store.UpdateBudget(ctx, budget)
	require.NoError(t, err)

	result, err := store.GetVirtualKey(ctx, "vk-with-refs")
	require.NoError(t, err)
	assert.Len(t, result.Budgets, 1)
	assert.Equal(t, "budget-for-vk", result.Budgets[0].ID)
	assert.NotNil(t, result.RateLimitID)
	assert.Equal(t, "rate-limit-for-vk", *result.RateLimitID)
}

func TestCreateVirtualKey_DuplicateName(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	vk1 := &tables.TableVirtualKey{
		ID:       "vk-1",
		Name:     "Same Name",
		Value:    "vk-value-1",
		IsActive: schemas.Ptr(true),
	}
	err := store.CreateVirtualKey(ctx, vk1)
	require.NoError(t, err)

	vk2 := &tables.TableVirtualKey{
		ID:       "vk-2",
		Name:     "Same Name", // Duplicate name
		Value:    "vk-value-2",
		IsActive: schemas.Ptr(true),
	}
	err = store.CreateVirtualKey(ctx, vk2)
	assert.Error(t, err, "Should fail with duplicate name")
}

func TestGetVirtualKeyByValue(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	vk := &tables.TableVirtualKey{
		ID:       "vk-lookup",
		Name:     "Lookup Key",
		Value:    "vk-unique-value-xyz",
		IsActive: schemas.Ptr(true),
	}
	err := store.CreateVirtualKey(ctx, vk)
	require.NoError(t, err)

	result, err := store.GetVirtualKeyByValue(ctx, "vk-unique-value-xyz")
	require.NoError(t, err)
	assert.Equal(t, "vk-lookup", result.ID)
}

func TestUpdateVirtualKey(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	vk := &tables.TableVirtualKey{
		ID:       "vk-update",
		Name:     "Original Name",
		Value:    "vk-update-value",
		IsActive: schemas.Ptr(true),
	}
	err := store.CreateVirtualKey(ctx, vk)
	require.NoError(t, err)

	// Update
	vk.Name = "Updated Name"
	vk.IsActive = schemas.Ptr(false)
	err = store.UpdateVirtualKey(ctx, vk)
	require.NoError(t, err)

	result, err := store.GetVirtualKey(ctx, "vk-update")
	require.NoError(t, err)
	assert.Equal(t, "Updated Name", result.Name)
	assert.False(t, result.IsActiveValue())
}

func TestDeleteVirtualKey(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	vk := &tables.TableVirtualKey{
		ID:       "vk-delete",
		Name:     "Delete Me",
		Value:    "vk-delete-value",
		IsActive: schemas.Ptr(true),
	}
	err := store.CreateVirtualKey(ctx, vk)
	require.NoError(t, err)

	err = store.DeleteVirtualKey(ctx, "vk-delete")
	require.NoError(t, err)

	_, err = store.GetVirtualKey(ctx, "vk-delete")
	assert.Error(t, err, "Should not find deleted virtual key")
}

func TestDeleteVirtualKey_CleansUpScopedModelConfigs(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	vk := &tables.TableVirtualKey{
		ID:       "vk-scoped",
		Name:     "Scoped VK",
		Value:    "vk-scoped-value",
		IsActive: schemas.Ptr(true),
	}
	require.NoError(t, store.CreateVirtualKey(ctx, vk))

	budget := &tables.TableBudget{ID: "b-scoped", MaxLimit: 100, ResetDuration: "1h"}
	require.NoError(t, store.CreateBudget(ctx, budget))
	rateLimit := &tables.TableRateLimit{
		ID:                 "rl-scoped",
		TokenMaxLimit:      schemas.Ptr(int64(1000)),
		TokenResetDuration: schemas.Ptr("1h"),
	}
	require.NoError(t, store.CreateRateLimit(ctx, rateLimit))

	mc := &tables.TableModelConfig{
		ID:          "mc-scoped",
		ModelName:   "gpt-4",
		Scope:       tables.ModelConfigScopeVirtualKey,
		ScopeID:     schemas.Ptr(vk.ID),
		BudgetID:    &budget.ID,
		RateLimitID: &rateLimit.ID,
	}
	require.NoError(t, store.CreateModelConfig(ctx, mc))

	// Sanity: the scoped config exists before deletion.
	_, err := store.GetModelConfigByID(ctx, "mc-scoped")
	require.NoError(t, err)

	// Deleting the VK must cascade-clean its scoped model config and owned budget/rate-limit.
	require.NoError(t, store.DeleteVirtualKey(ctx, vk.ID))

	_, err = store.GetModelConfigByID(ctx, "mc-scoped")
	assert.Error(t, err, "scoped model config should be deleted with the VK")

	var budgetCount int64
	require.NoError(t, store.DB().Model(&tables.TableBudget{}).Where("id = ?", "b-scoped").Count(&budgetCount).Error)
	assert.Equal(t, int64(0), budgetCount, "owned budget should be deleted")

	var rlCount int64
	require.NoError(t, store.DB().Model(&tables.TableRateLimit{}).Where("id = ?", "rl-scoped").Count(&rlCount).Error)
	assert.Equal(t, int64(0), rlCount, "owned rate limit should be deleted")
}

// TestDeleteVirtualKey_CleansUpMultiBudgetScopedModelConfigs is a regression test for a
// leak where DeleteVirtualKey cleaned only the legacy single BudgetID column and ignored
// the modern multi-budget rows owned via TableBudget.ModelConfigID. Those budgets carry no
// virtual_key_id, so the VK's own budget sweep didn't catch them either — they orphaned.
func TestDeleteVirtualKey_CleansUpMultiBudgetScopedModelConfigs(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	vk := &tables.TableVirtualKey{
		ID:       "vk-multibudget",
		Name:     "MultiBudget VK",
		Value:    "vk-multibudget-value",
		IsActive: schemas.Ptr(true),
	}
	require.NoError(t, store.CreateVirtualKey(ctx, vk))

	rateLimit := &tables.TableRateLimit{
		ID:                 "rl-mb",
		TokenMaxLimit:      schemas.Ptr(int64(1000)),
		TokenResetDuration: schemas.Ptr("1h"),
	}
	require.NoError(t, store.CreateRateLimit(ctx, rateLimit))

	mc := &tables.TableModelConfig{
		ID:          "mc-multibudget",
		ModelName:   "gpt-4",
		Scope:       tables.ModelConfigScopeVirtualKey,
		ScopeID:     schemas.Ptr(vk.ID),
		RateLimitID: &rateLimit.ID,
	}
	require.NoError(t, store.CreateModelConfig(ctx, mc))

	// Modern budgets are owned via ModelConfigID (no virtual_key_id), mirroring how the
	// governance handler creates them.
	mbBudgetIDs := []string{"b-mb-1", "b-mb-2"}
	for _, id := range mbBudgetIDs {
		require.NoError(t, store.CreateBudget(ctx, &tables.TableBudget{
			ID:            id,
			MaxLimit:      100,
			ResetDuration: "1h",
			ModelConfigID: &mc.ID,
		}))
	}

	require.NoError(t, store.DeleteVirtualKey(ctx, vk.ID))

	_, err := store.GetModelConfigByID(ctx, "mc-multibudget")
	assert.Error(t, err, "scoped model config should be deleted with the VK")

	for _, id := range mbBudgetIDs {
		var count int64
		require.NoError(t, store.DB().Model(&tables.TableBudget{}).Where("id = ?", id).Count(&count).Error)
		assert.Equal(t, int64(0), count, "modern model-config budget %s should be deleted, not orphaned", id)
	}

	var rlCount int64
	require.NoError(t, store.DB().Model(&tables.TableRateLimit{}).Where("id = ?", "rl-mb").Count(&rlCount).Error)
	assert.Equal(t, int64(0), rlCount, "owned rate limit should be deleted")
}

// TestCreateModelConfig_RejectsMissingScopeOwner verifies the scope-owner lock in
// CreateModelConfig: a virtual_key-scoped config whose scope_id points at a non-existent
// VK is rejected (ErrNotFound) rather than created as an orphan. This is the guard that
// closes the CreateModelConfig↔DeleteVirtualKey race.
func TestCreateModelConfig_RejectsMissingScopeOwner(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	mc := &tables.TableModelConfig{
		ID:        "mc-orphan",
		ModelName: "gpt-4",
		Scope:     tables.ModelConfigScopeVirtualKey,
		ScopeID:   schemas.Ptr("vk-does-not-exist"),
	}
	err := store.CreateModelConfig(ctx, mc)
	assert.ErrorIs(t, err, ErrNotFound, "creating a VK-scoped config for a missing VK should be rejected")

	_, getErr := store.GetModelConfigByID(ctx, "mc-orphan")
	assert.Error(t, getErr, "rejected config must not have been persisted")
}

func TestDeleteProvider_CleansUpProviderModelConfigs(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	now := time.Now()
	require.NoError(t, store.DB().Create(&tables.TableProvider{Name: "openai", CreatedAt: now, UpdatedAt: now}).Error)
	require.NoError(t, store.DB().Create(&tables.TableBudget{ID: "pb", MaxLimit: 100, ResetDuration: "1M", LastReset: now, CreatedAt: now, UpdatedAt: now}).Error)
	require.NoError(t, store.DB().Create(&tables.TableRateLimit{ID: "prl", TokenMaxLimit: schemas.Ptr(int64(1000)), TokenResetDuration: schemas.Ptr("1h"), TokenLastReset: now, RequestLastReset: now, CreatedAt: now, UpdatedAt: now}).Error)

	providerName := "openai"
	mc := &tables.TableModelConfig{
		ID:          "mc-wildcard",
		ModelName:   tables.ModelConfigAllModels,
		Provider:    &providerName,
		Scope:       tables.ModelConfigScopeGlobal,
		BudgetID:    schemas.Ptr("pb"),
		RateLimitID: schemas.Ptr("prl"),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	require.NoError(t, store.CreateModelConfig(ctx, mc))

	require.NoError(t, store.DeleteProvider(ctx, schemas.ModelProvider("openai")))

	// The provider's wildcard model config and its owned budget/rate-limit are cleaned up.
	_, err := store.GetModelConfigByID(ctx, "mc-wildcard")
	assert.Error(t, err, "provider-scoped model config should be deleted with the provider")
	for _, q := range []struct {
		model any
		id    string
		label string
	}{
		{&tables.TableBudget{}, "pb", "budget"},
		{&tables.TableRateLimit{}, "prl", "rate limit"},
	} {
		var count int64
		require.NoError(t, store.DB().Model(q.model).Where("id = ?", q.id).Count(&count).Error)
		assert.Equal(t, int64(0), count, "owned "+q.label+" should be deleted")
	}
}

// =============================================================================
// Virtual Key Provider Config Tests
// =============================================================================

func TestCreateVirtualKeyProviderConfig(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	// Create virtual key first
	vk := &tables.TableVirtualKey{
		ID:       "vk-for-pc",
		Name:     "VK For Provider Config",
		Value:    "vk-pc-value",
		IsActive: schemas.Ptr(true),
	}
	err := store.CreateVirtualKey(ctx, vk)
	require.NoError(t, err)

	// Create provider config
	weight := 1.0
	pc := &tables.TableVirtualKeyProviderConfig{
		VirtualKeyID: "vk-for-pc",
		Provider:     "openai",
		Weight:       &weight,
	}

	err = store.CreateVirtualKeyProviderConfig(ctx, pc)
	require.NoError(t, err)

	// Verify
	configs, err := store.GetVirtualKeyProviderConfigs(ctx, "vk-for-pc")
	require.NoError(t, err)
	assert.Len(t, configs, 1)
	assert.Equal(t, "openai", configs[0].Provider)
}

func TestCreateVirtualKeyProviderConfig_WithKeys(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	// Create provider with keys first
	providers := map[schemas.ModelProvider]ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: "key-for-pc", Name: "openai-pc-key", Value: *schemas.NewSecretVar("sk-test"), Weight: 1.0},
			},
		},
	}
	err := store.UpdateProvidersConfig(ctx, providers)
	require.NoError(t, err)

	// Create virtual key
	vk := &tables.TableVirtualKey{
		ID:       "vk-with-keys",
		Name:     "VK With Keys",
		Value:    "vk-keys-value",
		IsActive: schemas.Ptr(true),
	}
	err = store.CreateVirtualKey(ctx, vk)
	require.NoError(t, err)

	// Create provider config with key reference
	weight := 1.0
	pc := &tables.TableVirtualKeyProviderConfig{
		VirtualKeyID: "vk-with-keys",
		Provider:     "openai",
		Weight:       &weight,
		Keys: []tables.TableKey{
			{Name: "openai-pc-key"}, // Reference by name
		},
	}

	err = store.CreateVirtualKeyProviderConfig(ctx, pc)
	require.NoError(t, err)

	// Verify keys are associated
	configs, err := store.GetVirtualKeyProviderConfigs(ctx, "vk-with-keys")
	require.NoError(t, err)
	assert.Len(t, configs, 1)

	// Load with keys
	var configWithKeys tables.TableVirtualKeyProviderConfig
	err = store.DB().Preload("Keys").First(&configWithKeys, "id = ?", configs[0].ID).Error
	require.NoError(t, err)
	assert.Len(t, configWithKeys.Keys, 1)
}

func TestCreateVirtualKeyProviderConfig_UnresolvedKeys(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	// Create virtual key
	vk := &tables.TableVirtualKey{
		ID:       "vk-unresolved",
		Name:     "VK Unresolved",
		Value:    "vk-unresolved-value",
		IsActive: schemas.Ptr(true),
	}
	err := store.CreateVirtualKey(ctx, vk)
	require.NoError(t, err)

	// Try to create provider config with non-existent key
	weight := 1.0
	pc := &tables.TableVirtualKeyProviderConfig{
		VirtualKeyID: "vk-unresolved",
		Provider:     "openai",
		Weight:       &weight,
		Keys: []tables.TableKey{
			{Name: "non-existent-key"},
		},
	}

	err = store.CreateVirtualKeyProviderConfig(ctx, pc)
	assert.Error(t, err, "Should fail with unresolved keys")

	var unresolvedErr *ErrUnresolvedKeys
	assert.ErrorAs(t, err, &unresolvedErr, "Should be ErrUnresolvedKeys")
}

func TestUpdateProvider_RemovesStaleVirtualKeyProviderConfigKeyAssociations(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	providers := map[schemas.ModelProvider]ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: "key-a", Name: "openai-key-a", Value: *schemas.NewSecretVar("sk-a"), Weight: 1.0},
				{ID: "key-b", Name: "openai-key-b", Value: *schemas.NewSecretVar("sk-b"), Weight: 1.0},
			},
		},
	}
	err := store.UpdateProvidersConfig(ctx, providers)
	require.NoError(t, err)

	vk := &tables.TableVirtualKey{
		ID:       "vk-update-provider-cleanup",
		Name:     "VK Update Provider Cleanup",
		Value:    "vk-update-provider-cleanup-value",
		IsActive: schemas.Ptr(true),
	}
	err = store.CreateVirtualKey(ctx, vk)
	require.NoError(t, err)

	weight := 1.0
	pc := &tables.TableVirtualKeyProviderConfig{
		VirtualKeyID: "vk-update-provider-cleanup",
		Provider:     "openai",
		Weight:       &weight,
		Keys: []tables.TableKey{
			{Name: "openai-key-b"},
		},
	}
	err = store.CreateVirtualKeyProviderConfig(ctx, pc)
	require.NoError(t, err)

	updatedProviderConfig := ProviderConfig{
		Keys: []schemas.Key{
			{ID: "key-a", Name: "openai-key-a", Value: *schemas.NewSecretVar("sk-a"), Weight: 1.0},
		},
	}
	err = store.UpdateProvider(ctx, "openai", updatedProviderConfig)
	require.NoError(t, err)

	result, err := store.GetVirtualKey(ctx, "vk-update-provider-cleanup")
	require.NoError(t, err)
	require.Len(t, result.ProviderConfigs, 1)
	assert.Equal(t, "openai", result.ProviderConfigs[0].Provider)
	assert.False(t, result.ProviderConfigs[0].AllowAllKeys)
	assert.Empty(t, result.ProviderConfigs[0].Keys)
}

func TestDeleteProvider_RemovesVirtualKeyProviderConfigs(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	providers := map[schemas.ModelProvider]ProviderConfig{
		"openai": {
			Keys: []schemas.Key{{ID: "key-delete", Name: "openai-key-delete", Value: *schemas.NewSecretVar("sk-delete"), Weight: 1.0}},
		},
	}
	err := store.UpdateProvidersConfig(ctx, providers)
	require.NoError(t, err)

	vk := &tables.TableVirtualKey{
		ID:       "vk-delete-provider-cleanup",
		Name:     "VK Delete Provider Cleanup",
		Value:    "vk-delete-provider-cleanup-value",
		IsActive: schemas.Ptr(true),
	}
	err = store.CreateVirtualKey(ctx, vk)
	require.NoError(t, err)

	weight := 1.0
	pc := &tables.TableVirtualKeyProviderConfig{
		VirtualKeyID: "vk-delete-provider-cleanup",
		Provider:     "openai",
		Weight:       &weight,
	}
	err = store.CreateVirtualKeyProviderConfig(ctx, pc)
	require.NoError(t, err)

	err = store.DeleteProvider(ctx, "openai")
	require.NoError(t, err)

	result, err := store.GetVirtualKey(ctx, "vk-delete-provider-cleanup")
	require.NoError(t, err)
	assert.Empty(t, result.ProviderConfigs)
}

// =============================================================================
// Client Config Tests
// =============================================================================

func TestUpdateClientConfig(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	config := &ClientConfig{
		EnableLogging:        new(true),
		InitialPoolSize:      100,
		LogRetentionDays:     30,
		MaxRequestBodySizeMB: 50,
	}

	err := store.UpdateClientConfig(ctx, config)
	require.NoError(t, err)

	result, err := store.GetClientConfig(ctx)
	require.NoError(t, err)
	assert.True(t, result.EnableLogging != nil && *result.EnableLogging)
	assert.Equal(t, 100, result.InitialPoolSize)
}

func TestUpdateClientMetadata(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	err := store.UpdateClientConfig(ctx, &ClientConfig{
		EnableLogging:        new(true),
		InitialPoolSize:      100,
		LogRetentionDays:     30,
		MaxRequestBodySizeMB: 50,
	})
	require.NoError(t, err)

	err = store.UpdateClientMetadata(ctx, map[string]any{
		"onboarding_dismissed": true,
		"theme":                "dark",
	})
	require.NoError(t, err)

	err = store.UpdateClientMetadata(ctx, map[string]any{
		"theme": "light",
		"stale": nil,
	})
	require.NoError(t, err)

	metadata, err := store.GetClientMetadata(ctx)
	require.NoError(t, err)
	assert.Equal(t, true, metadata["onboarding_dismissed"])
	assert.Equal(t, "light", metadata["theme"])
	assert.NotContains(t, metadata, "stale")

	err = store.UpdateClientMetadata(ctx, map[string]any{"theme": nil})
	require.NoError(t, err)

	metadata, err = store.GetClientMetadata(ctx)
	require.NoError(t, err)
	assert.NotContains(t, metadata, "theme")
	assert.Equal(t, true, metadata["onboarding_dismissed"])

	// Nested objects must be merged recursively (RFC 7386), not replaced
	// wholesale, so sibling keys survive a partial nested patch.
	err = store.UpdateClientMetadata(ctx, map[string]any{
		"onboarding": map[string]any{"dismissed": true, "step": "a"},
	})
	require.NoError(t, err)

	err = store.UpdateClientMetadata(ctx, map[string]any{
		"onboarding": map[string]any{"step": "b"},
	})
	require.NoError(t, err)

	metadata, err = store.GetClientMetadata(ctx)
	require.NoError(t, err)
	onboarding, ok := metadata["onboarding"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, onboarding["dismissed"], "sibling key must survive nested patch")
	assert.Equal(t, "b", onboarding["step"])

	// A nil nested value deletes just that nested key.
	err = store.UpdateClientMetadata(ctx, map[string]any{
		"onboarding": map[string]any{"dismissed": nil},
	})
	require.NoError(t, err)

	metadata, err = store.GetClientMetadata(ctx)
	require.NoError(t, err)
	onboarding, ok = metadata["onboarding"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, onboarding, "dismissed")
	assert.Equal(t, "b", onboarding["step"])
}

func TestUpdateClientMetadataRequiresClientConfig(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	err := store.UpdateClientMetadata(ctx, map[string]any{"onboarding_dismissed": true})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNotFound)

	var count int64
	err = store.DB().WithContext(ctx).Model(&tables.TableClientConfig{}).Count(&count).Error
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestUpdateClientConfigPreservesMetadata(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	err := store.UpdateClientConfig(ctx, &ClientConfig{
		EnableLogging:        new(true),
		InitialPoolSize:      100,
		LogRetentionDays:     30,
		MaxRequestBodySizeMB: 50,
	})
	require.NoError(t, err)

	err = store.UpdateClientMetadata(ctx, map[string]any{"onboarding_dismissed": true})
	require.NoError(t, err)

	err = store.UpdateClientConfig(ctx, &ClientConfig{
		EnableLogging:        new(true),
		InitialPoolSize:      200,
		LogRetentionDays:     60,
		MaxRequestBodySizeMB: 100,
	})
	require.NoError(t, err)

	config, err := store.GetClientConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, 200, config.InitialPoolSize)

	metadata, err := store.GetClientMetadata(ctx)
	require.NoError(t, err)
	assert.Equal(t, true, metadata["onboarding_dismissed"])
}

// =============================================================================
// Transaction Tests
// =============================================================================

func TestExecuteTransaction_Success(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	err := store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		// Create budget in transaction
		budget := &tables.TableBudget{
			ID:            "tx-budget",
			MaxLimit:      100.0,
			ResetDuration: "1h",
		}
		return tx.Create(budget).Error
	})
	require.NoError(t, err)

	// Verify budget was created
	result, err := store.GetBudget(ctx, "tx-budget")
	require.NoError(t, err)
	assert.Equal(t, "tx-budget", result.ID)
}

func TestExecuteTransaction_Rollback(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	err := store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		// Create budget
		budget := &tables.TableBudget{
			ID:            "tx-rollback-budget",
			MaxLimit:      100.0,
			ResetDuration: "1h",
		}
		if err := tx.Create(budget).Error; err != nil {
			return err
		}

		// Force error to trigger rollback
		return assert.AnError
	})
	assert.Error(t, err)

	// Verify budget was NOT created (rolled back)
	_, err = store.GetBudget(ctx, "tx-rollback-budget")
	assert.Error(t, err, "Budget should not exist after rollback")
}

// =============================================================================
// Customer and Team Tests
// =============================================================================

func TestCreateCustomer(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	customer := &tables.TableCustomer{
		ID:   "customer-test",
		Name: "Test Customer",
	}

	err := store.CreateCustomer(ctx, customer)
	require.NoError(t, err)

	result, err := store.GetCustomer(ctx, "customer-test")
	require.NoError(t, err)
	assert.Equal(t, "Test Customer", result.Name)
}

func TestCreateTeam(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	// Create customer first
	customer := &tables.TableCustomer{
		ID:   "customer-for-team",
		Name: "Customer For Team",
	}
	err := store.CreateCustomer(ctx, customer)
	require.NoError(t, err)

	// Create team
	customerID := "customer-for-team"
	team := &tables.TableTeam{
		ID:         "team-test",
		Name:       "Test Team",
		CustomerID: &customerID,
	}

	err = store.CreateTeam(ctx, team)
	require.NoError(t, err)

	result, err := store.GetTeam(ctx, "team-test")
	require.NoError(t, err)
	assert.Equal(t, "Test Team", result.Name)
	assert.Equal(t, "customer-for-team", *result.CustomerID)
}

// =============================================================================
// Ping and Health Tests
// =============================================================================

func TestPing(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	err := store.Ping(ctx)
	assert.NoError(t, err)
}

// =============================================================================
// Error Handling Tests
// =============================================================================

func TestGetBudget_NotFound(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	_, err := store.GetBudget(ctx, "non-existent-budget")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestGetVirtualKey_NotFound(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	_, err := store.GetVirtualKey(ctx, "non-existent-vk")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestGetRateLimit_NotFound(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	_, err := store.GetRateLimit(ctx, "non-existent-rate-limit")
	assert.ErrorIs(t, err, ErrNotFound)
}

// =============================================================================
// Plugin Tests
// =============================================================================

func TestCreateAndGetPlugin(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	plugin := &tables.TablePlugin{
		Name:    "test-plugin",
		Enabled: true,
		Version: 1,
	}

	err := store.CreatePlugin(ctx, plugin)
	require.NoError(t, err)

	result, err := store.GetPlugin(ctx, "test-plugin")
	require.NoError(t, err)
	assert.Equal(t, "test-plugin", result.Name)
	assert.True(t, result.Enabled)
}

func TestUpsertPlugin(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	// Create plugin
	plugin := &tables.TablePlugin{
		Name:    "upsert-plugin",
		Enabled: true,
		Version: 1,
	}
	err := store.UpsertPlugin(ctx, plugin)
	require.NoError(t, err)

	// Upsert with update
	plugin.Version = 2
	err = store.UpsertPlugin(ctx, plugin)
	require.NoError(t, err)

	result, err := store.GetPlugin(ctx, "upsert-plugin")
	require.NoError(t, err)
	assert.Equal(t, int16(2), result.Version)
}

// =============================================================================
// Integration Test: Full Virtual Key with Provider Config Flow
// =============================================================================

func TestFullVirtualKeyFlow(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	// Step 1: Create provider with keys
	providers := map[schemas.ModelProvider]ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: "key-1", Name: "openai-main", Value: *schemas.NewSecretVar("sk-main"), Weight: 1.0},
				{ID: "key-2", Name: "openai-backup", Value: *schemas.NewSecretVar("sk-backup"), Weight: 0.5},
			},
		},
	}
	err := store.UpdateProvidersConfig(ctx, providers)
	require.NoError(t, err)

	// Step 2: Create budget
	budget := &tables.TableBudget{
		ID:            "integration-budget",
		MaxLimit:      500.0,
		ResetDuration: "1M",
	}
	err = store.CreateBudget(ctx, budget)
	require.NoError(t, err)

	// Step 3: Create rate limit
	tokenMax := int64(1000000)
	tokenDuration := "1d"
	rateLimit := &tables.TableRateLimit{
		ID:                 "integration-rate-limit",
		TokenMaxLimit:      &tokenMax,
		TokenResetDuration: &tokenDuration,
	}
	err = store.CreateRateLimit(ctx, rateLimit)
	require.NoError(t, err)

	// Step 4: Create virtual key
	rateLimitID := "integration-rate-limit"
	integrationVKID := "integration-vk"
	vk := &tables.TableVirtualKey{
		ID:          integrationVKID,
		Name:        "Integration Virtual Key",
		Value:       "vk-integration-xyz",
		IsActive:    schemas.Ptr(true),
		RateLimitID: &rateLimitID,
	}
	err = store.CreateVirtualKey(ctx, vk)
	require.NoError(t, err)

	// Link the existing budget to the VK via FK
	budget.VirtualKeyID = &integrationVKID
	err = store.UpdateBudget(ctx, budget)
	require.NoError(t, err)

	// Step 5: Create provider config with key reference
	weight := 1.0
	pc := &tables.TableVirtualKeyProviderConfig{
		VirtualKeyID: "integration-vk",
		Provider:     "openai",
		Weight:       &weight,
		Keys: []tables.TableKey{
			{Name: "openai-main"},
		},
	}
	err = store.CreateVirtualKeyProviderConfig(ctx, pc)
	require.NoError(t, err)

	// Step 6: Verify complete setup
	result, err := store.GetVirtualKey(ctx, "integration-vk")
	require.NoError(t, err)
	assert.Equal(t, "Integration Virtual Key", result.Name)
	assert.Len(t, result.Budgets, 1)
	assert.NotNil(t, result.RateLimitID)

	configs, err := store.GetVirtualKeyProviderConfigs(ctx, "integration-vk")
	require.NoError(t, err)
	assert.Len(t, configs, 1)
	assert.Equal(t, "openai", configs[0].Provider)
}

// TestGetVirtualKeysUsesInternalPagination verifies that the unpaginated
// virtual-key API still returns every row when the result spans multiple
// internal preload pages.
func TestGetVirtualKeysUsesInternalPagination(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	totalVirtualKeys := virtualKeyInternalPageSize + 5
	createdAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < totalVirtualKeys; i++ {
		vk := &tables.TableVirtualKey{
			ID:        fmt.Sprintf("vk-page-%04d", i),
			Name:      fmt.Sprintf("Virtual Key %04d", i),
			Value:     fmt.Sprintf("vk-value-%04d", i),
			IsActive:  schemas.Ptr(true),
			CreatedAt: createdAt,
			UpdatedAt: createdAt,
		}
		require.NoError(t, store.CreateVirtualKey(ctx, vk))
	}

	virtualKeys, err := store.GetVirtualKeys(ctx)
	require.NoError(t, err)
	require.Len(t, virtualKeys, totalVirtualKeys)
	require.Equal(t, "vk-page-0000", virtualKeys[0].ID)
	require.Equal(t, fmt.Sprintf("vk-page-%04d", totalVirtualKeys-1), virtualKeys[len(virtualKeys)-1].ID)
}

// =============================================================================
// Helper function tests
// =============================================================================

func TestGetWeight(t *testing.T) {
	// Test nil weight returns default
	assert.Equal(t, 1.0, getWeight(nil))

	// Test explicit weight
	w := 2.5
	assert.Equal(t, 2.5, getWeight(&w))

	// Test zero weight
	zero := 0.0
	assert.Equal(t, 0.0, getWeight(&zero))
}

// =============================================================================
// Concurrent Access Tests
// =============================================================================

func TestMultipleBudgetUpdates(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	// Create initial budget
	budget := &tables.TableBudget{
		ID:            "multi-update-budget",
		MaxLimit:      100.0,
		ResetDuration: "1h",
		CurrentUsage:  0,
	}
	err := store.CreateBudget(ctx, budget)
	require.NoError(t, err)

	// Simulate multiple sequential updates
	for i := 0; i < 10; i++ {
		b := &tables.TableBudget{
			ID:            "multi-update-budget",
			MaxLimit:      100.0 + float64(i),
			ResetDuration: "1h",
		}
		err := store.UpdateBudget(ctx, b)
		require.NoError(t, err)
	}

	// Verify budget exists and has the last value
	result, err := store.GetBudget(ctx, "multi-update-budget")
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 109.0, result.MaxLimit) // 100 + 9
}

// =============================================================================
// Duration Validation Tests (for budgets and rate limits)
// =============================================================================

func TestBudgetDurationFormats(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	validDurations := []string{"30s", "5m", "1h", "1d", "1w", "1M", "1Y"}

	for i, duration := range validDurations {
		budget := &tables.TableBudget{
			ID:            "budget-duration-" + string(rune('a'+i)),
			MaxLimit:      100.0,
			ResetDuration: duration,
		}
		err := store.CreateBudget(ctx, budget)
		assert.NoError(t, err, "Duration %s should be valid", duration)
	}
}

func TestRateLimitDurationFormats(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	validDurations := []string{"30s", "5m", "1h", "1d", "1w", "1M", "1Y"}

	for i, duration := range validDurations {
		tokenMax := int64(1000)
		rateLimit := &tables.TableRateLimit{
			ID:                 "rate-limit-duration-" + string(rune('a'+i)),
			TokenMaxLimit:      &tokenMax,
			TokenResetDuration: &duration,
		}
		err := store.CreateRateLimit(ctx, rateLimit)
		assert.NoError(t, err, "Duration %s should be valid", duration)
	}
}

// =============================================================================
// Prompt Deletion Tests
// =============================================================================

// testPromptTree holds IDs of entities created by createTestPromptTree for verification
type testPromptTree struct {
	FolderID   string
	PromptIDs  []string
	VersionIDs []uint
	SessionIDs []uint
}

// createTestPromptTree creates a folder with 2 prompts, each having 2 versions (with messages) and 1 session (with messages).
func createTestPromptTree(t *testing.T, store *RDBConfigStore, ctx context.Context) testPromptTree {
	t.Helper()

	tree := testPromptTree{}

	// Create folder
	folder := &tables.TableFolder{ID: "folder-1", Name: "Test Folder"}
	require.NoError(t, store.CreateFolder(ctx, folder))
	tree.FolderID = folder.ID

	for i, promptID := range []string{"prompt-1", "prompt-2"} {
		_ = i
		prompt := &tables.TablePrompt{ID: promptID, Name: "Prompt " + promptID, FolderID: &tree.FolderID}
		require.NoError(t, store.CreatePrompt(ctx, prompt))
		tree.PromptIDs = append(tree.PromptIDs, promptID)

		// Create 2 versions with messages
		for v := 0; v < 2; v++ {
			version := &tables.TablePromptVersion{
				PromptID:      promptID,
				CommitMessage: "version commit",
				Messages: []tables.TablePromptVersionMessage{
					{PromptID: promptID, Message: json.RawMessage(`{"role":"user","content":"hello"}`)},
				},
			}
			require.NoError(t, store.CreatePromptVersion(ctx, version))
			tree.VersionIDs = append(tree.VersionIDs, version.ID)
		}

		// Create 1 session with messages
		session := &tables.TablePromptSession{
			PromptID: promptID,
			Name:     "Session " + promptID,
			Messages: []tables.TablePromptSessionMessage{
				{PromptID: promptID, Message: json.RawMessage(`{"role":"user","content":"hi"}`)},
			},
		}
		require.NoError(t, store.CreatePromptSession(ctx, session))
		tree.SessionIDs = append(tree.SessionIDs, session.ID)
	}

	return tree
}

// countRows returns the number of rows in a table
func countRows(t *testing.T, store *RDBConfigStore, model interface{}) int64 {
	t.Helper()
	var count int64
	require.NoError(t, store.DB().Model(model).Count(&count).Error)
	return count
}

func TestDeleteFolder(t *testing.T) {
	t.Run("NotFound", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		err := store.DeleteFolder(ctx, "nonexistent")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("Empty", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		folder := &tables.TableFolder{ID: "folder-empty", Name: "Empty"}
		require.NoError(t, store.CreateFolder(ctx, folder))

		require.NoError(t, store.DeleteFolder(ctx, "folder-empty"))

		_, err := store.GetFolderByID(ctx, "folder-empty")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("CascadesAll", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		tree := createTestPromptTree(t, store, ctx)

		// Verify entities exist before deletion
		assert.Greater(t, countRows(t, store, &tables.TablePrompt{}), int64(0))
		assert.Greater(t, countRows(t, store, &tables.TablePromptVersion{}), int64(0))
		assert.Greater(t, countRows(t, store, &tables.TablePromptVersionMessage{}), int64(0))
		assert.Greater(t, countRows(t, store, &tables.TablePromptSession{}), int64(0))
		assert.Greater(t, countRows(t, store, &tables.TablePromptSessionMessage{}), int64(0))

		require.NoError(t, store.DeleteFolder(ctx, tree.FolderID))

		// All child entities should be deleted
		assert.Equal(t, int64(0), countRows(t, store, &tables.TableFolder{}))
		assert.Equal(t, int64(0), countRows(t, store, &tables.TablePrompt{}))
		assert.Equal(t, int64(0), countRows(t, store, &tables.TablePromptVersion{}))
		assert.Equal(t, int64(0), countRows(t, store, &tables.TablePromptVersionMessage{}))
		assert.Equal(t, int64(0), countRows(t, store, &tables.TablePromptSession{}))
		assert.Equal(t, int64(0), countRows(t, store, &tables.TablePromptSessionMessage{}))
	})
}

func TestDeletePrompt(t *testing.T) {
	t.Run("NotFound", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		err := store.DeletePrompt(ctx, "nonexistent")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("CascadesAll", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		tree := createTestPromptTree(t, store, ctx)

		require.NoError(t, store.DeletePrompt(ctx, tree.PromptIDs[0]))

		// First prompt and its children should be gone
		_, err := store.GetPromptByID(ctx, tree.PromptIDs[0])
		assert.ErrorIs(t, err, ErrNotFound)

		// Second prompt should still exist
		p2, err := store.GetPromptByID(ctx, tree.PromptIDs[1])
		require.NoError(t, err)
		assert.Equal(t, tree.PromptIDs[1], p2.ID)
	})

	t.Run("LeavesFolder", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		tree := createTestPromptTree(t, store, ctx)

		require.NoError(t, store.DeletePrompt(ctx, tree.PromptIDs[0]))

		// Folder should still exist
		folder, err := store.GetFolderByID(ctx, tree.FolderID)
		require.NoError(t, err)
		assert.Equal(t, tree.FolderID, folder.ID)
	})

	t.Run("LeavesSiblings", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		tree := createTestPromptTree(t, store, ctx)

		require.NoError(t, store.DeletePrompt(ctx, tree.PromptIDs[0]))

		// Sibling prompt's versions and sessions should be unaffected
		versions, err := store.GetPromptVersions(ctx, tree.PromptIDs[1])
		require.NoError(t, err)
		assert.Len(t, versions, 2)

		sessions, err := store.GetPromptSessions(ctx, tree.PromptIDs[1])
		require.NoError(t, err)
		assert.Len(t, sessions, 1)
	})
}

func TestDeletePromptVersion(t *testing.T) {
	t.Run("NotFound", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		err := store.DeletePromptVersion(ctx, 99999)
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("NonLatest", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		tree := createTestPromptTree(t, store, ctx)

		// Version at index 0 is v1 (non-latest), index 1 is v2 (latest) for prompt-1
		nonLatestID := tree.VersionIDs[0]
		latestID := tree.VersionIDs[1]

		require.NoError(t, store.DeletePromptVersion(ctx, nonLatestID))

		// Non-latest version should be gone
		_, err := store.GetPromptVersionByID(ctx, nonLatestID)
		assert.ErrorIs(t, err, ErrNotFound)

		// Latest version should still be latest
		latest, err := store.GetPromptVersionByID(ctx, latestID)
		require.NoError(t, err)
		assert.True(t, latest.IsLatest)
	})

	t.Run("LatestPromotesPrevious", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		tree := createTestPromptTree(t, store, ctx)

		// Delete the latest version (index 1 = v2 for prompt-1)
		latestID := tree.VersionIDs[1]
		prevID := tree.VersionIDs[0]

		require.NoError(t, store.DeletePromptVersion(ctx, latestID))

		// Previous version should now be latest
		prev, err := store.GetPromptVersionByID(ctx, prevID)
		require.NoError(t, err)
		assert.True(t, prev.IsLatest)
	})

	t.Run("LeavesPrompt", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		tree := createTestPromptTree(t, store, ctx)

		require.NoError(t, store.DeletePromptVersion(ctx, tree.VersionIDs[0]))

		// Prompt should still exist
		prompt, err := store.GetPromptByID(ctx, tree.PromptIDs[0])
		require.NoError(t, err)
		assert.Equal(t, tree.PromptIDs[0], prompt.ID)
	})
}

func TestDeletePromptSession(t *testing.T) {
	t.Run("NotFound", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		err := store.DeletePromptSession(ctx, 99999)
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("CascadesMessages", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		tree := createTestPromptTree(t, store, ctx)

		sessionID := tree.SessionIDs[0]
		require.NoError(t, store.DeletePromptSession(ctx, sessionID))

		// Session should be gone
		_, err := store.GetPromptSessionByID(ctx, sessionID)
		assert.ErrorIs(t, err, ErrNotFound)

		// Session messages for that session should be gone
		var msgCount int64
		require.NoError(t, store.DB().Model(&tables.TablePromptSessionMessage{}).Where("session_id = ?", sessionID).Count(&msgCount).Error)
		assert.Equal(t, int64(0), msgCount)
	})

	t.Run("LeavesPrompt", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		tree := createTestPromptTree(t, store, ctx)

		require.NoError(t, store.DeletePromptSession(ctx, tree.SessionIDs[0]))

		// Prompt and versions should still exist
		prompt, err := store.GetPromptByID(ctx, tree.PromptIDs[0])
		require.NoError(t, err)
		assert.Equal(t, tree.PromptIDs[0], prompt.ID)

		versions, err := store.GetPromptVersions(ctx, tree.PromptIDs[0])
		require.NoError(t, err)
		assert.Len(t, versions, 2)
	})
}
