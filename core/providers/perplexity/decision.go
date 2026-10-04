package perplexity

import (
	"fmt"
	"reflect"

	"github.com/tidwall/gjson"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

const (
	perplexityDecisionModel        = "pplx-decider-v1-27b"
	perplexityMaxDecisionBodyBytes = 32 << 20
	perplexityMaxQuestions         = 128
	perplexityMaxChoiceOptions     = 255
	perplexityMinScoreLevels       = 1
	perplexityMaxScoreLevels       = 10
	perplexityQuestionTypeNoul     = "noul"
	perplexityQuestionTypeChoice   = "choice"
	perplexityQuestionTypeScore    = "score"
)

var perplexityDecisionKinds = map[schemas.DecisionKind]string{
	schemas.DecisionKindNoul:   perplexityQuestionTypeNoul,
	schemas.DecisionKindChoice: perplexityQuestionTypeChoice,
	schemas.DecisionKindScore:  perplexityQuestionTypeScore,
}

func toPerplexityDecisionRequest(request *schemas.BifrostDecisionRequest) (*PerplexityDecisionRequest, error) {
	if request == nil {
		return nil, providerUtils.InvalidRequestErrorf("decision request is required")
	}
	if len(request.Questions) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("decision request requires at least one question")
	}
	if len(request.Questions) > perplexityMaxQuestions {
		return nil, providerUtils.InvalidRequestErrorf("decision request has %d questions; Perplexity allows at most %d", len(request.Questions), perplexityMaxQuestions)
	}
	if !perplexityStructuredValue(request.State) {
		return nil, providerUtils.InvalidRequestErrorf("state must be a string, object, or array")
	}

	questions := make(map[string]PerplexityDecisionQuestion, len(request.Questions))
	for name, question := range request.Questions {
		native, err := toPerplexityDecisionQuestion(name, question)
		if err != nil {
			return nil, err
		}
		questions[name] = *native
	}

	return &PerplexityDecisionRequest{
		State:     request.State,
		Model:     request.Model,
		Questions: questions,
	}, nil
}

func toPerplexityDecisionQuestion(name string, question schemas.DecisionQuestion) (*PerplexityDecisionQuestion, error) {
	nativeType, ok := perplexityDecisionKinds[question.Kind]
	if !ok {
		return nil, providerUtils.InvalidRequestErrorf("question %q has unsupported kind %q; expected noul, choice, or score", name, question.Kind)
	}
	if question.Instructions == nil {
		return nil, providerUtils.InvalidRequestErrorf("question %q has no instructions", name)
	}
	if !perplexityStructuredValue(question.Instructions) {
		return nil, providerUtils.InvalidRequestErrorf("question %q instructions must be a string, object, or array", name)
	}

	native := &PerplexityDecisionQuestion{
		Type:         nativeType,
		Instructions: question.Instructions,
	}
	switch question.Kind {
	case schemas.DecisionKindNoul:
		criteria, err := perplexityNoulCriteria(name, question.Criteria)
		if err != nil {
			return nil, err
		}
		native.Criteria = criteria
	case schemas.DecisionKindChoice:
		criteria, err := perplexityChoiceCriteria(name, question.Criteria)
		if err != nil {
			return nil, err
		}
		native.Criteria = criteria
	case schemas.DecisionKindScore:
		criteria, err := perplexityScoreCriteria(name, question.Criteria)
		if err != nil {
			return nil, err
		}
		native.Criteria = criteria
	}
	return native, nil
}

func perplexityNoulCriteria(name string, criteria interface{}) (interface{}, error) {
	if criteria == nil {
		return nil, nil
	}
	values, err := perplexityCriteriaMap(name, criteria, false)
	if err != nil {
		return nil, err
	}
	for key := range values {
		if key != "true" && key != "false" {
			return nil, providerUtils.InvalidRequestErrorf("question %q noul criteria allows only \"true\" and \"false\" keys, got %q", name, key)
		}
	}
	return values, nil
}

func perplexityChoiceCriteria(name string, criteria interface{}) (map[string]any, error) {
	if criteria == nil {
		return nil, providerUtils.InvalidRequestErrorf("question %q has kind choice and requires criteria options", name)
	}
	values, err := perplexityCriteriaMap(name, criteria, true)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, providerUtils.InvalidRequestErrorf("question %q has kind choice and requires criteria options", name)
	}
	if len(values) > perplexityMaxChoiceOptions {
		return nil, providerUtils.InvalidRequestErrorf("question %q has %d choice options; Perplexity allows at most %d", name, len(values), perplexityMaxChoiceOptions)
	}
	return values, nil
}

