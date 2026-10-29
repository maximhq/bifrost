package configstore

import (
	"context"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

// liveListingTestStore reuses the database fixtures and runs the production listing migration.
func liveListingTestStore(t *testing.T, backend string) *RDBConfigStore {
	t.Helper()
	var store *RDBConfigStore
	if backend == "Postgres" {
		store = setupPostgresDeadlockStore(t)
	} else {
		store = setupRDBTestStore(t)
		pool, err := store.DB().DB()
		require.NoError(t, err)
		pool.SetMaxOpenConns(1) // The fixture's :memory: database belongs to one connection.
		require.NoError(t, store.DB().Exec("PRAGMA foreign_keys = ON").Error)
	}
	pool, err := store.DB().DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, migrationAddLiveModelListings(t.Context(), store.DB(), testMigrationLogger))
	return store
}

// liveListingTestKey supplies an enabled wildcard key with stable fixture credentials.
func liveListingTestKey(id string) schemas.Key {
	return schemas.Key{ID: id, Name: id, Value: *schemas.NewSecretVar("credential-" + id),
		Models: schemas.WhiteList{"*"}, Enabled: schemas.Ptr(true)}
}

// putLiveListing seeds through the conditional production write rather than bypassing source checks.
func putLiveListing(t *testing.T, store *RDBConfigStore, source *LiveModelSource, unfiltered bool, data string) {
	t.Helper()
	stored, err := store.UpsertLiveModelListing(t.Context(), *source, unfiltered, data)
	require.NoError(t, err)
	require.True(t, stored)
}

// TestLiveModelListingsPersistence pins mode/key/provider isolation and the migration on both SQL backends.
func TestLiveModelListingsPersistence(t *testing.T) {
	for _, backend := range []string{"SQLite", "Postgres"} {
		t.Run(backend, func(t *testing.T) {
			store := liveListingTestStore(t, backend)
			ctx := t.Context()
			require.NoError(t, migrationAddLiveModelListings(ctx, store.DB(), testMigrationLogger), "migration is idempotent")
			key1, key2, other := liveListingTestKey("one"), liveListingTestKey("two"), liveListingTestKey("other")
			require.NoError(t, store.AddProvider(ctx, schemas.OpenAI, ProviderConfig{Keys: []schemas.Key{key1, key2}}))
			require.NoError(t, store.AddProvider(ctx, schemas.Anthropic, ProviderConfig{Keys: []schemas.Key{other}}))
			one, err := store.GetLiveModelSource(ctx, schemas.OpenAI, key1.ID)
			require.NoError(t, err)
			two, err := store.GetLiveModelSource(ctx, schemas.OpenAI, key2.ID)
			require.NoError(t, err)
			foreign, err := store.GetLiveModelSource(ctx, schemas.Anthropic, other.ID)
			require.NoError(t, err)
			putLiveListing(t, store, one, false, `{"Models":["allowed"],"Created":{"allowed":1700000000}}`)
			putLiveListing(t, store, one, true, `{"Models":["allowed","raw-only"]}`)
			putLiveListing(t, store, two, false, `{"Models":["second-key"]}`)
			putLiveListing(t, store, foreign, false, `{"Models":["other-provider"]}`)
			putLiveListing(t, store, one, false, `{"Models":["replacement"]}`)
			rows, err := store.GetLiveModelListings(ctx, schemas.OpenAI, &key1.ID)
			require.NoError(t, err)
			require.Len(t, rows, 2)
			data := map[bool]string{}
			for _, row := range rows {
				data[row.Unfiltered] = row.Data
			}
			require.JSONEq(t, `{"Models":["replacement"]}`, data[false])
			require.JSONEq(t, `{"Models":["allowed","raw-only"]}`, data[true])
			rows, err = store.GetLiveModelListings(ctx, schemas.OpenAI, nil)
			require.NoError(t, err)
			require.Len(t, rows, 3)
			rows, err = store.GetLiveModelListings(ctx, schemas.Anthropic, nil)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.JSONEq(t, `{"Models":["other-provider"]}`, rows[0].Data)
		})
	}
}

