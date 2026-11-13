package lib

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"
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

// modelRouterBootModes are the two ways config.json reconciles with the store,
// each as the prefix that opens the file.
var modelRouterBootModes = []struct {
	name          string
	prefix        string
	sourceOfTruth bool
}{
	{name: "split mode", prefix: `{`},
	{name: "source of truth", prefix: `{"source_of_truth":"config.json",`, sourceOfTruth: true},
}

// bootModelRouters boots a file whose model_routers section holds entries,
// opened by a prefix from modelRouterBootModes.
func bootModelRouters(t *testing.T, store configstore.ConfigStore, prefix string, entries ...string) {
	t.Helper()
	bootFromJSON(t, store, prefix+`"model_routers":[`+strings.Join(entries, ",")+`]}`)
}

// globalRoutingJSON renders a routing block for the global requests cel holds
// for; an empty cel takes them all.
func globalRoutingJSON(enabled bool, cel string, priority int) string {
	return fmt.Sprintf(`"routing":{"enabled":%t,"scope":"global","cel_expression":%q,"priority":%d}`, enabled, cel, priority)
}

// tierTargetJSON renders one target of a model router.
func tierTargetJSON(tier, provider, model string, weight float64) string {
	return fmt.Sprintf(`{"tier":%q,"provider":%q,"model":%q,"weight":%g}`, tier, provider, model, weight)
}

// targetsJSON renders a targets block from tierTargetJSON entries; none is an
// explicit empty list.
func targetsJSON(targets ...string) string {
	return `"targets":[` + strings.Join(targets, ",") + `]`
}

// storedModelRouter reads a router that must be stored and have routing.
func storedModelRouter(t *testing.T, store configstore.ConfigStore, id string) configstore.ModelRouter {
	t.Helper()
	router, err := store.GetModelRouter(context.Background(), id)
	require.NoError(t, err, "model router %s", id)
	require.NotNil(t, router.Routing, "model router %s has routing", id)
	return *router
}

// storedTargetModels lists a stored router's targets as "TIER provider/model
// weight", in the store's order: by tier, then heaviest first.
func storedTargetModels(t *testing.T, store configstore.ConfigStore, id string) []string {
	t.Helper()
	router := storedModelRouter(t, store, id)
	out := make([]string, 0, len(router.Targets))
	for _, target := range router.Targets {
		provider, model := "", ""
		if target.Provider != nil {
			provider = *target.Provider
		}
		if target.Model != nil {
			model = *target.Model
		}
		out = append(out, fmt.Sprintf("%s %s/%s %g", target.Tier, provider, model, target.Weight))
	}
	return out
}

// modelRouterWriteCounter records every model router write that reaches the
// store, so a test can pin that a boot wrote nothing.
type modelRouterWriteCounter struct {
	configstore.ConfigStore
	writes []string
}

func (s *modelRouterWriteCounter) CreateModelRouter(ctx context.Context, router *configstore.ModelRouter, tx ...*gorm.DB) error {
	s.writes = append(s.writes, "create "+router.ID)
	return s.ConfigStore.CreateModelRouter(ctx, router, tx...)
}

func (s *modelRouterWriteCounter) UpdateModelRouter(ctx context.Context, router *configstore.ModelRouter, tx ...*gorm.DB) error {
	s.writes = append(s.writes, "update "+router.ID)
	return s.ConfigStore.UpdateModelRouter(ctx, router, tx...)
}

func (s *modelRouterWriteCounter) DeleteModelRouter(ctx context.Context, id string, tx ...*gorm.DB) error {
	s.writes = append(s.writes, "delete "+id)
	return s.ConfigStore.DeleteModelRouter(ctx, id, tx...)
}

func (s *modelRouterWriteCounter) SetModelRouterTargets(ctx context.Context, routerID string, targets []tables.TableModelRouterTarget, tx ...*gorm.DB) error {
	s.writes = append(s.writes, "targets "+routerID)
	return s.ConfigStore.SetModelRouterTargets(ctx, routerID, targets, tx...)
}

func (s *modelRouterWriteCounter) SetModelRouterRouting(ctx context.Context, routerID string, routing configstore.ModelRouterRouting, tx ...*gorm.DB) error {
	s.writes = append(s.writes, "routing "+routerID)
	return s.ConfigStore.SetModelRouterRouting(ctx, routerID, routing, tx...)
}

