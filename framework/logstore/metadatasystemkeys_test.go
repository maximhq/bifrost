package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestGetDistinctMetadataKeysHidesLoadBalancerSystemKeys checks that the load balancer's
// schema-version and exclusion-list keys stay out of the filter sidebar while its enum-valued
// decision keys, and ordinary caller keys, remain visible.
func TestGetDistinctMetadataKeysHidesLoadBalancerSystemKeys(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close(context.Background())
	prefix := schemas.LoadBalancerMetadataPrefix
	now := time.Now().UTC()
	insertLogWithMetadata(t, store, "lb-keys",
		`{"tenant":"acme","`+prefix+`decision":"pinned_rerouted","`+prefix+`v":"1","`+prefix+`excluded_providers":"groq:failed","`+prefix+`excluded_keys":"k1:rate_limit"}`,
		now.Add(-time.Second))

	keys, err := store.GetDistinctMetadataKeys(context.Background(), 100, "")
	require.NoError(t, err)
	require.Contains(t, keys, "tenant")
	require.Contains(t, keys, prefix+"decision")
	require.Equal(t, []string{"pinned_rerouted"}, keys[prefix+"decision"])
	for _, hidden := range []string{prefix + "v", prefix + "excluded_providers", prefix + "excluded_keys"} {
		require.NotContains(t, keys, hidden)
	}
}
