package lib

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
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
		routerJSON("support-desk_2", "Support", `"description":"Support desk traffic",`+semantic+`,"session":{"enabled":true},`+
			`"targets":[{"tier":"COMPLEX","provider":"anthropic","model":"claude-opus","weight":0.5,"fallbacks":["openai/gpt-4o"]},{"tier":"COMPLEX","provider":"anthropic","model":"claude-sonnet","weight":0.5}],`+
			`"routing":{"enabled":true,"scope":"team","scope_id":"team-1","cel_expression":"model == \"support-assistant\"","query":{"combinator":"and","rules":[]},"priority":2}`, "s", "m", "c") + "," +
		routerJSON("llm-router", "LLM", `"classifier":"llm","llm":{"provider":"openai","model":"gpt-4o-mini"},"session":{"enabled":true}`, "s", "m", "c") + "," +
		routerJSON("suggested", "Suggested", semantic+`,"suggestions":{"enabled":true,"provider":"openai","model":"gpt-4.1-mini","lookback_days":7}`, "s", "m", "c") +
		`]}`
	require.NoError(t, ValidateConfigSchema([]byte(valid), schema))

	invalid := map[string]string{
		"list under governance":                   `{"governance":{"model_routers":[` + routerJSON("support", "Support", "", "s", "m", "c") + `]}}`,
		"id with a space":                         `{"model_routers":[` + routerJSON("support desk", "Support", "", "s", "m", "c") + `]}`,
		"missing name":                            `{"model_routers":[{"id":"support","keywords":{"simple_keywords":["s"],"medium_keywords":["m"],"complex_keywords":["c"]}}]}`,
		"whitespace-only name":                    `{"model_routers":[` + routerJSON("support", "  ", "", "s", "m", "c") + `]}`,
		"reserved id":                             `{"model_routers":[` + routerJSON("defaults", "Defaults", "", "s", "m", "c") + `]}`,
		"enabled suggestions with an llm primary": `{"model_routers":[` + routerJSON("support", "Support", semantic+`,"classifier":"llm","llm":{"provider":"openai","model":"gpt-4.1-mini"},"suggestions":{"enabled":true,"provider":"openai","model":"gpt-4.1-mini"}`, "s", "m", "c") + `]}`,
		"team routing with a null scope_id":       `{"model_routers":[` + routerJSON("support", "Support", `"routing":{"scope":"team","scope_id":null}`, "s", "m", "c") + `]}`,
		"team routing with an empty scope_id":     `{"model_routers":[` + routerJSON("support", "Support", `"routing":{"scope":"team","scope_id":""}`, "s", "m", "c") + `]}`,
		"unknown field":                           `{"model_routers":[` + routerJSON("support", "Support", `"phrases":["s"]`, "s", "m", "c") + `]}`,
		"session without semantic":                `{"model_routers":[` + routerJSON("support", "Support", `"session":{"enabled":true}`, "s", "m", "c") + `]}`,
		"enabled suggestions without semantic":    `{"model_routers":[` + routerJSON("support", "Support", `"suggestions":{"enabled":true,"provider":"openai","model":"gpt-4.1-mini"}`, "s", "m", "c") + `]}`,
		"llm without an llm block":                `{"model_routers":[` + routerJSON("support", "Support", `"classifier":"llm"`, "s", "m", "c") + `]}`,
		"unknown target tier":                     `{"model_routers":[` + routerJSON("support", "Support", `"targets":[{"tier":"HARD","model":"m","weight":1}]`, "s", "m", "c") + `]}`,
		"target key w/o provider":                 `{"model_routers":[` + routerJSON("support", "Support", `"targets":[{"tier":"SIMPLE","model":"m","key_id":"k","weight":1}]`, "s", "m", "c") + `]}`,
		"target with only a blank provider":       `{"model_routers":[` + routerJSON("support", "Support", `"targets":[{"tier":"SIMPLE","provider":"  ","weight":1}]`, "s", "m", "c") + `]}`,
		"target with only a blank model":          `{"model_routers":[` + routerJSON("support", "Support", `"targets":[{"tier":"SIMPLE","model":" ","weight":1}]`, "s", "m", "c") + `]}`,
		"routing user scope":                      `{"model_routers":[` + routerJSON("support", "Support", `"routing":{"scope":"user","scope_id":"u"}`, "s", "m", "c") + `]}`,
		"routing unknown field":                   `{"model_routers":[` + routerJSON("support", "Support", `"routing":{"scope":"global","chain_rule":true}`, "s", "m", "c") + `]}`,
		"routing team without id":                 `{"model_routers":[` + routerJSON("support", "Support", `"routing":{"scope":"team"}`, "s", "m", "c") + `]}`,
	}
	for name, config := range invalid {
		t.Run(name, func(t *testing.T) {
			require.Error(t, ValidateConfigSchema([]byte(config), schema))
		})
	}
}