func (s *modelRouterWriteCounter) SetModelRouterRoutings(ctx context.Context, changes []configstore.ModelRouterRoutingChange, tx ...*gorm.DB) error {
	for _, change := range changes {
		s.writes = append(s.writes, "routing "+change.RouterID)
	}
	return s.ConfigStore.SetModelRouterRoutings(ctx, changes, tx...)
}

// tierByTextDecision is a fake decision executor: it answers the complexity
// question with the tier mapped to the request's latest message, and SIMPLE
// for any other text.
func tierByTextDecision(tiers map[string]string) routing.DecisionRequestExecutor {
	return func(_ *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		tier := "SIMPLE"
		for _, message := range req.Input.Messages {
			if message.Content.Text == nil {
				continue
			}
			if mapped, ok := tiers[*message.Content.Text]; ok {
				tier = mapped
			}
		}
		confidence := 0.9
		return &schemas.BifrostDecisionResponse{
			Model: "decision-fake",
			Answers: []schemas.DecisionAnswer{
				{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("complexity_tier"), Choice: &schemas.DecisionScalar{Str: &tier}, Confidence: &confidence},
			},
		}, nil
	}
}

// TestModelRouterConfigFileToLiveRouting follows one config.json from the file
// to a routed request: the file declares a routing rule and two model routers,
// LoadConfig syncs both into the store, and a routing plugin started from that
// store serves them. It pins the order an operator relies on: the routing rule
// rewrites the model first, the router whose condition names the rewritten
// model takes the request, and its target for the classified tier is the
// final model. A router tried ahead of it whose condition names the model the
// client asked for does not take a request the rule rewrote, although it is
// live for the requests the rule leaves alone.
func TestModelRouterConfigFileToLiveRouting(t *testing.T) {
	initTestLogger()
	const (
		complexText = "design a sharded cache with a write-ahead log"
		mediumText  = "refactor this module to remove the global state"
		simpleText  = "what does this regex match"
	)
	governance := `{"routing_rules":[{
		"id":"support-alias","name":"Support alias","enabled":true,"scope":"global","priority":0,
		"cel_expression":"model == \"assistant\"",
		"targets":[{"provider":"openai","model":"support-assistant","weight":1}]}]}`
	routers := `[` +
		routerJSON("client-model", "Client model", `"classifier":"decision",`+
			targetsJSON(tierTargetJSON("COMPLEX", "openai", "legacy-large", 1))+","+
			globalRoutingJSON(true, `model == "assistant" || model == "legacy-assistant"`, 0), "s", "m", "c") + "," +
		routerJSON("support", "Support", `"classifier":"decision",`+
			targetsJSON(tierTargetJSON("SIMPLE", "openai", "gpt-4o-mini", 1), tierTargetJSON("COMPLEX", "anthropic", "claude-opus", 1))+","+
			globalRoutingJSON(true, `model == "support-assistant"`, 1), "s", "m", "c") +
		`]`
	require.NoError(t, ValidateConfigSchema([]byte(`{"governance":`+governance+`,"model_routers":`+routers+`}`), loadLocalSchema(t)),
		"the two sections are ones the schema accepts")

	tempDir := createTempDir(t)
	file, err := json.Marshal(makeConfigDataWithProvidersAndDir(map[string]configstore.ProviderConfig{
		"openai": makeProviderConfigWithNetwork("openai-key-1", "sk-test-123", "https://api.openai.com"),
	}, tempDir))
	require.NoError(t, err)
	file, err = sjson.SetRawBytes(file, "governance", []byte(governance))
	require.NoError(t, err)
	file, err = sjson.SetRawBytes(file, "model_routers", []byte(routers))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "config.json"), file, 0o644))

	ctx := context.Background()
	config, err := LoadConfig(ctx, tempDir)
	require.NoError(t, err)
	t.Cleanup(func() { config.Close(ctx) })

	plugin, err := routing.Init(ctx, nil, &testLogger{}, config.ConfigStore, routing.NewMockGovernance())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	plugin.SetDecisionRequestExecutor(tierByTextDecision(map[string]string{complexText: "COMPLEX", mediumText: "MEDIUM", simpleText: "SIMPLE"}))

	type routed struct {
		provider schemas.ModelProvider
		model    string
		routerID any
		engines  []string
	}
	route := func(model, text string) routed {
		t.Helper()
		reqCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		req := &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    model,
				Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &text}}},
			},
		}
		require.NoError(t, plugin.PreRequestHook(reqCtx, req))
		got := routed{routerID: reqCtx.Value(schemas.BifrostContextKeyModelRouterID)}
		got.provider, got.model, _ = req.GetRequestFields()
		got.engines, _ = reqCtx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string)
		return got
	}
	ruleThenRouter := []string{schemas.RoutingEngineRoutingRule, schemas.RoutingEngineModelRouter}

	got := route("assistant", complexText)
	assert.Equal(t, routed{provider: "anthropic", model: "claude-opus", routerID: "support", engines: ruleThenRouter}, got,
		"the rule rewrites the model, the router that names the rewritten model takes it, and its COMPLEX target is final")

	got = route("assistant", simpleText)
	assert.Equal(t, routed{provider: "openai", model: "gpt-4o-mini", routerID: "support", engines: ruleThenRouter}, got,
		"the same router sends a SIMPLE request to its SIMPLE target")

	got = route("assistant", mediumText)
	assert.Equal(t, routed{provider: "openai", model: "support-assistant", routerID: "support", engines: ruleThenRouter}, got,
		"a tier the router has no target for keeps the model the routing rule chose")

	got = route("legacy-assistant", complexText)
	assert.Equal(t, routed{provider: "openai", model: "legacy-large", routerID: "client-model", engines: []string{schemas.RoutingEngineModelRouter}}, got,
		"the router that names the client's model is live for a request the rule leaves alone")

	got = route("gpt-4o", complexText)
	assert.Equal(t, routed{provider: "openai", model: "gpt-4o"}, got, "a request no rule and no router names is left as it was")
}

