package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// testModelRouter returns a valid router with a semantic block.
func testModelRouter(id, name string) *ModelRouter {
	return &ModelRouter{
		ID:                       id,
		Name:                     name,
		Description:              "Requests from the " + name + " team",
		ComplexityAnalyzerConfig: *testSemanticAnalyzerConfig(),
	}
}

// fillEveryField sets every field reachable from v to a distinct non-zero
// value: pointers are allocated, slices and maps get one filled element. It
// fails on a field it cannot set, so an unexported field cannot slip past the
// round-trip test unchecked.
func fillEveryField(t *testing.T, v reflect.Value, path string, seq *int) {
	t.Helper()
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillEveryField(t, v.Elem(), path, seq)
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			*seq++
			v.Set(reflect.ValueOf(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(*seq) * time.Hour)))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			name := path + "." + v.Type().Field(i).Name
			if !field.CanSet() {
				t.Fatalf("%s is unexported; the round-trip test cannot prove it survives a save", name)
			}
			fillEveryField(t, field, name, seq)
		}
	case reflect.String:
		*seq++
		v.SetString(fmt.Sprintf("value-%d", *seq))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Interface:
		*seq++
		v.Set(reflect.ValueOf(fmt.Sprintf("value-%d", *seq)))
	case reflect.Int, reflect.Int64:
		*seq++
		v.SetInt(int64(*seq))
	case reflect.Float64:
		*seq++
		v.SetFloat(float64(*seq) / 1000)
	case reflect.Slice:
		slice := reflect.MakeSlice(v.Type(), 1, 1)
		fillEveryField(t, slice.Index(0), path+"[0]", seq)
		v.Set(slice)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		key := reflect.New(v.Type().Key()).Elem()
		fillEveryField(t, key, path+"{key}", seq)
		value := reflect.New(v.Type().Elem()).Elem()
		fillEveryField(t, value, path+"{value}", seq)
		m.SetMapIndex(key, value)
		v.Set(m)
	default:
		t.Fatalf("%s has kind %s, which fillEveryField does not know how to fill", path, v.Kind())
	}
}

// TestModelRouterTableRoundTripCoversEveryField pins that every field of a
// router, its config, suggestions block and routing included, has a column. A
// field added without one fails here instead of being silently dropped on
// save. Targets have a table of their own, which TestModelRouterTargetsStore
// covers.
func TestModelRouterTableRoundTripCoversEveryField(t *testing.T) {
	var router ModelRouter
	seq := 0
	v := reflect.ValueOf(&router).Elem()
	for i := 0; i < v.NumField(); i++ {
		if name := v.Type().Field(i).Name; name != "Targets" {
			fillEveryField(t, v.Field(i), "ModelRouter."+name, &seq)
		}
	}
	router.EmbeddingFingerprint = "" // the complexity config's bookkeeping; routers do not store it

	row, err := modelRouterToTable(&router)
	require.NoError(t, err)
	back, err := modelRouterFromTable(row)
	require.NoError(t, err)
	assert.Equal(t, router, *back)
}

