package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// scoreCriteriaLevels validates a score question's criteria and returns its
// ordered levels. The shared decision contract requires an ordered array of
// 2-10 level descriptions; missing, non-array, or out-of-range criteria are
// rejected so an emulated score is never generated or accepted without a rubric.
func scoreCriteriaLevels(criteria any) ([]any, error) {
	var levels []any
	switch typed := criteria.(type) {
	case []any:
		levels = typed
	case []string:
		for _, l := range typed {
			levels = append(levels, l)
		}
	default:
		return nil, fmt.Errorf("score criteria must be an ordered array of level descriptions")
	}
	if len(levels) < 2 || len(levels) > 10 {
		return nil, fmt.Errorf("score criteria must have 2-10 levels, got %d", len(levels))
	}
	return levels, nil
}

// validateConfidence rejects a missing, non-finite, or out-of-range confidence.
// The emulation schema requires confidence in [0,1]; a model can still omit it or
// return null/NaN, which must not become a fabricated successful answer.
func validateConfidence(name string, c *float64) error {
	if c == nil {
		return fmt.Errorf("answer for %q is missing confidence", name)
	}
	if math.IsNaN(*c) || math.IsInf(*c, 0) || *c < 0 || *c > 1 {
		return fmt.Errorf("confidence for %q is outside [0,1]: %v", name, *c)
	}
	return nil
}

// normalizeProbabilities rejects missing options, invalid values, or a total far
// from 1. Small rounding errors are tolerated but never forwarded to callers.
func normalizeProbabilities(name string, probs map[string]float64, allowed map[string]bool) (map[string]float64, error) {
	if len(probs) == 0 {
		return nil, nil
	}
	var sum float64
	for k, v := range probs {
		if allowed != nil && !allowed[k] {
			return nil, fmt.Errorf("probabilities for %q contain unknown key %q", name, k)
		}
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
			return nil, fmt.Errorf("probability for %q key %q is outside [0,1]: %v", name, k, v)
		}
		sum += v
	}
	// The contract is a full distribution: every option or level carries a
	// probability. A partial map that happens to sum to 1 (a one-hot answer
	// over a subset) is incomplete, not valid.
	for k := range allowed {
		if _, ok := probs[k]; !ok {
			return nil, fmt.Errorf("probabilities for %q are missing key %q; every option or level requires a probability", name, k)
		}
	}
	// Decimal probabilities can land just outside the boundary in binary
	// floating point (for example, 0.95 differs from 1 by slightly over 0.05).
	if math.Abs(sum-1) > 0.05+1e-12 {
		return nil, fmt.Errorf("probabilities for %q sum to %v, expected about 1", name, sum)
	}
	normalized := make(map[string]float64, len(probs))
	if math.Abs(sum-1) <= 1e-12 {
		for k, v := range probs {
			normalized[k] = v
		}
		return normalized, nil
	}
	for k, v := range probs {
		normalized[k] = v / sum
	}
	return normalized, nil
}

// Decision emulation lets any tool-capable chat model answer a decision request.
// The question set is encoded as a single function tool whose flat result object
// carries one property per question; the model fills value + confidence (and a
// probability distribution for choice). ParseDecisionAnswers maps the tool-call
// arguments back to the neutral DecisionAnswer shape, validating each value
// against its question kind the way the typesafe provider validates native
// answers (core/providers/typesafe/decision.go).

// DecisionToolName is the synthetic function tool the model is forced to call.
const DecisionToolName = "emit_decision"

// numberSchema builds a JSON-schema fragment for a bounded/unbounded number.
func numberSchema(desc string, min, max *float64) map[string]interface{} {
	s := map[string]interface{}{"type": "number"}
	if desc != "" {
		s["description"] = desc
	}
	if min != nil {
		s["minimum"] = *min
	}
	if max != nil {
		s["maximum"] = *max
	}
	return s
}

// confidenceSchema is the shared 0..1 confidence field on every answer.
func confidenceSchema() map[string]interface{} {
	zero, one := 0.0, 1.0
	return numberSchema("Your confidence in this answer, from 0 to 1.", &zero, &one)
}

// scoreIndexKeys returns the string level indexes "0".."n-1" of a score
// question - the probability-map keys shared by the schema and the parser.
func scoreIndexKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = strconv.Itoa(i)
	}
	return keys
}

