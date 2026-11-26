package configstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/relay"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testRelay(name string) *schemas.RelayConfig {
	return &schemas.RelayConfig{
		Name:         name,
		Description:  "search API",
		BaseURL:      "https://search.example.com",
		Auth:         schemas.RelayAuthConfig{Type: schemas.RelayAuthBearer},
		Credential:   &schemas.RelayCredential{Value: schemas.NewSecretVar("tvly-secret-value")},
		ExtraHeaders: map[string]schemas.SecretVar{"X-Tenant": *schemas.NewSecretVar("acme")},
		Usage:        &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.001)},
		Routes:       []schemas.RelayRouteConfig{{Method: "POST", Path: "/search", Usage: &schemas.RelayUsageConfig{CostPerUnit: schemas.Ptr(0.008), UnitsPath: "usage.credits"}}},
	}
}

func TestRelayPersistence(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)

	require.NoError(t, store.CreateRelay(ctx, testRelay("tavily"), "hash-1"))
	err := store.CreateRelay(ctx, testRelay("tavily"), "")
	require.ErrorIs(t, err, ErrRelayExists)

	got, err := store.GetRelay(ctx, "tavily")
	require.NoError(t, err)
	assert.Equal(t, "tvly-secret-value", got.Credential.Value.GetValue(), "the credential round-trips")
	tenant := got.ExtraHeaders["X-Tenant"]
	assert.Equal(t, "acme", tenant.GetValue())
	require.Len(t, got.Routes, 1)
	assert.InDelta(t, 0.008, *got.Routes[0].Usage.CostPerUnit, 1e-12, "per-route overrides round-trip")

	var raw tables.TableRelay
	require.NoError(t, store.DB().Session(&gorm.Session{SkipHooks: true}).First(&raw, "name = ?", "tavily").Error)
	assert.Equal(t, tables.EncryptionStatusEncrypted, raw.EncryptionStatus)
	require.NotNil(t, raw.CredentialValue)
	assert.NotEqual(t, "tvly-secret-value", raw.CredentialValue.Val, "the credential is encrypted at rest")
	assert.False(t, strings.Contains(raw.ExtraHeadersJSON, "acme"), "extra header values are encrypted at rest")
	assert.False(t, strings.Contains(raw.ConfigJSON, "tvly-secret-value"), "and never land in the plain config column")

	// An API update keeps the stored config hash; a file sync replaces it.
	got.Description = "edited"
	require.NoError(t, store.UpdateRelay(ctx, got, ""))
	hashes, err := store.GetRelayConfigHashes(ctx)
	require.NoError(t, err)
	assert.Equal(t, "hash-1", hashes["tavily"])
	require.NoError(t, store.UpdateRelay(ctx, got, "hash-2"))
	hashes, err = store.GetRelayConfigHashes(ctx)
	require.NoError(t, err)
	assert.Equal(t, "hash-2", hashes["tavily"])

	reloaded, err := store.GetRelay(ctx, "tavily")
	require.NoError(t, err)
	assert.Equal(t, "edited", reloaded.Description)
	assert.Equal(t, "tvly-secret-value", reloaded.Credential.Value.GetValue(), "an update re-encrypts and still decrypts")

	missing := testRelay("missing")
	require.ErrorIs(t, store.UpdateRelay(ctx, missing, ""), ErrNotFound)

	list, err := store.ListRelays(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
}

