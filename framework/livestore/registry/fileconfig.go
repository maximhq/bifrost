package registry

import (
	"slices"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// FileInput is what config.json declares for the live store, as lib assembles it for a gateway
// without a config store. Each domain that moves into the live store adds its part here.
type FileInput struct {
	Providers map[schemas.ModelProvider]configstore.ProviderConfig
	// ProviderKeys is each provider's keys in config.json order, keyed as Providers is.
	ProviderKeys map[schemas.ModelProvider][]schemas.Key
	Governance   *configstore.GovernanceConfig
	// VectorStoreSettings is each configured vector store's settings from config.json.
	VectorStoreSettings []tables.TableVectorStoreSettings
}

// FileRecords holds the records a gateway without a config store loads, built from config.json.
type FileRecords struct {
	Providers    []*Provider    // settings only; keys are ProviderKeys
	ProviderKeys []*ProviderKey // each provider's keys, Order their position in config.json
	VirtualKeys  []*VirtualKey
	Teams        []*Team
	Customers    []*Customer
	Budgets      []*Budget
	RateLimits   []*RateLimit
	ModelConfigs []*ModelConfig
	// PricingOverrides are the pricing overrides governance lists.
	PricingOverrides []*PricingOverride
	// GrantSets are the keys' grant sets, which the store interns before it loads the keys.
	GrantSets []*GrantSet
	// VectorStoreSettings are the configured vector stores' settings.
	VectorStoreSettings []*VectorStoreSettings
}

// FromConfig builds the records config.json declares, for a gateway without a config store: a
// settings-only provider record for each provider in.Providers holds, naming the budget and rate
// limit governance gives it, a key record for each of its keys, ordered by the key's position in
// config.json, and the governance records. Governance is joined the way such a gateway has always joined
// it: a key's, provider config's and customer's budgets are the ones nested under it; a team's are
// those nested under it and the listed budgets naming it; a model config's are those nested under
// it, or else the listed budgets naming it. A holder's rate limit is the one its rate_limit_id
// names, listed or nested under it. A listed budget or rate limit replaces a nested one with the
// same ID, and a listed one nothing holds is still loaded, with no holder. Providers come in name
// order.
func FromConfig(in FileInput) *FileRecords {
	g := &FileRecords{}
	for i := range in.VectorStoreSettings {
		g.VectorStoreSettings = append(g.VectorStoreSettings, &VectorStoreSettings{TableVectorStoreSettings: in.VectorStoreSettings[i]})
	}
	providerBudget, providerRateLimit := governanceRecords(g, in.Governance)
	if in.Governance != nil {
		for i := range in.Governance.PricingOverrides {
			g.PricingOverrides = append(g.PricingOverrides, &PricingOverride{TablePricingOverride: in.Governance.PricingOverrides[i]})
		}
	}
	names := make([]schemas.ModelProvider, 0, len(in.Providers))
	for name := range in.Providers {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		cfg := in.Providers[name]
		g.Providers = append(g.Providers, &Provider{
			Name: name, Config: cfg,
			BudgetID: providerBudget[string(name)], RateLimitID: providerRateLimit[string(name)],
		})
		for i, key := range in.ProviderKeys[name] {
			g.ProviderKeys = append(g.ProviderKeys, &ProviderKey{Provider: name, Order: int64(i), Key: key})
		}
	}
	return g
}

// addGrantSet keeps a key's grant set for the store to intern, and returns its ID.
func (g *FileRecords) addGrantSet(set *GrantSet) GrantSetID {
	g.GrantSets = append(g.GrantSets, set)
	return set.ID
}

// governanceRecords adds cfg's governance records to g, joined as FromConfig describes, and
// returns each provider's budget and rate limit IDs by provider name.
func governanceRecords(g *FileRecords, cfg *configstore.GovernanceConfig) (providerBudget, providerRateLimit map[string]string) {
	providerBudget, providerRateLimit = map[string]string{}, map[string]string{}
	if cfg == nil {
		return providerBudget, providerRateLimit
	}
	listedBudgets := make(map[string]tables.TableBudget, len(cfg.Budgets))
	for _, b := range cfg.Budgets {
		listedBudgets[b.ID] = b
	}
	listedRateLimits := make(map[string]tables.TableRateLimit, len(cfg.RateLimits))
	for _, r := range cfg.RateLimits {
		listedRateLimits[r.ID] = r
	}
	budgets := make(map[string]*Budget)
	rateLimits := make(map[string]*RateLimit)
	// holdBudgets records the budgets one holder holds, with the holder's calendar flag, and
	// returns their IDs.
	holdBudgets := func(nested []tables.TableBudget, calendar bool, set func(*Budget)) []string {
		ids := make([]string, 0, len(nested))
		for _, b := range nested {
			if listed, ok := listedBudgets[b.ID]; ok {
				b = listed
			}
			b.IsCalendarAligned = calendar
			rec := &Budget{TableBudget: b}
			set(rec)
			budgets[b.ID] = rec
			ids = append(ids, b.ID)
		}
		return ids
	}
	// holdRateLimit records the rate limit one holder names, nested or listed, and returns its ID.
	holdRateLimit := func(id *string, nested *tables.TableRateLimit, calendar bool, set func(*RateLimit)) string {
		var row tables.TableRateLimit
		switch {
		case id != nil && *id != "":
			listed, ok := listedRateLimits[*id]
			if !ok && (nested == nil || nested.ID != *id) {
				return *id
			}
			if ok {
				row = listed
			} else {
				row = *nested
			}
		default:
			return ""
		}
		row.IsCalendarAligned = calendar
		rec := &RateLimit{TableRateLimit: row}
		set(rec)
		rateLimits[row.ID] = rec
		return row.ID
	}

	for i := range cfg.Customers {
		c := &cfg.Customers[i]
		rec := &Customer{ID: c.ID, Name: c.Name, CalendarAligned: c.CalendarAligned}
		rec.BudgetIDs = holdBudgets(c.Budgets, c.CalendarAligned, func(b *Budget) { b.CustomerID = &c.ID })
		rec.RateLimitID = holdRateLimit(c.RateLimitID, c.RateLimit, c.CalendarAligned, func(r *RateLimit) { r.CustomerID = c.ID })
		g.Customers = append(g.Customers, rec)
	}
	for i := range cfg.Teams {
		t := &cfg.Teams[i]
		nested := append([]tables.TableBudget(nil), t.Budgets...)
		seen := make(map[string]bool, len(nested))
		for _, b := range nested {
			seen[b.ID] = true
		}
		for _, b := range cfg.Budgets {
			if b.TeamID != nil && *b.TeamID == t.ID && !seen[b.ID] {
				nested = append(nested, b)
			}
		}
		rec := &Team{ID: t.ID, Name: t.Name, CustomerID: deref(t.CustomerID), CalendarAligned: t.CalendarAligned, DisableContentLogging: t.DisableContentLogging}
		rec.BudgetIDs = holdBudgets(nested, t.CalendarAligned, func(b *Budget) { b.TeamID = &t.ID })
		rec.RateLimitID = holdRateLimit(t.RateLimitID, t.RateLimit, t.CalendarAligned, func(r *RateLimit) { r.TeamID = t.ID })
		g.Teams = append(g.Teams, rec)
	}
	for i := range cfg.ModelConfigs {
		mc := &cfg.ModelConfigs[i]
		nested := mc.Budgets
		if len(nested) == 0 {
			for _, b := range cfg.Budgets {
				if b.ModelConfigID != nil && *b.ModelConfigID == mc.ID {
					nested = append(nested, b)
				}
			}
		}
		ids := holdBudgets(nested, mc.CalendarAligned, func(b *Budget) { b.ModelConfigID = &mc.ID })
		rec := ModelConfigFromTable(mc, ids)
		rec.RateLimitID = holdRateLimit(mc.RateLimitID, mc.RateLimit, mc.CalendarAligned, func(r *RateLimit) { r.ModelConfigID = mc.ID })
		g.ModelConfigs = append(g.ModelConfigs, rec)
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.BudgetID != nil && *p.BudgetID != "" {
			var nested []tables.TableBudget
			if b, ok := listedBudgets[*p.BudgetID]; ok {
				nested = append(nested, b)
			} else if p.Budget != nil && p.Budget.ID == *p.BudgetID {
				nested = append(nested, *p.Budget)
			}
			holdBudgets(nested, false, func(b *Budget) { b.Provider = p.Name })
			providerBudget[p.Name] = *p.BudgetID
		}
		if id := holdRateLimit(p.RateLimitID, p.RateLimit, false, func(r *RateLimit) { r.Provider = p.Name }); id != "" {
			providerRateLimit[p.Name] = id
		}
	}
	for i := range cfg.VirtualKeys {
		vk := cfg.VirtualKeys[i]
		keyID := vk.ID
		ids := holdBudgets(vk.Budgets, vk.CalendarAligned, func(b *Budget) { b.VirtualKeyID = &keyID; b.KeyID = keyID })
		rateLimitID := holdRateLimit(vk.RateLimitID, vk.RateLimit, vk.CalendarAligned, func(r *RateLimit) { r.KeyID = keyID })
		configs := make([]tables.TableVirtualKeyProviderConfig, len(vk.ProviderConfigs))
		for j := range vk.ProviderConfigs {
			pc := vk.ProviderConfigs[j]
			configID := pc.ID
			pcIDs := holdBudgets(pc.Budgets, vk.CalendarAligned, func(b *Budget) { b.ProviderConfigID = &configID; b.KeyID = keyID })
			pc.Budgets = make([]tables.TableBudget, 0, len(pcIDs))
			for _, id := range pcIDs {
				pc.Budgets = append(pc.Budgets, tables.TableBudget{ID: id})
			}
			if id := holdRateLimit(pc.RateLimitID, pc.RateLimit, vk.CalendarAligned, func(r *RateLimit) { r.KeyID = keyID }); id != "" {
				pc.RateLimitID = &id
			}
			configs[j] = pc
		}
		vk.ProviderConfigs = configs
		rec := VirtualKeyFromTable(&vk, ids, nil, g.addGrantSet)
		rec.RateLimitID = rateLimitID
		g.VirtualKeys = append(g.VirtualKeys, rec)
	}
	// Every listed limit is loaded, as every listed limit always has been: the ones no holder above
	// takes come with no holder and the calendar flag off.
	for _, b := range cfg.Budgets {
		if _, held := budgets[b.ID]; !held {
			b.IsCalendarAligned = false
			budgets[b.ID] = &Budget{TableBudget: b}
		}
	}
	for _, r := range cfg.RateLimits {
		if _, held := rateLimits[r.ID]; !held {
			r.IsCalendarAligned = false
			rateLimits[r.ID] = &RateLimit{TableRateLimit: r}
		}
	}
	for _, b := range budgets {
		g.Budgets = append(g.Budgets, b)
	}
	for _, r := range rateLimits {
		g.RateLimits = append(g.RateLimits, r)
	}
	return providerBudget, providerRateLimit
}
