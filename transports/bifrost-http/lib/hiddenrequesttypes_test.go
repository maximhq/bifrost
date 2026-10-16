package lib

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/require"
)

func TestNormalizeHiddenRequestTypes(t *testing.T) {
	require.Nil(t, NormalizeHiddenRequestTypes(nil))
	require.Nil(t, NormalizeHiddenRequestTypes([]string{"", "  "}))
	require.Equal(t, []string{"count_tokens", "embedding"},
		NormalizeHiddenRequestTypes([]string{" count_tokens ", "embedding", "count_tokens", ""}))
}

func TestHiddenRequestTypesAffectClientConfigHash(t *testing.T) {
	base := configstore.ClientConfig{}
	withHidden := configstore.ClientConfig{HiddenRequestTypes: []string{"embedding", "count_tokens"}}
	reordered := configstore.ClientConfig{HiddenRequestTypes: []string{"count_tokens", "embedding"}}

	baseHash, err := base.GenerateClientConfigHash()
	require.NoError(t, err)
	hiddenHash, err := withHidden.GenerateClientConfigHash()
	require.NoError(t, err)
	reorderedHash, err := reordered.GenerateClientConfigHash()
	require.NoError(t, err)

	require.NotEqual(t, baseHash, hiddenHash, "hidden request types must change the hash so config.json edits sync")
	require.Equal(t, hiddenHash, reorderedHash, "hash must not depend on list order")
}

func TestHiddenRequestTypesPersistInConfigStore(t *testing.T) {
	SetLogger(&testLogger{})
	ctx := context.Background()
	store := createTestSQLiteConfigStore(t, t.TempDir())

	cc := DefaultClientConfig
	cc.HiddenRequestTypes = []string{"count_tokens", "embedding"}
	require.NoError(t, store.UpdateClientConfig(ctx, &cc))

	stored, err := store.GetClientConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"count_tokens", "embedding"}, stored.HiddenRequestTypes)

	// Clearing the list from the UI must clear it in the store too.
	cc.HiddenRequestTypes = nil
	require.NoError(t, store.UpdateClientConfig(ctx, &cc))
	stored, err = store.GetClientConfig(ctx)
	require.NoError(t, err)
	require.Empty(t, stored.HiddenRequestTypes)
}
