package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	scaleMigrationTestEnv     = "BIFROST_SCALE_MIGRATION_TEST"
	scaleMigrationRowsEnv     = "BIFROST_SCALE_MIGRATION_ROWS"
	scaleMigrationDefaultRows = 10_000_000
)

type lockWaitSample struct {
	ObservedAt      time.Time
	WaitingPID      int
	WaitingDuration time.Duration
	WaitingQuery    string
	BlockingPIDs    string
}

func TestScalePostgresLogstoreMigrations(t *testing.T) {
	if os.Getenv(scaleMigrationTestEnv) != "1" {
		t.Skipf("set %s=1 to run the destructive large Postgres migration test", scaleMigrationTestEnv)
	}

	rows := scaleMigrationDefaultRows
	if raw := os.Getenv(scaleMigrationRowsEnv); raw != "" {
		parsed, err := strconv.Atoi(raw)
		require.NoError(t, err, "invalid %s", scaleMigrationRowsEnv)
		require.Positive(t, parsed, "%s must be positive", scaleMigrationRowsEnv)
		rows = parsed
	}

	db := trySetupPostgresDB(t)
	require.NotNil(t, db, "Postgres must be reachable on %s", postgresDSN)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()

	resetScaleMigrationDB(t, ctx, db)
	createScaleMigrationTables(t, ctx, db)
	populateScaleMigrationTables(t, ctx, db, rows)
	createOldShapeScaleMatViews(t, ctx, db)

	sqlDB, err := db.DB()
	require.NoError(t, err)

	monitorCtx, stopMonitor := context.WithCancel(ctx)
	var sampleMu sync.Mutex
	var samples []lockWaitSample
	var monitorWG sync.WaitGroup
	monitorWG.Add(1)
	go func() {
		defer monitorWG.Done()
		monitorScaleMigrationLocks(monitorCtx, t, sqlDB, &sampleMu, &samples)
	}()
	defer monitorWG.Wait()
	defer stopMonitor()

	start := time.Now()
	require.NoError(t, triggerMigrations(ctx, db, testLogger{}), "logstore migrations should complete at scale")
	migrationElapsed := time.Since(start)

	conn := acquireScaleMigrationConn(t, ctx, sqlDB)
	defer conn.Close()

	start = time.Now()
	require.NoError(t, ensureMetadataGINIndex(ctx, conn), "metadata GIN index should be maintained at scale")
	require.NoError(t, ensureDashboardEnhancements(ctx, conn), "dashboard enhancements should be maintained at scale")
	require.NoError(t, ensurePerformanceIndexes(ctx, conn, testLogger{}), "performance indexes should be maintained at scale")
	indexElapsed := time.Since(start)

	start = time.Now()
	require.NoError(t, ensureMatViews(ctx, db), "matviews should be maintained at scale")
	matviewElapsed := time.Since(start)

	stopMonitor()
	monitorWG.Wait()

	t.Logf("scale rows per table: %d", rows)
	t.Logf("migration elapsed: %s", migrationElapsed)
	t.Logf("index/background maintenance elapsed: %s", indexElapsed)
	t.Logf("matview maintenance elapsed: %s", matviewElapsed)

	sampleMu.Lock()
	defer sampleMu.Unlock()
	if len(samples) == 0 {
		t.Log("no lock waits observed by sampler")
		return
	}
	t.Logf("observed %d lock-wait samples", len(samples))
	for i, sample := range samples {
		if i >= 20 {
			t.Logf("... %d additional lock-wait samples omitted", len(samples)-i)
			break
		}
		t.Logf(
			"lock wait at %s pid=%d duration=%s blockers=%s query=%q",
			sample.ObservedAt.Format(time.RFC3339),
			sample.WaitingPID,
			sample.WaitingDuration,
			sample.BlockingPIDs,
			sample.WaitingQuery,
		)
	}
}