// TestModelRouterTableKeepsAbsentAndZeroBlocksApart pins the NULL-anchor rule:
// an absent block (semantic off) and a present block holding only zero values
// must not read back as each other.
func TestModelRouterTableKeepsAbsentAndZeroBlocksApart(t *testing.T) {
	t.Run("absent blocks stay absent", func(t *testing.T) {
		router := ModelRouter{ID: "absent", Name: "Absent", Mode: tables.ModelRouterModeClassifier, ComplexityAnalyzerConfig: ComplexityAnalyzerConfig{Classifier: ComplexityClassifierSemantic}, Routing: &ModelRouterRouting{Scope: "global"}}
		row, err := modelRouterToTable(&router)
		require.NoError(t, err)
		assert.Nil(t, row.SemanticProvider)
		assert.Nil(t, row.DecisionProvider)
		assert.Nil(t, row.DecisionCriteriaJSON)
		assert.Nil(t, row.LLMProvider)
		assert.Nil(t, row.SessionEnabled)
		assert.Nil(t, row.SuggestionsEnabled)
		assert.Nil(t, row.SuggestionsScheduleEvery)
		assert.Nil(t, row.SuggestionsTierClassifier)

		back, err := modelRouterFromTable(row)
		require.NoError(t, err)
		assert.Equal(t, router, *back)
	})

	t.Run("zero-valued blocks stay present", func(t *testing.T) {
		router := ModelRouter{ID: "zero", Name: "Zero", Mode: tables.ModelRouterModeClassifier, ComplexityAnalyzerConfig: ComplexityAnalyzerConfig{
			Classifier: ComplexityClassifierSemantic,
			Semantic:   &ComplexitySemanticConfig{},
			Decision:   &ComplexityDecisionConfig{PreviousMessageCount: bifrost.Ptr(0)},
			LLM:        &ComplexityLLMConfig{},
			Session:    &ComplexitySessionConfig{},
		}, Routing: &ModelRouterRouting{Scope: "global"}}
		row, err := modelRouterToTable(&router)
		require.NoError(t, err)
		back, err := modelRouterFromTable(row)
		require.NoError(t, err)
		assert.Equal(t, router, *back)
		require.NotNil(t, back.Decision.PreviousMessageCount, "an explicit 0 history must not read back as unset")
		assert.Equal(t, 0, *back.Decision.PreviousMessageCount)
	})

	t.Run("unset decision history stays unset", func(t *testing.T) {
		router := ModelRouter{ID: "unset", Name: "Unset", ComplexityAnalyzerConfig: ComplexityAnalyzerConfig{
			Classifier: ComplexityClassifierDecision,
			Decision:   &ComplexityDecisionConfig{Provider: "openai", Model: "gpt-4o-mini"},
		}}
		row, err := modelRouterToTable(&router)
		require.NoError(t, err)
		assert.Nil(t, row.DecisionPreviousMessageCount)
		back, err := modelRouterFromTable(row)
		require.NoError(t, err)
		assert.Nil(t, back.Decision.PreviousMessageCount)
	})
}

// TestModelRouterStoreKeepsZeroSettings guards the gorm default pitfall through
// a real save: a declared column default would replace these zeros on insert.
func TestModelRouterStoreKeepsZeroSettings(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	router := testModelRouter("support", "Support")
	router.Semantic.MinSimilarity = 0
	router.Semantic.CountTowardBudgets = false
	router.Decision = &ComplexityDecisionConfig{PreviousMessageCount: bifrost.Ptr(0), MinConfidence: 0}
	router.Session = &ComplexitySessionConfig{Enabled: false}
	require.NoError(t, store.CreateModelRouter(ctx, router))

	var row tables.TableModelRouter
	require.NoError(t, store.DB().First(&row, "id = ?", "support").Error)
	require.NotNil(t, row.SessionEnabled)
	assert.False(t, *row.SessionEnabled)
	require.NotNil(t, row.DecisionPreviousMessageCount)
	assert.Equal(t, 0, *row.DecisionPreviousMessageCount)
	require.NotNil(t, row.SemanticCountTowardBudgets)
	assert.False(t, *row.SemanticCountTowardBudgets)

	got, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	require.NotNil(t, got.Session)
	assert.False(t, got.Session.Enabled)
	require.NotNil(t, got.Decision.PreviousMessageCount)
	assert.Equal(t, 0, *got.Decision.PreviousMessageCount)
	assert.Zero(t, got.Semantic.MinSimilarity)
}

