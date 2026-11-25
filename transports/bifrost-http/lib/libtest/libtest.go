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

// Config returns a lib.Config over a new SQLite config store holding providers, with its live
// store loaded from it.
func Config(t testing.TB, providers map[schemas.ModelProvider]configstore.ProviderConfig) *lib.Config {
	t.Helper()
	return WithProviders(t, &lib.Config{}, providers)
}

// WithProviders gives c a new SQLite config store holding providers and a live store loaded from
// it, and returns c. c must not have a config store of its own. A key without an ID or a name gets
// one, since the store keys rows by both; the caller's maps are left as they are.
func WithProviders(t testing.TB, c *lib.Config, providers map[schemas.ModelProvider]configstore.ProviderConfig) *lib.Config {
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
		if err := store.UpdateProvidersConfig(ctx, storable(providers)); err != nil {
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

// FileOnly loads providers into a new live store on c the way a gateway with the config store
// disabled loads config.json, and returns c. Nothing can write them afterwards, which suits tests
// that only read providers and need no database. A key without an ID gets one, as production gives
// it; explicit IDs and the key order are kept, and the caller's maps are left as they are.
func FileOnly(t testing.TB, c *lib.Config, providers map[schemas.ModelProvider]configstore.ProviderConfig) *lib.Config {
	t.Helper()
	return FileOnlyGovernance(t, c, providers, nil)
}

// FileOnlyGovernance is FileOnly with the virtual keys, teams, customers, budgets, rate limits and
// model configs gov declares loaded beside the providers, as config.json declares them to a
// gateway without a config store. A governance store built on c's live store then serves them.
func FileOnlyGovernance(t testing.TB, c *lib.Config, providers map[schemas.ModelProvider]configstore.ProviderConfig, gov *configstore.GovernanceConfig) *lib.Config {
	t.Helper()
	live, err := livestore.New()
	if err != nil {
		t.Fatalf("libtest: live store: %v", err)
	}
	t.Cleanup(live.Close)
	if err := c.AttachLiveStore(live); err != nil {
		t.Fatalf("libtest: attaching live store: %v", err)
	}
	if err := live.LoadRecords(context.Background(), lib.FileOnlyRecords(live, withKeyIDs(providers), gov)); err != nil {
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

// SetProvider writes a provider's configuration straight to c's config store and refreshes it, as
// a write made outside c is picked up, for a test that changes a provider partway through. A new
// provider is added with its keys. For an existing one the settings are saved, a key the store does
// not hold yet is created and one it holds is updated; a stored key config.Keys leaves out stays.
func SetProvider(t testing.TB, c *lib.Config, provider schemas.ModelProvider, config configstore.ProviderConfig) {
	t.Helper()
	if c.ConfigStore == nil {
		t.Fatal("libtest: SetProvider writes the config store; build the config with Config or WithProviders")
	}
	ctx := context.Background()
	config = storable(map[schemas.ModelProvider]configstore.ProviderConfig{provider: config})[provider]
	if !c.IsProviderConfigured(provider) {
		if err := c.ConfigStore.AddProvider(ctx, provider, config); err != nil {
			t.Fatalf("libtest: adding provider %s: %v", provider, err)
		}
	} else {
		if err := c.ConfigStore.UpdateProvider(ctx, provider, config); err != nil {
			t.Fatalf("libtest: writing provider %s: %v", provider, err)
		}
		for _, key := range config.Keys {
			err := c.ConfigStore.UpdateProviderKey(ctx, provider, key.ID, key)
			if errors.Is(err, configstore.ErrNotFound) {
				err = c.ConfigStore.CreateProviderKey(ctx, provider, key)
			}
			if err != nil {
				t.Fatalf("libtest: writing key %s of provider %s: %v", key.ID, provider, err)
			}
		}
	}
	if err := c.LiveStore.Refresh(ctx, []livestore.Ref{{Kind: livestore.KindProvider, ID: string(provider)}}); err != nil {
		t.Fatalf("libtest: refreshing provider %s: %v", provider, err)
	}
}

// storable copies providers, giving each key without an ID or a name one of its own.
func storable(providers map[schemas.ModelProvider]configstore.ProviderConfig) map[schemas.ModelProvider]configstore.ProviderConfig {
	out := make(map[schemas.ModelProvider]configstore.ProviderConfig, len(providers))
	for provider, config := range providers {
		keys := make([]schemas.Key, len(config.Keys))
		for i, key := range config.Keys {
			if key.ID == "" {
				key.ID = missingKeyID(provider, i)
			}
			if key.Name == "" {
				key.Name = key.ID
			}
			keys[i] = key
		}
		if config.Keys != nil {
			config.Keys = keys
		}
		out[provider] = config
	}
	return out
}

// withKeyIDs copies providers, giving each key without an ID one of its own, as the file loader
// does, so two keys a test leaves without an ID stay two key records.
func withKeyIDs(providers map[schemas.ModelProvider]configstore.ProviderConfig) map[schemas.ModelProvider]configstore.ProviderConfig {
	out := make(map[schemas.ModelProvider]configstore.ProviderConfig, len(providers))
	for provider, config := range providers {
		if config.Keys != nil {
			keys := make([]schemas.Key, len(config.Keys))
			for i, key := range config.Keys {
				if key.ID == "" {
					key.ID = missingKeyID(provider, i)
				}
				keys[i] = key
			}
			config.Keys = keys
		}
		out[provider] = config
	}
	return out
}

// missingKeyID is the ID the i-th key of provider gets when it has none. Production gives every
// key it loads an ID, and the live store holds one record per key ID, so keys a test leaves without
// one stay distinct keys.
func missingKeyID(provider schemas.ModelProvider, i int) string {
	return fmt.Sprintf("%s-key-%d", provider, i)
}

// Providers returns the providers c's live store holds, keyed by name, each with its keys.
func Providers(c *lib.Config) map[schemas.ModelProvider]configstore.ProviderConfig {
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
