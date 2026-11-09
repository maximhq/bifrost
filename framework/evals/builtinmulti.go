package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// Catalog keys of the built-ins that grade in several judge calls.
const (
	BuiltinBias             = "bias"
	BuiltinToxicity         = "toxicity"
	BuiltinFaithfulness     = "faithfulness"
	BuiltinContextPrecision = "context_precision"
	BuiltinContextRecall    = "context_recall"
	BuiltinContextRelevance = "context_relevance"
	BuiltinOutputRelevance  = "output_relevance"
	BuiltinSummarization    = "summarization"
	BuiltinHallucination    = "hallucination"
)

func init() {
	registerBuiltinAI(BuiltinBias, []string{VariableOutput}, runBias)
	registerBuiltinAI(BuiltinToxicity, []string{VariableOutput}, runToxicity)
	registerBuiltinAI(BuiltinFaithfulness, []string{VariableInput, VariableOutput, VariableContext}, runFaithfulness)
	registerBuiltinAI(BuiltinContextPrecision, []string{VariableInput, VariableExpectedOutput, VariableContext}, runContextPrecision)
	registerBuiltinAI(BuiltinContextRecall, []string{VariableExpectedOutput, VariableContext}, runContextRecall)
	registerBuiltinAI(BuiltinContextRelevance, []string{VariableInput, VariableContext}, runContextRelevance)
	registerBuiltinAI(BuiltinOutputRelevance, []string{VariableInput, VariableOutput}, runOutputRelevance)
	registerBuiltinAI(BuiltinSummarization, []string{VariableInput, VariableOutput}, runSummarization)
	registerBuiltinAI(BuiltinHallucination, []string{VariableOutput, VariableContext}, runHallucination)
}

// ----------------------------------------------------------------------------
// Shared pieces
// ----------------------------------------------------------------------------

func listSchema(field, description string) map[string]any {
	return objectSchema(map[string]any{field: stringListSchema(description)})
}

var (
	statementsSchema = listSchema("statements", "List of extracted statements from the text")
	reasonSchema     = objectSchema(map[string]any{"reason": stringSchema("The reason for the score")})

	// statementVerdictsSchema is a verdict per statement plus the judge's own summary.
	statementVerdictsSchema = objectSchema(map[string]any{
		"verdicts": map[string]any{
			"type":        "array",
			"description": "A list of individual statement verdicts",
			"items": objectSchema(map[string]any{
				"statement": stringSchema("The original input statement being evaluated"),
				"verdict":   enumSchema("The judgment on the statement", "yes", "no"),
				"reason":    stringSchema("Justification for the verdict"),
			}),
		},
		"score":     map[string]any{"type": "number", "description": "The number of yes in the verdict"},
		"reasoning": stringSchema("Overall reasoning or additional context for the verdicts"),
	})

	// verdictsSchema is a bare list of verdicts with reasons.
	verdictsSchema = objectSchema(map[string]any{
		"verdicts": map[string]any{
			"type": "array",
			"items": objectSchema(map[string]any{
				"verdict": enumSchema("The judgment", "yes", "no"),
				"reason":  stringSchema("Justification for the verdict"),
			}),
		},
	})
)

type verdict struct {
	verdict   string
	reason    string
	statement string
}

// verdictsAt reads the verdict list of a judge reply. The verdict text is normalised
// the way Maxim compares it: trimmed and lowercased.
func verdictsAt(reply, field string) ([]verdict, error) {
	list := gjson.Get(reply, field)
	if !list.IsArray() {
		return nil, fmt.Errorf("the judge reply has no %q list", field)
	}
	items := list.Array()
	out := make([]verdict, 0, len(items))
	for _, item := range items {
		out = append(out, verdict{
			verdict:   strings.ToLower(strings.TrimSpace(item.Get("verdict").String())),
			reason:    item.Get("reason").String(),
			statement: item.Get("statement").String(),
		})
	}
	return out, nil
}

// askList asks for one list of strings.
func (s *judgeSession) askList(ctx context.Context, promptText, field string, schema map[string]any) ([]string, error) {
	reply, err := s.ask(ctx, promptText, schema)
	if err != nil {
		return nil, err
	}
	if !gjson.Get(reply, field).IsArray() {
		return nil, fmt.Errorf("the judge reply has no %q list", field)
	}
	return stringsAt(reply, field), nil
}

