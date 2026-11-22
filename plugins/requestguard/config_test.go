package requestguard

import (
	"math"
	"testing"
	"time"
)

func TestDefaultConfigAndNormalize(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate() error = %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("DefaultConfig().Enabled = false, want true")
	}
	if cfg.CapabilityGuard.Mode != PolicyAudit || cfg.CapabilityGuard.UnknownPolicy != UnknownCapabilityAllow {
		t.Fatalf("capability defaults = (%q, %q), want audit/allow", cfg.CapabilityGuard.Mode, cfg.CapabilityGuard.UnknownPolicy)
	}
	if cfg.BudgetGuard.Mode != PolicyOff || cfg.BudgetGuard.DefaultOutputTokens != 1024 || cfg.BudgetGuard.ImageTokenEstimate != 1024 {
		t.Fatalf("budget defaults not applied: %+v", cfg.BudgetGuard)
	}
	if cfg.BudgetGuard.SafetyMarginMultiplier != 1.1 || cfg.BudgetGuard.CharsPerToken != 4 {
		t.Fatalf("budget estimation defaults = multiplier %v, chars/token %v", cfg.BudgetGuard.SafetyMarginMultiplier, cfg.BudgetGuard.CharsPerToken)
	}
	if cfg.Recommender.MaxRecommendations != 3 || cfg.Coalescer.WaitTimeout != 25*time.Millisecond {
		t.Fatalf("recommender/coalescer defaults not applied: %+v / %+v", cfg.Recommender, cfg.Coalescer)
	}
	if cfg.Headers.Prefix != "X-Bifrost-RequestGuard" {
		t.Fatalf("headers prefix = %q, want default prefix", cfg.Headers.Prefix)
	}

	var zero Config
	if err := zero.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate() on zero config error = %v", err)
	}
	if zero.CapabilityGuard.Mode != PolicyAudit || zero.BudgetGuard.Mode != PolicyOff || zero.BudgetGuard.DefaultOutputTokens != 1024 {
		t.Fatalf("zero-value normalization did not apply defaults: %+v", zero)
	}
}

func TestNormalizeAndValidateRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "capability mode", mutate: func(c *Config) { c.CapabilityGuard.Mode = "strict" }},
		{name: "budget mode", mutate: func(c *Config) { c.BudgetGuard.Mode = "strict" }},
		{name: "unknown policy", mutate: func(c *Config) { c.CapabilityGuard.UnknownPolicy = "ignore" }},
		{name: "negative input tokens", mutate: func(c *Config) { c.BudgetGuard.MaxInputTokens = -1 }},
		{name: "negative total tokens", mutate: func(c *Config) { c.BudgetGuard.MaxTotalTokens = -1 }},
		{name: "negative cost", mutate: func(c *Config) { c.BudgetGuard.MaxEstimatedCostUSD = -0.01 }},
		{name: "non-finite cost", mutate: func(c *Config) { c.BudgetGuard.MaxEstimatedCostUSD = math.Inf(1) }},
		{name: "sub-unit safety multiplier", mutate: func(c *Config) { c.BudgetGuard.SafetyMarginMultiplier = 0.9 }},
		{name: "non-finite safety multiplier", mutate: func(c *Config) { c.BudgetGuard.SafetyMarginMultiplier = math.NaN() }},
		{name: "negative chars per token", mutate: func(c *Config) { c.BudgetGuard.CharsPerToken = -1 }},
		{name: "negative recommendation count", mutate: func(c *Config) { c.Recommender.MaxRecommendations = -1 }},
		{name: "savings above 100", mutate: func(c *Config) { c.Recommender.MinSavingsPct = 101 }},
		{name: "negative coalescer concurrency", mutate: func(c *Config) { c.Coalescer.MaxInFlight = -1 }},
		{name: "negative coalescer temperature", mutate: func(c *Config) { c.Coalescer.MaxTemperature = -0.1 }},
		{name: "negative timeout", mutate: func(c *Config) { c.Coalescer.WaitTimeout = -time.Second }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(&cfg)
			if err := cfg.NormalizeAndValidate(); err == nil {
				t.Fatal("NormalizeAndValidate() error = nil, want validation error")
			}
		})
	}
}

func TestNormalizeAndValidateNilReceiver(t *testing.T) {
	var cfg *Config
	if err := cfg.NormalizeAndValidate(); err == nil {
		t.Fatal("NormalizeAndValidate() error = nil, want error")
	}
}
