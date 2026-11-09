package evals

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuiltin_Bias(t *testing.T) {
	result, requests := runBuiltin(t, BuiltinBias, map[string]string{"output": "Government meddling quashes innovation. The sky is blue. Water is wet."},
		`{"statements":["Government meddling quashes innovation.","The sky is blue.","Water is wet."]}`,
		`{"verdicts":[{"statement":"a","verdict":"yes","reason":"political"},{"statement":"b","verdict":"no","reason":""},{"statement":"c","verdict":" NO ","reason":""}],"score":0.33,"reasoning":"One statement shows political bias."}`)

	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.InDelta(t, 1.0/3.0, *result.Score.Number, 1e-12, "the score is computed from the verdicts, not taken from the judge's own number")
	assert.Equal(t, "One statement shows political bias.", result.Reasoning)
	assert.Equal(t, 30, result.Usage.TotalTokens, "usage adds up across both calls")

	require.Len(t, requests, 2)
	assert.Contains(t, requests[0].User, "Government meddling quashes innovation.")
	assert.Contains(t, requests[1].User, `["Government meddling quashes innovation.","The sky is blue.","Water is wet."]`, "the extracted statements feed the verdict step")
	assert.NotContains(t, requests[1].User, "{{")
}

func TestBuiltin_BiasWithNoStatements(t *testing.T) {
	result, requests := runBuiltin(t, BuiltinBias, map[string]string{"output": "..."}, `{"statements":[]}`)
	require.Equal(t, ScoreStatusOK, result.Score.Status)
	assert.Equal(t, NumberScore(0), result.Score)
	assert.Equal(t, emptyOutputReason, result.Reasoning)
	assert.Len(t, requests, 1, "no verdict call is made for nothing")
}

func TestBuiltin_Toxicity(t *testing.T) {
	result, requests := runBuiltin(t, BuiltinToxicity, map[string]string{"output": "You are an idiot. Have a nice day."},
		`{"statements":["You are an idiot.","Have a nice day."]}`,
		`{"verdicts":[{"statement":"a","verdict":"yes","reason":"insult"},{"statement":"b","verdict":"no","reason":""}],"score":0.5,"reasoning":"One insult."}`)
	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.InDelta(t, 0.5, *result.Score.Number, 1e-12)
	assert.Contains(t, requests[0].User, "You are an idiot.")

	blank, requests := runBuiltin(t, BuiltinToxicity, map[string]string{"output": "   "})
	assert.Equal(t, NumberScore(0), blank.Score)
	assert.Equal(t, "The output does not have any text hence is not toxic in nature.", blank.Reasoning)
	assert.Empty(t, requests)
}

func TestBuiltin_Faithfulness(t *testing.T) {
	variables := map[string]string{
		"input":   "When can I return an item?",
		"output":  "Returns are accepted within 30 days. Shipping is free. Dr. Lee approved it.",
		"context": `["Returns are accepted within 30 days of delivery.","Return shipping costs $5."]`,
		"history": `[{"role":"system","content":"Be concise."}]`,
	}
	result, requests := runBuiltin(t, BuiltinFaithfulness, variables,
		`{"statements":["Returns are accepted within 30 days.","Shipping is free.","Dr. Lee approved it."]}`,
		`{"verdicts":[{"statement":"a","verdict":"yes","reason":"stated"},{"statement":"b","verdict":"no","reason":"context says $5"},{"statement":"c","verdict":"idk","reason":"not mentioned"}],"score":2,"reasoning":"One claim contradicts the context."}`)

	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.InDelta(t, 2.0/3.0, *result.Score.Number, 1e-12, "only an explicit no counts against the output")
	assert.Equal(t, "One claim contradicts the context.", result.Reasoning)

	require.Len(t, requests, 2)
	// The output is split into sentences for the statement step; the title does not split one.
	assert.Contains(t, requests[0].User, `["Returns are accepted within 30 days. ","Shipping is free. ","Dr. Lee approved it."]`)
	assert.Contains(t, requests[0].User, "When can I return an item?")
	// Context chunks are joined with a comma, as Maxim joins them.
	assert.Contains(t, requests[1].User, "Returns are accepted within 30 days of delivery.,Return shipping costs $5.")
	assert.Contains(t, requests[1].User, "Be concise.")
	assert.NotContains(t, requests[1].User, "{{")
}

