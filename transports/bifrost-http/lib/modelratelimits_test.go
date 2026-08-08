package lib

import (
	"testing"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

func TestModelRateLimitReferenceKeyCanonicalizesWindows(t *testing.T) {
	maxLimit := int64(15)
	minute := "60s"
	rule := configstoreTables.TableRateLimit{
		Metric:               configstoreTables.ModelRateLimitMetricRequests,
		RequestMaxLimit:      &maxLimit,
		RequestResetDuration: &minute,
	}

	key, err := modelRateLimitReferenceKey(rule)
	require.NoError(t, err)
	require.Equal(t, "requests:1m0s", key)
}

func TestModelRateLimitReferenceKeyRejectsPairedOrInvalidRules(t *testing.T) {
	maxLimit := int64(15)
	duration := "1m"
	tests := []struct {
		name string
		rule configstoreTables.TableRateLimit
	}{
		{name: "missing metric", rule: configstoreTables.TableRateLimit{Metric: "", RequestMaxLimit: &maxLimit, RequestResetDuration: &duration}},
		{name: "paired token and request fields", rule: configstoreTables.TableRateLimit{Metric: configstoreTables.ModelRateLimitMetricRequests, TokenMaxLimit: &maxLimit, TokenResetDuration: &duration, RequestMaxLimit: &maxLimit, RequestResetDuration: &duration}},
		{name: "missing reset duration", rule: configstoreTables.TableRateLimit{Metric: configstoreTables.ModelRateLimitMetricTokens, TokenMaxLimit: &maxLimit}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := modelRateLimitReferenceKey(tt.rule)
			require.Error(t, err)
		})
	}
}