// TestScalePostgresEmbeddingInputBackfill benchmarks ensureEmbeddingInputBackfill
// against a realistic slice of the scale dataset (~1/8 of rows are embedding
// logs with input_history to reconstruct, matching a 10M-log/~1M-embedding
// deployment) and proves the batched rewrite does not stall concurrent queries
// against logs the way the old single-transaction DO block would have.
func TestScalePostgresEmbeddingInputBackfill(t *testing.T) {
	if os.Getenv(scaleMigrationTestEnv) != "1" {
		t.Skipf("set %s=1 to run the destructive large Postgres migration test", scaleMigrationTestEnv)
	}

	rows := scaleMigrationDefaultRows
	if raw := os.Getenv(scaleMigrationRowsEnv); raw != "" {
		parsed, err := strconv.Atoi(raw)
		require.NoError(t, err, "invalid %s", scaleMigrationRowsEnv)
		require.Positive(t, parsed, "%s must be positive", scaleMigrationRowsEnv)
		rows = parsed
	}

	db := trySetupPostgresDB(t)
	require.NotNil(t, db, "Postgres must be reachable on %s", postgresDSN)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()

	resetScaleMigrationDB(t, ctx, db)
	createScaleMigrationTables(t, ctx, db)
	populateScaleMigrationTables(t, ctx, db, rows)
	// Adds the embedding_input column only; the backfill itself is not part of
	// the ledger-tracked migration pipeline (see ensureEmbeddingInputBackfill's
	// doc comment) and is timed directly below.
	require.NoError(t, triggerMigrations(ctx, db, testLogger{}), "logstore migrations should complete at scale")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	conn := acquireScaleMigrationConn(t, ctx, sqlDB)
	defer conn.Close()

	monitorCtx, stopMonitor := context.WithCancel(ctx)
	var sampleMu sync.Mutex
	var samples []lockWaitSample
	var monitorWG sync.WaitGroup
	monitorWG.Add(1)
	go func() {
		defer monitorWG.Done()
		monitorScaleMigrationLocks(monitorCtx, t, sqlDB, &sampleMu, &samples)
	}()

	// A concurrent point-query workload against the same table: proves the
	// batching actually stops the backfill from starving other queries, which
	// is the entire point of the fix (the old single-DO-block implementation
	// held row locks and grew one transaction across every candidate row).
	// maxLatency is only ever touched by this one goroutine until workloadWG.Wait()
	// below synchronizes the main goroutine's read with its last write.
	workloadCtx, stopWorkload := context.WithCancel(ctx)
	var maxLatency time.Duration
	var workloadWG sync.WaitGroup
	workloadWG.Add(1)
	go func() {
		defer workloadWG.Done()
		for workloadCtx.Err() == nil {
			start := time.Now()
			var count int64
			_ = db.WithContext(workloadCtx).Raw("SELECT count(*) FROM logs WHERE id = 'log-12345'").Scan(&count)
			if d := time.Since(start); d > maxLatency {
				maxLatency = d
			}
		}
	}()

	start := time.Now()
	require.NoError(t, ensureEmbeddingInputBackfill(ctx, conn), "embedding_input backfill should complete at scale")
	backfillElapsed := time.Since(start)

	stopWorkload()
	workloadWG.Wait()
	stopMonitor()
	monitorWG.Wait()

	t.Logf("scale rows: %d (~%d embedding candidates)", rows, rows/8)
	t.Logf("embedding_input backfill elapsed: %s", backfillElapsed)
	t.Logf("max concurrent point-query latency during backfill: %s", maxLatency)
	assert.Less(t, maxLatency, 2*time.Second, "no page should hold locks long enough to stall a concurrent point query")

	var remaining int64
	require.NoError(t, db.WithContext(ctx).Raw(`
		SELECT count(*) FROM logs
		WHERE object_type = 'embedding' AND input_history IS NOT NULL
		  AND input_history NOT IN ('', '[]', 'null') AND embedding_input IS NULL
	`).Scan(&remaining).Error)
	assert.Zero(t, remaining, "every candidate row should be backfilled")

	if len(samples) > 0 {
		t.Logf("observed %d lock-wait samples", len(samples))
		for i, sample := range samples {
			if i >= 20 {
				t.Logf("... %d additional lock-wait samples omitted", len(samples)-i)
				break
			}
			assert.Less(t, sample.WaitingDuration, 5*time.Second, "no observed lock wait should approach the old whole-table-transaction scale")
			t.Logf("lock wait at %s pid=%d duration=%s blockers=%s query=%q",
				sample.ObservedAt.Format(time.RFC3339), sample.WaitingPID, sample.WaitingDuration, sample.BlockingPIDs, sample.WaitingQuery)
		}
	}

	// Re-run should find nothing left to do and complete quickly.
	start = time.Now()
	require.NoError(t, ensureEmbeddingInputBackfill(ctx, conn))
	t.Logf("idempotent re-run elapsed: %s", time.Since(start))
}

