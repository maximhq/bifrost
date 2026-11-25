package registry

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

// testKeyReads reads keys as the live store does, keeping their grant sets by ID in the map it
// returns.
func testKeyReads() (KeyReads, map[GrantSetID]*GrantSet) {
	sets := map[GrantSetID]*GrantSet{}
	return KeyReads{Grants: func(g *GrantSet) GrantSetID {
		sets[g.ID] = g
		return g.ID
	}}, sets
}

// virtualKeyIDsOf lists the IDs of key records in the order given.
func virtualKeyIDsOf(rows []*VirtualKey) []string {
	ids := make([]string, 0, len(rows))
	for _, k := range rows {
		ids = append(ids, VirtualKeyID(k))
	}
	return ids
}

// TestVirtualKeysPageAndRecord checks that pages walk every key in ID order, and that a key's
// record carries what it holds by ID (budgets in creation order, rate limit, each provider
// config's budgets and rate limit), its organization, and its rotation and expiry fields, and
// that its grant set carries the provider configs with their key IDs, the MCP configs with the
// client's name, and the virtual MCPs in ascending order.
func TestVirtualKeysPageAndRecord(t *testing.T) {
	ctx := context.Background()
	store := newProviderDB(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {Keys: []schemas.Key{key("k1"), key("k2"), key("k3")}},
	})
	db := store.DB()
	require.NoError(t, store.CreateMCPClientConfig(ctx, mcpClient("c1")))

	now := time.Now().Truncate(time.Millisecond)
	expired, graceUntil := now.Add(-time.Hour), now.Add(time.Hour)
	insert(t, db,
		rateLimitRow("rl-a", 1), rateLimitRow("rl-pc", 2),
		&tables.TableCustomer{ID: "cust-1", Name: "Acme"},
		&tables.TableTeam{ID: "team-1", Name: "Platform"},
	)
	// Created out of ID order, so the pages have to follow the cursor rather than insertion.
	insert(t, db,
		&tables.TableVirtualKey{ID: "vk-c", Name: "plain", Value: *schemas.NewSecretVar("sk-c"), PreviousValue: *schemas.NewSecretVar("sk-c-old")},
		&tables.TableVirtualKey{
			ID: "vk-a", Name: "full", Value: *schemas.NewSecretVar("sk-a"), TeamID: ptr("team-1"), RateLimitID: ptr("rl-a"),
			CalendarAligned: true, AllowAllProviders: true, DisableContentLogging: ptr(true), Metadata: map[string]string{"env": "prod"},
			CreatedByUserID: ptr("user-1"),
		},
		&tables.TableVirtualKey{
			ID: "vk-b", Name: "rotating", Value: *schemas.NewSecretVar("sk-b"), CustomerID: ptr("cust-1"), IsActive: ptr(false),
			PreviousValue: *schemas.NewSecretVar("sk-b-old"), PreviousValueExpiresAt: &graceUntil, ExpiresAt: &expired,
		},
	)
	pc := &tables.TableVirtualKeyProviderConfig{VirtualKeyID: "vk-a", Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-4o"}, RateLimitID: ptr("rl-pc")}
	insert(t, db, pc)
	var keyRows []tables.TableKey
	require.NoError(t, db.Where("key_id IN ?", []string{"k1", "k3"}).Find(&keyRows).Error)
	require.Len(t, keyRows, 2)
	for _, k := range keyRows {
		insert(t, db, &tables.TableVirtualKeyProviderConfigKey{TableVirtualKeyProviderConfigID: pc.ID, TableKeyID: k.ID})
	}
	// The key's budgets are created with the later ID first, so creation order shows through.
	first, second, pcBudget := budgetRow("b-2", 20), budgetRow("b-1", 10), budgetRow("b-pc", 30)
	first.VirtualKeyID, first.CreatedAt = ptr("vk-a"), now.Add(-2*time.Minute)
	second.VirtualKeyID, second.CreatedAt = ptr("vk-a"), now.Add(-time.Minute)
	pcBudget.ProviderConfigID = ptr(pc.ID)
	insert(t, db, first, second, pcBudget)
	var client tables.TableMCPClient
	require.NoError(t, db.Where("client_id = ?", "c1").First(&client).Error)
	insert(t, db, &tables.TableVirtualKeyMCPConfig{VirtualKeyID: "vk-a", MCPClientID: client.ID, ToolsToExecute: schemas.WhiteList{"search"}})
	for _, name := range []string{"one", "two", "three"} {
		insert(t, db, &tables.TableVirtualMCP{Name: name, EndpointSlug: name, Enabled: true})
	}
	insert(t, db,
		&tables.TableVirtualKeyVirtualMCP{VirtualKeyID: "vk-a", VirtualMCPID: 3},
		&tables.TableVirtualKeyVirtualMCP{VirtualKeyID: "vk-a", VirtualMCPID: 1},
	)

	reads, sets := testKeyReads()
	var walked []*VirtualKey
	after := ""
	for {
		rows, next, err := reads.Page(ctx, db, after, 2)
		require.NoError(t, err)
		walked = append(walked, rows...)
		if next == "" {
			break
		}
		after = next
	}
	require.Equal(t, []string{"vk-a", "vk-b", "vk-c"}, virtualKeyIDsOf(walked))
	full, rotating, plain := walked[0], walked[1], walked[2]

	require.Equal(t, "full", full.Name)
	require.Equal(t, "sk-a", full.Value)
	require.True(t, full.Active, "a key with no is_active value is active")
	require.True(t, full.CalendarAligned)
	grants := sets[full.Grants]
	require.NotNil(t, grants, "the key's grant set was stored")
	require.True(t, grants.AllowAllProviders)
	require.Equal(t, ptr(true), full.DisableContentLogging)
	require.Equal(t, map[string]string{"env": "prod"}, full.Metadata)
	require.Equal(t, "team-1", VirtualKeyTeamID(full))
	require.Empty(t, VirtualKeyCustomerID(full))
	require.Equal(t, "user-1", full.CreatedByUserID)
	require.Equal(t, "rl-a", full.RateLimitID)
	require.Equal(t, []string{"b-2", "b-1"}, full.BudgetIDs, "budgets in the order they were created")
	require.Equal(t, []uint{1, 3}, grants.VirtualMCPIDs, "virtual MCPs in ascending order")

	require.Len(t, grants.Providers, 1)
	config := grants.Providers[0]
	require.Equal(t, "openai", config.Provider)
	require.Equal(t, schemas.WhiteList{"gpt-4o"}, config.AllowedModels)
	require.ElementsMatch(t, []string{"k1", "k3"}, config.KeyIDs)
	require.Equal(t, []ProviderLimit{{ConfigID: pc.ID, Provider: "openai", BudgetIDs: []string{"b-pc"}, RateLimitID: "rl-pc"}}, full.ProviderLimits,
		"a config's budgets and rate limit are named by ID")

	require.Len(t, grants.MCP, 1)
	require.Equal(t, MCPGrant{ClientRowID: client.ID, ClientID: "c1", ClientName: "c1", Tools: schemas.WhiteList{"search"}}, grants.MCP[0])

	require.False(t, rotating.Active)
	require.Equal(t, "cust-1", VirtualKeyCustomerID(rotating))
	require.Empty(t, rotating.BudgetIDs)
	require.Empty(t, sets[rotating.Grants].VirtualMCPIDs)
	require.Equal(t, sets[rotating.Grants], sets[plain.Grants], "keys granted the same things share one grant set")
	require.Equal(t, "sk-b-old", rotating.PreviousValue)
	require.Equal(t, graceUntil.UnixNano(), rotating.PreviousValueExpiresAt)
	require.Equal(t, []string{"sk-b-old"}, VirtualKeyPreviousValue(rotating))
	require.True(t, rotating.HasActivePreviousValue(now))
	require.False(t, rotating.HasActivePreviousValue(graceUntil), "the retired value stops at its expiry")
	require.Equal(t, expired.UnixNano(), rotating.ExpiresAt)
	require.True(t, rotating.IsExpiredAt(now))
	require.False(t, rotating.IsExpiredAt(expired.Add(-time.Second)))

	require.Empty(t, plain.PreviousValue, "a retired value with no expiry is not carried")
	require.Zero(t, plain.PreviousValueExpiresAt)
	require.Nil(t, VirtualKeyPreviousValue(plain))
	require.False(t, plain.HasActivePreviousValue(now))
	require.Zero(t, plain.ExpiresAt)
	require.False(t, plain.IsExpiredAt(now), "a key with no expiry never expires")

	rows, err := reads.ByID(ctx, db, []string{"vk-c", "missing"})
	require.NoError(t, err)
	require.Equal(t, []string{"vk-c"}, virtualKeyIDsOf(rows), "an ID with no row is left out")
	rows, err = reads.ByTeams(ctx, db, []string{"team-1"})
	require.NoError(t, err)
	require.Equal(t, []string{"vk-a"}, virtualKeyIDsOf(rows))
	rows, err = reads.ByCustomers(ctx, db, []string{"cust-1"})
	require.NoError(t, err)
	require.Equal(t, []string{"vk-b"}, virtualKeyIDsOf(rows))
}

