package governance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// configDBTemplate is an empty config store database with every migration applied, built once per
// test binary: migrating takes most of a second under the race detector, and copying the file takes
// milliseconds.
var (
	configDBTemplateOnce sync.Once
	configDBTemplate     []byte
	configDBTemplateErr  error
)

// migratedConfigDB returns the bytes of an empty, migrated SQLite config store.
func migratedConfigDB() ([]byte, error) {
	configDBTemplateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "governance-configdb-")
		if err != nil {
			configDBTemplateErr = err
			return
		}
		defer os.RemoveAll(dir)
		ctx := context.Background()
		path := filepath.Join(dir, "config.db")
		store, err := openSQLiteConfigStore(ctx, path)
		if err != nil {
			configDBTemplateErr = err
			return
		}
		// Fold the write-ahead log into the file, so the copy holds every table.
		if err := store.DB().Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error; err != nil {
			configDBTemplateErr = err
			_ = store.Close(ctx)
			return
		}
		if err := store.Close(ctx); err != nil {
			configDBTemplateErr = err
			return
		}
		configDBTemplate, configDBTemplateErr = os.ReadFile(path)
	})
	return configDBTemplate, configDBTemplateErr
}

// openSQLiteConfigStore opens the SQLite config store at path, creating and migrating it when it
// is new.
func openSQLiteConfigStore(ctx context.Context, path string) (configstore.ConfigStore, error) {
	return configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: path},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
}

// newTestConfigStore returns an empty, migrated SQLite config store in a temporary directory, closed
// when the test ends.
func newTestConfigStore(t testing.TB) (configstore.ConfigStore, error) {
	t.Helper()
	template, err := migratedConfigDB()
	if err != nil {
		return nil, fmt.Errorf("migrate the template config store: %w", err)
	}
	path := filepath.Join(t.TempDir(), "config.db")
	if err := os.WriteFile(path, template, 0o600); err != nil {
		return nil, err
	}
	ctx := context.Background()
	store, err := openSQLiteConfigStore(ctx, path)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = store.Close(ctx) })
	return store, nil
}

// liveFixture is what a test's gateway is configured with besides governance: providers, MCP
// clients and agents. The harness writes the providers and clients as rows, so the live store
// holds them; the agents are the plugin's EnabledAgents.
type liveFixture struct {
	allowedByDefaultClients map[string]string // clientID -> clientName, open to every key
	clientNames             map[string]string // clientID -> clientName, configured clients not open by default
	clientSlugs             map[string]string // endpoint slug -> clientID
	configuredProviders     map[schemas.ModelProvider]configstore.ProviderConfig
	enabledAgents           map[string]bool // agent name -> allowed by default
}

// agents is the fixture's report of the enabled agents, nil when there is no fixture.
func (f *liveFixture) agents() func() map[string]bool {
	if f == nil {
		return nil
	}
	return func() map[string]bool { return f.enabledAgents }
}

// liveHarness is one test's configuration source: a SQLite config store and a live store loaded
// from it. A test changes configuration the way the gateway does: it writes the rows, then
// refreshes the records that read them.
type liveHarness struct {
	t           testing.TB
	ctx         context.Context
	configStore configstore.ConfigStore
	db          *gorm.DB
	live        *livestore.Store
}

// newLiveHarness writes cfg and fixture to a new SQLite config store and loads a live store from
// it, failing the test when either step fails.
func newLiveHarness(t testing.TB, cfg *configstore.GovernanceConfig, fixture *liveFixture) *liveHarness {
	t.Helper()
	h, err := openLiveHarness(t, nil, cfg, fixture)
	if err != nil {
		t.Fatalf("live harness: %v", err)
	}
	return h
}

// openLiveHarness writes cfg and fixture to configStore, a new SQLite config store when nil, and
// loads a live store from it.
func openLiveHarness(t testing.TB, configStore configstore.ConfigStore, cfg *configstore.GovernanceConfig, fixture *liveFixture) (*liveHarness, error) {
	t.Helper()
	if configStore == nil {
		var err error
		if configStore, err = newTestConfigStore(t); err != nil {
			return nil, err
		}
	}
	h := &liveHarness{t: t, ctx: context.Background(), configStore: configStore, db: configStore.DB()}
	if err := h.writeFixture(fixture); err != nil {
		return nil, fmt.Errorf("write fixture: %w", err)
	}
	if err := h.writeGovernance(cfg); err != nil {
		return nil, fmt.Errorf("write governance config: %w", err)
	}
	live, err := livestore.New()
	if err != nil {
		return nil, err
	}
	t.Cleanup(live.Close)
	if err := live.LoadAll(h.ctx, h.db); err != nil {
		return nil, err
	}
	h.live = live
	return h, nil
}