// TestModelRouterStoreCRUD covers creating, listing, renaming and deleting
// routers, and the identity rules every save enforces.
func TestModelRouterStoreCRUD(t *testing.T) {
	ctx := context.Background()

	t.Run("create, get and list", func(t *testing.T) {
		store := setupRDBTestStore(t)
		// Created in id order, so a created_at tie (falling back to id) cannot reorder them.
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", " Support ")))

		got, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, "Support", got.Name, "identity is trimmed on save")
		assert.Equal(t, testSemanticAnalyzerConfig().Normalized(), got.ComplexityAnalyzerConfig)

		routers, err := store.GetModelRouters(ctx)
		require.NoError(t, err)
		ids := make([]string, 0, len(routers))
		for _, r := range routers {
			ids = append(ids, r.ID)
		}
		assert.Equal(t, []string{"billing", "support"}, ids, "oldest first")
	})

	t.Run("taken id", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
		require.ErrorIs(t, store.CreateModelRouter(ctx, testModelRouter("support", "Other")), ErrAlreadyExists)
	})

	t.Run("identity validation", func(t *testing.T) {
		store := setupRDBTestStore(t)
		unknownMode := testModelRouter("support", "Support")
		unknownMode.Mode = "weighted"
		cases := map[string]*ModelRouter{
			"empty id":     testModelRouter("  ", "Support"),
			"id too long":  testModelRouter(strings.Repeat("a", 256), "Support"),
			"empty name":   testModelRouter("support", " "),
			"reserved id":  testModelRouter("defaults", "Defaults"),
			"unknown mode": unknownMode,
		}
		for name, router := range cases {
			t.Run(name, func(t *testing.T) {
				require.Error(t, store.CreateModelRouter(ctx, router))
			})
		}
		assert.NoError(t, store.CreateModelRouter(ctx, testModelRouter("b2c1a7e0-4c1f-4f7e-9a51-3f0f1d6f8b10", "Support")), "a UUID is a valid id")
		assert.NoError(t, store.CreateModelRouter(ctx, testModelRouter("default", "Default")), "only `defaults`, a route of its own, is reserved")
		assert.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support team/eu", "Support EU")), "an id may carry a space and a slash, as a routing rule's may")
		got, err := store.GetModelRouter(ctx, "support team/eu")
		require.NoError(t, err)
		assert.Equal(t, "support team/eu", got.ID)
	})

	t.Run("invalid config is rejected", func(t *testing.T) {
		store := setupRDBTestStore(t)
		router := testModelRouter("support", "Support")
		router.Keywords.SimpleKeywords = nil
		require.ErrorContains(t, store.CreateModelRouter(ctx, router), "keyword lists must be non-empty")
	})

	t.Run("update and delete need an existing router", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.ErrorIs(t, store.UpdateModelRouter(ctx, testModelRouter("missing", "Missing")), ErrNotFound)
		require.ErrorIs(t, store.DeleteModelRouter(ctx, "missing"), ErrNotFound)
		_, err := store.ResetModelRouter(ctx, "missing", testComplexityAnalyzerConfig())
		require.ErrorIs(t, err, ErrNotFound)

		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
		require.ErrorIs(t, store.UpdateModelRouter(ctx, testModelRouter("missing", "Support")), ErrNotFound)
		require.NoError(t, store.DeleteModelRouter(ctx, "support"))
		_, err = store.GetModelRouter(ctx, "support")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

// TestModelRouterStaysApartFromComplexityConfig pins that routers and the
// Complexity Router page's config never touch each other: saving either
// leaves the other as it was, and a router keeps its own suggestions block.
func TestModelRouterStaysApartFromComplexityConfig(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	cfg := testSemanticAnalyzerConfig()
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, cfg))
	routers, err := store.GetModelRouters(ctx)
	require.NoError(t, err)
	assert.Empty(t, routers, "saving the complexity config creates no router")

	router := testModelRouter("support", "Support")
	router.Suggestions = &ComplexitySuggestionsConfig{Enabled: true, SampleRate: 0.25, LookbackDays: 14, Provider: "openai", Model: "gpt-5"}
	require.NoError(t, store.CreateModelRouter(ctx, router))
	got, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	require.NotNil(t, got.Suggestions, "a router keeps its suggestions block")
	assert.True(t, got.Suggestions.Enabled)
	assert.Equal(t, 0.25, got.Suggestions.SampleRate)
	assert.Equal(t, 14, got.Suggestions.LookbackDays)
	assert.Equal(t, "gpt-5", got.Suggestions.Model)

	update := testModelRouter("support", "Support")
	update.Keywords.SimpleKeywords = []string{"router only"}
	require.NoError(t, store.UpdateModelRouter(ctx, update))
	stored, err := store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, cfg.Normalized().Keywords, stored.Keywords, "saving a router leaves the complexity config as it was")

	require.NoError(t, store.DeleteModelRouter(ctx, "support"))
	stored, err = store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	assert.NotNil(t, stored, "deleting a router leaves the complexity config in place")
}