// scaleMigrationClickHouseDefaultRows is deliberately smaller than
// scaleMigrationDefaultRows (10M, Postgres): there is no prior ClickHouse
// scale-test precedent to match, and the RMW+reinsert backfill's per-chunk
// cost profile is already covered by bulkUpdateCostChunkSize's existing
// tuning, so 1M rows (matching the user-stated ~1M embedding-row scale) is
// enough to validate throughput without an unnecessarily long default run.
const scaleMigrationClickHouseDefaultRows = 1_000_000

// TestScaleClickHouseEmbeddingInputBackfill benchmarks the ClickHouse
// read-modify-write-and-reinsert embedding_input backfill against a bulk-seeded
// dataset where ~1/8 of rows are embedding logs with input_history to
// reconstruct, mirroring the Postgres scale test above.
func TestScaleClickHouseEmbeddingInputBackfill(t *testing.T) {
	if os.Getenv(scaleMigrationTestEnv) != "1" {
		t.Skipf("set %s=1 to run the large ClickHouse embedding_input backfill test", scaleMigrationTestEnv)
	}

	rows := scaleMigrationClickHouseDefaultRows
	if raw := os.Getenv(scaleMigrationRowsEnv); raw != "" {
		parsed, err := strconv.Atoi(raw)
		require.NoError(t, err, "invalid %s", scaleMigrationRowsEnv)
		require.Positive(t, parsed, "%s must be positive", scaleMigrationRowsEnv)
		rows = parsed
	}

	store := trySetupClickHouseStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()

	t.Logf("seeding %d clickhouse rows (~%d embedding candidates)", rows, rows/8)
	seedSQL := fmt.Sprintf(`
INSERT INTO logs (id, timestamp, object_type, provider, model, input_history, embedding_input, status, created_at, latency, cost)
SELECT
	concat('ch-scale-', toString(number)),
	now() - toIntervalMinute(number %% 129600),
	if(number %% 8 = 4, 'embedding', 'chat.completion'),
	'openai', 'gpt-4o',
	if(number %% 8 = 4, concat('[{"role":"user","content":[{"type":"text","text":"embedding text ', toString(number), '"}]}]'), ''),
	'', 'success', now(), 1.0, 0.001
FROM numbers(%d)`, rows)
	require.NoError(t, store.db.WithContext(ctx).Exec(seedSQL).Error)

	start := time.Now()
	require.NoError(t, store.backfillEmbeddingInput(ctx), "embedding_input backfill should complete at scale")
	elapsed := time.Since(start)
	t.Logf("clickhouse embedding_input backfill elapsed: %s for %d rows", elapsed, rows)

	var remaining int64
	require.NoError(t, store.db.WithContext(ctx).Raw(`
		SELECT count(*) FROM logs
		WHERE object_type = 'embedding' AND input_history NOT IN ('', '[]', 'null') AND embedding_input = ''
	`).Scan(&remaining).Error)
	assert.Zero(t, remaining, "every candidate row should be backfilled")

	start = time.Now()
	require.NoError(t, store.backfillEmbeddingInput(ctx))
	t.Logf("idempotent re-run elapsed: %s", time.Since(start))
}