// TestModelRouterConfigTargets walks a router's targets through reboots: the
// file sets them, an entry without targets leaves dashboard targets alone, a
// dashboard edit survives an unchanged file, a changed entry re-applies them,
// source-of-truth mode reverts drift, and invalid targets skip the router.
func TestModelRouterConfigTargets(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()
	mini := `"targets":[{"tier":"SIMPLE","provider":"openai","model":"gpt-4o-mini","weight":1}]`
	boot := func(sourceOfTruth bool, name, extra string) {
		prefix := `{`
		if sourceOfTruth {
			prefix = `{"source_of_truth":"config.json",`
		}
		bootFromJSON(t, store, prefix+`"model_routers":[`+routerJSON("support", name, extra, "s", "m", "c")+`]}`)
	}
	models := func() []string {
		router, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		out := make([]string, 0, len(router.Targets))
		for _, target := range router.Targets {
			out = append(out, *target.Model)
		}
		return out
	}
	dashboardSets := func(model string) {
		require.NoError(t, store.SetModelRouterTargets(ctx, "support", []tables.TableModelRouterTarget{
			{Tier: "SIMPLE", Model: &model, Weight: 1},
		}))
	}

	boot(false, "Support", mini)
	assert.Equal(t, []string{"gpt-4o-mini"}, models())

	dashboardSets("claude-haiku")
	boot(false, "Support", mini)
	assert.Equal(t, []string{"claude-haiku"}, models(), "a dashboard edit survives an unchanged file")

	boot(false, "Support desk", mini)
	assert.Equal(t, []string{"gpt-4o-mini"}, models(), "a changed entry re-applies its targets")

	dashboardSets("claude-haiku")
	boot(true, "Support desk", mini)
	assert.Equal(t, []string{"gpt-4o-mini"}, models(), "source of truth reverts drift")

	dashboardSets("claude-haiku")
	boot(true, "Support desk", "")
	assert.Equal(t, []string{"claude-haiku"}, models(), "an entry without targets leaves them alone")

	boot(false, "Support desk", `"targets":[{"tier":"SIMPLE","model":"m","weight":0.5}]`)
	assert.Equal(t, []string{"claude-haiku"}, models(), "invalid targets are not applied")
}