// TestModelRouterSaveCarriesOverBookkeeping pins that a save that omits the
// config.json hashes or the name hash (every UI save) keeps the stored ones,
// and that a reset keeps them too.
func TestModelRouterSaveCarriesOverBookkeeping(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	seed := testModelRouter("support", "Support")
	hashes, err := GenerateComplexityAnalyzerConfigHashes(&seed.ComplexityAnalyzerConfig)
	require.NoError(t, err)
	seed.ConfigHashes = hashes
	seed.ConfigHash = GenerateModelRouterHash(*seed)
	require.NoError(t, store.CreateModelRouter(ctx, seed))

	update := testModelRouter("support", "Support")
	update.Semantic.EmbeddingModel = "text-embedding-3-large"
	require.NoError(t, store.UpdateModelRouter(ctx, update))

	got, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, "text-embedding-3-large", got.Semantic.EmbeddingModel)
	assert.Equal(t, hashes, got.ConfigHashes)
	assert.Equal(t, seed.ConfigHash, got.ConfigHash)

	defaults := testComplexityAnalyzerConfig()
	defaults.Keywords.SimpleKeywords = []string{"default simple"}
	reset, err := store.ResetModelRouter(ctx, "support", defaults)
	require.NoError(t, err)
	assert.Equal(t, []string{"default simple"}, reset.Keywords.SimpleKeywords)
	assert.Equal(t, "text-embedding-3-large", reset.Semantic.EmbeddingModel, "reset keeps the classifier settings")
	assert.Equal(t, "Support", reset.Name)
	assert.Equal(t, hashes, reset.ConfigHashes)

	got, err = store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, *reset, *got)
}

// TestModelRouterUnreadableRows covers rows this version cannot run, such as
// after a rollback from a newer build: they read as ErrConfigUnreadable, drop
// out of the list without failing it, and a save recovers them.
func TestModelRouterUnreadableRows(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))
	require.NoError(t, store.DB().Model(&tables.TableModelRouter{}).Where("id = ?", "support").
		Update("simple_phrases_json", `["unterminated`).Error)

	_, err := store.GetModelRouter(ctx, "support")
	require.ErrorIs(t, err, ErrConfigUnreadable)

	routers, err := store.GetModelRouters(ctx)
	require.NoError(t, err)
	require.Len(t, routers, 1)
	assert.Equal(t, "billing", routers[0].ID)

	require.NoError(t, store.UpdateModelRouter(ctx, testModelRouter("support", "Support")))
	_, err = store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
}

// setupModelRouterMigrationDB is the exemplar migration's test database pinned
// to one connection: :memory: SQLite gives every pooled connection its own
// database, and these tests open transactions after the migration.
func setupModelRouterMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupComplexityExemplarMigrationDB(t)
	pinSingleConnection(t, db)
	return db
}

// pinSingleConnection keeps a :memory: SQLite database on one connection.
func pinSingleConnection(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
}

// TestMigrationAddModelRouterSuggestionTierColumns pins that a model_routers
// table created before the suggestions tier block gets its columns, that a
// router saved then reads back without a tier block, and that rerunning is a
// no-op.
func TestMigrationAddModelRouterSuggestionTierColumns(t *testing.T) {
	ctx := context.Background()
	db := setupModelRouterMigrationDB(t)
	require.NoError(t, db.AutoMigrate(&tables.TableModelRouter{}))
	// A router saved before tiers were configurable: a suggestions block with
	// no tier block.
	saved := testModelRouter("support", "Support")
	saved.Suggestions = &ComplexitySuggestionsConfig{Enabled: true, SampleRate: 0.25, LookbackDays: 14, Provider: "openai", Model: "gpt-5"}
	row, err := modelRouterToTable(saved)
	require.NoError(t, err)
	require.NoError(t, db.Create(row).Error)
	for _, column := range modelRouterSuggestionTierColumns {
		require.NoError(t, db.Migrator().DropColumn(&tables.TableModelRouter{}, column))
	}

	require.NoError(t, migrationAddModelRouterSuggestionTierColumns(ctx, db, testMigrationLogger))
	for _, column := range modelRouterSuggestionTierColumns {
		assert.True(t, db.Migrator().HasColumn(&tables.TableModelRouter{}, column), column)
	}
	require.NoError(t, migrationAddModelRouterSuggestionTierColumns(ctx, db, testMigrationLogger))

	var stored tables.TableModelRouter
	require.NoError(t, db.First(&stored, "id = ?", "support").Error)
	back, err := modelRouterFromTable(&stored)
	require.NoError(t, err, "a router saved before the migration still reads")
	require.NotNil(t, back.Suggestions)
	assert.True(t, back.Suggestions.Enabled)
	assert.Equal(t, 14, back.Suggestions.LookbackDays)
	assert.Nil(t, back.Suggestions.Tier, "no tier block was saved, so none reads back")
}

