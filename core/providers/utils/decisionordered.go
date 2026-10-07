package utils

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// A map-form decision request (State plus the Questions map, see
// schemas.BifrostDecisionRequest) is served natively by a provider that speaks
// the ordered form by converting it with DecisionMapToOrdered and converting
// the ordered answers back with DecisionOrderedAnswersToMap. Both read the map
// form through the same helpers decision emulation uses, so a request means
// the same thing whether it is emulated or served natively.

// DecisionMapToOrdered converts a map-form decision request into the ordered
// form: the state becomes the input text, and each question becomes a named
// ordered question, sorted by name so the converted request is deterministic.
// A noul question becomes a predicate whose criteria, which a predicate cannot
// carry, are appended to its instructions; choice options become choices and
// score levels become levels labelled by their index. Structured state,
// instructions, and descriptions are rendered as sorted JSON text.
func DecisionMapToOrdered(request *schemas.BifrostDecisionRequest) (*schemas.DecisionInput, []schemas.DecisionOrderedQuestion, error) {
	if request == nil || !request.UsesMapForm() {
		return nil, nil, InvalidRequestErrorf("decision request must carry a questions map")
	}

	// The state is always sent: the map route accepts a null or empty state, so
	// neither may turn into a missing input. A null state is sent as "null" and
	// an empty string stays empty text.
	state, err := decisionStateText(request.State)
	if err != nil {
		return nil, nil, InvalidRequestErrorf("decision state could not be serialized: %v", err)
	}
	input := &schemas.DecisionInput{Text: &state}

	names := make([]string, 0, len(request.Questions))
	for name := range request.Questions {
		names = append(names, name)
	}
	sort.Strings(names)

	questions := make([]schemas.DecisionOrderedQuestion, 0, len(names))
	for _, name := range names {
		question := request.Questions[name]
		ordered := schemas.DecisionOrderedQuestion{
			Name:         schemas.Ptr(name),
			Instructions: instructionsText(question.Instructions),
		}
		switch question.Kind {
		case schemas.DecisionKindNoul:
			ordered.Type = schemas.DecisionOrderedKindPredicate
			ordered.Instructions = strings.TrimSpace(ordered.Instructions + noulCriteriaText(question.Criteria))
		case schemas.DecisionKindChoice:
			options, descriptions, err := choiceOptions(question.Criteria)
			if err != nil {
				return nil, nil, InvalidRequestErrorf("question %q: %v", name, err)
			}
			ordered.Type = schemas.DecisionOrderedKindChoice
			ordered.Choices = make([]schemas.DecisionChoiceOption, len(options))
			for i, option := range options {
				ordered.Choices[i] = schemas.DecisionChoiceOption{Value: schemas.DecisionScalar{Str: schemas.Ptr(option)}}
				if description := descriptions[option]; description != "" {
					ordered.Choices[i].Description = schemas.Ptr(description)
				}
			}
		case schemas.DecisionKindScore:
			levels, err := scoreCriteriaLevels(question.Criteria)
			if err != nil {
				return nil, nil, InvalidRequestErrorf("question %q: %v", name, err)
			}
			ordered.Type = schemas.DecisionOrderedKindScore
			ordered.Levels = make([]schemas.DecisionScoreLevel, len(levels))
			for i, level := range levels {
				ordered.Levels[i] = schemas.DecisionScoreLevel{Label: strconv.Itoa(i)}
				if description := renderStructuredText(level); description != "" {
					ordered.Levels[i].Description = schemas.Ptr(description)
				}
			}
		default:
			return nil, nil, InvalidRequestErrorf("question %q has unsupported kind %q", name, question.Kind)
		}
		questions = append(questions, ordered)
	}
	return input, questions, nil
}

