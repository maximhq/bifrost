package livestore

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/livestore/registry"
)

const (
	// KindProvider names the provider collection.
	KindProvider Kind = "provider"
	// KindProviderKey names the provider key collection, whose records are children of providers.
	KindProviderKey Kind = "provider_key"
)

// providerKeyListsTable names the table behind Views.ProviderKeyLists. It is in the store's
// internal range, so no component can create or drop a table under it.
const providerKeyListsTable = internalPrefix + "provider_key_lists"

// KindMCPClient names the MCP client collection.
const KindMCPClient Kind = "mcp_client"

// Governance kinds: virtual keys, the teams and customers they sit under, the budgets and rate
// limits those hold, model configs, and virtual MCPs.
const (
	KindVirtualKey  Kind = "virtual_key"
	KindTeam        Kind = "team"
	KindCustomer    Kind = "customer"
	KindBudget      Kind = "budget"
	KindRateLimit   Kind = "rate_limit"
	KindModelConfig Kind = "model_config"
	KindVirtualMCP  Kind = "virtual_mcp"
)

// Collections holds the typed collection of each config store table the live store loads. New
// registers every one of them, so a reader takes them from Store.Collections. The record types and
// the reads that load them are in the registry package.
type Collections struct {
	Providers    *Collection[registry.Provider]    // provider settings; their keys are ProviderKeys
	ProviderKeys *Collection[registry.ProviderKey] // one record per key, children of Providers
	MCPClients   *Collection[registry.MCPClient]
	VirtualKeys  *Collection[registry.VirtualKey]
	Teams        *Collection[registry.Team]
	Customers    *Collection[registry.Customer]
	Budgets      *Collection[registry.Budget]
	RateLimits   *Collection[registry.RateLimit]
	ModelConfigs *Collection[registry.ModelConfig]
	VirtualMCPs  *Collection[registry.VirtualMCP]
}

// Views holds the global derived tables: tables derived only from collections and read by more
// than one component across packages. New creates each one and keeps it current with a subscriber
// on the collections it is derived from, so a reader takes it from Store.Views and never writes
// it. A table one component derives or writes for itself stays with that component.
type Views struct {
	// ProviderKeyLists holds each provider's keys in creation order, keyed by provider name: the
	// keys the request path serves. It is state of KindProvider, so removing a provider drops its
	// entry, and a ProviderKeys subscriber rebuilds a provider's whole list once per publish that
	// changes its keys; a ProviderKeys subscriber registered after New runs after that rebuild. A
	// provider with no keys has no entry. The slice is shared by every reader and must not be
	// changed. Get allocates nothing.
	ProviderKeyLists *Table[string, *[]schemas.Key]
}

// New returns a store with a collection registered for each config store table and the views
// derived from them. It loads nothing: LoadAll fills the collections from the database, or
// LoadRecords from config.json when there is none, and the views follow every publish, the load
// included. A build that adds collections registers them on the returned store before either runs.
func New(opts ...StoreOption) (*Store, error) {
	var o storeOptions
	for _, opt := range opts {
		opt(&o)
	}
	s := newStore()
	cols, err := registerCollections(s, &o)
	if err != nil {
		return nil, err
	}
	s.Collections = cols
	lists := newTable[string, *[]schemas.Key](providerKeyListsTable, tableOptions{})
	if err := s.addTable(providerKeyListsTable, lists, KindProvider); err != nil {
		return nil, err
	}
	s.Views = &Views{ProviderKeyLists: lists}
	cols.ProviderKeys.Subscribe(s.rebuildProviderKeyLists)
	return s, nil
}

// StoreOption configures the store New returns.
type StoreOption func(*storeOptions)

// storeOptions holds what the StoreOptions given to New set.
type storeOptions struct{}

// registerCollections registers the collection of each config store table on s, with the
// registry's reads as its loader.
func registerCollections(s *Store, o *storeOptions) (*Collections, error) {
	c := &Collections{}
	var err error
	if c.Providers, err = s.Register(KindProvider, []Kind{KindBudget, KindRateLimit}, Loader[registry.Provider]{
		ID:   registry.ProviderName,
		Page: registry.ProvidersPage,
		ByID: registry.ProvidersByName,
	}); err != nil {
		return nil, err
	}
	if c.ProviderKeys, err = s.Register(KindProviderKey, []Kind{KindProvider}, Loader[registry.ProviderKey]{
		ID:   registry.ProviderKeyID,
		Page: registry.ProviderKeysPage,
		ByID: registry.ProviderKeysByID,
	}, ChildOf(KindProvider, registry.ProviderKeyProvider, registry.ProviderKeysByProviders, Cascade)); err != nil {
		return nil, err
	}
	if c.MCPClients, err = s.Register(KindMCPClient, nil, Loader[registry.MCPClient]{
		ID:   registry.MCPClientID,
		Page: registry.MCPClientsPage,
		ByID: registry.MCPClientsByID,
	},
		UniqueIndex(registry.MCPClientByName, registry.MCPClientName),
		UniqueIndex(registry.MCPClientBySlug, registry.MCPClientSlug),
	); err != nil {
		return nil, err
	}
	if err := registerGovernance(s, c, o); err != nil {
		return nil, err
	}
	return c, nil
}

