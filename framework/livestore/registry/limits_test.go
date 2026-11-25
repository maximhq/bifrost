package registry

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// insert creates each row without touching its associations, failing the test on an error.
func insert(t *testing.T, db *gorm.DB, rows ...any) {
	t.Helper()
	for _, row := range rows {
		require.NoError(t, db.Omit(clause.Associations).Create(row).Error)
	}
}

// ptr returns a pointer to v.
func ptr[T any](v T) *T { return &v }

// budgetRow is a valid budget row with this ID and limit, holderless until the caller sets one.
func budgetRow(id string, maxLimit float64) *tables.TableBudget {
	return &tables.TableBudget{ID: id, MaxLimit: maxLimit, ResetDuration: "1d"}
}

// rateLimitRow is a valid rate limit row with this ID and request limit.
func rateLimitRow(id string, requests int64) *tables.TableRateLimit {
	return &tables.TableRateLimit{ID: id, RequestMaxLimit: ptr(requests), RequestResetDuration: ptr("1m")}
}

// limitHolders writes one holder of every kind, each holding a budget and a rate limit: a
// calendar-aligned key (directly and through a provider config), a key that is not aligned, a
// team, a customer, a model config and a provider. It also writes limits no holder here names: a
// budget held only by a business unit, a holderless budget and a rate limit nothing references.
// It returns the provider config's row ID.
func limitHolders(t *testing.T, db *gorm.DB) uint {
	t.Helper()
	insert(t, db,
		rateLimitRow("rl-key", 1), rateLimitRow("rl-pc", 2), rateLimitRow("rl-team", 3), rateLimitRow("rl-cust", 4),
		rateLimitRow("rl-mc", 5), rateLimitRow("rl-prov", 6), rateLimitRow("rl-orphan", 7),
		budgetRow("b-prov", 6), budgetRow("b-orphan", 7),
		&tables.TableCustomer{ID: "cust-a", Name: "Acme", RateLimitID: ptr("rl-cust"), CalendarAligned: true},
		&tables.TableTeam{ID: "team-a", Name: "Platform", RateLimitID: ptr("rl-team"), CalendarAligned: true},
		&tables.TableVirtualKey{ID: "vk-a", Name: "aligned", Value: *schemas.NewSecretVar("sk-a"), RateLimitID: ptr("rl-key"), CalendarAligned: true},
		&tables.TableVirtualKey{ID: "vk-b", Name: "rolling", Value: *schemas.NewSecretVar("sk-b")},
		&tables.TableModelConfig{ID: "mc-a", ModelName: "gpt-4o", RateLimitID: ptr("rl-mc"), CalendarAligned: true},
		&tables.TableProvider{Name: "openai", BudgetID: ptr("b-prov"), RateLimitID: ptr("rl-prov")},
	)
	pc := &tables.TableVirtualKeyProviderConfig{VirtualKeyID: "vk-a", Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, RateLimitID: ptr("rl-pc")}
	insert(t, db, pc)
	key, pcHeld, team, cust, mc, other, bu := budgetRow("b-key", 1), budgetRow("b-pc", 2), budgetRow("b-team", 3), budgetRow("b-cust", 4), budgetRow("b-mc", 5), budgetRow("b-key-b", 8), budgetRow("b-bu", 9)
	key.VirtualKeyID = ptr("vk-a")
	pcHeld.ProviderConfigID = ptr(pc.ID)
	team.TeamID = ptr("team-a")
	cust.CustomerID = ptr("cust-a")
	mc.ModelConfigID = ptr("mc-a")
	other.VirtualKeyID = ptr("vk-b")
	bu.BusinessUnitID = ptr("bu-1")
	insert(t, db, key, pcHeld, team, cust, mc, other, bu)
	return pc.ID
}

// budgetIDsOf lists the IDs of budget records in the order given.
func budgetIDsOf(rows []*Budget) []string {
	ids := make([]string, 0, len(rows))
	for _, b := range rows {
		ids = append(ids, BudgetID(b))
	}
	return ids
}

// rateLimitIDsOf lists the IDs of rate limit records in the order given.
func rateLimitIDsOf(rows []*RateLimit) []string {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, RateLimitID(r))
	}
	return ids
}