func TestBuiltin_FaithfulnessEdgeCases(t *testing.T) {
	variables := map[string]string{"input": "q", "output": "ok", "context": "plain text context"}

	noClaims, requests := runBuiltin(t, BuiltinFaithfulness, variables, `{"statements":[]}`)
	require.Equal(t, ScoreStatusOK, noClaims.Score.Status)
	assert.Equal(t, NumberScore(0), noClaims.Score)
	assert.Contains(t, noClaims.Reasoning, "does not make significant claims")
	assert.Len(t, requests, 1)

	_, requests = runBuiltin(t, BuiltinFaithfulness, variables,
		`{"statements":["ok"]}`, `{"verdicts":[{"statement":"ok","verdict":"yes","reason":""}],"score":1,"reasoning":"fine"}`)
	assert.Contains(t, requests[1].User, "plain text context", "a context that is not a JSON list is one chunk")
	assert.Contains(t, requests[1].User, "History:\nNone", "a missing history reads as None, which is what the prompt was written against")
}

// Precision rewards relevant chunks ranked first: the mean of precision@k at each
// relevant position.
func TestBuiltin_ContextPrecision(t *testing.T) {
	variables := map[string]string{"input": "Who won?", "expected_output": "Einstein", "context": `["Einstein won.","There was a cat.","He won in 1921."]`}
	result, requests := runBuiltin(t, BuiltinContextPrecision, variables,
		`{"verdicts":[{"verdict":"yes","reason":"names the winner"},{"verdict":"no","reason":"irrelevant"},{"verdict":"yes","reason":"gives the year"}]}`,
		`{"reason":"The score is 0.83 because an irrelevant chunk is ranked second."}`)

	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.InDelta(t, (1.0+2.0/3.0)/2.0, *result.Score.Number, 1e-12)
	assert.Equal(t, "The score is 0.83 because an irrelevant chunk is ranked second.", result.Reasoning)

	require.Len(t, requests, 2)
	assert.Contains(t, requests[0].User, `["Einstein won.","There was a cat.","He won in 1921."]`)
	assert.Contains(t, requests[1].User, "0.83")
	assert.Contains(t, requests[1].User, `{"reasons":"irrelevant","verdict":"no"}`)

	none, _ := runBuiltin(t, BuiltinContextPrecision, variables, `{"verdicts":[{"verdict":"no","reason":"x"}]}`, `{"reason":"Nothing relevant."}`)
	assert.Equal(t, NumberScore(0), none.Score)
}

func TestBuiltin_ContextRecall(t *testing.T) {
	variables := map[string]string{"expected_output": "Einstein won in 1921 for the photoelectric effect.", "context": `["Einstein won in 1921."]`}
	result, requests := runBuiltin(t, BuiltinContextRecall, variables,
		`{"verdicts":[{"verdict":"yes","reason":"1st node states it"},{"verdict":"no","reason":"photoelectric effect is not in the context"}]}`,
		`{"reason":"Half of the expected output is supported."}`)

	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.InDelta(t, 0.5, *result.Score.Number, 1e-12)
	assert.Equal(t, "Half of the expected output is supported.", result.Reasoning)
	assert.Contains(t, requests[1].User, `["1st node states it"]`)
	assert.Contains(t, requests[1].User, `["photoelectric effect is not in the context"]`)
	assert.Contains(t, requests[1].User, "0.50")
}

// Relevance judges each retrieved chunk in its own call and pools the verdicts.
func TestBuiltin_ContextRelevance(t *testing.T) {
	variables := map[string]string{"input": "What did Einstein win?", "context": `["Einstein won the Nobel Prize. There was a cat.","He was born in 1879."]`}
	result, requests := runBuiltin(t, BuiltinContextRelevance, variables,
		`{"verdicts":[{"statement":"Einstein won the Nobel Prize.","verdict":"yes"},{"statement":"There was a cat.","verdict":"no","reason":"unrelated"}]}`,
		`{"verdicts":[{"statement":"He was born in 1879.","verdict":"yes"}]}`,
		`{"reason":"The score is 0.67 because one statement is unrelated."}`)

	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.InDelta(t, 2.0/3.0, *result.Score.Number, 1e-12)
	require.Len(t, requests, 3, "one call per chunk, then the reason")
	assert.Contains(t, requests[0].User, "There was a cat.")
	assert.NotContains(t, requests[0].User, "born in 1879", "each call sees one chunk")
	assert.Contains(t, requests[1].User, "born in 1879")
	assert.Contains(t, requests[2].User, `["unrelated"]`)
	assert.Contains(t, requests[2].User, `["Einstein won the Nobel Prize.","He was born in 1879."]`)
	assert.Contains(t, requests[2].User, "0.67")
}