// TestModelRouterConfigIdempotentBoot pins that booting twice from the same
// config.json writes nothing the second time, in split mode and with
// config.json as the source of truth: no router, targets or routing write
// reaches the store, and every row, updated_at and recorded hashes included,
// is as the first boot left it. The file covers the shapes a router is
// declared in: each classifier, a scoped and a global routing, a routing that
// leaves its scope and priority to their defaults, targets with a fallback
// pinned by key name, an explicit empty targets list, and neither.
func TestModelRouterConfigIdempotentBoot(t *testing.T) {
	initTestLogger()
	ids := []string{"support", "billing", "triage", "general", "cleared"}
	for _, mode := range modelRouterBootModes {
		t.Run(mode.name, func(t *testing.T) {
			ctx := context.Background()
			store := &modelRouterWriteCounter{ConfigStore: createTestSQLiteConfigStore(t, t.TempDir())}
			require.NoError(t, store.AddProvider(ctx, schemas.Anthropic, configstore.ProviderConfig{Keys: []schemas.Key{
				{ID: "k-anthropic-prod", Name: "Anthropic Prod", Value: *schemas.NewSecretVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 1},
			}}))
			file := mode.prefix + `"governance":{"teams":[{"id":"team-eng","name":"Engineering"}]},"model_routers":[` +
				routerJSON("support", "Support", `"description":"Support desk traffic",`+
					`"semantic":{"provider":"openai","embedding_model":"text-embedding-3-small"},"session":{"enabled":true},`+
					`"targets":[{"tier":"simple","provider":"openai","model":"gpt-4o-mini","weight":0.75},`+
					`{"tier":"SIMPLE","provider":"openai","model":"gpt-4.1-nano","weight":0.25},`+
					`{"tier":"COMPLEX","provider":"openai","model":"gpt-4o","weight":1,"fallbacks":["openai/gpt-4o-mini",{"provider":"anthropic","model":"claude-sonnet-4","provider_key_name":"Anthropic Prod"}]}],`+
					`"routing":{"enabled":true,"scope":"team","scope_id":"team-eng","cel_expression":" model == \"support-assistant\" ","priority":0}`,
					"support-simple", "support-medium", "support-complex") + "," +
				routerJSON("billing", "Billing", `"classifier":"decision",`+
					targetsJSON(tierTargetJSON("COMPLEX", "anthropic", "claude-opus", 1))+","+
					globalRoutingJSON(true, `model == "billing-assistant"`, 1), "s", "m", "c") + "," +
				routerJSON("triage", "Triage", `"classifier":"llm","llm":{"provider":"openai","model":"gpt-4o-mini"},"routing":{"enabled":true}`, "s", "m", "c") + "," +
				routerJSON("general", "General", "", "s", "m", "c") + "," +
				routerJSON("cleared", "Cleared", targetsJSON(), "s", "m", "c") +
				`]}`

			bootFromJSON(t, store, file)
			require.NotEmpty(t, store.writes, "the first boot writes the routers")
			rows := make(map[string]tables.TableModelRouter, len(ids))
			for _, id := range ids {
				rows[id] = storedModelRouterRow(t, store, id)
				assert.NotEmpty(t, rows[id].ConfigHash, "%s: the first boot records the entry's hash", id)
			}
			first, err := store.GetModelRouters(ctx)
			require.NoError(t, err)
			require.Len(t, first, len(ids))
			assert.Equal(t, configstore.ModelRouterRouting{Enabled: true, Scope: "team", ScopeID: schemas.Ptr("team-eng"), CelExpression: `model == "support-assistant"`},
				*storedModelRouter(t, store, "support").Routing)
			assert.Equal(t, []string{"COMPLEX openai/gpt-4o 1", "SIMPLE openai/gpt-4o-mini 0.75", "SIMPLE openai/gpt-4.1-nano 0.25"}, storedTargetModels(t, store, "support"))

			store.writes = nil
			bootFromJSON(t, store, file)

			assert.Empty(t, store.writes, "the second boot from the same file writes nothing")
			for _, id := range ids {
				assert.Equal(t, rows[id], storedModelRouterRow(t, store, id), "%s: the row is as the first boot left it", id)
			}
			second, err := store.GetModelRouters(ctx)
			require.NoError(t, err)
			assert.Equal(t, first, second, "config, targets and routing are unchanged")
		})
	}
}

