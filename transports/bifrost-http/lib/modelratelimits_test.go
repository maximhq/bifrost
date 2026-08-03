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
	tests := []configstoreTables.TableRateLimit{
		{Metric: "", RequestMaxLimit: &maxLimit, RequestResetDuration: &duration},
		{Metric: configstoreTables.ModelRateLimitMetricRequests, TokenMaxLimit: &maxLimit, TokenResetDuration: &duration, RequestMaxLimit: &maxLimit, RequestResetDuration: &duration},
		{Metric: configstoreTables.ModelRateLimitMetricTokens, TokenMaxLimit: &maxLimit},
	}

	for _, rule := range tests {
		_, err := modelRateLimitReferenceKey(rule)
		require.Error(t, err)
	}
}
