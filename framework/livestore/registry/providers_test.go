package registry

import (
	"context"
	"path/filepath"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/require"
)

// newProviderDB returns a new SQLite config store holding one provider, with default settings, per
// entry of keys, and that entry's keys.
func newProviderDB(t *testing.T, keys map[schemas.ModelProvider][]schemas.Key) configstore.ConfigStore {
	t.Helper()
	ctx := context.Background()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close(ctx) })
	providers := make(map[schemas.ModelProvider]configstore.ProviderConfig, len(keys))
	for provider := range keys {
		providers[provider] = configstore.ProviderConfig{}
	}
	require.NoError(t, store.UpdateProvidersConfig(ctx, providers, keys))
	return store
}

// key is a stored key with an ID, a name and a value.
func key(id string) schemas.Key {
	return schemas.Key{ID: id, Name: id, Value: *schemas.NewSecretVar("sk-" + id), Models: schemas.WhiteList{"*"}, Weight: 1}
}

// TestProvidersPageWalksEveryProviderInNameOrder checks that pages follow the name cursor to the
// end, each page holding at most the rows asked for.
func TestProvidersPageWalksEveryProviderInNameOrder(t *testing.T) {
	ctx := context.Background()
	store := newProviderDB(t, map[schemas.ModelProvider][]schemas.Key{
		schemas.Anthropic: {key("a1")},
		schemas.Gemini:    {key("g1")},
		schemas.OpenAI:    {key("o1")},
	})

	var names []schemas.ModelProvider
	var sizes []int
	after := ""
	for {
		rows, next, err := ProvidersPage(ctx, store.DB(), after, 2)
		require.NoError(t, err)
		sizes = append(sizes, len(rows))
		for _, p := range rows {
			names = append(names, p.Name)
		}
		if next == "" {
			break
		}
		after = next
	}
	require.Equal(t, []int{2, 1}, sizes, "each page holds at most the limit")
	require.Equal(t, []schemas.ModelProvider{schemas.Anthropic, schemas.Gemini, schemas.OpenAI}, names)
}

// TestProvidersByNameLeavesOutMissingRows checks that a name with no row is simply absent, which is
// how a refresh learns that a provider was deleted.
func TestProvidersByNameLeavesOutMissingRows(t *testing.T) {
	store := newProviderDB(t, map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {key("o1")},
	})
	rows, err := ProvidersByName(context.Background(), store.DB(), []string{string(schemas.OpenAI), string(schemas.Anthropic)})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, string(schemas.OpenAI), ProviderName(rows[0]))
}

// keyIDs lists the key IDs of recs in the order given.
func keyIDs(recs []*ProviderKey) []string {
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		ids = append(ids, ProviderKeyID(r))
	}
	return ids
}

// TestProviderKeysByProvidersReadsKeysInCreationOrder checks that a provider's refresh reads its keys
// in the order they were created, not their IDs' order, with the row ID as Order and the provider
// named as the parent, and leaves other providers' keys out.
func TestProviderKeysByProvidersReadsKeysInCreationOrder(t *testing.T) {
	ctx := context.Background()
	store := newProviderDB(t, map[schemas.ModelProvider][]schemas.Key{
		schemas.Anthropic: {key("a1")},
		schemas.OpenAI:    {key("o1")},
	})
	require.NoError(t, store.CreateProviderKey(ctx, schemas.OpenAI, key("o0")))

	recs, err := ProviderKeysByProviders(ctx, store.DB(), []string{string(schemas.OpenAI)})
	require.NoError(t, err)
	require.Equal(t, []string{"o1", "o0"}, keyIDs(recs), "keys keep their creation order, not their IDs' order")
	for _, r := range recs {
		require.Equal(t, string(schemas.OpenAI), ProviderKeyProvider(r))
		require.NotZero(t, r.RowID)
		require.Equal(t, int64(r.RowID), r.Order)
	}
	require.Less(t, recs[0].Order, recs[1].Order)
	require.Equal(t, "sk-o0", recs[1].Key.Value.GetValue())
}

// TestProviderKeysByIDAndPage checks that a refresh by key ID leaves out a key with no row, and that
// pages follow the key_id cursor to the end, each holding at most the rows asked for.
func TestProviderKeysByIDAndPage(t *testing.T) {
	ctx := context.Background()
	store := newProviderDB(t, map[schemas.ModelProvider][]schemas.Key{
		schemas.Anthropic: {key("a1")},
		schemas.OpenAI:    {key("o1"), key("o2")},
	})
	recs, err := ProviderKeysByID(ctx, store.DB(), []string{"o2", "gone"})
	require.NoError(t, err)
	require.Equal(t, []string{"o2"}, keyIDs(recs))
	require.Equal(t, string(schemas.OpenAI), ProviderKeyProvider(recs[0]))

	var walked []string
	var sizes []int
	after := ""
	for {
		page, next, err := ProviderKeysPage(ctx, store.DB(), after, 2)
		require.NoError(t, err)
		sizes = append(sizes, len(page))
		walked = append(walked, keyIDs(page)...)
		if next == "" {
			break
		}
		after = next
	}
	require.Equal(t, []int{2, 1}, sizes, "each page holds at most the limit")
	require.Equal(t, []string{"a1", "o1", "o2"}, walked)
}