// TestModelRouterConfigEditsBetweenBoots walks one router through the edits an
// operator makes to its config.json entry between boots, in split mode and
// with config.json as the source of truth: new targets replace the stored
// ones, a new condition, priority and enabled flag replace the stored routing,
// and an explicit empty targets list clears them. Each edit leaves the part of
// the router it does not touch, and the other router in the file, as they
// were. Removing a router from the file deletes it only when config.json is
// the source of truth; in split mode it stays as it was.
func TestModelRouterConfigEditsBetweenBoots(t *testing.T) {
	initTestLogger()
	billing := routerJSON("billing", "Billing",
		targetsJSON(tierTargetJSON("COMPLEX", "anthropic", "claude-opus", 1))+","+globalRoutingJSON(true, `model == "billing-assistant"`, 2), "s", "m", "c")
	support := func(targets, routing string) string {
		return routerJSON("support", "Support", targets+","+routing, "s", "m", "c")
	}
	mini := targetsJSON(tierTargetJSON("SIMPLE", "openai", "gpt-4o-mini", 1))
	split := targetsJSON(
		tierTargetJSON("SIMPLE", "openai", "gpt-4.1-nano", 0.25),
		tierTargetJSON("SIMPLE", "openai", "gpt-4o-mini", 0.75),
		tierTargetJSON("COMPLEX", "anthropic", "claude-opus", 1))
	on := globalRoutingJSON(true, `model == "support-assistant"`, 1)
	moved := globalRoutingJSON(false, `model == "help-assistant"`, 5)
	onRouting := configstore.ModelRouterRouting{Enabled: true, Scope: "global", CelExpression: `model == "support-assistant"`, Priority: 1}
	movedRouting := configstore.ModelRouterRouting{Scope: "global", CelExpression: `model == "help-assistant"`, Priority: 5}
	splitModels := []string{"COMPLEX anthropic/claude-opus 1", "SIMPLE openai/gpt-4o-mini 0.75", "SIMPLE openai/gpt-4.1-nano 0.25"}

	for _, mode := range modelRouterBootModes {
		t.Run(mode.name, func(t *testing.T) {
			store := createTestSQLiteConfigStore(t, t.TempDir())
			boot := func(entries ...string) { bootModelRouters(t, store, mode.prefix, entries...) }

			boot(support(mini, on), billing)
			require.Equal(t, []string{"SIMPLE openai/gpt-4o-mini 1"}, storedTargetModels(t, store, "support"))
			require.Equal(t, onRouting, *storedModelRouter(t, store, "support").Routing)
			billingRow := storedModelRouterRow(t, store, "billing")

			boot(support(split, on), billing)
			assert.Equal(t, splitModels, storedTargetModels(t, store, "support"), "changed targets replace the stored ones")
			assert.Equal(t, onRouting, *storedModelRouter(t, store, "support").Routing, "a targets edit leaves the routing alone")

			boot(support(split, moved), billing)
			assert.Equal(t, movedRouting, *storedModelRouter(t, store, "support").Routing, "a changed condition, priority and enabled flag replace the stored routing")
			assert.Equal(t, splitModels, storedTargetModels(t, store, "support"), "a routing edit leaves the targets alone")

			boot(support(targetsJSON(), moved), billing)
			assert.Empty(t, storedTargetModels(t, store, "support"), "an explicit empty targets list clears them")
			assert.Equal(t, movedRouting, *storedModelRouter(t, store, "support").Routing)

			assert.Equal(t, billingRow, storedModelRouterRow(t, store, "billing"), "edits to one router write nothing to the other")

			boot(support(targetsJSON(), moved))
			if mode.sourceOfTruth {
				requireModelRouterAbsent(t, store, "billing")
			} else {
				assert.Equal(t, billingRow, storedModelRouterRow(t, store, "billing"), "split mode keeps a router the file no longer declares")
				assert.Equal(t, []string{"COMPLEX anthropic/claude-opus 1"}, storedTargetModels(t, store, "billing"))
				assert.Equal(t, configstore.ModelRouterRouting{Enabled: true, Scope: "global", CelExpression: `model == "billing-assistant"`, Priority: 2},
					*storedModelRouter(t, store, "billing").Routing)
			}
			assert.Equal(t, movedRouting, *storedModelRouter(t, store, "support").Routing, "removing one router leaves the other alone")
		})
	}
}

