package configstore

import (
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
)

func TestGenerateRateLimitHashIncludesMetric(t *testing.T) {
	max := int64(15)
	duration := "1m"
	requests := tables.TableRateLimit{
		ID:                   "rule",
		Metric:               tables.ModelRateLimitMetricRequests,
		RequestMaxLimit:      &max,
		RequestResetDuration: &duration,
	}
	tokens := requests
	tokens.Metric = tables.ModelRateLimitMetricTokens
	tokens.RequestMaxLimit = nil
	tokens.RequestResetDuration = nil
	tokens.TokenMaxLimit = &max
	tokens.TokenResetDuration = &duration

	requestHash, err := GenerateRateLimitHash(requests)
	if err != nil {
		t.Fatal(err)
	}
	tokenHash, err := GenerateRateLimitHash(tokens)
	if err != nil {
		t.Fatal(err)
	}
	if requestHash == tokenHash {
		t.Fatal("changing the metric must change the rate-limit config hash")
	}
	metricOnly := requests
	metricOnly.Metric = tables.ModelRateLimitMetricTokens
	metricOnlyHash, err := GenerateRateLimitHash(metricOnly)
	if err != nil {
		t.Fatal(err)
	}
	if metricOnlyHash == requestHash {
		t.Fatal("metric alone must change the rate-limit config hash")
	}
}