// newTestGovernanceStore is a governance store over a live store loaded from cfg and fixture. Usage
// is kept in memory only, as with no config store.
func newTestGovernanceStore(t testing.TB, logger schemas.Logger, cfg *configstore.GovernanceConfig, modelCatalog *modelcatalog.ModelCatalog, fixture *liveFixture) (*LocalGovernanceStore, error) {
	t.Helper()
	h, err := openLiveHarness(t, nil, cfg, fixture)
	if err != nil {
		return nil, err
	}
	return h.governanceStore(logger, modelCatalog)
}

// governanceStore builds a governance store over the harness's live store, closed when the test
// ends. Usage is kept in memory only, as with no config store.
func (h *liveHarness) governanceStore(logger schemas.Logger, modelCatalog *modelcatalog.ModelCatalog) (*LocalGovernanceStore, error) {
	store, err := NewLocalGovernanceStore(h.ctx, logger, nil, h.live, modelCatalog)
	if err != nil {
		return nil, err
	}
	h.t.Cleanup(store.Close)
	return store, nil
}

// mustGovernanceStore is governanceStore failing the test on an error.
func (h *liveHarness) mustGovernanceStore(logger schemas.Logger, modelCatalog *modelcatalog.ModelCatalog) *LocalGovernanceStore {
	h.t.Helper()
	store, err := h.governanceStore(logger, modelCatalog)
	if err != nil {
		h.t.Fatalf("governance store: %v", err)
	}
	return store
}

// refresh re-reads the named rows into the live store, failing the test on an error.
func (h *liveHarness) refresh(refs ...livestore.Ref) {
	h.t.Helper()
	if err := h.live.Refresh(h.ctx, refs); err != nil {
		h.t.Fatalf("refresh %v: %v", refs, err)
	}
}

// must fails the test when err is set.
func (h *liveHarness) must(err error, what string) {
	h.t.Helper()
	if err != nil {
		h.t.Fatalf("%s: %v", what, err)
	}
}

// putVirtualKey writes vk with its provider configs, MCP configs, grants, budgets and rate limits,
// replacing the key's child rows when it already exists, and refreshes the key.
func (h *liveHarness) putVirtualKey(vk *configstoreTables.TableVirtualKey) {
	h.t.Helper()
	h.must(h.writeVirtualKey(vk, nil, nil), "write virtual key "+vk.ID)
	h.refresh(livestore.Ref{Kind: livestore.KindVirtualKey, ID: vk.ID})
}

// deleteVirtualKey deletes a key as the gateway does, with its budgets, rate limits and key-scoped
// model configs, and removes its record.
func (h *liveHarness) deleteVirtualKey(id string) {
	h.t.Helper()
	h.must(h.configStore.DeleteVirtualKey(h.ctx, id), "delete virtual key "+id)
	h.refresh(livestore.Ref{Kind: livestore.KindVirtualKey, ID: id, Remove: true})
}

// putTeam writes team with its budgets and rate limit and refreshes it, which re-reads its keys.
func (h *liveHarness) putTeam(team *configstoreTables.TableTeam) {
	h.t.Helper()
	h.must(h.writeTeam(team, nil, nil), "write team "+team.ID)
	h.refresh(livestore.Ref{Kind: livestore.KindTeam, ID: team.ID})
}

// deleteTeam deletes a team, clearing its keys' reference to it, and removes its record.
func (h *liveHarness) deleteTeam(id string) {
	h.t.Helper()
	h.must(h.configStore.DeleteTeam(h.ctx, id), "delete team "+id)
	h.refresh(livestore.Ref{Kind: livestore.KindTeam, ID: id, Remove: true})
}

// putCustomer writes customer with its budgets and rate limit and refreshes it.
func (h *liveHarness) putCustomer(customer *configstoreTables.TableCustomer) {
	h.t.Helper()
	h.must(h.writeCustomer(customer, nil, nil), "write customer "+customer.ID)
	h.refresh(livestore.Ref{Kind: livestore.KindCustomer, ID: customer.ID})
}

// putModelConfig writes mc with its budgets and rate limit and refreshes it.
func (h *liveHarness) putModelConfig(mc *configstoreTables.TableModelConfig) {
	h.t.Helper()
	h.must(h.writeModelConfig(mc, nil, nil), "write model config "+mc.ID)
	h.refresh(livestore.Ref{Kind: livestore.KindModelConfig, ID: mc.ID})
}

// deleteModelConfig deletes a model config with its budgets and rate limit and removes its record.
func (h *liveHarness) deleteModelConfig(id string) {
	h.t.Helper()
	h.must(h.configStore.DeleteModelConfig(h.ctx, id), "delete model config "+id)
	h.refresh(livestore.Ref{Kind: livestore.KindModelConfig, ID: id, Remove: true})
}

// putProvider writes a provider's governance columns (and its budget and rate limit) and refreshes
// it.
func (h *liveHarness) putProvider(p *configstoreTables.TableProvider) {
	h.t.Helper()
	h.must(h.writeProvider(p, nil, nil), "write provider "+p.Name)
	h.refresh(livestore.Ref{Kind: livestore.KindProvider, ID: p.Name})
}