// An env- or vault-backed header is stored as its reference, never as the value it resolves to, so
// a database copy holds no secret even for headers encryption skips.
func TestRelayHeaderReferencesStayReferences(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)
	t.Setenv("PTE_RELAY_HEADER_SECRET", "hunter2-from-env")
	cfg := testRelay("docs")
	cfg.ExtraHeaders["X-Env-Key"] = *schemas.NewSecretVar("env.PTE_RELAY_HEADER_SECRET")
	require.NoError(t, store.CreateRelay(ctx, cfg, ""))

	var raw tables.TableRelay
	require.NoError(t, store.DB().Session(&gorm.Session{SkipHooks: true}).First(&raw, "name = ?", "docs").Error)
	assert.False(t, strings.Contains(raw.ExtraHeadersJSON, "hunter2-from-env"), "the resolved value never reaches the column")
	assert.True(t, strings.Contains(raw.ExtraHeadersJSON, "env.PTE_RELAY_HEADER_SECRET"), "the reference is what is stored")

	got, err := store.GetRelay(ctx, "docs")
	require.NoError(t, err)
	envHeader := got.ExtraHeaders["X-Env-Key"]
	assert.True(t, envHeader.IsFromSecret(), "the header loads back as a reference")
	assert.Equal(t, "hunter2-from-env", envHeader.GetValue(), "and resolves from the environment on load")
	tenant := got.ExtraHeaders["X-Tenant"]
	assert.Equal(t, "acme", tenant.GetValue(), "a literal header still round-trips through encryption")
}

// Deleting a key removes its relay grant rows, as it removes its agent grants, so no grant can
// outlive the key that held it.
func TestDeleteVirtualKeyRemovesRelayGrants(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)
	require.NoError(t, store.CreateRelay(ctx, testRelay("tavily"), ""))
	require.NoError(t, store.CreateVirtualKey(ctx, &tables.TableVirtualKey{ID: "vk-granted", Name: "granted", Value: *schemas.NewSecretVar("v-granted"), IsActive: schemas.Ptr(true), RelayGrants: []tables.TableVirtualKeyRelayGrant{{VirtualKeyID: "vk-granted", RelayName: "tavily"}}}))
	ids, err := store.GetRelayGrantedVirtualKeyIDs(ctx, "tavily")
	require.NoError(t, err)
	assert.Equal(t, []string{"vk-granted"}, ids)

	require.NoError(t, store.DeleteVirtualKey(ctx, "vk-granted"))
	ids, err = store.GetRelayGrantedVirtualKeyIDs(ctx, "tavily")
	require.NoError(t, err)
	assert.Empty(t, ids, "the deleted key's grant rows are gone")
}

// TestRelayRoutesKeepIDsAcrossUpdates checks a full-replace save keeps each surviving route's ID
// and created_at, drops removed routes, and stores new routes after the kept ones.
func TestRelayRoutesKeepIDsAcrossUpdates(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)
	cfg := testRelay("tavily")
	cfg.Routes = append(cfg.Routes, schemas.RelayRouteConfig{Method: "POST", Path: "/extract"}, schemas.RelayRouteConfig{Method: "GET", Path: "/usage"})
	require.NoError(t, store.CreateRelay(ctx, cfg, ""))
	created, err := store.GetRelay(ctx, "tavily")
	require.NoError(t, err)
	require.Len(t, created.Routes, 3)
	ids := map[string]string{}
	for _, route := range created.Routes {
		require.NotEmpty(t, route.ID)
		ids[route.Path] = route.ID
	}
	assert.Equal(t, []string{"/search", "/extract", "/usage"}, routePaths(created.Routes), "routes come back in the order sent")

	// Resent without IDs and reordered: /usage dropped, /extract edited by ID, /crawl added first.
	edit := testRelay("tavily")
	edit.Routes = []schemas.RelayRouteConfig{
		{Method: "POST", Path: "/crawl"},
		{ID: ids["/extract"], Method: "POST", Path: "/extract", Billable: schemas.Ptr(false)},
		{Method: "POST", Path: "/search", Usage: edit.Routes[0].Usage},
	}
	require.NoError(t, store.UpdateRelay(ctx, edit, ""))
	got, err := store.GetRelay(ctx, "tavily")
	require.NoError(t, err)
	assert.Equal(t, []string{"/search", "/extract", "/crawl"}, routePaths(got.Routes), "kept routes keep their order; the new one goes last")
	assert.Equal(t, ids["/search"], got.Routes[0].ID)
	assert.Equal(t, ids["/extract"], got.Routes[1].ID)
	require.NotNil(t, got.Routes[1].Billable)
	assert.False(t, *got.Routes[1].Billable, "the edit is saved")
	assert.NotContains(t, []string{ids["/search"], ids["/extract"], ids["/usage"]}, got.Routes[2].ID)
	assert.Equal(t, got.Routes[2].ID, edit.Routes[0].ID, "assigned IDs are written back to the caller's config")

	var count int64
	require.NoError(t, store.DB().Model(&tables.TableRelayRoute{}).Where("relay_name = ?", "tavily").Count(&count).Error)
	assert.EqualValues(t, 3, count, "the removed route's row is gone")

	require.NoError(t, store.DeleteRelay(ctx, "tavily"))
	require.NoError(t, store.DB().Model(&tables.TableRelayRoute{}).Count(&count).Error)
	assert.Zero(t, count, "deleting the relay deletes its routes")
}

