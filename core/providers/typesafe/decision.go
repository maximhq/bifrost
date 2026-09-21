package typesafe

import (
	"fmt"
	"reflect"

	"github.com/tidwall/gjson"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// typesafeMaxChoiceOptions is Typesafe's documented cap on choice options.
const typesafeMaxChoiceOptions = 255

// typesafeMinScoreLevels and typesafeMaxScoreLevels bound a score criteria array.
const (
	typesafeMinScoreLevels = 2
	typesafeMaxScoreLevels = 10
)

// supportedDecisionKinds is the set of kinds Typesafe serves. The Bifrost kind
// vocabulary mirrors the native question types, so the mapping is identity.
var supportedDecisionKinds = map[schemas.DecisionKind]string{
	schemas.DecisionKindNoul:   TypesafeQuestionTypeNoul,
	schemas.DecisionKindChoice: TypesafeQuestionTypeChoice,
	schemas.DecisionKindScore:  TypesafeQuestionTypeScore,
}

// validStructuredValue reports whether a state or instructions value
// serializes to one of the documented JSON shapes: string, object, or array.
// Marshaling the value answers that exactly: pointers dereference, typed
// maps/slices/structs normalize, custom marshalers speak for themselves, and
// unmarshalable values (func, chan) fail closed with a local 400 instead of a
// request-marshal 500 later. Only the value is encoded for inspection; the
// request body is serialized once, unchanged, on the send path.
func validStructuredValue(value interface{}) bool {
	data, err := providerUtils.MarshalSorted(value)
	if err != nil {
		return false
	}
	for _, c := range data {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return c == '"' || c == '{' || c == '['
	}
	return false
}

// ToTypesafeDecisionRequest converts a Bifrost decision request into
// Typesafe's native systemone shape. Unsupported kinds and malformed criteria
// are rejected rather than silently approximated.
func ToTypesafeDecisionRequest(request *schemas.BifrostDecisionRequest) (*TypesafeDecisionRequest, error) {
	if len(request.Questions) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("decision request requires at least one question")
	}
	if !validStructuredValue(request.State) {
		return nil, providerUtils.InvalidRequestErrorf("state must be a string, object, or array")
	}

	questions := make(map[string]TypesafeQuestion, len(request.Questions))
	for name, question := range request.Questions {
		native, err := toTypesafeQuestion(name, question)
		if err != nil {
			return nil, err
		}
		questions[name] = *native
	}

	return &TypesafeDecisionRequest{
		State:     request.State,
		Model:     request.Model,
		Questions: questions,
	}, nil
}

// toTypesafeQuestion validates one question and maps it to the native shape.
func toTypesafeQuestion(name string, question schemas.DecisionQuestion) (*TypesafeQuestion, error) {
	nativeType, ok := supportedDecisionKinds[question.Kind]
	if !ok {
		return nil, providerUtils.InvalidRequestErrorf("question %q has unsupported kind %q; expected noul, choice, or score", name, question.Kind)
	}
	if question.Instructions == nil {
		return nil, providerUtils.InvalidRequestErrorf("question %q has no instructions", name)
	}
	if !validStructuredValue(question.Instructions) {
		return nil, providerUtils.InvalidRequestErrorf("question %q instructions must be a string, object, or array", name)
	}

	native := TypesafeQuestion{Type: nativeType, Instructions: question.Instructions}

	switch question.Kind {
	case schemas.DecisionKindNoul:
		criteria, err := noulCriteria(name, question.Criteria)
		if err != nil {
			return nil, err
		}
		native.Criteria = criteria
	case schemas.DecisionKindChoice:
		criteria, err := choiceCriteria(name, question.Criteria)
		if err != nil {
			return nil, err
		}
		native.Criteria = criteria
	case schemas.DecisionKindScore:
		criteria, err := scoreCriteria(name, question.Criteria)
		if err != nil {
			return nil, err
		}
		native.Criteria = criteria
	}

	return &native, nil
}

// noulCriteria validates optional noul criteria: a map whose only keys are
// "true" and "false", each described by a string, object, or array. Passed
// through losslessly.
func noulCriteria(name string, criteria interface{}) (interface{}, error) {
	if criteria == nil {
		return nil, nil
	}
	m, err := criteriaAsMap(name, criteria, false)
	if err != nil {
		return nil, err
	}
	for key := range m {
		if key != "true" && key != "false" {
			return nil, providerUtils.InvalidRequestErrorf("question %q noul criteria allows only \"true\" and \"false\" keys, got %q", name, key)
		}
	}
	return m, nil
}

