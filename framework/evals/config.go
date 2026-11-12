package evals

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
)

// Defaults taken from Maxim so ported evaluators keep their behaviour.
const (
	defaultScoreThreshold    = 0.8
	defaultRunPassPercent    = 80.0
	defaultScaleMin          = 1.0
	defaultScaleMax          = 5.0
	defaultAPIConcurrency    = 1
	errPrefixEntryLevel      = "pass_criteria.entry_level"
	errPrefixRunLevel        = "pass_criteria.run_level"
	errPrefixScoreNormalizer = "score_normalizer"
)

// apiMethods are the HTTP methods an API evaluator may use.
var apiMethods = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}

// ParseConfig decodes a stored config for the given evaluator type, fills defaults and
// validates it. Unknown JSON fields are ignored so a config written by a newer build
// still loads.
func ParseConfig(evaluatorType EvaluatorType, raw []byte) (Config, error) {
	var cfg Config
	switch evaluatorType {
	case EvaluatorTypeAI:
		cfg = &AIConfig{}
	case EvaluatorTypeProgrammatic:
		cfg = &ProgrammaticConfig{}
	case EvaluatorTypeAPI:
		cfg = &APIConfig{}
	case EvaluatorTypeStatistical:
		cfg = &StatisticalConfig{}
	case EvaluatorTypeHuman:
		cfg = &HumanConfig{}
	default:
		return nil, fmt.Errorf("unknown evaluator type %q", evaluatorType)
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("invalid evaluator config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// MarshalConfig encodes a config with sorted map keys, so equal configs always produce
// equal bytes.
func MarshalConfig(cfg Config) ([]byte, error) {
	if cfg == nil {
		return nil, errors.New("evaluator config is nil")
	}
	return providerUtils.MarshalSorted(cfg)
}

// IsValidEvaluatorType reports whether t is a known evaluator type.
func IsValidEvaluatorType(t EvaluatorType) bool {
	switch t {
	case EvaluatorTypeAI, EvaluatorTypeProgrammatic, EvaluatorTypeStatistical, EvaluatorTypeAPI, EvaluatorTypeHuman:
		return true
	}
	return false
}

// ----------------------------------------------------------------------------
// Defaults
// ----------------------------------------------------------------------------

// applyCriteriaDefaults fills pass criteria the caller left out. A multiselect score has
// no sensible default, so it is left for validation to reject.
func (c *CommonConfig) applyCriteriaDefaults(style GradingStyle) {
	entry := &c.PassCriteria.EntryLevel
	if entry.Operator == "" && entry.Value == nil {
		switch style {
		case GradingStyleYesNo:
			entry.Operator, entry.Value = OperatorEqual, ScoreLabelYes
		case GradingStyleMultiselect:
		default:
			entry.Operator, entry.Value = OperatorGreaterOrEqual, defaultScoreThreshold
		}
	}
	if entry.Name == "" {
		entry.Name = CriterionScore
	}
	run := &c.PassCriteria.RunLevel
	if run.Name == "" && run.Operator == "" && run.Value == nil {
		run.Name, run.Operator, run.Value = RunCriterionQueriesPassed, OperatorGreaterOrEqual, defaultRunPassPercent
	}
}

// defaultScale returns the scale to use for a style, filling the default range when a
// scale score has none.
func defaultScale(style GradingStyle, scale *Scale) *Scale {
	if style == GradingStyleScale && scale == nil {
		return &Scale{Min: defaultScaleMin, Max: defaultScaleMax}
	}
	return scale
}

func (c *AIConfig) applyDefaults() {
	c.Scale = defaultScale(c.GradingStyle, c.Scale)
	c.applyCriteriaDefaults(c.GradingStyle)
}

func (c *ProgrammaticConfig) applyDefaults() {
	c.applyCriteriaDefaults(c.ResponseType)
}

func (c *APIConfig) applyDefaults() {
	c.Method = strings.ToUpper(strings.TrimSpace(c.Method))
	if c.Concurrency == 0 {
		c.Concurrency = defaultAPIConcurrency
	}
	c.applyCriteriaDefaults(GradingStyleNumber)
}

func (c *StatisticalConfig) applyDefaults() {
	c.applyCriteriaDefaults(GradingStyleNumber)
}

func (c *HumanConfig) applyDefaults() {
	c.Scale = defaultScale(c.EvaluationType, c.Scale)
	c.applyCriteriaDefaults(c.EvaluationType)
}

// ----------------------------------------------------------------------------
// Validation
// ----------------------------------------------------------------------------

// Validate checks an AI judge config.
func (c *AIConfig) Validate() error {
	if strings.TrimSpace(c.Instructions) == "" {
		return errors.New("instructions is required")
	}
	if err := validateGradingStyle("grading_style", c.GradingStyle); err != nil {
		return err
	}
	if strings.TrimSpace(c.Provider) == "" {
		return errors.New("provider is required")
	}
	if strings.TrimSpace(c.Model) == "" {
		return errors.New("model is required")
	}
	if err := validateScale(c.Scale); err != nil {
		return err
	}
	return c.validateCommon(c.GradingStyle)
}

// Validate checks a programmatic config. Compiling the code is the sandbox's job.
func (c *ProgrammaticConfig) Validate() error {
	if strings.TrimSpace(c.Code) == "" {
		return errors.New("code is required")
	}
	if err := validateGradingStyle("response_type", c.ResponseType); err != nil {
		return err
	}
	return c.validateCommon(c.ResponseType)
}

// Validate checks an API evaluator config.
func (c *APIConfig) Validate() error {
	if !apiMethods[c.Method] {
		return fmt.Errorf("method: unsupported HTTP method %q", c.Method)
	}
	if strings.TrimSpace(c.URL) == "" {
		return errors.New("url is required")
	}
	parsed, err := url.Parse(c.URL)
	if err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("url: must be http or https")
	}
	if strings.TrimSpace(c.OutputMapping.Score) == "" {
		return errors.New("output_mapping.score is required")
	}
	if c.Concurrency < 0 {
		return errors.New("concurrency: must not be negative")
	}
	if c.TimeoutMs < 0 {
		return errors.New("timeout_ms: must not be negative")
	}
	for i, header := range c.Headers {
		if strings.TrimSpace(header.Key) == "" {
			return fmt.Errorf("headers[%d].key is required", i)
		}
		// Checked here as well as when a config is stored, so no path that builds an
		// evaluator from a config can resolve a reference.
		if header.Value.IsFromEnv() || header.Value.IsFromVault() || header.Value.IsFromSecret() {
			return fmt.Errorf("header %q: %w", header.Key, errSecretReference)
		}
	}
	return c.validateCommon(GradingStyleNumber)
}

// Validate checks a statistical config.
func (c *StatisticalConfig) Validate() error {
	return c.validateCommon(GradingStyleNumber)
}

// Validate checks a human evaluator config.
func (c *HumanConfig) Validate() error {
	if err := validateGradingStyle("evaluation_type", c.EvaluationType); err != nil {
		return err
	}
	if err := validateScale(c.Scale); err != nil {
		return err
	}
	return c.validateCommon(c.EvaluationType)
}

func validateGradingStyle(field string, style GradingStyle) error {
	switch style {
	case GradingStyleYesNo, GradingStyleScale, GradingStyleMultiselect, GradingStyleNumber:
		return nil
	}
	return fmt.Errorf("%s: unknown value %q", field, style)
}

func validateScale(scale *Scale) error {
	if scale != nil && scale.Min >= scale.Max {
		return errors.New("scale: min must be less than max")
	}
	return nil
}

// validateCommon checks the fields every evaluator type shares.
func (c *CommonConfig) validateCommon(style GradingStyle) error {
	entry := c.PassCriteria.EntryLevel
	if style == GradingStyleMultiselect && len(entry.Options) == 0 {
		return fmt.Errorf("%s.options: a multiselect score needs at least one option", errPrefixEntryLevel)
	}
	if entry.Name != CriterionScore {
		return fmt.Errorf("%s.name: must be %q", errPrefixEntryLevel, CriterionScore)
	}
	if err := validateCriterion(errPrefixEntryLevel, entry, true); err != nil {
		return err
	}
	run := c.PassCriteria.RunLevel
	switch run.Name {
	case RunCriterionMeanScore, RunCriterionMedianScore, RunCriterionMinimumScore, RunCriterionMaximumScore, RunCriterionQueriesPassed:
	default:
		return fmt.Errorf("%s.name: unknown value %q", errPrefixRunLevel, run.Name)
	}
	if err := validateCriterion(errPrefixRunLevel, run, false); err != nil {
		return err
	}
	return validateNormalizer(c.ScoreNormalizer)
}

// validateCriterion checks that the operator is known and the value fits it. Only an
// entry-level criterion may use "is one of", since run-level aggregates are numbers.
func validateCriterion(prefix string, criterion Criterion, allowIsOneOf bool) error {
	switch criterion.Operator {
	case OperatorGreater, OperatorLess, OperatorGreaterOrEqual, OperatorLessOrEqual:
		if !isNumber(criterion.Value) {
			return fmt.Errorf("%s.value: must be a number for operator %q", prefix, criterion.Operator)
		}
	case OperatorEqual, OperatorNotEqual:
		if !allowIsOneOf {
			if !isNumber(criterion.Value) {
				return fmt.Errorf("%s.value: must be a number for operator %q", prefix, criterion.Operator)
			}
			return nil
		}
		switch criterion.Value.(type) {
		case string, bool:
		default:
			if !isNumber(criterion.Value) {
				return fmt.Errorf("%s.value: must be a string, number or boolean for operator %q", prefix, criterion.Operator)
			}
		}
	case OperatorIsOneOf:
		if !allowIsOneOf {
			return fmt.Errorf("%s.operator: %q is not allowed here", prefix, criterion.Operator)
		}
		if !isNonEmptyStringList(criterion.Value) {
			return fmt.Errorf("%s.value: must be a non-empty list of strings for operator %q", prefix, criterion.Operator)
		}
	default:
		return fmt.Errorf("%s.operator: unknown operator %q", prefix, criterion.Operator)
	}
	return nil
}

func validateNormalizer(normalizer *ScoreNormalizer) error {
	if normalizer == nil {
		return nil
	}
	switch normalizer.Action {
	case NormalizerMultiply, NormalizerAdd, NormalizerSubtract:
		if normalizer.Value == nil {
			return fmt.Errorf("%s: action %q needs a value", errPrefixScoreNormalizer, normalizer.Action)
		}
	case NormalizerDivide:
		if normalizer.Value == nil {
			return fmt.Errorf("%s: action %q needs a value", errPrefixScoreNormalizer, normalizer.Action)
		}
		if *normalizer.Value == 0 {
			return fmt.Errorf("%s: cannot divide by zero", errPrefixScoreNormalizer)
		}
	case NormalizerMapping:
		if len(normalizer.Mapping) == 0 {
			return fmt.Errorf("%s: action %q needs at least one mapping entry", errPrefixScoreNormalizer, normalizer.Action)
		}
	default:
		return fmt.Errorf("%s: unknown action %q", errPrefixScoreNormalizer, normalizer.Action)
	}
	return nil
}

// isNumber reports whether v is a numeric value, in any form a Go caller or the JSON
// decoder can produce.
func isNumber(v any) bool {
	switch v.(type) {
	case float64, float32, int, int32, int64, json.Number:
		return true
	}
	return false
}

// isNonEmptyStringList reports whether v is a list holding only strings.
func isNonEmptyStringList(v any) bool {
	switch list := v.(type) {
	case []string:
		return len(list) > 0
	case []any:
		if len(list) == 0 {
			return false
		}
		for _, item := range list {
			if _, ok := item.(string); !ok {
				return false
			}
		}
		return true
	}
	return false
}
