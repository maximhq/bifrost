package logstore

import (
	"context"
	"fmt"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCasCompactHiddenBilling(t *testing.T) {
	c, _ := newTestCas(t)
	ctx := context.Background()
	defer c.Close(ctx)
	e := bigChatEntry("hidden-billing", strings.Repeat("private ", 100))
	e.ContentHidden = true
	e.TokenUsage = `{"prompt_tokens":100,"completion_tokens":2,"total_tokens":102}`
	e.CacheDebug = `{"cache_hit":true}`
	require.NoError(t, c.Create(ctx, e))
	full, err := c.FindByID(ctx, e.ID)
	require.NoError(t, err)
	require.Equal(t, e.TokenUsage, full.TokenUsage)
	require.Equal(t, e.CacheDebug, full.CacheDebug)
	require.Empty(t, full.InputHistory)
	before := boundarySnapshot(t, c.db)
	_, err = CompactCAS(ctx, c.db, true, true)
	require.NoError(t, err)
	require.Equal(t, before, boundarySnapshot(t, c.db))
}

func TestCasRefsExistingStartupRetry(t *testing.T) {
	c, _ := newTestCas(t)
	ctx := context.Background()
	defer c.Close(ctx)
	require.NoError(t, c.Create(ctx, bigChatEntry("retry", strings.Repeat("payload", 100))))
	// Simulate an old store's hex layout; the edge must reference real blobs
	// so the integer-id migration's fail-closed dangling check passes.
	require.NoError(t, c.db.Exec("DELETE FROM migrations WHERE id='cas_refs_without_rowid_v1'").Error)
	require.NoError(t, c.db.Exec("DELETE FROM migrations WHERE id='cas_integer_ref_ids_v1'").Error)
	require.NoError(t, c.db.Exec("DROP TABLE cas_refs").Error)
	require.NoError(t, c.db.Exec("CREATE TABLE cas_refs(owner_hash TEXT,target_hash TEXT,PRIMARY KEY(owner_hash,target_hash))").Error)
	require.NoError(t, c.db.Exec("INSERT INTO cas_refs(owner_hash, target_hash) SELECT blob_hash, blob_hash FROM cas_payloads LIMIT 1").Error)
	require.NoError(t, c.db.Exec("CREATE TABLE cas_refs_compact(block INTEGER)").Error)
	require.Error(t, triggerMigrations(ctx, c.db, hybridTestLogger{}))
	var count int64
	require.NoError(t, c.db.Raw("SELECT count(*) FROM migrations WHERE id='cas_refs_without_rowid_v1'").Scan(&count).Error)
	require.Zero(t, count)
	require.NoError(t, c.db.Exec("DROP TABLE cas_refs_compact").Error)
	require.NoError(t, triggerMigrations(ctx, c.db, hybridTestLogger{}))
	require.NoError(t, triggerMigrations(ctx, c.db, hybridTestLogger{}))
	require.NoError(t, c.db.Raw("SELECT count(*) FROM migrations WHERE id='cas_refs_without_rowid_v1'").Scan(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, c.db.Raw("SELECT count(*) FROM migrations WHERE id='cas_integer_ref_ids_v1'").Scan(&count).Error)
	require.EqualValues(t, 1, count)
	var ddl string
	require.NoError(t, c.db.Raw("SELECT sql FROM sqlite_master WHERE name='cas_refs'").Scan(&ddl).Error)
	require.Contains(t, ddl, "WITHOUT ROWID")
	require.Contains(t, ddl, "owner_id")
	// The pre-upgrade edge survives, resolved through integer ids.
	require.NoError(t, c.db.Raw("SELECT count(*) FROM cas_refs r JOIN cas_blobs b ON b.id = r.owner_id AND b.id = r.target_id").Scan(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestCasCompactRejectsMutatingTrigger(t *testing.T) {
	c, _ := newTestCas(t)
	ctx := context.Background()
	defer c.Close(ctx)
	require.NoError(t, c.Create(ctx, bigChatEntry("trigger", strings.Repeat("payload", 100))))
	require.NoError(t, c.db.Exec("CREATE TRIGGER change_status AFTER UPDATE ON logs BEGIN UPDATE logs SET status='lost' WHERE id=NEW.id; END").Error)
	before := boundarySnapshot(t, c.db)
	_, err := CompactCAS(ctx, c.db, true, true)
	require.ErrorContains(t, err, "triggers")
	require.Equal(t, before, boundarySnapshot(t, c.db))
}

func TestCasCompactSizeFixture(t *testing.T) {
	dir := os.Getenv("CAS_COMPACT_FIXTURE_DIR")
	if dir == "" {
		t.Skip("set CAS_COMPACT_FIXTURE_DIR to retain storage fixture")
	}
	require.NoError(t, os.MkdirAll(dir, 0700))
	ctx := context.Background()
	inner, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: filepath.Join(dir, "fixture.db")}, hybridTestLogger{})
	require.NoError(t, err)
	c, err := newCasLogStore(ctx, inner, &ContentAddressedConfig{Enabled: true, MinFieldBytes: 64, MinChunkBytes: 32}, hybridTestLogger{})
	require.NoError(t, err)
	defer c.Close(ctx)
	for i := 0; i < 600; i++ {
		msgs := make([]string, 0, 251)
		for j := 0; j < 250; j++ {
			msgs = append(msgs, fmt.Sprintf("shared context message %04d: %s", j, strings.Repeat("context ", 16)))
		}
		msgs = append(msgs, fmt.Sprintf("request %04d %s", i, strings.Repeat("long last user message ", 600)))
		e := bigChatEntry(fmt.Sprintf("fixture-%04d", i), msgs...)
		require.NoError(t, c.Create(ctx, e))
		// Reproduce base prepareDBEntry's redundant last-user-message bytes exactly.
		legacy := *e
		prepareDBEntry(&legacy, map[string]struct{}{"token_usage": {}, "cache_debug": {}})
		require.NoError(t, c.db.Model(&Log{}).Where("id = ?", e.ID).UpdateColumn("input_history", legacy.InputHistory).Error)
	}
	require.NoError(t, c.db.Exec("VACUUM").Error)
	require.NoError(t, c.db.Exec("VACUUM INTO ?", filepath.Join(dir, "baseline.db")).Error)
	baseline, err := CompactCAS(ctx, c.db, false, false)
	require.NoError(t, err)
	clean, err := CompactCAS(ctx, c.db, true, false)
	require.NoError(t, err)
	require.Equal(t, baseline.PayloadSHA256, clean.PayloadSHA256)
	require.NoError(t, c.db.Exec("VACUUM INTO ?", filepath.Join(dir, "inline-only.db")).Error)
	both, err := CompactCAS(ctx, c.db, false, true)
	require.NoError(t, err)
	require.Equal(t, baseline.PayloadSHA256, both.PayloadSHA256)
	require.NoError(t, c.db.Exec("VACUUM INTO ?", filepath.Join(dir, "both.db")).Error)
	t.Logf("baseline=%+v cleanup=%+v refs=%+v", baseline, clean, both)
}

func TestCasInlineResponsesHiddenUpdates(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		for _, api := range []string{"create", "ifabsent", "batch"} {
			t.Run(fmt.Sprintf("%v-%s", hidden, api), func(t *testing.T) {
				c, inner := newTestCas(t)
				ctx := context.Background()
				defer c.Close(ctx)
				raw := fmt.Sprintf(`[{"role":"user","content":"early"},{"role":"assistant","content":"reply"},{"role":"user","content":"%s"}]`, strings.Repeat("responses ", 10000))
				e := &Log{ID: "responses", ContentHidden: hidden, ResponsesInputHistory: raw}
				require.NoError(t, e.DeserializeFields())
				var err error
				switch api {
				case "create":
					err = c.Create(ctx, e)
				case "ifabsent":
					err = c.CreateIfNotExists(ctx, e)
				case "batch":
					err = c.BatchCreateIfNotExists(ctx, []*Log{e})
				}
				require.NoError(t, err)
				row, err := inner.FindByID(ctx, e.ID)
				require.NoError(t, err)
				require.Empty(t, row.ResponsesInputHistory)
				require.NoError(t, c.db.Transaction(func(tx *gorm.DB) error {
					values, err := verifiedCASValues(tx, e.ID)
					require.NoError(t, err)
					require.Equal(t, raw, values["responses_input_history"])
					return nil
				}))
				full, err := c.FindByID(ctx, e.ID)
				require.NoError(t, err)
				if hidden {
					require.Empty(t, full.ResponsesInputHistory)
					require.Empty(t, full.ContentSummary)
				} else {
					require.Equal(t, raw, full.ResponsesInputHistory)
				}
				for _, shape := range []string{"map", "pointer", "value"} {
					next := fmt.Sprintf(`[{"role":"user","content":"%s"}]`, strings.Repeat(shape, 3000))
					var update any
					switch shape {
					case "map":
						update = map[string]interface{}{"responses_input_history": next}
					case "pointer":
						update = &Log{ResponsesInputHistory: next}
					case "value":
						update = Log{ResponsesInputHistory: next}
					}
					require.NoError(t, c.Update(ctx, e.ID, update))
					var stored Log
					require.NoError(t, c.db.Where("id = ?", e.ID).Take(&stored).Error)
					require.Empty(t, stored.ResponsesInputHistory)
					values, err := verifiedCASValues(c.db, e.ID)
					require.NoError(t, err)
					require.Equal(t, next, values["responses_input_history"])
				}
			})
		}
	}
}

func TestCasInlineExcludedAndSmall(t *testing.T) {
	c, inner := newTestCas(t)
	ctx := context.Background()
	defer c.Close(ctx)
	c.excluded["input_history"] = struct{}{}
	e := bigChatEntry("excluded", strings.Repeat("keep ", 5000))
	require.NoError(t, c.Create(ctx, e))
	row, err := inner.FindByID(ctx, e.ID)
	require.NoError(t, err)
	require.Equal(t, e.InputHistory, row.InputHistory)
	delete(c.excluded, "input_history")
	c.minFieldBytes = 100000
	e = bigChatEntry("small", "hello")
	require.NoError(t, c.Create(ctx, e))
	row, err = inner.FindByID(ctx, e.ID)
	require.NoError(t, err)
	require.Equal(t, e.InputHistory, row.InputHistory)
	_ = schemas.ChatMessageRoleUser
}

func TestCasCompactVerifiedHistory(t *testing.T) {
	c, _ := newTestCas(t)
	ctx := context.Background()
	defer c.Close(ctx)
	for _, id := range []string{"a", "b"} {
		e := bigChatEntry(id, strings.Repeat("full content ", 1000))
		require.NoError(t, c.Create(ctx, e))
		require.NoError(t, c.db.Model(&Log{}).Where("id = ?", id).UpdateColumn("input_history", "redundant preview").Error)
	}
	before, err := CompactCAS(ctx, c.db, false, false)
	require.NoError(t, err)
	after, err := CompactCAS(ctx, c.db, true, true)
	require.NoError(t, err)
	require.Equal(t, before.PayloadSHA256, after.PayloadSHA256)
	require.EqualValues(t, 34, after.ClearedBytes)
	again, err := CompactCAS(ctx, c.db, false, false)
	require.NoError(t, err)
	require.Equal(t, before.PayloadSHA256, again.PayloadSHA256)
	require.Zero(t, again.ClearedBytes)
	require.NoError(t, c.DeleteLog(ctx, "a"))
	full, err := c.FindByID(ctx, "b")
	require.NoError(t, err)
	require.Contains(t, full.InputHistory, "full content")
	require.NoError(t, c.DeleteLog(ctx, "b"))
	require.Zero(t, casBlobCount(t, c))
}

func TestCasCompactFailureAtomic(t *testing.T) {
	for _, fault := range []string{"inventory", "blob", "refs", "legacy", "write"} {
		t.Run(fault, func(t *testing.T) {
			c, _ := newTestCas(t)
			ctx := context.Background()
			defer c.Close(ctx)
			for _, id := range []string{"a", "b"} {
				require.NoError(t, c.Create(ctx, bigChatEntry(id, strings.Repeat(id, 1000))))
				require.NoError(t, c.db.Model(&Log{}).Where("id = ?", id).UpdateColumn("input_history", "preview").Error)
			}
			switch fault {
			case "inventory":
				require.NoError(t, c.db.Where("log_id = ?", "b").Delete(&CASInventory{}).Error)
			case "legacy":
				require.NoError(t, c.db.Model(&CASInventory{}).Where("log_id = ?", "b").Update("provenance", "legacy_bootstrap").Error)
			case "blob":
				require.NoError(t, c.db.Exec("UPDATE cas_blobs SET data = x'00' WHERE hash IN (SELECT blob_hash FROM cas_payloads WHERE log_id='b')").Error)
			case "refs":
				require.NoError(t, c.db.Exec("DELETE FROM cas_refs WHERE owner_id IN (SELECT b.id FROM cas_payloads p JOIN cas_blobs b ON b.hash = p.blob_hash WHERE p.log_id='b')").Error)
			case "write":
				require.NoError(t, c.db.Exec("CREATE TRIGGER fail_cleanup BEFORE UPDATE OF input_history ON logs WHEN OLD.id='b' BEGIN SELECT RAISE(ABORT,'injected'); END").Error)
			}
			_, err := CompactCAS(ctx, c.db, true, true)
			require.Error(t, err)
			var row Log
			require.NoError(t, c.db.Where("id='a'").Take(&row).Error)
			require.Equal(t, "preview", row.InputHistory)
		})
	}
}

func TestCasRefsMigrationRollback(t *testing.T) {
	c, _ := newTestCas(t)
	defer c.Close(context.Background())
	require.NoError(t, c.db.Exec("DROP TABLE cas_refs").Error)
	require.NoError(t, c.db.Exec("CREATE TABLE cas_refs(owner_hash TEXT,target_hash TEXT,PRIMARY KEY(owner_hash,target_hash))").Error)
	require.NoError(t, c.db.Exec("CREATE INDEX custom_target ON cas_refs(target_hash)").Error)
	require.NoError(t, c.db.Exec("INSERT INTO cas_refs VALUES('one','two'),('one','three')").Error)
	err := c.db.Transaction(func(tx *gorm.DB) error { require.NoError(t, MigrateCASRefs(tx)); return context.Canceled })
	require.Error(t, err)
	var ddl string
	require.NoError(t, c.db.Raw("SELECT sql FROM sqlite_master WHERE name='cas_refs'").Scan(&ddl).Error)
	require.NotContains(t, ddl, "WITHOUT ROWID")
	require.NoError(t, MigrateCASRefs(c.db))
	require.NoError(t, MigrateCASRefs(c.db))
	require.NoError(t, c.db.Raw("SELECT sql FROM sqlite_master WHERE name='cas_refs'").Scan(&ddl).Error)
	require.Contains(t, ddl, "WITHOUT ROWID")
	type hexRef struct {
		OwnerHash  string `gorm:"column:owner_hash"`
		TargetHash string `gorm:"column:target_hash"`
	}
	var refs []hexRef
	require.NoError(t, c.db.Raw("SELECT owner_hash, target_hash FROM cas_refs ORDER BY target_hash").Scan(&refs).Error)
	require.Equal(t, []hexRef{{"one", "three"}, {"one", "two"}}, refs)
	require.NoError(t, c.db.Exec("INSERT OR IGNORE INTO cas_refs VALUES('one','two')").Error)
	require.True(t, c.db.Migrator().HasIndex("cas_refs", "custom_target"))
}

func TestCasInlineSelectedCreate(t *testing.T) {
	for _, api := range []string{"create", "ifabsent", "batch"} {
		t.Run(api, func(t *testing.T) {
			c, inner := newTestCas(t)
			ctx := context.Background()
			defer c.Close(ctx)
			e := bigChatEntry("inline", "early", "assistant", strings.Repeat("long user ", 10000))
			require.NoError(t, e.SerializeFields())
			original := e.InputHistory
			var err error
			switch api {
			case "create":
				err = c.Create(ctx, e)
			case "ifabsent":
				err = c.CreateIfNotExists(ctx, e)
			case "batch":
				err = c.BatchCreateIfNotExists(ctx, []*Log{e})
			}
			require.NoError(t, err)
			row, err := inner.FindByID(ctx, e.ID)
			require.NoError(t, err)
			require.Empty(t, row.InputHistory)
			require.LessOrEqual(t, len(row.ContentSummary), 2048)
			full, err := c.FindByID(ctx, e.ID)
			require.NoError(t, err)
			require.Equal(t, original, full.InputHistory)
		})
	}
}