// EditModelRouter edits the router as stored when it runs, under its row
// lock: two edits that each add a phrase both land, where reading, editing
// and saving apart would let the second overwrite the first. An edit that
// changes nothing writes nothing.
func TestEditModelRouterBuildsOnTheStoredRouter(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
	stale, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)

	add := func(phrase string) func(*ModelRouter) (bool, error) {
		return func(r *ModelRouter) (bool, error) {
			r.Keywords.SimpleKeywords = append(r.Keywords.SimpleKeywords, phrase)
			return true, nil
		}
	}
	_, err = store.EditModelRouter(ctx, "support", add("first added"))
	require.NoError(t, err)
	// A second edit started from the same read as the first still sees it.
	_, err = store.EditModelRouter(ctx, "support", add("second added"))
	require.NoError(t, err)
	got, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.ElementsMatch(t, append(append([]string{}, stale.Keywords.SimpleKeywords...), "first added", "second added"), got.Keywords.SimpleKeywords)

	var row tables.TableModelRouter
	require.NoError(t, store.DB().First(&row, "id = ?", "support").Error)
	unchanged, err := store.EditModelRouter(ctx, "support", func(*ModelRouter) (bool, error) { return false, nil })
	require.NoError(t, err)
	assert.Equal(t, got.Keywords, unchanged.Keywords)
	var after tables.TableModelRouter
	require.NoError(t, store.DB().First(&after, "id = ?", "support").Error)
	assert.True(t, row.UpdatedAt.Equal(after.UpdatedAt), "nothing is written for an edit that changed nothing")

	refused := errors.New("refused")
	_, err = store.EditModelRouter(ctx, "support", func(r *ModelRouter) (bool, error) {
		r.Keywords.SimpleKeywords = nil
		return false, refused
	})
	require.ErrorIs(t, err, refused)
	_, err = store.EditModelRouter(ctx, "missing", add("x"))
	require.ErrorIs(t, err, ErrNotFound)
}

// TestValidateModelRouterIdentityLimits pins the edges of the identity rules
// the store's identity cases leave out: the longest id and name that still
// save, both counted in characters rather than bytes so a non-Latin one gets
// the full 255, and that an id may carry any character, as a routing rule's
// may.
func TestValidateModelRouterIdentityLimits(t *testing.T) {
	longestID := strings.Repeat("識", 255)   // 765 bytes
	longestName := strings.Repeat("語", 255) // 765 bytes
	cases := []struct {
		name, id, routerName string
		wantErr              string
	}{
		{"id of 255 multibyte characters", longestID, "Support", ""},
		{"id of 256 multibyte characters", longestID + "識", "Support", "model router id must be at most 255 characters, got 256"},
		{"id with dashes, underscores, digits and capitals", "Support_Team-2", "Support", ""},
		{"id with a dot, a slash and a space", "support.team/eu west", "Support", ""},
		{"id with a non-ASCII letter", "süpport", "Support", ""},
		{"name of 255 multibyte characters", "support", longestName, ""},
		{"name of 256 multibyte characters", "support", longestName + "語", "at most 255 characters, got 256"},
		{"limits are checked after trimming", "  " + longestID + "  ", "  " + longestName + "  ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateModelRouterIdentity(&ModelRouter{ID: tc.id, Name: tc.routerName})
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}

	t.Run("id, name and description are trimmed in place", func(t *testing.T) {
		router := ModelRouter{ID: " support ", Name: "\tSupport desk\n", Description: "  Tickets and chat  "}
		require.NoError(t, ValidateModelRouterIdentity(&router))
		assert.Equal(t, "support", router.ID)
		assert.Equal(t, "Support desk", router.Name)
		assert.Equal(t, "Tickets and chat", router.Description)
	})

	t.Run("an empty mode is a classifier, and a mode is read in any letter case", func(t *testing.T) {
		router := ModelRouter{ID: "support", Name: "Support"}
		require.NoError(t, ValidateModelRouterIdentity(&router))
		assert.Equal(t, tables.ModelRouterModeClassifier, router.Mode)
		router.Mode = " Conditional "
		require.NoError(t, ValidateModelRouterIdentity(&router))
		assert.Equal(t, tables.ModelRouterModeConditional, router.Mode)
		router.Mode = "weighted"
		require.ErrorContains(t, ValidateModelRouterIdentity(&router), `invalid mode "weighted"`)
	})

	t.Run("the longest id and name save and read back whole", func(t *testing.T) {
		store := setupRDBTestStore(t)
		ctx := context.Background()
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter(longestID, longestName)))
		got, err := store.GetModelRouter(ctx, longestID)
		require.NoError(t, err)
		assert.Equal(t, longestID, got.ID)
		assert.Equal(t, longestName, got.Name)
	})
}

