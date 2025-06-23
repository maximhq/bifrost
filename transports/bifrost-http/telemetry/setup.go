// Package telemetry provides Prometheus metrics collection and monitoring functionality
// for the Bifrost HTTP service. This file contains the setup and configuration
// for Prometheus metrics collection, including HTTP middleware and metric definitions.
package telemetry

import (
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/valyala/fasthttp"
)

var (
	// httpRequestsTotal tracks the total number of HTTP requests
	httpRequestsTotal *prometheus.CounterVec
	// httpRequestDuration tracks the duration of HTTP requests
	httpRequestDuration *prometheus.HistogramVec
	// httpRequestSizeBytes tracks the size of incoming HTTP requests
	httpRequestSizeBytes *prometheus.HistogramVec
	// httpResponseSizeBytes tracks the size of outgoing HTTP responses
	httpResponseSizeBytes *prometheus.HistogramVec

	// bifrostUpstreamRequestsTotal tracks the total number of requests forwarded to upstream providers by Bifrost.
	bifrostUpstreamRequestsTotal *prometheus.CounterVec

	bifrostUpstreamLatencySeconds *prometheus.HistogramVec

	// customLabels stores the expected label names in order
	customLabels  []string
	isInitialized bool
)

func InitPrometheusMetrics(labels []string) {
	if isInitialized {
		return
	}

	customLabels = labels

	httpDefaultLabels := []string{"path", "method", "status"}
	bifrostDefaultLabels := []string{"target", "method"}

	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests.",
		},
		append(httpDefaultLabels, labels...),
	)

	// httpRequestDuration tracks the duration of HTTP requests
	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Duration of HTTP requests.",
			Buckets: prometheus.DefBuckets, // Default buckets: .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10
		},
		append(httpDefaultLabels, labels...),
	)

	// httpRequestSizeBytes tracks the size of incoming HTTP requests
	httpRequestSizeBytes = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_size_bytes",
			Help:    "Size of HTTP requests.",
			Buckets: prometheus.ExponentialBuckets(100, 10, 8), // 100B to 1GB
		},
		append(httpDefaultLabels, labels...),
	)

	// httpResponseSizeBytes tracks the size of outgoing HTTP responses
	httpResponseSizeBytes = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_response_size_bytes",
			Help:    "Size of HTTP responses.",
			Buckets: prometheus.ExponentialBuckets(100, 10, 8), // 100B to 1GB
		},
		append(httpDefaultLabels, labels...),
	)

	// Bifrost Upstream Metrics (Defined globally, used by PrometheusPlugin)
	bifrostUpstreamRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bifrost_upstream_requests_total",
			Help: "Total number of requests forwarded to upstream providers by Bifrost.",
		},
		append(bifrostDefaultLabels, labels...),
	)

	bifrostUpstreamLatencySeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "bifrost_upstream_latency_seconds",
			Help:    "Latency of requests forwarded to upstream providers by Bifrost.",
			Buckets: prometheus.DefBuckets,
		},
		append(bifrostDefaultLabels, labels...),
	)

	isInitialized = true
}

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
	ctx.Request.Header.VisitAll(func(key, value []byte) {
		keyStr := strings.ToLower(string(key))
		if strings.HasPrefix(keyStr, "x-bf-prom-") {
			labelName := strings.TrimPrefix(keyStr, "x-bf-prom-")
			labelValues[labelName] = string(value)
			ctx.SetUserValue(keyStr, string(value))
		}
	})

	return labelValues
}

// PrometheusMiddleware wraps a FastHTTP handler to collect Prometheus metrics.
// It tracks:
//   - Total number of requests
//   - Request duration
//   - Request and response sizes
//   - HTTP status codes
//   - Bifrost upstream requests and errors
func PrometheusMiddleware(handler fasthttp.RequestHandler) fasthttp.RequestHandler {
	if !isInitialized {
		log.Println("Prometheus metrics are not initialized. Please call InitPrometheusMetrics first. Skipping metrics collection.")
		return handler
	}

	return func(ctx *fasthttp.RequestCtx) {
		start := time.Now()

		// Collect request metrics and headers
		promKeyValues := collectPrometheusKeyValues(ctx)
		reqSize := float64(ctx.Request.Header.ContentLength())

		// Process the request
		handler(ctx)

		// Record metrics after request completion
		duration := time.Since(start).Seconds()
		status := strconv.Itoa(ctx.Response.StatusCode())
		respSize := float64(ctx.Response.Header.ContentLength())

		// Add status to the label values
		promKeyValues["status"] = status

		// Get label values in the correct order
		promLabelValues := getPrometheusLabelValues(append([]string{"path", "method", "status"}, customLabels...), promKeyValues)

		// Record all metrics with prometheus labels
		httpRequestsTotal.WithLabelValues(promLabelValues...).Inc()
		httpRequestDuration.WithLabelValues(promLabelValues...).Observe(duration)
		if reqSize >= 0 {
			safeObserve(httpRequestSizeBytes, reqSize, promLabelValues...)
		}
		if respSize >= 0 {
			safeObserve(httpResponseSizeBytes, respSize, promLabelValues...)
		}
	}
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