// choiceCriteria validates the required option -> description map. A
// description is a string, object, or array, or null when an option needs no
// extra detail.
func choiceCriteria(name string, criteria interface{}) (map[string]any, error) {
	if criteria == nil {
		return nil, providerUtils.InvalidRequestErrorf("question %q has kind choice and requires criteria options", name)
	}
	m, err := criteriaAsMap(name, criteria, true)
	if err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("question %q has kind choice and requires criteria options", name)
	}
	if len(m) > typesafeMaxChoiceOptions {
		return nil, providerUtils.InvalidRequestErrorf("question %q has %d choice options; Typesafe allows at most %d", name, len(m), typesafeMaxChoiceOptions)
	}
	return m, nil
}

// scoreCriteria validates the ordered array of 2-10 level descriptions, each
// a string, object, or array, preserved losslessly. Go SDK callers supply
// []string; HTTP JSON decoding supplies []interface{} - both are the same
// ordered array on the wire.
func scoreCriteria(name string, criteria interface{}) (interface{}, error) {
	var levels []any
	switch typed := criteria.(type) {
	case []any:
		levels = typed
	case []string:
		levels = make([]any, len(typed))
		for i, level := range typed {
			levels[i] = level
		}
	default:
		// Go SDK callers pass typed slices ([]Rubric, [][]string, named slice
		// types). Any slice or array is an ordered list of level descriptions;
		// elements are validated by serialized shape below, exactly like the
		// untyped forms, and carried losslessly.
		value := reflect.ValueOf(criteria)
		if !value.IsValid() || (value.Kind() != reflect.Slice && value.Kind() != reflect.Array) {
			return nil, providerUtils.InvalidRequestErrorf("question %q has kind score and requires criteria as an ordered array of level descriptions", name)
		}
		levels = make([]any, value.Len())
		for i := range levels {
			levels[i] = value.Index(i).Interface()
		}
	}
	if len(levels) < typesafeMinScoreLevels || len(levels) > typesafeMaxScoreLevels {
		return nil, providerUtils.InvalidRequestErrorf("question %q score criteria must have between %d and %d levels, got %d", name, typesafeMinScoreLevels, typesafeMaxScoreLevels, len(levels))
	}
	for i, level := range levels {
		if !validStructuredValue(level) {
			return nil, providerUtils.InvalidRequestErrorf("question %q score criteria level %d must be a string, object, or array", name, i)
		}
	}
	return levels, nil
}

// criteriaAsMap normalizes a criteria value into map[string]any, rejecting
// non-map shapes. Each description must serialize to a string, object, or
// array; null is additionally allowed when allowNull is set (choice options
// that need no extra detail). Descriptions are carried losslessly.
func criteriaAsMap(name string, criteria interface{}, allowNull bool) (map[string]any, error) {
	var m map[string]any
	switch typed := criteria.(type) {
	case map[string]string:
		m = make(map[string]any, len(typed))
		for key, value := range typed {
			m[key] = value
		}
	case map[string]any:
		m = typed
	default:
		// Go SDK callers pass typed maps (map[string]Rubric, map[string][]string,
		// ...). Any map with string keys is a map of named descriptions; its
		// values are validated by serialized shape below, exactly like the
		// untyped forms, and carried losslessly.
		value := reflect.ValueOf(criteria)
		if !value.IsValid() || value.Kind() != reflect.Map || value.Type().Key().Kind() != reflect.String {
			return nil, providerUtils.InvalidRequestErrorf("question %q criteria must be a map of descriptions", name)
		}
		m = make(map[string]any, value.Len())
		iter := value.MapRange()
		for iter.Next() {
			m[iter.Key().String()] = iter.Value().Interface()
		}
	}
	for key, value := range m {
		if isJSONNull(value) {
			if allowNull {
				continue
			}
			return nil, providerUtils.InvalidRequestErrorf("question %q criteria description for %q must be a string, object, or array", name, key)
		}
		if !validStructuredValue(value) {
			return nil, providerUtils.InvalidRequestErrorf("question %q criteria description for %q must be a string, object, or array", name, key)
		}
	}
	return m, nil
}

// isJSONNull reports whether a description serializes to JSON null. Typed nil
// pointers from Go SDK callers (map[string]*Rubric{"other": nil}) are non-nil
// interfaces wrapping nil pointers - value == nil misses them, but on the wire
// they are null exactly like an untyped nil, so both follow the null rules.
func isJSONNull(value any) bool {
	if value == nil {
		return true
	}
	data, err := providerUtils.MarshalSorted(value)
	if err != nil {
		return false
	}
	return gjson.ParseBytes(data).Type == gjson.Null
}

