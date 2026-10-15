package queue

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateName(t *testing.T) {
	valid := []string{"a", "orders", "Orders.v2", "team_a-events", strings.Repeat("x", MaxNameLength)}
	for _, name := range valid {
		assert.NoError(t, ValidateName("topic", name), name)
	}

	invalid := []string{"", strings.Repeat("x", MaxNameLength+1), "has space", "slash/name", "colon:name", "ünïcode", "new\nline"}
	for _, name := range invalid {
		err := ValidateName("topic", name)
		require.Error(t, err, name)
		assert.ErrorIs(t, err, ErrInvalidName, name)
	}
}

func TestValidateMessage(t *testing.T) {
	require.Error(t, validateMessage(nil, messageLimits{maxPayload: 10}))
	assert.ErrorIs(t, validateMessage(&Message{Topic: "bad topic"}, messageLimits{maxPayload: 10}), ErrInvalidName)

	ok := &Message{Topic: "t", Payload: make([]byte, 10)}
	assert.NoError(t, validateMessage(ok, messageLimits{maxPayload: 10}))
	assert.ErrorIs(t, validateMessage(&Message{Topic: "t", Payload: make([]byte, 11)}, messageLimits{maxPayload: 10}), ErrPayloadTooLarge)
	assert.Error(t, validateMessage(&Message{Topic: "t", ID: strings.Repeat("i", MaxIDLength+1)}, messageLimits{maxPayload: 0}))
	assert.Error(t, validateMessage(&Message{Topic: "t", Key: strings.Repeat("k", MaxIDLength+1)}, messageLimits{maxPayload: 0}))
	assert.NoError(t, validateMessage(&Message{Topic: "t", ID: strings.Repeat("i", MaxIDLength), Key: strings.Repeat("k", MaxIDLength)}, messageLimits{maxPayload: 0}))
	// A non-positive limit disables the size check.
	assert.NoError(t, validateMessage(&Message{Topic: "t", Payload: make([]byte, 1<<21)}, messageLimits{maxPayload: 0}))
}

func TestValidateMessageSchedule(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	limits := messageLimits{maxDelay: 7 * 24 * time.Hour, now: now}
	assert.NoError(t, validateMessage(&Message{Topic: "t"}, limits), "unscheduled")
	assert.NoError(t, validateMessage(&Message{Topic: "t", DeliverAt: now.Add(-time.Hour)}, limits), "in the past runs at once")
	assert.NoError(t, validateMessage(&Message{Topic: "t", DeliverAt: now.Add(7 * 24 * time.Hour)}, limits), "exactly at the horizon")
	assert.ErrorIs(t, validateMessage(&Message{Topic: "t", DeliverAt: now.Add(7*24*time.Hour + time.Second)}, limits), ErrScheduleTooFar)
	assert.NoError(t, validateMessage(&Message{Topic: "t", DeliverAt: now.Add(365 * 24 * time.Hour)}, messageLimits{}), "no horizon configured")
}

func TestPermanent(t *testing.T) {
	assert.Nil(t, Permanent(nil))

	base := errors.New("boom")
	p := Permanent(base)
	assert.True(t, IsPermanent(p))
	assert.ErrorIs(t, p, base)
	assert.Equal(t, "boom", p.Error())

	// Still detected when wrapped again by the handler.
	assert.True(t, IsPermanent(fmt.Errorf("handler: %w", p)))
	assert.False(t, IsPermanent(base))
	assert.False(t, IsPermanent(nil))
}

func TestDeliveryIDIsDeterministic(t *testing.T) {
	a := DeliveryID("m1", "g1")
	assert.Equal(t, a, DeliveryID("m1", "g1"))
	assert.NotEqual(t, a, DeliveryID("m1", "g2"))
	assert.NotEqual(t, a, DeliveryID("m2", "g1"))
	// The separator keeps ("ab","c") and ("a","bc") apart.
	assert.NotEqual(t, DeliveryID("ab", "c"), DeliveryID("a", "bc"))

	assert.Equal(t, deadLetterMessageID(a), deadLetterMessageID(a))
	assert.NotEqual(t, a, deadLetterMessageID(a))
}

func BenchmarkValidateName(b *testing.B) {
	for _, name := range []string{"orders", "team_a.events-v2.us-east-1", strings.Repeat("x", MaxNameLength)} {
		b.Run(fmt.Sprintf("len=%d", len(name)), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if err := ValidateName("topic", name); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