// TestVirtualKeyUnresolvedValueIsNotIndexed checks that a key whose value is a reference that
// does not resolve gets a record with an empty value, which the value index leaves out, so the key
// cannot be reached by any value.
func TestVirtualKeyUnresolvedValueIsNotIndexed(t *testing.T) {
	ctx := context.Background()
	db := newProviderDB(t, nil).DB()
	insert(t, db, &tables.TableVirtualKey{ID: "vk-env", Name: "env", Value: *schemas.NewSecretVar("sk-env")})
	// The save hook refuses an unresolvable reference, so the column is rewritten directly, as a
	// variable removed from the environment after the key was saved would leave it.
	require.NoError(t, db.Model(&tables.TableVirtualKey{}).Where("id = ?", "vk-env").
		UpdateColumn("value", "env.BIFROST_REGISTRY_TEST_UNSET_VARIABLE").Error)

	reads, _ := testKeyReads()
	rows, err := reads.ByID(ctx, db, []string{"vk-env"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Empty(t, rows[0].Value)
	require.Empty(t, VirtualKeyValue(rows[0]))

	// A rotating key whose current value stopped resolving fails closed on the previous-value
	// index as well: its retired value must not keep authenticating it.
	rotating := &VirtualKey{ID: "vk-rotating", PreviousValue: "sk-retired", PreviousValueExpiresAt: time.Now().Add(time.Hour).UnixNano()}
	require.Empty(t, VirtualKeyPreviousValue(rotating), "an unresolved key is not indexed by its retired value")
	rotating.Value = "sk-current"
	require.Equal(t, []string{"sk-retired"}, VirtualKeyPreviousValue(rotating))
}

// TestVirtualKeyFromTableNamesConfigRateLimits checks the builder a gateway without a config store
// uses: the record takes the given budget IDs, its grant set the given virtual MCP IDs, and a
// provider config's rate limit is named by ID only.
func TestVirtualKeyFromTableNamesConfigRateLimits(t *testing.T) {
	expires := time.Unix(1_700_000_000, 0)
	var stored *GrantSet
	rec := VirtualKeyFromTable(&tables.TableVirtualKey{
		ID: "vk-1", Name: "file", Value: *schemas.NewSecretVar("sk-file"), ExpiresAt: &expires,
		ProviderConfigs: []tables.TableVirtualKeyProviderConfig{{
			Provider: "openai", RateLimitID: ptr("rl-1"), RateLimit: &tables.TableRateLimit{ID: "rl-1"},
		}},
	}, []string{"b-1"}, []uint{4}, func(g *GrantSet) GrantSetID {
		stored = g
		return g.ID
	})
	require.Equal(t, "sk-file", rec.Value)
	require.True(t, rec.Active)
	require.Equal(t, expires.UnixNano(), rec.ExpiresAt)
	require.Equal(t, []string{"b-1"}, rec.BudgetIDs)
	require.NotNil(t, stored)
	require.Equal(t, stored.ID, rec.Grants)
	require.Equal(t, []uint{4}, stored.VirtualMCPIDs)
	require.Equal(t, []ProviderLimit{{Provider: "openai", RateLimitID: "rl-1"}}, rec.ProviderLimits)
}

// TestGrantSetIDFollowsContent checks that two grant sets with the same content get the same ID and
// that any difference, nil against empty included, gives a different one.
func TestGrantSetIDFollowsContent(t *testing.T) {
	weight := 0.5
	base := func() *GrantSet {
		w := weight
		return &GrantSet{
			Providers: []ProviderGrant{{Provider: "openai", Weight: &w, AllowedModels: schemas.WhiteList{"gpt-4o"},
				KeyIDs: schemas.WhiteList{"key-1"}}},
			MCP:           []MCPGrant{{ClientRowID: 3, ClientID: "c1", ClientName: "search", Tools: schemas.WhiteList{"find"}}},
			VirtualMCPIDs: []uint{2},
			Agents:        []string{"agent"},
			Relays:        []string{"relay"},
		}
	}
	id := SealGrantSet(base()).ID
	require.Equal(t, id, SealGrantSet(base()).ID, "equal content, equal ID")
	for name, change := range map[string]func(*GrantSet){
		"allow all providers": func(g *GrantSet) { g.AllowAllProviders = true },
		"provider":            func(g *GrantSet) { g.Providers[0].Provider = "anthropic" },
		"weight":              func(g *GrantSet) { *g.Providers[0].Weight = 0.25 },
		"no weight":           func(g *GrantSet) { g.Providers[0].Weight = nil },
		"allowed models":      func(g *GrantSet) { g.Providers[0].AllowedModels = schemas.WhiteList{"gpt-4o-mini"} },
		"empty allowed":       func(g *GrantSet) { g.Providers[0].AllowedModels = schemas.WhiteList{} },
		"nil allowed":         func(g *GrantSet) { g.Providers[0].AllowedModels = nil },
		"blocked models":      func(g *GrantSet) { g.Providers[0].BlacklistedModels = schemas.BlackList{"gpt-4o"} },
		"all keys":            func(g *GrantSet) { g.Providers[0].KeyIDs = schemas.WhiteList{"*"} },
		"key":                 func(g *GrantSet) { g.Providers[0].KeyIDs[0] = "key-2" },
		"no keys":             func(g *GrantSet) { g.Providers[0].KeyIDs = schemas.WhiteList{} },
		"mcp client":          func(g *GrantSet) { g.MCP[0].ClientID = "c2" },
		"mcp client name":     func(g *GrantSet) { g.MCP[0].ClientName = "other" },
		"mcp tools":           func(g *GrantSet) { g.MCP[0].Tools = schemas.WhiteList{"*"} },
		"virtual mcp":         func(g *GrantSet) { g.VirtualMCPIDs = []uint{3} },
		"agents":              func(g *GrantSet) { g.Agents = nil },
		"relays":              func(g *GrantSet) { g.Relays = []string{"relay", "other"} },
		"string boundary":     func(g *GrantSet) { g.Agents, g.Relays = []string{"agentrel"}, []string{"ay"} },
	} {
		g := base()
		change(g)
		require.NotEqual(t, id, SealGrantSet(g).ID, name)
	}
	empty, nilSet := &GrantSet{Agents: []string{}}, &GrantSet{}
	require.NotEqual(t, SealGrantSet(empty).ID, SealGrantSet(nilSet).ID, "an empty list differs from none")
}