// TestModelRouterConfigSplitModeDashboardTargetsPerEntry pins that split mode
// decides per router whether the file or the dashboard wins on targets: in one
// boot, the entry whose targets changed in the file replaces the dashboard's
// targets, the entry the file left as it was keeps the dashboard's, and an
// entry that only gained a phrase merges the phrase in and keeps the
// dashboard's targets, since targets follow the entry's own hash and not the
// phrase lists'.
func TestModelRouterConfigSplitModeDashboardTargetsPerEntry(t *testing.T) {
	initTestLogger()
	store := createTestSQLiteConfigStore(t, t.TempDir())
	ctx := context.Background()
	mini := targetsJSON(tierTargetJSON("SIMPLE", "openai", "gpt-4o-mini", 1))
	nano := targetsJSON(tierTargetJSON("SIMPLE", "openai", "gpt-4.1-nano", 1))
	entry := func(id, name, targets, medium string) string {
		return routerJSON(id, name, targets, id+"-simple", medium, id+"-complex")
	}
	dashboardSets := func(id string) {
		model := "claude-haiku"
		require.NoError(t, store.SetModelRouterTargets(ctx, id, []tables.TableModelRouterTarget{{Tier: "SIMPLE", Model: &model, Weight: 1}}))
	}

	bootModelRouters(t, store, `{`,
		entry("support", "Support", mini, "support-medium"),
		entry("billing", "Billing", mini, "billing-medium"),
		entry("sales", "Sales", mini, "sales-medium"))
	for _, id := range []string{"support", "billing", "sales"} {
		dashboardSets(id)
	}

	bootModelRouters(t, store, `{`,
		entry("support", "Support", nano, "support-medium"),
		entry("billing", "Billing", mini, "billing-medium"),
		entry("sales", "Sales", mini, "sales-medium-2"))

	assert.Equal(t, []string{"SIMPLE openai/gpt-4.1-nano 1"}, storedTargetModels(t, store, "support"), "the file wins once its entry's targets change")
	assert.Equal(t, []string{"SIMPLE /claude-haiku 1"}, storedTargetModels(t, store, "billing"), "an unchanged entry keeps the dashboard's targets although another entry changed")
	assert.Equal(t, []string{"SIMPLE /claude-haiku 1"}, storedTargetModels(t, store, "sales"), "a phrase added in the file leaves the dashboard's targets alone")
	assert.ElementsMatch(t, []string{"sales-medium", "sales-medium-2"}, storedModelRouter(t, store, "sales").Keywords.MediumKeywords, "the added phrase is merged in")
}

