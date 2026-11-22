package requestguard

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// PolicyMode controls what a guard does when a request violates its policy.
type PolicyMode string

const (
	PolicyOff       PolicyMode = "off"
	PolicyAudit     PolicyMode = "audit"
	PolicyWarn      PolicyMode = "warn"
	PolicyBlock     PolicyMode = "block"
	PolicyDowngrade PolicyMode = "downgrade"
)

const maxConfiguredRecommendations = 10

// UnknownCapabilityPolicy controls requests whose capability is not present in
// the model catalog.
type UnknownCapabilityPolicy string

const (
	UnknownCapabilityAllow UnknownCapabilityPolicy = "allow"
	UnknownCapabilityWarn  UnknownCapabilityPolicy = "warn"
	UnknownCapabilityBlock UnknownCapabilityPolicy = "block"
)

type Config struct {
	Enabled         bool                  `json:"enabled"`
	CapabilityGuard CapabilityGuardConfig `json:"capability_guard"`
	BudgetGuard     BudgetGuardConfig     `json:"budget_guard"`
	Recommender     RecommenderConfig     `json:"recommender"`
	Coalescer       CoalescerConfig       `json:"coalescer"`
	Headers         HeadersConfig         `json:"headers"`
	Metrics         MetricsConfig         `json:"metrics"`
}

type CapabilityGuardConfig struct {
	Mode                  PolicyMode              `json:"mode"`
	UnknownPolicy         UnknownCapabilityPolicy `json:"unknown_policy"`
	CheckTools            bool                    `json:"check_tools"`
	CheckParallelTools    bool                    `json:"check_parallel_tools"`
	CheckToolChoice       bool                    `json:"check_tool_choice"`
	CheckStructuredOutput bool                    `json:"check_structured_output"`
	CheckVision           bool                    `json:"check_vision"`
	CheckStreaming        bool                    `json:"check_streaming"`
	CheckContextLimit     bool                    `json:"check_context_limit"`
	CheckMaxOutputTokens  bool                    `json:"check_max_output_tokens"`
	AllowFallbacksOnBlock bool                    `json:"allow_fallbacks_on_block"`
}

type BudgetGuardConfig struct {
	Mode                   PolicyMode `json:"mode"`
	MaxInputTokens         int        `json:"max_input_tokens"`
	MaxTotalTokens         int        `json:"max_total_tokens"`
	MaxEstimatedCostUSD    float64    `json:"max_estimated_cost_usd"`
	DefaultOutputTokens    int        `json:"default_output_tokens"`
	SafetyMarginMultiplier float64    `json:"safety_margin_multiplier"`
	CharsPerToken          float64    `json:"chars_per_token"`
	ImageTokenEstimate     int        `json:"image_token_estimate"`
	AllowFallbacksOnBlock  bool       `json:"allow_fallbacks_on_block"`
}

type RecommenderConfig struct {
	Enabled                          bool     `json:"enabled"`
	MaxRecommendations               int      `json:"max_recommendations"`
	AllowedProviders                 []string `json:"allowed_providers,omitempty"`
	AllowedModels                    []string `json:"allowed_models,omitempty"`
	ExcludedModels                   []string `json:"excluded_models,omitempty"`
	SameProviderOnly                 bool     `json:"same_provider_only"`
	MinSavingsPct                    float64  `json:"min_savings_pct"`
	AutoDowngradeOnCapabilityFailure bool     `json:"auto_downgrade_on_capability_failure"`
	AutoDowngradeOnBudgetExceeded    bool     `json:"auto_downgrade_on_budget_exceeded"`
}

type CoalescerConfig struct {
	Enabled                bool          `json:"enabled"`
	EnableStreaming        bool          `json:"enable_streaming"`
	MaxInFlight            int           `json:"max_in_flight"`
	MaxTemperature         float64       `json:"max_temperature"`
	WaitTimeout            time.Duration `json:"wait_timeout"`
	MaxBufferedChunks      int           `json:"max_buffered_chunks"`
	MaxBufferedBytes       int           `json:"max_buffered_bytes"`
	IncludeVirtualKeyScope bool          `json:"include_virtual_key_scope"`
	IncludeCustomerScope   bool          `json:"include_customer_scope"`
	BypassHeader           string        `json:"bypass_header"`
}

type HeadersConfig struct {
	Enabled bool   `json:"enabled"`
	Prefix  string `json:"prefix"`
}

type MetricsConfig struct {
	Enabled    bool                  `json:"enabled"`
	Registerer prometheus.Registerer `json:"-"`
}