// probabilitiesSchema builds a closed probability-distribution object: every
// allowed key (choice option or score index) is an enumerated required
// property bounded to [0,1], and additionalProperties is false, so a
// schema-enforcing provider rejects stray keys and out-of-range values before
// the parser ever sees them. The ~1 total is validated parser-side only - the
// schema cannot express an approximate sum.
func probabilitiesSchema(desc string, keys []string) map[string]interface{} {
	zero, one := 0.0, 1.0
	keyProps := make(map[string]interface{}, len(keys))
	for _, key := range keys {
		keyProps[key] = numberSchema("", &zero, &one)
	}
	return map[string]interface{}{
		"type":                 "object",
		"description":          desc,
		"properties":           keyProps,
		"required":             keys,
		"additionalProperties": false,
	}
}

// instructionsText renders a question's instructions (string or structured) into
// a description string for the schema.
func instructionsText(instructions interface{}) string {
	if instructions == nil {
		return ""
	}
	return renderStructuredText(instructions)
}

// renderStructuredText renders a criteria or instructions value for prompt
// text: strings pass through, structured values serialize with sorted keys so
// the emitted schema stays byte-stable, nil renders empty.
func renderStructuredText(value any) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	if raw, err := MarshalSorted(value); err == nil {
		return string(raw)
	}
	return ""
}

// choiceOptions extracts the ordered option keys from a choice question's
// criteria (a map of option -> description). Sorted for deterministic schemas.
// Descriptions may be strings, structured values (rendered as JSON), or null
// (rendered empty), matching the criteria shapes the typesafe API accepts.
func choiceOptions(criteria interface{}) ([]string, map[string]string, error) {
	descs := map[string]string{}
	switch typed := criteria.(type) {
	case map[string]string:
		for k, v := range typed {
			descs[k] = v
		}
	case map[string]any:
		for k, v := range typed {
			descs[k] = renderStructuredText(v)
		}
	default:
		return nil, nil, fmt.Errorf("choice criteria must be a map of options")
	}
	// A choice with no options would emit enum: [] and make every answer
	// unparseable; fail locally before any model is called.
	if len(descs) == 0 {
		return nil, nil, fmt.Errorf("choice criteria requires at least one option")
	}
	opts := make([]string, 0, len(descs))
	for k := range descs {
		opts = append(opts, k)
	}
	sort.Strings(opts)
	return opts, descs, nil
}

// scoreLevels renders a score question's ordered criteria into a description.
func scoreLevels(criteria interface{}) string {
	var levels []interface{}
	switch typed := criteria.(type) {
	case []interface{}:
		levels = typed
	case []string:
		for _, l := range typed {
			levels = append(levels, l)
		}
	default:
		return ""
	}
	var out strings.Builder
	out.WriteString("Score levels (index -> meaning): ")
	for i, l := range levels {
		if i > 0 {
			out.WriteString(", ")
		}
		fmt.Fprintf(&out, "%d=%s", i, renderStructuredText(l))
	}
	return out.String()
}

// noulCriteriaText renders the optional true/false rubric descriptions for
// the noul value description, skipping absent keys. Like the choice and score
// rubrics, these are part of the question and must reach the model.
func noulCriteriaText(criteria any) string {
	var m map[string]any
	switch typed := criteria.(type) {
	case map[string]string:
		m = make(map[string]any, len(typed))
		for k, v := range typed {
			m[k] = v
		}
	case map[string]any:
		m = typed
	default:
		return ""
	}
	var out strings.Builder
	for _, key := range []string{"true", "false"} {
		desc := renderStructuredText(m[key])
		if desc == "" {
			continue
		}
		if out.Len() == 0 {
			out.WriteString(" Meaning (answer: meaning): ")
		} else {
			out.WriteString(", ")
		}
		out.WriteString(key)
		out.WriteByte('=')
		out.WriteString(desc)
	}
	return out.String()
}

// choiceOptionsText renders the option rubric descriptions for the choice
// property description, skipping options with no description. The rubric is
// part of the question; the model must see it, not only the enum keys.
func choiceOptionsText(opts []string, descs map[string]string) string {
	var out strings.Builder
	for _, opt := range opts {
		desc := descs[opt]
		if desc == "" {
			continue
		}
		if out.Len() == 0 {
			out.WriteString(" Options (option: meaning): ")
		} else {
			out.WriteString(", ")
		}
		out.WriteString(opt)
		out.WriteByte('=')
		out.WriteString(desc)
	}
	return out.String()
}

