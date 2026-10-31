package utils

import (
	"sort"
	"strconv"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// Some decision providers key choices by option name and score levels by index
// instead of listing them (Typesafe's System One shape, and the emulation tool
// schema). These helpers convert between those keys and the normalized
// questions and answers.

// DecisionLevelDescription is a score level's description for a provider with
// no level labels. A level labelled by its index (or not at all) keeps its
// description; another label is folded in: alone when there is no
// description, as "label: description" for a text description, and as a
// {label, description} object otherwise.
func DecisionLevelDescription(index int, level schemas.DecisionLevel) interface{} {
	description := level.Description.Value()
	if level.Label == "" || level.Label == strconv.Itoa(index) {
		return description
	}
	if description == nil {
		return level.Label
	}
	if text, ok := description.(string); ok {
		return level.Label + ": " + text
	}
	return map[string]interface{}{"label": level.Label, "description": description}
}

// DecisionScoreLegend builds a score answer's legend, level index ->
// description, from the question's levels, so every provider that does not
// return a legend of its own (OpenAI, emulation) carries the same one
// Typesafe's models send. Nil for a question without levels.
func DecisionScoreLegend(question schemas.DecisionQuestion) map[string]any {
	if len(question.Levels) == 0 {
		return nil
	}
	legend := make(map[string]any, len(question.Levels))
	for i, level := range question.Levels {
		legend[strconv.Itoa(i)] = DecisionLevelDescription(i, level)
	}
	return legend
}

// DecisionChoiceForKey returns the value of the question's choice keyed key,
// so a boolean choice answers with its boolean, or key as a string when no
// choice matches.
func DecisionChoiceForKey(question schemas.DecisionQuestion, key string) schemas.DecisionScalar {
	for _, choice := range question.Choices {
		if choiceKey, err := choice.Key(); err == nil && choiceKey == key {
			return choice.Value
		}
	}
	return schemas.DecisionScalar{Str: &key}
}

// DecisionProbabilitiesFromKeys orders a distribution keyed by option name
// (choice) or level index (score) by the question's choices or levels; a
// score entry carries its level index as the value and the level's label. Keys
// no choice or level matches follow in sorted order, so nothing a provider
// returned is dropped: a numeric key of a score question as a number,
// "true"/"false" of a predicate as a boolean, and any other key as a string.
func DecisionProbabilitiesFromKeys(question schemas.DecisionQuestion, probabilities map[string]float64) []schemas.DecisionProbability {
	if len(probabilities) == 0 {
		return nil
	}
	ordered := make([]schemas.DecisionProbability, 0, len(probabilities))
	used := make(map[string]struct{}, len(probabilities))
	switch question.Type {
	case schemas.DecisionTypeChoice:
		for _, choice := range question.Choices {
			key, err := choice.Key()
			if err != nil {
				continue
			}
			if probability, ok := probabilities[key]; ok {
				if _, seen := used[key]; !seen {
					ordered = append(ordered, schemas.DecisionProbability{Value: choice.Value, Probability: probability})
					used[key] = struct{}{}
				}
			}
		}
	case schemas.DecisionTypeScore:
		for i, level := range question.Levels {
			key := strconv.Itoa(i)
			if probability, ok := probabilities[key]; ok {
				// A level written without a label is labelled by its index.
				index, label := float64(i), level.Label
				if label == "" {
					label = key
				}
				ordered = append(ordered, schemas.DecisionProbability{Value: schemas.DecisionScalar{Num: &index}, Label: &label, Probability: probability})
				used[key] = struct{}{}
			}
		}
	}
	rest := make([]string, 0, len(probabilities)-len(used))
	for key := range probabilities {
		if _, seen := used[key]; !seen {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	for _, key := range rest {
		ordered = append(ordered, schemas.DecisionProbability{Value: decisionKeyScalar(question.Type, key), Probability: probabilities[key]})
	}
	return ordered
}

// decisionKeyScalar is the value of a distribution key no choice or level
// matched.
func decisionKeyScalar(questionType schemas.DecisionType, key string) schemas.DecisionScalar {
	if questionType == schemas.DecisionTypeScore {
		if index, err := strconv.ParseFloat(key, 64); err == nil {
			return schemas.DecisionScalar{Num: &index}
		}
	}
	if questionType == schemas.DecisionTypePredicate && (key == "true" || key == "false") {
		value := key == "true"
		return schemas.DecisionScalar{Bool: &value}
	}
	return schemas.DecisionScalar{Str: &key}
}

// DecisionKeyedProbabilities keys a distribution by option name or level
// index. An entry without a value is skipped.
func DecisionKeyedProbabilities(probabilities []schemas.DecisionProbability) map[string]float64 {
	if len(probabilities) == 0 {
		return nil
	}
	keyed := make(map[string]float64, len(probabilities))
	for _, entry := range probabilities {
		if key, ok := entry.Value.Key(); ok {
			keyed[key] = entry.Probability
		}
	}
	return keyed
}