// setProviders writes providers as a providers config save does and refreshes their records, so
// the live store holds them with their keys. Keys without an ID or name get one (key IDs are
// unique across providers).
func (h *liveHarness) setProviders(providers map[schemas.ModelProvider]configstore.ProviderConfig) {
	h.t.Helper()
	h.must(h.configStore.UpdateProvidersConfig(h.ctx, withKeyIDs(providers)), "write providers")
	refs := make([]livestore.Ref, 0, len(providers))
	for name := range providers {
		refs = append(refs, livestore.Ref{Kind: livestore.KindProvider, ID: string(name)})
	}
	h.refresh(refs...)
}

// deleteProvider deletes a provider with its keys and removes its record.
func (h *liveHarness) deleteProvider(name schemas.ModelProvider) {
	h.t.Helper()
	h.must(h.configStore.DeleteProvider(h.ctx, name), "delete provider "+string(name))
	h.refresh(livestore.Ref{Kind: livestore.KindProvider, ID: string(name), Remove: true})
}

// withKeyIDs returns a copy of providers whose keys all carry an ID and a name, filling the
// missing ones from the provider's name and the key's position.
func withKeyIDs(providers map[schemas.ModelProvider]configstore.ProviderConfig) map[schemas.ModelProvider]configstore.ProviderConfig {
	out := make(map[schemas.ModelProvider]configstore.ProviderConfig, len(providers))
	for name, cfg := range providers {
		cfg.Keys = slices.Clone(cfg.Keys)
		for i := range cfg.Keys {
			if cfg.Keys[i].ID == "" {
				cfg.Keys[i].ID = fmt.Sprintf("%s-key-%d", name, i)
			}
			if cfg.Keys[i].Name == "" {
				cfg.Keys[i].Name = cfg.Keys[i].ID
			}
		}
		out[name] = cfg
	}
	return out
}

// putBudget writes a budget's row and refreshes it. The budget keeps the holder its columns name.
func (h *liveHarness) putBudget(b *configstoreTables.TableBudget) {
	h.t.Helper()
	h.must(h.saveBudget(*b), "write budget "+b.ID)
	h.refresh(livestore.Ref{Kind: livestore.KindBudget, ID: b.ID})
}

// putRateLimit writes a rate limit's row and refreshes it.
func (h *liveHarness) putRateLimit(rl *configstoreTables.TableRateLimit) {
	h.t.Helper()
	h.must(h.saveRateLimit(*rl), "write rate limit "+rl.ID)
	h.refresh(livestore.Ref{Kind: livestore.KindRateLimit, ID: rl.ID})
}

// putVirtualMCP creates a virtual MCP definition (under its ID when it names one) or updates the
// one with its ID, and refreshes it, returning its row ID.
func (h *liveHarness) putVirtualMCP(def *configstoreTables.TableVirtualMCP) uint {
	h.t.Helper()
	var n int64
	if def.ID != 0 {
		h.must(h.db.Model(&configstoreTables.TableVirtualMCP{}).Where("id = ?", def.ID).Count(&n).Error, "look up virtual MCP")
	}
	if n == 0 {
		h.must(h.configStore.CreateVirtualMCP(h.ctx, def), "create virtual MCP "+def.Name)
	} else {
		h.must(h.configStore.UpdateVirtualMCP(h.ctx, def), "update virtual MCP "+def.Name)
	}
	h.refresh(livestore.Ref{Kind: livestore.KindVirtualMCP, ID: registry.VirtualMCPKey(def.ID)})
	return def.ID
}

// deleteVirtualMCP deletes a virtual MCP definition, with its assignments, and removes its record
// and refreshes the keys it was assigned to.
func (h *liveHarness) deleteVirtualMCP(id uint) {
	h.t.Helper()
	keyIDs, err := h.configStore.GetVirtualKeyIDsForVirtualMCP(h.ctx, id)
	h.must(err, "read virtual MCP assignments")
	h.must(h.configStore.DeleteVirtualMCP(h.ctx, id), "delete virtual MCP")
	refs := []livestore.Ref{{Kind: livestore.KindVirtualMCP, ID: registry.VirtualMCPKey(id), Remove: true}}
	for _, keyID := range keyIDs {
		refs = append(refs, livestore.Ref{Kind: livestore.KindVirtualKey, ID: keyID})
	}
	h.refresh(refs...)
}

// attachVirtualMCP assigns a virtual MCP to a key, as the gateway does, and refreshes the key.
func (h *liveHarness) attachVirtualMCP(vkID string, id uint) {
	h.t.Helper()
	h.must(h.configStore.AttachVirtualMCPToVirtualKey(h.ctx, id, vkID), "attach virtual MCP")
	h.refresh(livestore.Ref{Kind: livestore.KindVirtualKey, ID: vkID})
}