// TestModelRouterConfigDuplicateName pins what an operator gets when two
// entries share a name under different ids, which the schema cannot check:
// names are unique across routers, so the entry declared first is kept and the
// later one is skipped whole, on every boot, while the entries around it load
// with the targets and routing they declare. A name a dashboard router
// already holds is refused the same way, and that router is left as it was.
func TestModelRouterConfigDuplicateName(t *testing.T) {
	initTestLogger()
	entries := []string{
		routerJSON("support", "Support", `"description":"first",`+
			targetsJSON(tierTargetJSON("SIMPLE", "openai", "gpt-4o-mini", 1))+","+globalRoutingJSON(true, "", 1), "s", "m", "c"),
		routerJSON("support-eu", "Support", `"description":"second",`+
			targetsJSON(tierTargetJSON("SIMPLE", "openai", "gpt-4.1-nano", 1))+","+globalRoutingJSON(true, "", 2), "s", "m", "c"),
		routerJSON("general", "General",
			targetsJSON(tierTargetJSON("COMPLEX", "anthropic", "claude-opus", 1))+","+globalRoutingJSON(true, "", 3), "s", "m", "c"),
	}
	for _, mode := range modelRouterBootModes {
		t.Run(mode.name, func(t *testing.T) {
			store := createTestSQLiteConfigStore(t, t.TempDir())
			for boot := 1; boot <= 2; boot++ {
				bootModelRouters(t, store, mode.prefix, entries...)

				support := storedModelRouter(t, store, "support")
				assert.Equal(t, "first", support.Description, "boot %d: the entry declared first keeps the name", boot)
				assert.Equal(t, 1, support.Routing.Priority)
				assert.Equal(t, []string{"SIMPLE openai/gpt-4o-mini 1"}, storedTargetModels(t, store, "support"))
				requireModelRouterAbsent(t, store, "support-eu")
				general := storedModelRouter(t, store, "general")
				assert.Equal(t, 3, general.Routing.Priority, "boot %d: the entry after the skipped one still loads", boot)
				assert.True(t, general.Routing.Enabled)
				assert.Equal(t, []string{"COMPLEX anthropic/claude-opus 1"}, storedTargetModels(t, store, "general"))
			}
		})
	}

	t.Run("a name a dashboard router holds", func(t *testing.T) {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		ctx := context.Background()
		require.NoError(t, store.CreateModelRouter(ctx, &configstore.ModelRouter{ID: "dashboard", Name: "Support", ComplexityAnalyzerConfig: configstore.ComplexityAnalyzerConfig{
			Keywords: configstore.ComplexityEditableKeywordConfig{SimpleKeywords: []string{"dash-simple"}, MediumKeywords: []string{"dash-medium"}, ComplexKeywords: []string{"dash-complex"}},
		}}))
		dashboard := storedModelRouterRow(t, store, "dashboard")

		bootModelRouters(t, store, `{`, entries[0], entries[2])

		requireModelRouterAbsent(t, store, "support")
		assert.Equal(t, dashboard, storedModelRouterRow(t, store, "dashboard"), "the router that holds the name is left as it was")
		assert.Equal(t, 3, storedModelRouter(t, store, "general").Routing.Priority)
	})
}

// TestModelRouterConfigSkipsUserScope pins that config.json cannot scope a
// router to a user, which only the schema says: the loader skips such an
// entry whole, as it does a scope whose entity does not exist, so a new
// router is not created and a stored one keeps its last persisted version,
// while the other entries in the file load.
func TestModelRouterConfigSkipsUserScope(t *testing.T) {
	initTestLogger()
	userScoped := `"routing":{"enabled":true,"scope":"user","scope_id":"user-1","priority":0}`
	for _, mode := range modelRouterBootModes {
		t.Run(mode.name, func(t *testing.T) {
			store := createTestSQLiteConfigStore(t, t.TempDir())

			bootModelRouters(t, store, mode.prefix,
				routerJSON("personal", "Personal", userScoped+","+targetsJSON(tierTargetJSON("SIMPLE", "openai", "gpt-4o-mini", 1)), "s", "m", "c"),
				routerJSON("general", "General", targetsJSON(tierTargetJSON("SIMPLE", "openai", "gpt-4o-mini", 1))+","+globalRoutingJSON(true, "", 0), "s", "m", "c"))

			requireModelRouterAbsent(t, store, "personal")
			general := storedModelRouter(t, store, "general")
			assert.Equal(t, configstore.ModelRouterRouting{Enabled: true, Scope: "global"}, *general.Routing, "the entry beside the user-scoped one loads")
			assert.Equal(t, []string{"SIMPLE openai/gpt-4o-mini 1"}, storedTargetModels(t, store, "general"))

			bootModelRouters(t, store, mode.prefix,
				routerJSON("general", "General desk", targetsJSON(tierTargetJSON("SIMPLE", "openai", "gpt-4.1-nano", 1))+","+userScoped, "s", "m", "c"))

			general = storedModelRouter(t, store, "general")
			assert.Equal(t, "General", general.Name, "a stored router keeps its last persisted version")
			assert.Equal(t, configstore.ModelRouterRouting{Enabled: true, Scope: "global"}, *general.Routing)
			assert.Equal(t, []string{"SIMPLE openai/gpt-4o-mini 1"}, storedTargetModels(t, store, "general"))
		})
	}
}