// TestModelRouterConfigRouting walks a router's routing through reboots on
// the same terms as its targets, and pins what keeps routing from applying: a
// condition that uses complexity_tier skips the whole entry, and a scope
// whose entity does not exist, or a user scope, leaves the stored routing.
func TestModelRouterConfigRouting(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()
	on := `"routing":{"enabled":true,"scope":"global","cel_expression":"model == \"support-assistant\"","priority":3}`
	boot := func(sourceOfTruth bool, name, extra string) {
		prefix := `{`
		if sourceOfTruth {
			prefix = `{"source_of_truth":"config.json",`
		}
		bootFromJSON(t, store, prefix+`"model_routers":[`+routerJSON("support", name, extra, "s", "m", "c")+`]}`)
	}
	routing := func() configstore.ModelRouterRouting {
		router, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		require.NotNil(t, router.Routing)
		return *router.Routing
	}
	dashboardTurnsOff := func() {
		current := routing()
		current.Enabled = false
		require.NoError(t, store.SetModelRouterRouting(ctx, "support", current))
	}

	boot(false, "Support", on)
	assert.Equal(t, configstore.ModelRouterRouting{Enabled: true, Scope: "global", CelExpression: `model == "support-assistant"`, Priority: 3}, routing())

	dashboardTurnsOff()
	boot(false, "Support", on)
	assert.False(t, routing().Enabled, "a dashboard edit survives an unchanged file")

	boot(false, "Support desk", on)
	assert.True(t, routing().Enabled, "a changed entry re-applies its routing")

	dashboardTurnsOff()
	boot(true, "Support desk", on)
	assert.True(t, routing().Enabled, "source of truth reverts drift")

	dashboardTurnsOff()
	boot(true, "Support desk", "")
	assert.False(t, routing().Enabled, "an entry without routing leaves it alone")

	boot(true, "Support desk", `"routing":{"enabled":true,"scope":"team","scope_id":"no-such-team","priority":3}`)
	assert.Equal(t, "global", routing().Scope, "a scope whose entity does not exist is not applied")

	boot(true, "Customer support", `"routing":{"enabled":true,"scope":"global","cel_expression":"complexity_tier == \"SIMPLE\""}`)
	router, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, "Support desk", router.Name, "a condition that uses complexity_tier skips the entry")
}

// TestModelRouterConfigRetriesUnresolvedScope pins that a router whose
// routing names a scope entity that does not exist yet is skipped whole, as a
// routing rule is: a new router is not created and a stored one keeps its
// last persisted version, so its file hash is not recorded and a later boot
// applies the entry once the entity exists, in split mode too.
func TestModelRouterConfigRetriesUnresolvedScope(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()
	teamScoped := func(priority int) string {
		return fmt.Sprintf(`"routing":{"enabled":true,"scope":"team","scope_id":"team-eng","priority":%d}`, priority)
	}
	boot := func(id, name, extra string) {
		bootFromJSON(t, store, `{"model_routers":[`+routerJSON(id, name, extra, "s", "m", "c")+`]}`)
	}

	boot("support", "Support", teamScoped(3))
	requireModelRouterAbsent(t, store, "support")

	boot("billing", "Billing", `"routing":{"enabled":true,"scope":"global","priority":1}`)
	boot("billing", "Billing desk", teamScoped(4))
	billing, err := store.GetModelRouter(ctx, "billing")
	require.NoError(t, err)
	assert.Equal(t, "Billing", billing.Name, "a stored router keeps its last persisted version")
	assert.Equal(t, "global", billing.Routing.Scope)

	require.NoError(t, store.CreateTeam(ctx, &tables.TableTeam{ID: "team-eng", Name: "Engineering"}))

	boot("support", "Support", teamScoped(3))
	support, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	require.NotNil(t, support.Routing.ScopeID)
	assert.Equal(t, "team", support.Routing.Scope, "the skipped router is created once its team exists")
	assert.Equal(t, "team-eng", *support.Routing.ScopeID)

	boot("billing", "Billing desk", teamScoped(4))
	billing, err = store.GetModelRouter(ctx, "billing")
	require.NoError(t, err)
	assert.Equal(t, "Billing desk", billing.Name, "the held-back entry applies once its team exists")
	assert.Equal(t, "team", billing.Routing.Scope)
}