func resetScaleMigrationDB(t *testing.T, ctx context.Context, db *gorm.DB) {
	t.Helper()
	statements := []string{
		"DROP MATERIALIZED VIEW IF EXISTS mv_logs_hourly CASCADE",
		"DROP MATERIALIZED VIEW IF EXISTS mv_logs_filterdata CASCADE",
		"DROP TABLE IF EXISTS mcp_tool_logs CASCADE",
		"DROP TABLE IF EXISTS async_jobs CASCADE",
		"DROP TABLE IF EXISTS logs CASCADE",
		"CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)",
		"DELETE FROM migrations",
	}
	for _, stmt := range statements {
		require.NoError(t, db.WithContext(ctx).Exec(stmt).Error, stmt)
	}
}

func createScaleMigrationTables(t *testing.T, ctx context.Context, db *gorm.DB) {
	t.Helper()
	logsSQL := `
CREATE UNLOGGED TABLE logs (
	id VARCHAR(255) NOT NULL,
	parent_request_id VARCHAR(255),
	timestamp TIMESTAMP NOT NULL,
	object_type VARCHAR(255) NOT NULL,
	provider VARCHAR(255) NOT NULL,
	model VARCHAR(255) NOT NULL,
	number_of_retries INTEGER DEFAULT 0,
	fallback_index INTEGER DEFAULT 0,
	selected_key_id VARCHAR(255),
	selected_key_name VARCHAR(255),
	virtual_key_id VARCHAR(255),
	virtual_key_name VARCHAR(255),
	routing_engines_used VARCHAR(255),
	routing_rule_id VARCHAR(255),
	routing_rule_name VARCHAR(255),
	input_history TEXT,
	responses_input_history TEXT,
	output_message TEXT,
	responses_output TEXT,
	embedding_output TEXT,
	rerank_output TEXT,
	ocr_output TEXT,
	params TEXT,
	tools TEXT,
	tool_calls TEXT,
	speech_input TEXT,
	transcription_input TEXT,
	image_generation_input TEXT,
	speech_output TEXT,
	transcription_output TEXT,
	image_generation_output TEXT,
	list_models_output TEXT,
	cache_debug TEXT,
	latency DOUBLE PRECISION,
	token_usage TEXT,
	cost DOUBLE PRECISION,
	status VARCHAR(50) NOT NULL,
	error_details TEXT,
	stream BOOLEAN DEFAULT FALSE,
	content_summary TEXT,
	raw_request TEXT,
	raw_response TEXT,
	passthrough_request_body TEXT,
	passthrough_response_body TEXT,
	routing_engine_logs TEXT,
	metadata TEXT,
	is_large_payload_request BOOLEAN DEFAULT FALSE,
	is_large_payload_response BOOLEAN DEFAULT FALSE,
	prompt_tokens INTEGER DEFAULT 0,
	completion_tokens INTEGER DEFAULT 0,
	total_tokens INTEGER DEFAULT 0,
	cached_read_tokens INTEGER DEFAULT 0,
	created_at TIMESTAMP NOT NULL
)`
	require.NoError(t, db.WithContext(ctx).Exec(logsSQL).Error)

	mcpSQL := `
CREATE UNLOGGED TABLE mcp_tool_logs (
	id VARCHAR(255) NOT NULL,
	request_id VARCHAR(255),
	llm_request_id VARCHAR(255),
	timestamp TIMESTAMP NOT NULL,
	tool_name VARCHAR(255) NOT NULL,
	server_label VARCHAR(255),
	virtual_key_id VARCHAR(255),
	virtual_key_name VARCHAR(255),
	arguments TEXT,
	result TEXT,
	error_details TEXT,
	latency DOUBLE PRECISION,
	cost DOUBLE PRECISION,
	status VARCHAR(50) NOT NULL,
	metadata TEXT,
	has_object BOOLEAN DEFAULT FALSE,
	created_at TIMESTAMP NOT NULL
)`
	require.NoError(t, db.WithContext(ctx).Exec(mcpSQL).Error)
}

