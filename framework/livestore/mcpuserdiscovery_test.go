package livestore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/stretchr/testify/require"
)

// TestMCPUserDiscoveriesDropADiscoveryNoCallerHolds checks the per-user discovery collection end
// to end over a database: callers holding one discovery share one copy, and once every caller has
// moved off it or had theirs deleted, the copy has no use left and the sweep drops it a grace period
// later, while the discovery they moved to stays. This is what keeps memory to the discoveries
// callers hold now rather than every one they ever had.
func TestMCPUserDiscoveriesDropADiscoveryNoCallerHolds(t *testing.T) {
	ctx := context.Background()
	store := governanceDB(t)
	clock := setClock(t, time.Unix(1_000_000, 0))
	client := &schemas.MCPClientConfig{
		ID: "client-wiki", Name: "wiki", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("http://upstream.invalid/mcp"), PerUserDiscovery: schemas.Ptr(true),
	}
	require.NoError(t, store.CreateMCPClientConfig(ctx, client))
	user, vk := schemas.MCPAuthModeUser, schemas.MCPAuthModeVK
	// write stores a caller's discovery with the given tools under a synthetic content hash.
	write := func(mode schemas.MCPAuthMode, identity, hash string, tools ...string) {
		t.Helper()
		row := &tables.TableMCPPerUserDiscovery{
			MCPClientID: client.ID, AuthMode: string(mode), Tools: map[string]schemas.ChatTool{},
			DiscoveryHash: hash, DiscoveredAt: clock.UTC(),
		}
		for _, name := range tools {
			row.Tools[name] = schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: name}}
		}
		if mode == user {
			row.UserID = &identity
		} else {
			row.VirtualKeyID = &identity
		}
		require.NoError(t, store.UpsertMCPPerUserDiscovery(ctx, row))
	}
	key := func(mode schemas.MCPAuthMode, identity string) string {
		return registry.MCPUserDiscoveryKey(string(mode), identity, client.ID)
	}
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64) // hex digests, as the store writes them
	write(user, "alice", first, "search")
	write(user, "bob", first, "search")
	write(vk, "vk-1", first, "search")

	s := loadedGovernance(t, store.DB())
	cols := s.Collections
	held := func(mode schemas.MCPAuthMode, identity string) *registry.MCPUserDiscovery {
		t.Helper()
		b, ok := cols.MCPUserDiscoveries.Get(key(mode, identity))
		require.True(t, ok, "%s %s is held", mode, identity)
		require.NotNil(t, b.Discovery, "%s %s holds its discovery", mode, identity)
		return b
	}
	shared := held(user, "alice").Discovery
	require.Same(t, shared, held(user, "bob").Discovery, "callers holding one discovery share one copy")
	require.Same(t, shared, held(vk, "vk-1").Discovery)
	firstHash := mcpDiscoveryHash(shared)
	uses, _ := usesOf(cols.mcpDiscoveries, firstHash)
	require.Equal(t, 3, uses)

	// alice and bob move to another discovery; vk-1's is deleted.
	write(user, "alice", second, "search", "create_page")
	write(user, "bob", second, "search", "create_page")
	require.NoError(t, store.DeleteMCPPerUserDiscovery(ctx, vk, "vk-1", client.ID))
	require.NoError(t, s.Refresh(ctx, []Ref{
		{Kind: KindMCPUserDiscovery, ID: key(user, "alice")},
		{Kind: KindMCPUserDiscovery, ID: key(user, "bob")},
		{Kind: KindMCPUserDiscovery, ID: key(vk, "vk-1"), Remove: true},
	}))
	uses, kept := usesOf(cols.mcpDiscoveries, firstHash)
	require.Equal(t, 0, uses, "no caller holds the first discovery")
	require.True(t, kept, "it is kept for the grace period")

	*clock = clock.Add(internedGrace + time.Second)
	s.sweep()
	_, kept = sharedCopy(cols.mcpDiscoveries, firstHash)
	require.False(t, kept, "a discovery no caller holds is dropped once the grace period has passed")
	now := held(user, "alice").Discovery
	require.Equal(t, second, now.Hash)
	require.Same(t, now, held(user, "bob").Discovery, "the callers share the discovery they moved to")
	uses, kept = usesOf(cols.mcpDiscoveries, mcpDiscoveryHash(now))
	require.True(t, kept)
	require.Equal(t, 2, uses)
}
