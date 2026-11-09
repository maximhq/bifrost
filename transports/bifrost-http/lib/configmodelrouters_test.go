package lib

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// bootFromJSON runs the governance and model router loaders against store in
// boot order, parsing raw so section presence comes from the file itself.
func bootFromJSON(t *testing.T, store configstore.ConfigStore, raw string) *Config {
	t.Helper()
	var configData ConfigData
	require.NoError(t, json.Unmarshal([]byte(raw), &configData))
	config := &Config{ConfigStore: store}
	loadGovernanceConfig(context.Background(), config, &configData)
	syncModelRoutersFromFile(context.Background(), config, &configData)
	return config
}

// routerJSON renders one model_routers entry with a phrase per tier.
func routerJSON(id, name, extra string, simple, medium, complex string) string {
	entry := fmt.Sprintf(`{"id":%q,"name":%q,"keywords":{"simple_keywords":[%q],"medium_keywords":[%q],"complex_keywords":[%q]}`, id, name, simple, medium, complex)
	if extra != "" {
		entry += "," + extra
	}
	return entry + "}"
}

// storedModelRouterRow reads a router's raw row, for timestamps the store type does not expose.
func storedModelRouterRow(t *testing.T, store configstore.ConfigStore, id string) tables.TableModelRouter {
	t.Helper()
	var row tables.TableModelRouter
	require.NoError(t, store.ExecuteTransaction(context.Background(), func(tx *gorm.DB) error {
		return tx.First(&row, "id = ?", id).Error
	}))
	return row
}

// requireModelRouterAbsent asserts no router is stored under id.
func requireModelRouterAbsent(t *testing.T, store configstore.ConfigStore, id string) {
	t.Helper()
	_, err := store.GetModelRouter(context.Background(), id)
	require.ErrorIs(t, err, configstore.ErrNotFound)
}

// TestModelRouterConfigLeavesComplexityConfigAlone pins that model routers and
// governance.complexity_analyzer_config are separate: the governance key keeps
// configuring the complexity config routing rules read complexity_tier from,
// creates no router, and is applied the same whether model_routers is present
// or not.
func TestModelRouterConfigLeavesComplexityConfigAlone(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	governance := `"governance":{"complexity_analyzer_config":{"keywords":{
		"simple_keywords":["governance-simple"],"medium_keywords":["governance-medium"],"complex_keywords":["governance-complex"]}}}`

	t.Run("governance key alone", func(t *testing.T) {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		bootFromJSON(t, store, `{`+governance+`}`)

		cfg, err := store.GetComplexityAnalyzerConfig(ctx)
		require.NoError(t, err)
		require.NotNil(t, cfg)
		assert.Contains(t, cfg.Keywords.SimpleKeywords, "governance-simple")
		routers, err := store.GetModelRouters(ctx)
		require.NoError(t, err)
		assert.Empty(t, routers, "the complexity config creates no router")
	})

	t.Run("both keys", func(t *testing.T) {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		bootFromJSON(t, store, `{`+governance+`,"model_routers":[`+
			routerJSON("support", "Support", "", "support-simple", "support-medium", "support-complex")+`]}`)

		cfg, err := store.GetComplexityAnalyzerConfig(ctx)
		require.NoError(t, err)
		require.NotNil(t, cfg)
		assert.Contains(t, cfg.Keywords.SimpleKeywords, "governance-simple")
		assert.NotContains(t, cfg.Keywords.SimpleKeywords, "support-simple")
		support, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, []string{"support-simple"}, support.Keywords.SimpleKeywords)
	})
}

