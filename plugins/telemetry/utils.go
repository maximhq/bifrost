// Package telemetry provides Prometheus metrics collection and monitoring functionality
// for the Bifrost HTTP service. This file contains the setup and configuration
// for Prometheus metrics collection, including HTTP middleware and metric definitions.
package telemetry

import (
	"log"
	"math"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/valyala/fasthttp"
)

// getPrometheusLabelValues takes an array of expected label keys and a map of header values,
// and returns an array of values in the same order as the keys, using empty string for missing values.
func getPrometheusLabelValues(expectedLabels []string, headerValues map[string]string) []string {
	values := make([]string, len(expectedLabels))
	for i, label := range expectedLabels {
		if value, exists := headerValues[label]; exists {
			values[i] = value
		} else {
			values[i] = "" // Default empty value for missing labels
		}
	}
	return values
}

// collectPrometheusKeyValues collects all metrics for a request including:
// - Default metrics (path, method, status)
// - Custom dimension headers (x-bf-dim-*) — the canonical header prefix
// - Deprecated custom prometheus headers (x-bf-prom-*) — kept for backward compatibility
// Returns a map of all label values
func collectPrometheusKeyValues(ctx *fasthttp.RequestCtx) map[string]string {
	path := string(ctx.Path())
	// Prefer the matched route template over the raw path, whose embedded model names
	// and resource IDs would explode metric cardinality.
	if route, ok := ctx.UserValue(string(schemas.BifrostContextKeyHTTPRoute)).(string); ok && route != "" {
		path = route
	}
	method := string(ctx.Method())

	// Initialize with default metrics
	labelValues := map[string]string{
		"path":   path,
		"method": method,
	}

	// Collect custom dimension and prometheus headers
	ctx.Request.Header.All()(func(key, value []byte) bool {
		keyStr := strings.ToLower(string(key))
		// x-bf-dim-* (canonical; replaces x-bf-prom-*)
		if labelName, ok := strings.CutPrefix(keyStr, "x-bf-dim-"); ok && labelName != "" {
			if labelName != "path" && labelName != "method" { // it was prepoulated in the context
				labelValues[labelName] = string(value)
				ctx.SetUserValue(keyStr, string(value))
			}
		}
		// x-bf-prom-* (deprecated; kept for backward compatibility)
		if labelName, ok := strings.CutPrefix(keyStr, "x-bf-prom-"); ok && labelName != "" {
			// Only set if not already provided via x-bf-dim-* (x-bf-dim takes precedence)
			if _, alreadySet := labelValues[labelName]; !alreadySet && labelName != "path" && labelName != "method" {
				labelValues[labelName] = string(value)
				ctx.SetUserValue(keyStr, string(value))
			}
		}
		return true
	})

	return labelValues
}

// safeObserve safely records a value in a Prometheus histogram.
// It prevents recording invalid values (negative or infinite) that could cause issues.
func safeObserve(histogram *prometheus.HistogramVec, value float64, labels ...string) {
	if value > 0 && value < math.MaxFloat64 {
		metric, err := histogram.GetMetricWithLabelValues(labels...)
		if err != nil {
			log.Printf("Error getting metric with label values: %v", err)
		} else {
			metric.Observe(value)
		}
	}
}

// filterDisabledLabels returns labels with any entry present in disabled
// removed, using containsLabel's hyphen/underscore-insensitive match.
func filterDisabledLabels(labels, disabled []string, setName string, logger schemas.Logger) []string {
	if len(disabled) == 0 {
		return labels
	}
	// Cap-clip ([:0:0]) so a downstream `append(defaultBifrostLabels, ...)`
	// can't write through the caller's backing array.
	out := labels[:0:0]
	for _, label := range labels {
		if containsLabel(disabled, label) {
			logger.Info("%s label %s is disabled via disabled_labels, it will be omitted from all metrics", setName, label)
			continue
		}
		out = append(out, label)
	}
	return out
}

// containsLabel checks if a string slice contains a specific label, ignoring differences
// between underscores and hyphens. It checks for:
// - Direct match
// - Match after removing underscores
// - Match after replacing hyphens with underscores
// - Match after replacing underscores with hyphens
func containsLabel(slice []string, label string) bool {
	for _, s := range slice {
		// Direct match
		if s == label {
			return true
		}
		// Match after replacing hyphens with underscores
		if strings.ReplaceAll(s, "-", "_") == strings.ReplaceAll(label, "-", "_") {
			return true
		}
		// Match after replacing underscores with hyphens
		if strings.ReplaceAll(s, "_", "-") == strings.ReplaceAll(label, "_", "-") {
			return true
		}
	}
	return false
}