// scoreLegend builds the level index -> description legend from score criteria,
// so the emulated answer carries the same legend a native provider would: each
// level's description echoed verbatim (string, object, or array).
func scoreLegend(criteria interface{}) map[string]any {
	var levels []interface{}
	switch typed := criteria.(type) {
	case []interface{}:
		levels = typed
	case []string:
		for _, l := range typed {
			levels = append(levels, l)
		}
	default:
		return nil
	}
	if len(levels) == 0 {
		return nil
	}
	legend := make(map[string]any, len(levels))
	for i, l := range levels {
		legend[fmt.Sprintf("%d", i)] = l
	}
	return legend
}

// BuildDecisionSchema builds the tool parameters: an object with one nested
// object property per question (value/choice + confidence, plus probabilities
// for choice). Property names are the question identifiers.
func BuildDecisionSchema(questions map[string]schemas.DecisionQuestion) (*schemas.ToolFunctionParameters, error) {
	if len(questions) == 0 {
		return nil, fmt.Errorf("decision emulation requires at least one question")
	}
	names := make([]string, 0, len(questions))
	for name := range questions {
		names = append(names, name)
	}
	sort.Strings(names)

	props := schemas.NewOrderedMapWithCapacity(len(names))
	zero, one := 0.0, 1.0
	for _, name := range names {
		q := questions[name]
		desc := instructionsText(q.Instructions)
		var nested map[string]interface{}
		switch q.Kind {
		case schemas.DecisionKindNoul:
			// The native noul answer is {type, noul} only - the value near 0.5
			// is the uncertainty signal - so confidence is an optional extra
			// the model may volunteer, never a required field.
			nested = map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"value":      numberSchema("Probability from 0 to 1. "+desc+noulCriteriaText(q.Criteria), &zero, &one),
					"confidence": confidenceSchema(),
				},
				"required":             []string{"value"},
				"additionalProperties": false,
			}
		case schemas.DecisionKindChoice:
			opts, descs, err := choiceOptions(q.Criteria)
			if err != nil {
				return nil, fmt.Errorf("question %q: %w", name, err)
			}
			nested = map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"choice":        map[string]interface{}{"type": "string", "enum": opts, "description": desc + " Select an option with the highest probability." + choiceOptionsText(opts, descs)},
					"confidence":    confidenceSchema(),
					"probabilities": probabilitiesSchema("Required. Probability for each option; values must sum to 1.", opts),
				},
				"required":             []string{"choice", "confidence", "probabilities"},
				"additionalProperties": false,
			}
		case schemas.DecisionKindScore:
			scoreLevelsList, err := scoreCriteriaLevels(q.Criteria)
			if err != nil {
				return nil, fmt.Errorf("question %q: %w", name, err)
			}
			// The score is the probability-weighted average of the level
			// indexes, so it is bounded by the first and last index.
			maxLevel := float64(len(scoreLevelsList) - 1)
			nested = map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"value":      numberSchema(desc+" "+scoreLevels(q.Criteria), &zero, &maxLevel),
					"confidence": confidenceSchema(),
					"probabilities": probabilitiesSchema(
						"Required. Probability for each level index (\"0\", \"1\", ...); values must sum to 1.",
						scoreIndexKeys(len(scoreLevelsList)),
					),
				},
				"required":             []string{"value", "confidence", "probabilities"},
				"additionalProperties": false,
			}
		default:
			return nil, fmt.Errorf("question %q has unsupported kind %q", name, q.Kind)
		}
		props.Set(name, nested)
	}

	return &schemas.ToolFunctionParameters{
		Type:       "object",
		Properties: props,
		Required:   names,
	}, nil
}

// DecisionToolDescription frames the forced function call for the model.
const DecisionToolDescription = "Emit the decision for every question. Fill each field from the given state."

// BuildDecisionResponsesTool wraps the decision schema in a forced-callable
// Responses-API function tool. Decision emulation runs through provider.Responses
// (the richest interface every provider implements - natively on openai/anthropic/
// gemini, via chat translation elsewhere), so the tool is expressed in the
// Responses tool shape rather than the chat one.
func BuildDecisionResponsesTool(questions map[string]schemas.DecisionQuestion) (*schemas.ResponsesTool, error) {
	params, err := BuildDecisionSchema(questions)
	if err != nil {
		return nil, err
	}
	name := DecisionToolName
	desc := DecisionToolDescription
	return &schemas.ResponsesTool{
		Type:        schemas.ResponsesToolTypeFunction,
		Name:        &name,
		Description: &desc,
		ResponsesToolFunction: &schemas.ResponsesToolFunction{
			Parameters: params,
		},
	}, nil
}

