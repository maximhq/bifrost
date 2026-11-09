package evals

import (
	"context"
	"strings"

	"github.com/tidwall/gjson"
)

// Catalog keys of the built-ins that grade with one judge call.
const (
	BuiltinClarity                      = "clarity"
	BuiltinConciseness                  = "conciseness"
	BuiltinSQLCorrectness               = "sql_correctness"
	BuiltinAgentTrajectory              = "agent_trajectory"
	BuiltinTaskSuccess                  = "task_success"
	BuiltinStepUtility                  = "step_utility"
	BuiltinToolSelection                = "tool_selection"
	BuiltinStepCompletionStrictMatch    = "step_completion_strict_match"
	BuiltinStepCompletionUnorderedMatch = "step_completion_unordered_match"
	BuiltinCodeCompletion               = "code_completion"
	BuiltinCodeLanguageSelection        = "code_language_selection"
	BuiltinFollowupCheck                = "followup_check"
)

func init() {
	registerBuiltinAI(BuiltinClarity, []string{VariableOutput}, rubricOnOutput("clarity__generate_verdicts"))
	registerBuiltinAI(BuiltinConciseness, []string{VariableOutput}, rubricOnOutput("conciseness__generate_verdicts"))
	registerBuiltinAI(BuiltinSQLCorrectness, []string{VariableInput, VariableOutput, VariableDBSchema}, runSQLCorrectness)

	registerBuiltinAI(BuiltinAgentTrajectory, []string{VariableSession}, sessionJudge("agent_trajectory__generate_eval_prompt_v3", thoughtBinarySchema, false))
	registerBuiltinAI(BuiltinTaskSuccess, []string{VariableSession}, sessionJudge("task_success__generate_eval_prompt", thoughtBinarySchema, false))
	registerBuiltinAI(BuiltinStepUtility, []string{VariableSession}, sessionJudge("step_utility__generate_eval_prompt", scoreListSchema, true))
	registerBuiltinAI(BuiltinToolSelection, []string{VariableSession}, sessionJudge("tool_selection__generate_eval_prompt", thoughtScoreListSchema, true))
	registerBuiltinAI(BuiltinStepCompletionStrictMatch, []string{VariableSession, VariableExpectedSteps}, stepCompletion("step_completion_strict_match__generate_eval_prompt"))
	registerBuiltinAI(BuiltinStepCompletionUnorderedMatch, []string{VariableSession, VariableExpectedSteps}, stepCompletion("step_completion_unordered_match__generate_eval_prompt"))

	registerBuiltinAI(BuiltinCodeCompletion, []string{VariableInput, VariableOutput}, runCodeCompletion)
	registerBuiltinAI(BuiltinCodeLanguageSelection, []string{VariableInput, VariableOutput}, runCodeLanguageSelection)
	registerBuiltinAI(BuiltinFollowupCheck, []string{VariableTranscript}, runFollowupCheck)
}

// rubricOnOutput grades the output alone against a rubric that returns a score and
// reasoning. An empty output scores 0 without a model call.
func rubricOnOutput(promptName string) builtinRun {
	return func(ctx context.Context, s *judgeSession, in Input) (float64, string, error) {
		output := in.Variables[VariableOutput]
		if output == "" {
			return 0, emptyOutputReason, nil
		}
		reply, err := s.ask(ctx, renderPrompt(promptName, map[string]string{"actual_output": output}), scoreAndReasoningSchema)
		if err != nil {
			return 0, "", err
		}
		return scoreAndReasoning(reply)
	}
}

// scoreAndReasoning reads the two fields every rubric reply carries.
func scoreAndReasoning(reply string) (float64, string, error) {
	score, err := requireNumber(reply, "score")
	if err != nil {
		return 0, "", err
	}
	reasoning, err := requireReasoning(reply, "reasoning")
	if err != nil {
		return 0, "", err
	}
	return score, reasoning, nil
}

func runSQLCorrectness(ctx context.Context, s *judgeSession, in Input) (float64, string, error) {
	output := in.Variables[VariableOutput]
	if output == "" {
		return 0, emptyOutputReason, nil
	}
	reply, err := s.ask(ctx, renderPrompt("sql_correctness__generate_verdicts", map[string]string{
		"input":           in.Variables[VariableInput],
		"actual_output":   output,
		"database_schema": in.Variables[VariableDBSchema],
	}), binaryScoreSchema)
	if err != nil {
		return 0, "", err
	}
	return scoreAndReasoning(reply)
}

