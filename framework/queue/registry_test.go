package queue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubQueue is a Queue that records nothing; registry tests only need identity.
type stubQueue struct{ Queue }

// testQueueType returns a type name no other run has registered, since the
// registry is process-global and tests may run with -count > 1.
func testQueueType(prefix string) QueueType {
	return QueueType(prefix + "-" + uuid.NewString()[:8])
}

func TestNewQueueDisabledOrNil(t *testing.T) {
	q, err := NewQueue(context.Background(), nil, Dependencies{}, nil)
	assert.NoError(t, err)
	assert.Nil(t, q)

	q, err = NewQueue(context.Background(), &Config{Enabled: false, Type: "anything"}, Dependencies{}, nil)
	assert.NoError(t, err)
	assert.Nil(t, q)
}

func TestNewQueueUnknownType(t *testing.T) {
	_, err := NewQueue(context.Background(), &Config{Enabled: true, Type: "test-unregistered"}, Dependencies{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown queue type")
}

func TestRegisterAndNewQueue(t *testing.T) {
	typ := testQueueType("test-registered")
	want := &stubQueue{}
	var gotRaw json.RawMessage
	Register(typ, func(ctx context.Context, cfg *Config, deps Dependencies, logger schemas.Logger) (Queue, error) {
		gotRaw, _ = cfg.Config.(json.RawMessage)
		return want, nil
	})
	assert.Contains(t, Registered(), typ)

	var cfg Config
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"type":"`+string(typ)+`","config":{"a":1}}`), &cfg))
	q, err := NewQueue(context.Background(), &cfg, Dependencies{}, nil)
	require.NoError(t, err)
	assert.Same(t, want, q)
	assert.JSONEq(t, `{"a":1}`, string(gotRaw))

	assert.Panics(t, func() {
		Register(typ, func(context.Context, *Config, Dependencies, schemas.Logger) (Queue, error) { return nil, nil })
	}, "duplicate registration")
}

func TestRegisterFactoryErrorPropagates(t *testing.T) {
	typ := testQueueType("test-failing")
	boom := errors.New("boom")
	Register(typ, func(context.Context, *Config, Dependencies, schemas.Logger) (Queue, error) { return nil, boom })
	_, err := NewQueue(context.Background(), &Config{Enabled: true, Type: typ}, Dependencies{}, nil)
	assert.ErrorIs(t, err, boom)
}

func TestRegisterRejectsBadInput(t *testing.T) {
	assert.Panics(t, func() {
		Register("", func(context.Context, *Config, Dependencies, schemas.Logger) (Queue, error) { return nil, nil })
	})
	assert.Panics(t, func() { Register(testQueueType("test-nil-factory"), nil) })
}