// TestModelRouterNameTaken pins the one thing the store does about names:
// answer whether another router in a scope has one. A name is taken only
// within its scope and only by another router; surrounding whitespace is not
// part of it, and letter case is. The store's own writes never compare names,
// so two routers can hold the same one, as two routing rules in different
// scopes can.
func TestModelRouterNameTaken(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouterWithRouting("team-support", "Team support",
		ModelRouterRouting{Scope: "team", ScopeID: bifrost.Ptr("team-1")})))

	taken := func(routerID, name, scope string, scopeID *string) bool {
		t.Helper()
		got, err := store.ModelRouterNameTaken(ctx, routerID, name, scope, scopeID)
		require.NoError(t, err)
		return got
	}
	assert.True(t, taken("other", "Support", "global", nil))
	assert.True(t, taken("other", "  Support\t", "", nil), "padding is not part of a name, and no scope is the global one")
	assert.False(t, taken("support", "Support", "global", nil), "a router does not take its own name")
	assert.False(t, taken("other", "support", "global", nil), "letter case is part of a name")
	assert.False(t, taken("other", "Support", "team", bifrost.Ptr("team-1")), "a name is taken within its scope only")
	assert.True(t, taken("other", "Team support", "team", bifrost.Ptr("team-1")))
	assert.False(t, taken("other", "Team support", "team", bifrost.Ptr("team-2")), "another team is another scope")
	assert.False(t, taken("other", "Team support", "customer", bifrost.Ptr("team-1")), "a customer and a team with the same id are different scopes")
	assert.False(t, taken("other", "Team support", "global", nil))

	t.Run("the store's own writes do not compare names", func(t *testing.T) {
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support-copy", "Support")), "a create with a taken name is stored")
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))
		require.NoError(t, store.UpdateModelRouter(ctx, testModelRouter("billing", " Support ")), "a rename to a taken name is stored")

		routers, err := store.GetModelRouters(ctx)
		require.NoError(t, err)
		named := 0
		for _, router := range routers {
			if router.Name == "Support" {
				named++
			}
		}
		assert.Equal(t, 3, named)
	})

	t.Run("an id is compared trimmed", func(t *testing.T) {
		require.ErrorIs(t, store.CreateModelRouter(ctx, testModelRouter(" support ", "Another")), ErrAlreadyExists)
	})
}

