package lib

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	bifrost "github.com/maximhq/bifrost/core"
	coremcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/livestoretest"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/maximhq/bifrost/framework/mcpdiscovery"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// withProviders loads providers, each with its keys from keys (which may be nil), into a new live
// store on c, the way file-only boot does, and returns c. It suits tests and benchmarks that only
// read providers; a write needs a database (providerStore).
func withProviders(t testing.TB, c *Config, providers map[schemas.ModelProvider]configstore.ProviderConfig, keys map[schemas.ModelProvider][]schemas.Key) *Config {
	t.Helper()
	live, err := livestore.New()
	require.NoError(t, err)
	t.Cleanup(live.Close)
	require.NoError(t, c.AttachLiveStore(live))
	require.NoError(t, live.LoadRecords(context.Background(), (&bootConfig{providers: providers, providerKeys: keys}).records(live)))
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

// providerStore returns a Config over a new SQLite config store holding providers and, for each
// provider in keys, its keys, with its live store loaded from it, for tests that write providers.
func providerStore(t *testing.T, providers map[schemas.ModelProvider]configstore.ProviderConfig, keys map[schemas.ModelProvider][]schemas.Key) *Config {
	t.Helper()
	ctx := context.Background()
	store := sqliteConfigStore(t)
	if len(providers) > 0 {
		require.NoError(t, store.UpdateProvidersConfig(ctx, providers, keys))
	}
	live, err := livestore.New()
	require.NoError(t, err)
	t.Cleanup(live.Close)
	c := &Config{ConfigStore: store}
	require.NoError(t, c.AttachLiveStore(live))
	require.NoError(t, live.LoadAll(ctx, store.DB()))
	return c
}

// newLiveStoreForTest returns a new, empty live store closed when the test ends.
var newLiveStoreForTest = livestoretest.New

// providersOf returns the providers c's live store holds, keyed by name. The configs hold no keys:
// c.GetProviderKeys reads a provider's keys.
func providersOf(c *Config) map[schemas.ModelProvider]configstore.ProviderConfig {
	out := make(map[schemas.ModelProvider]configstore.ProviderConfig)
	c.LiveStore.Collections.Providers.Range(func(p *registry.Provider) bool {
		out[p.Name] = p.Config
		return true
	})
	return out
}

// mcpClientsOf returns the MCP clients c's live store holds, in client ID order.
func mcpClientsOf(c *Config) []*schemas.MCPClientConfig {
	var out []*schemas.MCPClientConfig
	c.LiveStore.Collections.MCPClients.Range(func(client *registry.MCPClient) bool {
		config := client.Config
		out = append(out, &config)
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

// TestLiveStoreLoadHookRunsBeforeTheLoad checks that the registered hook runs with the config store
// open and before the live store has loaded, only when there is a config store, and that its error
// fails LoadConfig.
func TestLiveStoreLoadHookRunsBeforeTheLoad(t *testing.T) {
	initTestLogger()
	// Build the shared template database first: its boot runs before any test's hook is registered.
	createTempDir(t)
	t.Cleanup(func() { RegisterLiveStoreLoadHook(nil) })
	calls := 0
	RegisterLiveStoreLoadHook(func(ctx context.Context, config *Config) error {
		calls++
		require.NotNil(t, config.ConfigStore)
		// A store that has not loaded yet refuses a refresh with ErrNotLoaded, not the kind check.
		require.ErrorIs(t, config.LiveStore.RefreshKind(ctx, livestore.Kind("missing")), livestore.ErrNotLoaded)
		return nil
	})
	bootLive(t, true, "")
	require.Equal(t, 1, calls)
	bootLive(t, false, "")
	require.Equal(t, 1, calls, "no config store, no hook")

	RegisterLiveStoreLoadHook(func(context.Context, *Config) error { return fmt.Errorf("tables not ready") })
	dir := createTempDir(t)
	body := fmt.Sprintf(`{"$schema": "https://www.getbifrost.ai/schema", "config_store": {"enabled": true, "type": "sqlite", "config": {"path": %q}}}`, filepath.Join(dir, "config.db"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600))
	_, err := LoadConfig(context.Background(), dir)
	require.ErrorContains(t, err, "tables not ready")
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

// TestAttachLiveStoreRefusesNilAndASecondAttach checks that AttachLiveStore answers an error, not a
// panic, when it is given no store, and when the store already holds the shared tables because a
// config was attached to it before.
func TestAttachLiveStoreRefusesNilAndASecondAttach(t *testing.T) {
	require.ErrorIs(t, (&Config{}).AttachLiveStore(nil), livestore.ErrNoLiveStore)
	live, err := livestore.New()
	require.NoError(t, err)
	t.Cleanup(live.Close)
	require.NoError(t, (&Config{}).AttachLiveStore(live))
	require.Error(t, (&Config{}).AttachLiveStore(live))
}

// TestProviderRefreshReadsAnOutsideWrite checks the refresh path: a provider and key written to the
// database by someone else reach memory only when a refresh re-reads them, and a provider whose
// row is not readable yet is reported, not removed.
func TestProviderRefreshReadsAnOutsideWrite(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	cfg := providerStore(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {},
	}, map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {{ID: "k1", Name: "first", Value: *schemas.NewSecretVar("sk-1"), Models: schemas.WhiteList{"*"}, Weight: 1}},
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
		schemas.OpenAI: {ConfigHash: "from-file"},
	}, map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {{ID: "k1", Name: "first", Value: *schemas.NewSecretVar("sk-1"), Models: schemas.WhiteList{"*"}, Weight: 1}},
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
		schemas.OpenAI: {},
	}, map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{ID: "k1", Name: "first", Value: *schemas.NewSecretVar("sk-1"), Models: schemas.WhiteList{"*"}, Weight: 1},
			{ID: "k2", Name: "second", Value: *schemas.NewSecretVar("sk-2"), Models: schemas.WhiteList{"*"}, Weight: 1},
		},
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
		schemas.OpenAI: {},
	}, map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{ID: "k1", Name: "first", Value: *schemas.NewSecretVar("sk-1"), Models: schemas.WhiteList{"*"}, Weight: 1},
			{ID: "k2", Name: "second", Value: *schemas.NewSecretVar("sk-2"), Models: schemas.WhiteList{"*"}, Weight: 1},
		},
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
		schemas.OpenAI: {},
	}, map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {{ID: "k1", Name: "first", Value: *schemas.NewSecretVar("sk-1"), Models: schemas.WhiteList{"*"}, Weight: 1}},
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
		schemas.OpenAI: {},
	}, map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{ID: "k1", Name: "first", Value: *schemas.NewSecretVar("sk-1"), Models: schemas.WhiteList{"*"}, Weight: 1},
			{ID: "k2", Name: "second", Value: *schemas.NewSecretVar("sk-2"), Models: schemas.WhiteList{"*"}, Weight: 1},
		},
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
	// and checks the keys the database and memory then hold.
	saveSettings := func(want []string, msg string) {
		t.Helper()
		require.NoError(t, cfg.UpdateProviderConfig(ctx, schemas.OpenAI, providersOf(cfg)[schemas.OpenAI]))
		stored, err := cfg.ConfigStore.GetProviderKeys(ctx, schemas.OpenAI)
		require.NoError(t, err)
		require.Equal(t, want, keyIDs(stored), "database: "+msg)
		require.Equal(t, want, keyIDs(cfg.GetProviderKeys(schemas.OpenAI)), "memory: "+msg)
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

// TestDisableMCPClientRollbackKeepsADisabledClientDisabled checks the rollback of a disable the
// runtime refuses: the row goes back to the flag it held before, so a client that was already
// disabled stays disabled instead of being enabled by the rollback.
func TestDisableMCPClientRollbackKeepsADisabledClientDisabled(t *testing.T) {
	ctx := context.Background()
	c := providerStore(t, nil, nil)
	// A core client started without MCP has no MCP manager, so the runtime refuses every disable.
	c.SetBifrostClient(newNoopProviderClient(t, c))
	client := &schemas.MCPClientConfig{
		ID: "docs-id", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("http://127.0.0.1:1"), AuthType: schemas.MCPAuthTypeNone,
		ToolsToExecute: schemas.WhiteList{"*"}, Disabled: true,
	}
	require.NoError(t, c.ConfigStore.CreateMCPClientConfig(ctx, client))

	require.Error(t, c.DisableMCPClient(ctx, client.ID), "the runtime has no MCP manager to disable the client in")
	stored, err := c.ConfigStore.GetMCPClientByID(ctx, client.ID)
	require.NoError(t, err)
	require.True(t, stored.Disabled, "the failed disable's rollback enabled a client that was already disabled")
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

// TestAddMCPClientAgainKeepsTheHeldClient checks a repeated add of a client the runtime and memory
// already hold: the runtime refuses it, and memory keeps the client the runtime still serves, since
// this call did not add it. Only a client the failed add itself loaded is dropped again.
func TestAddMCPClientAgainKeepsTheHeldClient(t *testing.T) {
	ctx := context.Background()
	upstream := mcpserver.NewMCPServer("docs", "1.0.0", mcpserver.WithToolCapabilities(true))
	upstream.AddTool(mcp.NewTool("search"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("found"), nil
	})
	ts := httptest.NewServer(mcpserver.NewStreamableHTTPServer(upstream))
	t.Cleanup(ts.Close)

	c := providerStore(t, nil, nil)
	c.SetBifrostClient(newNoopProviderClient(t, c))
	client := &schemas.MCPClientConfig{
		ID: "docs-id", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), AuthType: schemas.MCPAuthTypeNone, ToolsToExecute: schemas.WhiteList{"*"},
	}
	require.NoError(t, c.ConfigStore.CreateMCPClientConfig(ctx, client))
	require.NoError(t, c.AddMCPClient(ctx, client.ID))

	require.Error(t, c.AddMCPClient(ctx, client.ID), "the runtime already holds the client")
	_, ok := c.liveMCPClient(client.ID)
	require.True(t, ok, "a failed repeat add dropped the client the runtime still serves from memory")
	served, err := NewBaseAccount(c).GetConfigForMCPClient(client.ID)
	require.NoError(t, err)
	require.Equal(t, "docs", served.Name)
}

// newDiscoveryRestoreConfig returns a Config with a database, a discovery store and a runtime
// keeping shared discoveries in that store. The runtime has no credential store, so a client whose
// tools are not restored from its row is left without them: the admin credential a discovery
// needs cannot be resolved.
func newDiscoveryRestoreConfig(t *testing.T) (*Config, *bifrost.Bifrost) {
	t.Helper()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	c := providerStore(t, nil, nil)
	shared, err := mcpdiscovery.NewSharedStore(c.ConfigStore, c.LiveStore, logger)
	require.NoError(t, err)
	perUser, err := mcpdiscovery.NewPerUserStore(c.ConfigStore, c.LiveStore, logger)
	require.NoError(t, err)
	c.MCPSharedDiscoveryStore = shared
	client := newNoopProviderClient(t, c)
	client.SetMCPManager(coremcp.NewMCPManager(context.Background(), schemas.MCPConfig{
		SharedDiscoveryStore:  shared,
		PerUserDiscoveryStore: perUser,
	}, NewBaseAccount(c), nil, logger, nil))
	c.SetBifrostClient(client)
	return c, client
}

// tokenExchangeClient is a token_exchange client, which connects per call and discovers its tools
// with a retained admin credential.
func tokenExchangeClient() *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID: "tx-id", Name: "tx", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("http://127.0.0.1:1/mcp"), AuthType: schemas.MCPAuthTypeTokenExchange,
		TokenExchange:  &schemas.MCPTokenExchangeConfig{Audience: "api://saas", ClientID: schemas.NewSecretVar("exchange-app")},
		ToolsToExecute: schemas.WhiteList{"*"},
	}
}

