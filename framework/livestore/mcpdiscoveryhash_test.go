package livestore

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/stretchr/testify/require"
)

// TestMCPDiscoveryHashReadsTheStoredDigest checks a per-user discovery's key in its pool: the first
// sixteen bytes of the SHA-256 hex digest its rows carry, and, for a value that is not one, the
// first sixteen bytes of the value's own SHA-256, so every interning key is SHA-256 based.
func TestMCPDiscoveryHashReadsTheStoredDigest(t *testing.T) {
	digest := strings.Repeat("0123456789abcdef", 4)
	var want Hash
	_, err := hex.Decode(want[:], []byte(digest[:32]))
	require.NoError(t, err)
	require.Equal(t, want, mcpDiscoveryHash(&registry.MCPUserDiscoveryContent{Hash: digest}))

	sum := sha256.Sum256([]byte("not a digest"))
	copy(want[:], sum[:])
	require.Equal(t, want, mcpDiscoveryHash(&registry.MCPUserDiscoveryContent{Hash: "not a digest"}))
}
