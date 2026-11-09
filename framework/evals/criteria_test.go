package evals

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvaluateEntry(t *testing.T) {
	cases := []struct {
		name      string
		criterion Criterion
		score     Score
		wantPass  bool
		wantOK    bool
	}{
		{"number meets threshold", Criterion{Operator: OperatorGreaterOrEqual, Value: 0.8}, NumberScore(0.8), true, true},
		{"number below threshold", Criterion{Operator: OperatorGreaterOrEqual, Value: 0.8}, NumberScore(0.79), false, true},
		{"strictly greater excludes equal", Criterion{Operator: OperatorGreater, Value: 3.0}, NumberScore(3), false, true},
		{"less than", Criterion{Operator: OperatorLess, Value: 0.2}, NumberScore(0.1), true, true},
		{"less or equal", Criterion{Operator: OperatorLessOrEqual, Value: 0.2}, NumberScore(0.2), true, true},
		{"integer threshold from a Go caller", Criterion{Operator: OperatorGreaterOrEqual, Value: 4}, NumberScore(4), true, true},
		{"numeric string threshold is coerced", Criterion{Operator: OperatorGreaterOrEqual, Value: "0.5"}, NumberScore(0.6), true, true},
		{"numeric string score is coerced", Criterion{Operator: OperatorGreaterOrEqual, Value: 0.5}, StringScore("0.6"), true, true},
		{"ordering on a word fails", Criterion{Operator: OperatorGreater, Value: 1.0}, StringScore("good"), false, true},
		{"ordering on a boolean fails", Criterion{Operator: OperatorGreater, Value: 0.0}, BoolScore(true), false, true},
		{"number equality", Criterion{Operator: OperatorEqual, Value: 1.0}, NumberScore(1), true, true},
		{"number inequality", Criterion{Operator: OperatorNotEqual, Value: 1.0}, NumberScore(0), true, true},
		{"true equals Yes", Criterion{Operator: OperatorEqual, Value: "Yes"}, BoolScore(true), true, true},
		{"false does not equal Yes", Criterion{Operator: OperatorEqual, Value: "Yes"}, BoolScore(false), false, true},
		{"false equals No", Criterion{Operator: OperatorEqual, Value: "No"}, BoolScore(false), true, true},
		{"true is not No", Criterion{Operator: OperatorNotEqual, Value: "No"}, BoolScore(true), true, true},
		{"yes label ignores case", Criterion{Operator: OperatorEqual, Value: "yes"}, BoolScore(true), true, true},
		{"boolean threshold", Criterion{Operator: OperatorEqual, Value: true}, BoolScore(true), true, true},
		{"string equality", Criterion{Operator: OperatorEqual, Value: "good"}, StringScore("good"), true, true},
		{"string inequality", Criterion{Operator: OperatorNotEqual, Value: "good"}, StringScore("bad"), true, true},
		{"is one of hit", Criterion{Operator: OperatorIsOneOf, Value: []any{"good", "great"}}, StringScore("great"), true, true},
		{"is one of miss", Criterion{Operator: OperatorIsOneOf, Value: []string{"good", "great"}}, StringScore("bad"), false, true},
		{"is one of on a number fails", Criterion{Operator: OperatorIsOneOf, Value: []string{"1"}}, NumberScore(1), false, true},
		{"na is not judged", Criterion{Operator: OperatorGreaterOrEqual, Value: 0.8}, NAScore(), false, false},
		{"error is not judged", Criterion{Operator: OperatorGreaterOrEqual, Value: 0.8}, ErrorScore(), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pass, ok := EvaluateEntry(tc.criterion, tc.score)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantPass, pass)
		})
	}
}