// tokenExchangeTools is the discovery a verification of tokenExchangeClient found: the tool lookup.
func tokenExchangeTools() map[string]schemas.ChatTool {
	return map[string]schemas.ChatTool{
		"tx-lookup": {Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "tx-lookup"}},
	}
}

// requireServesTheRowsTools checks that the runtime serves the MCP client with this ID as healthy,
// with the one tool tokenExchangeTools holds.
func requireServesTheRowsTools(t *testing.T, client *bifrost.Bifrost, id string) {
	t.Helper()
	clients, err := client.GetMCPClients()
	require.NoError(t, err)
	for _, got := range clients {
		if got.Config.ID != id {
			continue
		}
		names := make([]string, 0, len(got.Tools))
		for _, tool := range got.Tools {
			names = append(names, tool.Name)
		}
		require.Equal(t, schemas.MCPConnectionStateHealthy, got.State, "last failure: %+v", got.LastFailure)
		require.Equal(t, []string{"lookup"}, names, "the client serves the tools its row holds")
		return
	}
	require.FailNow(t, "the runtime does not hold the client", id)
}

// TestAddMCPClientServesTheDiscoveryItsRowHolds checks a client connected while Bifrost runs whose
// row already holds its discovery: one verified when it was created, or created by another process
// over the same database. It serves the row's tools at once, as after a restart, without
// discovering them again, though no admin credential could discover them yet.
func TestAddMCPClientServesTheDiscoveryItsRowHolds(t *testing.T) {
	ctx := context.Background()
	c, client := newDiscoveryRestoreConfig(t)
	stored := tokenExchangeClient()
	stored.DiscoveredTools = tokenExchangeTools()
	require.NoError(t, c.ConfigStore.CreateMCPClientConfig(ctx, stored))

	require.NoError(t, c.AddMCPClient(ctx, stored.ID))
	requireServesTheRowsTools(t, client, stored.ID)
}

