package configstore

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGenerateClientConfigHash_StreamKeepAliveIntervalNoChurn verifies that an
// unset stream_keepalive_interval (0) contributes nothing to the config hash, so
// adding the field on upgrade does not invalidate stored hashes and trigger a
// spurious config-file resync. A configured non-zero interval must still change
// the hash so a real config change is detected.
func TestGenerateClientConfigHash_StreamKeepAliveIntervalNoChurn(t *testing.T) {
	base := &ClientConfig{}
	baseHash, err := base.GenerateClientConfigHash()
	require.NoError(t, err)

	unset := &ClientConfig{StreamKeepAliveInterval: 0}
	unsetHash, err := unset.GenerateClientConfigHash()
	require.NoError(t, err)
	require.Equal(t, baseHash, unsetHash, "explicit 0 must hash identically to unset (no upgrade churn)")

	configured := &ClientConfig{StreamKeepAliveInterval: 30}
	configuredHash, err := configured.GenerateClientConfigHash()
	require.NoError(t, err)
	require.NotEqual(t, baseHash, configuredHash, "a configured interval must change the hash")
}
