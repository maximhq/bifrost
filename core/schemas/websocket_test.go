package schemas

import (
	"testing"
)

func TestWebSocketConfig_CheckAndSetDefaults(t *testing.T) {
	config := &WebSocketConfig{}
	config.CheckAndSetDefaults()

	if config.MaxConnections != DefaultWSMaxConnections {
		t.Errorf("expected MaxConnections=%d, got %d", DefaultWSMaxConnections, config.MaxConnections)
	}
	if config.TranscriptBufferSize != DefaultWSTranscriptBufferSize {
		t.Errorf("expected TranscriptBufferSize=%d, got %d", DefaultWSTranscriptBufferSize, config.TranscriptBufferSize)
	}
	if config.Pool == nil {
		t.Fatal("expected Pool to be initialized")
	}
	if config.Pool.MaxIdlePerKey != DefaultWSMaxIdlePerKey {
		t.Errorf("expected Pool.MaxIdlePerKey=%d, got %d", DefaultWSMaxIdlePerKey, config.Pool.MaxIdlePerKey)
	}
	if config.Pool.MaxTotalConnections != DefaultWSMaxTotalConnections {
		t.Errorf("expected Pool.MaxTotalConnections=%d, got %d", DefaultWSMaxTotalConnections, config.Pool.MaxTotalConnections)
	}
	if config.Pool.IdleTimeoutSeconds != DefaultWSIdleTimeoutSeconds {
		t.Errorf("expected Pool.IdleTimeoutSeconds=%d, got %d", DefaultWSIdleTimeoutSeconds, config.Pool.IdleTimeoutSeconds)
	}
	if config.Pool.MaxConnectionLifetimeSeconds != DefaultWSMaxConnectionLifetimeSeconds {
		t.Errorf("expected Pool.MaxConnectionLifetimeSeconds=%d, got %d", DefaultWSMaxConnectionLifetimeSeconds, config.Pool.MaxConnectionLifetimeSeconds)
	}
}

func TestWebSocketConfig_PreservesExistingValues(t *testing.T) {
	config := &WebSocketConfig{
		MaxConnections: 20,
		TranscriptBufferSize:  100,
		Pool: &WSPoolConfig{
			MaxIdlePerKey:                5,
			MaxTotalConnections:          50,
			IdleTimeoutSeconds:           60,
			MaxConnectionLifetimeSeconds: 1800,
		},
	}
	config.CheckAndSetDefaults()

	if config.MaxConnections != 20 {
		t.Errorf("expected MaxConnections=20, got %d", config.MaxConnections)
	}
	if config.TranscriptBufferSize != 100 {
		t.Errorf("expected TranscriptBufferSize=100, got %d", config.TranscriptBufferSize)
	}
	if config.Pool.MaxIdlePerKey != 5 {
		t.Errorf("expected Pool.MaxIdlePerKey=5, got %d", config.Pool.MaxIdlePerKey)
	}
	if config.Pool.MaxTotalConnections != 50 {
		t.Errorf("expected Pool.MaxTotalConnections=50, got %d", config.Pool.MaxTotalConnections)
	}
}
