// Package libtest builds lib.Config values for tests outside lib: a config over a new SQLite
// config store with its live store loaded from it, so provider reads and writes behave as they do
// in a running gateway.
package libtest

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
)

// Config returns a lib.Config over a new SQLite config store holding providers, each with its keys
// from keys, with its live store loaded from it. keys may be nil.
func Config(t testing.TB, providers map[schemas.ModelProvider]configstore.ProviderConfig, keys map[schemas.ModelProvider][]schemas.Key) *lib.Config {
	t.Helper()
	return WithProviders(t, &lib.Config{}, providers, keys)
}

// WithProviders gives c a new SQLite config store holding providers, each with its keys from keys,
// and a live store loaded from it, and returns c. c must not have a config store of its own. A key
// without an ID or a name gets one, since the store keys rows by both; the caller's maps are left
// as they are. A provider in keys must also be in providers.
func WithProviders(t testing.TB, c *lib.Config, providers map[schemas.ModelProvider]configstore.ProviderConfig, keys map[schemas.ModelProvider][]schemas.Key) *lib.Config {
	t.Helper()
	if c.ConfigStore != nil {
		t.Fatal("libtest: WithProviders creates the config store; build the config without one")
	}
	ctx := context.Background()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("libtest: config store: %v", err)
	}
	t.Cleanup(func() { store.Close(ctx) })
	if len(providers) > 0 {
		if err := store.UpdateProvidersConfig(ctx, providers, storable(keys)); err != nil {
			t.Fatalf("libtest: writing providers: %v", err)
		}
	}
	live, err := livestore.New()
	if err != nil {
		t.Fatalf("libtest: live store: %v", err)
	}
	t.Cleanup(live.Close)
	if err := c.AttachLiveStore(live); err != nil {
		t.Fatalf("libtest: attaching live store: %v", err)
	}
	if err := live.LoadAll(ctx, store.DB()); err != nil {
		t.Fatalf("libtest: loading live store: %v", err)
	}
	c.ConfigStore = store
	return c
}

// FileOnly loads providers, each with its keys from keys, into a new live store on c the way a
// gateway with the config store disabled loads config.json, and returns c. Nothing can write them
// afterwards, which suits tests that only read providers and need no database. A key without an ID
// gets one, as production gives it; explicit IDs and the key order are kept, and the caller's maps
// are left as they are.
func FileOnly(t testing.TB, c *lib.Config, providers map[schemas.ModelProvider]configstore.ProviderConfig, keys map[schemas.ModelProvider][]schemas.Key) *lib.Config {
	t.Helper()
	return FileOnlyGovernance(t, c, providers, keys, nil)
}

// FileOnlyGovernance is FileOnly with the virtual keys, teams, customers, budgets, rate limits and
// model configs gov declares loaded beside the providers, as config.json declares them to a
// gateway without a config store. A governance store built on c's live store then serves them.
func FileOnlyGovernance(t testing.TB, c *lib.Config, providers map[schemas.ModelProvider]configstore.ProviderConfig, keys map[schemas.ModelProvider][]schemas.Key, gov *configstore.GovernanceConfig) *lib.Config {
	t.Helper()
	live, err := livestore.New()
	if err != nil {
		t.Fatalf("libtest: live store: %v", err)
	}
	t.Cleanup(live.Close)
	if err := c.AttachLiveStore(live); err != nil {
		t.Fatalf("libtest: attaching live store: %v", err)
	}
	if err := live.LoadRecords(context.Background(), lib.FileOnlyRecords(live, providers, withKeyIDs(keys), gov)); err != nil {
		t.Fatalf("libtest: loading live store: %v", err)
	}
	return c
}

// MCPClients loads clients into a new live store on c without a database, replacing any live
// store c has, and returns c. Nothing can refresh them afterwards, which suits tests that only
// read clients; a test that changes a client loads the changed set again.
func MCPClients(t testing.TB, c *lib.Config, clients ...*schemas.MCPClientConfig) *lib.Config {
	t.Helper()
	live, err := livestore.New()
	if err != nil {
		t.Fatalf("libtest: live store: %v", err)
	}
	t.Cleanup(live.Close)
	if err := c.AttachLiveStore(live); err != nil {
		t.Fatalf("libtest: attaching live store: %v", err)
	}
	records := make([]livestore.Record, 0, len(clients))
	for _, client := range clients {
		records = append(records, livestore.Rec(live.Collections.MCPClients, &registry.MCPClient{Config: *client}))
	}
	if err := live.LoadRecords(context.Background(), records); err != nil {
		t.Fatalf("libtest: loading live store: %v", err)
	}
	return c
}

