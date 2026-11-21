package evals

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
)

// promptFiles holds the judge prompts of the built-in evaluators. They are Maxim's
// prompts, extracted from its evaluation service with each argument replaced by a
// {{name}} placeholder, so the wording is theirs and not a retyped copy.
//
//go:embed prompts/*.txt
var promptFiles embed.FS

var (
	promptCache   = map[string]template{}
	promptCacheMu sync.RWMutex
)

// prompt returns a compiled built-in prompt by file name, without the extension.
func prompt(name string) template {
	promptCacheMu.RLock()
	compiled, ok := promptCache[name]
	promptCacheMu.RUnlock()
	if ok {
		return compiled
	}
	data, err := promptFiles.ReadFile("prompts/" + name + ".txt")
	if err != nil {
		// A missing prompt is a build defect, caught by the catalog test.
		panic("evals: missing built-in prompt " + name)
	}
	compiled = compileTemplate(string(data))
	promptCacheMu.Lock()
	promptCache[name] = compiled
	promptCacheMu.Unlock()
	return compiled
}

// renderPrompt fills a built-in prompt. args maps the prompt's placeholders to values.
func renderPrompt(name string, args map[string]string) string {
	return prompt(name).renderString(Input{Variables: args}, nil)
}

// Variables the built-in evaluators read, beyond output and expected output.
const (
	VariableInput         = "input"
	VariableContext       = "context"
	VariableSession       = "session"
	VariableExpectedSteps = "expected_steps"
	VariableTranscript    = "transcript"
	VariableScheduledDate = "scheduled_date"
	VariableCurrentDate   = "current_date"
	VariableHistory       = "history"
)

// emptyOutputReason is what Maxim reports when there is nothing to grade.
const emptyOutputReason = "The score is 0.0 because the output does not contain any text."

// errInvalidJudgeJSON mirrors the message Maxim returns for an unusable judge reply.
var errInvalidJudgeJSON = errors.New("the evaluation model returned invalid JSON; use a more capable judge model")

// judgeSession makes the model calls of one built-in evaluation and adds up what they cost.
type judgeSession struct {
	judge  Judge
	cfg    *AIConfig
	params []byte // the model parameters, encoded once for the evaluator
	usage  *Usage
	cost   *float64
}

// ask sends a prompt and returns the JSON object in the reply. Maxim sends the whole
// prompt as one user message, so there is no system message here. A provider that
// rejects structured output gets one more try as plain text.
func (s *judgeSession) ask(ctx context.Context, promptText string, schema map[string]any) (string, error) {
	return s.askMessages(ctx, "", promptText, schema)
}

// askMessages is ask for the few built-ins whose prompt is split into a system message
// and a user message.
func (s *judgeSession) askMessages(ctx context.Context, system, user string, schema map[string]any) (string, error) {
	req := JudgeRequest{Provider: s.cfg.Provider, Model: s.cfg.Model, System: system, User: user, Params: s.cfg.ModelParams, ParamsJSON: s.params, Schema: schema}
	resp, err := s.judge(ctx, req)
	if err != nil && schema != nil && isStructuredOutputRejection(err) {
		req.Schema = nil
		resp, err = s.judge(ctx, req)
	}
	if err != nil {
		return "", err
	}
	s.usage = addUsage(s.usage, resp.Usage)
	s.cost = addCost(s.cost, resp.Cost)
	if resp.Truncated {
		return "", errors.New("the judge reply was cut off by the token limit; raise the model's max tokens")
	}
	object, err := firstJSONObject(resp.Text)
	if err != nil {
		return "", errInvalidJudgeJSON
	}
	return object, nil
}

// builtinRun scores an input with a judge session. It returns the score and reasoning.
type builtinRun func(ctx context.Context, s *judgeSession, in Input) (score float64, reasoning string, err error)

// builtinAI is one LLM-judged catalog entry.
type builtinAI struct {
	required []string
	run      builtinRun
}

// builtinAIEvaluator runs a built-in judge pipeline with the evaluator's judge model.
type builtinAIEvaluator struct {
	spec   builtinAI
	cfg    *AIConfig
	judge  Judge
	params []byte // the model parameters, encoded once
}

func (e *builtinAIEvaluator) RequiredVariables() []string { return e.spec.required }

