package evals

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// RegisterAIEvaluator installs the custom LLM-as-judge evaluator on an engine.
func RegisterAIEvaluator(engine *Engine, judge Judge) {
	engine.Register(EvaluatorTypeAI, "", func(_ Definition, cfg Config) (Evaluator, error) {
		ai, ok := cfg.(*AIConfig)
		if !ok {
			return nil, fmt.Errorf("an AI evaluator needs an AI config, got %T", cfg)
		}
		if judge == nil {
			return nil, errors.New("no judge model client is configured")
		}
		return newAIEvaluator(ai, judge), nil
	})
}

// aiEvaluator asks a model to grade the input against the user's instructions.
type aiEvaluator struct {
	cfg       *AIConfig
	judge     Judge
	variables []string // every variable the judge may receive, in prompt order
	required  []string // the ones that must be present
	system    string
	schema    map[string]any
	params    []byte // the model parameters, encoded once
}

func newAIEvaluator(cfg *AIConfig, judge Judge) *aiEvaluator {
	// Prompt order is the order the instructions use the variables, then any the config
	// declares but the instructions never name.
	variables := ExtractVariables(cfg.Instructions)
	for _, declared := range [][]string{sortedKeys(cfg.Variables), sortedKeys(cfg.OptionalVariables)} {
		for _, name := range declared {
			if !slices.Contains(variables, name) {
				variables = append(variables, name)
			}
		}
	}
	var required []string
	for _, name := range variables {
		if _, optional := cfg.OptionalVariables[name]; !optional {
			required = append(required, name)
		}
	}
	evaluator := &aiEvaluator{cfg: cfg, judge: judge, variables: variables, required: required, params: encodeJudgeParams(cfg.ModelParams)}
	evaluator.system = evaluator.systemPrompt()
	evaluator.schema = evaluator.responseSchema()
	return evaluator
}

// RequiredVariables lists the variables named in the instructions that are not optional.
func (e *aiEvaluator) RequiredVariables() []string { return e.required }

// Evaluate makes the judge call and turns its reply into a score.
func (e *aiEvaluator) Evaluate(ctx context.Context, in Input) (Result, error) {
	req := JudgeRequest{
		Provider:   e.cfg.Provider,
		Model:      e.cfg.Model,
		System:     e.system,
		User:       e.userPrompt(in),
		Params:     e.cfg.ModelParams,
		ParamsJSON: e.params,
		Schema:     e.schema,
	}
	resp, err := e.judge(ctx, req)
	if err != nil && isStructuredOutputRejection(err) {
		// The system prompt already demands JSON, so plain text is a workable fallback.
		req.Schema = nil
		resp, err = e.judge(ctx, req)
	}
	if err != nil {
		return Result{}, err
	}

	// From here the call was billed, so usage rides along even when the reply is unusable.
	result := Result{Usage: resp.Usage, Cost: resp.Cost}
	fail := func(err error) (Result, error) {
		result.Score = ErrorScore()
		result.Reasoning = err.Error()
		return result, nil
	}
	if resp.Truncated {
		return fail(errors.New("the judge reply was cut off by the token limit; raise the model's max tokens"))
	}
	if strings.TrimSpace(resp.Text) == "" {
		return fail(errors.New("the judge returned an empty reply"))
	}
	object, err := firstJSONObject(resp.Text)
	if err != nil {
		return fail(err)
	}
	rawScore := gjson.Get(object, "score")
	if !rawScore.Exists() || rawScore.Type == gjson.Null {
		return fail(errors.New(`the judge reply has no "score" field`))
	}
	score, err := scoreFromJudge(e.cfg.GradingStyle, rawScore)
	if err != nil {
		return fail(err)
	}
	result.Score = score
	result.Reasoning = reasoningText(gjson.Get(object, "reasoning"))
	return result, nil
}

// systemPrompt builds the system message. It follows Maxim's contract: the user's
// instructions verbatim, the variables to expect, a grading clause, and a strict JSON
// instruction. It holds no per-entry data, so it is identical across a run.
func (e *aiEvaluator) systemPrompt() string {
	var b strings.Builder
	b.WriteString(e.cfg.Instructions)
	if len(e.variables) > 0 {
		b.WriteString("\n\nYou will be provided with one or more of the following: ")
		for i, name := range e.variables {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("{{")
			b.WriteString(name)
			b.WriteString("}}")
		}
		b.WriteString(".")
	}
	b.WriteString("\n\n")
	b.WriteString(e.gradingClause())
	b.WriteString("\n\nRespond with only a JSON object of the form {\"score\": <score>, \"reasoning\": \"<why you gave this score>\"}. Do not add any other text.")
	return b.String()
}