// FromStore loads a new live store on c from the config store c already has, as LoadConfig does
// at boot, and returns c.
func FromStore(t testing.TB, c *lib.Config) *lib.Config {
	t.Helper()
	if c.ConfigStore == nil {
		t.Fatal("libtest: FromStore loads from the config store; give the config one first")
	}
	live, err := livestore.New()
	if err != nil {
		t.Fatalf("libtest: live store: %v", err)
	}
	t.Cleanup(live.Close)
	if err := c.AttachLiveStore(live); err != nil {
		t.Fatalf("libtest: attaching live store: %v", err)
	}
	if err := live.LoadAll(context.Background(), c.ConfigStore.DB()); err != nil {
		t.Fatalf("libtest: loading live store: %v", err)
	}
	return c
}

// SetProvider writes a provider's configuration and keys straight to c's config store and
// refreshes it, as a write made outside c is picked up, for a test that changes a provider partway
// through. A new provider is added with its keys. For an existing one the settings are saved, a key
// the store does not hold yet is created and one it holds is updated; a stored key keys leaves out
// stays.
func SetProvider(t testing.TB, c *lib.Config, provider schemas.ModelProvider, config configstore.ProviderConfig, keys []schemas.Key) {
	t.Helper()
	if c.ConfigStore == nil {
		t.Fatal("libtest: SetProvider writes the config store; build the config with Config or WithProviders")
	}
	ctx := context.Background()
	keys = storable(map[schemas.ModelProvider][]schemas.Key{provider: keys})[provider]
	if !c.IsProviderConfigured(provider) {
		if err := c.ConfigStore.AddProvider(ctx, provider, config); err != nil {
			t.Fatalf("libtest: adding provider %s: %v", provider, err)
		}
	} else if err := c.ConfigStore.UpdateProvider(ctx, provider, config); err != nil {
		t.Fatalf("libtest: writing provider %s: %v", provider, err)
	}
	for _, key := range keys {
		err := c.ConfigStore.UpdateProviderKey(ctx, provider, key.ID, key)
		if errors.Is(err, configstore.ErrNotFound) {
			err = c.ConfigStore.CreateProviderKey(ctx, provider, key)
		}
		if err != nil {
			t.Fatalf("libtest: writing key %s of provider %s: %v", key.ID, provider, err)
		}
	}
	if err := c.LiveStore.Refresh(ctx, []livestore.Ref{{Kind: livestore.KindProvider, ID: string(provider)}}); err != nil {
		t.Fatalf("libtest: refreshing provider %s: %v", provider, err)
	}
}

// storable copies keys, giving each key without an ID or a name one of its own.
func storable(keys map[schemas.ModelProvider][]schemas.Key) map[schemas.ModelProvider][]schemas.Key {
	out := make(map[schemas.ModelProvider][]schemas.Key, len(keys))
	for provider, providerKeys := range keys {
		if providerKeys == nil {
			out[provider] = nil
			continue
		}
		copied := make([]schemas.Key, len(providerKeys))
		for i, key := range providerKeys {
			if key.ID == "" {
				key.ID = missingKeyID(provider, i)
			}
			if key.Name == "" {
				key.Name = key.ID
			}
			copied[i] = key
		}
		out[provider] = copied
	}
	return out
}

// withKeyIDs copies keys, giving each key without an ID one of its own, as the file loader does, so
// two keys a test leaves without an ID stay two key records.
func withKeyIDs(keys map[schemas.ModelProvider][]schemas.Key) map[schemas.ModelProvider][]schemas.Key {
	if keys == nil {
		return nil
	}
	out := make(map[schemas.ModelProvider][]schemas.Key, len(keys))
	for provider, list := range keys {
		if list == nil {
			out[provider] = nil
			continue
		}
		filled := make([]schemas.Key, len(list))
		for i, key := range list {
			if key.ID == "" {
				key.ID = missingKeyID(provider, i)
			}
			filled[i] = key
		}
		out[provider] = filled
	}
	return out
}

// missingKeyID is the ID the i-th key of provider gets when it has none. Production gives every
// key it loads an ID, and the live store holds one record per key ID, so keys a test leaves without
// one stay distinct keys.
func missingKeyID(provider schemas.ModelProvider, i int) string {
	return fmt.Sprintf("%s-key-%d", provider, i)
}

// Providers returns the configurations of the providers c's live store holds, keyed by name. A
// test reads a provider's keys with c.GetProviderKeys.
func Providers(c *lib.Config) map[schemas.ModelProvider]configstore.ProviderConfig {
	out := make(map[schemas.ModelProvider]configstore.ProviderConfig)
	c.LiveStore.Collections.Providers.Range(func(p *registry.Provider) bool {
		out[p.Name] = p.Config
		return true
	})
	return out
}