// rebuildProviderKeyLists is the ProviderKeys subscriber behind Views.ProviderKeyLists. For every
// provider a publish touched, it rebuilds the provider's list from the collection, which already
// holds the publish's key records, sorts it by Order and then key ID, and stores it whole, so a
// publish that changes several keys swaps the list once; a provider with no keys left loses its
// entry. Publishes run one at a time, so two rebuilds never race.
func (s *Store) rebuildProviderKeyLists(_ context.Context, changes []Change[registry.ProviderKey]) {
	var providers []schemas.ModelProvider
	for _, ch := range changes {
		for _, rec := range []*registry.ProviderKey{ch.Old, ch.New} {
			if rec != nil && !slices.Contains(providers, rec.Provider) {
				providers = append(providers, rec.Provider)
			}
		}
	}
	for _, provider := range providers {
		// ChildOf names the parent index "parent:" plus the parent kind; the engine exports no
		// helper for the name yet.
		recs := s.Collections.ProviderKeys.List("parent:"+string(KindProvider), string(provider))
		if len(recs) == 0 {
			s.Views.ProviderKeyLists.Delete(string(provider))
			continue
		}
		slices.SortFunc(recs, func(a, b *registry.ProviderKey) int {
			return cmp.Or(cmp.Compare(a.Order, b.Order), strings.Compare(a.Key.ID, b.Key.ID))
		})
		keys := make([]schemas.Key, len(recs))
		for i, rec := range recs {
			keys[i] = rec.Key
		}
		s.Views.ProviderKeyLists.Put(string(provider), &keys)
	}
}

// registerGovernance registers the governance collections on s. A limit follows its holder: a
// refresh of a key, team, customer, model config or provider re-reads the budgets and rate limits
// it holds, and removing the holder removes them. Keys and teams keep their records when the
// team or customer above them is removed, re-read with the reference cleared, and key-scoped
// model configs go with their key.
func registerGovernance(s *Store, c *Collections, o *storeOptions) error {
	var err error
	if c.Customers, err = s.Register(KindCustomer, []Kind{KindBudget, KindRateLimit}, Loader[registry.Customer]{
		ID:   registry.CustomerID,
		Page: registry.CustomersPage,
		ByID: registry.CustomersByID,
	}); err != nil {
		return err
	}
	if c.Teams, err = s.Register(KindTeam, []Kind{KindBudget, KindRateLimit, KindCustomer}, Loader[registry.Team]{
		ID:   registry.TeamID,
		Page: registry.TeamsPage,
		ByID: registry.TeamsByID,
	},
		ChildOf(KindCustomer, registry.TeamCustomerID, registry.TeamsByCustomers, SetNull),
	); err != nil {
		return err
	}
	if c.VirtualKeys, err = s.Register(KindVirtualKey, []Kind{KindBudget, KindRateLimit, KindTeam, KindCustomer, KindVirtualMCP}, Loader[registry.VirtualKey]{
		ID:   registry.VirtualKeyID,
		Page: registry.VirtualKeysPage,
		ByID: registry.VirtualKeysByID,
	},
		UniqueIndex(registry.VirtualKeyByValue, registry.VirtualKeyValue),
		MultiIndex(registry.VirtualKeyByPreviousValue, registry.VirtualKeyPreviousValue),
		ChildOf(KindTeam, registry.VirtualKeyTeamID, registry.VirtualKeysByTeams, SetNull),
		ChildOf(KindCustomer, registry.VirtualKeyCustomerID, registry.VirtualKeysByCustomers, SetNull),
	); err != nil {
		return err
	}
	if c.ModelConfigs, err = s.Register(KindModelConfig, []Kind{KindBudget, KindRateLimit}, Loader[registry.ModelConfig]{
		ID:   registry.ModelConfigID,
		Page: registry.ModelConfigsPage,
		ByID: registry.ModelConfigsByID,
	},
		MultiIndex(registry.ModelConfigByScope, func(m *registry.ModelConfig) []string { return []string{registry.ModelConfigScope(m)} }),
		ChildOf(KindVirtualKey, registry.ModelConfigKeyID, registry.ModelConfigsByKeys, Cascade),
	); err != nil {
		return err
	}
	if c.Budgets, err = s.Register(KindBudget, nil, Loader[registry.Budget]{
		ID:   registry.BudgetID,
		Page: registry.BudgetsPage,
		ByID: registry.BudgetsByID,
	},
		ChildOf(KindVirtualKey, registry.BudgetKeyID, registry.BudgetsByKeys, Cascade),
		ChildOf(KindTeam, registry.BudgetTeamID, registry.BudgetsByTeams, Cascade),
		ChildOf(KindCustomer, registry.BudgetCustomerID, registry.BudgetsByCustomers, Cascade),
		ChildOf(KindModelConfig, registry.BudgetModelConfigID, registry.BudgetsByModelConfigs, Cascade),
		ChildOf(KindProvider, registry.BudgetProvider, registry.BudgetsByProviders, Cascade),
	); err != nil {
		return err
	}
	if c.RateLimits, err = s.Register(KindRateLimit, nil, Loader[registry.RateLimit]{
		ID:   registry.RateLimitID,
		Page: registry.RateLimitsPage,
		ByID: registry.RateLimitsByID,
	},
		ChildOf(KindVirtualKey, registry.RateLimitKeyID, registry.RateLimitsByKeys, Cascade),
		ChildOf(KindTeam, registry.RateLimitTeamID, registry.RateLimitsByTeams, Cascade),
		ChildOf(KindCustomer, registry.RateLimitCustomerID, registry.RateLimitsByCustomers, Cascade),
		ChildOf(KindModelConfig, registry.RateLimitModelConfigID, registry.RateLimitsByModelConfigs, Cascade),
		ChildOf(KindProvider, registry.RateLimitProvider, registry.RateLimitsByProviders, Cascade),
	); err != nil {
		return err
	}
	if c.VirtualMCPs, err = s.Register(KindVirtualMCP, nil, Loader[registry.VirtualMCP]{
		ID:   registry.VirtualMCPID,
		Page: registry.VirtualMCPsPage,
		ByID: registry.VirtualMCPsByID,
	}); err != nil {
		return err
	}
	return nil
}
