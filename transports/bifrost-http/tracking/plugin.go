// Package tracking provides Prometheus metrics collection and monitoring functionality
// for the Bifrost HTTP service. It includes middleware for HTTP request tracking
// and a plugin for tracking upstream provider metrics.
package tracking

import (
	"context"
	"fmt"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/prometheus/client_golang/prometheus"
)

// Define context key type for storing start time
type contextKey string

// PrometheusContextKey is a custom type for prometheus context keys to prevent collisions
type PrometheusContextKey string

const startTimeKey contextKey = "startTime"
const methodKey contextKey = "method"

// PrometheusPlugin implements the schemas.Plugin interface for Prometheus metrics.
// It tracks metrics for upstream provider requests, including:
//   - Total number of requests
//   - Request latency
//   - Error counts
type PrometheusPlugin struct {
	// Metrics are defined using promauto for automatic registration
	UpstreamRequestsTotal *prometheus.CounterVec
	UpstreamLatency       *prometheus.HistogramVec
}

// NewPrometheusPlugin creates a new PrometheusPlugin with initialized metrics.
func NewPrometheusPlugin() *PrometheusPlugin {
	return &PrometheusPlugin{
		UpstreamRequestsTotal: bifrostUpstreamRequestsTotal,
		UpstreamLatency:       bifrostUpstreamLatencySeconds,
	}
}

// PreHook records the start time of the request in the context.
// This time is used later in PostHook to calculate request duration.
func (p *PrometheusPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, error) {
	*ctx = context.WithValue(*ctx, startTimeKey, time.Now())

	if req.Input.ChatCompletionInput != nil {
		*ctx = context.WithValue(*ctx, methodKey, "chat")
	} else if req.Input.TextCompletionInput != nil {
		*ctx = context.WithValue(*ctx, methodKey, "text")
	}

	return req, nil
}

// PostHook calculates duration and records upstream metrics for successful requests.
// It records:
//   - Request latency
//   - Total request count
func (p *PrometheusPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse) (*schemas.BifrostResponse, error) {
	startTime, ok := (*ctx).Value(startTimeKey).(time.Time)
	if !ok {
		fmt.Println("Warning: startTime not found in context for Prometheus PostHook")
		return result, nil
	}

	method, ok := (*ctx).Value(methodKey).(string)
	if !ok {
		fmt.Println("Warning: method not found in context for Prometheus PostHook")
		return result, nil
	}

	// Collect prometheus labels from context
	labelValues := map[string]string{
		"target": fmt.Sprintf("%s/%s", result.ExtraFields.Provider, result.Model),
		"method": method,
	}

	// Get all prometheus labels from context
	for _, key := range customLabels {
		if value := (*ctx).Value(PrometheusContextKey(key)); value != nil {
			if strValue, ok := value.(string); ok {
				labelValues[key] = strValue
			}
		}
	}

	// Get label values in the correct order
	promLabelValues := getPrometheusLabelValues(append([]string{"target", "method"}, customLabels...), labelValues)

	duration := time.Since(startTime).Seconds()
	p.UpstreamLatency.WithLabelValues(promLabelValues...).Observe(duration)
	p.UpstreamRequestsTotal.WithLabelValues(promLabelValues...).Inc()

	return result, nil
}
