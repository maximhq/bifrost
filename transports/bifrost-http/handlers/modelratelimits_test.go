package handlers

import (
	"testing"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

func TestValidateModelRateLimitRules(t *testing.T) {
	valid := []ModelRateLimitRuleRequest{
		{Metric: configstoreTables.ModelRateLimitMetricRequests, MaxLimit: 15, ResetDuration: "1m"},
		{Metric: configstoreTables.ModelRateLimitMetricRequests, MaxLimit: 1500, ResetDuration: "1d"},
		{Metric: configstoreTables.ModelRateLimitMetricTokens, MaxLimit: 1000, ResetDuration: "1m"},
		{Metric: configstoreTables.ModelRateLimitMetricTokens, MaxLimit: 100000, ResetDuration: "1d"},
	}
	if err := validateModelRateLimitRules(valid, false); err != nil {
		t.Fatalf("valid multi-rule model limit rejected: %v", err)
	}

	tests := []struct {
		name  string
		rules []ModelRateLimitRuleRequest
	}{
		{
			name: "duplicate duration aliases",
			rules: []ModelRateLimitRuleRequest{
				{Metric: configstoreTables.ModelRateLimitMetricRequests, MaxLimit: 15, ResetDuration: "60s"},
				{Metric: configstoreTables.ModelRateLimitMetricRequests, MaxLimit: 15, ResetDuration: "1m"},
			},
		},
		{
			name:  "unknown metric",
			rules: []ModelRateLimitRuleRequest{{Metric: "rpm", MaxLimit: 15, ResetDuration: "1m"}},
		},
		{
			name:  "zero limit",
			rules: []ModelRateLimitRuleRequest{{Metric: configstoreTables.ModelRateLimitMetricRequests, MaxLimit: 0, ResetDuration: "1m"}},
		},
		{
			name:  "invalid duration",
			rules: []ModelRateLimitRuleRequest{{Metric: configstoreTables.ModelRateLimitMetricTokens, MaxLimit: 100, ResetDuration: "not-a-duration"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateModelRateLimitRules(tt.rules, false); err == nil {
				t.Fatal("expected invalid model rate-limit rules to be rejected")
			}
		})
	}
}