// gradingClause tells the judge what kind of score to return.
func (e *aiEvaluator) gradingClause() string {
	switch e.cfg.GradingStyle {
	case GradingStyleYesNo:
		return "Grade with a Boolean score: true if the requirement is met, false if it is not."
	case GradingStyleScale:
		scale := e.scale()
		return fmt.Sprintf("Grade with a numeric score from %s to %s.", formatNumber(scale.Min), formatNumber(scale.Max))
	case GradingStyleMultiselect:
		var b strings.Builder
		b.WriteString("Grade with a score that is exactly one of these options: ")
		for i, option := range e.options() {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strconv.Quote(option))
		}
		b.WriteString(".")
		return b.String()
	default:
		return "Grade with a numeric score. The score must be a number."
	}
}

// responseSchema is the JSON schema the judge is asked to follow.
func (e *aiEvaluator) responseSchema() map[string]any {
	var score map[string]any
	switch e.cfg.GradingStyle {
	case GradingStyleYesNo:
		score = map[string]any{"type": "boolean"}
	case GradingStyleScale:
		scale := e.scale()
		score = map[string]any{"type": "number", "minimum": scale.Min, "maximum": scale.Max}
	case GradingStyleMultiselect:
		options := e.options()
		enum := make([]any, len(options))
		for i, option := range options {
			enum[i] = option
		}
		score = map[string]any{"type": "string", "enum": enum}
	default:
		score = map[string]any{"type": "number"}
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reasoning": map[string]any{"type": "string"},
			"score":     score,
		},
		"required":             []any{"reasoning", "score"},
		"additionalProperties": false,
	}
}

// userPrompt renders the variable values as {{name}} = "value" lines. Variables the
// input does not carry are left out.
func (e *aiEvaluator) userPrompt(in Input) string {
	// Sized up front so a long transcript is copied once, not regrown as it is appended.
	size := 0
	for _, name := range e.variables {
		if value, present := in.Variables[name]; present {
			size += len(name) + len(value) + 10
		}
	}
	var b strings.Builder
	b.Grow(size)
	for _, name := range e.variables {
		value, present := in.Variables[name]
		if !present {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("{{")
		b.WriteString(name)
		b.WriteString("}} = \"")
		b.WriteString(value)
		b.WriteString("\"")
	}
	return b.String()
}

func (e *aiEvaluator) scale() Scale {
	if e.cfg.Scale != nil {
		return *e.cfg.Scale
	}
	return Scale{Min: defaultScaleMin, Max: defaultScaleMax}
}

func (e *aiEvaluator) options() []string {
	return e.cfg.PassCriteria.EntryLevel.Options
}

// scoreFromJudge converts the judge's score field to a typed score for the grading
// style. Judges are loose with types, so the obvious spellings are accepted.
func scoreFromJudge(style GradingStyle, raw gjson.Result) (Score, error) {
	switch style {
	case GradingStyleYesNo:
		switch raw.Type {
		case gjson.True:
			return BoolScore(true), nil
		case gjson.False:
			return BoolScore(false), nil
		case gjson.String:
			switch strings.ToLower(strings.TrimSpace(raw.String())) {
			case "yes", "true":
				return BoolScore(true), nil
			case "no", "false":
				return BoolScore(false), nil
			}
		}
		return Score{}, fmt.Errorf("the judge returned %s where a boolean score was expected", raw.Raw)
	case GradingStyleMultiselect:
		if raw.Type == gjson.String {
			return StringScore(strings.TrimSpace(raw.String())), nil
		}
		return Score{}, fmt.Errorf("the judge returned %s where one of the options was expected", raw.Raw)
	default:
		switch raw.Type {
		case gjson.Number:
			return NumberScore(raw.Float()), nil
		case gjson.String:
			if v, ok := parseNumber(raw.String()); ok {
				return NumberScore(v), nil
			}
		}
		return Score{}, fmt.Errorf("the judge returned %s where a numeric score was expected", raw.Raw)
	}
}

func formatNumber(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