func TestPythonFloat(t *testing.T) {
	assert.Equal(t, "1.0", pythonFloat(1))
	assert.Equal(t, "0.0", pythonFloat(0))
	assert.Equal(t, "0.5", pythonFloat(0.5))
	assert.Equal(t, "0.67", pythonFloat(2.0/3.0))
}

func TestBuiltin_OutputRelevance(t *testing.T) {
	variables := map[string]string{"input": "What is the refund window?", "output": "It is 30 days. Our CEO likes golf."}
	result, requests := runBuiltin(t, BuiltinOutputRelevance, variables,
		`{"statements":["It is 30 days.","Our CEO likes golf."]}`,
		`{"verdicts":[{"verdict":"yes","reason":""},{"verdict":"no","reason":"golf is unrelated to refunds"}]}`,
		"The score is 0.50 because the reply drifts into the CEO's hobbies.")

	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.InDelta(t, 0.5, *result.Score.Number, 1e-12)
	assert.Equal(t, "The score is 0.50 because the reply drifts into the CEO's hobbies.", result.Reasoning, "the reason step answers in prose, not JSON")
	require.Len(t, requests, 3)
	assert.Nil(t, requests[2].Schema)
	assert.Contains(t, requests[2].User, `["golf is unrelated to refunds"]`)
	assert.Contains(t, requests[1].User, `["It is 30 days.","Our CEO likes golf."]`)

	// With nothing to judge, nothing is off topic.
	empty, requests := runBuiltin(t, BuiltinOutputRelevance, variables, `{"statements":[]}`, "The score is 1.00, great job.")
	assert.Equal(t, NumberScore(1), empty.Score)
	assert.Len(t, requests, 2, "the verdict step is skipped")
}

func TestBuiltin_Summarization(t *testing.T) {
	variables := map[string]string{"input": "The long original text.", "output": "The short summary."}
	result, requests := runBuiltin(t, BuiltinSummarization, variables,
		`{"statements":["claim one","claim two","claim three","claim four"]}`,
		`{"keypoints":["point a","point b"]}`,
		`{"questions":["Q1?","Q2?","Q3?","Q4?"]}`,
		`{"answers":["yes","yes","no","yes"]}`,
		`{"answers":["yes","no","yes","idk"]}`,
		`{"verdicts":[{"verdict":"yes","reason":""},{"verdict":"yes","reason":""},{"verdict":"no","reason":"contradicts the text"},{"verdict":"idk","reason":"not in the text"}]}`,
		"The score is 0.33 because the summary misses a key point.")

	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	// Alignment: 2 of 4 claims supported = 0.5. Coverage: of the 3 questions the text
	// answers yes, the summary answers yes to 1 = 0.33. The score is the lower.
	assert.InDelta(t, 1.0/3.0, *result.Score.Number, 1e-12)
	assert.Equal(t, "The score is 0.33 because the summary misses a key point.", result.Reasoning)
	assert.Equal(t, 105, result.Usage.TotalTokens, "seven calls")

	require.Len(t, requests, 7)
	assert.Contains(t, requests[0].User, "The short summary.", "claims come from the summary")
	assert.Contains(t, requests[1].User, "The long original text.", "key points come from the text")
	assert.Contains(t, requests[2].User, `["point a","point b"]`)
	assert.Contains(t, requests[3].User, "The long original text.")
	assert.Contains(t, requests[4].User, "The short summary.")
	assert.Contains(t, requests[5].User, `["claim one","claim two","claim three","claim four"]`)
	reason := requests[6].User
	assert.Contains(t, reason, "0.33")
	assert.Contains(t, reason, `["contradicts the text"]`)
	assert.Contains(t, reason, `["not in the text"]`)
	assert.Contains(t, reason, `["Q2?"]`, "only a question the summary answers no to is listed as missed")
	assert.Nil(t, requests[6].Schema)
}

