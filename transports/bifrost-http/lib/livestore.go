package lib

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
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

// LiveStoreLoadHook runs once LoadConfig has written everything it writes to the config store,
// right before the live store loads from it. Use it to prepare the tables that registered
// collections read, so the load finds them; its error fails LoadConfig. It does not run without a
// config store.
type LiveStoreLoadHook func(ctx context.Context, config *Config) error

var (
	liveStoreLoadHookMu sync.RWMutex
	liveStoreLoadHook   LiveStoreLoadHook
)

// RegisterLiveStoreLoadHook installs the hook LoadConfig calls before the live store loads. Must be
// called before LoadConfig. Passing nil clears it.
func RegisterLiveStoreLoadHook(fn LiveStoreLoadHook) {
	liveStoreLoadHookMu.Lock()
	liveStoreLoadHook = fn
	liveStoreLoadHookMu.Unlock()
}

// runLiveStoreLoadHook calls the registered hook, if any.
func runLiveStoreLoadHook(ctx context.Context, config *Config) error {
	liveStoreLoadHookMu.RLock()
	hook := liveStoreLoadHook
	liveStoreLoadHookMu.RUnlock()
	if hook == nil {
		return nil
	}
	return hook(ctx, config)
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

// attachLiveStore gives config its live store, before anything that keeps state in it is built:
// the store SetLiveStore handed over, or a new one. It is empty until loadLiveStore fills it.
func attachLiveStore(config *Config) error {
	live := takePresetStore()
	if live == nil {
		var err error
		if live, err = livestore.New(); err != nil {
			return fmt.Errorf("failed to create live store: %w", err)
		}
	}
	return config.AttachLiveStore(live)
}

// loadLiveStore fills config's live store once everything LoadConfig writes to the config store has
// been written: from the config store when there is one, otherwise from config.json alone.
func loadLiveStore(ctx context.Context, config *Config) error {
	live := config.LiveStore
	if config.ConfigStore != nil {
		if err := runLiveStoreLoadHook(ctx, config); err != nil {
			return err
		}
		if err := live.LoadAll(ctx, config.ConfigStore.DB()); err != nil {
			return fmt.Errorf("failed to load live store: %w", err)
		}
	} else if err := config.boot.uniqueKeyIDs(); err != nil {
		return err
	} else if err := live.LoadRecords(ctx, config.boot.records(live)); err != nil {
		return fmt.Errorf("failed to load live store: %w", err)
	}
	config.boot = nil
	config.rebuildMCPHeaderAllowlist()
	return nil
}

// moveGovernanceContentToBoot moves the governance entity lists loadGovernanceConfig assembled
// into the boot struct, where every config.json content kind waits for the live store, leaving
// Config.GovernanceConfig its settings (auth, the complexity analyzer). From here on governance
// and the admin API read the live store and the database, never these lists, and loadLiveStore
// drops them with the boot struct.
func (c *Config) moveGovernanceContentToBoot() {
	g := c.GovernanceConfig
	if g == nil || c.boot == nil {
		return
	}
	c.boot.governance = &configstore.GovernanceConfig{
		VirtualKeys: g.VirtualKeys, Teams: g.Teams, Customers: g.Customers,
		Budgets: g.Budgets, RateLimits: g.RateLimits, ModelConfigs: g.ModelConfigs, Providers: g.Providers,
		RoutingRules: g.RoutingRules, PricingOverrides: g.PricingOverrides,
	}
	g.VirtualKeys, g.Teams, g.Customers = nil, nil, nil
	g.Budgets, g.RateLimits, g.ModelConfigs, g.Providers = nil, nil, nil, nil
	g.RoutingRules, g.PricingOverrides = nil, nil
}

// oauth2SigningKeyCache names the cache holding the OAuth2 signing key, and
// oauth2SigningKeyCacheKey is its one entry's key. The key is one config-store row, which by the
// live store's rule would be a collection; it is a cache only because the gateway has always read
// it on first use rather than at boot, and that is kept.
const (
	oauth2SigningKeyCache    = "oauth2_signing_key"
	oauth2SigningKeyCacheKey = "key"
)

// AttachLiveStore makes live the config's live store, creates the shared tables and the signing
// key cache on it, and keeps the views derived from it current: a provider's "name/model" prefix is
// registered when its record is published and unregistered when it is removed, and the MCP header
// allowlist is rebuilt whenever a client changes. A refresh calls its subscribers before it
// returns, so a created provider routes by its prefix once AddProvider returns, before any reload
// or model discovery, and a provider written elsewhere routes once its refresh returns. Provider
// keys are served from live.Views.ProviderKeyLists, which the store itself keeps current. Call it
// once per live store, before live loads, so the configured providers are registered at boot too; a
// test that builds its own live store calls it the same way. It fails when live is nil or already
// holds these tables.
func (c *Config) AttachLiveStore(live *livestore.Store) error {
	if live == nil {
		return fmt.Errorf("failed to attach live store: %w", livestore.ErrNoLiveStore)
	}
	shared, err := newSharedTables(context.Background(), live)
	if err != nil {
		return err
	}
	// The key never changes once created, so a load that started before a caller's miss returns the
	// same key: callers arriving during the first load share it rather than load again after it.
	signingKey, err := live.NewCache[*configstoreTables.OAuth2SigningKey](context.Background(), oauth2SigningKeyCache,
		livestore.WithSharedFills[*configstoreTables.OAuth2SigningKey]())
	if err != nil {
		return err
	}
	c.LiveStore = live
	c.Shared = shared
	c.oauth2SigningKey = signingKey
	live.Collections.Providers.Subscribe(func(_ context.Context, changes []livestore.Change[registry.Provider]) {
		for _, change := range changes {
			if change.New != nil {
				schemas.RegisterKnownProvider(change.New.Name)
			} else {
				schemas.UnregisterKnownProvider(schemas.ModelProvider(change.ID))
			}
		}
	})
	live.Collections.MCPClients.Subscribe(func(context.Context, []livestore.Change[registry.MCPClient]) {
		c.rebuildMCPHeaderAllowlist()
	})
	c.rebuildMCPHeaderAllowlist()
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
//  3. Never write a live-store collection: loadLiveStore is the one place collections are filled,
//     with LoadAll from the database when there is a config store, and otherwise with LoadRecords
//     from the records this struct builds (records). It then drops the struct, so nothing reads it
//     after boot. Boot steps that run before the load read their content here. Live-store tables
//     are filled by their owners instead (initFeatureFlags, loadDatasheetOverridesFromFile).
//
// It exists only while LoadConfig runs: a loader called outside it (a test, or a later reload)
// leaves it alone. Each field says what fills it and whether a gateway without a config store
// loads it. A kind skipped without a config store keeps its loader's early return and warning;
// loading it in that mode means adding it to records and dropping that early return.
type bootConfig struct {
	// providers is loadProviders' merged provider set. Loaded without a config store.
	providers map[schemas.ModelProvider]configstore.ProviderConfig
	// providerKeys is the keys of each provider in providers, in config.json order. Loaded
	// without a config store.
	providerKeys map[schemas.ModelProvider][]schemas.Key
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
	// vectorStoreSettings is each configured vector store's retention and storage limit from
	// config.json, set by loadVectorStoreSettings. Loaded without a config store; with one, the
	// database rows it synced are loaded instead.
	vectorStoreSettings []configstoreTables.TableVectorStoreSettings
}

// records builds the live store records a gateway without a config store loads, from b: the
// providers with their keys, the governance lists and the vector stores' settings. Every other content kind is skipped in that
// mode, as its loader has always skipped it: MCP clients and virtual MCPs (loadMCPConfig warns that
// the MCP manager will not be initialized), webhooks, agents, relays, model routers and routing
// rules. Datasheet overrides and feature flags are tables their owners fill in both modes. Loading
// a skipped kind in this mode means adding its records here and dropping its loader's early return.
func (b *bootConfig) records(live *livestore.Store) []livestore.Record {
	return live.Collections.FileRecords(registry.FromConfig(registry.FileInput{
		Providers: b.providers, ProviderKeys: b.providerKeys, Governance: b.governance, VectorStoreSettings: b.vectorStoreSettings,
	}))
}

// FileOnlyRecords wraps the records a gateway without a config store loads into live, as
// registry.FromConfig builds them from config.json: settings-only provider records, a record per
// provider key ordered by its position in config.json, and the governance records. Tests outside
// lib load them the same way through libtest.
func FileOnlyRecords(live *livestore.Store, providers map[schemas.ModelProvider]configstore.ProviderConfig, keys map[schemas.ModelProvider][]schemas.Key, declared *configstore.GovernanceConfig) []livestore.Record {
	return live.Collections.FileRecords(registry.FromConfig(registry.FileInput{Providers: providers, ProviderKeys: keys, Governance: declared}))
}

// uniqueKeyIDs fails when two keys of b's providers share a key ID, naming the ID and both
// providers. The live store holds one record per key ID, so file-only boot, with no database to
// refuse the second key, would otherwise keep one of them and drop the other without an error.
// Providers are visited by name, so the error names the same pair on every boot.
func (b *bootConfig) uniqueKeyIDs() error {
	owners := make(map[string]schemas.ModelProvider)
	for _, name := range slices.Sorted(maps.Keys(b.providerKeys)) {
		for _, key := range b.providerKeys[name] {
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