// TestBudgetsFindHolderAndCalendarFlag checks that pages walk every budget row in ID order, that
// each budget names its holder and takes its holder's calendar flag (a provider has none), that
// budgets no holder here names are loaded with no holder and the flag off, and what each child
// reader returns.
func TestBudgetsFindHolderAndCalendarFlag(t *testing.T) {
	ctx := context.Background()
	db := newProviderDB(t, nil).DB()
	pcID := limitHolders(t, db)

	var walked []*Budget
	after := ""
	for {
		rows, next, err := BudgetsPage(ctx, db, after, 2)
		require.NoError(t, err)
		walked = append(walked, rows...)
		if next == "" {
			break
		}
		after = next
	}
	require.Equal(t, []string{"b-bu", "b-cust", "b-key", "b-key-b", "b-mc", "b-orphan", "b-pc", "b-prov", "b-team"}, budgetIDsOf(walked),
		"every budget row in ID order, the business unit's and the holderless one included")

	byID := make(map[string]*Budget, len(walked))
	for _, b := range walked {
		byID[b.ID] = b
	}
	type holderOf struct {
		key, team, customer, modelConfig, provider string
		calendar                                   bool
	}
	want := map[string]holderOf{
		"b-key":    {key: "vk-a", calendar: true},
		"b-pc":     {key: "vk-a", calendar: true},
		"b-key-b":  {key: "vk-b", calendar: false},
		"b-team":   {team: "team-a", calendar: true},
		"b-cust":   {customer: "cust-a", calendar: true},
		"b-mc":     {modelConfig: "mc-a", calendar: true},
		"b-prov":   {provider: "openai", calendar: false},
		"b-bu":     {},
		"b-orphan": {},
	}
	for id, w := range want {
		b := byID[id]
		require.NotNil(t, b, id)
		got := holderOf{BudgetKeyID(b), BudgetTeamID(b), BudgetCustomerID(b), BudgetModelConfigID(b), BudgetProvider(b), b.IsCalendarAligned}
		require.Equal(t, w, got, id)
	}
	require.Equal(t, pcID, *byID["b-pc"].ProviderConfigID, "a provider config's budget keeps its config row")

	rows, err := BudgetsByID(ctx, db, []string{"b-key", "b-bu", "b-orphan", "missing"})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"b-bu", "b-key", "b-orphan"}, budgetIDsOf(rows), "a missing budget is left out; unheld ones are read")

	rows, err = BudgetsByKeys(ctx, db, []string{"vk-a"})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"b-key", "b-pc"}, budgetIDsOf(rows), "a key's budgets include its provider configs'")
	rows, err = BudgetsByTeams(ctx, db, []string{"team-a"})
	require.NoError(t, err)
	require.Equal(t, []string{"b-team"}, budgetIDsOf(rows))
	rows, err = BudgetsByCustomers(ctx, db, []string{"cust-a"})
	require.NoError(t, err)
	require.Equal(t, []string{"b-cust"}, budgetIDsOf(rows))
	rows, err = BudgetsByModelConfigs(ctx, db, []string{"mc-a"})
	require.NoError(t, err)
	require.Equal(t, []string{"b-mc"}, budgetIDsOf(rows))
	rows, err = BudgetsByProviders(ctx, db, []string{"openai"})
	require.NoError(t, err)
	require.Equal(t, []string{"b-prov"}, budgetIDsOf(rows))
	require.Equal(t, "openai", rows[0].Provider)
	rows, err = BudgetsByKeys(ctx, db, []string{"missing"})
	require.NoError(t, err)
	require.Empty(t, rows)
}

