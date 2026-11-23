package schemas

import "time"

// UpstreamHealthWindow is the trailing window UpstreamHealth.RecentFailures and
// RecentRateLimits count over.
const UpstreamHealthWindow = 5 * time.Minute

// UpstreamHealthStatus is the routing-relevant verdict for one provider/model pair, derived
// from the outcomes Bifrost itself observed when it dispatched requests upstream.
type UpstreamHealthStatus string

const (
	// UpstreamHealthAvailable: the last observed outcome was a success, or every failure
	// it recorded has served its cooldown.
	UpstreamHealthAvailable UpstreamHealthStatus = "available"
	// UpstreamHealthDegraded: recent requests failed transiently (5xx, network, timeout) but
	// nothing says waiting is required; the route is usable and may recover on retry.
	UpstreamHealthDegraded UpstreamHealthStatus = "degraded"
	// UpstreamHealthCoolingDown: every key Bifrost tried for this pair was refused for a
	// reason that waiting is needed for (rate limit, exhausted quota, rejected credential,
	// no model access, region block, retired model). Requests sent before CooldownUntil are
	// expected to fail the same way.
	UpstreamHealthCoolingDown UpstreamHealthStatus = "cooling_down"
)

// UpstreamHealth is a point-in-time snapshot of what Bifrost has recently observed from one
// upstream provider serving one model. Model is the model Bifrost sent to the provider
// before any per-key alias resolution, without a provider prefix.
//
// The state is in-memory and per process: it starts empty on boot, a pair appears only once
// a request has failed on it, and a later success on the pair returns it to available.
type UpstreamHealth struct {
	Provider ModelProvider        `json:"provider"`
	Model    string               `json:"model"`
	Status   UpstreamHealthStatus `json:"status"`
	// Available is false exactly while Status is cooling_down.
	Available bool `json:"available"`
	// EffectiveRemainingFraction is 0 while the pair is cooling down, otherwise the smallest
	// remaining fraction of the static provider and per-model limits the reading virtual key
	// has configured for this provider (1 when it has none).
	EffectiveRemainingFraction float64 `json:"effective_remaining_fraction"`
	// FailureClass and StatusCode describe the most recent recorded failure.
	FailureClass FailureClass `json:"failure_class,omitempty"`
	StatusCode   *int         `json:"status_code,omitempty"`
	// CooldownUntil is when a cooling_down pair is expected to accept requests again;
	// RetryAfterMs is the time left until then, as of the snapshot.
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
	RetryAfterMs  int64      `json:"retry_after_ms,omitempty"`
	LastFailureAt *time.Time `json:"last_failure_at,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	// ConsecutiveFailures counts failed requests since the last success.
	ConsecutiveFailures int `json:"consecutive_failures"`
	// RecentFailures and RecentRateLimits count failed requests (and those of them refused
	// for rate limit or quota) inside the trailing window reported as
	// upstream_health_window_seconds.
	RecentFailures   int `json:"recent_failures"`
	RecentRateLimits int `json:"recent_rate_limits"`
}
