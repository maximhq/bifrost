package queue

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigUnmarshalLogStore(t *testing.T) {
	var c Config
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"type":"logstore","config":{"retention_hours":24,"janitor_interval":"1m","max_payload_bytes":2048}}`), &c))
	assert.True(t, c.Enabled)
	assert.Equal(t, QueueTypeLogStore, c.Type)
	cfg, ok := c.Config.(*LogStoreQueueConfig)
	require.True(t, ok, "got %T", c.Config)
	assert.Equal(t, 24, cfg.RetentionHours)
	assert.Equal(t, 24*time.Hour, cfg.Retention())
	d, err := cfg.Janitor()
	require.NoError(t, err)
	assert.Equal(t, time.Minute, d)
	assert.Equal(t, 2048, cfg.MaxPayloadBytes)
}

func TestConfigUnmarshalOmittedBlockTakesDefaults(t *testing.T) {
	for _, raw := range []string{
		`{"enabled":true,"type":"memory"}`,
		`{"enabled":true,"type":"memory","config":null}`,
		`{"enabled":true,"type":"memory","config":{}}`,
	} {
		var c Config
		require.NoError(t, json.Unmarshal([]byte(raw), &c), raw)
		cfg, ok := c.Config.(*MemoryQueueConfig)
		require.True(t, ok, "%s: got %T", raw, c.Config)
		assert.Equal(t, EngineConfig{
			RetentionHours:      DefaultRetentionHours,
			AckedRetentionHours: DefaultAckedRetentionHours,
			JanitorInterval:     DefaultJanitorInterval,
			MaxPayloadBytes:     DefaultMaxPayloadBytes,
		}, cfg.WithDefaults(), raw)
	}
}

func TestConfigUnmarshalUnknownTypeKeepsRawConfig(t *testing.T) {
	var c Config
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"type":"kafka","config":{"brokers":["b1:9092"]}}`), &c))
	assert.Equal(t, QueueType("kafka"), c.Type)
	raw, ok := c.Config.(json.RawMessage)
	require.True(t, ok, "got %T", c.Config)
	assert.JSONEq(t, `{"brokers":["b1:9092"]}`, string(raw))
}

func TestConfigUnmarshalDisabledDropsConfig(t *testing.T) {
	var c Config
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":false,"type":"logstore","config":{"retention_hours":"not even valid"}}`), &c))
	assert.False(t, c.Enabled)
	assert.Nil(t, c.Config)
}

func TestConfigUnmarshalErrors(t *testing.T) {
	for _, raw := range []string{
		`{"enabled":true}`,
		`{"enabled":true,"type":"logstore","config":{"retention_hours":"x"}}`,
		`not json`,
	} {
		var c Config
		assert.Error(t, json.Unmarshal([]byte(raw), &c), raw)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	in := Config{Enabled: true, Type: QueueTypeLogStore, Config: &LogStoreQueueConfig{EngineConfig: EngineConfig{RetentionHours: 12}}}
	data, err := json.Marshal(in)
	require.NoError(t, err)
	assert.JSONEq(t, `{"enabled":true,"type":"logstore","config":{"retention_hours":12}}`, string(data))

	var out Config
	require.NoError(t, json.Unmarshal(data, &out))
	assert.Equal(t, in, out)
}

func TestEngineConfigPurgePolicy(t *testing.T) {
	assert.Equal(t, PurgePolicy{DeadRetention: 168 * time.Hour, AckedRetention: 24 * time.Hour}, EngineConfig{}.PurgePolicy(),
		"acknowledged messages are kept a day, dead deliveries a week")

	var c Config
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"type":"logstore","config":{"retention_hours":48,"acked_retention_hours":2}}`), &c))
	assert.Equal(t, PurgePolicy{DeadRetention: 48 * time.Hour, AckedRetention: 2 * time.Hour}, c.Config.(*LogStoreQueueConfig).PurgePolicy())
}

func TestEngineConfigJanitorValidation(t *testing.T) {
	_, err := EngineConfig{JanitorInterval: "soon"}.Janitor()
	assert.Error(t, err)
	_, err = EngineConfig{JanitorInterval: "-1s"}.Janitor()
	assert.Error(t, err)
}