// TestModelRouterModeIsSetOnCreate pins that a router's mode is stored when it
// is created and kept by every later save: an update, an edit and a reset do
// not change it, whatever the router they are given says.
func TestModelRouterModeIsSetOnCreate(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	router := testModelRouter("fast-lane", "Fast lane")
	router.Mode = tables.ModelRouterModeConditional
	require.NoError(t, store.CreateModelRouter(ctx, router))
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))

	check := func(when string) {
		t.Helper()
		got, err := store.GetModelRouter(ctx, "fast-lane")
		require.NoError(t, err)
		assert.Equal(t, tables.ModelRouterModeConditional, got.Mode, when)
	}
	check("after create")
	stored, err := store.GetModelRouter(ctx, "fast-lane")
	require.NoError(t, err)
	assert.Equal(t, ComplexityAnalyzerConfig{}, stored.ComplexityAnalyzerConfig,
		"a conditional router classifies nothing, so the classifier config it was created with is not stored")

	update := testModelRouter("fast-lane", "Fast lane, renamed")
	update.Mode = tables.ModelRouterModeClassifier
	require.NoError(t, store.UpdateModelRouter(ctx, update))
	check("after an update that names another mode")

	_, err = store.EditModelRouter(ctx, "fast-lane", func(r *ModelRouter) (bool, error) {
		r.Description, r.Mode = "edited", tables.ModelRouterModeClassifier
		return true, nil
	})
	require.NoError(t, err)
	check("after an edit that names another mode")

	_, err = store.ResetModelRouter(ctx, "fast-lane", testComplexityAnalyzerConfig())
	require.NoError(t, err)
	check("after a reset")

	got, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, tables.ModelRouterModeClassifier, got.Mode, "a router created without a mode is a classifier")

	t.Run("meta is set once and kept by every later save", func(t *testing.T) {
		fromRule := testModelRouter("from-rule", "From a rule")
		fromRule.Meta = &tables.ModelRouterMeta{RoutingRuleIDs: []string{"rule-7", "rule-8"}}
		require.NoError(t, store.CreateModelRouter(ctx, fromRule))
		want := []string{"rule-7", "rule-8"}

		update := testModelRouter("from-rule", "From a rule, renamed")
		require.NoError(t, store.UpdateModelRouter(ctx, update), "an update carries no meta")
		_, err := store.EditModelRouter(ctx, "from-rule", func(r *ModelRouter) (bool, error) {
			r.Description, r.Meta = "edited", nil
			return true, nil
		})
		require.NoError(t, err)
		_, err = store.ResetModelRouter(ctx, "from-rule", testComplexityAnalyzerConfig())
		require.NoError(t, err)

		got, err := store.GetModelRouter(ctx, "from-rule")
		require.NoError(t, err)
		require.NotNil(t, got.Meta)
		assert.Equal(t, want, got.Meta.RoutingRuleIDs)
		raw, err := json.Marshal(got)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "routing_rule_ids", "meta is internal, never in the API")
	})

	t.Run("a router stored before modes existed reads as a classifier", func(t *testing.T) {
		require.NoError(t, store.DB().Model(&tables.TableModelRouter{}).Where("id = ?", "support").UpdateColumn("mode", "").Error)
		got, err := store.GetModelRouter(ctx, "support")
		require.NoError(t, err)
		assert.Equal(t, tables.ModelRouterModeClassifier, got.Mode)
		require.NoError(t, store.SetModelRouterTargets(ctx, "support", testModelRouterTargets()), "its tiered targets still save")
	})

	t.Run("a config.json entry's hash covers a conditional mode", func(t *testing.T) {
		bare := *testModelRouter("support", "Support")
		classifier, conditional := bare, bare
		classifier.Mode = tables.ModelRouterModeClassifier
		conditional.Mode = tables.ModelRouterModeConditional
		assert.Equal(t, GenerateModelRouterHash(bare), GenerateModelRouterHash(classifier), "naming the default mode changes nothing")
		assert.NotEqual(t, GenerateModelRouterHash(bare), GenerateModelRouterHash(conditional))
	})
}