// TestModelRouterConfigFallbackKeyNames pins that a target fallback's
// provider_key_name is resolved to its key_id at load, as a routing rule's
// is, and only the key_id is stored; that a changed key pin is a changed
// entry; and that a name that does not resolve skips the router entry.
func TestModelRouterConfigFallbackKeyNames(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()
	require.NoError(t, store.AddProvider(ctx, schemas.Anthropic, configstore.ProviderConfig{Keys: []schemas.Key{
		{ID: "k-anthropic-prod", Name: "Anthropic Prod", Value: *schemas.NewSecretVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 1},
		{ID: "k-anthropic-backup", Name: "Anthropic Backup", Value: *schemas.NewSecretVar("sk-b"), Models: schemas.WhiteList{"*"}, Weight: 1},
	}}))
	withFallback := func(fallback string) string {
		return `"targets":[{"tier":"SIMPLE","provider":"openai","model":"gpt-4o-mini","weight":1,"fallbacks":[` + fallback + `]}]`
	}
	boot := func(id, name, extra string) {
		bootFromJSON(t, store, `{"model_routers":[`+routerJSON(id, name, extra, "s", "m", "c")+`]}`)
	}
	fallback := func(id string) tables.RoutingFallback {
		router, err := store.GetModelRouter(ctx, id)
		require.NoError(t, err)
		require.Len(t, router.Targets, 1)
		require.Len(t, router.Targets[0].ParsedFallbacks, 1)
		return router.Targets[0].ParsedFallbacks[0]
	}

	boot("support", "Support", withFallback(`{"provider":"anthropic","model":"claude-sonnet-4","provider_key_name":"Anthropic Prod"}`))
	got := fallback("support")
	assert.Equal(t, schemas.Fallback{Provider: schemas.Anthropic, Model: "claude-sonnet-4", KeyID: "k-anthropic-prod"}, got.Resolved())
	assert.Nil(t, got.ProviderKeyName, "the config-only alias is not stored")

	boot("support", "Support", withFallback(`{"provider":"anthropic","model":"claude-sonnet-4","provider_key_name":"Anthropic Backup"}`))
	assert.Equal(t, "k-anthropic-backup", fallback("support").KeyID, "a changed key name is a changed entry")

	boot("support", "Support", withFallback(`{"provider":"anthropic","model":"claude-sonnet-4","key_id":"k-anthropic-prod"}`))
	assert.Equal(t, "k-anthropic-prod", fallback("support").KeyID, "a changed key id is a changed entry")

	boot("support", "Support desk", withFallback(`{"provider":"anthropic","model":"claude-sonnet-4","provider_key_name":"Missing"}`))
	router, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, "Support", router.Name, "a key name that does not resolve skips the entry")

	boot("billing", "Billing", withFallback(`{"provider":"anthropic","provider_key_name":"Missing"}`))
	requireModelRouterAbsent(t, store, "billing")
}

// TestModelRouterConfigFallbackKeyRotation pins that a fallback pinned by
// provider_key_name follows the key when it is rotated (the name now names a
// key with another id) with config.json unchanged, as a routing rule's does:
// the entry is hashed with the resolved key_id.
func TestModelRouterConfigFallbackKeyRotation(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()
	require.NoError(t, store.AddProvider(ctx, schemas.Anthropic, configstore.ProviderConfig{Keys: []schemas.Key{
		{ID: "k-anthropic-prod", Name: "Anthropic Prod", Value: *schemas.NewSecretVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 1},
	}}))
	file := `{"model_routers":[` + routerJSON("support", "Support",
		`"targets":[{"tier":"SIMPLE","provider":"openai","model":"gpt-4o-mini","weight":1,"fallbacks":[{"provider":"anthropic","model":"claude-sonnet-4","provider_key_name":"Anthropic Prod"}]}]`,
		"s", "m", "c") + `]}`
	fallbackKey := func() string {
		router, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		require.Len(t, router.Targets, 1)
		require.Len(t, router.Targets[0].ParsedFallbacks, 1)
		return router.Targets[0].ParsedFallbacks[0].KeyID
	}

	bootFromJSON(t, store, file)
	require.Equal(t, "k-anthropic-prod", fallbackKey())

	require.NoError(t, store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		return tx.Model(&tables.TableKey{}).Where("name = ?", "Anthropic Prod").Update("key_id", "k-anthropic-prod-2").Error
	}))
	bootFromJSON(t, store, file)
	assert.Equal(t, "k-anthropic-prod-2", fallbackKey(), "a rotated key is pinned again although the file did not change")
}