func populateScaleMigrationTables(t *testing.T, ctx context.Context, db *gorm.DB, rows int) {
	t.Helper()
	t.Logf("populating %d logs and %d mcp_tool_logs rows", rows, rows)
	require.NoError(t, db.WithContext(ctx).Exec("SET synchronous_commit = off").Error)

	insertLogsSQL := `
INSERT INTO logs (
	id, timestamp, object_type, provider, model,
	parent_request_id, selected_key_id, selected_key_name, virtual_key_id, virtual_key_name,
	routing_engines_used, routing_rule_id, routing_rule_name,
	latency, token_usage, cost, status, stream, content_summary, metadata,
	prompt_tokens, completion_tokens, total_tokens, cached_read_tokens, created_at, input_history
)
SELECT
	'log-' || gs,
	NOW() - ((gs % 129600) * INTERVAL '1 minute'),
	CASE gs % 8
		WHEN 0 THEN 'chat.completion'
		WHEN 1 THEN 'chat_completion'
		WHEN 2 THEN 'text.completion'
		WHEN 3 THEN 'responses'
		WHEN 4 THEN 'embedding'
		WHEN 5 THEN 'audio.speech'
		WHEN 6 THEN 'chat.completion.chunk'
		ELSE 'chat_completion_stream'
	END,
	CASE gs % 4 WHEN 0 THEN 'openai' WHEN 1 THEN 'anthropic' WHEN 2 THEN 'bedrock' ELSE 'gemini' END,
	CASE gs % 5 WHEN 0 THEN 'gpt-4o' WHEN 1 THEN 'claude-3-5-sonnet' WHEN 2 THEN 'nova-pro' WHEN 3 THEN 'gemini-1.5-pro' ELSE 'gpt-4o-mini' END,
	CASE WHEN gs % 10 = 0 THEN 'parent-' || (gs / 10) ELSE NULL END,
	'key-' || (gs % 1000),
	'key-name-' || (gs % 1000),
	'vk-' || (gs % 10000),
	'vk-name-' || (gs % 10000),
	CASE WHEN gs % 3 = 0 THEN 'routing-rule,governance' ELSE 'loadbalancing' END,
	'rule-' || (gs % 500),
	'rule-name-' || (gs % 500),
	(gs % 20000)::DOUBLE PRECISION / 10.0,
	CASE WHEN gs % 7 = 0 THEN '{"prompt_tokens_details":{"cached_read_tokens":12}}' ELSE '{}' END,
	(gs % 1000)::DOUBLE PRECISION / 100000.0,
	CASE WHEN gs % 11 = 0 THEN 'error' ELSE 'success' END,
	(gs % 2 = 0),
	CASE WHEN gs % 13 = 0 THEN repeat('summary ', 8) ELSE NULL END,
	CASE WHEN gs % 17 = 0 THEN 'not-json' ELSE '{"tenant":"scale"}' END,
	(gs % 4000)::INTEGER,
	(gs % 2000)::INTEGER,
	(gs % 6000)::INTEGER,
	0,
	NOW() - ((gs % 129600) * INTERVAL '1 minute'),
	CASE WHEN gs % 8 = 4 THEN '[{"role":"user","content":[{"type":"text","text":"embedding text ' || gs || '"}]}]' ELSE NULL END
FROM generate_series(1, ?) AS gs`
	require.NoError(t, db.WithContext(ctx).Exec(insertLogsSQL, rows).Error)

	insertMCPSQL := `
INSERT INTO mcp_tool_logs (
	id, request_id, llm_request_id, timestamp, tool_name, server_label,
	virtual_key_id, virtual_key_name, arguments, result, latency, cost, status, metadata, created_at
)
SELECT
	'mcp-' || gs,
	CASE WHEN gs % 2 = 0 THEN '' ELSE NULL END,
	'log-' || gs,
	NOW() - ((gs % 129600) * INTERVAL '1 minute'),
	'tool-' || (gs % 250),
	'server-' || (gs % 50),
	'vk-' || (gs % 10000),
	'vk-name-' || (gs % 10000),
	'{"x":1}',
	'{"ok":true}',
	(gs % 5000)::DOUBLE PRECISION / 10.0,
	(gs % 500)::DOUBLE PRECISION / 100000.0,
	CASE WHEN gs % 19 = 0 THEN 'error' ELSE 'success' END,
	'{"tenant":"scale"}',
	NOW() - ((gs % 129600) * INTERVAL '1 minute')
FROM generate_series(1, ?) AS gs`
	require.NoError(t, db.WithContext(ctx).Exec(insertMCPSQL, rows).Error)
}

