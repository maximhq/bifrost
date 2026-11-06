package logstore

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfig_UnmarshalCaptureClientIP keeps client IP capture opt-in: absent means off, and the flag survives a disabled store and a config-store round trip.
func TestConfig_UnmarshalCaptureClientIP(t *testing.T) {
	var cfg Config
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"type":"sqlite","config":{"path":"logs.db"}}`), &cfg))
	assert.False(t, cfg.CaptureClientIP, "client IP capture is off unless configured")

	cfg = Config{}
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":false,"capture_client_ip":true}`), &cfg))
	assert.True(t, cfg.CaptureClientIP)

	cfg = Config{}
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"type":"sqlite","config":{"path":"logs.db"},"capture_client_ip":true}`), &cfg))
	data, err := json.Marshal(&cfg)
	require.NoError(t, err)
	var roundTripped Config
	require.NoError(t, json.Unmarshal(data, &roundTripped))
	assert.True(t, roundTripped.CaptureClientIP, "the flag persists through the config store's JSON column")
}