// TestModelRouterConfigRoutingKeepsBuilderQuery pins that the dashboard's
// condition builder state survives a file routing that leaves the condition
// as it is, and is cleared once the file changes the condition, as a routing
// update through the API clears it.
func TestModelRouterConfigRoutingKeepsBuilderQuery(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()
	routingWith := func(cel string, priority int) string {
		return fmt.Sprintf(`"routing":{"enabled":true,"scope":"global","cel_expression":%q,"priority":%d}`, cel, priority)
	}
	boot := func(extra string) {
		bootFromJSON(t, store, `{"model_routers":[`+routerJSON("support", "Support", extra, "s", "m", "c")+`]}`)
	}
	routing := func() configstore.ModelRouterRouting {
		router, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		require.NotNil(t, router.Routing)
		return *router.Routing
	}

	boot(routingWith(`model == "support-assistant"`, 3))
	builder := routing()
	builder.Query = map[string]any{"combinator": "and", "rules": []any{map[string]any{"field": "model", "operator": "=", "value": "support-assistant"}}}
	require.NoError(t, store.SetModelRouterRouting(ctx, "support", builder))

	boot(routingWith(`model == "support-assistant"`, 4))
	assert.Equal(t, 4, routing().Priority)
	assert.Equal(t, builder.Query, routing().Query, "an unchanged condition keeps the builder state")

	boot(routingWith(`model == "billing-assistant"`, 4))
	assert.Equal(t, `model == "billing-assistant"`, routing().CelExpression)
	assert.Nil(t, routing().Query, "a changed condition clears the builder state")
}

// TestModelRouterConfigSkipsTakenPriority pins that a router whose routing
// priority another router already holds in its scope is skipped whole, as a
// routing rule is: a new router is not created and a stored one keeps its
// last persisted version, so its file hash is not recorded and a later boot
// applies the entry once the priority is free, in split mode too. Among
// declared routers, the one declared first keeps the priority.
func TestModelRouterConfigSkipsTakenPriority(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()
	global := func(priority int) string {
		return fmt.Sprintf(`"routing":{"enabled":true,"scope":"global","priority":%d}`, priority)
	}
	boot := func(entries ...string) {
		bootFromJSON(t, store, `{"model_routers":[`+strings.Join(entries, ",")+`]}`)
	}
	get := func(id string) configstore.ModelRouter {
		router, err := store.GetModelRouter(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, router.Routing)
		return *router
	}
	dashboardRouter := func(id string, priority int) {
		router := &configstore.ModelRouter{ID: id, Name: id, ComplexityAnalyzerConfig: configstore.ComplexityAnalyzerConfig{
			Keywords: configstore.ComplexityEditableKeywordConfig{SimpleKeywords: []string{"s"}, MediumKeywords: []string{"m"}, ComplexKeywords: []string{"c"}},
		}}
		require.NoError(t, store.CreateModelRouter(ctx, router))
		require.NoError(t, store.SetModelRouterRouting(ctx, id, configstore.ModelRouterRouting{Scope: "global", Priority: priority}))
	}
	dashboardRouter("dash-a", 3)
	dashboardRouter("dash-b", 5)

	boot(routerJSON("support", "Support", global(3), "s", "m", "c"))
	requireModelRouterAbsent(t, store, "support")

	boot(routerJSON("billing", "Billing", global(1), "s", "m", "c"))
	boot(routerJSON("billing", "Billing desk", global(5), "s", "m", "c"))
	billing := get("billing")
	assert.Equal(t, "Billing", billing.Name, "a stored router keeps its last persisted version")
	assert.Equal(t, 1, billing.Routing.Priority)

	boot(routerJSON("alpha", "Alpha", global(7), "s", "m", "c"), routerJSON("beta", "Beta", global(7), "s", "m", "c"))
	assert.Equal(t, 7, get("alpha").Routing.Priority, "the router declared first keeps the priority")
	requireModelRouterAbsent(t, store, "beta")

	require.NoError(t, store.SetModelRouterRouting(ctx, "dash-a", configstore.ModelRouterRouting{Scope: "global", Priority: 30}))
	require.NoError(t, store.SetModelRouterRouting(ctx, "dash-b", configstore.ModelRouterRouting{Scope: "global", Priority: 31}))
	boot(routerJSON("support", "Support", global(3), "s", "m", "c"), routerJSON("billing", "Billing desk", global(5), "s", "m", "c"))
	assert.Equal(t, 3, get("support").Routing.Priority, "the skipped router is created once its priority is free")
	billing = get("billing")
	assert.Equal(t, "Billing desk", billing.Name, "the held-back entry applies once its priority is free")
	assert.Equal(t, 5, billing.Routing.Priority)
}

