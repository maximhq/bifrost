package queue

import (
	"fmt"
	"math"
	"math/rand/v2"
	"time"
)

// Subscription defaults applied by SubscribeOptions.WithDefaults.
const (
	DefaultConcurrency   = 4
	DefaultBatchSize     = 32
	DefaultPollInterval  = time.Second
	DefaultLeaseDuration = 30 * time.Second
	DefaultMaxAttempts   = 5
	DefaultBackoffBase   = time.Second
	DefaultBackoffMax    = 5 * time.Minute

	// minLeaseDuration keeps the heartbeat (lease/3) from spinning.
	minLeaseDuration = 300 * time.Millisecond
	// pollJitter spreads polls of many instances so they do not hit the store
	// in lockstep.
	pollJitter = 0.2
)

// StartFrom selects which messages a consumer group receives when it is
// registered for the first time. It has no effect on a group that already
// exists.
type StartFrom string

const (
	// StartFromLatest delivers only messages published after the group is
	// registered, matching Kafka's "latest" offset reset.
	StartFromLatest StartFrom = "latest"
	// StartFromEarliest also delivers every message still retained on the topic.
	StartFromEarliest StartFrom = "earliest"
)

// SubscribeOptions tunes one subscription.
type SubscribeOptions struct {
	Concurrency     int           // handlers running at once; also the max in-flight deliveries
	BatchSize       int           // max deliveries claimed per poll
	PollInterval    time.Duration // wait between polls that found nothing
	LeaseDuration   time.Duration // how long a claim is held without a heartbeat
	MaxAttempts     int           // attempts before a delivery is dead-lettered
	BackoffBase     time.Duration // first retry delay
	BackoffMax      time.Duration // retry delay cap
	DeadLetterTopic string        // optional topic that receives a copy of dead deliveries
	MaxInFlight     int           // cap on the group's deliveries in flight across all instances; 0 means none
	StartFrom       StartFrom     // applies only when the group is first registered
}

// SubscribeOption configures a subscription.
type SubscribeOption func(*SubscribeOptions)

// WithConcurrency sets how many handlers run at once.
func WithConcurrency(n int) SubscribeOption {
	return func(o *SubscribeOptions) { o.Concurrency = n }
}

// WithBatchSize sets the maximum number of deliveries claimed per poll.
func WithBatchSize(n int) SubscribeOption {
	return func(o *SubscribeOptions) { o.BatchSize = n }
}

// WithPollInterval sets the wait between polls that found nothing.
func WithPollInterval(d time.Duration) SubscribeOption {
	return func(o *SubscribeOptions) { o.PollInterval = d }
}

// WithLeaseDuration sets how long a claim survives without a heartbeat.
func WithLeaseDuration(d time.Duration) SubscribeOption {
	return func(o *SubscribeOptions) { o.LeaseDuration = d }
}

// WithMaxAttempts sets how many attempts a delivery gets before it is
// dead-lettered.
func WithMaxAttempts(n int) SubscribeOption {
	return func(o *SubscribeOptions) { o.MaxAttempts = n }
}

// WithBackoff sets the exponential retry backoff's first delay and cap.
func WithBackoff(base, max time.Duration) SubscribeOption {
	return func(o *SubscribeOptions) {
		o.BackoffBase = base
		o.BackoffMax = max
	}
}

// WithDeadLetterTopic publishes a copy of every dead-lettered delivery to
// topic.
func WithDeadLetterTopic(topic string) SubscribeOption {
	return func(o *SubscribeOptions) { o.DeadLetterTopic = topic }
}

// WithMaxInFlight caps how many of the group's deliveries run at once across
// every instance consuming it, on top of each instance's own Concurrency.
// Every subscriber of the group should use the same value. Zero means no cap.
func WithMaxInFlight(n int) SubscribeOption {
	return func(o *SubscribeOptions) { o.MaxInFlight = n }
}

// WithStartFrom selects which messages a newly registered group receives.
func WithStartFrom(s StartFrom) SubscribeOption {
	return func(o *SubscribeOptions) { o.StartFrom = s }
}

// WithDefaults returns a copy of o with zero-value fields filled.
func (o SubscribeOptions) WithDefaults() SubscribeOptions {
	if o.Concurrency <= 0 {
		o.Concurrency = DefaultConcurrency
	}
	if o.BatchSize <= 0 {
		o.BatchSize = DefaultBatchSize
	}
	if o.PollInterval <= 0 {
		o.PollInterval = DefaultPollInterval
	}
	if o.LeaseDuration <= 0 {
		o.LeaseDuration = DefaultLeaseDuration
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = DefaultMaxAttempts
	}
	if o.BackoffBase <= 0 {
		o.BackoffBase = DefaultBackoffBase
	}
	if o.BackoffMax <= 0 {
		o.BackoffMax = DefaultBackoffMax
	}
	if o.StartFrom == "" {
		o.StartFrom = StartFromLatest
	}
	return o
}

// NewSubscribeOptions applies opts over the defaults and validates the result.
func NewSubscribeOptions(opts ...SubscribeOption) (SubscribeOptions, error) {
	var o SubscribeOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	o = o.WithDefaults()
	if o.LeaseDuration < minLeaseDuration {
		return o, fmt.Errorf("queue: lease duration %s is below the %s minimum", o.LeaseDuration, minLeaseDuration)
	}
	if o.BackoffMax < o.BackoffBase {
		return o, fmt.Errorf("queue: backoff max %s is below backoff base %s", o.BackoffMax, o.BackoffBase)
	}
	if o.MaxInFlight < 0 {
		return o, fmt.Errorf("queue: max in flight %d is negative", o.MaxInFlight)
	}
	if o.StartFrom != StartFromLatest && o.StartFrom != StartFromEarliest {
		return o, fmt.Errorf("queue: unknown start position %q", o.StartFrom)
	}
	if o.DeadLetterTopic != "" {
		if err := ValidateName("dead letter topic", o.DeadLetterTopic); err != nil {
			return o, err
		}
	}
	return o, nil
}

// Backoff returns the delay before retrying a delivery whose attempt-th try
// failed: BackoffBase doubled per attempt, capped at BackoffMax, with "equal
// jitter" so the result lies in [d/2, d]. rnd returns a value in [0, 1); nil
// uses math/rand.
func (o SubscribeOptions) Backoff(attempt int, rnd func() float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := o.BackoffMax
	// Guard the shift: 2^62 nanoseconds already exceeds any sane cap.
	if shift := attempt - 1; shift < 62 {
		if exp := float64(o.BackoffBase) * math.Pow(2, float64(shift)); exp < float64(o.BackoffMax) {
			d = time.Duration(exp)
		}
	}
	if rnd == nil {
		rnd = rand.Float64
	}
	half := d / 2
	return half + time.Duration(rnd()*float64(d-half))
}

// jitterInterval returns d scaled by a random factor in [1-pollJitter,
// 1+pollJitter].
func jitterInterval(d time.Duration, rnd func() float64) time.Duration {
	if rnd == nil {
		rnd = rand.Float64
	}
	factor := 1 - pollJitter + 2*pollJitter*rnd()
	return time.Duration(float64(d) * factor)
}
