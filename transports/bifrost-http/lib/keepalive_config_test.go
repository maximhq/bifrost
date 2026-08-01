package lib

import (
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/assert"
)

// TestGetStreamKeepAliveInterval_OptIn verifies the SSE keepalive is opt-in:
// an absent client config or a zero interval disables it, and a positive
// interval is returned verbatim.
func TestGetStreamKeepAliveInterval_OptIn(t *testing.T) {
	assert.Equal(t, 0, (&Config{}).GetStreamKeepAliveInterval(),
		"nil client config disables the keepalive")
	assert.Equal(t, 0, (&Config{ClientConfig: &configstore.ClientConfig{}}).GetStreamKeepAliveInterval(),
		"unset interval (0) disables the keepalive")
	assert.Equal(t, 20, (&Config{ClientConfig: &configstore.ClientConfig{StreamKeepAliveInterval: 20}}).GetStreamKeepAliveInterval(),
		"positive interval is returned verbatim")
}

// TestGetStreamKeepAliveInterval_ClampsOverflow verifies a value above the
// int64-nanosecond ceiling is clamped rather than left to overflow silently
// when the streaming handler multiplies it by time.Second.
func TestGetStreamKeepAliveInterval_ClampsOverflow(t *testing.T) {
	cfg := &Config{ClientConfig: &configstore.ClientConfig{StreamKeepAliveInterval: streamKeepAliveIntervalUpperBoundSeconds + 1}}
	assert.Equal(t, streamKeepAliveIntervalUpperBoundSeconds, cfg.GetStreamKeepAliveInterval())
}