// TestModelRouterConfigSwapsPriorities pins that two routers that swap
// priorities in config.json both apply, in split mode and in source-of-truth
// mode: the check runs on the state after the sync, as it does for routing
// rules, and the routing is written in one transaction.
func TestModelRouterConfigSwapsPriorities(t *testing.T) {
	initTestLogger()
	for _, prefix := range []string{`{`, `{"source_of_truth":"config.json",`} {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		ctx := context.Background()
		boot := func(x, y int) {
			global := func(priority int) string {
				return fmt.Sprintf(`"routing":{"enabled":true,"scope":"global","priority":%d}`, priority)
			}
			bootFromJSON(t, store, prefix+`"model_routers":[`+
				routerJSON("x", "X", global(x), "s", "m", "c")+","+routerJSON("y", "Y", global(y), "s", "m", "c")+`]}`)
		}
		priority := func(id string) int {
			router, err := store.GetModelRouter(ctx, id)
			require.NoError(t, err)
			require.NotNil(t, router.Routing)
			return router.Routing.Priority
		}

		boot(1, 2)
		boot(2, 1)
		assert.Equal(t, 2, priority("x"), "%s: swapped priorities both apply", prefix)
		assert.Equal(t, 1, priority("y"), "%s: swapped priorities both apply", prefix)
	}
}

// TestModelRouterConfigPlacesRoutersWithoutRoutingLast pins where a router
// declared without routing is created: off, global, and after every global
// priority the sync leaves taken, the ones it writes included. The store's own
// default looks only at what is stored, so such a router could take a
// priority a routing declared in the same file needs, and that routing would
// then never apply.
func TestModelRouterConfigPlacesRoutersWithoutRoutingLast(t *testing.T) {
	initTestLogger()
	global := func(priority int) string {
		return fmt.Sprintf(`"routing":{"enabled":true,"scope":"global","priority":%d}`, priority)
	}
	cases := map[string]struct {
		entries  []string
		declared map[string]int
	}{
		"declared after a routed router": {
			entries:  []string{routerJSON("billing", "Billing", global(1), "s", "m", "c"), routerJSON("support", "Support", "", "s", "m", "c")},
			declared: map[string]int{"billing": 1},
		},
		"declared before routed routers": {
			entries: []string{
				routerJSON("support", "Support", "", "s", "m", "c"),
				routerJSON("billing", "Billing", global(0), "s", "m", "c"),
				routerJSON("sales", "Sales", global(5), "s", "m", "c"),
			},
			declared: map[string]int{"billing": 0, "sales": 5},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := createTestSQLiteConfigStore(t, t.TempDir())
			ctx := context.Background()
			bootFromJSON(t, store, `{"model_routers":[`+strings.Join(tc.entries, ",")+`]}`)
			for id, want := range tc.declared {
				router, err := store.GetModelRouter(ctx, id)
				require.NoError(t, err)
				require.NotNil(t, router.Routing)
				assert.Equal(t, want, router.Routing.Priority, "%s keeps its declared priority", id)
			}
			support, err := store.GetModelRouter(ctx, "support")
			require.NoError(t, err)
			require.NotNil(t, support.Routing)
			assert.Equal(t, "global", support.Routing.Scope)
			assert.False(t, support.Routing.Enabled, "a router declared without routing starts off")
			for id, priority := range tc.declared {
				assert.Greater(t, support.Routing.Priority, priority, "support goes after %s", id)
			}
		})
	}
}