var (
	thoughtBinarySchema = objectSchema(map[string]any{
		"thought":   stringSchema("The model's reasoning or thought process behind the decision."),
		"score":     map[string]any{"type": "integer", "description": "A binary score representing 0 (false) or 1 (true).", "enum": []any{0, 1}},
		"reasoning": stringSchema("A detailed explanation justifying the score."),
	})
	scoreListSchema = objectSchema(map[string]any{
		"score": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "integer", "enum": []any{0, 1}},
			"description": "A binary score representing 0 (false) or 1 (true).",
		},
		"reasoning": stringSchema("A detailed explanation justifying the score."),
	})
	thoughtScoreListSchema = objectSchema(map[string]any{
		"thought": stringSchema("The model's reasoning or thought process behind the decision."),
		"score": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "integer", "enum": []any{0, 1}},
			"description": "A binary score representing 0 (false) or 1 (true).",
		},
		"reasoning": stringSchema("A detailed explanation justifying the score."),
	})
)

// sessionJudge grades an agent session. With averaged set, the judge returns one binary
// score per step and the result is their mean; otherwise it returns a single score.
func sessionJudge(promptName string, schema map[string]any, averaged bool) builtinRun {
	return func(ctx context.Context, s *judgeSession, in Input) (float64, string, error) {
		reply, err := s.ask(ctx, renderPrompt(promptName, map[string]string{"session": in.Variables[VariableSession]}), schema)
		if err != nil {
			return 0, "", err
		}
		reasoning, err := requireReasoning(reply, "reasoning")
		if err != nil {
			return 0, "", err
		}
		if averaged {
			if !gjson.Get(reply, "score").IsArray() {
				return 0, "", errInvalidJudgeJSON
			}
			return meanOf(reply, "score"), reasoning, nil
		}
		score, err := requireNumber(reply, "score")
		if err != nil {
			return 0, "", err
		}
		return score, reasoning, nil
	}
}

// stepCompletion grades whether a session carried out the expected steps.
func stepCompletion(promptName string) builtinRun {
	return func(ctx context.Context, s *judgeSession, in Input) (float64, string, error) {
		reply, err := s.ask(ctx, renderPrompt(promptName, map[string]string{
			"session":        in.Variables[VariableSession],
			"expected_steps": in.Variables[VariableExpectedSteps],
		}), binaryScoreSchema)
		if err != nil {
			return 0, "", err
		}
		return scoreAndReasoning(reply)
	}
}

var codeCompletionSchema = objectSchema(map[string]any{
	"completeness_score": map[string]any{"type": "number", "description": "Completeness score from 1 to 5, where 5 is fully complete and runnable"},
	"checklist": map[string]any{
		"type":        "object",
		"description": "Checklist of completeness dimensions",
		"properties": map[string]any{
			"all_imports_present":     enumSchema("All imports/dependencies present", "yes", "no"),
			"no_placeholders":         enumSchema("No placeholder functions (TODO, pass, etc.)", "yes", "no"),
			"all_files_included":      enumSchema("All necessary files included", "yes", "no"),
			"dependencies_documented": enumSchema("Dependencies/installation documented", "yes", "no"),
			"data_handling_addressed": enumSchema("Data handling addressed or N/A", "yes", "no", "not applicable"),
			"clear_entry_point":       enumSchema("Clear how to run the code", "yes", "no"),
		},
		"required":             []any{"all_files_included", "all_imports_present", "clear_entry_point", "data_handling_addressed", "dependencies_documented", "no_placeholders"},
		"additionalProperties": false,
	},
	"missing_elements": stringListSchema("List of missing components that prevent completeness"),
	"assumptions_made": stringListSchema("Assumptions that might prevent running the code"),
	"reasoning":        stringSchema("3-4 sentences explaining the completeness assessment"),
})

// runCodeCompletion grades whether generated code is complete and runnable. The judge
// scores 1 to 5, which is mapped onto 0 to 1.
func runCodeCompletion(ctx context.Context, s *judgeSession, in Input) (float64, string, error) {
	reply, err := s.ask(ctx, renderPrompt("code_completion__evaluate_completeness", map[string]string{
		"input_request": in.Variables[VariableInput],
		"code_output":   in.Variables[VariableOutput],
	}), codeCompletionSchema)
	if err != nil {
		return 0, "", err
	}
	raw := 1.0
	if value := gjson.Get(reply, "completeness_score"); value.Exists() {
		raw = value.Float()
	}
	raw = max(1.0, min(5.0, raw))
	reasoning := reasoningText(gjson.Get(reply, "reasoning"))
	if missing := stringsAt(reply, "missing_elements"); len(missing) > 0 {
		reasoning += "\n\nMissing: " + strings.Join(missing, "; ")
	}
	if assumptions := stringsAt(reply, "assumptions_made"); len(assumptions) > 0 {
		reasoning += "\nAssumptions: " + strings.Join(assumptions, "; ")
	}
	return (raw - 1.0) / 4.0, reasoning, nil
}