// TestLiveModelSourceEndpointDefaults matches file-loaded keys to SQL defaults without ignoring an enabled preference.
func TestLiveModelSourceEndpointDefaults(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			store := liveListingTestStore(t, "SQLite")
			key := liveListingTestKey("key")
			if enabled {
				key.UseAnthropicEndpoints = schemas.Ptr(true)
				key.UseOpenAIEndpoints = schemas.Ptr(true)
			}
			config := ProviderConfig{Keys: []schemas.Key{key}}
			require.NoError(t, store.AddProvider(t.Context(), schemas.OpenAI, config))
			source, err := store.GetLiveModelSource(t.Context(), schemas.OpenAI, key.ID)
			require.NoError(t, err)
			fileHash, err := LiveModelSourceHash(schemas.OpenAI, &config, key)
			require.NoError(t, err)
			require.Equal(t, fileHash, source.Hash, "SQL's explicit false defaults must match unset file preferences")
			key.UseAnthropicEndpoints = schemas.Ptr(!enabled)
			key.UseOpenAIEndpoints = schemas.Ptr(!enabled)
			changed, err := LiveModelSourceHash(schemas.OpenAI, &config, key)
			require.NoError(t, err)
			require.NotEqual(t, fileHash, changed, "a real endpoint preference change must still invalidate provenance")
		})
	}
}

// TestLiveModelListingSourceChanges rejects obsolete work while preserving status and URL-edit fallback semantics.
func TestLiveModelListingSourceChanges(t *testing.T) {
	cases := []struct {
		name               string
		mutate             func(context.Context, *RDBConfigStore, schemas.Key) error
		canCommit, canLoad bool
	}{
		{"credential", func(ctx context.Context, s *RDBConfigStore, key schemas.Key) error {
			key.Value = *schemas.NewSecretVar("new-credential")
			return s.UpdateProviderKey(ctx, schemas.OpenAI, key.ID, key)
		}, false, false},
		{"allowlist", func(ctx context.Context, s *RDBConfigStore, key schemas.Key) error {
			key.Models = schemas.WhiteList{"different-model"}
			return s.UpdateProviderKey(ctx, schemas.OpenAI, key.ID, key)
		}, false, false},
		{"blacklist", func(ctx context.Context, s *RDBConfigStore, key schemas.Key) error {
			key.BlacklistedModels = schemas.BlackList{"old-model"}
			return s.UpdateProviderKey(ctx, schemas.OpenAI, key.ID, key)
		}, false, false},
		{"aliases", func(ctx context.Context, s *RDBConfigStore, key schemas.Key) error {
			key.Aliases = schemas.KeyAliases{"alias": {ModelID: "new-target"}}
			return s.UpdateProviderKey(ctx, schemas.OpenAI, key.ID, key)
		}, false, false},
		{"disabled", func(ctx context.Context, s *RDBConfigStore, key schemas.Key) error {
			key.Enabled = schemas.Ptr(false)
			return s.UpdateProviderKey(ctx, schemas.OpenAI, key.ID, key)
		}, false, false},
		{"deleted and recreated with same key ID", func(ctx context.Context, s *RDBConfigStore, key schemas.Key) error {
			if err := s.DeleteProviderKey(ctx, schemas.OpenAI, key.ID); err != nil {
				return err
			}
			return s.CreateProviderKey(ctx, schemas.OpenAI, key)
		}, false, false},
		{"provider URL", func(ctx context.Context, s *RDBConfigStore, key schemas.Key) error {
			return s.UpdateProvider(ctx, schemas.OpenAI, ProviderConfig{Keys: []schemas.Key{key},
				NetworkConfig: &schemas.NetworkConfig{BaseURL: "https://new-upstream.example"}})
		}, false, true},
		{"status only", func(ctx context.Context, s *RDBConfigStore, key schemas.Key) error {
			return s.UpdateStatus(ctx, schemas.OpenAI, key.ID, string(schemas.KeyStatusListModelsFailed), "timeout")
		}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := liveListingTestStore(t, "SQLite")
			key := liveListingTestKey("key")
			require.NoError(t, store.AddProvider(t.Context(), schemas.OpenAI, ProviderConfig{Keys: []schemas.Key{key}}))
			source, err := store.GetLiveModelSource(t.Context(), schemas.OpenAI, key.ID)
			require.NoError(t, err)
			putLiveListing(t, store, source, false, `{"Models":["old-model"]}`)
			require.NoError(t, tc.mutate(t.Context(), store, key))
			rows, err := store.GetLiveModelListings(t.Context(), schemas.OpenAI, &key.ID)
			require.NoError(t, err)
			require.Equal(t, tc.canLoad, len(rows) == 1)
			stored, err := store.UpsertLiveModelListing(t.Context(), *source, false, `{"Models":["new-model"]}`)
			require.NoError(t, err)
			require.Equal(t, tc.canCommit, stored)
			require.NoError(t, store.PruneLiveModelListings(t.Context(), schemas.OpenAI))
			var count int64
			require.NoError(t, store.DB().Model(&tables.TableLiveModelListing{}).Count(&count).Error)
			if tc.canLoad {
				require.EqualValues(t, 1, count)
			} else {
				require.Zero(t, count)
			}
		})
	}
}