// DefaultConfig returns an opt-in, audit-first configuration. The other
// request-path features remain disabled until explicitly enabled.
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		CapabilityGuard: CapabilityGuardConfig{
			Mode:                  PolicyAudit,
			UnknownPolicy:         UnknownCapabilityAllow,
			CheckTools:            true,
			CheckParallelTools:    true,
			CheckToolChoice:       true,
			CheckStructuredOutput: true,
			CheckVision:           true,
			CheckStreaming:        true,
			CheckContextLimit:     true,
			CheckMaxOutputTokens:  true,
		},
		BudgetGuard: BudgetGuardConfig{
			Mode:                   PolicyOff,
			DefaultOutputTokens:    1024,
			SafetyMarginMultiplier: 1.1,
			CharsPerToken:          4,
			ImageTokenEstimate:     1024,
		},
		Recommender: RecommenderConfig{
			MaxRecommendations: 3,
			MinSavingsPct:      10,
		},
		Coalescer: CoalescerConfig{
			MaxInFlight:            128,
			WaitTimeout:            25 * time.Millisecond,
			MaxBufferedChunks:      1024,
			MaxBufferedBytes:       1 << 20,
			IncludeVirtualKeyScope: true,
			IncludeCustomerScope:   true,
		},
		Headers: HeadersConfig{Prefix: "X-Bifrost-RequestGuard"},
		Metrics: MetricsConfig{Enabled: true},
	}
}

// MarshalJSON represents WaitTimeout in milliseconds, matching the config
// schema and the duration convention used by NetworkConfig.
func (c CoalescerConfig) MarshalJSON() ([]byte, error) {
	type alias CoalescerConfig
	return json.Marshal(struct {
		*alias
		WaitTimeoutMS int64 `json:"wait_timeout"`
	}{alias: (*alias)(&c), WaitTimeoutMS: c.WaitTimeout.Milliseconds()})
}

// UnmarshalJSON accepts wait_timeout as integer milliseconds while preserving
// default values for fields omitted from a partial nested config object.
func (c *CoalescerConfig) UnmarshalJSON(data []byte) error {
	if c == nil {
		return fmt.Errorf("coalescer config is nil")
	}
	type alias CoalescerConfig
	var wire struct {
		*alias
		WaitTimeout json.RawMessage `json:"wait_timeout"`
	}
	wire.alias = (*alias)(c)
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if len(wire.WaitTimeout) == 0 || string(wire.WaitTimeout) == "null" {
		return nil
	}
	var milliseconds int64
	if err := json.Unmarshal(wire.WaitTimeout, &milliseconds); err != nil {
		return fmt.Errorf("coalescer.wait_timeout must be integer milliseconds: %w", err)
	}
	if milliseconds > math.MaxInt64/int64(time.Millisecond) || milliseconds < math.MinInt64/int64(time.Millisecond) {
		return fmt.Errorf("coalescer.wait_timeout is out of range")
	}
	c.WaitTimeout = time.Duration(milliseconds) * time.Millisecond
	return nil
}