var codeLanguageSchema = objectSchema(map[string]any{
	"correct_languages":  enumSchema("Whether the correct programming languages were used", "yes", "no"),
	"expected_languages": stringListSchema("List of expected languages/frameworks for the task"),
	"actual_languages":   stringListSchema("List of detected languages/frameworks in the output"),
	"reasoning":          stringSchema("2-3 sentences explaining the language selection assessment"),
})

// runCodeLanguageSelection grades whether code is written in the languages asked for.
func runCodeLanguageSelection(ctx context.Context, s *judgeSession, in Input) (float64, string, error) {
	reply, err := s.ask(ctx, renderPrompt("code_language_selection__evaluate_code_language_selection", map[string]string{
		"input_request": in.Variables[VariableInput],
		"actual_output": in.Variables[VariableOutput],
	}), codeLanguageSchema)
	if err != nil {
		return 0, "", err
	}
	score := 0.0
	if isYes(gjson.Get(reply, "correct_languages").String()) {
		score = 1
	}
	var parts []string
	if reasoning := reasoningText(gjson.Get(reply, "reasoning")); reasoning != "" {
		parts = append(parts, reasoning)
	}
	if expected := stringsAt(reply, "expected_languages"); len(expected) > 0 {
		parts = append(parts, "Expected languages: "+strings.Join(expected, ", "))
	}
	if actual := stringsAt(reply, "actual_languages"); len(actual) > 0 {
		parts = append(parts, "Actual languages: "+strings.Join(actual, ", "))
	}
	return score, strings.TrimSpace(strings.Join(parts, "\n\n")), nil
}

var followupCheckSchema = objectSchema(map[string]any{
	"tool_output_valid":       enumSchema("Whether the tool output accurately reflects the conversation", "yes", "no", "partial"),
	"followup_date_valid":     enumSchema("Whether the followup date is valid and appropriate", "yes", "no", "not_applicable"),
	"tool_output_issues":      stringListSchema("List of issues found in the tool output (empty if valid)"),
	"followup_date_issues":    stringListSchema("List of issues with the followup date (empty if valid)"),
	"medical_appropriateness": enumSchema("Whether the followup timing is medically appropriate", "appropriate", "questionable", "inappropriate", "not_applicable"),
	"reasoning":               stringSchema("2-3 sentences explaining the overall assessment"),
})

// followupNoCurrentDate is what the prompt says when no current date is given.
const followupNoCurrentDate = "Not provided (cannot validate if followup date is in the future)"

// runFollowupCheck grades whether a scheduled follow-up matches what was agreed in a
// conversation. The tool output counts for half and the date for half, unless the
// judge says a date does not apply.
func runFollowupCheck(ctx context.Context, s *judgeSession, in Input) (float64, string, error) {
	currentDate := in.Variables[VariableCurrentDate]
	if currentDate == "" {
		currentDate = followupNoCurrentDate
	}
	reply, err := s.ask(ctx, renderPrompt("followup_check__evaluate_followup_check", map[string]string{
		"input_transcript": in.Variables[VariableTranscript],
		"tool_output":      in.Variables[VariableScheduledDate],
		"current_date":     currentDate,
	}), followupCheckSchema)
	if err != nil {
		return 0, "", err
	}

	toolScore := 0.0
	switch gjson.Get(reply, "tool_output_valid").String() {
	case "yes":
		toolScore = 1
	case "partial":
		toolScore = 0.5
	}
	score := toolScore
	// A missing verdict counts as "no", as it does in Maxim.
	if dateValid := gjson.Get(reply, "followup_date_valid").String(); dateValid != "not_applicable" {
		dateScore := 0.0
		if dateValid == "yes" {
			dateScore = 1
		}
		score = (toolScore + dateScore) / 2
	}

	var parts []string
	if reasoning := reasoningText(gjson.Get(reply, "reasoning")); reasoning != "" {
		parts = append(parts, reasoning)
	}
	if issues := stringsAt(reply, "tool_output_issues"); len(issues) > 0 {
		parts = append(parts, "Tool Output Issues: "+strings.Join(issues, "; "))
	}
	if issues := stringsAt(reply, "followup_date_issues"); len(issues) > 0 {
		parts = append(parts, "Followup Date Issues: "+strings.Join(issues, "; "))
	}
	if appropriateness := gjson.Get(reply, "medical_appropriateness").String(); appropriateness != "" && appropriateness != "not_applicable" {
		parts = append(parts, "Medical Appropriateness: "+appropriateness)
	}
	return score, strings.Join(parts, "\n\n"), nil
}