// TestMigrationAddRelayRoutesTable checks the migration moves routes out of config_json into rows
// in their saved order, so routes of equal rank keep the order that breaks their tie.
func TestMigrationAddRelayRoutesTable(t *testing.T) {
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, migrationAddRelaysTables(ctx, db, testMigrationLogger))

	legacy := []schemas.RelayRouteConfig{
		{Method: "POST", Path: "/v1/*", Headers: map[string]string{"X-Mode": "a*"}},
		{Method: "POST", Path: "/v1/*", Headers: map[string]string{"X-Tier": "b*"}},
		{Method: "POST", Path: "/v1/search", Billable: schemas.Ptr(false)},
		{Method: "POST", Path: "/v1/search", Billable: schemas.Ptr(false)}, // a config.json duplicate
	}
	configJSON, err := json.Marshal(map[string]any{"base_url": "https://api.example.com", "routes": legacy, "skip_route_check": true})
	require.NoError(t, err)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, db.Exec("INSERT INTO config_relays (name, description, config_json, created_at, updated_at) VALUES (?, '', ?, ?, ?)", "legacy", string(configJSON), created, created).Error)

	require.NoError(t, migrationAddRelayRoutesTable(ctx, db, testMigrationLogger))

	var stripped string
	require.NoError(t, db.Table("config_relays").Where("name = ?", "legacy").Pluck("config_json", &stripped).Error)
	assert.NotContains(t, stripped, "routes", "config_json no longer carries routes")
	assert.Contains(t, stripped, "skip_route_check", "other settings are untouched")

	store := &RDBConfigStore{}
	store.db.Store(db)
	got, err := store.GetRelay(ctx, "legacy")
	require.NoError(t, err)
	require.Len(t, got.Routes, 4, "duplicates each get their own row")
	seen := map[string]bool{}
	for i, route := range got.Routes {
		require.NotEmpty(t, route.ID)
		assert.False(t, seen[route.ID])
		seen[route.ID] = true
		route.ID = ""
		assert.Equal(t, legacy[i], route, "route %d keeps its saved position and settings", i)
	}
	assert.Equal(t, relay.RouteMatchOrder(legacy), relay.RouteMatchOrder(got.Routes), "match order, ties included, is unchanged")
}

// routePaths lists route paths in order.
func routePaths(routes []schemas.RelayRouteConfig) []string {
	out := make([]string, len(routes))
	for i, route := range routes {
		out[i] = route.Path
	}
	return out
}