// NormalizeAndValidate fills zero-value defaults and rejects invalid policy
// values, negative limits, and non-finite or invalid multipliers.
func (c *Config) NormalizeAndValidate() error {
	if c == nil {
		return fmt.Errorf("requestguard config is nil")
	}
	defaults := DefaultConfig()

	var err error
	if c.CapabilityGuard.Mode, err = normalizePolicyMode(c.CapabilityGuard.Mode, defaults.CapabilityGuard.Mode, "capability_guard.mode"); err != nil {
		return err
	}
	if c.BudgetGuard.Mode, err = normalizePolicyMode(c.BudgetGuard.Mode, defaults.BudgetGuard.Mode, "budget_guard.mode"); err != nil {
		return err
	}
	if c.CapabilityGuard.UnknownPolicy == "" {
		c.CapabilityGuard.UnknownPolicy = defaults.CapabilityGuard.UnknownPolicy
	} else {
		c.CapabilityGuard.UnknownPolicy = UnknownCapabilityPolicy(strings.ToLower(strings.TrimSpace(string(c.CapabilityGuard.UnknownPolicy))))
		switch c.CapabilityGuard.UnknownPolicy {
		case UnknownCapabilityAllow, UnknownCapabilityWarn, UnknownCapabilityBlock:
		default:
			return fmt.Errorf("invalid capability_guard.unknown_policy %q", c.CapabilityGuard.UnknownPolicy)
		}
	}

	if c.BudgetGuard.MaxInputTokens < 0 || c.BudgetGuard.MaxTotalTokens < 0 || c.BudgetGuard.DefaultOutputTokens < 0 || c.BudgetGuard.ImageTokenEstimate < 0 {
		return fmt.Errorf("budget_guard token limits must not be negative")
	}
	if !finite(c.BudgetGuard.MaxEstimatedCostUSD) || c.BudgetGuard.MaxEstimatedCostUSD < 0 {
		return fmt.Errorf("budget_guard.max_estimated_cost_usd must be finite and non-negative")
	}
	if c.BudgetGuard.SafetyMarginMultiplier < 0 || !finite(c.BudgetGuard.SafetyMarginMultiplier) {
		return fmt.Errorf("budget_guard.safety_margin_multiplier must be finite and positive")
	}
	if c.BudgetGuard.SafetyMarginMultiplier == 0 {
		c.BudgetGuard.SafetyMarginMultiplier = defaults.BudgetGuard.SafetyMarginMultiplier
	}
	if c.BudgetGuard.SafetyMarginMultiplier < 1 {
		return fmt.Errorf("budget_guard.safety_margin_multiplier must be at least 1")
	}
	if c.BudgetGuard.CharsPerToken < 0 || !finite(c.BudgetGuard.CharsPerToken) {
		return fmt.Errorf("budget_guard.chars_per_token must be finite and positive")
	}
	if c.BudgetGuard.CharsPerToken == 0 {
		c.BudgetGuard.CharsPerToken = defaults.BudgetGuard.CharsPerToken
	}
	if c.BudgetGuard.DefaultOutputTokens == 0 {
		c.BudgetGuard.DefaultOutputTokens = defaults.BudgetGuard.DefaultOutputTokens
	}
	if c.BudgetGuard.ImageTokenEstimate == 0 {
		c.BudgetGuard.ImageTokenEstimate = defaults.BudgetGuard.ImageTokenEstimate
	}

	if c.Recommender.MaxRecommendations < 0 {
		return fmt.Errorf("recommender.max_recommendations must not be negative")
	}
	if c.Recommender.MaxRecommendations == 0 {
		c.Recommender.MaxRecommendations = defaults.Recommender.MaxRecommendations
	}
	if c.Recommender.MaxRecommendations > maxConfiguredRecommendations {
		return fmt.Errorf("recommender.max_recommendations must not exceed %d", maxConfiguredRecommendations)
	}
	if !finite(c.Recommender.MinSavingsPct) || c.Recommender.MinSavingsPct < 0 || c.Recommender.MinSavingsPct > 100 {
		return fmt.Errorf("recommender.min_savings_pct must be between 0 and 100")
	}

	if c.Coalescer.MaxInFlight < 0 || c.Coalescer.MaxBufferedChunks < 0 || c.Coalescer.MaxBufferedBytes < 0 {
		return fmt.Errorf("coalescer limits must not be negative")
	}
	if !finite(c.Coalescer.MaxTemperature) || c.Coalescer.MaxTemperature < 0 {
		return fmt.Errorf("coalescer.max_temperature must be finite and non-negative")
	}
	if c.Coalescer.WaitTimeout < 0 {
		return fmt.Errorf("coalescer.wait_timeout must not be negative")
	}
	if c.Coalescer.MaxInFlight == 0 {
		c.Coalescer.MaxInFlight = defaults.Coalescer.MaxInFlight
	}
	if c.Coalescer.WaitTimeout == 0 {
		c.Coalescer.WaitTimeout = defaults.Coalescer.WaitTimeout
	}
	if c.Coalescer.MaxBufferedChunks == 0 {
		c.Coalescer.MaxBufferedChunks = defaults.Coalescer.MaxBufferedChunks
	}
	if c.Coalescer.MaxBufferedBytes == 0 {
		c.Coalescer.MaxBufferedBytes = defaults.Coalescer.MaxBufferedBytes
	}
	if strings.TrimSpace(c.Headers.Prefix) == "" {
		c.Headers.Prefix = defaults.Headers.Prefix
	} else {
		c.Headers.Prefix = strings.TrimSpace(c.Headers.Prefix)
	}
	if c.Coalescer.BypassHeader != "" {
		c.Coalescer.BypassHeader = strings.TrimSpace(c.Coalescer.BypassHeader)
	}
	return nil
}

func normalizePolicyMode(mode, fallback PolicyMode, field string) (PolicyMode, error) {
	if mode == "" {
		return fallback, nil
	}
	mode = PolicyMode(strings.ToLower(strings.TrimSpace(string(mode))))
	switch mode {
	case PolicyOff, PolicyAudit, PolicyWarn, PolicyBlock, PolicyDowngrade:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid %s %q", field, mode)
	}
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