// detachVirtualMCP removes a virtual MCP from a key, as the gateway does, and refreshes the key.
func (h *liveHarness) detachVirtualMCP(vkID string, id uint) {
	h.t.Helper()
	h.must(h.configStore.DetachVirtualMCPFromVirtualKey(h.ctx, id, vkID), "detach virtual MCP")
	h.refresh(livestore.Ref{Kind: livestore.KindVirtualKey, ID: vkID})
}

// setVirtualMCPAssignments replaces the virtual MCPs assigned to a key and refreshes the key.
func (h *liveHarness) setVirtualMCPAssignments(vkID string, ids ...uint) {
	h.t.Helper()
	h.must(h.db.Where("virtual_key_id = ?", vkID).Delete(&configstoreTables.TableVirtualKeyVirtualMCP{}).Error, "clear assignments")
	for _, id := range ids {
		h.must(h.db.Omit(clause.Associations).Create(&configstoreTables.TableVirtualKeyVirtualMCP{VirtualKeyID: vkID, VirtualMCPID: id}).Error, "assign virtual MCP")
	}
	h.refresh(livestore.Ref{Kind: livestore.KindVirtualKey, ID: vkID})
}

// writeFixture writes the fixture's providers and MCP clients.
func (h *liveHarness) writeFixture(f *liveFixture) error {
	if f == nil {
		return nil
	}
	if len(f.configuredProviders) > 0 {
		if err := h.configStore.UpdateProvidersConfig(h.ctx, withKeyIDs(f.configuredProviders)); err != nil {
			return err
		}
	}
	slugs := make(map[string]string, len(f.clientSlugs))
	ids := make([]string, 0, len(f.allowedByDefaultClients)+len(f.clientNames)+len(f.clientSlugs))
	for slug, id := range f.clientSlugs {
		slugs[id] = slug
		ids = append(ids, id)
	}
	for id := range f.allowedByDefaultClients {
		ids = append(ids, id)
	}
	for id := range f.clientNames {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		name, open := f.allowedByDefaultClients[id]
		if !open {
			name = f.clientNames[id]
		}
		if _, err := h.ensureMCPClient(id, name, slugs[id], open); err != nil {
			return err
		}
	}
	return nil
}

