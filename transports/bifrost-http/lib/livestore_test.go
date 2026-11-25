package lib

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// withProviders loads providers into a new live store on c, the way file-only boot does, and
// returns c. It suits tests and benchmarks that only read providers; a write needs a database
// (providerStore).
func withProviders(t testing.TB, c *Config, providers map[schemas.ModelProvider]configstore.ProviderConfig) *Config {
	t.Helper()
	live, err := livestore.New()
	require.NoError(t, err)
	t.Cleanup(live.Close)
	require.NoError(t, c.AttachLiveStore(live))
	require.NoError(t, live.LoadRecords(context.Background(), (&bootConfig{providers: providers}).records(live)))
	return c
}

// sqliteConfigStore returns a new SQLite config store in a temporary directory, closed when the
// test ends.
func sqliteConfigStore(t *testing.T) configstore.ConfigStore {
	t.Helper()
	ctx := context.Background()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, &testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { store.Close(ctx) })
	return store
}

// providerStore returns a Config over a new SQLite config store holding providers, with its live
// store loaded from it, for tests that write providers.
func providerStore(t *testing.T, providers map[schemas.ModelProvider]configstore.ProviderConfig) *Config {
	t.Helper()
	ctx := context.Background()
	store := sqliteConfigStore(t)
	if len(providers) > 0 {
		require.NoError(t, store.UpdateProvidersConfig(ctx, providers))
	}
	live, err := livestore.New()
	require.NoError(t, err)
	t.Cleanup(live.Close)
	c := &Config{ConfigStore: store}
	require.NoError(t, c.AttachLiveStore(live))
	require.NoError(t, live.LoadAll(ctx, store.DB()))
	return c
}

// providersOf returns the providers c's live store holds, keyed by name, each with its keys.
func providersOf(c *Config) map[schemas.ModelProvider]configstore.ProviderConfig {
	out := make(map[schemas.ModelProvider]configstore.ProviderConfig)
	c.LiveStore.Collections.Providers.Range(func(p *registry.Provider) bool {
		config := p.Settings.Config()
		if keys, ok := c.LiveStore.Views.ProviderKeyLists.Get(string(p.Name)); ok {
			config.Keys = *keys
		}
		out[p.Name] = config
		return true
	})
	return out
}

// storedKey returns a copy of a provider's key from c's live store.
func storedKey(c *Config, provider schemas.ModelProvider, id string) (schemas.Key, bool) {
	rec, ok := c.liveProviderKey(provider, id)
	if !ok {
		return schemas.Key{}, false
	}
	return rec.Key, true
}

// refreshProviderRow re-reads a provider after a write made outside the config.
func refreshProviderRow(ctx context.Context, c *Config, provider schemas.ModelProvider) error {
	return c.LiveStore.Refresh(ctx, []livestore.Ref{{Kind: livestore.KindProvider, ID: string(provider)}}, livestore.RetryMissing(1, time.Millisecond))
}

// bootLive runs LoadConfig over a config.json holding fields, either with an SQLite config store
// in a fresh directory or with the config store disabled, and closes the config when the test
// ends. fields is a JSON object body without braces; empty means no other settings.
func bootLive(t *testing.T, withStore bool, fields string) *Config {
	t.Helper()
	dir := createTempDir(t)
	store := `"config_store": {"enabled": false}`
	if withStore {
		store = fmt.Sprintf(`"config_store": {"enabled": true, "type": "sqlite", "config": {"path": %q}}`, filepath.Join(dir, "config.db"))
	}
	body := `"$schema": "https://www.getbifrost.ai/schema", ` + store
	if fields != "" {
		body += ", " + fields
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte("{"+body+"}"), 0o600))
	ctx := context.Background()
	config, err := LoadConfig(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { config.Close(ctx) })
	return config
}

// bootBothModes boots the same config.json twice, over a config store and with none, so a test
// can check that both modes put the same records in the live store.
func bootBothModes(t *testing.T, fields string) (withStore, fileOnly *Config) {
	t.Helper()
	return bootLive(t, true, fields), bootLive(t, false, fields)
}

