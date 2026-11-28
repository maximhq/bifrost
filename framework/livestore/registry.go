package livestore

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// KindMCPUserDiscovery names the collection of per-user MCP discoveries, and
// KindMCPUserDiscoveryContent the interned discoveries they hold.
const (
	KindMCPUserDiscovery        Kind = "mcp_user_discovery"
	KindMCPUserDiscoveryContent Kind = "mcp_user_discovery_content"
)

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

// KindAgent names the agent-gateway registration collection.
const KindAgent Kind = "agent"

// KindRoutingRule names the routing rule collection.
const KindRoutingRule Kind = "routing_rule"

// KindModelRouter names the model router collection.
const KindModelRouter Kind = "model_router"

// KindPricingOverride names the pricing override collection.
const KindPricingOverride Kind = "pricing_override"

// KindWebhookEndpoint names the webhook endpoint collection.
const KindWebhookEndpoint Kind = "webhook_endpoint"

// KindPrompt and KindPromptVersion name the prompt collections: prompts, and their versions with
// their messages.
const (
	KindPrompt        Kind = "prompt"
	KindPromptVersion Kind = "prompt_version"
)

// KindGrantSet names the interned grant sets: what keys may use, stored once per content.
const KindGrantSet Kind = "grant_set"

// Collections holds the typed collection of each config store table the live store loads. New
// registers every one of them, so a reader takes them from Store.Collections. The record types and
// the reads that load them are in the registry package.
type Collections struct {
	Providers          *Collection[registry.Provider]    // provider settings; their keys are ProviderKeys
	ProviderKeys       *Collection[registry.ProviderKey] // one record per key, children of Providers
	MCPClients         *Collection[registry.MCPClient]
	MCPUserDiscoveries *Collection[registry.MCPUserDiscovery]
	VirtualKeys        *Collection[registry.VirtualKey]
	Teams              *Collection[registry.Team]
	Customers          *Collection[registry.Customer]
	Budgets            *Collection[registry.Budget]
	RateLimits         *Collection[registry.RateLimit]
	ModelConfigs       *Collection[registry.ModelConfig]
	VirtualMCPs        *Collection[registry.VirtualMCP]
	Agents             *Collection[registry.Agent]
	RoutingRules       *Collection[registry.RoutingRule]
	ModelRouters       *Collection[registry.ModelRouter]
	Prompts            *Collection[registry.Prompt]
	// PricingOverrides holds every pricing override; the model catalog prices requests with them.
	PricingOverrides *Collection[registry.PricingOverride]
	// WebhookEndpoints holds every webhook endpoint, by ID and by name, for async job submissions
	// and the delivery worker.
	WebhookEndpoints *Collection[registry.WebhookEndpoint]
	// PromptVersions holds every prompt version, by prompt and number and as each prompt's latest;
	// a prompt's refresh re-reads its versions and its removal removes them.
	PromptVersions *Collection[registry.PromptVersion]
	// grantSets holds the grant set of every key, once per content; VirtualKey.Grants holds the
	// key's shared copy.
	grantSets *Interned[registry.GrantSet]
	// mcpDiscoveries holds every per-user MCP discovery once per content; a binding's Discovery
	// holds its shared copy.
	mcpDiscoveries *Interned[registry.MCPUserDiscoveryContent]
}