// TestUpdateMCPClientCredentialsServesTheDiscoveryItsRowHolds checks a per-call client that waited
// for its admin step and completes it through a credential update once its row holds the
// discovery: it serves the row's tools at once, without discovering them again.
func TestUpdateMCPClientCredentialsServesTheDiscoveryItsRowHolds(t *testing.T) {
	ctx := context.Background()
	c, client := newDiscoveryRestoreConfig(t)
	stored := tokenExchangeClient()
	stored.PendingVerification = true
	require.NoError(t, c.ConfigStore.CreateMCPClientConfig(ctx, stored))
	require.NoError(t, c.AddMCPClient(ctx, stored.ID))
	clients, err := client.GetMCPClients()
	require.NoError(t, err)
	require.Len(t, clients, 1)
	require.Equal(t, schemas.MCPConnectionStatePendingVerification, clients[0].State, "fixture check: the client waits for its admin step")

	require.NoError(t, c.ConfigStore.UpdateMCPClientDiscovery(ctx, stored.ID, &schemas.MCPDiscovery{Tools: tokenExchangeTools()}))
	require.NoError(t, c.ConfigStore.ClearMCPClientPendingVerification(ctx, stored.ID))
	require.NoError(t, c.UpdateMCPClientCredentials(ctx, stored.ID))
	requireServesTheRowsTools(t, client, stored.ID)
}