// TestModelRouterConfigRotatesPriorities pins that three routers that rotate
// their priorities in one config.json edit all apply, in split mode and in
// source-of-truth mode: no router's new priority is free until another's has
// moved, so the routing has to be checked on the state after the sync and
// written together.
func TestModelRouterConfigRotatesPriorities(t *testing.T) {
	initTestLogger()
	for _, mode := range modelRouterBootModes {
		t.Run(mode.name, func(t *testing.T) {
			store := createTestSQLiteConfigStore(t, t.TempDir())
			boot := func(a, b, c int) {
				bootModelRouters(t, store, mode.prefix,
					routerJSON("a", "A", globalRoutingJSON(true, "", a), "s", "m", "c"),
					routerJSON("b", "B", globalRoutingJSON(true, "", b), "s", "m", "c"),
					routerJSON("c", "C", globalRoutingJSON(true, "", c), "s", "m", "c"))
			}
			priorities := func() map[string]int {
				out := map[string]int{}
				for _, id := range []string{"a", "b", "c"} {
					out[id] = storedModelRouter(t, store, id).Routing.Priority
				}
				return out
			}

			boot(1, 2, 3)
			require.Equal(t, map[string]int{"a": 1, "b": 2, "c": 3}, priorities())

			boot(2, 3, 1)
			assert.Equal(t, map[string]int{"a": 2, "b": 3, "c": 1}, priorities(), "each router takes the priority the next one held")

			boot(3, 1, 2)
			assert.Equal(t, map[string]int{"a": 3, "b": 1, "c": 2}, priorities(), "a second rotation applies too")
		})
	}
}

// TestModelRouterConfigReassignsDroppedRoutersPriority pins one edit with
// config.json as the source of truth: the file drops a router and gives its
// priority to another, and declares a new router at the priority that one
// left. The dropped router is pruned first, so both get the priority they
// declare. In split mode the same edit leaves the dropped router stored and
// holding its priority, so the router that asked for it keeps its last
// persisted version.
func TestModelRouterConfigReassignsDroppedRoutersPriority(t *testing.T) {
	initTestLogger()
	entry := func(id, name string, priority int) string {
		return routerJSON(id, name, globalRoutingJSON(true, "", priority), "s", "m", "c")
	}

	t.Run("source of truth", func(t *testing.T) {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		prefix := `{"source_of_truth":"config.json",`
		bootModelRouters(t, store, prefix, entry("x", "X", 1), entry("y", "Y", 2))

		bootModelRouters(t, store, prefix, entry("y", "Y", 1), entry("z", "Z", 2))

		requireModelRouterAbsent(t, store, "x")
		assert.Equal(t, 1, storedModelRouter(t, store, "y").Routing.Priority, "y takes the priority the dropped router held")
		assert.Equal(t, 2, storedModelRouter(t, store, "z").Routing.Priority, "a new router takes the priority y left")
	})

	t.Run("split mode keeps the dropped router and its priority", func(t *testing.T) {
		store := createTestSQLiteConfigStore(t, t.TempDir())
		bootModelRouters(t, store, `{`, entry("x", "X", 1), entry("y", "Y", 2))

		bootModelRouters(t, store, `{`, entry("y", "Y", 1))

		assert.Equal(t, 1, storedModelRouter(t, store, "x").Routing.Priority)
		assert.Equal(t, 2, storedModelRouter(t, store, "y").Routing.Priority, "the priority is still held, so y keeps its last persisted version")
	})
}