func perplexityScoreCriteria(name string, criteria interface{}) (interface{}, error) {
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
		value := reflect.ValueOf(criteria)
		if !value.IsValid() || (value.Kind() != reflect.Slice && value.Kind() != reflect.Array) {
			return nil, providerUtils.InvalidRequestErrorf("question %q has kind score and requires criteria as an ordered array of level descriptions", name)
		}
		levels = make([]any, value.Len())
		for i := range levels {
			levels[i] = value.Index(i).Interface()
		}
	}
	if len(levels) < perplexityMinScoreLevels || len(levels) > perplexityMaxScoreLevels {
		return nil, providerUtils.InvalidRequestErrorf("question %q score criteria must have between %d and %d levels, got %d", name, perplexityMinScoreLevels, perplexityMaxScoreLevels, len(levels))
	}
	for i, level := range levels {
		if !perplexityStructuredValue(level) {
			return nil, providerUtils.InvalidRequestErrorf("question %q score criteria level %d must be a string, object, or array", name, i)
		}
	}
	return levels, nil
}

func perplexityCriteriaMap(name string, criteria interface{}, allowNull bool) (map[string]any, error) {
	var values map[string]any
	switch typed := criteria.(type) {
	case map[string]string:
		values = make(map[string]any, len(typed))
		for key, value := range typed {
			values[key] = value
		}
	case map[string]any:
		values = typed
	default:
		value := reflect.ValueOf(criteria)
		if !value.IsValid() || value.Kind() != reflect.Map || value.Type().Key().Kind() != reflect.String {
			return nil, providerUtils.InvalidRequestErrorf("question %q criteria must be a map of descriptions", name)
		}
		values = make(map[string]any, value.Len())
		iter := value.MapRange()
		for iter.Next() {
			values[iter.Key().String()] = iter.Value().Interface()
		}
	}
	for key, value := range values {
		if perplexityJSONNull(value) {
			if allowNull {
				continue
			}
			return nil, providerUtils.InvalidRequestErrorf("question %q criteria description for %q must be a string, object, or array", name, key)
		}
		if !perplexityStructuredValue(value) {
			return nil, providerUtils.InvalidRequestErrorf("question %q criteria description for %q must be a string, object, or array", name, key)
		}
	}
	return values, nil
}

func perplexityStructuredValue(value interface{}) bool {
	data, err := providerUtils.MarshalSorted(value)
	if err != nil {
		return false
	}
	for _, character := range data {
		switch character {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return character == '"' || character == '{' || character == '['
	}
	return false
}

func perplexityJSONNull(value any) bool {
	if value == nil {
		return true
	}
	data, err := providerUtils.MarshalSorted(value)
	if err != nil {
		return false
	}
	return gjson.ParseBytes(data).Type == gjson.Null
}

func toBifrostPerplexityDecisionResponse(response *PerplexityDecisionResponse, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	answers := make(map[string]schemas.DecisionAnswer, len(request.Questions))
	for name, question := range request.Questions {
		native, ok := response.Answers[name]
		if !ok {
			return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("perplexity returned no answer for question %q", name), nil)
		}
		expected := perplexityDecisionKinds[question.Kind]
		if native.Type != expected {
			return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("perplexity answered question %q as %q; expected %q", name, native.Type, expected), nil)
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
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("perplexity noul answer for question %q carries no value", name), nil)
			}
			if *native.Noul < 0 || *native.Noul > 1 {
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("perplexity noul answer for question %q is outside [0,1]: %v", name, *native.Noul), nil)
			}
			answer.Value = *native.Noul
		case schemas.DecisionKindChoice:
			if native.Choice == nil {
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("perplexity choice answer for question %q carries no value", name), nil)
			}
			if _, ok := answerChoiceOptions(question.Criteria)[*native.Choice]; !ok {
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("perplexity choice answer for question %q selected unknown option %q", name, *native.Choice), nil)
			}
			answer.Value = *native.Choice
		case schemas.DecisionKindScore:
			if native.Score == nil {
				return nil, providerUtils.NewBifrostOperationError(fmt.Sprintf("perplexity score answer for question %q carries no value", name), nil)
			}
			answer.Value = *native.Score
		}
		answers[name] = answer
	}

	bifrostResponse := &schemas.BifrostDecisionResponse{
		ID:      response.ID,
		Model:   response.Model,
		Answers: answers,
	}
	if response.Usage != nil {
		bifrostResponse.Usage = &schemas.BifrostLLMUsage{
			PromptTokens: response.Usage.InputTokens,
			TotalTokens:  response.Usage.InputTokens,
		}
	}
	return bifrostResponse, nil
}

func answerChoiceOptions(criteria interface{}) map[string]any {
	values, err := perplexityCriteriaMap("choice", criteria, true)
	if err != nil {
		return nil
	}
	return values
}