// TestRateLimitsFindHolderAndCalendarFlag checks rate limits the way budgets are checked: pages
// walk every rate limit row in ID order, each names its holder (found by the ID the holder names)
// with that holder's calendar flag, one nothing references is loaded with no holder and the flag
// off, and the child readers return each holder's.
func TestRateLimitsFindHolderAndCalendarFlag(t *testing.T) {
	ctx := context.Background()
	db := newProviderDB(t, nil).DB()
	limitHolders(t, db)

	var walked []*RateLimit
	after := ""
	for {
		rows, next, err := RateLimitsPage(ctx, db, after, 2)
		require.NoError(t, err)
		walked = append(walked, rows...)
		if next == "" {
			break
		}
		after = next
	}
	require.Equal(t, []string{"rl-cust", "rl-key", "rl-mc", "rl-orphan", "rl-pc", "rl-prov", "rl-team"}, rateLimitIDsOf(walked),
		"every rate limit row in ID order, the unreferenced one included")

	type holderOf struct {
		key, team, customer, modelConfig, provider string
		calendar                                   bool
	}
	want := map[string]holderOf{
		"rl-key":    {key: "vk-a", calendar: true},
		"rl-pc":     {key: "vk-a", calendar: true},
		"rl-team":   {team: "team-a", calendar: true},
		"rl-cust":   {customer: "cust-a", calendar: true},
		"rl-mc":     {modelConfig: "mc-a", calendar: true},
		"rl-prov":   {provider: "openai", calendar: false},
		"rl-orphan": {},
	}
	for _, r := range walked {
		w := want[r.ID]
		got := holderOf{RateLimitKeyID(r), RateLimitTeamID(r), RateLimitCustomerID(r), RateLimitModelConfigID(r), RateLimitProvider(r), r.IsCalendarAligned}
		require.Equal(t, w, got, r.ID)
	}

	rows, err := RateLimitsByID(ctx, db, []string{"rl-team", "rl-orphan", "missing"})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"rl-orphan", "rl-team"}, rateLimitIDsOf(rows), "a missing rate limit is left out; an unreferenced one is read")

	rows, err = RateLimitsByKeys(ctx, db, []string{"vk-a"})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"rl-key", "rl-pc"}, rateLimitIDsOf(rows), "a key's rate limits include its provider configs'")
	rows, err = RateLimitsByTeams(ctx, db, []string{"team-a"})
	require.NoError(t, err)
	require.Equal(t, []string{"rl-team"}, rateLimitIDsOf(rows))
	rows, err = RateLimitsByCustomers(ctx, db, []string{"cust-a"})
	require.NoError(t, err)
	require.Equal(t, []string{"rl-cust"}, rateLimitIDsOf(rows))
	rows, err = RateLimitsByModelConfigs(ctx, db, []string{"mc-a"})
	require.NoError(t, err)
	require.Equal(t, []string{"rl-mc"}, rateLimitIDsOf(rows))
	rows, err = RateLimitsByProviders(ctx, db, []string{"openai"})
	require.NoError(t, err)
	require.Equal(t, []string{"rl-prov"}, rateLimitIDsOf(rows))
	rows, err = RateLimitsByKeys(ctx, db, []string{"vk-b"})
	require.NoError(t, err)
	require.Empty(t, rows, "a key with no rate limit has none")
}

// postgresAdminDSN reaches the local test Postgres the config store's own tests use
// (tests/docker-compose.yml).
const postgresAdminDSN = "host=localhost user=bifrost password=bifrost_password dbname=bifrost port=5432 sslmode=disable"

// newPostgresConfigStore migrates a config store into a new, empty database on the local test
// Postgres and drops the database when the test ends. It skips the test when Postgres is not
// reachable.
func newPostgresConfigStore(t *testing.T) configstore.ConfigStore {
	t.Helper()
	admin, err := gorm.Open(postgres.Open(postgresAdminDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Skipf("local test Postgres is not reachable: %v", err)
	}
	adminDB, err := admin.DB()
	require.NoError(t, err)
	name := fmt.Sprintf("livestore_registry_%d", time.Now().UnixNano())
	require.NoError(t, admin.Exec("CREATE DATABASE "+name).Error)
	t.Cleanup(func() {
		_ = admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)").Error
		_ = adminDB.Close()
	})
	ctx := context.Background()
	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypePostgres,
		Config: &configstore.PostgresConfig{
			Host:     schemas.NewSecretVar("localhost"),
			Port:     schemas.NewSecretVar("5432"),
			User:     schemas.NewSecretVar("bifrost"),
			Password: schemas.NewSecretVar("bifrost_password"),
			DBName:   schemas.NewSecretVar(name),
			SSLMode:  schemas.NewSecretVar("disable"),
		},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close(ctx) })
	return store
}

// sqlRecorder is a gorm logger that keeps every statement it traces, with its values inlined.
type sqlRecorder struct {
	logger.Interface
	mu   sync.Mutex
	sqls []string
}

// LogMode keeps the recorder whatever level is asked for.
func (r *sqlRecorder) LogMode(logger.LogLevel) logger.Interface { return r }

// Trace records the statement.
func (r *sqlRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	r.mu.Lock()
	r.sqls = append(r.sqls, sql)
	r.mu.Unlock()
}