// ToBifrostDecisionResponse converts a native systemone response back into
// the shared decision shape. Every requested question must produce an answer
// of its declared kind.
func ToBifrostDecisionResponse(resp *TypesafeDecisionResponse, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	answers := make(map[string]schemas.DecisionAnswer, len(request.Questions))
	for name, question := range request.Questions {
		native, ok := resp.Answers[name]
		if !ok {
			return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("typesafe returned no answer for question %q", name), nil)
		}
		if expected := supportedDecisionKinds[question.Kind]; native.Type != expected {
			return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("typesafe answered question %q as %q; expected %q", name, native.Type, expected), nil)
		}

		answer := schemas.DecisionAnswer{
			Kind:          question.Kind,
			Confidence:    native.Confidence,
			Probabilities: native.Probabilities,
			Legend:        native.Legend,
		}
		switch question.Kind {
		case schemas.DecisionKindNoul:
			if native.Noul == nil {
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("typesafe noul answer for question %q carries no value", name), nil)
			}
			if *native.Noul < 0 || *native.Noul > 1 {
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("typesafe noul answer for question %q is outside [0,1]: %v", name, *native.Noul), nil)
			}
			answer.Value = *native.Noul
		case schemas.DecisionKindChoice:
			if native.Choice == nil {
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("typesafe choice answer for question %q carries no value", name), nil)
			}
			answer.Value = *native.Choice
		case schemas.DecisionKindScore:
			if native.Score == nil {
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("typesafe score answer for question %q carries no value", name), nil)
			}
			answer.Value = *native.Score
		}
		answers[name] = answer
	}

	response := &schemas.BifrostDecisionResponse{
		Model:   resp.Model,
		Answers: answers,
	}
	if resp.Usage != nil {
		response.Usage = &schemas.BifrostLLMUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		}
	}
	return response, nil
}

// ToBifrostDecisionRequest converts a native systemone request into the
// shared decision shape. Question identifiers become answer field names; the
// native type vocabulary is identical to the Bifrost kind vocabulary.
func (req *TypesafeDecisionRequest) ToBifrostDecisionRequest(ctx *schemas.BifrostContext) (*schemas.BifrostDecisionRequest, error) {
	if req == nil {
		return nil, providerUtils.InvalidRequestErrorf("request body is required")
	}
	if req.State == nil {
		return nil, providerUtils.InvalidRequestErrorf("state is required")
	}
	if len(req.Questions) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("at least one question is required")
	}

	provider, model := schemas.ParseModelString(req.Model, schemas.Typesafe)

	questions := make(map[string]schemas.DecisionQuestion, len(req.Questions))
	for name, question := range req.Questions {
		questions[name] = schemas.DecisionQuestion{
			Kind:         schemas.DecisionKind(question.Type),
			Instructions: question.Instructions,
			Criteria:     question.Criteria,
		}
	}

	return &schemas.BifrostDecisionRequest{
		Provider:  provider,
		Model:     model,
		State:     req.State,
		Questions: questions,
	}, nil
}

// ToTypesafeNativeDecisionResponse converts a shared decision response back
// into Typesafe's native model, answers, and usage shape, covering all three
// answer types.
func ToTypesafeNativeDecisionResponse(resp *schemas.BifrostDecisionResponse) (*TypesafeDecisionResponse, error) {
	if resp == nil {
		return nil, fmt.Errorf("decision response is nil")
	}

	answers := make(map[string]TypesafeAnswer, len(resp.Answers))
	for name, answer := range resp.Answers {
		native := TypesafeAnswer{
			Confidence:    answer.Confidence,
			Probabilities: answer.Probabilities,
			Legend:        answer.Legend,
		}
		switch answer.Kind {
		case schemas.DecisionKindNoul:
			number, ok := answer.Value.(float64)
			if !ok {
				return nil, fmt.Errorf("decision answer %q is not a number", name)
			}
			native.Type = TypesafeQuestionTypeNoul
			native.Noul = &number
		case schemas.DecisionKindChoice:
			choice, ok := answer.Value.(string)
			if !ok {
				return nil, fmt.Errorf("decision answer %q is not a string", name)
			}
			native.Type = TypesafeQuestionTypeChoice
			native.Choice = &choice
		case schemas.DecisionKindScore:
			number, ok := answer.Value.(float64)
			if !ok {
				return nil, fmt.Errorf("decision answer %q is not a number", name)
			}
			native.Type = TypesafeQuestionTypeScore
			native.Score = &number
		default:
			return nil, fmt.Errorf("decision answer %q has unknown kind %q", name, answer.Kind)
		}
		answers[name] = native
	}

	native := &TypesafeDecisionResponse{
		Model:   resp.Model,
		Answers: answers,
	}
	if resp.Usage != nil {
		native.Usage = &TypesafeUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		}
	}
	return native, nil
}