func (e *builtinAIEvaluator) Evaluate(ctx context.Context, in Input) (Result, error) {
	session := &judgeSession{judge: e.judge, cfg: e.cfg, params: e.params}
	score, reasoning, err := e.spec.run(ctx, session, in)
	// Calls made before a failure were billed, so usage is reported either way.
	result := Result{Usage: session.usage, Cost: session.cost}
	if err != nil {
		result.Score = ErrorScore()
		result.Reasoning = err.Error()
		return result, nil
	}
	result.Score = NumberScore(score)
	result.Reasoning = reasoning
	return result, nil
}

// builtinAIEvaluators are the LLM-judged built-ins by catalog key.
var builtinAIEvaluators = map[string]builtinAI{}

// registerBuiltinAI adds a built-in to the table. It is called from init functions.
func registerBuiltinAI(key string, required []string, run builtinRun) {
	if _, dup := builtinAIEvaluators[key]; dup {
		panic("evals: built-in registered twice: " + key)
	}
	builtinAIEvaluators[key] = builtinAI{required: required, run: run}
}

// RegisterBuiltinAIEvaluators installs every LLM-judged built-in on an engine.
func RegisterBuiltinAIEvaluators(engine *Engine, judge Judge) {
	for key, spec := range builtinAIEvaluators {
		engine.Register(EvaluatorTypeAI, key, func(_ Definition, cfg Config) (Evaluator, error) {
			ai, ok := cfg.(*AIConfig)
			if !ok {
				return nil, fmt.Errorf("an AI evaluator needs an AI config, got %T", cfg)
			}
			if judge == nil {
				return nil, errors.New("no judge model client is configured")
			}
			return &builtinAIEvaluator{spec: spec, cfg: ai, judge: judge, params: encodeJudgeParams(ai.ModelParams)}, nil
		})
	}
}

// ----------------------------------------------------------------------------
// Schema and reply helpers
// ----------------------------------------------------------------------------

// objectSchema builds a strict JSON object schema in which every property is required.
func objectSchema(properties map[string]any) map[string]any {
	required := make([]any, 0, len(properties))
	for _, name := range sortedSchemaKeys(properties) {
		required = append(required, name)
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func sortedSchemaKeys(properties map[string]any) []string {
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	// Sorted so the schema, and with it the request, is the same on every call.
	slices.Sort(names)
	return names
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func enumSchema(description string, values ...any) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": description}
}

func stringListSchema(description string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": description}
}

// scoreAndReasoningSchema is the reply shape of the rubric-style judges.
var scoreAndReasoningSchema = objectSchema(map[string]any{
	"score":     map[string]any{"type": "number", "description": "The score assigned by the rubric"},
	"reasoning": stringSchema("A brief explanation justifying the score based on the rubric"),
})

// binaryScoreSchema is the reply shape of the pass/fail judges.
var binaryScoreSchema = objectSchema(map[string]any{
	"score":     map[string]any{"type": "integer", "description": "A binary score representing 0 (false) or 1 (true).", "enum": []any{0, 1}},
	"reasoning": stringSchema("A detailed explanation justifying the score."),
})

// requireNumber reads a numeric field of a judge reply.
func requireNumber(object, field string) (float64, error) {
	value := gjson.Get(object, field)
	switch value.Type {
	case gjson.Number:
		return value.Float(), nil
	case gjson.String:
		if parsed, ok := parseNumber(value.String()); ok {
			return parsed, nil
		}
	}
	return 0, fmt.Errorf("the judge reply has no numeric %q field", field)
}

// requireReasoning reads the reasoning field, which every judge reply must carry.
func requireReasoning(object, field string) (string, error) {
	value := gjson.Get(object, field)
	if !value.Exists() {
		return "", fmt.Errorf("the judge reply has no %q field", field)
	}
	return reasoningText(value), nil
}

// stringsAt reads an array of strings from a judge reply, skipping other values.
func stringsAt(object, field string) []string {
	var out []string
	for _, item := range gjson.Get(object, field).Array() {
		if item.Type == gjson.String {
			out = append(out, item.String())
		}
	}
	return out
}

// meanOf averages a list of binary scores, 0 when the list is empty.
func meanOf(object, field string) float64 {
	values := gjson.Get(object, field).Array()
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value.Float()
	}
	return total / float64(len(values))
}

func isYes(value string) bool { return strings.EqualFold(strings.TrimSpace(value), "yes") }
