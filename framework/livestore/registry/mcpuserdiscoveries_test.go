package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestMCPUserDiscoveriesRereadARowThatChangedBetweenReads checks a binding whose row moves to
// another discovery after the bindings are read and before their discoveries are: the row is read
// again whole, so the binding holds the discovery its row holds now instead of none.
func TestMCPUserDiscoveriesRereadARowThatChangedBetweenReads(t *testing.T) {
	ctx := context.Background()
	store := newProviderDB(t, nil)
	require.NoError(t, store.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID: "client-wiki", Name: "wiki", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("http://upstream.invalid/mcp"), PerUserDiscovery: schemas.Ptr(true),
	}))
	identity := "alice"
	upsert := func(hash string, tools ...string) {
		row := &tables.TableMCPPerUserDiscovery{
			MCPClientID: "client-wiki", AuthMode: string(schemas.MCPAuthModeUser), UserID: &identity,
			Tools: map[string]schemas.ChatTool{}, DiscoveryHash: hash, DiscoveredAt: time.Unix(1_000_000, 0).UTC(),
		}
		for _, name := range tools {
			row.Tools[name] = schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: name}}
		}
		require.NoError(t, store.UpsertMCPPerUserDiscovery(ctx, row))
	}
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64) // hex digests, as the store writes them
	upsert(first, "search")

	db := store.DB()
	var moved atomic.Bool
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:move_after_the_binding_read", func(tx *gorm.DB) {
		if tx.Statement.Table == (tables.TableMCPPerUserDiscovery{}).TableName() && moved.CompareAndSwap(false, true) {
			upsert(second, "search", "create_page")
		}
	}))
	recs, err := MCPUserDiscoveriesByID(ctx, db, []string{MCPUserDiscoveryKey(string(schemas.MCPAuthModeUser), identity, "client-wiki")})
	require.NoError(t, err)
	require.True(t, moved.Load(), "the row moved between the reads")
	require.Len(t, recs, 1)
	require.NotNil(t, recs[0].Discovery, "the binding holds the discovery its row holds now, not none")
	require.Equal(t, second, recs[0].Discovery.Hash)
	require.Contains(t, recs[0].Discovery.Tools, "create_page")
}

// TestMCPUserDiscoveriesDropARowGoneAtTheReread checks a binding whose row is deleted between the
// reads, while another caller's row takes its old discovery: the re-read finds the first row gone,
// so it is left out, and the other binding holds the discovery its row holds now.
func TestMCPUserDiscoveriesDropARowGoneAtTheReread(t *testing.T) {
	ctx := context.Background()
	store := newProviderDB(t, nil)
	require.NoError(t, store.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID: "client-wiki", Name: "wiki", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("http://upstream.invalid/mcp"), PerUserDiscovery: schemas.Ptr(true),
	}))
	upsert := func(identity, hash string, tools ...string) {
		row := &tables.TableMCPPerUserDiscovery{
			MCPClientID: "client-wiki", AuthMode: string(schemas.MCPAuthModeUser), UserID: &identity,
			Tools: map[string]schemas.ChatTool{}, DiscoveryHash: hash, DiscoveredAt: time.Unix(1_000_000, 0).UTC(),
		}
		for _, name := range tools {
			row.Tools[name] = schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: name}}
		}
		require.NoError(t, store.UpsertMCPPerUserDiscovery(ctx, row))
	}
	first, second, third := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	upsert("alice", first, "search")
	upsert("bob", second, "search", "create_page")

	db := store.DB()
	var inHook, moved, swapped atomic.Bool // the hook's own writes read the table too; they are not the loader's reads
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:move_between_the_reads", func(tx *gorm.DB) {
		// A dry run only builds a subquery's SQL; it reads nothing.
		if tx.DryRun || tx.Statement.Table != (tables.TableMCPPerUserDiscovery{}).TableName() || !inHook.CompareAndSwap(false, true) {
			return
		}
		defer inHook.Store(false)
		switch {
		case moved.CompareAndSwap(false, true): // after the binding read: alice's row goes, bob's moves to a third discovery
			require.NoError(t, store.DeleteMCPPerUserDiscovery(ctx, schemas.MCPAuthModeUser, "alice", "client-wiki"))
			upsert("bob", third, "search", "create_page", "archive")
		case strings.Contains(tx.Statement.SQL.String(), "MIN(") && swapped.CompareAndSwap(false, true):
			// after the discovery read, which found neither old discovery: bob's row takes alice's old one
			upsert("bob", first, "search")
		}
	}))
	user := string(schemas.MCPAuthModeUser)
	recs, err := MCPUserDiscoveriesByID(ctx, db, []string{
		MCPUserDiscoveryKey(user, "alice", "client-wiki"),
		MCPUserDiscoveryKey(user, "bob", "client-wiki"),
	})
	require.NoError(t, err)
	require.True(t, swapped.Load(), "bob's row took alice's old discovery after the discovery read")
	require.Len(t, recs, 1, "alice's row is gone at the re-read, so her binding is left out")
	require.Equal(t, MCPUserDiscoveryKey(user, "bob", "client-wiki"), recs[0].ID)
	require.NotNil(t, recs[0].Discovery)
	require.Equal(t, first, recs[0].Discovery.Hash)
}

// TestMCPUserDiscoveryContentPoolKeyReadsTheStoredDigest checks a discovery's key in the shared pool:
// the first sixteen bytes of the SHA-256 hex digest its rows carry, and, for a value that is not
// one, the first sixteen bytes of the value's own SHA-256, so every pool key is SHA-256 based.
func TestMCPUserDiscoveryContentPoolKeyReadsTheStoredDigest(t *testing.T) {
	digest := strings.Repeat("0123456789abcdef", 4)
	var want [16]byte
	_, err := hex.Decode(want[:], []byte(digest[:32]))
	require.NoError(t, err)
	require.Equal(t, want, (&MCPUserDiscoveryContent{Hash: digest}).PoolKey())

	sum := sha256.Sum256([]byte("not a digest"))
	copy(want[:], sum[:])
	require.Equal(t, want, (&MCPUserDiscoveryContent{Hash: "not a digest"}).PoolKey())
}