// ensureMCPClient returns the row ID of the MCP client with this client ID, creating it with name,
// slug and allow-by-default flag when it does not exist. A client with no name is named by its ID.
func (h *liveHarness) ensureMCPClient(clientID, name, slug string, allowByDefault bool) (uint, error) {
	var row configstoreTables.TableMCPClient
	err := h.db.Select("id").Where("client_id = ?", clientID).First(&row).Error
	if err == nil {
		return row.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	if name == "" {
		name = clientID
	}
	if err := h.configStore.CreateMCPClientConfig(h.ctx, &schemas.MCPClientConfig{
		ID: clientID, Name: name, EndpointSlug: slug, AllowByDefault: allowByDefault,
		ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar("https://" + clientID + ".example/mcp"),
		AuthType: schemas.MCPAuthTypeNone, ToolsToExecute: schemas.WhiteList{"*"},
	}); err != nil {
		return 0, err
	}
	if err := h.db.Select("id").Where("client_id = ?", clientID).First(&row).Error; err != nil {
		return 0, err
	}
	return row.ID, nil
}

// writeGovernance writes every row cfg declares. A listed budget or rate limit wins over a nested
// copy with the same ID; a nested one is held by what it is nested under, and a listed one nothing
// nests by the holder its columns name.
func (h *liveHarness) writeGovernance(cfg *configstore.GovernanceConfig) error {
	if cfg == nil {
		return nil
	}
	listedBudgets := make(map[string]configstoreTables.TableBudget, len(cfg.Budgets))
	for _, b := range cfg.Budgets {
		listedBudgets[b.ID] = b
	}
	listedRateLimits := make(map[string]configstoreTables.TableRateLimit, len(cfg.RateLimits))
	for _, rl := range cfg.RateLimits {
		listedRateLimits[rl.ID] = rl
		if err := h.saveRateLimit(rl); err != nil {
			return err
		}
	}
	nested := make(map[string]bool)
	markNested := func(budgets []configstoreTables.TableBudget) {
		for _, b := range budgets {
			nested[b.ID] = true
		}
	}
	for i := range cfg.Customers {
		markNested(cfg.Customers[i].Budgets)
	}
	for i := range cfg.Teams {
		markNested(cfg.Teams[i].Budgets)
	}
	for i := range cfg.VirtualKeys {
		markNested(cfg.VirtualKeys[i].Budgets)
		for j := range cfg.VirtualKeys[i].ProviderConfigs {
			markNested(cfg.VirtualKeys[i].ProviderConfigs[j].Budgets)
		}
	}
	for i := range cfg.ModelConfigs {
		markNested(cfg.ModelConfigs[i].Budgets)
	}
	// Budgets with no holder (a provider's, or listed ones naming none) go first, since providers
	// and legacy model config columns reference them.
	var heldListed []configstoreTables.TableBudget
	for _, b := range cfg.Budgets {
		if nested[b.ID] {
			continue
		}
		if budgetHolderCount(&b) == 0 {
			if err := h.saveBudget(b); err != nil {
				return err
			}
			continue
		}
		heldListed = append(heldListed, b)
	}
	for i := range cfg.Customers {
		if err := h.writeCustomer(&cfg.Customers[i], listedBudgets, listedRateLimits); err != nil {
			return err
		}
	}
	for i := range cfg.Teams {
		if err := h.writeTeam(&cfg.Teams[i], listedBudgets, listedRateLimits); err != nil {
			return err
		}
	}
	for i := range cfg.Providers {
		if err := h.writeProvider(&cfg.Providers[i], listedBudgets, listedRateLimits); err != nil {
			return err
		}
	}
	for i := range cfg.VirtualKeys {
		if err := h.writeVirtualKey(&cfg.VirtualKeys[i], listedBudgets, listedRateLimits); err != nil {
			return err
		}
	}
	for i := range cfg.ModelConfigs {
		if err := h.writeModelConfig(&cfg.ModelConfigs[i], listedBudgets, listedRateLimits); err != nil {
			return err
		}
	}
	for _, b := range heldListed {
		if err := h.saveBudget(b); err != nil {
			return err
		}
	}
	return nil
}

// writeRateLimitOf writes the rate limit a holder names, the listed copy when there is one, and
// returns its ID for the holder's column ("" when it names none).
func (h *liveHarness) writeRateLimitOf(id *string, nested *configstoreTables.TableRateLimit, listed map[string]configstoreTables.TableRateLimit) (*string, error) {
	switch {
	case nested != nil && nested.ID != "":
		if _, ok := listed[nested.ID]; !ok {
			if err := h.saveRateLimit(*nested); err != nil {
				return nil, err
			}
		}
		rlID := nested.ID
		return &rlID, nil
	case id != nil && *id != "":
		// A holder naming a rate limit no fixture spells out gets an empty row under that ID, the
		// least the reference needs to be stored.
		var n int64
		if err := h.db.Model(&configstoreTables.TableRateLimit{}).Where("id = ?", *id).Count(&n).Error; err != nil {
			return nil, err
		}
		if _, ok := listed[*id]; !ok && n == 0 {
			if err := h.saveRateLimit(configstoreTables.TableRateLimit{ID: *id}); err != nil {
				return nil, err
			}
		}
		return id, nil
	}
	return nil, nil
}

// writeHeldBudgets writes the budgets nested under one holder, each the listed copy when there is
// one, with set naming the holder on the row.
func (h *liveHarness) writeHeldBudgets(budgets []configstoreTables.TableBudget, listed map[string]configstoreTables.TableBudget, set func(*configstoreTables.TableBudget)) error {
	for _, b := range budgets {
		if l, ok := listed[b.ID]; ok {
			b = l
		}
		clearBudgetHolders(&b)
		set(&b)
		if err := h.saveBudget(b); err != nil {
			return err
		}
	}
	return nil
}

// writeCustomer writes a customer with its rate limit and budgets.
func (h *liveHarness) writeCustomer(c *configstoreTables.TableCustomer, listedBudgets map[string]configstoreTables.TableBudget, listedRateLimits map[string]configstoreTables.TableRateLimit) error {
	row := *c
	rlID, err := h.writeRateLimitOf(c.RateLimitID, c.RateLimit, listedRateLimits)
	if err != nil {
		return err
	}
	row.RateLimitID = rlID
	if err := h.upsertByID(&row, row.ID); err != nil {
		return err
	}
	if err := h.db.Where("customer_id = ?", c.ID).Delete(&configstoreTables.TableBudget{}).Error; err != nil {
		return err
	}
	id := c.ID
	return h.writeHeldBudgets(c.Budgets, listedBudgets, func(b *configstoreTables.TableBudget) { b.CustomerID = &id })
}

// writeTeam writes a team with its rate limit and budgets.
func (h *liveHarness) writeTeam(t *configstoreTables.TableTeam, listedBudgets map[string]configstoreTables.TableBudget, listedRateLimits map[string]configstoreTables.TableRateLimit) error {
	row := *t
	rlID, err := h.writeRateLimitOf(t.RateLimitID, t.RateLimit, listedRateLimits)
	if err != nil {
		return err
	}
	row.RateLimitID = rlID
	if err := h.upsertByID(&row, row.ID); err != nil {
		return err
	}
	if err := h.db.Where("team_id = ?", t.ID).Delete(&configstoreTables.TableBudget{}).Error; err != nil {
		return err
	}
	id := t.ID
	return h.writeHeldBudgets(t.Budgets, listedBudgets, func(b *configstoreTables.TableBudget) { b.TeamID = &id })
}

// writeModelConfig writes a model config with its rate limit and budgets.
func (h *liveHarness) writeModelConfig(mc *configstoreTables.TableModelConfig, listedBudgets map[string]configstoreTables.TableBudget, listedRateLimits map[string]configstoreTables.TableRateLimit) error {
	row := *mc
	rlID, err := h.writeRateLimitOf(mc.RateLimitID, mc.RateLimit, listedRateLimits)
	if err != nil {
		return err
	}
	row.RateLimitID = rlID
	// A scope another build registers is refused by the row's validation here, so the row is
	// written under a known scope and then given the fixture's.
	foreignScope := row.Scope != "" && !configstoreTables.IsValidModelConfigScope(row.Scope)
	if foreignScope {
		placeholder := "scope-placeholder-" + row.ID
		row.Scope, row.ScopeID = configstoreTables.ModelConfigScopeVirtualKey, &placeholder
	}
	if err := h.upsertByID(&row, row.ID); err != nil {
		return err
	}
	if foreignScope {
		if err := h.db.Model(&configstoreTables.TableModelConfig{}).Where("id = ?", mc.ID).
			UpdateColumns(map[string]any{"scope": mc.Scope, "scope_id": mc.ScopeID}).Error; err != nil {
			return err
		}
	}
	if err := h.db.Where("model_config_id = ?", mc.ID).Delete(&configstoreTables.TableBudget{}).Error; err != nil {
		return err
	}
	id := mc.ID
	return h.writeHeldBudgets(mc.Budgets, listedBudgets, func(b *configstoreTables.TableBudget) { b.ModelConfigID = &id })
}

// writeProvider writes a provider's governance columns, creating the provider row when there is
// none, with its budget and rate limit.
func (h *liveHarness) writeProvider(p *configstoreTables.TableProvider, listedBudgets map[string]configstoreTables.TableBudget, listedRateLimits map[string]configstoreTables.TableRateLimit) error {
	rlID, err := h.writeRateLimitOf(p.RateLimitID, p.RateLimit, listedRateLimits)
	if err != nil {
		return err
	}
	budgetID := p.BudgetID
	if p.Budget != nil && p.Budget.ID != "" {
		if _, listed := listedBudgets[p.Budget.ID]; !listed {
			b := *p.Budget
			clearBudgetHolders(&b)
			if err := h.saveBudget(b); err != nil {
				return err
			}
		}
		id := p.Budget.ID
		budgetID = &id
	}
	var existing configstoreTables.TableProvider
	err = h.db.Select("id").Where("name = ?", p.Name).First(&existing).Error
	switch {
	case err == nil:
		return h.db.Model(&configstoreTables.TableProvider{}).Where("name = ?", p.Name).
			UpdateColumns(map[string]any{"budget_id": budgetID, "rate_limit_id": rlID}).Error
	case errors.Is(err, gorm.ErrRecordNotFound):
		row := configstoreTables.TableProvider{Name: p.Name, BudgetID: budgetID, RateLimitID: rlID, CustomProviderConfig: p.CustomProviderConfig}
		return h.db.Omit(clause.Associations).Create(&row).Error
	default:
		return err
	}
}

// writeVirtualKey writes a key and its child rows: provider configs with their keys, budgets and
// rate limits, MCP configs (creating the MCP clients they name), agent and relay grants (creating
// the agents and relays they name), and its own budgets and rate limit. A key that exists has its
// child rows replaced.
func (h *liveHarness) writeVirtualKey(vk *configstoreTables.TableVirtualKey, listedBudgets map[string]configstoreTables.TableBudget, listedRateLimits map[string]configstoreTables.TableRateLimit) error {
	if err := h.clearVirtualKeyChildren(vk.ID); err != nil {
		return err
	}
	row := *vk
	rlID, err := h.writeRateLimitOf(vk.RateLimitID, vk.RateLimit, listedRateLimits)
	if err != nil {
		return err
	}
	row.RateLimitID = rlID
	// A key naming both a team and a customer (or a business unit) is refused on save today, so
	// the row is written under its team and then given the fixture's other owners, the way an
	// older row can still hold them.
	extraOwners := row.TeamID != nil && (row.CustomerID != nil || row.BusinessUnitID != nil)
	if extraOwners {
		row.CustomerID, row.BusinessUnitID = nil, nil
	}
	if err := h.upsertByID(&row, row.ID); err != nil {
		return err
	}
	if extraOwners {
		if err := h.db.Model(&configstoreTables.TableVirtualKey{}).Where("id = ?", vk.ID).
			UpdateColumns(map[string]any{"customer_id": vk.CustomerID, "business_unit_id": vk.BusinessUnitID}).Error; err != nil {
			return err
		}
	}
	for i := range vk.ProviderConfigs {
		pc := vk.ProviderConfigs[i]
		pcRow := pc
		pcRow.VirtualKeyID = vk.ID
		if pcRow.RateLimitID, err = h.writeRateLimitOf(pc.RateLimitID, pc.RateLimit, listedRateLimits); err != nil {
			return err
		}
		if err := h.db.Omit(clause.Associations).Create(&pcRow).Error; err != nil {
			return err
		}
		for _, k := range pc.Keys {
			keyRowID, err := h.ensureProviderKey(pc.Provider, k)
			if err != nil {
				return err
			}
			if err := h.db.Create(&configstoreTables.TableVirtualKeyProviderConfigKey{TableVirtualKeyProviderConfigID: pcRow.ID, TableKeyID: keyRowID}).Error; err != nil {
				return err
			}
		}
		configID := pcRow.ID
		if err := h.writeHeldBudgets(pc.Budgets, listedBudgets, func(b *configstoreTables.TableBudget) { b.ProviderConfigID = &configID }); err != nil {
			return err
		}
	}
	for _, mc := range vk.MCPConfigs {
		clientRowID := mc.MCPClientID
		if mc.MCPClient.ClientID != "" {
			if clientRowID, err = h.ensureMCPClient(mc.MCPClient.ClientID, mc.MCPClient.Name, mc.MCPClient.EndpointSlug, mc.MCPClient.AllowByDefault); err != nil {
				return err
			}
		}
		if err := h.db.Omit(clause.Associations).Create(&configstoreTables.TableVirtualKeyMCPConfig{
			VirtualKeyID: vk.ID, MCPClientID: clientRowID, ToolsToExecute: mc.ToolsToExecute,
		}).Error; err != nil {
			return err
		}
	}
	for _, g := range vk.AgentGrants {
		if err := h.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&configstoreTables.TableAgentRegistration{Name: g.AgentName, AgentCardURL: "https://" + g.AgentName + ".example/card", Enabled: true}).Error; err != nil {
			return err
		}
		if err := h.db.Omit(clause.Associations).Create(&configstoreTables.TableVirtualKeyAgentGrant{VirtualKeyID: vk.ID, AgentName: g.AgentName}).Error; err != nil {
			return err
		}
	}
	for _, g := range vk.RelayGrants {
		if err := h.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&configstoreTables.TableRelay{Name: g.RelayName, ConfigJSON: "{}"}).Error; err != nil {
			return err
		}
		if err := h.db.Omit(clause.Associations).Create(&configstoreTables.TableVirtualKeyRelayGrant{VirtualKeyID: vk.ID, RelayName: g.RelayName}).Error; err != nil {
			return err
		}
	}
	id := vk.ID
	return h.writeHeldBudgets(vk.Budgets, listedBudgets, func(b *configstoreTables.TableBudget) { b.VirtualKeyID = &id })
}