// TestMCPClientWritesReachMemoryThroughTheDatabase checks the MCP client write path: the caller
// writes the row, the runtime takes the client, and only then does the live store load the row,
// which the Account serves from there. An edit and a removal reach memory the same way.
func TestMCPClientWritesReachMemoryThroughTheDatabase(t *testing.T) {
	ctx := context.Background()
	upstream := mcpserver.NewMCPServer("docs", "1.0.0", mcpserver.WithToolCapabilities(true))
	upstream.AddTool(mcp.NewTool("search"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("found"), nil
	})
	ts := httptest.NewServer(mcpserver.NewStreamableHTTPServer(upstream))
	t.Cleanup(ts.Close)

	c := providerStore(t, nil, nil)
	c.SetBifrostClient(newNoopProviderClient(t, c))
	client := &schemas.MCPClientConfig{
		ID: "docs-id", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), AuthType: schemas.MCPAuthTypeNone, ToolsToExecute: schemas.WhiteList{"*"},
	}
	require.NoError(t, c.ConfigStore.CreateMCPClientConfig(ctx, client))
	_, ok := c.liveMCPClient(client.ID)
	require.False(t, ok, "a written row is not in memory until the runtime has taken the client")

	require.NoError(t, c.AddMCPClient(ctx, client.ID))
	account := NewBaseAccount(c)
	ids, err := account.GetConfiguredMCPClients()
	require.NoError(t, err)
	require.Equal(t, []string{client.ID}, ids)
	served, err := account.GetConfigForMCPClient(client.ID)
	require.NoError(t, err)
	require.Equal(t, "docs", served.Name)

	renamed := *client
	renamed.Name = "docs_renamed"
	storeMCPClientEdit(t, c, &renamed)
	require.NoError(t, c.UpdateMCPClient(ctx, client.ID))
	_, ok = c.LiveStore.Collections.MCPClients.GetBy(registry.MCPClientByName, "docs_renamed")
	require.True(t, ok, "the edited row is in memory")
	_, ok = c.LiveStore.Collections.MCPClients.GetBy(registry.MCPClientByName, "docs")
	require.False(t, ok, "and the old name is gone from the index")

	require.NoError(t, c.ConfigStore.DeleteMCPClientConfig(ctx, client.ID))
	require.NoError(t, c.RemoveMCPClient(ctx, client.ID))
	_, err = account.GetConfigForMCPClient(client.ID)
	require.ErrorIs(t, err, ErrNotFound, "a removed client leaves memory")
}