// TestRelayGrantRoutes checks route-level virtual-key grants: an omitted list is every route, a
// list must name routes of its relay, and a relay edit that removes routes prunes them from grants,
// deleting a grant left naming none.
func TestRelayGrantRoutes(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)
	cfg := testRelay("tavily")
	cfg.Routes = append(cfg.Routes, schemas.RelayRouteConfig{Method: "POST", Path: "/extract"}, schemas.RelayRouteConfig{Method: "GET", Path: "/usage"})
	require.NoError(t, store.CreateRelay(ctx, cfg, ""))
	search, extract, usage := cfg.Routes[0].ID, cfg.Routes[1].ID, cfg.Routes[2].ID
	for _, id := range []string{"vk-all", "vk-two", "vk-one"} {
		require.NoError(t, store.CreateVirtualKey(ctx, &tables.TableVirtualKey{ID: id, Name: id, Value: *schemas.NewSecretVar("v-" + id), IsActive: schemas.Ptr(true)}))
	}
	grant := func(vk string, routes schemas.WhiteList) error {
		return store.ReplaceVirtualKeyRelayGrants(ctx, vk, []tables.TableVirtualKeyRelayGrant{{RelayName: "tavily", Routes: routes}})
	}
	require.NoError(t, grant("vk-all", nil))
	require.NoError(t, grant("vk-two", schemas.WhiteList{search, extract}))
	require.NoError(t, grant("vk-one", schemas.WhiteList{usage}))

	assert.ErrorIs(t, grant("vk-one", schemas.WhiteList{}), ErrInvalidRelayGrant, "an empty list grants nothing")
	assert.ErrorIs(t, grant("vk-one", schemas.WhiteList{"99999999-9999-4999-8999-999999999999"}), ErrInvalidRelayGrant, "a route the relay does not have")
	assert.ErrorIs(t, grant("vk-one", schemas.WhiteList{"*", search}), ErrInvalidRelayGrant, "the wildcard stands alone")
	assert.ErrorIs(t, grant("vk-one", schemas.WhiteList{"/search"}), ErrInvalidRelayGrant, "routes are named by ID")

	routesOf := func(vk string) schemas.WhiteList {
		var rows []tables.TableVirtualKeyRelayGrant
		require.NoError(t, store.DB().Where("virtual_key_id = ?", vk).Find(&rows).Error)
		if len(rows) == 0 {
			return nil
		}
		return rows[0].Routes
	}
	assert.Equal(t, schemas.WhiteList{"*"}, routesOf("vk-all"), "an omitted list is stored as every route")
	assert.Equal(t, schemas.WhiteList{usage}, routesOf("vk-one"), "a refused replacement leaves the grant as it was")
	require.NoError(t, grant("vk-one", nil))
	assert.Equal(t, schemas.WhiteList{usage}, routesOf("vk-one"), "resending a grant without routes keeps the routes it has")

	// Drop /extract and /usage.
	edit := testRelay("tavily")
	edit.Routes = []schemas.RelayRouteConfig{{ID: search, Method: "POST", Path: "/search"}}
	require.NoError(t, store.UpdateRelay(ctx, edit, ""))
	assert.Equal(t, schemas.WhiteList{"*"}, routesOf("vk-all"), "an all-routes grant is untouched")
	assert.Equal(t, schemas.WhiteList{search}, routesOf("vk-two"), "a removed route leaves the grant")
	assert.Nil(t, routesOf("vk-one"), "a grant left naming no route is deleted")
}

// TestMigrationAddRelayGrantRoutesColumn checks grants made before routes existed become
// all-routes grants, so no key loses access.
func TestMigrationAddRelayGrantRoutesColumn(t *testing.T) {
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE governance_virtual_key_relay_grants (virtual_key_id varchar(255), relay_name varchar(64), created_at datetime, PRIMARY KEY (virtual_key_id, relay_name))").Error)
	require.NoError(t, db.Exec("INSERT INTO governance_virtual_key_relay_grants (virtual_key_id, relay_name, created_at) VALUES ('vk-1', 'tavily', ?)", time.Now()).Error)

	require.NoError(t, migrationAddRelayGrantRoutesColumn(ctx, db, testMigrationLogger))

	var routes string
	require.NoError(t, db.Table("governance_virtual_key_relay_grants").Where("virtual_key_id = ?", "vk-1").Pluck("routes", &routes).Error)
	assert.JSONEq(t, `["*"]`, routes)
}

// TestRelayGrantHashKeepsAllRoutesHashes checks an all-routes grant hashes as its relay name
// alone, the form grants had before routes, so existing config.json keys do not resync.
func TestRelayGrantHashKeepsAllRoutesHashes(t *testing.T) {
	const b, a = "bbbbbbbb-0000-4000-8000-000000000002", "aaaaaaaa-0000-4000-8000-000000000001"
	entries := RelayGrantHashEntries([]tables.TableVirtualKeyRelayGrant{
		{RelayName: "tavily"}, {RelayName: "exa", Routes: schemas.WhiteList{"*"}}, {RelayName: "textract", Routes: schemas.WhiteList{b, a}},
	})
	assert.Equal(t, []string{"exa", "tavily", "textract=" + a + "|" + b}, entries)
}