// TestModelRouterConfigDeclaresRouters covers the shape: every entry becomes
// its own router holding exactly the phrases the file gives it.
func TestModelRouterConfigDeclaresRouters(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()

	bootFromJSON(t, store, `{"model_routers":[`+
		routerJSON("general", "General", "", "general-simple", "general-medium", "general-complex")+","+
		routerJSON("support", "Support", `"description":"Support desk traffic","semantic":{"provider":"openai","embedding_model":"text-embedding-3-small"}`,
			"reset my password", "summarize this ticket thread", "draft an escalation plan")+
		`]}`)

	general, err := store.GetModelRouter(ctx, "general")
	require.NoError(t, err)
	assert.Equal(t, "General", general.Name)
	assert.Equal(t, []string{"general-simple"}, general.Keywords.SimpleKeywords, "a new router is not seeded with the built-in phrases")

	support, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, "Support", support.Name)
	assert.Equal(t, "Support desk traffic", support.Description)
	assert.Equal(t, []string{"reset my password"}, support.Keywords.SimpleKeywords)
	require.NotNil(t, support.Semantic)
	assert.Equal(t, "text-embedding-3-small", support.Semantic.EmbeddingModel)
	assert.False(t, support.ConfigHashes.Empty())
}

// TestModelRouterConfigSplitModeKeepsDashboardEdits walks one router through
// reboots: an unchanged file writes nothing, a changed section is merged in
// without undoing dashboard edits to other sections or to the name, and a
// changed name is applied.
func TestModelRouterConfigSplitModeKeepsDashboardEdits(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()
	semantic := `"semantic":{"provider":"openai","embedding_model":"text-embedding-3-small"}`
	boot := func(name, medium string) {
		bootFromJSON(t, store, `{"model_routers":[`+
			routerJSON("support", name, semantic, "support-simple", medium, "support-complex")+`]}`)
	}

	boot("Support", "support-medium")
	router, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	router.Name = "Support desk"
	router.Semantic.EmbeddingModel = "text-embedding-3-large"
	router.ConfigHashes = configstore.ComplexityAnalyzerConfigHashes{}
	router.ConfigHash = ""
	require.NoError(t, store.UpdateModelRouter(ctx, router))
	edited := storedModelRouterRow(t, store, "support")

	boot("Support", "support-medium")
	assert.Equal(t, edited.UpdatedAt, storedModelRouterRow(t, store, "support").UpdatedAt, "an unchanged file writes nothing")

	boot("Support", "support-medium-2")
	router, err = store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"support-medium", "support-medium-2"}, router.Keywords.MediumKeywords, "a changed phrase list merges in")
	assert.Equal(t, "text-embedding-3-large", router.Semantic.EmbeddingModel, "an unchanged section keeps its dashboard edit")
	assert.Equal(t, "Support desk", router.Name, "an unchanged name keeps its dashboard edit")

	boot("Customer support", "support-medium-2")
	router, err = store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, "Customer support", router.Name)
	assert.Equal(t, "text-embedding-3-large", router.Semantic.EmbeddingModel)
}