// TestGetModelRoutersListsOldestFirstWithRoutingAndTargets pins what the list
// returns: routers in the order they were created, not by id or name, and each
// one whole, with its own routing and its own targets, as a single read gives.
func TestGetModelRoutersListsOldestFirstWithRoutingAndTargets(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	zeta := testModelRouter("zeta", "Zeta")
	zeta.Routing = &ModelRouterRouting{Enabled: true, Scope: "team", ScopeID: bifrost.Ptr("team-1"), CelExpression: `model == "gpt-4o"`, Priority: 4}
	require.NoError(t, store.CreateModelRouter(ctx, zeta))
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("alpha", "Alpha")))
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("middle", "Middle")))
	require.NoError(t, store.SetModelRouterTargets(ctx, "zeta", testModelRouterTargets()))
	require.NoError(t, store.SetModelRouterTargets(ctx, "alpha", []tables.TableModelRouterTarget{
		{Tier: tables.ModelRouterTierMedium, Provider: bifrost.Ptr("openai"), Model: bifrost.Ptr("gpt-4o"), Weight: 1},
	}))
	// Creation times set apart by hand, in an order no sort by id or name gives.
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"zeta", "alpha", "middle"} {
		require.NoError(t, store.DB().Model(&tables.TableModelRouter{}).Where("id = ?", id).
			UpdateColumn("created_at", created.Add(time.Duration(i)*time.Hour)).Error)
	}

	listed, err := store.GetModelRouters(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 3)
	for i, id := range []string{"zeta", "alpha", "middle"} {
		assert.Equal(t, id, listed[i].ID, "oldest first")
		single, err := store.GetModelRouter(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, *single, listed[i], "the list gives the router a single read gives")
	}
	assert.Equal(t, zeta.Routing, listed[0].Routing)
	assert.Len(t, listed[0].Targets, 3)
	assert.Equal(t, &ModelRouterRouting{Scope: "global", Priority: 0}, listed[1].Routing)
	require.Len(t, listed[1].Targets, 1)
	assert.Equal(t, "gpt-4o", derefStr(listed[1].Targets[0].Model))
	assert.Equal(t, &ModelRouterRouting{Scope: "global", Priority: 1}, listed[2].Routing)
	assert.Empty(t, listed[2].Targets, "a router without targets lists with none")
}

// TestDeleteModelRouterFreesNameAndPriority pins that deleting a router gives
// back everything it held: its priority can go to another router, and its id
// and name can be used for a new router, which starts from scratch with the
// default routing and none of the deleted router's targets.
func TestDeleteModelRouterFreesNameAndPriority(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	held := ModelRouterRouting{Enabled: true, Scope: "global", Priority: 3}

	support := testModelRouter("support", "Support")
	support.Routing = &held
	require.NoError(t, store.CreateModelRouter(ctx, support))
	require.NoError(t, store.SetModelRouterTargets(ctx, "support", testModelRouterTargets()))
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))
	require.ErrorIs(t, store.SetModelRouterRouting(ctx, "billing", held), ErrModelRouterPriorityTaken)

	require.NoError(t, store.DeleteModelRouter(ctx, " support "), "the id is trimmed")
	require.NoError(t, store.SetModelRouterRouting(ctx, "billing", held), "the deleted router's priority is free")

	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")), "its id and name are free")
	got, err := store.GetModelRouter(ctx, "support")
	require.NoError(t, err)
	assert.Equal(t, &ModelRouterRouting{Scope: "global", Priority: 4}, got.Routing, "the new router gets the default routing, not the old one")
	assert.Empty(t, got.Targets, "the deleted router's targets do not come back")
}

// TestModelRouterReadsHonorQueryScope pins that router reads honor a row-visibility QueryScope on
// ctx, as routing rule reads do, so the enterprise DAC wrapper can hide routers whose routing is
// outside the caller's scope. The scope names model_routers columns, so it narrows only the router
// rows: a visible router still comes back with its targets. A ctx without a scope reads every router.
func TestModelRouterReadsHonorQueryScope(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))
	require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
	require.NoError(t, store.SetModelRouterTargets(ctx, "support", testModelRouterTargets()))
	require.NoError(t, store.SetModelRouterRouting(ctx, "billing", ModelRouterRouting{Scope: "team", ScopeID: bifrost.Ptr("team-b")}))
	require.NoError(t, store.SetModelRouterRouting(ctx, "support", ModelRouterRouting{Scope: "team", ScopeID: bifrost.Ptr("team-a")}))

	scoped := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB {
		return db.Where("model_routers.scope = ? AND model_routers.scope_id = ?", "team", "team-a")
	})

	routers, err := store.GetModelRouters(scoped)
	require.NoError(t, err)
	require.Len(t, routers, 1)
	assert.Equal(t, "support", routers[0].ID)
	assert.Len(t, routers[0].Targets, 3, "the scope narrows routers, not their targets")

	got, err := store.GetModelRouter(scoped, "support")
	require.NoError(t, err)
	assert.Len(t, got.Targets, 3)
	_, err = store.GetModelRouter(scoped, "billing")
	assert.ErrorIs(t, err, ErrNotFound, "a router outside the scope reads as not found")

	all, err := store.GetModelRouters(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 2, "a ctx without a scope reads every router")
}