// TestModelRouterConfigSkipsOnlyInvalidRouters pins a file that mixes valid
// and invalid routers: a condition that does not compile, a condition that
// uses complexity_tier, target weights that do not sum to 1 and an unknown
// tier each skip their own entry, and the valid entries before, between and
// after them load with the targets and routing they declare. An invalid entry
// does not hold the priority it declares, so a valid one can take it.
func TestModelRouterConfigSkipsOnlyInvalidRouters(t *testing.T) {
	initTestLogger()
	mini := targetsJSON(tierTargetJSON("SIMPLE", "openai", "gpt-4o-mini", 1))
	invalid := map[string]string{
		"bad-condition":  mini + "," + globalRoutingJSON(true, `model ==`, 1),
		"tier-condition": mini + "," + globalRoutingJSON(true, `complexity_tier == "SIMPLE"`, 2),
		"bad-weights": targetsJSON(tierTargetJSON("SIMPLE", "openai", "gpt-4o-mini", 0.5), tierTargetJSON("SIMPLE", "openai", "gpt-4.1-nano", 0.3)) +
			"," + globalRoutingJSON(true, "", 3),
		"bad-tier": targetsJSON(tierTargetJSON("HARD", "openai", "gpt-4o", 1)) + "," + globalRoutingJSON(true, "", 4),
	}
	valid := func(id, name string, priority int) string {
		return routerJSON(id, name, mini+","+globalRoutingJSON(true, `model == "`+id+`"`, priority), "s", "m", "c")
	}
	bad := func(id string) string { return routerJSON(id, id, invalid[id], "s", "m", "c") }

	for _, mode := range modelRouterBootModes {
		t.Run(mode.name, func(t *testing.T) {
			store := createTestSQLiteConfigStore(t, t.TempDir())
			ctx := context.Background()

			bootModelRouters(t, store, mode.prefix,
				valid("first", "First", 0),
				bad("bad-condition"),
				bad("tier-condition"),
				valid("middle", "Middle", 1),
				bad("bad-weights"),
				bad("bad-tier"),
				valid("last", "Last", 4))

			for id := range invalid {
				requireModelRouterAbsent(t, store, id)
			}
			routers, err := store.GetModelRouters(ctx)
			require.NoError(t, err)
			assert.Len(t, routers, 3, "only the valid routers are stored")
			for id, priority := range map[string]int{"first": 0, "middle": 1, "last": 4} {
				assert.Equal(t, configstore.ModelRouterRouting{Enabled: true, Scope: "global", CelExpression: `model == "` + id + `"`, Priority: priority},
					*storedModelRouter(t, store, id).Routing, "%s loads with the routing it declares", id)
				assert.Equal(t, []string{"SIMPLE openai/gpt-4o-mini 1"}, storedTargetModels(t, store, id), "%s loads with its targets", id)
			}
		})
	}
}

// TestModelRouterConfigScopesToEntitiesInSameFile pins that a router can be
// scoped to a virtual key, team or customer the same config.json declares
// under governance: governance loads first, so the routing applies on the
// first boot instead of waiting for a second. A priority is unique within a
// scope and its entity, so routers in different scopes, and for different
// teams, can all declare the same one.
func TestModelRouterConfigScopesToEntitiesInSameFile(t *testing.T) {
	initTestLogger()
	scoped := func(scope, scopeID string) string {
		return fmt.Sprintf(`"routing":{"enabled":true,"scope":%q,"scope_id":%q,"priority":0}`, scope, scopeID)
	}
	scopes := map[string][2]string{
		"by-key":      {"virtual_key", "vk-support"},
		"by-team":     {"team", "team-eng"},
		"by-team-ops": {"team", "team-ops"},
		"by-customer": {"customer", "cust-acme"},
	}
	for _, mode := range modelRouterBootModes {
		t.Run(mode.name, func(t *testing.T) {
			store := createTestSQLiteConfigStore(t, t.TempDir())

			bootFromJSON(t, store, mode.prefix+`"governance":{`+
				`"customers":[{"id":"cust-acme","name":"Acme"}],`+
				`"teams":[{"id":"team-eng","name":"Engineering"},{"id":"team-ops","name":"Operations"}],`+
				`"virtual_keys":[{"id":"vk-support","name":"support-key","value":"sk-bf-support-test"}]},`+
				`"model_routers":[`+
				routerJSON("by-key", "By key", scoped("virtual_key", "vk-support"), "s", "m", "c")+","+
				routerJSON("by-team", "By team", scoped("team", "team-eng"), "s", "m", "c")+","+
				routerJSON("by-team-ops", "By team ops", scoped("team", "team-ops"), "s", "m", "c")+","+
				routerJSON("by-customer", "By customer", scoped("customer", "cust-acme"), "s", "m", "c")+","+
				routerJSON("everyone", "Everyone", globalRoutingJSON(true, "", 0), "s", "m", "c")+
				`]}`)

			for id, scope := range scopes {
				assert.Equal(t, configstore.ModelRouterRouting{Enabled: true, Scope: scope[0], ScopeID: schemas.Ptr(scope[1])},
					*storedModelRouter(t, store, id).Routing, "%s is scoped on the first boot", id)
			}
			assert.Equal(t, configstore.ModelRouterRouting{Enabled: true, Scope: "global"}, *storedModelRouter(t, store, "everyone").Routing)
		})
	}
}