// JudgeEntry judges a normalized score in the mapped space: a criterion label that is a
// mapping key becomes the number it maps to, and a criterion written as a number is unchanged.
func TestJudgeEntry(t *testing.T) {
	yesNo := &ScoreNormalizer{Action: NormalizerMapping, Mapping: map[string]float64{"Yes": 1, "No": 0}}
	words := &ScoreNormalizer{Action: NormalizerMapping, Mapping: map[string]float64{"good": 1, "great": 2, "bad": 0}}
	common := func(normalizer *ScoreNormalizer, criterion Criterion) *CommonConfig {
		return &CommonConfig{ScoreNormalizer: normalizer, PassCriteria: PassCriteria{EntryLevel: criterion}}
	}
	cases := []struct {
		name     string
		common   *CommonConfig
		raw      Score
		wantPass bool
		wantOK   bool
	}{
		{"mapped Yes meets = Yes", common(yesNo, Criterion{Operator: OperatorEqual, Value: "Yes"}), BoolScore(true), true, true},
		{"mapped No fails = Yes", common(yesNo, Criterion{Operator: OperatorEqual, Value: "Yes"}), BoolScore(false), false, true},
		{"yes word matches the Yes entry", common(yesNo, Criterion{Operator: OperatorEqual, Value: "yes"}), BoolScore(true), true, true},
		{"mapped No meets != Yes", common(yesNo, Criterion{Operator: OperatorNotEqual, Value: "Yes"}), BoolScore(false), true, true},
		{"mapped Yes meets = true", common(yesNo, Criterion{Operator: OperatorEqual, Value: true}), BoolScore(true), true, true},
		{"mapped No fails = true", common(yesNo, Criterion{Operator: OperatorEqual, Value: true}), BoolScore(false), false, true},
		{"mapped Yes fails != true", common(yesNo, Criterion{Operator: OperatorNotEqual, Value: true}), BoolScore(true), false, true},
		{"mapped No meets = false", common(yesNo, Criterion{Operator: OperatorEqual, Value: false}), BoolScore(false), true, true},
		{"is one of Yes with a mapped Yes", common(yesNo, Criterion{Operator: OperatorIsOneOf, Value: []any{"Yes"}}), BoolScore(true), true, true},
		{"is one of Yes with a mapped No", common(yesNo, Criterion{Operator: OperatorIsOneOf, Value: []any{"Yes"}}), BoolScore(false), false, true},
		{"is one of several mapped words", common(words, Criterion{Operator: OperatorIsOneOf, Value: []string{"good", "great"}}), StringScore("great"), true, true},
		{"is one of misses a mapped word", common(words, Criterion{Operator: OperatorIsOneOf, Value: []string{"good", "great"}}), StringScore("bad"), false, true},
		{"unmapped word stays a word", common(words, Criterion{Operator: OperatorIsOneOf, Value: []string{"good", "meh"}}), StringScore("meh"), true, true},
		{"numeric criterion is unchanged", common(yesNo, Criterion{Operator: OperatorGreaterOrEqual, Value: 1.0}), BoolScore(true), true, true},
		{"numeric equality is unchanged", common(yesNo, Criterion{Operator: OperatorEqual, Value: 0.0}), BoolScore(false), true, true},
		{"no normalizer judges as written", common(nil, Criterion{Operator: OperatorEqual, Value: "Yes"}), BoolScore(true), true, true},
		{"na is not judged", common(yesNo, Criterion{Operator: OperatorEqual, Value: "Yes"}), NAScore(), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pass, ok := JudgeEntry(tc.common, ApplyNormalizer(tc.common.ScoreNormalizer, tc.raw))
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantPass, pass)
		})
	}
}

func TestApplyNormalizer(t *testing.T) {
	cases := []struct {
		name       string
		normalizer *ScoreNormalizer
		in         Score
		want       Score
	}{
		{"nil leaves the score alone", nil, NumberScore(4), NumberScore(4)},
		{"multiply", &ScoreNormalizer{Action: NormalizerMultiply, Value: new(float64(20))}, NumberScore(4), NumberScore(80)},
		{"divide", &ScoreNormalizer{Action: NormalizerDivide, Value: new(float64(5))}, NumberScore(4), NumberScore(0.8)},
		{"add", &ScoreNormalizer{Action: NormalizerAdd, Value: new(float64(1))}, NumberScore(4), NumberScore(5)},
		{"subtract", &ScoreNormalizer{Action: NormalizerSubtract, Value: new(float64(1))}, NumberScore(4), NumberScore(3)},
		{"arithmetic skips a string", &ScoreNormalizer{Action: NormalizerMultiply, Value: new(float64(2))}, StringScore("good"), StringScore("good")},
		{"arithmetic skips a boolean", &ScoreNormalizer{Action: NormalizerMultiply, Value: new(float64(2))}, BoolScore(true), BoolScore(true)},
		{"mapping a string", &ScoreNormalizer{Action: NormalizerMapping, Mapping: map[string]float64{"good": 1, "bad": 0}}, StringScore("good"), NumberScore(1)},
		{"mapping a number", &ScoreNormalizer{Action: NormalizerMapping, Mapping: map[string]float64{"1": 0, "5": 1}}, NumberScore(5), NumberScore(1)},
		{"mapping a fractional number", &ScoreNormalizer{Action: NormalizerMapping, Mapping: map[string]float64{"0.5": 50}}, NumberScore(0.5), NumberScore(50)},
		{"mapping a boolean by label", &ScoreNormalizer{Action: NormalizerMapping, Mapping: map[string]float64{"Yes": 1, "No": 0}}, BoolScore(false), NumberScore(0)},
		{"mapping without an entry leaves the score alone", &ScoreNormalizer{Action: NormalizerMapping, Mapping: map[string]float64{"good": 1}}, StringScore("meh"), StringScore("meh")},
		{"na is never rewritten", &ScoreNormalizer{Action: NormalizerAdd, Value: new(float64(1))}, NAScore(), NAScore()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ApplyNormalizer(tc.normalizer, tc.in))
		})
	}
}