// TestLiveModelListingsDeletion verifies provider and key cascades on both SQL backends.
func TestLiveModelListingsDeletion(t *testing.T) {
	for _, backend := range []string{"SQLite", "Postgres"} {
		t.Run(backend, func(t *testing.T) {
			store := liveListingTestStore(t, backend)
			one, two := liveListingTestKey("one"), liveListingTestKey("two")
			require.NoError(t, store.AddProvider(t.Context(), schemas.OpenAI, ProviderConfig{Keys: []schemas.Key{one, two}}))
			for _, key := range []schemas.Key{one, two} {
				source, err := store.GetLiveModelSource(t.Context(), schemas.OpenAI, key.ID)
				require.NoError(t, err)
				putLiveListing(t, store, source, false, `{"Models":["model"]}`)
				putLiveListing(t, store, source, true, `{"Models":["model"]}`)
			}
			require.NoError(t, store.DeleteProviderKey(t.Context(), schemas.OpenAI, one.ID))
			var count int64
			require.NoError(t, store.DB().Model(&tables.TableLiveModelListing{}).Count(&count).Error)
			require.EqualValues(t, 2, count)
			require.NoError(t, store.DeleteProvider(t.Context(), schemas.OpenAI))
			require.NoError(t, store.DB().Model(&tables.TableLiveModelListing{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

// TestLiveModelListingsPruneKeepsReplacement prevents delayed configuration cleanup from deleting a current snapshot.
func TestLiveModelListingsPruneKeepsReplacement(t *testing.T) {
	for _, backend := range []string{"SQLite", "Postgres"} {
		t.Run(backend, func(t *testing.T) {
			store := liveListingTestStore(t, backend)
			key := liveListingTestKey("key")
			require.NoError(t, store.AddProvider(t.Context(), schemas.OpenAI, ProviderConfig{Keys: []schemas.Key{key}}))
			old, err := store.GetLiveModelSource(t.Context(), schemas.OpenAI, key.ID)
			require.NoError(t, err)
			putLiveListing(t, store, old, false, `{"Models":["old"]}`)
			putLiveListing(t, store, old, true, `{"Models":["old-raw"]}`)
			key.Value = *schemas.NewSecretVar("new-credential")
			require.NoError(t, store.UpdateProviderKey(t.Context(), schemas.OpenAI, key.ID, key))
			current, err := store.GetLiveModelSource(t.Context(), schemas.OpenAI, key.ID)
			require.NoError(t, err)
			putLiveListing(t, store, current, false, `{"Models":["new"]}`)
			stored, err := store.UpsertLiveModelListing(t.Context(), *old, false, `{"Models":["obsolete"]}`)
			require.NoError(t, err)
			require.False(t, stored)
			require.NoError(t, store.PruneLiveModelListings(t.Context(), schemas.OpenAI))
			rows, err := store.GetLiveModelListings(t.Context(), schemas.OpenAI, nil)
			require.NoError(t, err)
			require.Len(t, rows, 1, "incompatible raw mode is pruned without deleting the fresh filtered mode")
			require.JSONEq(t, `{"Models":["new"]}`, rows[0].Data)
		})
	}
}

// TestLiveModelListingKeylessScope permits only the configured sentinel, regardless of residual keys.
func TestLiveModelListingKeylessScope(t *testing.T) {
	store := liveListingTestStore(t, "SQLite")
	key := liveListingTestKey("residual")
	key.Enabled = schemas.Ptr(false)
	const provider schemas.ModelProvider = "keyless"
	require.NoError(t, store.AddProvider(t.Context(), provider, ProviderConfig{Keys: []schemas.Key{key},
		CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true}}))
	source, err := store.GetLiveModelSource(t.Context(), provider, "")
	require.NoError(t, err)
	require.Nil(t, source.TableKeyID)
	putLiveListing(t, store, source, false, `{"Models":["keyless-model"]}`)
	_, err = store.GetLiveModelSource(t.Context(), provider, key.ID)
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, store.AddProvider(t.Context(), schemas.OpenAI, ProviderConfig{Keys: []schemas.Key{liveListingTestKey("keyed")}}))
	_, err = store.GetLiveModelSource(t.Context(), schemas.OpenAI, "")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, store.DeleteProvider(t.Context(), provider))
	var count int64
	require.NoError(t, store.DB().Model(&tables.TableLiveModelListing{}).Count(&count).Error)
	require.Zero(t, count, "keyless listings belong to their provider even without a key row")
}