// askText sends a prompt whose answer is prose, not JSON.
func (s *judgeSession) askText(ctx context.Context, promptText string) (string, error) {
	resp, err := s.judge(ctx, JudgeRequest{Provider: s.cfg.Provider, Model: s.cfg.Model, User: promptText, Params: s.cfg.ModelParams, ParamsJSON: s.params})
	if err != nil {
		return "", err
	}
	s.usage = addUsage(s.usage, resp.Usage)
	s.cost = addCost(s.cost, resp.Cost)
	return strings.TrimSpace(resp.Text), nil
}

// listText renders a list for a prompt. Maxim interpolates Python lists, which print
// with single quotes; JSON is used here, which a model reads the same way.
func listText(items any) string {
	data, err := json.Marshal(items)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// contextChunks reads the context variable as a list of retrieved chunks. A JSON array
// of strings is taken as the chunks; anything else is one chunk.
func contextChunks(value string) []string {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "[") {
		var chunks []string
		if err := json.Unmarshal([]byte(trimmed), &chunks); err == nil {
			return chunks
		}
	}
	if trimmed == "" {
		return nil
	}
	return []string{value}
}

// historyText is the conversation history as the prompts expect it. Maxim's prompts
// were written against Python, where a missing history prints as None.
func historyText(in Input) string {
	if history, ok := in.Variables[VariableHistory]; ok && history != "" {
		return history
	}
	return "None"
}

func twoDecimals(score float64) string { return strconv.FormatFloat(score, 'f', 2, 64) }

// share is the fraction of verdicts that match, 0 when there are none.
func share(verdicts []verdict, match func(verdict) bool) float64 {
	if len(verdicts) == 0 {
		return 0
	}
	count := 0
	for _, v := range verdicts {
		if match(v) {
			count++
		}
	}
	return float64(count) / float64(len(verdicts))
}

func saysYes(v verdict) bool   { return v.verdict == "yes" }
func notSaysNo(v verdict) bool { return v.verdict != "no" }

// ----------------------------------------------------------------------------
// Bias and toxicity: lower is better
// ----------------------------------------------------------------------------

// statementShare extracts statements from the output, asks for a verdict on each, and
// scores the share judged "yes". It is the shape of both bias and toxicity.
func statementShare(ctx context.Context, s *judgeSession, statementsPrompt, verdictsPrompt string) (float64, string, error) {
	statements, err := s.askList(ctx, statementsPrompt, "statements", statementsSchema)
	if err != nil {
		return 0, "", err
	}
	if len(statements) == 0 {
		return 0, emptyOutputReason, nil
	}
	reply, err := s.ask(ctx, renderPrompt(verdictsPrompt, map[string]string{"statements": listText(statements)}), statementVerdictsSchema)
	if err != nil {
		return 0, "", err
	}
	verdicts, err := verdictsAt(reply, "verdicts")
	if err != nil {
		return 0, "", err
	}
	reasoning, err := requireReasoning(reply, "reasoning")
	if err != nil {
		return 0, "", err
	}
	return share(verdicts, saysYes), reasoning, nil
}

// runBias scores the share of the output's statements that show bias.
func runBias(ctx context.Context, s *judgeSession, in Input) (float64, string, error) {
	statements := renderPrompt("bias__generate_statements", map[string]string{"actual_output": in.Variables[VariableOutput]})
	return statementShare(ctx, s, statements, "bias__generate_verdicts")
}

// runToxicity scores the share of the output's statements that are toxic.
func runToxicity(ctx context.Context, s *judgeSession, in Input) (float64, string, error) {
	output := in.Variables[VariableOutput]
	if strings.TrimSpace(output) == "" {
		return 0, "The output does not have any text hence is not toxic in nature.", nil
	}
	statements := renderPrompt("toxicity__generate_statements", map[string]string{"output": output})
	return statementShare(ctx, s, statements, "toxicity__generate_verdicts")
}

// ----------------------------------------------------------------------------
// Faithfulness
// ----------------------------------------------------------------------------