// TestModelRouterConfigSourceOfTruth pins config.json as the source of truth:
// dashboard drift is reverted, undeclared routers are deleted, and an invalid
// entry does not delete the router it was meant to configure.
func TestModelRouterConfigSourceOfTruth(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	keywords := func(prefix string) *configstore.ModelRouter {
		return &configstore.ModelRouter{ComplexityAnalyzerConfig: configstore.ComplexityAnalyzerConfig{
			Keywords: configstore.ComplexityEditableKeywordConfig{
				SimpleKeywords:  []string{prefix + "-simple"},
				MediumKeywords:  []string{prefix + "-medium"},
				ComplexKeywords: []string{prefix + "-complex"},
			},
		}}
	}
	seed := func(t *testing.T, store configstore.ConfigStore, id, name string) {
		router := keywords(id)
		router.ID, router.Name = id, name
		require.NoError(t, store.CreateModelRouter(ctx, router))
	}

	t.Run("reverts drift and prunes", func(t *testing.T) {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		seed(t, store, "support", "Support desk")
		seed(t, store, "retired", "Retired")
		seed(t, store, "typo", "Typo")

		bootFromJSON(t, store, `{"source_of_truth":"config.json","model_routers":[`+
			routerJSON("support", "Support", "", "file-simple", "file-medium", "file-complex")+","+
			`{"id":"typo","name":"Typo","keywords":{"simple_keywords":["only-one-tier"]}}`+
			`]}`)

		support, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, "Support", support.Name)
		assert.Equal(t, []string{"file-simple"}, support.Keywords.SimpleKeywords)
		requireModelRouterAbsent(t, store, "retired")
		_, err = store.GetModelRouter(ctx, "typo")
		require.NoError(t, err, "an invalid entry must not delete the router it names")
	})

	t.Run("a declared router can take the name of a pruned one", func(t *testing.T) {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		seed(t, store, "retired", "Support")

		bootFromJSON(t, store, `{"source_of_truth":"config.json","model_routers":[`+
			routerJSON("support", "Support", "", "file-simple", "file-medium", "file-complex")+`]}`)

		requireModelRouterAbsent(t, store, "retired")
		support, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, "Support", support.Name)
	})

	t.Run("an empty list prunes every router", func(t *testing.T) {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		seed(t, store, "support", "Support")

		bootFromJSON(t, store, `{"source_of_truth":"config.json","model_routers":[]}`)

		requireModelRouterAbsent(t, store, "support")
	})

	t.Run("a null model_routers section prunes nothing", func(t *testing.T) {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		seed(t, store, "support", "Support")

		bootFromJSON(t, store, `{"source_of_truth":"config.json","model_routers":null}`)

		_, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err, "null is an omitted section; only an explicit [] prunes")
	})

	t.Run("no model_routers section prunes nothing", func(t *testing.T) {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		seed(t, store, "support", "Support")

		bootFromJSON(t, store, `{"source_of_truth":"config.json","governance":{"budgets":[]}}`)

		_, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
	})
}

// TestModelRouterConfigSkipsBadEntries pins that one bad entry is skipped
// without holding back the rest: a repeated id keeps its first valid
// declaration, and an entry that fails validation is dropped on its own,
// before its id is counted, so it cannot shadow a valid one.
func TestModelRouterConfigSkipsBadEntries(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()

	bootFromJSON(t, store, `{"model_routers":[`+
		routerJSON("support", " ", `"description":"invalid, no name"`, "x-simple", "x-medium", "x-complex")+","+
		routerJSON("support", "Support", `"description":"first"`, "a-simple", "a-medium", "a-complex")+","+
		routerJSON("support", "Support again", `"description":"second"`, "b-simple", "b-medium", "b-complex")+","+
		routerJSON("bad id!", "Bad", "", "c-simple", "c-medium", "c-complex")+","+
		routerJSON("general", "General", "", "d-simple", "d-medium", "d-complex")+
		`]}`)

	support, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, "first", support.Description)
	requireModelRouterAbsent(t, store, "bad id!")
	_, err = store.GetModelRouter(ctx, "general")
	require.NoError(t, err, "the entries after a bad one still apply")
}

// TestModelRouterConfigReplacesUnreadableRouter pins that a router whose stored
// row this version cannot read is replaced by its config.json declaration.
func TestModelRouterConfigReplacesUnreadableRouter(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()
	declare := `{"model_routers":[` + routerJSON("support", "Support", "", "s", "m", "c") + `]}`

	bootFromJSON(t, store, declare)
	require.NoError(t, store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		return tx.Model(&tables.TableModelRouter{}).Where("id = ?", "support").Update("simple_phrases_json", `["broken`).Error
	}))
	_, err := store.GetModelRouter(ctx, "support")
	require.ErrorIs(t, err, configstore.ErrConfigUnreadable)

	bootFromJSON(t, store, declare)
	router, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, []string{"s"}, router.Keywords.SimpleKeywords)
}

// TestModelRouterConfigWithoutStore pins that a gateway with no config store
// skips its model_routers section instead of failing the boot.
func TestModelRouterConfigWithoutStore(t *testing.T) {
	initTestLogger()
	var configData ConfigData
	require.NoError(t, json.Unmarshal([]byte(`{"model_routers":[`+
		routerJSON("support", "Support", "", "simple-one", "medium-one", "complex-one")+`]}`), &configData))
	require.NotPanics(t, func() { syncModelRoutersFromFile(context.Background(), &Config{}, &configData) })
}

