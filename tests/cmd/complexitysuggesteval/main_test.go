package main

import (
	"hash/fnv"
	"io"
	"math"
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

func TestValidateFloatFlags(t *testing.T) {
	// Finite split and rescue keep their meaning: 0 default, negative off.
	for _, ok := range [][3]float64{{0, 0, 0}, {-1, -1, 1}, {0.8, 0.75, 0.5}, {2, 3, 0}} {
		assert.NoError(t, validateFloatFlags(ok[0], ok[1], ok[2]), "%v", ok)
	}
	for name, bad := range map[string][3]float64{
		"NaN split":           {math.NaN(), 0, 0},
		"+Inf split":          {math.Inf(1), 0, 0},
		"-Inf rescue":         {0, math.Inf(-1), 0},
		"NaN rescue":          {0, math.NaN(), 0},
		"NaN drop-below":      {0, 0, math.NaN()},
		"+Inf drop-below":     {0, 0, math.Inf(1)},
		"negative drop-below": {0, 0, -0.1},
		"drop-below above 1":  {0, 0, 1.1},
	} {
		assert.Error(t, validateFloatFlags(bad[0], bad[1], bad[2]), name)
	}
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

// captureStdout returns what fn prints; purity reports on stdout like the
// rest of the run.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	stdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = stdout }()
	fn()
	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

// labelled builds train items of the given intent:gold pairs, in order, so
// item i is referred to by member ID "i".
func labelled(pairs ...string) []*item {
	var out []*item
	for _, p := range pairs {
		intent, gold, _ := strings.Cut(p, ":")
		out = append(out, &item{Text: p, Count: 1, Intent: intent, Gold: gold})
	}
	return out
}

func cluster(size int, ids ...string) *suggest.Cluster {
	cl := &suggest.Cluster{Size: size}
	for _, id := range ids {
		cl.Members = append(cl.Members, suggest.Member{Request: suggest.Request{ID: id}})
	}
	return cl
}

func TestPurityReport(t *testing.T) {
	heldOut := labelled("a:SIMPLE", "c:COMPLEX")
	heldOut[1].test = true
	heldOut = append(heldOut, &item{Text: "no label", Count: 1})

	cases := []struct {
		name     string
		items    []*item
		clusters []*suggest.Cluster
		want     string
	}{
		{
			name:     "input without types prints nothing",
			items:    []*item{{Text: "x", Count: 1}},
			clusters: []*suggest.Cluster{cluster(1, "0")},
			want:     "",
		},
		{
			name:     "a type with a cluster of its own",
			items:    labelled("a:SIMPLE", "a:SIMPLE"),
			clusters: []*suggest.Cluster{cluster(2, "0", "1")},
			want:     "types 1 · own cluster 1 · absorbed 0 (cross-tier 0) · dropped 0 · member purity 100.0%\n",
		},
		{
			name:     "absorbed by a type of the same tier is not cross-tier",
			items:    labelled("a:SIMPLE", "a:SIMPLE", "a:SIMPLE", "b:SIMPLE"),
			clusters: []*suggest.Cluster{cluster(4, "0", "1", "2", "3")},
			want:     "types 2 · own cluster 1 · absorbed 1 (cross-tier 0) · dropped 0 · member purity 75.0%\n",
		},
		{
			name:     "absorbed by a type of another tier is reported with an example",
			items:    labelled("a:COMPLEX", "a:COMPLEX", "b:SIMPLE"),
			clusters: []*suggest.Cluster{cluster(5, "0", "1", "2")},
			want: "types 2 · own cluster 1 · absorbed 1 (cross-tier 1) · dropped 0 · member purity 66.7%\n" +
				"  cross-tier: b (SIMPLE, 1 train, 1 here) in a (COMPLEX, cluster of 5)\n",
		},
		{
			// Cross-tier, so the example line names which type won the tie.
			name:     "a majority tie goes to the alphabetically first type",
			items:    labelled("b:SIMPLE", "a:COMPLEX"),
			clusters: []*suggest.Cluster{cluster(2, "0", "1")},
			want: "types 2 · own cluster 1 · absorbed 1 (cross-tier 1) · dropped 0 · member purity 50.0%\n" +
				"  cross-tier: b (SIMPLE, 1 train, 1 here) in a (COMPLEX, cluster of 2)\n",
		},
		{
			name:     "a type no cluster kept a member of is dropped",
			items:    labelled("a:SIMPLE", "b:MEDIUM"),
			clusters: []*suggest.Cluster{cluster(1, "0")},
			want:     "types 2 · own cluster 1 · absorbed 0 (cross-tier 0) · dropped 1 · member purity 100.0%\n",
		},
		{
			name:     "unreadable member IDs, held-out and unlabelled items are not counted",
			items:    heldOut,
			clusters: []*suggest.Cluster{cluster(5, "0", "x", "-1", "9", "2")},
			want:     "types 1 · own cluster 1 · absorbed 0 (cross-tier 0) · dropped 0 · member purity 100.0%\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := captureStdout(t, func() { purity(tc.items, tc.clusters) })
			assert.Equal(t, tc.want, got)
		})
	}
}