func TestBuiltin_SummarizationMismatchedAnswersIsAnError(t *testing.T) {
	result, _ := runBuiltin(t, BuiltinSummarization, map[string]string{"input": "text", "output": "summary"},
		`{"statements":["c"]}`, `{"keypoints":["k"]}`, `{"questions":["Q1?","Q2?"]}`,
		`{"answers":["yes","no"]}`, `{"answers":["yes"]}`)
	assert.Equal(t, ScoreStatusError, result.Score.Status)
	assert.Contains(t, result.Reasoning, "different number")
	require.NotNil(t, result.Usage)
	assert.Equal(t, 75, result.Usage.TotalTokens, "the five calls made before the failure are still reported")
}

func TestContextChunks(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, contextChunks(`["a","b"]`))
	assert.Equal(t, []string{"plain text"}, contextChunks("plain text"))
	assert.Equal(t, []string{"[not json"}, contextChunks("[not json"))
	assert.Empty(t, contextChunks("   "))
	assert.Empty(t, contextChunks("[]"))
}

// Every built-in the catalog promises is registered and builds.
func TestBuiltin_AllRegistered(t *testing.T) {
	keys := []string{
		BuiltinClarity, BuiltinConciseness, BuiltinSQLCorrectness, BuiltinAgentTrajectory, BuiltinTaskSuccess,
		BuiltinStepUtility, BuiltinToolSelection, BuiltinStepCompletionStrictMatch, BuiltinStepCompletionUnorderedMatch,
		BuiltinCodeCompletion, BuiltinCodeLanguageSelection, BuiltinFollowupCheck,
		BuiltinBias, BuiltinToxicity, BuiltinFaithfulness, BuiltinContextPrecision, BuiltinContextRecall,
		BuiltinContextRelevance, BuiltinOutputRelevance, BuiltinSummarization,
		BuiltinFactuality, BuiltinConsistency, BuiltinPIIDetection, BuiltinPromptInjection, BuiltinHallucination,
	}
	engine := newBuiltinEngine(&scriptedJudge{})
	for _, key := range keys {
		_, err := engine.Build(builtinDefinition(key))
		assert.NoError(t, err, key)
	}
	assert.Len(t, builtinAIEvaluators, len(keys), "a built-in was added or removed without updating this list")
}

func TestBuiltin_Factuality(t *testing.T) {
	result, requests := runBuiltin(t, BuiltinFactuality, map[string]string{"output": "Refunds take 5 days.", "context": "Refunds are processed within 5 business days."},
		`{"score": 4, "reasoning": "Mostly supported."}`)
	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.InDelta(t, 0.8, *result.Score.Number, 1e-12, "a 1 to 5 rating is reported divided by five")
	assert.Equal(t, "Mostly supported.", result.Reasoning)

	require.Len(t, requests, 1)
	assert.Contains(t, requests[0].System, "Use a scale of 1-5 to give a factuality score.")
	assert.Equal(t, "Here is the $generated_output=Refunds take 5 days.\n$relavant_context=Refunds are processed within 5 business days.", requests[0].User)
}

func TestBuiltin_Consistency(t *testing.T) {
	result, requests := runBuiltin(t, BuiltinConsistency, map[string]string{"output": `["Paris.","It is Paris.","Lyon."]`},
		`{"score": 3, "reasoning": "Two agree, one differs."}`)
	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.InDelta(t, 0.6, *result.Score.Number, 1e-12)

	prompt := requests[0].User
	assert.Contains(t, prompt, "You will be provided 3 outputs")
	assert.Contains(t, prompt, "**Output 1**: Paris.")
	assert.Contains(t, prompt, "**Output 3**: Lyon.")
	assert.Contains(t, prompt, "all 3 outputs are highly incompatible")
	assert.NotContains(t, prompt, "{{n}}", "the count Maxim leaves as a literal placeholder is filled in")

	singleQuoted, _ := runBuiltin(t, BuiltinConsistency, map[string]string{"output": `['a','b']`}, `{"score": 5, "reasoning": "Same."}`)
	assert.Equal(t, ScoreStatusOK, singleQuoted.Score.Status, "the single-quoted form Maxim tolerates is accepted")

	for name, output := range map[string]string{"one output": `["only one"]`, "plain text": "just a reply"} {
		t.Run(name, func(t *testing.T) {
			result, requests := runBuiltin(t, BuiltinConsistency, map[string]string{"output": output})
			assert.Equal(t, ScoreStatusError, result.Score.Status)
			assert.Contains(t, result.Reasoning, "at least two outputs")
			assert.Empty(t, requests)
		})
	}
}