// TestModelRouterConfigSchema pins where model routers live in config.json: a
// top-level list, accepted beside the governance complexity config, whose ids
// are ones the store accepts. The schema rejects the list anywhere else. A
// router carries its own suggestions block, under the same rules as the
// complexity config's.
func TestModelRouterConfigSchema(t *testing.T) {
	schema := loadLocalSchema(t)
	semantic := `"semantic":{"provider":"openai","embedding_model":"text-embedding-3-small"}`

	valid := `{"governance":{"complexity_analyzer_config":{"keywords":{"simple_keywords":["a"],"medium_keywords":["b"],"complex_keywords":["c"]}}},` +
		`"model_routers":[` +
		routerJSON("default", "General", "", "s", "m", "c") + "," +
		routerJSON("support-desk_2", "Support", `"description":"Support desk traffic",`+semantic+`,"session":{"enabled":true}`, "s", "m", "c") + "," +
		routerJSON("suggested", "Suggested", semantic+`,"suggestions":{"enabled":true,"provider":"openai","model":"gpt-4.1-mini","lookback_days":7}`, "s", "m", "c") +
		`]}`
	require.NoError(t, ValidateConfigSchema([]byte(valid), schema))

	invalid := map[string]string{
		"list under governance":                `{"governance":{"model_routers":[` + routerJSON("support", "Support", "", "s", "m", "c") + `]}}`,
		"id with a space":                      `{"model_routers":[` + routerJSON("support desk", "Support", "", "s", "m", "c") + `]}`,
		"missing name":                         `{"model_routers":[{"id":"support","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]}}]}`,
		"whitespace-only name":                 `{"model_routers":[` + routerJSON("support", "  ", "", "s", "m", "c") + `]}`,
		"unknown field":                        `{"model_routers":[` + routerJSON("support", "Support", `"phrases":["s"]`, "s", "m", "c") + `]}`,
		"session without semantic":             `{"model_routers":[` + routerJSON("support", "Support", `"session":{"enabled":true}`, "s", "m", "c") + `]}`,
		"enabled suggestions without semantic": `{"model_routers":[` + routerJSON("support", "Support", `"suggestions":{"enabled":true,"provider":"openai","model":"gpt-4.1-mini"}`, "s", "m", "c") + `]}`,
	}
	for name, config := range invalid {
		t.Run(name, func(t *testing.T) {
			require.Error(t, ValidateConfigSchema([]byte(config), schema))
		})
	}
}

// TestModelRouterConfigLoadConfigEndToEnd boots a real config.json through
// LoadConfig, schema validation included, and checks that the routers it
// declares reach the store.
func TestModelRouterConfigLoadConfigEndToEnd(t *testing.T) {
	initTestLogger()
	tempDir := createTempDir(t)
	keywords := func(prefix string) configstore.ComplexityAnalyzerConfig {
		return configstore.ComplexityAnalyzerConfig{
			TierBoundaries: configstore.DefaultComplexityTierBoundaries(),
			Keywords: configstore.ComplexityEditableKeywordConfig{
				SimpleKeywords:  []string{prefix + "-simple"},
				MediumKeywords:  []string{prefix + "-medium"},
				ComplexKeywords: []string{prefix + "-complex"},
			},
		}
	}
	configData := makeConfigDataWithProvidersAndDir(map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}, tempDir)
	configData.ModelRouters = []configstore.ModelRouter{
		{ID: "general", Name: "General", ComplexityAnalyzerConfig: keywords("general")},
		{ID: "support", Name: "Support", Description: "Support desk traffic", ComplexityAnalyzerConfig: keywords("support")},
	}
	createConfigFile(t, tempDir, configData)

	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	require.NoError(t, err)
	defer config.Close(ctx)

	general, err := config.ConfigStore.GetModelRouter(ctx, "general")
	require.NoError(t, err)
	assert.Equal(t, "General", general.Name)
	support, err := config.ConfigStore.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, "Support desk traffic", support.Description)
	assert.Equal(t, []string{"support-simple"}, support.Keywords.SimpleKeywords)
}