// TestLoadConfigFillsLiveStore checks that LoadConfig leaves a loaded live store in both modes:
// reading the config store when there is one, and with no database otherwise.
func TestLoadConfigFillsLiveStore(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	withStore, fileOnly := bootBothModes(t, "")

	require.NotNil(t, withStore.LiveStore)
	require.NotNil(t, withStore.LiveStore.Collections)
	require.NotNil(t, fileOnly.LiveStore)
	// A loaded store with a database reaches the kind check; a store with none stops before it.
	require.ErrorContains(t, withStore.LiveStore.RefreshKind(ctx, livestore.Kind("missing")), "unknown kind")
	require.ErrorIs(t, fileOnly.LiveStore.RefreshKind(ctx, livestore.Kind("missing")), livestore.ErrNoDatabase)
}

// TestSetLiveStoreIsTakenOnce checks that LoadConfig loads the store SetLiveStore handed over, and
// that the next LoadConfig creates its own.
func TestSetLiveStoreIsTakenOnce(t *testing.T) {
	initTestLogger()
	preset, err := livestore.New()
	require.NoError(t, err)
	SetLiveStore(preset)
	t.Cleanup(func() { SetLiveStore(nil) })

	first := bootLive(t, true, "")
	require.Same(t, preset, first.LiveStore)
	second := bootLive(t, true, "")
	require.NotSame(t, preset, second.LiveStore)
}

// TestLoadConfigLoadsProvidersInBothModes checks that the same config.json puts the same providers
// and keys in the live store whether boot reads them back from the config store or loads them
// from the file alone, and that boot drops what it held once the store has loaded.
func TestLoadConfigLoadsProvidersInBothModes(t *testing.T) {
	initTestLogger()
	withStore, fileOnly := bootBothModes(t, `"providers": {"openai": {"keys": [
		{"id": "k1", "name": "first", "value": "sk-1", "models": ["*"], "weight": 1},
		{"id": "k2", "name": "second", "value": "sk-2", "models": ["*"], "weight": 2}
	]}}`)

	for name, config := range map[string]*Config{"config store": withStore, "file only": fileOnly} {
		_, ok := config.liveProvider(schemas.OpenAI)
		require.True(t, ok, name)
		list, ok := config.LiveStore.Views.ProviderKeyLists.Get(string(schemas.OpenAI))
		require.True(t, ok, name)
		keys := *list
		require.Len(t, keys, 2, name)
		require.Equal(t, []string{"k1", "k2"}, []string{keys[0].ID, keys[1].ID}, "%s: keys keep their order", name)
		require.Equal(t, "sk-2", keys[1].Value.GetValue(), name)
		rec, ok := config.liveProviderKey(schemas.OpenAI, "k2")
		require.True(t, ok, "%s: each key is a record of its own", name)
		first, _ := config.liveProviderKey(schemas.OpenAI, "k1")
		require.Less(t, first.Order, rec.Order, name)
		require.Nil(t, config.boot, "%s: boot config is dropped once the live store loads", name)
	}
}

