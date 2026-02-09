// Package telemetry provides Prometheus metrics collection and monitoring functionality
// for the Bifrost HTTP service. This file contains the setup and configuration
// for Prometheus metrics collection, including HTTP middleware and metric definitions.
package telemetry

import (
	"log"
	"math"
	"strings"

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
// - Default metrics (path, method, status, request size)
// - Custom prometheus headers (x-bf-prom-*)
// Returns a map of all label values
func collectPrometheusKeyValues(ctx *fasthttp.RequestCtx) map[string]string {
	path := string(ctx.Path())
	method := string(ctx.Method())

	// Initialize with default metrics
	labelValues := map[string]string{
		"path":   path,
		"method": method,
	}

	// Collect custom prometheus headers
	ctx.Request.Header.All()(func(key, value []byte) bool {
		keyStr := strings.ToLower(string(key))
		if strings.HasPrefix(keyStr, "x-bf-prom-") {
			labelName := strings.TrimPrefix(keyStr, "x-bf-prom-")
			labelValues[labelName] = string(value)
			ctx.SetUserValue(keyStr, string(value))
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