// emulatedAnswer is the per-question object the model returns.
type emulatedAnswer struct {
	Value         interface{}        `json:"value"`
	Choice        *string            `json:"choice"`
	Confidence    *float64           `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

// emulatedAnswerFields are the keys an emulatedAnswer object may carry.
var emulatedAnswerFields = map[string]bool{"value": true, "choice": true, "confidence": true, "probabilities": true}

// leakedParameterTagPrefix opens Anthropic's XML tool-call parameter syntax.
const leakedParameterTagPrefix = `<parameter name="`

// recoverFlattenedDecisionArguments rebuilds per-question answer objects from
// arguments a model flattened into the top level. Claude models on large nested
// schemas (seen on openrouter/anthropic/claude-opus-4.1) emit
//
//	{"category": "<parameter name=\"choice\">billing", "confidence": 1.0, "probabilities": {...}, "urgency": ...}
//
// leaking the XML parameter syntax into the first field and repeating the rest
// as duplicate top-level keys. Keys are read in order with a streaming decoder
// because a map decode would keep only the last duplicate. Each answer field
// attaches to the question that precedes it; anything else (a field before any
// question, a repeated field, an unknown key, a string answer without the tag)
// fails recovery. The result still goes through the full answer validation, so
// recovery can only restore answers the model gave, never invent one.
func recoverFlattenedDecisionArguments(argumentsJSON []byte, questions map[string]schemas.DecisionQuestion) (map[string]emulatedAnswer, bool) {
	dec := json.NewDecoder(bytes.NewReader(argumentsJSON))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	fieldsByQuestion := make(map[string]map[string]json.RawMessage, len(questions))
	var current map[string]json.RawMessage
	for dec.More() {
		tok, err := dec.Token()
		key, isKey := tok.(string)
		if err != nil || !isKey {
			return nil, false
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		if _, isQuestion := questions[key]; isQuestion {
			if _, seen := fieldsByQuestion[key]; seen {
				return nil, false
			}
			if len(value) > 0 && value[0] == '{' {
				// A well-formed answer object among flattened ones is complete
				// on its own; no sibling field may attach to it.
				var fields map[string]json.RawMessage
				if err := sonic.Unmarshal(value, &fields); err != nil {
					return nil, false
				}
				fieldsByQuestion[key] = fields
				current = nil
				continue
			}
			field, fieldValue, ok := parseLeakedParameterTag(value)
			if !ok {
				return nil, false
			}
			current = map[string]json.RawMessage{field: fieldValue}
			fieldsByQuestion[key] = current
			continue
		}
		if current == nil || !emulatedAnswerFields[key] {
			return nil, false
		}
		if _, repeated := current[key]; repeated {
			return nil, false
		}
		current[key] = value
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}

	recovered := make(map[string]emulatedAnswer, len(fieldsByQuestion))
	for name, fields := range fieldsByQuestion {
		encoded, err := MarshalSorted(fields)
		if err != nil {
			return nil, false
		}
		var answer emulatedAnswer
		if err := sonic.Unmarshal(encoded, &answer); err != nil {
			return nil, false
		}
		recovered[name] = answer
	}
	return recovered, true
}

// parseLeakedParameterTag reads a `<parameter name="field">value` string (the
// closing tag is optional) into the answer field it names and that field's JSON
// value. A choice stays a string; any other value must itself be valid JSON.
func parseLeakedParameterTag(value json.RawMessage) (string, json.RawMessage, bool) {
	var s string
	if err := sonic.Unmarshal(value, &s); err != nil || !strings.HasPrefix(s, leakedParameterTagPrefix) {
		return "", nil, false
	}
	rest := s[len(leakedParameterTagPrefix):]
	end := strings.Index(rest, `">`)
	if end < 0 {
		return "", nil, false
	}
	field := rest[:end]
	if !emulatedAnswerFields[field] {
		return "", nil, false
	}
	text := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest[end+2:]), "</parameter>"))
	if field == "choice" {
		encoded, err := sonic.Marshal(text)
		if err != nil {
			return "", nil, false
		}
		return field, encoded, true
	}
	if !json.Valid([]byte(text)) {
		return "", nil, false
	}
	return field, json.RawMessage(text), true
}

