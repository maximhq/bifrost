package lib

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
)

var (
	presetStoreMu sync.Mutex
	presetStore   *livestore.Store
)

// SetLiveStore hands LoadConfig the live store to load instead of a new one. A build that
// registers collections of its own creates the store with livestore.New, registers its own on it,
// and calls SetLiveStore before LoadConfig. Passing nil clears it. LoadConfig takes the preset
// once, so a later LoadConfig in the same process creates its own unless SetLiveStore is called
// again.
func SetLiveStore(store *livestore.Store) {
	presetStoreMu.Lock()
	presetStore = store
	presetStoreMu.Unlock()
}

// takePresetStore returns the store SetLiveStore handed over, clearing it, or nil when there is
// none.
func takePresetStore() *livestore.Store {
	presetStoreMu.Lock()
	defer presetStoreMu.Unlock()
	store := presetStore
	presetStore = nil
	return store
}

// loadLiveStore fills the live store once everything LoadConfig writes to the config store has
// been written: from the config store when there is one, otherwise from config.json alone.
func loadLiveStore(ctx context.Context, config *Config) error {
	live := takePresetStore()
	if live == nil {
		var err error
		if live, err = livestore.New(); err != nil {
			return fmt.Errorf("failed to create live store: %w", err)
		}
	}
	if err := config.AttachLiveStore(live); err != nil {
		return err
	}
	if config.ConfigStore != nil {
		if err := live.LoadAll(ctx, config.ConfigStore.DB()); err != nil {
			return fmt.Errorf("failed to load live store: %w", err)
		}
	} else if err := config.boot.uniqueKeyIDs(); err != nil {
		return err
	} else if err := live.LoadRecords(ctx, config.boot.records(live)); err != nil {
		return fmt.Errorf("failed to load live store: %w", err)
	}
	config.boot = nil
	return nil
}

// AttachLiveStore makes live the config's live store and keeps the routing view derived from it
// current: a provider's "name/model" prefix is registered when its record is published and
// unregistered when it is removed. A refresh calls its subscribers before it returns, so a created
// provider routes by its prefix once AddProvider returns, before any reload or model discovery, and
// a provider written elsewhere routes once its refresh returns. Provider keys are served from
// live.Views.ProviderKeyLists, which the store itself keeps current. Call it once per live store,
// before live loads, so the configured providers are registered at boot too; a test that builds
// its own live store calls it the same way. It fails when live is nil.
func (c *Config) AttachLiveStore(live *livestore.Store) error {
	if live == nil {
		return fmt.Errorf("failed to attach live store: %w", livestore.ErrNoLiveStore)
	}
	c.LiveStore = live
	live.Collections.Providers.Subscribe(func(_ context.Context, changes []livestore.Change[registry.Provider]) {
		for _, change := range changes {
			if change.New != nil {
				schemas.RegisterKnownProvider(change.New.Name)
			} else {
				schemas.UnregisterKnownProvider(schemas.ModelProvider(change.ID))
			}
		}
	})
	return nil
}

// bootConfig is the one place config.json content waits between the section loaders and the live
// store. Settings (client, server, websocket, framework, auth, proxy and the rest) stay on Config;
// everything config.json declares that becomes live-store data has a field here, and only here.
//
// The rule every section loader follows:
//  1. Parse and validate its section into its field below (merged with the config store's rows,
//     where the loader merges).
//  2. With a config store, sync that content into the database.
//  3. Never write the live store: loadLiveStore is the one place memory is filled, with LoadAll
//     from the database when there is a config store, and otherwise with LoadRecords from the
//     records this struct builds (records). It then drops the struct, so nothing reads it after
//     boot. Boot steps that run before the load read their content here.
//
// It exists only while LoadConfig runs: a loader called outside it (a test, or a later reload)
// leaves it alone. Each field says what fills it and whether a gateway without a config store
// loads it. A kind skipped without a config store keeps its loader's early return and warning;
// loading it in that mode means adding it to records and dropping that early return.
type bootConfig struct {
	// providers is loadProviders' merged provider set. Loaded without a config store.
	providers map[schemas.ModelProvider]configstore.ProviderConfig
	// mcpClients is the MCP client list loadMCPConfig hands the runtime. Skipped without a config
	// store: MCP needs one.
	mcpClients []*schemas.MCPClientConfig
	// virtualMCPs is config.json's virtual MCPs, set by loadMCPConfig. Skipped without a config store.
	virtualMCPs []schemas.VirtualMCPConfig
	// governance is loadGovernanceConfig's entity lists (keys, teams, customers, budgets, rate
	// limits, model configs, provider governance, routing rules and pricing overrides); its
	// settings stay on Config.GovernanceConfig. Loaded without a config store, routing rules
	// excepted: those need one.
	governance *configstore.GovernanceConfig
	// webhooks is config.json's webhook endpoints, set by loadWebhooksConfig. Skipped without a
	// config store.
	webhooks []*WebhookEndpointConfig
	// agents is config.json's agent registrations, set by loadAgentsConfig. Skipped without a
	// config store.
	agents []*AgentRegistrationFileConfig
	// relays is config.json's relays, set by loadRelaysConfig. Skipped without a config store.
	relays []*schemas.RelayConfig
	// modelRouters is config.json's model routers, set by syncModelRoutersFromFile. Skipped without a
	// config store.
	modelRouters []configstore.ModelRouter
	// datasheetOverrides is config.json's datasheet overrides, set by
	// loadDatasheetOverridesFromFile; the model catalog applies them in both modes.
	datasheetOverrides []DatasheetOverrideFileConfig
	// featureFlags is config.json's feature flag overrides, set by initFeatureFlags; the flag store
	// applies them in both modes.
	featureFlags *FeatureFlagsFileConfig
}