// clearVirtualKeyChildren deletes a key's provider configs (with their key links and budgets), MCP
// configs, grants and own budgets, so writeVirtualKey can write them afresh.
func (h *liveHarness) clearVirtualKeyChildren(id string) error {
	var configIDs []uint
	if err := h.db.Model(&configstoreTables.TableVirtualKeyProviderConfig{}).Where("virtual_key_id = ?", id).Pluck("id", &configIDs).Error; err != nil {
		return err
	}
	if len(configIDs) > 0 {
		if err := h.db.Where("table_virtual_key_provider_config_id IN ?", configIDs).Delete(&configstoreTables.TableVirtualKeyProviderConfigKey{}).Error; err != nil {
			return err
		}
		if err := h.db.Where("provider_config_id IN ?", configIDs).Delete(&configstoreTables.TableBudget{}).Error; err != nil {
			return err
		}
	}
	for _, model := range []any{
		&configstoreTables.TableVirtualKeyProviderConfig{}, &configstoreTables.TableVirtualKeyMCPConfig{},
		&configstoreTables.TableVirtualKeyAgentGrant{}, &configstoreTables.TableVirtualKeyRelayGrant{}, &configstoreTables.TableBudget{},
	} {
		if err := h.db.Where("virtual_key_id = ?", id).Delete(model).Error; err != nil {
			return err
		}
	}
	return nil
}

