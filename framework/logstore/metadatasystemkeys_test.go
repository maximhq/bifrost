package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestGetDistinctMetadataKeysHidesLoadBalancerKeys checks that no key under
// schemas.LoadBalancerMetadataPrefix reaches the metadata filter or the metadata columns, whatever
// the key: they are the router's own record of an attempt, not caller metadata. Ordinary caller
// keys stay.
func TestGetDistinctMetadataKeysHidesLoadBalancerKeys(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close(context.Background())
	prefix := schemas.LoadBalancerMetadataPrefix
	now := time.Now().UTC()
	insertLogWithMetadata(t, store, "lb-keys",
		`{"tenant":"acme","`+prefix+`decision":"pinned_rerouted","`+prefix+`route_state":"healthy","`+prefix+`v":"1","`+prefix+`excluded_providers":"groq:failed","`+prefix+`excluded_keys":"k1:rate_limit"}`,
		now.Add(-time.Second))

	keys, err := store.GetDistinctMetadataKeys(context.Background(), 100, "")
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"tenant": {"acme"}}, keys)
}