// records builds the live store records for everything b holds, for file-only mode. A provider's
// keys become records of their own, ordered by their position in config.json.
func (b *bootConfig) records(live *livestore.Store) []livestore.Record {
	records := make([]livestore.Record, 0, len(b.providers)+len(b.mcpClients))
	for name, providerConfig := range b.providers {
		records = append(records, livestore.Rec(live.Collections.Providers, &registry.Provider{Name: name, Settings: providerConfig.Settings()}))
		for i, key := range providerConfig.Keys {
			records = append(records, livestore.Rec(live.Collections.ProviderKeys, &registry.ProviderKey{Provider: name, Order: int64(i), Key: key}))
		}
	}
	for _, client := range b.mcpClients {
		records = append(records, livestore.Rec(live.Collections.MCPClients, &registry.MCPClient{Config: *client}))
	}
	return records
}

// uniqueKeyIDs fails when two keys of b's providers share a key ID, naming the ID and both
// providers. The live store holds one record per key ID, so file-only boot, with no database to
// refuse the second key, would otherwise keep one of them and drop the other without an error.
// Providers are visited by name, so the error names the same pair on every boot.
func (b *bootConfig) uniqueKeyIDs() error {
	owners := make(map[string]schemas.ModelProvider)
	for _, name := range slices.Sorted(maps.Keys(b.providers)) {
		for _, key := range b.providers[name].Keys {
			first, taken := owners[key.ID]
			switch {
			case !taken:
				owners[key.ID] = name
			case first == name:
				return fmt.Errorf("key ID %q is used by two keys of provider %s: give each key in config.json its own id", key.ID, name)
			default:
				return fmt.Errorf("key ID %q is used by providers %s and %s: give each key in config.json its own id", key.ID, first, name)
			}
		}
	}
	return nil
}

// adminRetryBudget is how long an admin write keeps retrying the re-read of the rows it wrote when
// its context has no deadline, as a *fasthttp.RequestCtx does not. With AdminRetry's wait of
// 2*DBLookupDelay it allows the first read and up to two retries: enough to ride out a dropped
// connection, a failover or a lock timeout, while the operator still gets an answer within seconds.
// A peer applying a change made elsewhere has no one waiting and uses DBLookupRetry's longer run.
const adminRetryBudget = DBLookupMaxRetries * DBLookupDelay

// AdminRetry is the refresh options for an admin write re-reading the rows it just wrote: a read
// that fails with an error is retried 2*DBLookupDelay apart, DBLookupRetry's wait, until the
// context's deadline or, when it has none, for adminRetryBudget. A missing row is not retried:
// this node just wrote it, so a row it cannot find was deleted since, and its record is removed as
// on any refresh. When the retries run out the refresh returns the read's error, the previous
// record stays, and the write answers 500. Callers spread it: Refresh(ctx, refs, AdminRetry()...).
func AdminRetry() []livestore.RefreshOption {
	return []livestore.RefreshOption{livestore.RetryErrorsWithin(adminRetryBudget, 2*DBLookupDelay)}
}

// DBLookupRetry is the refresh options for applying a change made elsewhere, whose row may not be
// readable yet and whose failed read has no caller to report to: a row not found, or a read that
// fails with an error, is retried DBLookupMaxRetries times, 2*DBLookupDelay apart, the same wait
// the reload wrappers give their own row reads, and a missing row is never removed. Callers spread
// it: Refresh(ctx, refs, DBLookupRetry()...).
func DBLookupRetry() []livestore.RefreshOption {
	return []livestore.RefreshOption{
		livestore.RetryMissing(DBLookupMaxRetries, 2*DBLookupDelay),
		livestore.RetryErrors(DBLookupMaxRetries, 2*DBLookupDelay),
	}
}

// mcpClientExists reports whether b holds an MCP client with this name.
func (b *bootConfig) mcpClientExists(name string) bool {
	for _, client := range b.mcpClients {
		if client != nil && client.Name == name {
			return true
		}
	}
	return false
}
