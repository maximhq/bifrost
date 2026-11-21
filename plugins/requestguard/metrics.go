package requestguard

import (
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics owns the request-guard collectors and the registry they are exposed
// through. A private registry is used when no registerer is provided, so the
// package never changes Prometheus' process-wide default registry.
type Metrics struct {
	registry            *prometheus.Registry
	requestsBlocked     *prometheus.CounterVec
	capabilityFailures  *prometheus.CounterVec
	downgrades          *prometheus.CounterVec
	coalescedExecutions *prometheus.CounterVec
	estimatedSavingsUSD *prometheus.CounterVec
	evalDuration        *prometheus.HistogramVec
}

// NewMetrics registers request-guard collectors when metrics are enabled. If
// cfg.Registerer is nil, the collectors are registered on a private registry.
func NewMetrics(cfg MetricsConfig) (*Metrics, error) {
	m := &Metrics{}
	if !cfg.Enabled {
		return m, nil
	}

	registerer := cfg.Registerer
	if registerer == nil {
		m.registry = prometheus.NewRegistry()
		registerer = m.registry
	} else if registry, ok := registerer.(*prometheus.Registry); ok {
		m.registry = registry
	}

	collectors := []struct {
		name string
		new  func() prometheus.Collector
		set  func(prometheus.Collector) error
	}{
		{
			name: "requests_blocked_total",
			new: func() prometheus.Collector {
				return prometheus.NewCounterVec(prometheus.CounterOpts{
					Namespace: "bifrost", Subsystem: "requestguard",
					Name: "requests_blocked_total", Help: "Total requests blocked by request-guard policy.",
				}, []string{"reason", "provider", "model"})
			},
			set: func(c prometheus.Collector) error {
				v, ok := c.(*prometheus.CounterVec)
				if !ok {
					return collectorTypeError("requests_blocked_total")
				}
				m.requestsBlocked = v
				return nil
			},
		},
		{
			name: "capability_failures_total",
			new: func() prometheus.Collector {
				return prometheus.NewCounterVec(prometheus.CounterOpts{
					Namespace: "bifrost", Subsystem: "requestguard",
					Name: "capability_failures_total", Help: "Total request capability failures observed by request guard.",
				}, []string{"capability", "provider", "model"})
			},
			set: func(c prometheus.Collector) error {
				v, ok := c.(*prometheus.CounterVec)
				if !ok {
					return collectorTypeError("capability_failures_total")
				}
				m.capabilityFailures = v
				return nil
			},
		},
		{
			name: "downgrades_total",
			new: func() prometheus.Collector {
				return prometheus.NewCounterVec(prometheus.CounterOpts{
					Namespace: "bifrost", Subsystem: "requestguard",
					Name: "downgrades_total", Help: "Total model downgrades selected by request guard.",
				}, []string{"reason", "from_model", "to_model"})
			},
			set: func(c prometheus.Collector) error {
				v, ok := c.(*prometheus.CounterVec)
				if !ok {
					return collectorTypeError("downgrades_total")
				}
				m.downgrades = v
				return nil
			},
		},
		{
			name: "coalesced_executions_avoided_total",
			new: func() prometheus.Collector {
				return prometheus.NewCounterVec(prometheus.CounterOpts{
					Namespace: "bifrost", Subsystem: "requestguard",
					Name: "coalesced_executions_avoided_total", Help: "Total duplicate executions avoided by request coalescing.",
				}, []string{"provider", "model", "stream"})
			},
			set: func(c prometheus.Collector) error {
				v, ok := c.(*prometheus.CounterVec)
				if !ok {
					return collectorTypeError("coalesced_executions_avoided_total")
				}
				m.coalescedExecutions = v
				return nil
			},
		},
		{
			name: "estimated_savings_usd_total",
			new: func() prometheus.Collector {
				return prometheus.NewCounterVec(prometheus.CounterOpts{
					Namespace: "bifrost", Subsystem: "requestguard",
					Name: "estimated_savings_usd_total", Help: "Estimated request cost savings attributed to request-guard decisions.",
				}, []string{"source", "provider", "model"})
			},
			set: func(c prometheus.Collector) error {
				v, ok := c.(*prometheus.CounterVec)
				if !ok {
					return collectorTypeError("estimated_savings_usd_total")
				}
				m.estimatedSavingsUSD = v
				return nil
			},
		},
		{
			name: "eval_duration_seconds",
			new: func() prometheus.Collector {
				return prometheus.NewHistogramVec(prometheus.HistogramOpts{
					Namespace: "bifrost", Subsystem: "requestguard",
					Name: "eval_duration_seconds", Help: "Time spent evaluating request-guard stages.",
					Buckets: prometheus.DefBuckets,
				}, []string{"stage"})
			},
			set: func(c prometheus.Collector) error {
				v, ok := c.(*prometheus.HistogramVec)
				if !ok {
					return collectorTypeError("eval_duration_seconds")
				}
				m.evalDuration = v
				return nil
			},
		},
	}

	for _, spec := range collectors {
		collector, err := registerCollector(registerer, spec.new())
		if err != nil {
			return nil, errors.New("register requestguard metric " + spec.name + ": " + err.Error())
		}
		if err := spec.set(collector); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func collectorTypeError(name string) error {
	return errors.New("registered collector has an unexpected type for " + name)
}

func registerCollector(registerer prometheus.Registerer, collector prometheus.Collector) (prometheus.Collector, error) {
	if err := registerer.Register(collector); err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			return already.ExistingCollector, nil
		}
		return nil, err
	}
	return collector, nil
}

// GetRegistry returns the registry owned or directly supplied to this Metrics
// instance. It returns nil for non-Registry registerers and disabled metrics.
func (m *Metrics) GetRegistry() *prometheus.Registry {
	if m == nil {
		return nil
	}
	return m.registry
}

func (m *Metrics) RecordRequestBlocked(reason string, provider schemas.ModelProvider, model string) {
	if m != nil && m.requestsBlocked != nil {
		m.requestsBlocked.WithLabelValues(reason, string(provider), model).Inc()
	}
}

func (m *Metrics) RecordCapabilityFailure(capability string, provider schemas.ModelProvider, model string) {
	if m != nil && m.capabilityFailures != nil {
		m.capabilityFailures.WithLabelValues(capability, string(provider), model).Inc()
	}
}

func (m *Metrics) RecordDowngrade(reason, fromModel, toModel string) {
	if m != nil && m.downgrades != nil {
		m.downgrades.WithLabelValues(reason, fromModel, toModel).Inc()
	}
}

func (m *Metrics) RecordCoalescedExecutionAvoided(provider schemas.ModelProvider, model string, streaming bool) {
	if m != nil && m.coalescedExecutions != nil {
		m.coalescedExecutions.WithLabelValues(string(provider), model, strconv.FormatBool(streaming)).Inc()
	}
}

func (m *Metrics) RecordEstimatedSavingsUSD(source string, provider schemas.ModelProvider, model string, amount float64) {
	if m == nil || m.estimatedSavingsUSD == nil || amount <= 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return
	}
	m.estimatedSavingsUSD.WithLabelValues(source, string(provider), model).Add(amount)
}

func (m *Metrics) ObserveEvalDuration(stage string, elapsed time.Duration) {
	if m != nil && m.evalDuration != nil && elapsed >= 0 {
		m.evalDuration.WithLabelValues(stage).Observe(elapsed.Seconds())
	}
}
