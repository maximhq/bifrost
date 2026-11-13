package main

import (
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func unit(dim, i int) []float32 {
	v := make([]float32, dim)
	v[i] = 1
	return v
}

func TestRouteFloorBoundaryAndAbstention(t *testing.T) {
	set := []suggest.Phrase{{Tier: "SIMPLE", Vector: []float32{1, 0}}, {Tier: "COMPLEX", Vector: []float32{1, 0}}}
	i, ok := route([]float32{1, 0}, set, 1)
	assert.True(t, ok, "a similarity equal to the floor routes")
	assert.Equal(t, 0, i, "ties keep the first phrase")

	_, ok = route([]float32{1, 0}, set, 1.0001)
	assert.False(t, ok, "below the floor abstains")

	i, ok = route([]float32{1, 0}, nil, 0)
	assert.False(t, ok)
	assert.Equal(t, -1, i)
}

func TestScoreSplitsDefaultAndSuggestedPhrases(t *testing.T) {
	ex := complexity.DefaultComplexityExemplars()
	nD := len(ex.SimpleKeywords) + len(ex.MediumKeywords) + len(ex.ComplexKeywords)
	dim := nD + 3
	var set []suggest.Phrase
	for i := range nD {
		set = append(set, suggest.Phrase{Tier: "SIMPLE", Vector: unit(dim, i)})
	}
	set = append(set,
		suggest.Phrase{Tier: "MEDIUM", Vector: unit(dim, nD)},
		suggest.Phrase{Tier: "COMPLEX", Vector: unit(dim, nD+1)})
	test := []*item{
		{Gold: "SIMPLE", Count: 2, vec: unit(dim, 0)},    // default phrase, correct
		{Gold: "MEDIUM", Count: 3, vec: unit(dim, nD)},   // suggested, correct
		{Gold: "COMPLEX", Count: 1, vec: unit(dim, nD)},  // suggested, wrong
		{Gold: "SIMPLE", Count: 4, vec: unit(dim, nD+1)}, // suggested, wrong
		{Gold: "SIMPLE", Count: 5, vec: unit(dim, nD+2)}, // below the floor
		{Gold: "", Count: 100, vec: unit(dim, 0)},        // unlabelled, ignored
	}
	r := score(test, set, 0.4)
	assert.Equal(t, 15, r.requests)
	assert.Equal(t, 10, r.answered)
	assert.Equal(t, 5, r.correct)
	assert.Equal(t, 5, r.abstained)
	assert.Equal(t, 8, r.suggestedMatched)
	assert.Equal(t, 3, r.suggestedCorrect)
	// Best single tier per suggested phrase: MEDIUM phrase 3 of 4, COMPLEX phrase 4 of 4.
	assert.InDelta(t, 7.0/8.0, r.oracle, 1e-9)
}

func writeJSONL(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in.jsonl")
	require.NoError(t, os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	return p
}

func byText(items []*item) map[string]*item {
	m := map[string]*item{}
	for _, it := range items {
		m[it.Text] = it
	}
	return m
}

func TestLoadDeduplicatesAndAggregatesCounts(t *testing.T) {
	p := writeJSONL(t,
		`{"text":"hello"}`,
		``,
		`{"text":"  hello  ","count":2}`,
		`{"text":"   "}`,
		`{"text":"other","count":5}`,
	)
	items, err := load(p, 25)
	require.NoError(t, err)
	require.Len(t, items, 2)
	got := byText(items)
	assert.Equal(t, 3, got["hello"].Count)
	assert.Equal(t, 5, got["other"].Count)
}

func TestLoadNormalizesGold(t *testing.T) {
	p := writeJSONL(t,
		`{"text":"a","gold":" medium "}`,
		`{"text":"b","gold":"hard"}`,
		`{"text":"c"}`,
		`{"text":"c","gold":"simple"}`,
		`{"text":"d","gold":"COMPLEX"}`,
		`{"text":"d","gold":"SIMPLE"}`,
	)
	items, err := load(p, 25)
	require.NoError(t, err)
	got := byText(items)
	assert.Equal(t, complexity.TierMedium, got["a"].Gold)
	assert.Equal(t, "", got["b"].Gold, "an invalid gold is cleared so the judge labels it")
	assert.Equal(t, complexity.TierSimple, got["c"].Gold, "a later gold fills an empty one")
	assert.Equal(t, complexity.TierComplex, got["d"].Gold, "a later gold never overwrites an existing one")
	assert.Equal(t, 2, got["c"].Count)
}

func TestLoadSplitIsStableFNV32a(t *testing.T) {
	var lines []string
	for _, s := range []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "theta"} {
		lines = append(lines, `{"text":"`+s+`"}`)
	}
	p := writeJSONL(t, lines...)
	first, err := load(p, 40)
	require.NoError(t, err)
	again, err := load(p, 40)
	require.NoError(t, err)
	for i, it := range first {
		h := fnv.New32a()
		h.Write([]byte(it.Text))
		assert.Equal(t, int(h.Sum32()%100) < 40, it.test, it.Text)
		assert.Equal(t, it.test, again[i].test, it.Text)
	}
	none, err := load(p, 0)
	require.NoError(t, err)
	all, err := load(p, 100)
	require.NoError(t, err)
	for i := range none {
		assert.False(t, none[i].test)
		assert.True(t, all[i].test)
	}
}

func TestWriteOutputsReportsWriteFailure(t *testing.T) {
	dir := t.TempDir()
	// A directory where heldout.jsonl should be makes the create fail.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "heldout.jsonl"), 0o755))
	err := writeOutputs(dir, nil, []*item{{Text: "x", Gold: "SIMPLE", Count: 1, vec: []float32{1}}}, nil, 0.4)
	assert.Error(t, err)
}

func TestWriteOutputsHeldoutKeysAreSorted(t *testing.T) {
	dir := t.TempDir()
	set := []suggest.Phrase{{Tier: "SIMPLE", Text: "p", Vector: []float32{1}}}
	require.NoError(t, writeOutputs(dir, nil, []*item{{Text: "x", Gold: "SIMPLE", Count: 1, vec: []float32{1}}}, set, 0.4))
	data, err := os.ReadFile(filepath.Join(dir, "heldout.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, `{"count":1,"gold":"SIMPLE","routed":"SIMPLE","text":"x","via":"p"}`+"\n", string(data))
}
