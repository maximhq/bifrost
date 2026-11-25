package registry

import (
	"context"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/require"
)

// pastSQLiteParamLimit is more IDs than one SQLite statement may bind (32,766), so a read that
// binds each ID as its own parameter fails on it.
const pastSQLiteParamLimit = 40_000

// withMissingIDs returns ids followed by enough IDs with no row to pass SQLite's parameter limit.
func withMissingIDs(ids ...string) []string {
	out := make([]string, 0, len(ids)+pastSQLiteParamLimit)
	out = append(out, ids...)
	for i := range pastSQLiteParamLimit {
		out = append(out, fmt.Sprintf("missing-%05d", i))
	}
	return out
}

// TestProvidersByNameReadsMoreNamesThanTheParameterLimit checks that a refresh of more provider
// names than one statement may bind still reads the ones that exist.
func TestProvidersByNameReadsMoreNamesThanTheParameterLimit(t *testing.T) {
	store := newProviderDB(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {Keys: []schemas.Key{key("o1")}},
	})
	rows, err := ProvidersByName(context.Background(), store.DB(), withMissingIDs(string(schemas.OpenAI)))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, string(schemas.OpenAI), ProviderName(rows[0]))
}

// TestProviderKeysByIDReadsMoreIDsThanTheParameterLimit checks that a refresh of more key IDs, or
// of more providers' keys, than one statement may bind still reads the ones that exist.
func TestProviderKeysByIDReadsMoreIDsThanTheParameterLimit(t *testing.T) {
	ctx := context.Background()
	store := newProviderDB(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {Keys: []schemas.Key{key("o1")}},
	})
	recs, err := ProviderKeysByID(ctx, store.DB(), withMissingIDs("o1"))
	require.NoError(t, err)
	require.Equal(t, []string{"o1"}, keyIDs(recs))
	recs, err = ProviderKeysByProviders(ctx, store.DB(), withMissingIDs(string(schemas.OpenAI)))
	require.NoError(t, err)
	require.Equal(t, []string{"o1"}, keyIDs(recs))
}
