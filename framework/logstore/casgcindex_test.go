package logstore

import (
	"context"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

// Both reachability directions must be indexed. A WITHOUT ROWID composite
// primary key only covers owner_id; scanning refs once per blob makes GC
// quadratic while holding SQLite's sole writer reservation.
func TestCasGCIndexMigrationRejectsCollision(t *testing.T) {
	cas, _ := newTestCas(t)
	require.NoError(t, cas.db.Exec("DELETE FROM migrations WHERE id = ?", "cas_reverse_lookup_indexes_v1").Error)
	require.NoError(t, cas.db.Exec("DROP INDEX idx_cas_refs_target_id").Error)
	require.NoError(t, cas.db.Exec("CREATE INDEX idx_cas_refs_target_id ON cas_refs(owner_id)").Error)
	err := migrationCasReverseLookupIndexes(context.Background(), cas.db, hybridTestLogger{})
	require.ErrorContains(t, err, "unexpected columns or table")
	require.NoError(t, cas.db.Exec("DROP INDEX idx_cas_refs_target_id").Error)
	require.NoError(t, migrationCasReverseLookupIndexes(context.Background(), cas.db, hybridTestLogger{}))
	require.NoError(t, migrationCasReverseLookupIndexes(context.Background(), cas.db, hybridTestLogger{}))
}

func TestCasGCIndexMigrationPartialRollback(t *testing.T) {
	cas, _ := newTestCas(t)
	require.NoError(t, cas.db.Exec("DELETE FROM migrations WHERE id = ?", "cas_reverse_lookup_indexes_v1").Error)
	require.NoError(t, cas.db.Exec("DROP INDEX idx_cas_refs_target_id").Error)
	require.NoError(t, cas.db.Exec("DROP INDEX idx_cas_payloads_blob_hash").Error)
	require.NoError(t, cas.db.Exec("CREATE INDEX idx_cas_payloads_blob_hash ON cas_payloads(blob_hash) WHERE log_id = 'partial'").Error)
	require.ErrorContains(t, migrationCasReverseLookupIndexes(context.Background(), cas.db, hybridTestLogger{}), "nonpartial")
	var count int64
	require.NoError(t, cas.db.Raw("SELECT count(*) FROM migrations WHERE id=?", "cas_reverse_lookup_indexes_v1").Scan(&count).Error)
	require.Zero(t, count)
	require.False(t, cas.db.Migrator().HasIndex("cas_refs", "idx_cas_refs_target_id"), "first index creation must rollback when second fails")
	require.NoError(t, cas.db.Exec("DROP INDEX idx_cas_payloads_blob_hash").Error)
	require.NoError(t, migrationCasReverseLookupIndexes(context.Background(), cas.db, hybridTestLogger{}))
}

func TestCasGCReverseLookupPlans(t *testing.T) {
	cas, _ := newTestCas(t)
	for _, q := range []struct {
		sql   string
		param any
	}{
		{"SELECT 1 FROM cas_refs WHERE target_id = ?", int64(0)},
		{"SELECT 1 FROM cas_payloads WHERE blob_hash = ?", "missing"},
	} {
		var plan []struct{ Detail string }
		require.NoError(t, cas.db.Raw("EXPLAIN QUERY PLAN "+q.sql, q.param).Scan(&plan).Error)
		require.NotEmpty(t, plan)
		for _, row := range plan {
			require.NotContains(t, strings.ToUpper(row.Detail), "SCAN", q.sql+": "+row.Detail)
		}
	}
}
