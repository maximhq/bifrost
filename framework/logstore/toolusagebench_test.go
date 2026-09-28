package logstore

// TEMPORARY benchmark for the tool_usage backfill; not for commit.

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const benchTokenUsage = `{"prompt_tokens":1532,"prompt_tokens_details":{"text_tokens":1532,"cached_read_tokens":1024,"cached_write_tokens":0,"cached_tokens":1024,"cache_write_tokens":0},"completion_tokens":412,"completion_tokens_details":{"text_tokens":284,"reasoning_tokens":128,"num_search_queries":2},"total_tokens":1944,"cost":{"input_cost":0.00213,"input_cost_details":{"text_cost":0.00213},"output_cost":0.02618,"output_cost_details":{"text_cost":0.00618,"search_queries_cost":0.02},"total_cost":0.02831}}`

func benchRows(t *testing.T) int {
	if os.Getenv("BIFROST_TOOLUSAGE_BENCH") != "1" {
		t.Skip("set BIFROST_TOOLUSAGE_BENCH=1")
	}
	n := 10_000_000
	if v := os.Getenv("BIFROST_TOOLUSAGE_BENCH_ROWS"); v != "" {
		n, _ = strconv.Atoi(v)
	}
	return n
}

type latencies struct {
	mu   sync.Mutex
	vals map[string][]time.Duration
	errs map[string]*int64
}

func newLatencies() *latencies {
	return &latencies{vals: map[string][]time.Duration{}, errs: map[string]*int64{}}
}

func (l *latencies) observe(op string, start time.Time, err error, t *testing.T) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.vals[op] = append(l.vals[op], time.Since(start))
	if l.errs[op] == nil {
		l.errs[op] = new(int64)
	}
	if err != nil {
		if atomic.AddInt64(l.errs[op], 1) <= 3 {
			t.Logf("LOAD ERROR %s: %v", op, err)
		}
	}
}

func (l *latencies) report(t *testing.T) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for op, v := range l.vals {
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
		p := func(q float64) time.Duration { return v[int(float64(len(v)-1)*q)] }
		t.Logf("LOAD %-18s n=%-6d errors=%-3d p50=%-10v p99=%-10v max=%v", op, len(v), *l.errs[op], p(0.5), p(0.99), v[len(v)-1])
	}
}

// runLoad drives normal gateway traffic against store until stop is closed.
func runLoad(t *testing.T, store LogStore, oldIDs func() string, stop <-chan struct{}, lat *latencies) *sync.WaitGroup {
	var wg sync.WaitGroup
	loop := func(every time.Duration, fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tk := time.NewTicker(every)
			defer tk.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tk.C:
					fn()
				}
			}
		}()
	}
	ctx := context.Background()
	loop(10*time.Millisecond, func() {
		id := uuid.NewString()
		now := time.Now().UTC()
		s := time.Now()
		err := store.CreateIfNotExists(ctx, &Log{ID: id, Timestamp: now, Object: "chat.completion", Provider: "openai", Model: "gpt-4o", Status: "processing", CreatedAt: now})
		lat.observe("insert-new", s, err, t)
		s = time.Now()
		err = store.Update(ctx, id, map[string]interface{}{"status": "success", "token_usage": benchTokenUsage})
		lat.observe("update-new", s, err, t)
	})
	loop(50*time.Millisecond, func() {
		s := time.Now()
		err := store.Update(ctx, oldIDs(), map[string]interface{}{"status": "success"})
		if err == ErrNotFound {
			err = nil
		}
		lat.observe("update-old-row", s, err, t)
	})
	loop(200*time.Millisecond, func() {
		s := time.Now()
		_, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 50})
		lat.observe("search-logs", s, err, t)
	})
	loop(500*time.Millisecond, func() {
		s := time.Now()
		_, err := store.GetStats(ctx, SearchFilters{})
		lat.observe("get-stats", s, err, t)
	})
	return &wg
}