// TestMCPHeaderAllowlistFollowsTheClients checks the MCP header allowlist view: it combines every
// client's allowed extra headers, follows a client's row as it is written and refreshed, and becomes
// unrestricted while any client allows every header.
func TestMCPHeaderAllowlistFollowsTheClients(t *testing.T) {
	ctx := context.Background()
	c := providerStore(t, nil, nil)
	client := func(id string, headers ...string) *schemas.MCPClientConfig {
		return &schemas.MCPClientConfig{
			ID: id, Name: id, ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar("https://" + id + ".example/mcp"),
			AuthType: schemas.MCPAuthTypeNone, ToolsToExecute: schemas.WhiteList{"*"}, AllowedExtraHeaders: headers,
		}
	}
	require.Empty(t, c.GetMCPHeaderCombinedAllowlist(), "no clients, no headers")

	require.NoError(t, c.ConfigStore.CreateMCPClientConfig(ctx, client("docs", "x-docs")))
	require.NoError(t, c.ConfigStore.CreateMCPClientConfig(ctx, client("search", "x-search")))
	require.NoError(t, c.refreshMCPClient(ctx, "docs"))
	require.NoError(t, c.refreshMCPClient(ctx, "search"))
	require.ElementsMatch(t, schemas.WhiteList{"x-docs", "x-search"}, c.GetMCPHeaderCombinedAllowlist())

	storeMCPClientEdit(t, c, client("docs", "*"))
	require.NoError(t, c.refreshMCPClient(ctx, "docs"))
	require.Equal(t, schemas.WhiteList{"*"}, c.GetMCPHeaderCombinedAllowlist(), "one client allowing every header allows every header")

	require.NoError(t, c.ConfigStore.DeleteMCPClientConfig(ctx, "docs"))
	require.NoError(t, c.refreshMCPClient(ctx, "docs"))
	require.Equal(t, schemas.WhiteList{"x-search"}, c.GetMCPHeaderCombinedAllowlist(), "a deleted client's headers go with it")
}

// TestLoadConfigLoadsGovernanceInBothModes checks that the governance config.json declares is
// served from the live store whether boot reads it back from the config store or, with none, loads
// it from the file alone: a key under a team holding a budget, and a model config holding a rate
// limit, are found by the governance store built on that live store.
func TestLoadConfigLoadsGovernanceInBothModes(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	withStore, fileOnly := bootBothModes(t, `"providers": {"openai": {"keys": [
		{"id": "k1", "name": "first", "value": "sk-1", "models": ["*"], "weight": 1}
	]}},
	"governance": {
		"budgets": [{"id": "budget-team", "max_limit": 50, "reset_duration": "1d", "team_id": "team-1"}],
		"rate_limits": [{"id": "rl-mc", "request_max_limit": 10, "request_reset_duration": "1m"}],
		"teams": [{"id": "team-1", "name": "platform"}],
		"virtual_keys": [{"id": "vk-1", "name": "app", "value": "sk-bf-app", "is_active": true, "team_id": "team-1",
			"provider_configs": [{"provider": "openai", "weight": 1, "allowed_models": ["*"]}]}],
		"model_configs": [{"id": "mc-1", "model_name": "gpt-4o", "provider": "openai", "rate_limit_id": "rl-mc"}]
	}`)

	for name, config := range map[string]*Config{"config store": withStore, "file only": fileOnly} {
		gs, err := governance.NewLocalGovernanceStore(ctx, governance.NewMockLogger(), nil, config.LiveStore, nil)
		require.NoError(t, err, name)
		t.Cleanup(gs.Close)

		vk, ok := gs.GetVirtualKey(ctx, "sk-bf-app")
		require.True(t, ok, "%s: the key is served by its value", name)
		require.Equal(t, "vk-1", vk.ID, name)
		require.True(t, vk.Active, name)
		require.Equal(t, "team-1", vk.TeamID, name)
		require.Equal(t, "platform", gs.GetTeamName(ctx, "team-1"), name)

		budgets := gs.CollectTeamBudgets(ctx, "team-1")
		require.Len(t, budgets, 1, "%s: the team holds its budget", name)
		require.Equal(t, "budget-team", budgets[0].ID, name)
		require.Equal(t, 50.0, budgets[0].MaxLimit, name)

		_, rateLimits := gs.GlobalModelLimits(ctx, schemas.OpenAI, "gpt-4o")
		require.Len(t, rateLimits, 1, "%s: the model config holds its rate limit", name)
		require.Equal(t, "rl-mc", rateLimits[0].ID, name)
		require.Equal(t, "mc-1", rateLimits[0].HolderID, name)
		rateLimit := gs.LoadRateLimit(ctx, "rl-mc")
		require.NotNil(t, rateLimit, name)
		require.Equal(t, int64(10), *rateLimit.RequestMaxLimit, name)
	}
}

