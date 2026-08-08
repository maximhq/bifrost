package tables

import "testing"

func TestTableRateLimitBeforeSaveValidatesModelRuleShape(t *testing.T) {
	max := int64(15)
	duration := "1m"
	modelID := "model-config"

	tests := []struct {
		name    string
		rule    TableRateLimit
		wantErr bool
	}{
		{
			name: "request rule",
			rule: TableRateLimit{
				ID:                   "requests",
				ModelConfigID:        &modelID,
				Metric:               ModelRateLimitMetricRequests,
				RequestMaxLimit:      &max,
				RequestResetDuration: &duration,
			},
		},
		{
			name: "token rule",
			rule: TableRateLimit{
				ID:                 "tokens",
				ModelConfigID:      &modelID,
				Metric:             ModelRateLimitMetricTokens,
				TokenMaxLimit:      &max,
				TokenResetDuration: &duration,
			},
		},
		{
			name: "invalid metric",
			rule: TableRateLimit{
				ID:                   "invalid",
				ModelConfigID:        &modelID,
				Metric:               "rpm",
				RequestMaxLimit:      &max,
				RequestResetDuration: &duration,
			},
			wantErr: true,
		},
		{
			name: "request metric with token fields",
			rule: TableRateLimit{
				ID:                 "mixed",
				ModelConfigID:      &modelID,
				Metric:             ModelRateLimitMetricRequests,
				TokenMaxLimit:      &max,
				TokenResetDuration: &duration,
			},
			wantErr: true,
		},
		{
			name: "token metric with request fields",
			rule: TableRateLimit{
				ID:                   "mixed-token",
				ModelConfigID:        &modelID,
				Metric:               ModelRateLimitMetricTokens,
				RequestMaxLimit:      &max,
				RequestResetDuration: &duration,
			},
			wantErr: true,
		},
		{
			name: "metric without limit fields",
			rule: TableRateLimit{
				ID:            "empty",
				ModelConfigID: &modelID,
				Metric:        ModelRateLimitMetricRequests,
			},
			wantErr: true,
		},
		{
			name: "legacy paired rule remains valid",
			rule: TableRateLimit{
				ID:                   "legacy",
				TokenMaxLimit:        &max,
				TokenResetDuration:   &duration,
				RequestMaxLimit:      &max,
				RequestResetDuration: &duration,
			},
		},
		{
			name: "metric without model owner",
			rule: TableRateLimit{
				ID:                   "unowned-metric",
				Metric:               ModelRateLimitMetricRequests,
				RequestMaxLimit:      &max,
				RequestResetDuration: &duration,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.rule.BeforeSave(nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BeforeSave() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