func TestBenchToolUsageBackfillPostgres(t *testing.T) {
	n := benchRows(t)
	ctx := context.Background()
	admin, err := gorm.Open(postgres.Open("host=localhost user=bifrost password=bifrost_password dbname=bifrost port=5432 sslmode=disable"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, admin.Exec("DROP SCHEMA IF EXISTS toolusage_bench CASCADE").Error)
	require.NoError(t, admin.Exec("CREATE SCHEMA toolusage_bench").Error)
	db, err := gorm.Open(postgres.Open("host=localhost user=bifrost password=bifrost_password dbname=bifrost port=5432 sslmode=disable search_path=toolusage_bench"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(20)
	require.NoError(t, triggerMigrations(ctx, db, testLogger{}))

	seed := time.Now()
	const chunk = 1_000_000
	for off := 0; off < n; off += chunk {
		hi := min(off+chunk, n)
		require.NoError(t, db.Exec(`
			INSERT INTO logs (id, timestamp, object_type, provider, model, status, created_at, prompt_tokens, completion_tokens, total_tokens, token_usage, output_message)
			SELECT md5(g::text), now() - (g || ' seconds')::interval, 'chat.completion', 'openai', 'gpt-4o', 'success', now(), 1532, 412, 1944, ?,
			       '{"role":"assistant","content":"' || repeat(md5(g::text) || md5((g+1)::text), 30) || '"}'
			FROM generate_series(?::bigint, ?::bigint) g`, benchTokenUsage, off+1, hi).Error)
	}
	require.NoError(t, db.Exec("VACUUM ANALYZE logs").Error)
	var sizeBefore string
	db.Raw("SELECT pg_size_pretty(pg_total_relation_size('logs'))").Scan(&sizeBefore)
	t.Logf("PG seeded %d rows in %v; logs total size %s", n, time.Since(seed), sizeBefore)

	store := &RDBLogStore{db: db, logger: testLogger{}}
	pgOld := func() string { return md5Hex(rand.Intn(n) + 1) }
	base := newLatencies()
	stopBase := make(chan struct{})
	wgBase := runLoad(t, store, pgOld, stopBase, base)
	time.Sleep(60 * time.Second)
	close(stopBase)
	wgBase.Wait()
	t.Log("BASELINE (load only, no backfill):")
	base.report(t)

	lat := newLatencies()
	stop := make(chan struct{})
	wg := runLoad(t, store, pgOld, stop, lat)
	go func() { time.Sleep(60 * time.Second); close(stop) }()

	start := time.Now()
	require.NoError(t, ensureToolUsageWebSearchBackfill(ctx, db))
	elapsed := time.Since(start)
	wg.Wait()
	t.Log("DURING BACKFILL (first 60s):")

	var migrated, remaining int64
	db.Raw(`SELECT count(*) FROM logs WHERE token_usage LIKE '%"tool_usage"%'`).Scan(&migrated)
	db.Raw(`SELECT count(*) FROM logs WHERE token_usage LIKE '%"num_search_queries"%' AND token_usage NOT LIKE '%"tool_usage"%'`).Scan(&remaining)
	var sizeAfter string
	db.Raw("SELECT pg_size_pretty(pg_total_relation_size('logs'))").Scan(&sizeAfter)
	t.Logf("PG backfill: %d rows in %v (%.0f rows/s); rows with tool_usage=%d remaining legacy=%d; size %s -> %s",
		n, elapsed, float64(n)/elapsed.Seconds(), migrated, remaining, sizeBefore, sizeAfter)
	lat.report(t)

	rescan := time.Now()
	require.NoError(t, backfillToolUsageWebSearch(ctx, db))
	t.Logf("PG no-op rescan of already-migrated table: %v", time.Since(rescan))
}

func md5Hex(i int) string {
	var s string
	_ = gormMD5(&s, i)
	return s
}

var benchPG *gorm.DB

func gormMD5(out *string, i int) error {
	if benchPG == nil {
		benchPG, _ = gorm.Open(postgres.Open("host=localhost user=bifrost password=bifrost_password dbname=bifrost port=5432 sslmode=disable"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	}
	return benchPG.Raw("SELECT md5(?::text)", strconv.Itoa(i)).Scan(out).Error
}

func TestBenchToolUsageBackfillClickHouse(t *testing.T) {
	n := benchRows(t)
	store := trySetupClickHouseStore(t)
	benchCH = store.db
	ctx := context.Background()

	seed := time.Now()
	const chunk = 1_000_000
	for off := 0; off < n; off += chunk {
		hi := min(off+chunk, n)
		require.NoError(t, store.db.Exec(fmt.Sprintf(`
			INSERT INTO logs (id, timestamp, object_type, provider, model, status, created_at, prompt_tokens, completion_tokens, total_tokens, token_usage, output_message)
			SELECT toString(cityHash64(number)), now64(3) - toIntervalMillisecond(number * 37), 'chat.completion', 'openai', 'gpt-4o', 'success', now64(3), 1532, 412, 1944, '%s',
			       concat('{"role":"assistant","content":"', repeat(hex(MD5(toString(number))), 30), '"}')
			FROM numbers(%d, %d)`, benchTokenUsage, off, hi-off)).Error)
	}
	store.db.Exec("OPTIMIZE TABLE logs FINAL")
	var parts int64
	var sizeBefore string
	store.db.Raw("SELECT count() FROM system.parts WHERE table = 'logs' AND active").Scan(&parts)
	store.db.Raw("SELECT formatReadableSize(sum(bytes_on_disk)) FROM system.parts WHERE table = 'logs' AND active").Scan(&sizeBefore)
	t.Logf("CH seeded %d rows in %v; active parts=%d size=%s", n, time.Since(seed), parts, sizeBefore)

	chOld := func() string { return strconv.FormatUint(cityHash(rand.Intn(n)), 10) }
	base := newLatencies()
	stopBase := make(chan struct{})
	wgBase := runLoad(t, store, chOld, stopBase, base)
	time.Sleep(60 * time.Second)
	close(stopBase)
	wgBase.Wait()
	t.Log("BASELINE (load only, no backfill):")
	base.report(t)

	lat := newLatencies()
	stop := make(chan struct{})
	wg := runLoad(t, store, chOld, stop, lat)
	go func() { time.Sleep(60 * time.Second); close(stop) }()

	start := time.Now()
	err := store.backfillToolUsageWebSearch(ctx)
	elapsed := time.Since(start)
	wg.Wait()
	t.Log("DURING BACKFILL (first 60s):")
	if err != nil {
		t.Logf("CH backfill ERROR after %v: %v", elapsed, err)
	}

	var migrated, remaining int64
	store.db.Raw(`SELECT count() FROM logs WHERE token_usage LIKE '%"tool_usage"%'`).Scan(&migrated)
	store.db.Raw(`SELECT count() FROM logs WHERE token_usage LIKE '%"num_search_queries"%' AND token_usage NOT LIKE '%"tool_usage"%'`).Scan(&remaining)
	var sizeAfter string
	store.db.Raw("SELECT count() FROM system.parts WHERE table = 'logs' AND active").Scan(&parts)
	store.db.Raw("SELECT formatReadableSize(sum(bytes_on_disk)) FROM system.parts WHERE table = 'logs' AND active").Scan(&sizeAfter)
	t.Logf("CH backfill: %d rows in %v (%.0f rows/s); rows with tool_usage=%d remaining legacy=%d; active parts=%d size %s -> %s",
		n, elapsed, float64(n)/elapsed.Seconds(), migrated, remaining, parts, sizeBefore, sizeAfter)
	lat.report(t)

	rescan := time.Now()
	require.NoError(t, store.backfillToolUsageWebSearch(ctx))
	t.Logf("CH no-op rescan of already-migrated table (runs every boot): %v", time.Since(rescan))
}

var benchCH *gorm.DB

func cityHash(i int) uint64 {
	var v uint64
	benchCH.Raw("SELECT cityHash64(toUInt64(?))", i).Scan(&v)
	return v
}