// FileRecords returns every record in fc, for LoadRecords: what a gateway without a config store
// loads from config.json.
func (c *Collections) FileRecords(fc *registry.FileRecords) []Record {
	recs := make([]Record, 0, len(fc.Providers)+len(fc.ProviderKeys)+len(fc.VirtualKeys)+len(fc.Teams)+
		len(fc.Customers)+len(fc.Budgets)+len(fc.RateLimits)+len(fc.ModelConfigs)+len(fc.PricingOverrides))
	for _, p := range fc.Providers {
		recs = append(recs, Rec(c.Providers, p))
	}
	for _, k := range fc.ProviderKeys {
		recs = append(recs, Rec(c.ProviderKeys, k))
	}
	for _, k := range fc.VirtualKeys {
		recs = append(recs, Rec(c.VirtualKeys, k))
	}
	for _, t := range fc.Teams {
		recs = append(recs, Rec(c.Teams, t))
	}
	for _, cu := range fc.Customers {
		recs = append(recs, Rec(c.Customers, cu))
	}
	for _, b := range fc.Budgets {
		recs = append(recs, Rec(c.Budgets, b))
	}
	for _, r := range fc.RateLimits {
		recs = append(recs, Rec(c.RateLimits, r))
	}
	for _, m := range fc.ModelConfigs {
		recs = append(recs, Rec(c.ModelConfigs, m))
	}
	for _, o := range fc.PricingOverrides {
		recs = append(recs, Rec(c.PricingOverrides, o))
	}
	return recs
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

// storeOptions holds what the StoreOptions given to New set: the owners of limits a build adds
// (LimitOwners).
type storeOptions struct {
	limits registry.LimitReads
}

// LimitOwners adds a build's own owners of budgets and rate limits to the ones the registry knows,
// for a build whose own entities hold limits. Every read of budgets and rate limits reads them in
// the same query as the registry's own, and a limit no holder the registry knows holds is calendar
// aligned when one of these owners holds it with its flag on. An owner that names a Parent makes
// its limits children of that kind, which the build registers: a refresh of a holder re-reads the
// limits it holds, keeps one that moved to another holder of the kind, drops one no holder of the
// kind holds any more, and removing a holder removes its limits (Cascade), as for a key or a team.
// New fails on owners registry.LimitReads cannot link, and LoadAll on a Parent no collection
// registers. A read that fails, these owners' part included, fails the load or the refresh, so the
// records stay as they were.
func LimitOwners(budgets, rateLimits []registry.LimitOwner) StoreOption {
	return func(o *storeOptions) {
		o.limits = registry.LimitReads{BudgetOwners: budgets, RateLimitOwners: rateLimits}
	}
}

// registerCollections registers the collection of each config store table on s, with the
// registry's reads as its loader.
func registerCollections(s *Store, o *storeOptions) (*Collections, error) {
	c := &Collections{}
	var err error
	if c.grantSets, err = s.RegisterInterned[registry.GrantSet](KindGrantSet, func(g *registry.GrantSet) Hash { return Hash(g.ID) }); err != nil {
		return nil, err
	}
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
	if err := registerMCPUserDiscoveries(s, c); err != nil {
		return nil, err
	}
	if err := registerGovernance(s, c, o); err != nil {
		return nil, err
	}
	if c.Agents, err = s.Register(KindAgent, nil, Loader[registry.Agent]{
		ID:   registry.AgentName,
		Page: registry.AgentsPage,
		ByID: registry.AgentsByName,
	}, MultiIndex(registry.AgentsAllowedByDefault, registry.AgentAllowedByDefault)); err != nil {
		return nil, err
	}
	if c.RoutingRules, err = s.Register(KindRoutingRule, nil, Loader[registry.RoutingRule]{
		ID:   registry.RoutingRuleID,
		Page: registry.RoutingRulesPage,
		ByID: registry.RoutingRulesByID,
	},
		MultiIndex(registry.RoutingRuleByScope, registry.RoutingRuleScopes),
		ReadInBatches[registry.RoutingRule](DefaultReadBatch),
	); err != nil {
		return nil, err
	}
	if c.ModelRouters, err = s.Register(KindModelRouter, nil, Loader[registry.ModelRouter]{
		ID:   registry.ModelRouterID,
		Page: registry.ModelRoutersPage,
		ByID: registry.ModelRoutersByID,
	}); err != nil {
		return nil, err
	}
	if c.PricingOverrides, err = s.Register(KindPricingOverride, nil, Loader[registry.PricingOverride]{
		ID:   registry.PricingOverrideID,
		Page: registry.PricingOverridesPage,
		ByID: registry.PricingOverridesByID,
	}); err != nil {
		return nil, err
	}
	if c.WebhookEndpoints, err = s.Register(KindWebhookEndpoint, nil, Loader[registry.WebhookEndpoint]{
		ID:   registry.WebhookEndpointID,
		Page: registry.WebhookEndpointsPage,
		ByID: registry.WebhookEndpointsByID,
	}, UniqueIndex(registry.WebhookEndpointByName, registry.WebhookEndpointName)); err != nil {
		return nil, err
	}
	if c.Prompts, err = s.Register(KindPrompt, nil, Loader[registry.Prompt]{
		ID:   registry.PromptID,
		Page: registry.PromptsPage,
		ByID: registry.PromptsByID,
	}); err != nil {
		return nil, err
	}
	if c.PromptVersions, err = s.Register(KindPromptVersion, []Kind{KindPrompt}, Loader[registry.PromptVersion]{
		ID:   registry.PromptVersionID,
		Page: registry.PromptVersionsPage,
		ByID: registry.PromptVersionsByID,
	},
		ChildOf(KindPrompt, registry.PromptVersionPrompt, registry.PromptVersionsByPrompts, Cascade),
		UniqueIndex(registry.PromptVersionByNumber, registry.PromptVersionNumberKey),
		MultiIndex(registry.PromptVersionLatest, registry.PromptVersionLatestKey),
	); err != nil {
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
		recs := s.Collections.ProviderKeys.List(ParentIndex(KindProvider), string(provider))
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
// model configs go with their key. Every read of keys goes through o's keyReads, interning the
// keys' grant sets, at most DefaultReadBatch keys at a time, since each key read binds the keys' IDs
// again in its grant preloads; and every read of limits reads o's limit owners with the registry's
// own.
func registerGovernance(s *Store, c *Collections, o *storeOptions) error {
	var err error
	if c.Customers, err = s.Register(KindCustomer, []Kind{KindBudget, KindRateLimit}, Loader[registry.Customer]{
		ID:   registry.CustomerID,
		Page: registry.CustomersPage,
		ByID: registry.CustomersByID,
	},
		UniqueIndex(registry.CustomerByName, registry.CustomerName),
	); err != nil {
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
		MultiIndex(registry.VirtualKeyByCreator, func(k *registry.VirtualKey) []string { return []string{registry.VirtualKeyCreator(k)} }),
		MultiIndex(registry.VirtualKeyByBusinessUnit, func(k *registry.VirtualKey) []string { return []string{registry.VirtualKeyBusinessUnitID(k)} }),
		ReadInBatches[registry.VirtualKey](DefaultReadBatch),
		ChildIDsOf(KindTeam, registry.VirtualKeyTeamID, registry.VirtualKeyIDsByTeams, SetNull),
		ChildIDsOf(KindCustomer, registry.VirtualKeyCustomerID, registry.VirtualKeyIDsByCustomers, SetNull),
		Interns(c.grantSets, func(k *registry.VirtualKey) **registry.GrantSet { return &k.Grants }),
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
	// The limits of a build's owner with a Parent are children of that kind too.
	budgetLinks, err := o.limits.BudgetLinks()
	if err != nil {
		return err
	}
	rateLimitLinks, err := o.limits.RateLimitLinks()
	if err != nil {
		return err
	}
	if c.Budgets, err = s.Register(KindBudget, nil, Loader[registry.Budget]{
		ID:   registry.BudgetID,
		Page: o.limits.BudgetsPage,
		ByID: o.limits.BudgetsByID,
	}, append([]Option[registry.Budget]{
		ChildOf(KindVirtualKey, registry.BudgetKeyID, o.limits.BudgetsByKeys, Cascade),
		ChildOf(KindTeam, registry.BudgetTeamID, o.limits.BudgetsByTeams, Cascade),
		ChildOf(KindCustomer, registry.BudgetCustomerID, o.limits.BudgetsByCustomers, Cascade),
		ChildOf(KindModelConfig, registry.BudgetModelConfigID, o.limits.BudgetsByModelConfigs, Cascade),
		ChildOf(KindProvider, registry.BudgetProvider, o.limits.BudgetsByProviders, Cascade),
	}, buildLimitLinks(budgetLinks)...)...); err != nil {
		return err
	}
	if c.RateLimits, err = s.Register(KindRateLimit, nil, Loader[registry.RateLimit]{
		ID:   registry.RateLimitID,
		Page: o.limits.RateLimitsPage,
		ByID: o.limits.RateLimitsByID,
	}, append([]Option[registry.RateLimit]{
		ChildOf(KindVirtualKey, registry.RateLimitKeyID, o.limits.RateLimitsByKeys, Cascade),
		ChildOf(KindTeam, registry.RateLimitTeamID, o.limits.RateLimitsByTeams, Cascade),
		ChildOf(KindCustomer, registry.RateLimitCustomerID, o.limits.RateLimitsByCustomers, Cascade),
		ChildOf(KindModelConfig, registry.RateLimitModelConfigID, o.limits.RateLimitsByModelConfigs, Cascade),
		ChildOf(KindProvider, registry.RateLimitProvider, o.limits.RateLimitsByProviders, Cascade),
	}, buildLimitLinks(rateLimitLinks)...)...); err != nil {
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

// buildLimitLinks declares each link of the limits a build's own kinds hold as a ChildIDsOf link,
// removed with its holder.
func buildLimitLinks[T any](links []registry.LimitLink[T]) []Option[T] {
	opts := make([]Option[T], 0, len(links))
	for _, l := range links {
		opts = append(opts, ChildIDsOf(Kind(l.Parent), l.ParentID, l.ChildIDs, Cascade))
	}
	return opts
}

// registerMCPUserDiscoveries registers the per-user MCP discoveries: bindings keyed by caller and
// client, read in batches, removed with their client and re-read when it is refreshed, listed by
// caller, and holding their discovery's shared copy.
func registerMCPUserDiscoveries(s *Store, c *Collections) error {
	var err error
	if c.mcpDiscoveries, err = s.RegisterInterned[registry.MCPUserDiscoveryContent](KindMCPUserDiscoveryContent, mcpDiscoveryHash); err != nil {
		return err
	}
	c.MCPUserDiscoveries, err = s.Register(KindMCPUserDiscovery, []Kind{KindMCPClient}, Loader[registry.MCPUserDiscovery]{
		ID:   registry.MCPUserDiscoveryID,
		Page: registry.MCPUserDiscoveriesPage,
		ByID: registry.MCPUserDiscoveriesByID,
	},
		ReadInBatches[registry.MCPUserDiscovery](DefaultReadBatch),
		ChildIDsOf(KindMCPClient, registry.MCPUserDiscoveryClientID, registry.MCPUserDiscoveryIDsByClients, Cascade),
		MultiIndex(registry.MCPUserDiscoveryByCaller, registry.MCPUserDiscoveryCallers),
		Interns(c.mcpDiscoveries, func(d *registry.MCPUserDiscovery) **registry.MCPUserDiscoveryContent { return &d.Discovery }),
	)
	return err
}

// mcpDiscoveryHash is a discovery's key in its pool: the SHA-256 content hash its rows carry, read
// as the first sixteen bytes of the hex digest, or the first sixteen bytes of the string's own
// SHA-256 when it is not hex. Equal keys share one copy, so the key must be collision resistant.
func mcpDiscoveryHash(d *registry.MCPUserDiscoveryContent) Hash {
	var h Hash
	if len(d.Hash) >= 2*len(h) {
		if _, err := hex.Decode(h[:], []byte(d.Hash[:2*len(h)])); err == nil {
			return h
		}
	}
	sum := sha256.Sum256([]byte(d.Hash))
	copy(h[:], sum[:])
	return h
}
