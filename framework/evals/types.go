// Package evals holds the evaluator engine: config types, validation, and (in later
// changes) the evaluators that score model output.
package evals

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// EvaluatorType is the kind of evaluator, which decides how it is executed.
type EvaluatorType string

const (
	EvaluatorTypeAI           EvaluatorType = "ai"           // LLM-as-judge
	EvaluatorTypeProgrammatic EvaluatorType = "programmatic" // user code in a sandbox
	EvaluatorTypeStatistical  EvaluatorType = "statistical"  // deterministic metric
	EvaluatorTypeAPI          EvaluatorType = "api"          // user HTTP endpoint
	EvaluatorTypeHuman        EvaluatorType = "human"        // scored by a reviewer
)

// GradingStyle is the shape of the score an evaluator produces.
type GradingStyle string

const (
	GradingStyleYesNo       GradingStyle = "yes_no"      // boolean
	GradingStyleScale       GradingStyle = "scale"       // number within a range
	GradingStyleMultiselect GradingStyle = "multiselect" // one of a fixed set of strings
	GradingStyleNumber      GradingStyle = "number"      // any number
)

// Operators a pass criterion can use.
const (
	OperatorEqual          = "="
	OperatorNotEqual       = "!="
	OperatorGreater        = ">"
	OperatorLess           = "<"
	OperatorGreaterOrEqual = ">="
	OperatorLessOrEqual    = "<="
	OperatorIsOneOf        = "is one of"
)

// Criterion names. An entry-level criterion always judges the score; a run-level
// criterion judges an aggregate over every entry of a run.
const (
	CriterionScore            = "score"
	RunCriterionMeanScore     = "mean_score"
	RunCriterionMedianScore   = "median_score"
	RunCriterionMinimumScore  = "minimum_score"
	RunCriterionMaximumScore  = "maximum_score"
	RunCriterionQueriesPassed = "queries_passed" // percent of entries that passed
)

// Score normalizer actions.
const (
	NormalizerMultiply = "multiply"
	NormalizerDivide   = "divide"
	NormalizerAdd      = "add"
	NormalizerSubtract = "subtract"
	NormalizerMapping  = "mapping"
)

// Boolean scores are compared against these labels in pass criteria.
const (
	ScoreLabelYes = "Yes"
	ScoreLabelNo  = "No"
)

// Criterion is one pass/fail rule: name, operator and the value to compare against.
type Criterion struct {
	Name     string   `json:"name"`
	Operator string   `json:"operator"`
	Value    any      `json:"value"`             // number, string, bool or list of strings
	Options  []string `json:"options,omitempty"` // choices for a multiselect score
}

// PassCriteria holds the rule for a single entry and the rule for a whole run.
type PassCriteria struct {
	EntryLevel Criterion `json:"entry_level"`
	RunLevel   Criterion `json:"run_level"`
}

// Scale is the inclusive range of a scale score.
type Scale struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// ScoreNormalizer rewrites a raw score before pass criteria are applied.
type ScoreNormalizer struct {
	Action  string             `json:"action"`
	Value   *float64           `json:"value,omitempty"`   // operand for arithmetic actions
	Mapping map[string]float64 `json:"mapping,omitempty"` // raw score to number, for "mapping"
}

// CommonConfig is shared by every evaluator type.
type CommonConfig struct {
	PassCriteria      PassCriteria      `json:"pass_criteria"`
	Reversed          bool              `json:"reversed,omitempty"` // lower score is better
	ScoreNormalizer   *ScoreNormalizer  `json:"score_normalizer,omitempty"`
	Variables         map[string]string `json:"variables,omitempty"`          // required: name to description
	OptionalVariables map[string]string `json:"optional_variables,omitempty"` // name to description
}

// AIConfig configures an LLM-as-judge evaluator.
type AIConfig struct {
	CommonConfig
	Instructions string         `json:"instructions"`
	GradingStyle GradingStyle   `json:"grading_style"`
	Scale        *Scale         `json:"scale,omitempty"` // scale style only; defaults to 1 through 5
	Provider     string         `json:"provider"`
	Model        string         `json:"model"`
	ModelParams  map[string]any `json:"model_params,omitempty"`
}

// ProgrammaticConfig configures a user-code evaluator.
type ProgrammaticConfig struct {
	CommonConfig
	Code         string       `json:"code"` // must define validate(...)
	ResponseType GradingStyle `json:"response_type"`
}

// HeaderPair is one request header whose value may be a secret reference.
type HeaderPair struct {
	Key   string            `json:"key"`
	Value schemas.SecretVar `json:"value"`
}

// KeyValue is one query parameter.
type KeyValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// OutputMapping locates the score and reasoning in an API evaluator's response body.
type OutputMapping struct {
	Score     string `json:"score"`               // gjson path
	Reasoning string `json:"reasoning,omitempty"` // gjson path
}

// APIConfig configures an evaluator that calls a user HTTP endpoint.
type APIConfig struct {
	CommonConfig
	Method        string        `json:"method"`
	URL           string        `json:"url"`
	Headers       []HeaderPair  `json:"headers,omitempty"`
	Params        []KeyValue    `json:"params,omitempty"`
	Payload       string        `json:"payload,omitempty"`
	OutputMapping OutputMapping `json:"output_mapping"`
	TimeoutMs     int           `json:"timeout_ms,omitempty"`
	Concurrency   int           `json:"concurrency,omitempty"` // max in-flight calls; defaults to 1
}

// StatisticalConfig configures a deterministic metric.
type StatisticalConfig struct {
	CommonConfig
	EmbeddingProvider string `json:"embedding_provider,omitempty"` // embedding-based metrics only
	EmbeddingModel    string `json:"embedding_model,omitempty"`    // embedding-based metrics only
}

// HumanConfig configures an evaluator that a reviewer scores by hand.
type HumanConfig struct {
	CommonConfig
	Instructions   string       `json:"instructions,omitempty"`
	EvaluationType GradingStyle `json:"evaluation_type"`
	Scale          *Scale       `json:"scale,omitempty"` // scale style only; defaults to 1 through 5
}

// Config is the typed form of an evaluator's stored config.
type Config interface {
	// Type reports which evaluator type this config belongs to.
	Type() EvaluatorType
	// Common returns the fields shared by every evaluator type.
	Common() *CommonConfig
	// Validate reports the first problem that would stop the config from running.
	Validate() error
	// applyDefaults fills fields the caller left out.
	applyDefaults()
}

func (c *AIConfig) Type() EvaluatorType           { return EvaluatorTypeAI }
func (c *ProgrammaticConfig) Type() EvaluatorType { return EvaluatorTypeProgrammatic }
func (c *APIConfig) Type() EvaluatorType          { return EvaluatorTypeAPI }
func (c *StatisticalConfig) Type() EvaluatorType  { return EvaluatorTypeStatistical }
func (c *HumanConfig) Type() EvaluatorType        { return EvaluatorTypeHuman }

func (c *AIConfig) Common() *CommonConfig           { return &c.CommonConfig }
func (c *ProgrammaticConfig) Common() *CommonConfig { return &c.CommonConfig }
func (c *APIConfig) Common() *CommonConfig          { return &c.CommonConfig }
func (c *StatisticalConfig) Common() *CommonConfig  { return &c.CommonConfig }
func (c *HumanConfig) Common() *CommonConfig        { return &c.CommonConfig }