func createOldShapeScaleMatViews(t *testing.T, ctx context.Context, db *gorm.DB) {
	t.Helper()
	oldHourlySQL := `
CREATE MATERIALIZED VIEW mv_logs_hourly AS
SELECT
	date_trunc('hour', timestamp) AS hour,
	provider,
	model,
	status,
	object_type,
	selected_key_id,
	COALESCE(virtual_key_id, '') AS virtual_key_id,
	COALESCE(routing_rule_id, '') AS routing_rule_id,
	COUNT(*) AS count,
	SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) AS success_count,
	SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) AS error_count,
	COALESCE(AVG(latency), 0) AS avg_latency,
	COALESCE(percentile_cont(0.90) WITHIN GROUP (ORDER BY latency), 0) AS p90_latency,
	COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency), 0) AS p95_latency,
	COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY latency), 0) AS p99_latency,
	COALESCE(SUM(prompt_tokens), 0) AS total_prompt_tokens,
	COALESCE(SUM(completion_tokens), 0) AS total_completion_tokens,
	COALESCE(SUM(total_tokens), 0) AS total_tokens,
	COALESCE(SUM(cached_read_tokens), 0) AS total_cached_read_tokens,
	COALESCE(SUM(cost), 0) AS total_cost
FROM logs
WHERE status IN ('success', 'error')
GROUP BY 1, 2, 3, 4, 5, 6, 7, 8`
	require.NoError(t, db.WithContext(ctx).Exec(oldHourlySQL).Error)

	oldFilterDataSQL := `
CREATE MATERIALIZED VIEW mv_logs_filterdata AS
SELECT DISTINCT
	model,
	provider,
	selected_key_id,
	selected_key_name,
	COALESCE(virtual_key_id, '') AS virtual_key_id,
	COALESCE(virtual_key_name, '') AS virtual_key_name,
	COALESCE(routing_rule_id, '') AS routing_rule_id,
	COALESCE(routing_rule_name, '') AS routing_rule_name,
	COALESCE(routing_engines_used, '') AS routing_engines_used
FROM logs
WHERE timestamp >= NOW() - INTERVAL '60 days'
	AND model IS NOT NULL AND model != ''`
	require.NoError(t, db.WithContext(ctx).Exec(oldFilterDataSQL).Error)
}

func acquireScaleMigrationConn(t *testing.T, ctx context.Context, sqlDB *sql.DB) *sql.Conn {
	t.Helper()
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	return conn
}

func monitorScaleMigrationLocks(ctx context.Context, t *testing.T, db *sql.DB, mu *sync.Mutex, samples *[]lockWaitSample) {
	t.Helper()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rows, err := db.QueryContext(ctx, `
				SELECT
					a.pid,
					EXTRACT(EPOCH FROM (now() - COALESCE(a.query_start, now())))::float8,
					LEFT(a.query, 500),
					array_to_string(pg_blocking_pids(a.pid), ',')
				FROM pg_stat_activity a
				WHERE cardinality(pg_blocking_pids(a.pid)) > 0
				  AND a.datname = current_database()
				ORDER BY now() - COALESCE(a.query_start, now()) DESC
			`)
			if err != nil {
				t.Logf("lock monitor query failed: %v", err)
				continue
			}
			for rows.Next() {
				var sample lockWaitSample
				var waitingSeconds float64
				if err := rows.Scan(&sample.WaitingPID, &waitingSeconds, &sample.WaitingQuery, &sample.BlockingPIDs); err != nil {
					t.Logf("lock monitor scan failed: %v", err)
					continue
				}
				sample.ObservedAt = time.Now()
				sample.WaitingDuration = time.Duration(waitingSeconds * float64(time.Second))
				mu.Lock()
				*samples = append(*samples, sample)
				mu.Unlock()
			}
			if err := rows.Err(); err != nil {
				t.Logf("lock monitor rows failed: %v", err)
			}
			_ = rows.Close()
		}
	}
}