func resultWith(score Score, pass *bool) Result { return Result{Score: score, Pass: pass} }

func TestAggregate(t *testing.T) {
	yes, no := true, false

	t.Run("numbers", func(t *testing.T) {
		agg := Aggregate([]Result{
			resultWith(NumberScore(1), &yes),
			resultWith(NumberScore(0.5), &no),
			resultWith(NumberScore(0), &no),
			resultWith(NAScore(), nil),
			resultWith(ErrorScore(), nil),
		})
		assert.Equal(t, 5, agg.Total)
		assert.Equal(t, 3, agg.Scored, "na and error results are left out of the statistics")
		assert.Equal(t, 1, agg.NA)
		assert.Equal(t, 1, agg.Errors)
		assert.Equal(t, 1, agg.Passed)
		require.NotNil(t, agg.Mean)
		assert.InDelta(t, 0.5, *agg.Mean, 1e-12)
		assert.InDelta(t, 0.5, *agg.Median, 1e-12)
		assert.InDelta(t, 0, *agg.Min, 1e-12)
		assert.InDelta(t, 1, *agg.Max, 1e-12)
		assert.InDelta(t, 100.0/3.0, agg.PassRate, 1e-9)
		assert.Empty(t, agg.Histogram)
	})

	t.Run("booleans average as one and zero", func(t *testing.T) {
		agg := Aggregate([]Result{
			resultWith(BoolScore(true), &yes),
			resultWith(BoolScore(true), &yes),
			resultWith(BoolScore(false), &no),
			resultWith(BoolScore(true), &yes),
		})
		assert.InDelta(t, 0.75, *agg.Mean, 1e-12)
		assert.InDelta(t, 1, *agg.Median, 1e-12)
		assert.InDelta(t, 75, agg.PassRate, 1e-12)
	})

	t.Run("even count median is the midpoint", func(t *testing.T) {
		agg := Aggregate([]Result{
			resultWith(NumberScore(1), nil), resultWith(NumberScore(4), nil),
			resultWith(NumberScore(2), nil), resultWith(NumberScore(3), nil),
		})
		assert.InDelta(t, 2.5, *agg.Median, 1e-12)
	})

	t.Run("huge numbers keep a finite mean and median", func(t *testing.T) {
		agg := Aggregate([]Result{resultWith(NumberScore(1e308), nil), resultWith(NumberScore(1e308), nil)})
		require.NotNil(t, agg.Mean)
		assert.False(t, math.IsInf(*agg.Mean, 0), "mean overflowed: %v", *agg.Mean)
		assert.False(t, math.IsInf(*agg.Median, 0), "median overflowed: %v", *agg.Median)
		assert.InDelta(t, 1e308, *agg.Mean, 1e293)
		assert.InDelta(t, 1e308, *agg.Median, 1e293)
	})

	t.Run("a small score survives large scores that cancel", func(t *testing.T) {
		agg := Aggregate([]Result{resultWith(NumberScore(-1e16), nil), resultWith(NumberScore(1), nil), resultWith(NumberScore(1e16), nil)})
		require.NotNil(t, agg.Mean)
		assert.InDelta(t, 1.0/3.0, *agg.Mean, 1e-12)
	})

	t.Run("strings count per option", func(t *testing.T) {
		agg := Aggregate([]Result{
			resultWith(StringScore("good"), &yes),
			resultWith(StringScore("bad"), &no),
			resultWith(StringScore("good"), &yes),
		})
		assert.Nil(t, agg.Mean, "labels have no mean")
		assert.Equal(t, map[string]int{"good": 2, "bad": 1}, agg.Histogram)
		assert.InDelta(t, 200.0/3.0, agg.PassRate, 1e-9)
	})

	t.Run("nothing scored", func(t *testing.T) {
		agg := Aggregate([]Result{resultWith(NAScore(), nil), resultWith(ErrorScore(), nil)})
		assert.Zero(t, agg.Scored)
		assert.Nil(t, agg.Mean)
		assert.Zero(t, agg.PassRate)
	})
}