// TestLoadConfigRefusesAKeyIDSharedByTwoProvidersWithoutAConfigStore checks that file-only boot
// fails, naming the ID and both providers, when two providers' keys in config.json share an explicit
// key ID: the live store holds one record per key ID, so loading both would silently drop one key.
func TestLoadConfigRefusesAKeyIDSharedByTwoProvidersWithoutAConfigStore(t *testing.T) {
	initTestLogger()
	dir := createTempDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{
		"$schema": "https://www.getbifrost.ai/schema",
		"config_store": {"enabled": false},
		"providers": {
			"openai": {"keys": [{"id": "shared", "name": "openai-key", "value": "sk-1", "models": ["*"], "weight": 1}]},
			"anthropic": {"keys": [{"id": "shared", "name": "anthropic-key", "value": "sk-2", "models": ["*"], "weight": 1}]}
		}
	}`), 0o600))
	ctx := context.Background()
	config, err := LoadConfig(ctx, dir)
	if err == nil {
		t.Cleanup(func() { config.Close(ctx) })
		keys := map[schemas.ModelProvider]int{}
		for _, provider := range []schemas.ModelProvider{schemas.Anthropic, schemas.OpenAI} {
			if list, ok := config.LiveStore.Views.ProviderKeyLists.Get(string(provider)); ok {
				keys[provider] = len(*list)
			}
		}
		t.Fatalf("LoadConfig booted with keys per provider %v, want an error naming the shared key ID", keys)
	}
	require.ErrorContains(t, err, `key ID "shared" is used by providers anthropic and openai`)
}

// TestAttachLiveStoreRefusesNil checks that AttachLiveStore answers an error, not a panic, when it
// is given no store.
func TestAttachLiveStoreRefusesNil(t *testing.T) {
	require.ErrorIs(t, (&Config{}).AttachLiveStore(nil), livestore.ErrNoLiveStore)
}

// TestProviderRefreshReadsAnOutsideWrite checks the refresh path: a provider and key written to the
// database by someone else reach memory only when a refresh re-reads them, and a provider whose
// row is not readable yet is reported, not removed.
func TestProviderRefreshReadsAnOutsideWrite(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	cfg := providerStore(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {Keys: []schemas.Key{{ID: "k1", Name: "first", Value: *schemas.NewSecretVar("sk-1"), Models: schemas.WhiteList{"*"}, Weight: 1}}},
	})

	// Someone else adds a key: the live store does not see it until the refresh.
	require.NoError(t, cfg.ConfigStore.CreateProviderKey(ctx, schemas.OpenAI, schemas.Key{ID: "k2", Name: "second", Value: *schemas.NewSecretVar("sk-2"), Models: schemas.WhiteList{"*"}, Weight: 1}))
	_, ok := storedKey(cfg, schemas.OpenAI, "k2")
	require.False(t, ok)
	require.NoError(t, refreshProviderRow(ctx, cfg, schemas.OpenAI))
	key, ok := storedKey(cfg, schemas.OpenAI, "k2")
	require.True(t, ok)
	require.Equal(t, "sk-2", key.Value.GetValue())

	// A provider with no row yet is reported after the retries and is not invented in memory.
	var missing *livestore.MissingError
	require.ErrorAs(t, refreshProviderRow(ctx, cfg, schemas.Anthropic), &missing)
	require.False(t, cfg.IsProviderConfigured(schemas.Anthropic))
}

// TestProviderKeyWritesReachMemoryThroughTheDatabase checks that each key write lands in the
// database first and that memory then holds the row as written.
func TestProviderKeyWritesReachMemoryThroughTheDatabase(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	cfg := providerStore(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {ConfigHash: "from-file", Keys: []schemas.Key{{ID: "k1", Name: "first", Value: *schemas.NewSecretVar("sk-1"), Models: schemas.WhiteList{"*"}, Weight: 1}}},
	})
	cfg.client = newNoopProviderClient(t, cfg)

	require.NoError(t, cfg.AddProviderKey(ctx, schemas.OpenAI, schemas.Key{ID: "k2", Name: "second", Value: *schemas.NewSecretVar("sk-2"), Models: schemas.WhiteList{"*"}, Weight: 1}))
	storedKeys, err := cfg.ConfigStore.GetProviderKeys(ctx, schemas.OpenAI)
	require.NoError(t, err)
	var stored schemas.Key
	for _, k := range storedKeys {
		if k.ID == "k2" {
			stored = k
		}
	}
	require.Equal(t, "k2", stored.ID, "the database holds the added key")
	inMemory, ok := storedKey(cfg, schemas.OpenAI, "k2")
	require.True(t, ok)
	require.Equal(t, stored.Value.GetValue(), inMemory.Value.GetValue())

	updated := inMemory
	updated.Weight = 3
	require.NoError(t, cfg.UpdateProviderKey(ctx, schemas.OpenAI, "k2", updated))
	inMemory, ok = storedKey(cfg, schemas.OpenAI, "k2")
	require.True(t, ok)
	require.Equal(t, 3.0, inMemory.Weight)

	require.NoError(t, cfg.RemoveProviderKey(ctx, schemas.OpenAI, "k2"))
	_, ok = storedKey(cfg, schemas.OpenAI, "k2")
	require.False(t, ok)

	// An update keeps the hash config.json gave the provider, so a restart still compares against it.
	next := providersOf(cfg)[schemas.OpenAI]
	next.ConfigHash = "from-request"
	require.NoError(t, cfg.UpdateProviderConfig(ctx, schemas.OpenAI, next))
	require.Equal(t, "from-file", providersOf(cfg)[schemas.OpenAI].ConfigHash)
}

// TestDBLookupRetryRereadsAFailedRead checks that DBLookupRetry reads a row again after a read
// that fails with an error, not only after one that finds no row: applying a change made
// elsewhere has no caller to report a failed read to, so one transient error would leave memory
// behind the database.
func TestDBLookupRetryRereadsAFailedRead(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	live, err := livestore.New()
	require.NoError(t, err)
	t.Cleanup(live.Close)
	type row struct{ ID string }
	var reads atomic.Int32
	rows, err := live.Register(livestore.Kind("lib_test_row"), nil, livestore.Loader[row]{
		ID: func(r *row) string { return r.ID },
		Page: func(context.Context, *gorm.DB, string, int) ([]*row, string, error) {
			return nil, "", nil
		},
		ByID: func(_ context.Context, _ *gorm.DB, ids []string) ([]*row, error) {
			if reads.Add(1) == 1 {
				return nil, errors.New("read failed")
			}
			out := make([]*row, 0, len(ids))
			for _, id := range ids {
				out = append(out, &row{ID: id})
			}
			return out, nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, live.LoadAll(ctx, sqliteConfigStore(t).DB()))

	require.NoError(t, live.Refresh(ctx, []livestore.Ref{{Kind: "lib_test_row", ID: "r1"}}, DBLookupRetry()...))
	_, ok := rows.Get("r1")
	require.True(t, ok, "the row the retry read is published")
	require.Equal(t, int32(2), reads.Load(), "the failed read and one retry")
}

// TestRemoveProviderKeyCatchesUpWhenTheRowIsAlreadyGone checks a retried key delete whose row is
// already gone, as after a delete whose refresh failed: it still answers not found, and memory and
// the keys requests are served from drop the key, so the retry converges.
func TestRemoveProviderKeyCatchesUpWhenTheRowIsAlreadyGone(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	cfg := providerStore(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {Keys: []schemas.Key{
			{ID: "k1", Name: "first", Value: *schemas.NewSecretVar("sk-1"), Models: schemas.WhiteList{"*"}, Weight: 1},
			{ID: "k2", Name: "second", Value: *schemas.NewSecretVar("sk-2"), Models: schemas.WhiteList{"*"}, Weight: 1},
		}},
	})
	cfg.client = newNoopProviderClient(t, cfg)

	// The row is deleted but memory was never refreshed, so it still holds the key.
	require.NoError(t, cfg.ConfigStore.DeleteProviderKey(ctx, schemas.OpenAI, "k2"))
	_, ok := storedKey(cfg, schemas.OpenAI, "k2")
	require.True(t, ok)

	require.ErrorIs(t, cfg.RemoveProviderKey(ctx, schemas.OpenAI, "k2"), ErrNotFound)
	_, ok = storedKey(cfg, schemas.OpenAI, "k2")
	require.False(t, ok, "the live record still holds the deleted key")
	keys, err := NewBaseAccount(cfg).GetKeysForProvider(ctx, schemas.OpenAI)
	require.NoError(t, err)
	var ids []string
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	require.Equal(t, []string{"k1"}, ids, "requests are still served the deleted key")

	// With the provider's row gone too the answer stays not found and the key is dropped. A key
	// delete never reads its provider, so the provider record stays until its own delete arrives.
	require.NoError(t, cfg.ConfigStore.DeleteProvider(ctx, schemas.OpenAI))
	require.ErrorIs(t, cfg.RemoveProviderKey(ctx, schemas.OpenAI, "k1"), ErrNotFound)
	_, ok = storedKey(cfg, schemas.OpenAI, "k1")
	require.False(t, ok)
}

// failingReads makes every read of table on db fail while it is on, as an unreachable database
// does, and counts the reads it failed.
type failingReads struct {
	on     atomic.Bool
	left   atomic.Int32 // when above zero, only this many more reads fail
	failed atomic.Int32
}

// install registers the failure on db's query callbacks for reads of table.
func (f *failingReads) install(t *testing.T, db *gorm.DB, table string) {
	t.Helper()
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:failing_reads_"+table, func(tx *gorm.DB) {
		if tx.Statement.Table != table || !f.on.Load() {
			return
		}
		if f.left.Load() > 0 && f.left.Add(-1) == 0 {
			f.on.Store(false)
		}
		f.failed.Add(1)
		_ = tx.AddError(errors.New("database unreachable"))
	}))
}

// writeThenFailReads is a config store whose key update or delete commits and then leaves the
// database unreadable, as a database lost right after the write does.
type writeThenFailReads struct {
	configstore.ConfigStore
	reads *failingReads
}

// UpdateProviderKey writes the key, then makes reads fail.
func (s writeThenFailReads) UpdateProviderKey(ctx context.Context, provider schemas.ModelProvider, keyID string, key schemas.Key, tx ...*gorm.DB) error {
	err := s.ConfigStore.UpdateProviderKey(ctx, provider, keyID, key, tx...)
	s.reads.on.Store(true)
	return err
}

// DeleteProviderKey deletes the key, then makes reads fail.
func (s writeThenFailReads) DeleteProviderKey(ctx context.Context, provider schemas.ModelProvider, keyID string, tx ...*gorm.DB) error {
	err := s.ConfigStore.DeleteProviderKey(ctx, provider, keyID, tx...)
	s.reads.on.Store(true)
	return err
}

// TestRemoveProviderKeyDropsTheKeyWithoutReadingTheDatabase checks that a key delete drops the key
// from memory without a read: when the database becomes unreadable right after the delete commits,
// the deleted key still stops serving requests.
func TestRemoveProviderKeyDropsTheKeyWithoutReadingTheDatabase(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	cfg := providerStore(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {Keys: []schemas.Key{
			{ID: "k1", Name: "first", Value: *schemas.NewSecretVar("sk-1"), Models: schemas.WhiteList{"*"}, Weight: 1},
			{ID: "k2", Name: "second", Value: *schemas.NewSecretVar("sk-2"), Models: schemas.WhiteList{"*"}, Weight: 1},
		}},
	})
	cfg.client = newNoopProviderClient(t, cfg)
	reads := &failingReads{}
	for _, table := range []string{"config_keys", "config_providers"} {
		reads.install(t, cfg.ConfigStore.DB(), table)
	}
	cfg.ConfigStore = writeThenFailReads{ConfigStore: cfg.ConfigStore, reads: reads}

	require.NoError(t, cfg.RemoveProviderKey(ctx, schemas.OpenAI, "k2"))
	require.Zero(t, reads.failed.Load(), "the removal read nothing")
	keys, err := NewBaseAccount(cfg).GetKeysForProvider(ctx, schemas.OpenAI)
	require.NoError(t, err)
	require.Equal(t, []string{"k1"}, []string{keys[0].ID}, "the deleted key is no longer served")
	require.Len(t, keys, 1)
}

// TestProviderKeyWritesRetryAFailedReread checks that a key write re-reads its row with AdminRetry:
// a read that fails once is retried and the write succeeds, and when the context's deadline leaves
// no time for a retry the write fails and memory keeps the previous record.
func TestProviderKeyWritesRetryAFailedReread(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	cfg := providerStore(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {Keys: []schemas.Key{{ID: "k1", Name: "first", Value: *schemas.NewSecretVar("sk-1"), Models: schemas.WhiteList{"*"}, Weight: 1}}},
	})
	cfg.client = newNoopProviderClient(t, cfg)
	reads := &failingReads{}
	reads.install(t, cfg.ConfigStore.DB(), "config_keys")

	// One failed read: the retry 2*DBLookupDelay later reads the row.
	reads.left.Store(1)
	reads.on.Store(true)
	require.NoError(t, cfg.AddProviderKey(ctx, schemas.OpenAI, schemas.Key{ID: "k2", Name: "second", Value: *schemas.NewSecretVar("sk-2"), Models: schemas.WhiteList{"*"}, Weight: 1}))
	require.Equal(t, int32(1), reads.failed.Load())
	_, ok := storedKey(cfg, schemas.OpenAI, "k2")
	require.True(t, ok, "the retried read is published")

	// Every read after the write fails and the deadline comes before the next retry: the row is
	// saved, the update reports the failed reload, and memory keeps the previous record.
	store := cfg.ConfigStore
	cfg.ConfigStore = writeThenFailReads{ConfigStore: store, reads: reads}
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	key, _ := storedKey(cfg, schemas.OpenAI, "k2")
	key.Weight = 5
	require.Error(t, cfg.UpdateProviderKey(short, schemas.OpenAI, "k2", key))
	reads.on.Store(false)
	cfg.ConfigStore = store
	inMemory, _ := storedKey(cfg, schemas.OpenAI, "k2")
	require.Equal(t, 1.0, inMemory.Weight, "the previous record stays")
	stored, err := cfg.ConfigStore.GetProviderKey(ctx, schemas.OpenAI, "k2")
	require.NoError(t, err)
	require.NotNil(t, stored.Weight)
	require.Equal(t, 5.0, *stored.Weight, "the row was written")
}

// TestProviderSettingsUpdateKeepsKeyRowsAsStored checks that a provider settings update writes the
// key rows the database holds, not the live record's copy of them. After a key write whose refresh
// failed, on this node or on another, the record is behind the database: writing its keys back
// would bring a deleted key back, or delete a new key with its assignments.
func TestProviderSettingsUpdateKeepsKeyRowsAsStored(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	cfg := providerStore(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {Keys: []schemas.Key{
			{ID: "k1", Name: "first", Value: *schemas.NewSecretVar("sk-1"), Models: schemas.WhiteList{"*"}, Weight: 1},
			{ID: "k2", Name: "second", Value: *schemas.NewSecretVar("sk-2"), Models: schemas.WhiteList{"*"}, Weight: 1},
		}},
	})
	cfg.client = newNoopProviderClient(t, cfg)
	keyIDs := func(keys []schemas.Key) []string {
		ids := make([]string, 0, len(keys))
		for _, k := range keys {
			ids = append(ids, k.ID)
		}
		slices.Sort(ids)
		return ids
	}
	// saveSettings saves the provider's settings as the settings route does, from the live record,
	// keys included, and checks the keys the database and memory then hold.
	saveSettings := func(want []string, msg string) {
		t.Helper()
		require.NoError(t, cfg.UpdateProviderConfig(ctx, schemas.OpenAI, providersOf(cfg)[schemas.OpenAI]))
		stored, err := cfg.ConfigStore.GetProviderKeys(ctx, schemas.OpenAI)
		require.NoError(t, err)
		require.Equal(t, want, keyIDs(stored), "database: "+msg)
		require.Equal(t, want, keyIDs(providersOf(cfg)[schemas.OpenAI].Keys), "memory: "+msg)
	}

	// k2's row is deleted but memory was never refreshed, so it still holds the key.
	require.NoError(t, cfg.ConfigStore.DeleteProviderKey(ctx, schemas.OpenAI, "k2"))
	_, ok := storedKey(cfg, schemas.OpenAI, "k2")
	require.True(t, ok)
	saveSettings([]string{"k1"}, "a settings save must not bring the deleted key back")

	// k3's row is added but memory was never refreshed, so it does not hold the key.
	require.NoError(t, cfg.ConfigStore.CreateProviderKey(ctx, schemas.OpenAI, schemas.Key{ID: "k3", Name: "third", Value: *schemas.NewSecretVar("sk-3"), Models: schemas.WhiteList{"*"}, Weight: 1}))
	_, ok = storedKey(cfg, schemas.OpenAI, "k3")
	require.False(t, ok)
	saveSettings([]string{"k1", "k3"}, "a settings save must not delete the added key")
}

// newNoopProviderClient starts a core client over c's providers, so provider writes can rebuild
// instances. Nothing in these tests sends a request through it.
func newNoopProviderClient(t *testing.T, c *Config) *bifrost.Bifrost {
	t.Helper()
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: NewBaseAccount(c),
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)
	return client
}