// ensureProviderKey returns the row ID of the provider key with k's key ID, creating it (and its
// provider row) when it does not exist.
func (h *liveHarness) ensureProviderKey(provider string, k configstoreTables.TableKey) (uint, error) {
	var row configstoreTables.TableKey
	err := h.db.Select("id").Where("key_id = ?", k.KeyID).First(&row).Error
	if err == nil {
		return row.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	var p configstoreTables.TableProvider
	err = h.db.Select("id").Where("name = ?", provider).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		p = configstoreTables.TableProvider{Name: provider}
		err = h.db.Omit(clause.Associations).Create(&p).Error
	}
	if err != nil {
		return 0, err
	}
	name := k.Name
	if name == "" {
		name = k.KeyID
	}
	key := configstoreTables.TableKey{
		Name: name, KeyID: k.KeyID, ProviderID: p.ID, Provider: provider, Value: *schemas.NewSecretVar("sk-" + k.KeyID),
		ModelsJSON: k.ModelsJSON,
	}
	if err := h.db.Omit(clause.Associations).Create(&key).Error; err != nil {
		return 0, err
	}
	return key.ID, nil
}

// saveBudget writes a budget's row, keeping the creation time the fixture gave it: a rolling
// budget's windows are anchored on it, and a zero one anchors them on the last reset.
// A fixture with no reset duration, which the row's validation refuses, is written with a
// placeholder that is then cleared, so the row holds what the fixture says.
func (h *liveHarness) saveBudget(b configstoreTables.TableBudget) error {
	createdAt, resetDuration := b.CreatedAt, b.ResetDuration
	if resetDuration == "" {
		b.ResetDuration = "1h"
	}
	if err := h.upsertByID(&b, b.ID); err != nil {
		return err
	}
	return h.db.Model(&configstoreTables.TableBudget{}).Where("id = ?", b.ID).
		UpdateColumns(map[string]any{"created_at": createdAt, "reset_duration": resetDuration}).Error
}