// decisionStateText renders a map-form state as the ordered input text, the
// way decision emulation does: a string verbatim, a null state as "null", and
// anything else as sorted JSON. A state that cannot be encoded is an error
// rather than empty text, so a request is never sent without the caller's
// evidence.
func decisionStateText(state any) (string, error) {
	if state == nil {
		return "null", nil
	}
	if s, ok := state.(string); ok {
		return s, nil
	}
	raw, err := MarshalSorted(state)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// DecisionOrderedAnswersToMap converts the ordered answers to a request built
// by DecisionMapToOrdered back into the map-form answers, matched by question
// name. A predicate becomes a noul value, a choice keeps its value,
// confidence, and probabilities keyed by option, and a score keeps its value,
// confidence, probabilities keyed by level index, and the level legend. Every
// requested question must be answered with its own kind; a missing answer, a
// refusal, or an unrecognized answer is an error rather than a fabricated
// result, so the request can fall through to the next fallback.
func DecisionOrderedAnswersToMap(answers []schemas.DecisionOrderedAnswer, questions map[string]schemas.DecisionQuestion) (map[string]schemas.DecisionAnswer, error) {
	byName := make(map[string]schemas.DecisionOrderedAnswer, len(answers))
	for _, answer := range answers {
		if answer.Name != nil {
			byName[*answer.Name] = answer
		}
	}

	result := make(map[string]schemas.DecisionAnswer, len(questions))
	for name, question := range questions {
		answer, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("provider returned no answer for question %q", name)
		}
		if !answer.IsRecognized() {
			return nil, fmt.Errorf("provider answered question %q with unrecognized type %q", name, answer.Type)
		}
		if answer.Type == schemas.DecisionOrderedKindRefusal {
			return nil, fmt.Errorf("provider declined to answer question %q", name)
		}

		converted := schemas.DecisionAnswer{Kind: question.Kind, Confidence: answer.Confidence}
		switch question.Kind {
		case schemas.DecisionKindNoul:
			if answer.Type != schemas.DecisionOrderedKindPredicate || answer.Probability == nil {
				return nil, fmt.Errorf("provider answer for noul question %q carries no probability", name)
			}
			if *answer.Probability < 0 || *answer.Probability > 1 {
				return nil, fmt.Errorf("provider answer for noul question %q is outside [0,1]: %v", name, *answer.Probability)
			}
			converted.Value = *answer.Probability
		case schemas.DecisionKindChoice:
			if answer.Type != schemas.DecisionOrderedKindChoice || answer.Choice == nil || answer.Choice.Str == nil {
				return nil, fmt.Errorf("provider answer for choice question %q carries no option", name)
			}
			options, _, err := choiceOptions(question.Criteria)
			if err != nil {
				return nil, fmt.Errorf("question %q: %w", name, err)
			}
			if !containsString(options, *answer.Choice.Str) {
				return nil, fmt.Errorf("provider answer %q for choice question %q is not an allowed option", *answer.Choice.Str, name)
			}
			converted.Value = *answer.Choice.Str
			probabilities, err := orderedProbabilities(answer.Probabilities, func(value schemas.DecisionScalar) (string, bool) {
				if value.Str == nil {
					return "", false
				}
				return *value.Str, true
			})
			if err != nil {
				return nil, fmt.Errorf("provider answer for choice question %q: %w", name, err)
			}
			converted.Probabilities = probabilities
		case schemas.DecisionKindScore:
			if answer.Type != schemas.DecisionOrderedKindScore || answer.Score == nil {
				return nil, fmt.Errorf("provider answer for score question %q carries no score", name)
			}
			converted.Value = *answer.Score
			converted.Legend = scoreLegend(question.Criteria)
			probabilities, err := orderedProbabilities(answer.Probabilities, func(value schemas.DecisionScalar) (string, bool) {
				if value.Num == nil || *value.Num < 0 || *value.Num != math.Trunc(*value.Num) {
					return "", false
				}
				return strconv.Itoa(int(*value.Num)), true
			})
			if err != nil {
				return nil, fmt.Errorf("provider answer for score question %q: %w", name, err)
			}
			converted.Probabilities = probabilities
		default:
			return nil, fmt.Errorf("question %q has unsupported kind %q", name, question.Kind)
		}
		result[name] = converted
	}
	return result, nil
}

// orderedProbabilities keys an ordered probability distribution by the map-form
// key each entry's value maps to. An entry whose value has no key (wrong type,
// or a score index that is not a whole non-negative number) or whose key repeats
// is an error, so a malformed reply fails over instead of yielding a partial or
// overwritten distribution. It returns nil for an empty distribution so the
// field is omitted.
func orderedProbabilities(entries []schemas.DecisionProbability, key func(schemas.DecisionScalar) (string, bool)) (map[string]float64, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	probabilities := make(map[string]float64, len(entries))
	for i, entry := range entries {
		k, ok := key(entry.Value)
		if !ok {
			return nil, fmt.Errorf("probability %d has an unusable value", i)
		}
		if _, seen := probabilities[k]; seen {
			return nil, fmt.Errorf("probability %d repeats the key %q", i, k)
		}
		probabilities[k] = entry.Probability
	}
	return probabilities, nil
}