// runFaithfulness scores the share of the output's claims that the context does not
// contradict. The output is split into sentences, the judge rewrites them as
// standalone statements, and each statement gets a verdict against the context.
func runFaithfulness(ctx context.Context, s *judgeSession, in Input) (float64, string, error) {
	input, output := in.Variables[VariableInput], in.Variables[VariableOutput]
	contextText := strings.Join(contextChunks(in.Variables[VariableContext]), ",")

	statements, err := s.askList(ctx, renderPrompt("faithfulness__generate_statements", map[string]string{
		"input":     input,
		"output":    output,
		"sentences": listText(SplitSentences(output)),
	}), "statements", statementsSchema)
	if err != nil {
		return 0, "", err
	}
	if len(statements) == 0 {
		return 0, "The score is 0.0 because the output does not make significant claims to evaluate faithfulness to the context.", nil
	}
	reply, err := s.ask(ctx, renderPrompt("faithfulness__generate_verdicts", map[string]string{
		"history":    historyText(in),
		"input":      input,
		"statements": listText(statements),
		"context":    contextText,
	}), statementVerdictsSchema)
	if err != nil {
		return 0, "", err
	}
	verdicts, err := verdictsAt(reply, "verdicts")
	if err != nil {
		return 0, "", err
	}
	reasoning, err := requireReasoning(reply, "reasoning")
	if err != nil {
		return 0, "", err
	}
	return share(verdicts, notSaysNo), reasoning, nil
}

// ----------------------------------------------------------------------------
// Retrieval: context precision, recall and relevance
// ----------------------------------------------------------------------------

// runContextPrecision scores whether the relevant chunks are ranked ahead of the
// irrelevant ones: the mean of precision@k over the positions of the relevant chunks.
func runContextPrecision(ctx context.Context, s *judgeSession, in Input) (float64, string, error) {
	input := in.Variables[VariableInput]
	reply, err := s.ask(ctx, renderPrompt("context_precision__generate_verdicts", map[string]string{
		"input":             input,
		"expected_output":   in.Variables[VariableExpectedOutput],
		"retrieved_context": listText(contextChunks(in.Variables[VariableContext])),
	}), verdictsSchema)
	if err != nil {
		return 0, "", err
	}
	verdicts, err := verdictsAt(reply, "verdicts")
	if err != nil {
		return 0, "", err
	}

	score, relevant, weighted := 0.0, 0, 0.0
	for k, v := range verdicts {
		if v.verdict == "yes" {
			relevant++
			weighted += float64(relevant) / float64(k+1)
		}
	}
	if relevant > 0 {
		score = weighted / float64(relevant)
	}

	summary := make([]map[string]string, len(verdicts))
	for i, v := range verdicts {
		summary[i] = map[string]string{"verdict": v.verdict, "reasons": v.reason}
	}
	reasoning, err := s.askReason(ctx, renderPrompt("context_precision__generate_reason", map[string]string{
		"input":    input,
		"verdicts": listText(summary),
		"score":    twoDecimals(score),
	}))
	return score, reasoning, err
}

// runHallucination scores the share of retrieved chunks the output contradicts. The
// judge gives one verdict per chunk: "yes" when the output agrees with it, "no" only
// for a contradiction. Lower is better.
//
// One deliberate difference from Maxim: its reason prompt receives the two lists
// swapped (contradictions under "Factual Alignments"). Here each list goes under its
// own heading. The score is computed the same way.
func runHallucination(ctx context.Context, s *judgeSession, in Input) (float64, string, error) {
	reply, err := s.ask(ctx, renderPrompt("hallucination__generate_verdicts", map[string]string{
		"contexts":      listText(contextChunks(in.Variables[VariableContext])),
		"actual_output": in.Variables[VariableOutput],
	}), verdictsSchema)
	if err != nil {
		return 0, "", err
	}
	verdicts, err := verdictsAt(reply, "verdicts")
	if err != nil {
		return 0, "", err
	}
	score := share(verdicts, func(v verdict) bool { return v.verdict == "no" })

	alignments, contradictions := []string{}, []string{}
	for _, v := range verdicts {
		if v.verdict == "no" {
			contradictions = append(contradictions, v.reason)
		} else {
			alignments = append(alignments, v.reason)
		}
	}
	reasoning, err := s.askText(ctx, renderPrompt("hallucination__generate_reason", map[string]string{
		"factual_alignments": listText(alignments),
		"contradictions":     listText(contradictions),
		"score":              twoDecimals(score),
	}))
	return score, reasoning, err
}

// runContextRecall scores the share of the expected output that the retrieved context
// can be credited with.
func runContextRecall(ctx context.Context, s *judgeSession, in Input) (float64, string, error) {
	expected := in.Variables[VariableExpectedOutput]
	reply, err := s.ask(ctx, renderPrompt("context_recall__generate_verdicts", map[string]string{
		"expected_output":   expected,
		"retrieved_context": listText(contextChunks(in.Variables[VariableContext])),
	}), verdictsSchema)
	if err != nil {
		return 0, "", err
	}
	verdicts, err := verdictsAt(reply, "verdicts")
	if err != nil {
		return 0, "", err
	}
	score := share(verdicts, saysYes)

	supportive, unsupportive := []string{}, []string{}
	for _, v := range verdicts {
		if v.verdict == "yes" {
			supportive = append(supportive, v.reason)
		} else {
			unsupportive = append(unsupportive, v.reason)
		}
	}
	reasoning, err := s.askReason(ctx, renderPrompt("context_recall__generate_reason", map[string]string{
		"expected_output":      expected,
		"supportive_reasons":   listText(supportive),
		"unsupportive_reasons": listText(unsupportive),
		"score":                twoDecimals(score),
	}))
	return score, reasoning, err
}

