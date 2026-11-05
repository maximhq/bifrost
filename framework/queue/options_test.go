package queue

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscribeOptionsDefaults(t *testing.T) {
	o, err := NewSubscribeOptions()
	require.NoError(t, err)
	assert.Equal(t, SubscribeOptions{
		Concurrency:   DefaultConcurrency,
		BatchSize:     DefaultBatchSize,
		PollInterval:  DefaultPollInterval,
		LeaseDuration: DefaultLeaseDuration,
		MaxAttempts:   DefaultMaxAttempts,
		BackoffBase:   DefaultBackoffBase,
		BackoffMax:    DefaultBackoffMax,
		StartFrom:     StartFromLatest,
	}, o)
}

func TestSubscribeOptionsApply(t *testing.T) {
	o, err := NewSubscribeOptions(
		WithConcurrency(8),
		WithBatchSize(100),
		WithPollInterval(250*time.Millisecond),
		WithLeaseDuration(time.Minute),
		WithMaxAttempts(3),
		WithBackoff(time.Millisecond, time.Second),
		WithDeadLetterTopic("orders.dlq"),
		WithStartFrom(StartFromEarliest),
		WithMaxInFlight(5),
		nil, // nil options are ignored
	)
	require.NoError(t, err)
	assert.Equal(t, 8, o.Concurrency)
	assert.Equal(t, 100, o.BatchSize)
	assert.Equal(t, 250*time.Millisecond, o.PollInterval)
	assert.Equal(t, time.Minute, o.LeaseDuration)
	assert.Equal(t, 3, o.MaxAttempts)
	assert.Equal(t, time.Millisecond, o.BackoffBase)
	assert.Equal(t, time.Second, o.BackoffMax)
	assert.Equal(t, "orders.dlq", o.DeadLetterTopic)
	assert.Equal(t, StartFromEarliest, o.StartFrom)
	assert.Equal(t, 5, o.MaxInFlight)
}

func TestSubscribeOptionsValidation(t *testing.T) {
	cases := map[string][]SubscribeOption{
		"lease too short":  {WithLeaseDuration(time.Millisecond)},
		"backoff inverted": {WithBackoff(time.Minute, time.Second)},
		"bad start":        {WithStartFrom("middle")},
		"bad dead letter":  {WithDeadLetterTopic("not valid")},
	}
	for name, opts := range cases {
		_, err := NewSubscribeOptions(opts...)
		assert.Error(t, err, name)
	}
}

func TestBackoffBoundsAndGrowth(t *testing.T) {
	o := SubscribeOptions{BackoffBase: time.Second, BackoffMax: time.Minute}
	low := func() float64 { return 0 }
	high := func() float64 { return 0.999999 }

	// Equal jitter: the delay lies in [d/2, d] where d doubles per attempt.
	for attempt, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 6: 32 * time.Second} {
		assert.Equal(t, want/2, o.Backoff(attempt, low), "attempt %d low", attempt)
		assert.InDelta(t, float64(want), float64(o.Backoff(attempt, high)), float64(time.Millisecond), "attempt %d high", attempt)
	}

	// Capped at BackoffMax, including attempts large enough to overflow 2^n.
	for _, attempt := range []int{7, 20, 64, 1000} {
		d := o.Backoff(attempt, high)
		assert.LessOrEqual(t, d, time.Minute, "attempt %d", attempt)
		assert.GreaterOrEqual(t, o.Backoff(attempt, low), 30*time.Second, "attempt %d", attempt)
	}

	// Attempts below 1 behave like the first attempt.
	assert.Equal(t, o.Backoff(1, low), o.Backoff(0, low))

	// The default random source stays in bounds.
	for range 100 {
		d := o.Backoff(3, nil)
		assert.GreaterOrEqual(t, d, 2*time.Second)
		assert.LessOrEqual(t, d, 4*time.Second)
	}
}

func TestJitterInterval(t *testing.T) {
	assert.Equal(t, 800*time.Millisecond, jitterInterval(time.Second, func() float64 { return 0 }))
	assert.Equal(t, time.Second, jitterInterval(time.Second, func() float64 { return 0.5 }))
	for range 100 {
		d := jitterInterval(time.Second, nil)
		assert.GreaterOrEqual(t, d, 800*time.Millisecond)
		assert.LessOrEqual(t, d, 1200*time.Millisecond)
	}
}