// TestKeyLimitReadsUseIndexesOnPostgres checks that reading a key's budgets and rate limits uses
// the indexes on Postgres. Every key write re-reads them, so a query that scans the limit tables
// costs time in proportion to every budget and rate limit stored. The key's limits and its
// provider config's are both found among 20,000 that belong to nobody.
func TestKeyLimitReadsUseIndexesOnPostgres(t *testing.T) {
	ctx := context.Background()
	db := newPostgresConfigStore(t).DB()

	budgets := make([]*tables.TableBudget, 0, 20000)
	rateLimits := make([]*tables.TableRateLimit, 0, 20000)
	for i := range 20000 {
		budgets = append(budgets, budgetRow(fmt.Sprintf("b-free-%05d", i), 1))
		rateLimits = append(rateLimits, rateLimitRow(fmt.Sprintf("rl-free-%05d", i), 1))
	}
	require.NoError(t, db.Omit(clause.Associations).CreateInBatches(budgets, 1000).Error)
	require.NoError(t, db.Omit(clause.Associations).CreateInBatches(rateLimits, 1000).Error)
	insert(t, db,
		rateLimitRow("rl-key", 1), rateLimitRow("rl-pc", 2),
		&tables.TableProvider{Name: "openai"},
		&tables.TableVirtualKey{ID: "vk-a", Name: "a", Value: *schemas.NewSecretVar("sk-a"), RateLimitID: ptr("rl-key")},
	)
	pc := &tables.TableVirtualKeyProviderConfig{VirtualKeyID: "vk-a", Provider: "openai", AllowedModels: schemas.WhiteList{"*"}, RateLimitID: ptr("rl-pc")}
	insert(t, db, pc)
	key, pcHeld := budgetRow("b-key", 1), budgetRow("b-pc", 2)
	key.VirtualKeyID = ptr("vk-a")
	pcHeld.ProviderConfigID = ptr(pc.ID)
	insert(t, db, key, pcHeld)
	require.NoError(t, db.Exec("ANALYZE").Error)

	rec := &sqlRecorder{Interface: logger.Default.LogMode(logger.Silent)}
	traced := db.Session(&gorm.Session{Logger: rec})
	gotBudgets, err := BudgetsByKeys(ctx, traced, []string{"vk-a"})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"b-key", "b-pc"}, budgetIDsOf(gotBudgets))
	gotRateLimits, err := RateLimitsByKeys(ctx, traced, []string{"vk-a"})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"rl-key", "rl-pc"}, rateLimitIDsOf(gotRateLimits))

	checked := 0
	for _, sql := range rec.sqls {
		if !strings.Contains(sql, `FROM "governance_budgets"`) && !strings.Contains(sql, `FROM "governance_rate_limits"`) {
			continue
		}
		var plan []string
		require.NoError(t, db.Raw("EXPLAIN "+sql).Scan(&plan).Error)
		joined := strings.Join(plan, "\n")
		require.NotContains(t, joined, "Seq Scan on governance_budgets", "%s\n%s", sql, joined)
		require.NotContains(t, joined, "Seq Scan on governance_rate_limits", "%s\n%s", sql, joined)
		checked++
	}
	require.NotZero(t, checked, "no read of the limit tables was traced")

	// The owners of the limits read are found in one query; its budget branches start from the
	// budgets read, so they too must use the primary key rather than scan every budget.
	owners := 0
	for _, sql := range rec.sqls {
		if !strings.Contains(sql, "UNION ALL") {
			continue
		}
		var plan []string
		require.NoError(t, db.Raw("EXPLAIN "+sql).Scan(&plan).Error)
		joined := strings.Join(plan, "\n")
		require.NotContains(t, joined, "Seq Scan on governance_budgets", "%s\n%s", sql, joined)
		require.NotContains(t, joined, "Seq Scan on governance_rate_limits", "%s\n%s", sql, joined)
		owners++
	}
	require.NotZero(t, owners, "no owners read was traced")
}

// TestNumericIDIsTheNumberZeroPaddedTo20Digits pins NumericID to the zero-padded decimal record IDs
// have always had, from zero to the largest uint, with one allocation per call.
func TestNumericIDIsTheNumberZeroPaddedTo20Digits(t *testing.T) {
	for _, id := range []uint{0, 1, 9, 10, 42, 1234567890, 1<<53 + 1, ^uint(0)} {
		require.Equal(t, fmt.Sprintf("%020d", id), NumericID(id))
	}
	require.LessOrEqual(t, testing.AllocsPerRun(100, func() { _ = NumericID(1234567890) }), float64(1))
}