var contextRelevanceVerdictsSchema = objectSchema(map[string]any{
	"verdicts": map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"statement": stringSchema("A statement found in the context"),
				"verdict":   enumSchema("Whether the statement is relevant to the input", "yes", "no"),
				"reason":    stringSchema("Why the statement is irrelevant"),
			},
			"required": []any{"statement", "verdict"},
		},
	},
})

// runContextRelevance scores the share of statements in the retrieved chunks that are
// relevant to the input. Each chunk is judged in its own call.
func runContextRelevance(ctx context.Context, s *judgeSession, in Input) (float64, string, error) {
	input := in.Variables[VariableInput]
	var all []verdict
	for _, chunk := range contextChunks(in.Variables[VariableContext]) {
		reply, err := s.ask(ctx, renderPrompt("context_relevance__generate_verdicts", map[string]string{
			"input":   input,
			"context": chunk,
			"history": historyText(in),
		}), contextRelevanceVerdictsSchema)
		if err != nil {
			return 0, "", err
		}
		verdicts, err := verdictsAt(reply, "verdicts")
		if err != nil {
			return 0, "", err
		}
		all = append(all, verdicts...)
	}
	score := share(all, saysYes)

	irrelevancies, relevant := []string{}, []string{}
	for _, v := range all {
		if v.verdict == "no" {
			irrelevancies = append(irrelevancies, v.reason)
		} else {
			relevant = append(relevant, v.statement)
		}
	}
	reasoning, err := s.askReason(ctx, renderPrompt("context_relevance__generate_reason", map[string]string{
		"input":               input,
		"irrelevancies":       listText(irrelevancies),
		"relevant_statements": listText(relevant),
		"score":               pythonFloat(score),
	}))
	return score, reasoning, err
}

// askReason asks for the one-field reply {"reason": ...}.
func (s *judgeSession) askReason(ctx context.Context, promptText string) (string, error) {
	reply, err := s.ask(ctx, promptText, reasonSchema)
	if err != nil {
		return "", err
	}
	return requireReasoning(reply, "reason")
}

// pythonFloat prints a score rounded to two decimals the way Python prints a float,
// which is how this one prompt receives it: 1.0, 0.5, 0.67.
func pythonFloat(score float64) string {
	rounded, _ := strconv.ParseFloat(twoDecimals(score), 64)
	text := strconv.FormatFloat(rounded, 'f', -1, 64)
	if !strings.Contains(text, ".") {
		text += ".0"
	}
	return text
}

// ----------------------------------------------------------------------------
// Output relevance
// ----------------------------------------------------------------------------

// runOutputRelevance scores the share of the output's statements that address the
// input. An output with no statements scores 1: nothing in it is off topic.
func runOutputRelevance(ctx context.Context, s *judgeSession, in Input) (float64, string, error) {
	input := in.Variables[VariableInput]
	statements, err := s.askList(ctx, renderPrompt("output_relevance__generate_statements", map[string]string{
		"actual_output": in.Variables[VariableOutput],
	}), "statements", statementsSchema)
	if err != nil {
		return 0, "", err
	}

	var verdicts []verdict
	if len(statements) > 0 {
		reply, err := s.ask(ctx, renderPrompt("output_relevance__generate_verdicts", map[string]string{
			"input":         input,
			"actual_output": listText(statements),
			"history":       historyText(in),
		}), verdictsSchema)
		if err != nil {
			return 0, "", err
		}
		if verdicts, err = verdictsAt(reply, "verdicts"); err != nil {
			return 0, "", err
		}
	}
	score := 1.0
	if len(verdicts) > 0 {
		score = share(verdicts, notSaysNo)
	}

	irrelevant := []string{}
	for _, v := range verdicts {
		if v.verdict == "no" {
			irrelevant = append(irrelevant, v.reason)
		}
	}
	reasoning, err := s.askText(ctx, renderPrompt("output_relevance__generate_reason", map[string]string{
		"irrelevant_statements": listText(irrelevant),
		"input":                 input,
		"score":                 twoDecimals(score),
	}))
	return score, reasoning, err
}

