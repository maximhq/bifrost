package configstore

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// vkMetadataPGSchema keeps these tests' tables apart from the shared test schema, so the whole
// schema can be dropped afterwards.
const vkMetadataPGSchema = "configstore_vk_metadata_test"

// vkMetadataStoreBackends returns the SQLite store and, when the local test Postgres is reachable,
// a Postgres store with the virtual key tables. The metadata filter is dialect-specific SQL, so
// both must agree.
func vkMetadataStoreBackends(t *testing.T) map[string]*RDBConfigStore {
	t.Helper()
	backends := map[string]*RDBConfigStore{"sqlite": setupRDBTestStore(t)}
	if pg := trySetupPostgresVKMetadataStore(t); pg != nil {
		backends["postgres"] = pg
	}
	return backends
}

// trySetupPostgresVKMetadataStore returns a Postgres store in its own schema, or nil when the
// local test Postgres is not reachable. Once the server answers, any setup error (schema
// creation, migration) fails the test instead of quietly dropping the Postgres backend, so the
// dialect-specific metadata filter cannot go untested while the run still passes on SQLite.
func trySetupPostgresVKMetadataStore(t *testing.T) *RDBConfigStore {
	t.Helper()
	db, err := gorm.Open(postgres.Open(strings.Replace(postgresDSN, pgTestSchema, vkMetadataPGSchema, 1)), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil
	}
	if sqlDB.Ping() != nil {
		_ = sqlDB.Close()
		return nil
	}
	// Registered before the schema statements so the connection is closed even if they fail.
	t.Cleanup(func() {
		_ = db.Exec("DROP SCHEMA IF EXISTS " + vkMetadataPGSchema + " CASCADE").Error
		_ = sqlDB.Close()
	})
	require.NoError(t, db.Exec("DROP SCHEMA IF EXISTS "+vkMetadataPGSchema+" CASCADE").Error)
	require.NoError(t, db.Exec("CREATE SCHEMA "+vkMetadataPGSchema).Error)
	require.NoError(t, db.AutoMigrate(
		&tables.TableCustomer{},
		&tables.TableTeam{},
		&tables.TableBudget{},
		&tables.TableRateLimit{},
		&tables.TableProvider{},
		&tables.TableKey{},
		&tables.TableMCPClient{},
		&tables.TableVirtualKey{},
		&tables.TableVirtualKeyProviderConfig{},
		&tables.TableVirtualKeyProviderConfigKey{},
		&tables.TableVirtualKeyMCPConfig{},
	))
	require.NoError(t, db.SetupJoinTable(&tables.TableVirtualKeyProviderConfig{}, "Keys", &tables.TableVirtualKeyProviderConfigKey{}))
	s := &RDBConfigStore{logger: nil}
	s.db.Store(db)
	s.migrateOnFreshFn = func(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
		return fn(ctx, s.DB())
	}
	s.refreshPoolFn = func(ctx context.Context) error { return nil }
	return s
}

func TestGetVirtualKeysPaginated_MetadataFiltersAndSearch(t *testing.T) {
	for name, store := range vkMetadataStoreBackends(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			seed := []*tables.TableVirtualKey{
				{ID: "vk-md-a", Name: "alpha", Value: *schemas.NewSecretVar("vk-md-a-val"), Metadata: map[string]string{"cost_center": "cc-42", "env": "prod"}},
				{ID: "vk-md-b", Name: "bravo", Value: *schemas.NewSecretVar("vk-md-b-val"), Metadata: map[string]string{"cost_center": "cc-42", "env": "dev"}},
				{ID: "vk-md-c", Name: "charlie", Value: *schemas.NewSecretVar("vk-md-c-val"), Metadata: map[string]string{"cost_center": "cc-7", "owner.email": "Ops@Example.com", "quote": `say "hi"`}},
				{ID: "vk-md-none", Name: "delta", Value: *schemas.NewSecretVar("vk-md-none-val")},
			}
			for _, vk := range seed {
				require.NoError(t, store.CreateVirtualKey(ctx, vk))
			}

			tests := []struct {
				name    string
				params  VirtualKeyQueryParams
				wantIDs []string
			}{
				{name: "one pair", params: VirtualKeyQueryParams{MetadataFilters: map[string]string{"cost_center": "cc-42"}}, wantIDs: []string{"vk-md-a", "vk-md-b"}},
				{name: "pairs AND together", params: VirtualKeyQueryParams{MetadataFilters: map[string]string{"cost_center": "cc-42", "env": "prod"}}, wantIDs: []string{"vk-md-a"}},
				{name: "dotted key", params: VirtualKeyQueryParams{MetadataFilters: map[string]string{"owner.email": "Ops@Example.com"}}, wantIDs: []string{"vk-md-c"}},
				{name: "value with quotes", params: VirtualKeyQueryParams{MetadataFilters: map[string]string{"quote": `say "hi"`}}, wantIDs: []string{"vk-md-c"}},
				{name: "value match is exact", params: VirtualKeyQueryParams{MetadataFilters: map[string]string{"cost_center": "cc-4"}}, wantIDs: nil},
				{name: "unknown key", params: VirtualKeyQueryParams{MetadataFilters: map[string]string{"team": "x"}}, wantIDs: nil},
				{name: "invalid key matches nothing", params: VirtualKeyQueryParams{MetadataFilters: map[string]string{`a" OR 1=1 --`: "x"}}, wantIDs: nil},
				{name: "filter narrows search", params: VirtualKeyQueryParams{Search: "a", MetadataFilters: map[string]string{"env": "dev"}}, wantIDs: []string{"vk-md-b"}},
				{name: "search matches metadata value", params: VirtualKeyQueryParams{Search: "cc-7"}, wantIDs: []string{"vk-md-c"}},
				{name: "search on metadata is case-insensitive", params: VirtualKeyQueryParams{Search: "ops@example"}, wantIDs: []string{"vk-md-c"}},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					vks, total, err := store.GetVirtualKeysPaginated(ctx, tt.params)
					require.NoError(t, err)
					var got []string
					for _, vk := range vks {
						got = append(got, vk.ID)
					}
					sort.Strings(got)
					assert.Equal(t, tt.wantIDs, got)
					assert.Equal(t, int64(len(tt.wantIDs)), total)
				})
			}
		})
	}
}
