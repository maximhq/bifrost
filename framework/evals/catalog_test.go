package evals

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The catalog and the registered evaluators must describe the same set of built-ins.
func TestCatalog_CoversEveryBuiltin(t *testing.T) {
	inCatalog := map[string]CatalogEntry{}
	for _, entry := range Catalog() {
		_, dup := inCatalog[entry.Key]
		require.False(t, dup, "built-in %s is listed twice", entry.Key)
		inCatalog[entry.Key] = entry

		assert.NotEmpty(t, entry.Name, entry.Key)
		assert.NotEmpty(t, entry.Description, entry.Key)
		assert.NotEmpty(t, entry.Category, entry.Key)
		assert.NotEmpty(t, entry.Variables, "%s must say what it reads", entry.Key)
		assert.NotEmpty(t, entry.Scale, entry.Key)
	}
	for key := range builtinAIEvaluators {
		entry, ok := inCatalog[key]
		require.True(t, ok, "AI built-in %s is registered but not in the catalog", key)
		assert.Equal(t, EvaluatorTypeAI, entry.Type)
		assert.True(t, entry.NeedsJudgeModel)
	}
	statistical := []string{BuiltinSQLiteValidation, BuiltinToolCallAccuracy}
	for key := range textMetrics {
		statistical = append(statistical, key)
	}
	for key := range embeddingMetrics {
		statistical = append(statistical, key)
		assert.True(t, inCatalog[key].NeedsEmbedding, key)
	}
	for _, key := range statistical {
		entry, ok := inCatalog[key]
		require.True(t, ok, "statistical built-in %s is registered but not in the catalog", key)
		assert.Equal(t, EvaluatorTypeStatistical, entry.Type)
	}
	assert.Len(t, inCatalog, len(builtinAIEvaluators)+len(statistical), "the catalog lists a built-in that is not registered")
}

// Installing any built-in yields a config the engine accepts and can build.
func TestCatalog_EveryEntryInstallsAndBuilds(t *testing.T) {
	engine := NewEngine(EngineOptions{})
	judge := &scriptedJudge{}
	RegisterBuiltinAIEvaluators(engine, judge.call)
	RegisterStatisticalEvaluators(engine, func(context.Context, string, string, []string) (EmbeddingResponse, error) {
		return EmbeddingResponse{}, nil
	})

	for _, entry := range Catalog() {
		t.Run(entry.Key, func(t *testing.T) {
			config, err := entry.InstallConfig(InstallOptions{Provider: "openai", Model: "some-model"})
			require.NoError(t, err)

			parsed, err := ParseConfig(entry.Type, config)
			require.NoError(t, err)
			assert.Equal(t, entry.LowerIsBetter, parsed.Common().Reversed)
			assert.Equal(t, entry.PassCriteria.EntryLevel.Operator, parsed.Common().PassCriteria.EntryLevel.Operator)

			def := Definition{ID: "installed-" + entry.Key, Type: entry.Type, BuiltinKey: entry.Key, Version: 1, Config: config}
			_, err = engine.Build(def)
			require.NoError(t, err)

			// The variables the catalog promises are exactly what the engine will demand.
			built, err := engine.build(def)
			require.NoError(t, err)
			assert.ElementsMatch(t, entry.Variables, built.required)
		})
	}
}

func TestCatalog_InstallNeedsAModelWhereOneIsUsed(t *testing.T) {
	judged, _ := CatalogEntryFor(BuiltinClarity)
	_, err := judged.InstallConfig(InstallOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "judge provider and model")

	embedded, _ := CatalogEntryFor(BuiltinCosineDistance)
	_, err = embedded.InstallConfig(InstallOptions{Provider: "openai"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "embedding provider and model")

	plain, _ := CatalogEntryFor(BuiltinBLEU)
	config, err := plain.InstallConfig(InstallOptions{})
	require.NoError(t, err, "a metric computed from the text needs no model")
	assert.NotContains(t, string(config), "embedding_provider")
}

func TestCatalog_InstallCanOverridePassCriteria(t *testing.T) {
	entry, ok := CatalogEntryFor(BuiltinToxicity)
	require.True(t, ok)
	strict := atMost(0)
	config, err := entry.InstallConfig(InstallOptions{Provider: "openai", Model: "m", PassCriteria: &strict})
	require.NoError(t, err)
	parsed, err := ParseConfig(EvaluatorTypeAI, config)
	require.NoError(t, err)
	assert.Equal(t, float64(0), parsed.Common().PassCriteria.EntryLevel.Value)

	_, ok = CatalogEntryFor("vibes")
	assert.False(t, ok)
}

// A lower-is-better built-in passes when its score is low.
func TestCatalog_LowerIsBetterBuiltinsPassOnLowScores(t *testing.T) {
	entry, _ := CatalogEntryFor(BuiltinToxicity)
	config, err := entry.InstallConfig(InstallOptions{Provider: "openai", Model: "m"})
	require.NoError(t, err)

	judge := &scriptedJudge{replies: []JudgeResponse{
		{Text: `{"statements":["a","b","c","d","e"]}`},
		{Text: `{"verdicts":[{"statement":"a","verdict":"no","reason":""},{"statement":"b","verdict":"no","reason":""},{"statement":"c","verdict":"no","reason":""},{"statement":"d","verdict":"no","reason":""},{"statement":"e","verdict":"no","reason":""}],"score":0,"reasoning":"Clean."}`},
	}}
	result := newBuiltinEngine(judge).Run(context.Background(),
		Definition{Type: EvaluatorTypeAI, BuiltinKey: BuiltinToxicity, Config: config}, Input{Variables: map[string]string{"output": "all fine"}})
	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.Equal(t, NumberScore(0), result.Score)
	require.NotNil(t, result.Pass)
	assert.True(t, *result.Pass, "zero toxicity passes")
}