// ----------------------------------------------------------------------------
// Summarization
// ----------------------------------------------------------------------------

var summarizationAlignmentSchema = objectSchema(map[string]any{
	"verdicts": map[string]any{
		"type": "array",
		"items": objectSchema(map[string]any{
			"verdict": enumSchema("Whether the original text supports the claim", "yes", "no", "idk"),
			"reason":  stringSchema("Justification for the verdict"),
		}),
	},
})

// runSummarization scores a summary (the output) of a text (the input) as the lower of
// two measures. Alignment is the share of the summary's claims the text supports.
// Coverage is the share of questions the text can answer that the summary can also
// answer, over questions the judge writes from the text's key points.
func runSummarization(ctx context.Context, s *judgeSession, in Input) (float64, string, error) {
	text, summary := in.Variables[VariableInput], in.Variables[VariableOutput]

	claims, err := s.askList(ctx, renderPrompt("summarization__generate_statements", map[string]string{"text": summary}), "statements", statementsSchema)
	if err != nil {
		return 0, "", err
	}
	keypoints, err := s.askList(ctx, renderPrompt("summarization__generate_keypoints", map[string]string{"text": text}), "keypoints", listSchema("keypoints", "Key points of the text"))
	if err != nil {
		return 0, "", err
	}
	questions, err := s.askList(ctx, renderPrompt("summarization__generate_questions_v2", map[string]string{
		"text":      text,
		"keypoints": listText(keypoints),
	}), "questions", listSchema("questions", "Closed questions the text can answer"))
	if err != nil {
		return 0, "", err
	}
	answer := func(source string) ([]string, error) {
		return s.askList(ctx, renderPrompt("summarization__generate_answers", map[string]string{
			"questions": listText(questions),
			"text":      source,
		}), "answers", listSchema("answers", "A yes or no answer to each question"))
	}
	fromText, err := answer(text)
	if err != nil {
		return 0, "", err
	}
	fromSummary, err := answer(summary)
	if err != nil {
		return 0, "", err
	}
	if len(fromText) != len(fromSummary) || len(fromText) > len(questions) {
		return 0, "", errors.New("the judge answered a different number of questions for the text and for the summary")
	}

	var alignment []verdict
	if len(claims) > 0 {
		reply, err := s.ask(ctx, renderPrompt("summarization__generate_alignment_verdicts", map[string]string{
			"summary_claims": listText(claims),
			"original_text":  text,
		}), summarizationAlignmentSchema)
		if err != nil {
			return 0, "", err
		}
		if alignment, err = verdictsAt(reply, "verdicts"); err != nil {
			return 0, "", err
		}
	}
	alignmentScore := share(alignment, saysYes)

	answerable, covered := 0, 0
	missed := []string{}
	for i := range fromText {
		if !isYes(fromText[i]) {
			continue
		}
		answerable++
		if isYes(fromSummary[i]) {
			covered++
		} else if strings.EqualFold(strings.TrimSpace(fromSummary[i]), "no") {
			missed = append(missed, questions[i])
		}
	}
	coverageScore := 0.0
	if answerable > 0 {
		coverageScore = float64(covered) / float64(answerable)
	}
	score := min(alignmentScore, coverageScore)

	contradictions, redundancies := []string{}, []string{}
	for _, v := range alignment {
		switch v.verdict {
		case "no":
			contradictions = append(contradictions, v.reason)
		case "idk":
			redundancies = append(redundancies, v.reason)
		}
	}
	reasoning, err := s.askText(ctx, summarizationReasonPrompt(contradictions, redundancies, missed, score))
	return score, reasoning, err
}

// summarizationReasonPrompt builds the prompt that asks the judge to explain a
// summarization score.
func summarizationReasonPrompt(contradictions, redundancies, missed []string, score float64) string {
	var reasonPrompt strings.Builder
	reasonPrompt.WriteString(renderPrompt("summarization__generate_reason", map[string]string{
		"contradictions": listText(contradictions),
		"redundancies":   listText(redundancies),
		"score":          twoDecimals(score),
	}))
	if len(missed) > 0 {
		reasonPrompt.WriteString("Questions the original text can answer but not the summary:\n            ")
		reasonPrompt.WriteString(listText(missed))
		reasonPrompt.WriteString("\n\n")
	}
	// The template ends just before this marker, so the prompt has one, and it is last.
	reasonPrompt.WriteString("Reason:")
	return reasonPrompt.String()
}