// signingKeyStore is a config store whose GetOAuth2SigningKey counts its calls and holds the first
// one until release is closed, failing it when failFirst is set.
type signingKeyStore struct {
	configstore.ConfigStore
	calls     atomic.Int32
	started   chan struct{}
	release   chan struct{}
	failFirst bool
}

// GetOAuth2SigningKey counts the call; the first one closes started, waits for release, and fails
// when failFirst is set.
func (s *signingKeyStore) GetOAuth2SigningKey(context.Context) (*configstoreTables.OAuth2SigningKey, error) {
	if s.calls.Add(1) == 1 {
		close(s.started)
		<-s.release
		if s.failFirst {
			return nil, errors.New("signing key read failed")
		}
	}
	return &configstoreTables.OAuth2SigningKey{KID: "kid-1"}, nil
}

// signingKeyConfig returns a config reading its signing key from store through a new live store.
func signingKeyConfig(t *testing.T, store *signingKeyStore) *Config {
	t.Helper()
	live, err := livestore.New()
	require.NoError(t, err)
	t.Cleanup(live.Close)
	cfg := &Config{ConfigStore: store}
	require.NoError(t, cfg.AttachLiveStore(live))
	return cfg
}

// TestOAuth2SigningKeyFirstLoadIsShared checks that callers arriving while the first signing-key
// load runs share it, so a burst of first requests costs one config-store read and decrypt rather
// than a second load after the first.
func TestOAuth2SigningKeyFirstLoadIsShared(t *testing.T) {
	store := &signingKeyStore{started: make(chan struct{}), release: make(chan struct{})}
	releaseLoad := sync.OnceFunc(func() { close(store.release) })
	t.Cleanup(releaseLoad)
	cfg := signingKeyConfig(t, store)
	ctx := t.Context()

	const callers = 8
	errs := make(chan error, callers)
	get := func() {
		key, err := cfg.GetOAuth2SigningKey(ctx)
		if err == nil && key.KID != "kid-1" {
			err = fmt.Errorf("got key %q, want kid-1", key.KID)
		}
		errs <- err
	}
	go get()
	select {
	case <-store.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the first signing-key load did not start within 10s")
	}
	for range callers - 1 {
		go get()
	}
	// Give the other callers time to reach the running load. One that arrives after the key is
	// installed reads it from the cache, which makes no second read either.
	time.Sleep(100 * time.Millisecond)
	releaseLoad()
	for range callers {
		require.NoError(t, <-errs)
	}
	require.Equal(t, int32(1), store.calls.Load(), "callers that arrived during the first load must share it")
}

// TestOAuth2SigningKeyFailedLoadIsNotCached checks that a failed signing-key load is not cached: the
// next call reads the config store again and caches the key it gets.
func TestOAuth2SigningKeyFailedLoadIsNotCached(t *testing.T) {
	store := &signingKeyStore{started: make(chan struct{}), release: make(chan struct{}), failFirst: true}
	close(store.release)
	cfg := signingKeyConfig(t, store)
	ctx := t.Context()

	_, err := cfg.GetOAuth2SigningKey(ctx)
	require.Error(t, err, "the first load fails")
	key, err := cfg.GetOAuth2SigningKey(ctx)
	require.NoError(t, err, "the next call loads again")
	require.Equal(t, "kid-1", key.KID)
	_, err = cfg.GetOAuth2SigningKey(ctx)
	require.NoError(t, err)
	require.Equal(t, int32(2), store.calls.Load(), "the failed load is retried once and the key is then cached")
}