// saveRateLimit writes a rate limit's row, keeping its creation time as saveBudget does. Limits the
// row's validation refuses (a zero maximum, a maximum with no window) are written with placeholders
// that are then replaced by the fixture's values, so the row holds what the fixture says.
func (h *liveHarness) saveRateLimit(rl configstoreTables.TableRateLimit) error {
	exact := map[string]any{
		"created_at":             rl.CreatedAt,
		"token_max_limit":        rl.TokenMaxLimit,
		"token_reset_duration":   rl.TokenResetDuration,
		"request_max_limit":      rl.RequestMaxLimit,
		"request_reset_duration": rl.RequestResetDuration,
	}
	one, window := int64(1), "1m"
	if rl.TokenMaxLimit != nil {
		if *rl.TokenMaxLimit <= 0 {
			rl.TokenMaxLimit = &one
		}
		if rl.TokenResetDuration == nil {
			rl.TokenResetDuration = &window
		}
	}
	if rl.RequestMaxLimit != nil {
		if *rl.RequestMaxLimit <= 0 {
			rl.RequestMaxLimit = &one
		}
		if rl.RequestResetDuration == nil {
			rl.RequestResetDuration = &window
		}
	}
	if err := h.upsertByID(&rl, rl.ID); err != nil {
		return err
	}
	return h.db.Model(&configstoreTables.TableRateLimit{}).Where("id = ?", rl.ID).UpdateColumns(exact).Error
}

// upsertByID creates row, or updates every column of the row with this ID when it exists, without
// touching associations. Hooks run either way.
func (h *liveHarness) upsertByID(row any, id string) error {
	var n int64
	if err := h.db.Model(row).Where("id = ?", id).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return h.db.Omit(clause.Associations).Create(row).Error
	}
	return h.db.Model(row).Select("*").Omit(clause.Associations).Updates(row).Error
}

// budgetHolderCount is how many holder columns a budget row sets.
func budgetHolderCount(b *configstoreTables.TableBudget) int {
	n := 0
	for _, p := range []*string{b.VirtualKeyID, b.TeamID, b.CustomerID, b.ModelConfigID, b.BusinessUnitID} {
		if p != nil {
			n++
		}
	}
	if b.ProviderConfigID != nil {
		n++
	}
	return n
}

// clearBudgetHolders clears every holder column of a budget row.
func clearBudgetHolders(b *configstoreTables.TableBudget) {
	b.VirtualKeyID, b.TeamID, b.CustomerID, b.ModelConfigID, b.BusinessUnitID, b.ProviderConfigID = nil, nil, nil, nil, nil, nil
}

// virtualKeyRecord builds the record the live store would hold for vk from its table struct alone,
// for tests that hand a key straight to a function rather than resolving it from the store. Budget
// and virtual MCP IDs are taken from vk's nested budgets.
func virtualKeyRecord(vk *configstoreTables.TableVirtualKey) *registry.VirtualKey {
	if vk == nil {
		return nil
	}
	clone := *vk
	clone.ProviderConfigs = slices.Clone(vk.ProviderConfigs)
	budgetIDs := make([]string, 0, len(vk.Budgets))
	for _, b := range vk.Budgets {
		budgetIDs = append(budgetIDs, b.ID)
	}
	rec := registry.VirtualKeyFromTable(&clone, budgetIDs, nil)
	if rec.RateLimitID == "" && vk.RateLimit != nil {
		rec.RateLimitID = vk.RateLimit.ID
	}
	return rec
}

// fixtureStore is a governance store over a live store holding fixture's providers and MCP clients
// and no governance rows, for tests that hand keys to the store directly.
func fixtureStore(t testing.TB, fixture *liveFixture) *LocalGovernanceStore {
	t.Helper()
	return newLiveHarness(t, &configstore.GovernanceConfig{}, fixture).mustGovernanceStore(NewMockLogger(), nil)
}

// storedVirtualKey returns the record store holds for the key with this ID, failing the test when
// there is none.
func storedVirtualKey(t testing.TB, store *LocalGovernanceStore, id string) *registry.VirtualKey {
	t.Helper()
	rec, ok := store.live.Collections.VirtualKeys.Get(id)
	if !ok {
		t.Fatalf("virtual key %s is not in the live store", id)
	}
	return rec
}
