package configstore

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testModelRouter returns a valid non-default router with a semantic block.
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
// router, its config and suggestions block included, has a column. A field
// added to the config without one fails here instead of being silently
// dropped on save.
func TestModelRouterTableRoundTripCoversEveryField(t *testing.T) {
	var router ModelRouter
	seq := 0
	fillEveryField(t, reflect.ValueOf(&router).Elem(), "ModelRouter", &seq)
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
		router := ModelRouter{ID: "absent", Name: "Absent", ComplexityAnalyzerConfig: ComplexityAnalyzerConfig{Classifier: ComplexityClassifierSemantic}}
		row, err := modelRouterToTable(&router)
		require.NoError(t, err)
		assert.Nil(t, row.SemanticProvider)
		assert.Nil(t, row.DecisionProvider)
		assert.Nil(t, row.DecisionCriteriaJSON)
		assert.Nil(t, row.LLMProvider)
		assert.Nil(t, row.SessionEnabled)

		back, err := modelRouterFromTable(row)
		require.NoError(t, err)
		assert.Equal(t, router, *back)
	})

	t.Run("zero-valued blocks stay present", func(t *testing.T) {
		router := ModelRouter{ID: "zero", Name: "Zero", ComplexityAnalyzerConfig: ComplexityAnalyzerConfig{
			Classifier: ComplexityClassifierSemantic,
			Semantic:   &ComplexitySemanticConfig{},
			Decision:   &ComplexityDecisionConfig{PreviousMessageCount: bifrost.Ptr(0)},
			LLM:        &ComplexityLLMConfig{},
			Session:    &ComplexitySessionConfig{},
		}}
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

	t.Run("taken id or name", func(t *testing.T) {
		store := setupRDBTestStore(t)
		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("support", "Support")))
		require.ErrorIs(t, store.CreateModelRouter(ctx, testModelRouter("support", "Other")), ErrAlreadyExists)
		require.ErrorIs(t, store.CreateModelRouter(ctx, testModelRouter("other", "Support")), ErrAlreadyExists)

		require.NoError(t, store.CreateModelRouter(ctx, testModelRouter("billing", "Billing")))
		rename := testModelRouter("billing", "Support")
		require.ErrorIs(t, store.UpdateModelRouter(ctx, rename), ErrAlreadyExists)
	})

	t.Run("identity validation", func(t *testing.T) {
		store := setupRDBTestStore(t)
		cases := map[string]*ModelRouter{
			"empty id":        testModelRouter("  ", "Support"),
			"id with a space": testModelRouter("support team", "Support"),
			"id with a slash": testModelRouter("support/team", "Support"),
			"id too long":     testModelRouter(fmt.Sprintf("%065d", 0), "Support"),
			"empty name":      testModelRouter("support", " "),
		}
		for name, router := range cases {
			t.Run(name, func(t *testing.T) {
				require.Error(t, store.CreateModelRouter(ctx, router))
			})
		}
		assert.NoError(t, store.CreateModelRouter(ctx, testModelRouter("b2c1a7e0-4c1f-4f7e-9a51-3f0f1d6f8b10", "Support")), "a UUID is a valid id")
		assert.NoError(t, store.CreateModelRouter(ctx, testModelRouter("default", "Default")), "no id or name is reserved")
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
		require.ErrorIs(t, store.UpdateModelRouter(ctx, testModelRouter("missing", "Support")), ErrNotFound,
			"a missing router is not found even when the name it asks for is taken")
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