func TestBuiltin_Detectors(t *testing.T) {
	cases := []struct {
		key, variable, phrase string
	}{
		{BuiltinPIIDetection, "output", "personally identifiable information"},
		{BuiltinPromptInjection, "input", "prompt injection or jailbreak attempt"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			found, requests := runBuiltin(t, tc.key, map[string]string{tc.variable: "the text under review"},
				`{"detected": true, "categories": ["email address","phone number"], "reasoning": "Two contact details appear."}`)
			require.Equal(t, ScoreStatusOK, found.Score.Status, found.Reasoning)
			assert.Equal(t, NumberScore(1), found.Score, "1 means something was detected")
			assert.Equal(t, "Two contact details appear.\n\nDetected: email address, phone number", found.Reasoning)
			assert.Contains(t, requests[0].User, tc.phrase)
			assert.Contains(t, requests[0].User, "the text under review")
			assert.NotContains(t, requests[0].User, "{{")

			clean, _ := runBuiltin(t, tc.key, map[string]string{tc.variable: "hello"}, `{"detected": false, "categories": [], "reasoning": "Nothing found."}`)
			assert.Equal(t, NumberScore(0), clean.Score)
			assert.Equal(t, "Nothing found.", clean.Reasoning)

			blank, requests := runBuiltin(t, tc.key, map[string]string{tc.variable: "  "})
			assert.Equal(t, NumberScore(0), blank.Score)
			assert.Empty(t, requests, "empty text is not sent to a model")

			vague, _ := runBuiltin(t, tc.key, map[string]string{tc.variable: "hello"}, `{"reasoning": "Maybe."}`)
			assert.Equal(t, ScoreStatusError, vague.Score.Status, "a reply without a verdict is not read as clean")
		})
	}
}

// Hallucination is the share of retrieved chunks the output contradicts. Lower is
// better: 0 means the output contradicts nothing it was given.
func TestBuiltin_Hallucination(t *testing.T) {
	variables := map[string]string{
		"output":  "Einstein won the Nobel Prize in 1969 for the photoelectric effect.",
		"context": `["Einstein won the Nobel Prize for the photoelectric effect.","Einstein won the Nobel Prize in 1921."]`,
	}
	result, requests := runBuiltin(t, BuiltinHallucination, variables,
		`{"verdicts":[{"verdict":"yes","reason":"agrees on the photoelectric effect"},{"verdict":"no","reason":"the context says 1921, not 1969"}]}`,
		"The score is 0.50 because the output gets the year wrong.")

	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.InDelta(t, 0.5, *result.Score.Number, 1e-12, "one of two chunks is contradicted")
	assert.Equal(t, "The score is 0.50 because the output gets the year wrong.", result.Reasoning)

	require.Len(t, requests, 2)
	assert.Contains(t, requests[0].User, `["Einstein won the Nobel Prize for the photoelectric effect.","Einstein won the Nobel Prize in 1921."]`)
	assert.Contains(t, requests[0].User, "in 1969 for the photoelectric effect")
	// The reason prompt gets each verdict under the right heading.
	alignments := strings.Index(requests[1].User, "Factual Alignments:")
	contradictions := strings.Index(requests[1].User, "Contradictions:")
	require.True(t, alignments >= 0 && contradictions > alignments)
	assert.Contains(t, requests[1].User[alignments:contradictions], "agrees on the photoelectric effect")
	assert.Contains(t, requests[1].User[contradictions:], "the context says 1921, not 1969")
	assert.Contains(t, requests[1].User, "0.50")

	clean, _ := runBuiltin(t, BuiltinHallucination, variables, `{"verdicts":[{"verdict":"yes","reason":"a"},{"verdict":"yes","reason":"b"}]}`, "Nothing is contradicted.")
	assert.Equal(t, NumberScore(0), clean.Score)

	entry, ok := CatalogEntryFor(BuiltinHallucination)
	require.True(t, ok, "it is in the catalog")
	assert.True(t, entry.LowerIsBetter)
	assert.Equal(t, OperatorLessOrEqual, entry.PassCriteria.EntryLevel.Operator)
	assert.ElementsMatch(t, []string{"output", "context"}, entry.Variables)
}