// ParseDecisionAnswers decodes the tool-call arguments and validates each answer
// against its question kind, producing the neutral DecisionAnswer map. Every
// requested question must be answered; a missing, wrong-typed, or out-of-range
// answer is an error so the request falls through to the next fallback rather
// than returning a fabricated result.
func ParseDecisionAnswers(argumentsJSON []byte, questions map[string]schemas.DecisionQuestion) (map[string]schemas.DecisionAnswer, error) {
	var raw map[string]emulatedAnswer
	if err := sonic.Unmarshal(argumentsJSON, &raw); err != nil {
		recovered, ok := recoverFlattenedDecisionArguments(argumentsJSON, questions)
		if !ok {
			return nil, fmt.Errorf("decision tool-call arguments are not a JSON object: %w", err)
		}
		raw = recovered
	}

	answers := make(map[string]schemas.DecisionAnswer, len(questions))
	for name, q := range questions {
		got, ok := raw[name]
		if !ok {
			return nil, fmt.Errorf("model returned no answer for question %q", name)
		}
		// Confidence is required on choice and score answers; a noul answer
		// carries none natively, so there it is validated only when the model
		// volunteers one.
		if q.Kind != schemas.DecisionKindNoul || got.Confidence != nil {
			if err := validateConfidence(name, got.Confidence); err != nil {
				return nil, err
			}
		}
		answer := schemas.DecisionAnswer{Kind: q.Kind, Confidence: got.Confidence}
		switch q.Kind {
		case schemas.DecisionKindNoul:
			f, ok := toFloat(got.Value)
			if !ok {
				return nil, fmt.Errorf("noul answer for %q is not a number", name)
			}
			if f < 0 || f > 1 {
				return nil, fmt.Errorf("noul answer for %q is outside [0,1]: %v", name, f)
			}
			answer.Value = f
		case schemas.DecisionKindChoice:
			if got.Choice == nil {
				return nil, fmt.Errorf("choice answer for %q carries no choice", name)
			}
			opts, _, err := choiceOptions(q.Criteria)
			if err != nil {
				return nil, fmt.Errorf("question %q: %w", name, err)
			}
			if !containsString(opts, *got.Choice) {
				return nil, fmt.Errorf("choice answer %q for %q is not an allowed option", *got.Choice, name)
			}
			// Probabilities are a required field on native choice answers; an
			// answer without them would fail strict SDK clients downstream.
			if len(got.Probabilities) == 0 {
				return nil, fmt.Errorf("choice answer for %q is missing probabilities", name)
			}
			allowed := make(map[string]bool, len(opts))
			for _, o := range opts {
				allowed[o] = true
			}
			probabilities, err := normalizeProbabilities(name, got.Probabilities, allowed)
			if err != nil {
				return nil, err
			}
			for option, probability := range probabilities {
				if probability > probabilities[*got.Choice] {
					return nil, fmt.Errorf("choice answer %q for %q does not have the highest probability (option %q is higher)", *got.Choice, name, option)
				}
			}
			answer.Value = *got.Choice
			answer.Probabilities = probabilities
		case schemas.DecisionKindScore:
			levels, err := scoreCriteriaLevels(q.Criteria)
			if err != nil {
				return nil, fmt.Errorf("score question %q: %w", name, err)
			}
			if _, ok := toFloat(got.Value); !ok {
				return nil, fmt.Errorf("score answer for %q is not a number", name)
			}
			// Probabilities are a required field on native score answers; an
			// answer without them would fail strict SDK clients downstream.
			if len(got.Probabilities) == 0 {
				return nil, fmt.Errorf("score answer for %q is missing probabilities", name)
			}
			legend := scoreLegend(q.Criteria)
			allowed := make(map[string]bool, len(levels))
			for i := range levels {
				allowed[fmt.Sprintf("%d", i)] = true
			}
			probabilities, err := normalizeProbabilities(name, got.Probabilities, allowed)
			if err != nil {
				return nil, err
			}
			// The native contract defines score as the probability-weighted
			// average of the level indexes ("Expected score ... may fall
			// between integer levels"), so derive it from the validated
			// distribution; the model's separately supplied value (checked
			// above only for being numeric) cannot contradict its own
			// probabilities. Bounded 0..len(levels)-1 by construction.
			var derived float64
			for key, p := range probabilities {
				index, _ := strconv.Atoi(key) // keys already validated against the "0".."n-1" set
				derived += float64(index) * p
			}
			answer.Value = derived
			answer.Legend = legend
			answer.Probabilities = probabilities
		default:
			return nil, fmt.Errorf("question %q has unsupported kind %q", name, q.Kind)
		}
		answers[name] = answer
	}
	return answers, nil
}

func toFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