// failingRoutingStore fails every routing write for one router while fail is
// set, standing in for a store write that fails after validation passed.
type failingRoutingStore struct {
	configstore.ConfigStore
	routerID string
	fail     bool
}

func (s *failingRoutingStore) SetModelRouterRoutings(ctx context.Context, changes []configstore.ModelRouterRoutingChange, tx ...*gorm.DB) error {
	for _, change := range changes {
		if s.fail && change.RouterID == s.routerID {
			return errors.New("injected routing write failure")
		}
	}
	return s.ConfigStore.SetModelRouterRoutings(ctx, changes, tx...)
}

func (s *failingRoutingStore) SetModelRouterRouting(ctx context.Context, routerID string, routing configstore.ModelRouterRouting, tx ...*gorm.DB) error {
	if s.fail && routerID == s.routerID {
		return errors.New("injected routing write failure")
	}
	return s.ConfigStore.SetModelRouterRouting(ctx, routerID, routing, tx...)
}

// TestModelRouterConfigRetriesFailedRouting pins that a router whose routing
// write fails is not recorded as applied: the other routers' routing still
// lands on that boot, and the failed one is retried by the next boot, in
// split mode, where an entry recorded as applied is otherwise left alone.
func TestModelRouterConfigRetriesFailedRouting(t *testing.T) {
	initTestLogger()
	store := &failingRoutingStore{ConfigStore: createTestSQLiteConfigStore(t, t.TempDir()), routerID: "billing", fail: true}
	ctx := context.Background()
	global := func(priority int) string {
		return fmt.Sprintf(`"routing":{"enabled":true,"scope":"global","priority":%d}`, priority)
	}
	file := `{"model_routers":[` +
		routerJSON("billing", "Billing", global(0), "s", "m", "c") + "," +
		routerJSON("sales", "Sales", global(5), "s", "m", "c") + `]}`
	priority := func(id string) int {
		router, err := store.GetModelRouter(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, router.Routing)
		return router.Routing.Priority
	}

	bootFromJSON(t, store, file)
	assert.Equal(t, 5, priority("sales"), "a failure on another router does not hold back this one's routing")
	assert.NotEqual(t, 0, priority("billing"), "billing's routing was not written")

	store.fail = false
	bootFromJSON(t, store, file)
	assert.Equal(t, 0, priority("billing"), "the next boot retries the routing it could not write")
	assert.Equal(t, 5, priority("sales"))
}

// failingTargetsStore fails SetModelRouterTargets for one router while fail is
// set, standing in for a store write that fails after validation passed.
type failingTargetsStore struct {
	configstore.ConfigStore
	routerID string
	fail     bool
}

func (s *failingTargetsStore) SetModelRouterTargets(ctx context.Context, routerID string, targets []tables.TableModelRouterTarget, tx ...*gorm.DB) error {
	if s.fail && routerID == s.routerID {
		return errors.New("injected targets write failure")
	}
	return s.ConfigStore.SetModelRouterTargets(ctx, routerID, targets, tx...)
}

// TestModelRouterConfigRetriesFailedTargets pins that a router whose targets
// write fails is not recorded as applied, so the next boot writes them, in
// split mode, where an entry recorded as applied is otherwise left alone.
func TestModelRouterConfigRetriesFailedTargets(t *testing.T) {
	initTestLogger()
	store := &failingTargetsStore{ConfigStore: createTestSQLiteConfigStore(t, t.TempDir()), routerID: "support", fail: true}
	ctx := context.Background()
	file := `{"model_routers":[` + routerJSON("support", "Support",
		`"targets":[{"tier":"SIMPLE","provider":"openai","model":"gpt-4o-mini","weight":1}]`, "s", "m", "c") + `]}`

	bootFromJSON(t, store, file)
	router, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Empty(t, router.Targets, "the targets write failed")

	store.fail = false
	bootFromJSON(t, store, file)
	router, err = store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	require.Len(t, router.Targets, 1, "the next boot retries the targets it could not write")
	assert.Equal(t, "gpt-4o-mini", *router.Targets[0].Model)
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