func TestEvaluateRun(t *testing.T) {
	yes, no := true, false
	results := []Result{
		resultWith(NumberScore(0.9), &yes),
		resultWith(NumberScore(0.9), &yes),
		resultWith(NumberScore(0.9), &yes),
		resultWith(NumberScore(0.9), &yes),
		resultWith(NumberScore(0.1), &no),
		resultWith(ErrorScore(), nil),
	}
	cases := []struct {
		name      string
		criterion Criterion
		wantPass  bool
	}{
		{"80 percent of scored entries passed", Criterion{Name: RunCriterionQueriesPassed, Operator: OperatorGreaterOrEqual, Value: 80.0}, true},
		{"but not 90 percent", Criterion{Name: RunCriterionQueriesPassed, Operator: OperatorGreaterOrEqual, Value: 90.0}, false},
		{"mean is 0.74", Criterion{Name: RunCriterionMeanScore, Operator: OperatorGreaterOrEqual, Value: 0.7}, true},
		{"mean misses 0.8", Criterion{Name: RunCriterionMeanScore, Operator: OperatorGreaterOrEqual, Value: 0.8}, false},
		{"median is 0.9", Criterion{Name: RunCriterionMedianScore, Operator: OperatorGreaterOrEqual, Value: 0.9}, true},
		{"minimum is 0.1", Criterion{Name: RunCriterionMinimumScore, Operator: OperatorGreaterOrEqual, Value: 0.5}, false},
		{"maximum is 0.9", Criterion{Name: RunCriterionMaximumScore, Operator: OperatorLessOrEqual, Value: 0.9}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict := EvaluateRun(tc.criterion, results)
			assert.Equal(t, tc.wantPass, verdict.Pass)
			assert.Equal(t, 4, verdict.PassCount)
			assert.Equal(t, 5, verdict.TotalCount, "the errored entry is not counted")
		})
	}

	t.Run("exactly 58 percent passes queries_passed >= 58", func(t *testing.T) {
		var mixed []Result
		for i := range 50 {
			if i < 29 {
				mixed = append(mixed, resultWith(NumberScore(1), &yes))
			} else {
				mixed = append(mixed, resultWith(NumberScore(0), &no))
			}
		}
		verdict := EvaluateRun(Criterion{Name: RunCriterionQueriesPassed, Operator: OperatorGreaterOrEqual, Value: 58.0}, mixed)
		assert.True(t, verdict.Pass, "29 of 50 is 58 percent")
		assert.Equal(t, 29, verdict.PassCount)
		assert.Equal(t, 50, verdict.TotalCount)
	})

	t.Run("mean_score > 0 passes when large scores cancel around a small one", func(t *testing.T) {
		cancelling := []Result{resultWith(NumberScore(-1e16), nil), resultWith(NumberScore(1), nil), resultWith(NumberScore(1e16), nil)}
		verdict := EvaluateRun(Criterion{Name: RunCriterionMeanScore, Operator: OperatorGreater, Value: 0.0}, cancelling)
		assert.True(t, verdict.Pass, "the mean is 1/3")
	})

	t.Run("a run with nothing scored fails", func(t *testing.T) {
		verdict := EvaluateRun(Criterion{Name: RunCriterionQueriesPassed, Operator: OperatorGreaterOrEqual, Value: 0.0}, []Result{resultWith(NAScore(), nil)})
		assert.False(t, verdict.Pass)
		assert.Zero(t, verdict.TotalCount)
	})

	t.Run("a mean criterion over labels fails", func(t *testing.T) {
		verdict := EvaluateRun(Criterion{Name: RunCriterionMeanScore, Operator: OperatorGreaterOrEqual, Value: 0.0}, []Result{resultWith(StringScore("good"), &yes)})
		assert.False(t, verdict.Pass)
	})
}
